package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/jobs"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/scoring"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

// Dismissal reason codes.
var dismissReasons = []string{"off_topic", "low_intent", "wrong_fit", "too_old", "duplicate", "bad_source", "other"}

var oppStatuses = []string{"new", "saved", "acted", "dismissed", "snoozed", "expired"}

// Events clients may log.
var clientEvents = []string{"viewed", "opened_link", "copied_draft", "outcome"}

var outcomes = []string{"no_response", "reply", "dm", "meeting", "lead", "client"}

func numeric(f float64) pgtype.Numeric {
	var n pgtype.Numeric
	_ = n.Scan(strconv.FormatFloat(f, 'f', 3, 64))
	return n
}

func numF(n pgtype.Numeric) float64 {
	f, _ := n.Float64Value()
	return f.Float64
}

// today is the inbox date in the server's inbox timezone.
func (s *server) today() pgtype.Date {
	d, _ := jobs.InboxDay(time.Now(), s.loc)
	return d
}

type opportunityDTO struct {
	ID             uuid.UUID       `json:"id"`
	Lane           string          `json:"lane"`
	OppType        string          `json:"opp_type"`
	Score          float64         `json:"score"`
	ScoreBreakdown json.RawMessage `json:"score_breakdown"`
	Explanation    string          `json:"explanation"`
	Deadline       *time.Time      `json:"deadline"`
	Status         string          `json:"status"`
	SnoozedUntil   *time.Time      `json:"snoozed_until"`
	Featured       bool            `json:"featured"`
	CreatedAt      time.Time       `json:"created_at"`
	RawItemID      uuid.UUID       `json:"raw_item_id"`
	URL            string          `json:"url"`
	Title          *string         `json:"title"`
	Snippet        string          `json:"snippet"`
	Community      *string         `json:"community"`
	SourceName     string          `json:"source_name"`
	PublishedAt    *time.Time      `json:"published_at"`
	FetchedAt      time.Time       `json:"fetched_at"`
}

func (s *server) handleListOpportunities(c *gin.Context) {
	m := memberOf(c)
	today := s.today()
	p := store.ListOpportunitiesParams{WorkspaceID: m.WorkspaceID, Today: today, Lim: 50, MinScore: numeric(0)}
	if v := c.Query("lane"); v != "" {
		if err := oneOf("lane", v, lanes...); err != nil {
			s.fail(c, err)
			return
		}
		l := store.Lane(v)
		p.Lane = &l
	}
	status := c.DefaultQuery("status", "new")
	if status != "all" {
		if err := oneOf("status", status, oppStatuses...); err != nil {
			s.fail(c, err)
			return
		}
		st := store.OppStatus(status)
		p.Status = &st
	}
	if v := c.Query("type"); v != "" {
		p.OppType = &v
	}
	if v := c.Query("min_score"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 || f > 1 {
			s.fail(c, badRequest("min_score must be between 0 and 1"))
			return
		}
		p.MinScore = numeric(f)
	}
	p.FeaturedOnly = c.Query("featured") == "true"
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil && n > 0 && n <= 200 {
			p.Lim = int32(n)
		}
	}
	// Cursor: "<score>_<id>" of the last item seen (only for non-featured pages).
	if v := c.Query("cursor"); v != "" {
		sc, id, ok := strings.Cut(v, "_")
		f, ferr := strconv.ParseFloat(sc, 64)
		uid, uerr := uuid.Parse(id)
		if !ok || ferr != nil || uerr != nil {
			s.fail(c, badRequest("invalid cursor"))
			return
		}
		p.CursorScore, p.CursorID = numeric(f), &uid
	}
	q := store.New(s.pool)
	rows, err := q.ListOpportunities(c.Request.Context(), p)
	if err != nil {
		s.fail(c, err)
		return
	}
	out := make([]opportunityDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, opportunityDTO{
			ID: r.ID, Lane: string(r.Lane), OppType: r.OppType, Score: numF(r.Score), ScoreBreakdown: orEmptyJSON(r.ScoreBreakdown),
			Explanation: r.Explanation, Deadline: r.Deadline, Status: string(r.Status), SnoozedUntil: r.SnoozedUntil,
			Featured: r.FeaturedOn.Valid && r.FeaturedOn.Time.Equal(today.Time), CreatedAt: r.CreatedAt, RawItemID: r.RawItemID,
			URL: r.Url, Title: r.Title, Snippet: r.Snippet, Community: r.Community, SourceName: r.SourceName, PublishedAt: r.PublishedAt, FetchedAt: r.FetchedAt,
		})
	}
	var next *string
	if len(rows) == int(p.Lim) {
		last := rows[len(rows)-1]
		cur := fmt.Sprintf("%.3f_%s", numF(last.Score), last.ID)
		next = &cur
	}
	counts, err := q.CountOpportunitiesByLane(c.Request.Context(), store.CountOpportunitiesByLaneParams{WorkspaceID: m.WorkspaceID, Today: today})
	if err != nil {
		s.fail(c, err)
		return
	}
	laneCounts := map[string]gin.H{}
	for _, l := range lanes {
		laneCounts[l] = gin.H{"new": 0, "featured": 0}
	}
	for _, cnt := range counts {
		laneCounts[string(cnt.Lane)] = gin.H{"new": cnt.NewCount, "featured": cnt.FeaturedCount}
	}
	c.JSON(http.StatusOK, gin.H{"items": out, "next_cursor": next, "counts": laneCounts})
}

