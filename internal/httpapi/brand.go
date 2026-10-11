package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/brand"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/jobs"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm/router"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

// inTx runs fn in a transaction with a Queries bound to it.
func (s *server) inTx(ctx context.Context, fn func(tx pgx.Tx, q *store.Queries) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx, store.New(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// enqueueEmbed schedules re-embedding inside the caller's transaction.
func (s *server) enqueueEmbed(ctx context.Context, tx pgx.Tx, ws uuid.UUID, entityType string, id uuid.UUID) error {
	if _, err := s.jobs.InsertTx(ctx, tx, jobs.EmbedEntityArgs{WorkspaceID: ws, EntityType: entityType, EntityID: id}, nil); err != nil {
		return fmt.Errorf("enqueue embed: %w", err)
	}
	return nil
}

// ---- brand profile ----

type profileDTO struct {
	Profile   brand.Profile `json:"profile"`
	UpdatedAt *time.Time    `json:"updated_at"`
}

func validateProfile(p *brand.Profile) error {
	var err error
	if p.Services, err = cleanList("services", p.Services, 50, 300); err != nil {
		return err
	}
	if p.ValueProps, err = cleanList("value_props", p.ValueProps, 50, 500); err != nil {
		return err
	}
	if p.Differentiators, err = cleanList("differentiators", p.Differentiators, 50, 500); err != nil {
		return err
	}
	if err := optString("tone.voice", &p.Tone.Voice, 1000); err != nil {
		return err
	}
	if p.Tone.Do, err = cleanList("tone.do", p.Tone.Do, 50, 300); err != nil {
		return err
	}
	if p.Tone.Dont, err = cleanList("tone.dont", p.Tone.Dont, 100, 300); err != nil {
		return err
	}
	if p.Tone.Examples, err = cleanList("tone.examples", p.Tone.Examples, 30, 1000); err != nil {
		return err
	}
	t := &p.Targeting
	for _, f := range []struct {
		name string
		v    *[]string
	}{
		{"targeting.prioritize", &t.Prioritize}, {"targeting.avoid", &t.Avoid}, {"targeting.geography", &t.Geography},
		{"targeting.excluded_venues", &t.ExcludedVenues}, {"targeting.topics", &t.Topics}, {"targeting.media_targets", &t.MediaTargets},
	} {
		if *f.v, err = cleanList(f.name, *f.v, 50, 300); err != nil {
			return err
		}
	}
	return nil
}

func (s *server) handleGetProfile(c *gin.Context) {
	m := memberOf(c)
	row, err := store.New(s.pool).GetBrandProfile(c.Request.Context(), m.WorkspaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusOK, profileDTO{Profile: emptyProfile()})
		return
	}
	if err != nil {
		s.fail(c, err)
		return
	}
	p := emptyProfile()
	if err := json.Unmarshal(row.Profile, &p); err != nil {
		s.fail(c, fmt.Errorf("decode brand profile: %w", err))
		return
	}
	p = normProfile(p)
	c.JSON(http.StatusOK, profileDTO{Profile: p, UpdatedAt: &row.UpdatedAt})
}

// normProfile replaces nil lists (fields added after a profile was saved)
// with empty ones so clients always get arrays.
func normProfile(p brand.Profile) brand.Profile {
	e := emptyProfile()
	for _, pair := range []struct{ dst, def *[]string }{
		{&p.Services, &e.Services}, {&p.ValueProps, &e.ValueProps}, {&p.Differentiators, &e.Differentiators},
		{&p.Tone.Do, &e.Tone.Do}, {&p.Tone.Dont, &e.Tone.Dont}, {&p.Tone.Examples, &e.Tone.Examples},
		{&p.Targeting.Prioritize, &e.Targeting.Prioritize}, {&p.Targeting.Avoid, &e.Targeting.Avoid},
		{&p.Targeting.Geography, &e.Targeting.Geography}, {&p.Targeting.ExcludedVenues, &e.Targeting.ExcludedVenues},
		{&p.Targeting.Topics, &e.Targeting.Topics}, {&p.Targeting.MediaTargets, &e.Targeting.MediaTargets},
	} {
		if *pair.dst == nil {
			*pair.dst = *pair.def
		}
	}
	return p
}

func emptyProfile() brand.Profile {
	return brand.Profile{Services: []string{}, ValueProps: []string{}, Differentiators: []string{},
		Tone:      brand.Tone{Do: []string{}, Dont: []string{}, Examples: []string{}},
		Targeting: brand.Targeting{Prioritize: []string{}, Avoid: []string{}, Geography: []string{}, ExcludedVenues: []string{}, Topics: []string{}, MediaTargets: []string{}}}
}

func (s *server) handlePutProfile(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	var p brand.Profile
	if err := bindJSON(c, &p); err != nil {
		s.fail(c, err)
		return
	}
	if err := validateProfile(&p); err != nil {
		s.fail(c, err)
		return
	}
	b, err := json.Marshal(p)
	if err != nil {
		s.fail(c, err)
		return
	}
	var row store.BrandProfile
	err = s.inTx(c.Request.Context(), func(_ pgx.Tx, q *store.Queries) error {
		var err error
		row, err = q.UpsertBrandProfile(c.Request.Context(), store.UpsertBrandProfileParams{WorkspaceID: m.WorkspaceID, Profile: b})
		if err != nil {
			return err
		}
		s.audit(c.Request.Context(), q, m.WorkspaceID, &u.ID, "brand_profile.update", "brand_profile", m.WorkspaceID.String(), nil)
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, profileDTO{Profile: p, UpdatedAt: &row.UpdatedAt})
}

// ---- portfolio ----

type portfolioInput struct {
	Kind    string              `json:"kind"`
	Title   string              `json:"title"`
	URL     *string             `json:"url"`
	Summary string              `json:"summary"`
	Tags    brand.PortfolioTags `json:"tags"`
}

type portfolioDTO struct {
	ID        uuid.UUID           `json:"id"`
	Kind      string              `json:"kind"`
	Title     string              `json:"title"`
	URL       *string             `json:"url"`
	Summary   string              `json:"summary"`
	Tags      brand.PortfolioTags `json:"tags"`
	CreatedAt time.Time           `json:"created_at"`
	Embedded  bool                `json:"embedded"`
}

func (in *portfolioInput) validate() error {
	if err := oneOf("kind", in.Kind, brand.PortfolioKinds...); err != nil {
		return err
	}
	if err := reqString("title", &in.Title, 300); err != nil {
		return err
	}
	if err := reqString("summary", &in.Summary, 5000); err != nil {
		return err
	}
	u, err := optURL("url", in.URL)
	if err != nil {
		return err
	}
	in.URL = u
	if in.Tags.Industries, err = cleanList("tags.industries", in.Tags.Industries, 30, 100); err != nil {
		return err
	}
	if in.Tags.Services, err = cleanList("tags.services", in.Tags.Services, 30, 100); err != nil {
		return err
	}
	in.Tags.Problems, err = cleanList("tags.problems", in.Tags.Problems, 30, 200)
	return err
}

func toPortfolioDTO(p store.PortfolioItem, embedded bool) (portfolioDTO, error) {
	var tags brand.PortfolioTags
	if err := json.Unmarshal(p.Tags, &tags); err != nil {
		return portfolioDTO{}, fmt.Errorf("decode tags: %w", err)
	}
	tags = normTags(tags)
	return portfolioDTO{p.ID, p.Kind, p.Title, p.Url, p.Summary, tags, p.CreatedAt, embedded}, nil
}

func normTags(t brand.PortfolioTags) brand.PortfolioTags {
	if t.Industries == nil {
		t.Industries = []string{}
	}
	if t.Services == nil {
		t.Services = []string{}
	}
	if t.Problems == nil {
		t.Problems = []string{}
	}
	return t
}

func (s *server) handleListPortfolio(c *gin.Context) {
	m := memberOf(c)
	q := store.New(s.pool)
	es, _, err := router.EmbeddingSettingsFor(c.Request.Context(), q, m.WorkspaceID)
	if err != nil {
		s.fail(c, err)
		return
	}
	rows, err := q.ListPortfolioItems(c.Request.Context(), store.ListPortfolioItemsParams{WorkspaceID: m.WorkspaceID, EmbedModel: es.Model})
	if err != nil {
		s.fail(c, err)
		return
	}
	out := make([]portfolioDTO, 0, len(rows))
	for _, r := range rows {
		d, err := toPortfolioDTO(store.PortfolioItem{ID: r.ID, WorkspaceID: r.WorkspaceID, Kind: r.Kind, Title: r.Title, Url: r.Url, Summary: r.Summary, Tags: r.Tags, CreatedAt: r.CreatedAt}, r.Embedded)
		if err != nil {
			s.fail(c, err)
			return
		}
		out = append(out, d)
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

func (s *server) handleGetPortfolio(c *gin.Context) {
	m := memberOf(c)
	id, err := pathUUID(c, "id")
	if err != nil {
		s.fail(c, err)
		return
	}
	p, err := store.New(s.pool).GetPortfolioItem(c.Request.Context(), store.GetPortfolioItemParams{WorkspaceID: m.WorkspaceID, ID: id})
	if err != nil {
		s.fail(c, err)
		return
	}
	d, err := toPortfolioDTO(p, false)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, d)
}

func (s *server) handleSavePortfolio(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	var id uuid.UUID
	creating := c.Request.Method == http.MethodPost
	if !creating {
		var err error
		if id, err = pathUUID(c, "id"); err != nil {
			s.fail(c, err)
			return
		}
	}
	var in portfolioInput
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	if err := in.validate(); err != nil {
		s.fail(c, err)
		return
	}
	tags, _ := json.Marshal(in.Tags)
	ctx := c.Request.Context()
	var saved store.PortfolioItem
	err := s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		var err error
		if creating {
			saved, err = q.CreatePortfolioItem(ctx, store.CreatePortfolioItemParams{
				WorkspaceID: m.WorkspaceID, Kind: in.Kind, Title: in.Title, Url: in.URL, Summary: in.Summary, Tags: tags})
		} else {
			saved, err = q.UpdatePortfolioItem(ctx, store.UpdatePortfolioItemParams{
				WorkspaceID: m.WorkspaceID, ID: id, Kind: in.Kind, Title: in.Title, Url: in.URL, Summary: in.Summary, Tags: tags})
		}
		if err != nil {
			return err
		}
		action := "portfolio.update"
		if creating {
			action = "portfolio.create"
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, action, "portfolio_item", saved.ID.String(), nil)
		return s.enqueueEmbed(ctx, tx, m.WorkspaceID, jobs.EntityPortfolioItem, saved.ID)
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	d, err := toPortfolioDTO(saved, false)
	if err != nil {
		s.fail(c, err)
		return
	}
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
	}
	c.JSON(status, d)
}

func (s *server) handleDeletePortfolio(c *gin.Context) {
	s.deleteEmbedded(c, "portfolio_item", jobs.EntityPortfolioItem,
		func(ctx context.Context, q *store.Queries, ws, id uuid.UUID) (int64, error) {
			return q.DeletePortfolioItem(ctx, store.DeletePortfolioItemParams{WorkspaceID: ws, ID: id})
		})
}

// deleteEmbedded deletes an entity and its embeddings in one transaction.
func (s *server) deleteEmbedded(c *gin.Context, target, entityType string,
	del func(context.Context, *store.Queries, uuid.UUID, uuid.UUID) (int64, error)) {
	m, u := memberOf(c), userOf(c)
	id, err := pathUUID(c, "id")
	if err != nil {
		s.fail(c, err)
		return
	}
	ctx := c.Request.Context()
	err = s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		n, err := del(ctx, q, m.WorkspaceID, id)
		if err != nil {
			return err
		}
		if n == 0 {
			return errNotFound
		}
		if err := q.DeleteEmbeddingsForEntity(ctx, store.DeleteEmbeddingsForEntityParams{
			WorkspaceID: m.WorkspaceID, EntityType: entityType, EntityID: id}); err != nil {
			return err
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, target+".delete", target, id.String(), nil)
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ---- ICPs ----

type icpInput struct {
	Name       string              `json:"name"`
	Definition brand.ICPDefinition `json:"definition"`
}

type icpDTO struct {
	ID         uuid.UUID           `json:"id"`
	Name       string              `json:"name"`
	Definition brand.ICPDefinition `json:"definition"`
	Embedded   bool                `json:"embedded"`
}

func (in *icpInput) validate() error {
	if err := reqString("name", &in.Name, 200); err != nil {
		return err
	}
	d := &in.Definition
	var err error
	for _, f := range []struct {
		name string
		v    *[]string
	}{
		{"definition.industries", &d.Industries}, {"definition.company_sizes", &d.CompanySizes},
		{"definition.roles", &d.Roles}, {"definition.pain_points", &d.PainPoints},
		{"definition.buying_signals", &d.BuyingSignals},
	} {
		if *f.v, err = cleanList(f.name, *f.v, 50, 300); err != nil {
			return err
		}
	}
	return nil
}

func toICPDTO(id uuid.UUID, name string, def []byte, embedded bool) (icpDTO, error) {
	var d brand.ICPDefinition
	if err := json.Unmarshal(def, &d); err != nil {
		return icpDTO{}, fmt.Errorf("decode icp: %w", err)
	}
	for _, l := range []*[]string{&d.Industries, &d.CompanySizes, &d.Roles, &d.PainPoints, &d.BuyingSignals} {
		if *l == nil {
			*l = []string{}
		}
	}
	return icpDTO{id, name, d, embedded}, nil
}

func (s *server) handleListICPs(c *gin.Context) {
	m := memberOf(c)
	q := store.New(s.pool)
	es, _, err := router.EmbeddingSettingsFor(c.Request.Context(), q, m.WorkspaceID)
	if err != nil {
		s.fail(c, err)
		return
	}
	rows, err := q.ListICPs(c.Request.Context(), store.ListICPsParams{WorkspaceID: m.WorkspaceID, EmbedModel: es.Model})
	if err != nil {
		s.fail(c, err)
		return
	}
	out := make([]icpDTO, 0, len(rows))
	for _, r := range rows {
		d, err := toICPDTO(r.ID, r.Name, r.Definition, r.Embedded)
		if err != nil {
			s.fail(c, err)
			return
		}
		out = append(out, d)
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

func (s *server) handleSaveICP(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	creating := c.Request.Method == http.MethodPost
	var id uuid.UUID
	if !creating {
		var err error
		if id, err = pathUUID(c, "id"); err != nil {
			s.fail(c, err)
			return
		}
	}
	var in icpInput
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	if err := in.validate(); err != nil {
		s.fail(c, err)
		return
	}
	def, _ := json.Marshal(in.Definition)
	ctx := c.Request.Context()
	var saved store.Icp
	err := s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		var err error
		if creating {
			saved, err = q.CreateICP(ctx, store.CreateICPParams{WorkspaceID: m.WorkspaceID, Name: in.Name, Definition: def})
		} else {
			saved, err = q.UpdateICP(ctx, store.UpdateICPParams{WorkspaceID: m.WorkspaceID, ID: id, Name: in.Name, Definition: def})
		}
		if err != nil {
			return err
		}
		action := "icp.update"
		if creating {
			action = "icp.create"
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, action, "icp", saved.ID.String(), nil)
		return s.enqueueEmbed(ctx, tx, m.WorkspaceID, jobs.EntityICP, saved.ID)
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	d, err := toICPDTO(saved.ID, saved.Name, saved.Definition, false)
	if err != nil {
		s.fail(c, err)
		return
	}
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
	}
	c.JSON(status, d)
}

func (s *server) handleDeleteICP(c *gin.Context) {
	s.deleteEmbedded(c, "icp", jobs.EntityICP,
		func(ctx context.Context, q *store.Queries, ws, id uuid.UUID) (int64, error) {
			return q.DeleteICP(ctx, store.DeleteICPParams{WorkspaceID: ws, ID: id})
		})
}

// ---- keywords ----

type keywordInput struct {
	Term string  `json:"term"`
	Kind string  `json:"kind"`
	Lane *string `json:"lane"`
}

type keywordDTO struct {
	ID   uuid.UUID `json:"id"`
	Term string    `json:"term"`
	Kind string    `json:"kind"`
	Lane *string   `json:"lane"`
}

var lanes = []string{"inbound", "media", "thought_leadership"}

// validate returns the lane to store; nil means all lanes.
func (in *keywordInput) validate() (*store.Lane, error) {
	if err := reqString("term", &in.Term, 200); err != nil {
		return nil, err
	}
	if err := oneOf("kind", in.Kind, "track", "negative"); err != nil {
		return nil, err
	}
	if in.Lane == nil || *in.Lane == "" {
		return nil, nil
	}
	if err := oneOf("lane", *in.Lane, lanes...); err != nil {
		return nil, err
	}
	l := store.Lane(*in.Lane)
	return &l, nil
}

func toKeywordDTO(k store.Keyword) keywordDTO {
	var lane *string
	if k.Lane != nil {
		l := string(*k.Lane)
		lane = &l
	}
	return keywordDTO{k.ID, k.Term, k.Kind, lane}
}

func (s *server) handleListKeywords(c *gin.Context) {
	rows, err := store.New(s.pool).ListKeywords(c.Request.Context(), memberOf(c).WorkspaceID)
	if err != nil {
		s.fail(c, err)
		return
	}
	out := make([]keywordDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, toKeywordDTO(r))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

func (s *server) handleSaveKeyword(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	creating := c.Request.Method == http.MethodPost
	var id uuid.UUID
	if !creating {
		var err error
		if id, err = pathUUID(c, "id"); err != nil {
			s.fail(c, err)
			return
		}
	}
	var in keywordInput
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	lane, err := in.validate()
	if err != nil {
		s.fail(c, err)
		return
	}
	ctx := c.Request.Context()
	var saved store.Keyword
	err = s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		var err error
		if creating {
			saved, err = q.CreateKeyword(ctx, store.CreateKeywordParams{WorkspaceID: m.WorkspaceID, Term: in.Term, Kind: in.Kind, Lane: lane})
		} else {
			saved, err = q.UpdateKeyword(ctx, store.UpdateKeywordParams{WorkspaceID: m.WorkspaceID, ID: id, Term: in.Term, Kind: in.Kind, Lane: lane})
		}
		if err != nil {
			return err
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "keyword.save", "keyword", saved.ID.String(), map[string]any{"term": saved.Term, "kind": saved.Kind})
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
	}
	c.JSON(status, toKeywordDTO(saved))
}

func (s *server) handleDeleteKeyword(c *gin.Context) {
	s.deleteSimple(c, "keyword", func(ctx context.Context, q *store.Queries, ws, id uuid.UUID) (int64, error) {
		return q.DeleteKeyword(ctx, store.DeleteKeywordParams{WorkspaceID: ws, ID: id})
	})
}

func (s *server) deleteSimple(c *gin.Context, target string,
	del func(context.Context, *store.Queries, uuid.UUID, uuid.UUID) (int64, error)) {
	m, u := memberOf(c), userOf(c)
	id, err := pathUUID(c, "id")
	if err != nil {
		s.fail(c, err)
		return
	}
	ctx := c.Request.Context()
	err = s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		n, err := del(ctx, q, m.WorkspaceID, id)
		if err != nil {
			return err
		}
		if n == 0 {
			return errNotFound
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, target+".delete", target, id.String(), nil)
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ---- competitors ----

type competitorInput struct {
	Name    string   `json:"name"`
	Domains []string `json:"domains"`
	Notes   *string  `json:"notes"`
}

type competitorDTO struct {
	ID      uuid.UUID `json:"id"`
	Name    string    `json:"name"`
	Domains []string  `json:"domains"`
	Notes   *string   `json:"notes"`
}

func (in *competitorInput) validate() error {
	if err := reqString("name", &in.Name, 200); err != nil {
		return err
	}
	var err error
	if in.Domains, err = cleanList("domains", in.Domains, 20, 253); err != nil {
		return err
	}
	return optString("notes", in.Notes, 5000)
}

func toCompetitorDTO(r store.Competitor) competitorDTO {
	d := r.Domains
	if d == nil {
		d = []string{}
	}
	return competitorDTO{r.ID, r.Name, d, r.Notes}
}

func (s *server) handleListCompetitors(c *gin.Context) {
	rows, err := store.New(s.pool).ListCompetitors(c.Request.Context(), memberOf(c).WorkspaceID)
	if err != nil {
		s.fail(c, err)
		return
	}
	out := make([]competitorDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, toCompetitorDTO(r))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

func (s *server) handleSaveCompetitor(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	creating := c.Request.Method == http.MethodPost
	var id uuid.UUID
	if !creating {
		var err error
		if id, err = pathUUID(c, "id"); err != nil {
			s.fail(c, err)
			return
		}
	}
	var in competitorInput
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	if err := in.validate(); err != nil {
		s.fail(c, err)
		return
	}
	ctx := c.Request.Context()
	var saved store.Competitor
	err := s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		var err error
		if creating {
			saved, err = q.CreateCompetitor(ctx, store.CreateCompetitorParams{WorkspaceID: m.WorkspaceID, Name: in.Name, Domains: in.Domains, Notes: in.Notes})
		} else {
			saved, err = q.UpdateCompetitor(ctx, store.UpdateCompetitorParams{WorkspaceID: m.WorkspaceID, ID: id, Name: in.Name, Domains: in.Domains, Notes: in.Notes})
		}
		if err != nil {
			return err
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "competitor.save", "competitor", saved.ID.String(), nil)
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
	}
	c.JSON(status, toCompetitorDTO(saved))
}

func (s *server) handleDeleteCompetitor(c *gin.Context) {
	s.deleteSimple(c, "competitor", func(ctx context.Context, q *store.Queries, ws, id uuid.UUID) (int64, error) {
		return q.DeleteCompetitor(ctx, store.DeleteCompetitorParams{WorkspaceID: ws, ID: id})
	})
}
