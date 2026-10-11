package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/jobs"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/pipeline"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

type draftPortfolioDTO struct {
	ID    uuid.UUID `json:"id"`
	Kind  string    `json:"kind"`
	Title string    `json:"title"`
	URL   *string   `json:"url"`
}

type draftDTO struct {
	ID             uuid.UUID           `json:"id"`
	Kind           string              `json:"kind"`
	Variant        string              `json:"variant"`
	Subject        *string             `json:"subject"`
	Body           string              `json:"body"`
	WordCount      int32               `json:"word_count"`
	WordLimit      int                 `json:"word_limit"`
	IncludeLink    bool                `json:"include_link"`
	PortfolioItems []draftPortfolioDTO `json:"portfolio_items"`
	RuleFlags      json.RawMessage     `json:"rule_flags"`
	Instruction    *string             `json:"instruction"`
	CreatedAt      time.Time           `json:"created_at"`
}

// draftingDTO reports the latest draft job for an opportunity.
type draftingDTO struct {
	State string  `json:"state"` // idle | pending | failed
	Error *string `json:"error"`
}

var allJobStates = []rivertype.JobState{
	rivertype.JobStateAvailable, rivertype.JobStateCancelled, rivertype.JobStateCompleted, rivertype.JobStateDiscarded,
	rivertype.JobStatePending, rivertype.JobStateRetryable, rivertype.JobStateRunning, rivertype.JobStateScheduled,
}

// loadDrafts returns the newest batch per kind with portfolio details.
func (s *server) loadDrafts(ctx context.Context, q *store.Queries, ws, opp uuid.UUID) ([]draftDTO, error) {
	rows, err := q.ListLatestDrafts(ctx, store.ListLatestDraftsParams{WorkspaceID: ws, OpportunityID: opp})
	if err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	for _, r := range rows {
		ids = append(ids, r.PortfolioItemIds...)
	}
	items := map[uuid.UUID]draftPortfolioDTO{}
	if len(ids) > 0 {
		ps, err := q.PortfolioItemsByIDs(ctx, store.PortfolioItemsByIDsParams{WorkspaceID: ws, Ids: ids})
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			items[p.ID] = draftPortfolioDTO{p.ID, p.Kind, p.Title, p.Url}
		}
	}
	out := make([]draftDTO, 0, len(rows))
	for _, r := range rows {
		d := draftDTO{ID: r.ID, Kind: r.Kind, Variant: r.Variant, Subject: r.Subject, Body: r.Body, WordCount: r.WordCount,
			WordLimit: pipeline.KindSpecs[r.Kind].WordLimit(r.Variant), IncludeLink: r.IncludeLink, PortfolioItems: []draftPortfolioDTO{},
			RuleFlags: orEmptyArray(r.RuleFlags), Instruction: r.Instruction, CreatedAt: r.CreatedAt}
		for _, id := range r.PortfolioItemIds {
			if p, ok := items[id]; ok {
				d.PortfolioItems = append(d.PortfolioItems, p)
			}
		}
		out = append(out, d)
	}
	return out, nil
}