func (s *server) handleGetOpportunity(c *gin.Context) {
	m := memberOf(c)
	id, err := pathUUID(c, "id")
	if err != nil {
		s.fail(c, err)
		return
	}
	q := store.New(s.pool)
	o, err := q.GetOpportunity(c.Request.Context(), store.GetOpportunityParams{WorkspaceID: m.WorkspaceID, ID: id})
	if err != nil {
		s.fail(c, err)
		return
	}
	events, err := q.ListOpportunityEvents(c.Request.Context(), store.ListOpportunityEventsParams{WorkspaceID: m.WorkspaceID, ID: id})
	if err != nil {
		s.fail(c, err)
		return
	}
	type eventDTO struct {
		Event     string    `json:"event"`
		Reason    *string   `json:"reason"`
		Outcome   *string   `json:"outcome"`
		CreatedAt time.Time `json:"created_at"`
	}
	evs := make([]eventDTO, 0, len(events))
	for _, e := range events {
		evs = append(evs, eventDTO{e.Event, e.Reason, e.Outcome, e.CreatedAt})
	}
	drafts, err := s.loadDrafts(c.Request.Context(), q, m.WorkspaceID, id)
	if err != nil {
		s.fail(c, err)
		return
	}
	today := s.today()
	c.JSON(http.StatusOK, gin.H{
		"id": o.ID, "lane": o.Lane, "opp_type": o.OppType, "score": numF(o.Score), "score_breakdown": orEmptyJSON(o.ScoreBreakdown),
		"explanation": o.Explanation, "deadline": o.Deadline, "status": o.Status, "snoozed_until": o.SnoozedUntil,
		"featured": o.FeaturedOn.Valid && o.FeaturedOn.Time.Equal(today.Time), "created_at": o.CreatedAt,
		"item": gin.H{"id": o.RawItemID, "url": o.Url, "title": o.Title, "body": o.Body, "community": o.Community, "author_handle": o.AuthorHandle,
			"published_at": o.PublishedAt, "fetched_at": o.FetchedAt, "metrics": orEmptyJSON(o.Metrics), "source_name": o.SourceName, "connector": o.Connector},
		"events": evs,
		"drafts": drafts, "draft_kind": draftKindFor(c.Request.Context(), q, m.WorkspaceID, o.Lane),
		"drafting": s.draftingState(c.Request.Context(), m.WorkspaceID, id, drafts),
	})
}

