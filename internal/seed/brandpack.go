package seed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/brand"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/jobs"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

// Brand pack import modes.
const (
	// ImportMerge fills empty profile fields and adds keywords, competitors,
	// ICPs, and portfolio items the workspace doesn't have yet (matched by
	// term, name, or title). Nothing existing is changed.
	ImportMerge = "merge"
	// ImportReplace replaces the profile and every keyword, competitor, ICP,
	// and portfolio item with the pack's.
	ImportReplace = "replace"
)

// ImportSummary counts what an import changed (or would change).
type ImportSummary struct {
	Mode          string `json:"mode"`
	ProfileFields int    `json:"profile_fields"` // profile fields set
	Keywords      int    `json:"keywords"`
	Competitors   int    `json:"competitors"`
	ICPs          int    `json:"icps"`
	Portfolio     int    `json:"portfolio"`
}

// ImportPack applies a brand pack to a workspace inside tx (the caller
// commits, or rolls back for a dry run). ICPs and portfolio items are queued
// for embedding in the same transaction.
func ImportPack(ctx context.Context, tx pgx.Tx, ins jobInserter, ws uuid.UUID, pack *brand.Pack, mode string) (ImportSummary, error) {
	if mode != ImportMerge && mode != ImportReplace {
		return ImportSummary{}, fmt.Errorf("unknown import mode %q", mode)
	}
	q := store.New(tx)
	sum := ImportSummary{Mode: mode}

	// Profile.
	var cur brand.Profile
	if row, err := q.GetBrandProfile(ctx, ws); err == nil {
		_ = json.Unmarshal(row.Profile, &cur)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return sum, err
	}
	next := pack.Profile
	if mode == ImportMerge {
		next, sum.ProfileFields = mergeProfile(cur, pack.Profile)
	} else {
		sum.ProfileFields = countProfileFields(pack.Profile)
	}
	pb, err := json.Marshal(next)
	if err != nil {
		return sum, err
	}
	if _, err := q.UpsertBrandProfile(ctx, store.UpsertBrandProfileParams{WorkspaceID: ws, Profile: pb}); err != nil {
		return sum, fmt.Errorf("brand profile: %w", err)
	}

	if mode == ImportReplace {
		for _, del := range []func(context.Context, uuid.UUID) error{q.DeleteAllKeywords, q.DeleteAllCompetitors, q.DeleteAllICPs, q.DeleteAllPortfolioItems, q.DeleteOrphanBrandEmbeddings} {
			if err := del(ctx, ws); err != nil {
				return sum, err
			}
		}
	}

	// Keywords: added when the term (any case) isn't there for that kind.
	have, err := q.ListKeywords(ctx, ws)
	if err != nil {
		return sum, err
	}
	for kind, terms := range map[string][]string{"track": pack.Keywords.Track, "negative": pack.Keywords.Negative} {
		for _, t := range terms {
			t = strings.TrimSpace(t)
			if slices.ContainsFunc(have, func(k store.Keyword) bool { return k.Kind == kind && strings.EqualFold(k.Term, t) }) {
				continue
			}
			if err := q.InsertKeywordIfMissing(ctx, store.InsertKeywordIfMissingParams{WorkspaceID: ws, Term: t, Kind: kind}); err != nil {
				return sum, fmt.Errorf("keyword %q: %w", t, err)
			}
			have = append(have, store.Keyword{Term: t, Kind: kind})
			sum.Keywords++
		}
	}

	for _, c := range pack.Competitors {
		if _, err := q.GetCompetitorByName(ctx, store.GetCompetitorByNameParams{WorkspaceID: ws, Name: c.Name}); err == nil {
			continue
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return sum, err
		}
		notes := c.Notes
		if _, err := q.CreateCompetitor(ctx, store.CreateCompetitorParams{WorkspaceID: ws, Name: c.Name, Domains: c.Domains, Notes: &notes}); err != nil {
			return sum, fmt.Errorf("competitor %s: %w", c.Name, err)
		}
		sum.Competitors++
	}

	icps, err := q.ListICPs(ctx, store.ListICPsParams{WorkspaceID: ws})
	if err != nil {
		return sum, err
	}
	for _, i := range pack.ICPs {
		if slices.ContainsFunc(icps, func(r store.ListICPsRow) bool { return strings.EqualFold(r.Name, i.Name) }) {
			continue
		}
		d, _ := json.Marshal(i.ICPDefinition)
		row, err := q.CreateICP(ctx, store.CreateICPParams{WorkspaceID: ws, Name: i.Name, Definition: d})
		if err != nil {
			return sum, fmt.Errorf("icp %s: %w", i.Name, err)
		}
		if _, err := ins.InsertTx(ctx, tx, jobs.EmbedEntityArgs{WorkspaceID: ws, EntityType: jobs.EntityICP, EntityID: row.ID}, nil); err != nil {
			return sum, err
		}
		icps = append(icps, store.ListICPsRow{Name: i.Name})
		sum.ICPs++
	}

	items, err := q.ListPortfolioItems(ctx, store.ListPortfolioItemsParams{WorkspaceID: ws})
	if err != nil {
		return sum, err
	}
	for _, p := range pack.Portfolio {
		if slices.ContainsFunc(items, func(r store.ListPortfolioItemsRow) bool { return strings.EqualFold(r.Title, p.Title) }) {
			continue
		}
		tags, _ := json.Marshal(p.Tags)
		var url *string
		if p.URL != "" {
			u := p.URL
			url = &u
		}
		row, err := q.CreatePortfolioItem(ctx, store.CreatePortfolioItemParams{WorkspaceID: ws, Kind: p.Kind, Title: p.Title, Url: url, Summary: p.Summary, Tags: tags})
		if err != nil {
			return sum, fmt.Errorf("portfolio %q: %w", p.Title, err)
		}
		if _, err := ins.InsertTx(ctx, tx, jobs.EmbedEntityArgs{WorkspaceID: ws, EntityType: jobs.EntityPortfolioItem, EntityID: row.ID}, nil); err != nil {
			return sum, err
		}
		items = append(items, store.ListPortfolioItemsRow{Title: p.Title})
		sum.Portfolio++
	}
	return sum, nil
}