// draftingState reads the most recent draft job for the opportunity.
// Failures older than the newest drafts are no longer interesting.
func (s *server) draftingState(ctx context.Context, ws, opp uuid.UUID, drafts []draftDTO) draftingDTO {
	idle := draftingDTO{State: "idle"}
	if s.jobs == nil {
		return idle
	}
	res, err := s.jobs.JobList(ctx, river.NewJobListParams().Kinds(jobs.GenerateDraftsArgs{}.Kind()).
		Metadata(string(jobs.DraftJobMetadata(ws, opp))).States(allJobStates...).
		OrderBy(river.JobListOrderByID, river.SortOrderDesc).First(1))
	if err != nil || len(res.Jobs) == 0 {
		if err != nil {
			s.log.WarnContext(ctx, "list draft jobs", "err", err)
		}
		return idle
	}
	j := res.Jobs[0]
	lastErr := func() *string {
		if len(j.Errors) == 0 {
			return nil
		}
		msg := j.Errors[len(j.Errors)-1].Error
		if utf8.RuneCountInString(msg) > 300 {
			msg = string([]rune(msg)[:300]) + "…"
		}
		return &msg
	}
	switch j.State {
	case rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled:
		return draftingDTO{State: "pending", Error: lastErr()}
	case rivertype.JobStateDiscarded, rivertype.JobStateCancelled:
		newest := time.Time{}
		for _, d := range drafts {
			if d.CreatedAt.After(newest) {
				newest = d.CreatedAt
			}
		}
		if j.FinalizedAt != nil && j.FinalizedAt.After(newest) {
			return draftingDTO{State: "failed", Error: lastErr()}
		}
	}
	return idle
}

type draftRequestInput struct {
	Kind        string  `json:"kind"`
	Instruction *string `json:"instruction"`
}

// handleRequestDrafts queues a draft job; the detail endpoint reports
// progress and returns the drafts once stored.
func (s *server) handleRequestDrafts(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	id, err := pathUUID(c, "id")
	if err != nil {
		s.fail(c, err)
		return
	}
	var in draftRequestInput
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	if in.Kind != "" {
		if _, ok := pipeline.KindSpecs[in.Kind]; !ok {
			s.fail(c, badRequest("kind must be one of: reply, pitch, comment"))
			return
		}
	}
	if err := optString("instruction", in.Instruction, pipeline.MaxInstructionChars); err != nil {
		s.fail(c, err)
		return
	}
	instruction := ""
	if in.Instruction != nil {
		instruction = strings.TrimSpace(*in.Instruction)
	}
	ctx := c.Request.Context()
	var duplicate bool
	err = s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		if _, err := q.GetOpportunity(ctx, store.GetOpportunityParams{WorkspaceID: m.WorkspaceID, ID: id}); err != nil {
			return err
		}
		res, err := s.jobs.InsertTx(ctx, tx, jobs.GenerateDraftsArgs{WorkspaceID: m.WorkspaceID, OpportunityID: id, DraftKind: in.Kind, Instruction: instruction, UserID: &u.ID},
			jobs.DraftJobOpts(m.WorkspaceID, id, jobs.PriorityRequested))
		if err != nil {
			return err
		}
		duplicate = res.UniqueSkippedAsDuplicate
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "drafts.request", "opportunity", id.String(), map[string]any{"kind": in.Kind, "instruction": instruction != ""})
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"queued": !duplicate})
}

// validDraftEvent checks a copied_draft event names one of the
// opportunity's drafts.
func validDraftEvent(ctx context.Context, q *store.Queries, ws, opp uuid.UUID, meta map[string]any) error {
	raw, _ := meta["draft_id"].(string)
	draftID, err := uuid.Parse(raw)
	if err != nil {
		return badRequest("copied_draft needs meta.draft_id")
	}
	ok, err := q.DraftBelongsTo(ctx, store.DraftBelongsToParams{WorkspaceID: ws, OpportunityID: opp, ID: draftID})
	if err != nil {
		return err
	}
	if !ok {
		return badRequest("meta.draft_id is not a draft of this opportunity")
	}
	return nil
}

func orEmptyArray(b []byte) json.RawMessage {
	if len(b) == 0 || string(b) == "null" {
		return json.RawMessage(`[]`)
	}
	return b
}

// draftKindFor returns the lane's default draft kind.
func draftKindFor(ctx context.Context, q *store.Queries, ws uuid.UUID, lane store.Lane) string {
	lanes, err := q.ListLaneConfigs(ctx, ws)
	if err != nil {
		return ""
	}
	i := slices.IndexFunc(lanes, func(l store.LaneConfig) bool { return l.Lane == lane })
	if i < 0 {
		return ""
	}
	return lanes[i].DraftKind
}