type statusInput struct {
	Status       string     `json:"status"`
	Reason       *string    `json:"reason"`
	Note         *string    `json:"note"`
	SnoozedUntil *time.Time `json:"snoozed_until"`
}

func (s *server) handleSetStatus(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	id, err := pathUUID(c, "id")
	if err != nil {
		s.fail(c, err)
		return
	}
	var in statusInput
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	if err := oneOf("status", in.Status, "new", "saved", "acted", "dismissed", "snoozed"); err != nil {
		s.fail(c, err)
		return
	}
	var snooze *time.Time
	switch in.Status {
	case "dismissed":
		if in.Reason == nil || !slices.Contains(dismissReasons, *in.Reason) {
			s.fail(c, badRequest("dismissing requires a reason: "+strings.Join(dismissReasons, ", ")))
			return
		}
	case "snoozed":
		if in.SnoozedUntil == nil || !in.SnoozedUntil.After(time.Now()) || in.SnoozedUntil.After(time.Now().AddDate(0, 3, 0)) {
			s.fail(c, badRequest("snoozed_until must be in the next 3 months"))
			return
		}
		snooze = in.SnoozedUntil
	}
	if err := optString("note", in.Note, 1000); err != nil {
		s.fail(c, err)
		return
	}
	ctx := c.Request.Context()
	err = s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		if _, err := q.SetOpportunityStatus(ctx, store.SetOpportunityStatusParams{WorkspaceID: m.WorkspaceID, ID: id, Status: store.OppStatus(in.Status), SnoozedUntil: snooze}); err != nil {
			return err
		}
		event := map[string]string{"new": "restored", "saved": "saved", "acted": "acted", "dismissed": "dismissed", "snoozed": "snoozed"}[in.Status]
		meta, _ := json.Marshal(map[string]any{"note": in.Note})
		if err := q.InsertOpportunityEvent(ctx, store.InsertOpportunityEventParams{WorkspaceID: m.WorkspaceID, ID: id, UserID: &u.ID, Event: event, Reason: in.Reason, Meta: meta}); err != nil {
			return err
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "opportunity.status", "opportunity", id.String(), map[string]any{"status": in.Status, "reason": in.Reason})
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *server) handleLogEvent(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	id, err := pathUUID(c, "id")
	if err != nil {
		s.fail(c, err)
		return
	}
	var in struct {
		Event   string         `json:"event"`
		Outcome *string        `json:"outcome"`
		Meta    map[string]any `json:"meta"`
	}
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	if err := oneOf("event", in.Event, clientEvents...); err != nil {
		s.fail(c, err)
		return
	}
	if in.Event == "outcome" && (in.Outcome == nil || !slices.Contains(outcomes, *in.Outcome)) {
		s.fail(c, badRequest("outcome must be one of: "+strings.Join(outcomes, ", ")))
		return
	}
	meta, _ := json.Marshal(in.Meta)
	if len(meta) > 4096 {
		s.fail(c, badRequest("meta is too large"))
		return
	}
	q := store.New(s.pool)
	if _, err := q.GetOpportunity(c.Request.Context(), store.GetOpportunityParams{WorkspaceID: m.WorkspaceID, ID: id}); err != nil {
		s.fail(c, err)
		return
	}
	if in.Event == "copied_draft" {
		if err := validDraftEvent(c.Request.Context(), q, m.WorkspaceID, id, in.Meta); err != nil {
			s.fail(c, err)
			return
		}
	}
	if err := q.InsertOpportunityEvent(c.Request.Context(), store.InsertOpportunityEventParams{WorkspaceID: m.WorkspaceID, ID: id, UserID: &u.ID,
		Event: in.Event, Outcome: in.Outcome, Meta: meta}); err != nil {
		s.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// handleProcessPending enqueues the pipeline for unprocessed items, or (with
// rescore=true) first resets recent items that produced no opportunity.
func (s *server) handleProcessPending(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	rescore := c.Query("rescore") == "true"
	ctx := c.Request.Context()
	var n int
	err := s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		if rescore {
			if _, err := q.ResetRawItemsForReprocess(ctx, m.WorkspaceID); err != nil {
				return err
			}
		}
		ids, err := q.ListUnprocessedRawItems(ctx, store.ListUnprocessedRawItemsParams{WorkspaceID: m.WorkspaceID, Limit: 1000})
		if err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := s.jobs.InsertTx(ctx, tx, jobs.ProcessItemArgs{WorkspaceID: m.WorkspaceID, RawItemID: id}, nil); err != nil {
				return err
			}
		}
		n = len(ids)
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "items.process", "workspace", m.WorkspaceID.String(), map[string]any{"count": n, "rescore": rescore})
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"enqueued": n})
}