// ExportPack reads a workspace's brand knowledge base as a pack named name.
func ExportPack(ctx context.Context, q *store.Queries, ws uuid.UUID, name string) (*brand.Pack, error) {
	p := &brand.Pack{Name: name}
	if row, err := q.GetBrandProfile(ctx, ws); err == nil {
		_ = json.Unmarshal(row.Profile, &p.Profile)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	kws, err := q.ListKeywords(ctx, ws)
	if err != nil {
		return nil, err
	}
	for _, k := range kws {
		if k.Kind == "negative" {
			p.Keywords.Negative = append(p.Keywords.Negative, k.Term)
		} else {
			p.Keywords.Track = append(p.Keywords.Track, k.Term)
		}
	}
	comps, err := q.ListCompetitors(ctx, ws)
	if err != nil {
		return nil, err
	}
	for _, c := range comps {
		notes := ""
		if c.Notes != nil {
			notes = *c.Notes
		}
		p.Competitors = append(p.Competitors, brand.PackCompetitor{Name: c.Name, Domains: c.Domains, Notes: notes})
	}
	icps, err := q.ListICPs(ctx, store.ListICPsParams{WorkspaceID: ws})
	if err != nil {
		return nil, err
	}
	for _, i := range icps {
		var d brand.ICPDefinition
		_ = json.Unmarshal(i.Definition, &d)
		p.ICPs = append(p.ICPs, brand.PackICP{Name: i.Name, ICPDefinition: d})
	}
	items, err := q.ListPortfolioItems(ctx, store.ListPortfolioItemsParams{WorkspaceID: ws})
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		var tags brand.PortfolioTags
		_ = json.Unmarshal(it.Tags, &tags)
		url := ""
		if it.Url != nil {
			url = *it.Url
		}
		p.Portfolio = append(p.Portfolio, brand.PackPortfolioItem{Kind: it.Kind, Title: it.Title, URL: url, Summary: it.Summary, Tags: tags})
	}
	return p, nil
}

// mergeProfile fills cur's empty fields from in and counts them.
func mergeProfile(cur, in brand.Profile) (brand.Profile, int) {
	n := 0
	list := func(dst *[]string, src []string) {
		if len(*dst) == 0 && len(src) > 0 {
			*dst = src
			n++
		}
	}
	str := func(dst *string, src string) {
		if strings.TrimSpace(*dst) == "" && strings.TrimSpace(src) != "" {
			*dst = src
			n++
		}
	}
	list(&cur.Services, in.Services)
	list(&cur.ValueProps, in.ValueProps)
	list(&cur.Differentiators, in.Differentiators)
	str(&cur.Tone.Voice, in.Tone.Voice)
	list(&cur.Tone.Do, in.Tone.Do)
	list(&cur.Tone.Dont, in.Tone.Dont)
	list(&cur.Tone.Examples, in.Tone.Examples)
	list(&cur.Targeting.Prioritize, in.Targeting.Prioritize)
	list(&cur.Targeting.Avoid, in.Targeting.Avoid)
	list(&cur.Targeting.Geography, in.Targeting.Geography)
	list(&cur.Targeting.ExcludedVenues, in.Targeting.ExcludedVenues)
	list(&cur.Targeting.Topics, in.Targeting.Topics)
	list(&cur.Targeting.MediaTargets, in.Targeting.MediaTargets)
	return cur, n
}

func countProfileFields(p brand.Profile) int {
	_, n := mergeProfile(brand.Profile{}, p)
	return n
}