// ---- lane configs (Settings) ----

type laneConfigDTO struct {
	Lane           string             `json:"lane"`
	Weights        map[string]float64 `json:"weights"`
	DailyQuota     int32              `json:"daily_quota"`
	FreshnessHours int32              `json:"freshness_hours"`
	MinScore       float64            `json:"min_score"`
	DraftKind      string             `json:"draft_kind"`
}

var factorNames = []string{scoring.Relevance, scoring.Intent, scoring.Fit, scoring.Recency, scoring.Reach, scoring.Deadline}

func (s *server) handleListLanes(c *gin.Context) {
	rows, err := store.New(s.pool).ListLaneConfigs(c.Request.Context(), memberOf(c).WorkspaceID)
	if err != nil {
		s.fail(c, err)
		return
	}
	out := make([]laneConfigDTO, 0, len(rows))
	for _, r := range rows {
		d := laneConfigDTO{Lane: string(r.Lane), DailyQuota: r.DailyQuota, FreshnessHours: r.FreshnessHours, MinScore: numF(r.MinScore), DraftKind: r.DraftKind}
		_ = json.Unmarshal(r.Weights, &d.Weights)
		out = append(out, d)
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

func (s *server) handleSaveLane(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	lane := c.Param("lane")
	if err := oneOf("lane", lane, lanes...); err != nil {
		s.fail(c, errNotFound)
		return
	}
	var in laneConfigDTO
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	var total float64
	for k, w := range in.Weights {
		if !slices.Contains(factorNames, k) {
			s.fail(c, badRequest("unknown weight "+k+"; use: "+strings.Join(factorNames, ", ")))
			return
		}
		if w < 0 || w > 1 {
			s.fail(c, badRequest("weights must be between 0 and 1"))
			return
		}
		total += w
	}
	switch {
	case total <= 0:
		s.fail(c, badRequest("at least one weight must be positive"))
		return
	case in.DailyQuota < 0 || in.DailyQuota > 100:
		s.fail(c, badRequest("daily_quota must be between 0 and 100"))
		return
	case in.FreshnessHours < 1 || in.FreshnessHours > 24*90:
		s.fail(c, badRequest("freshness_hours must be between 1 and 2160"))
		return
	case in.MinScore < 0 || in.MinScore > 1:
		s.fail(c, badRequest("min_score must be between 0 and 1"))
		return
	}
	if err := oneOf("draft_kind", in.DraftKind, "reply", "pitch", "comment"); err != nil {
		s.fail(c, err)
		return
	}
	w, _ := json.Marshal(in.Weights)
	ctx := c.Request.Context()
	err := s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		if _, err := q.UpdateLaneConfig(ctx, store.UpdateLaneConfigParams{WorkspaceID: m.WorkspaceID, Lane: store.Lane(lane), Weights: w,
			DailyQuota: in.DailyQuota, FreshnessHours: in.FreshnessHours, MinScore: numeric(in.MinScore), DraftKind: in.DraftKind}); err != nil {
			return err
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "lane.update", "lane_config", lane, map[string]any{"weights": in.Weights, "quota": in.DailyQuota})
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
