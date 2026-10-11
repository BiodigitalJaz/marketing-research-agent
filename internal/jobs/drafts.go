package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/pipeline"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

// GenerateDraftsArgs drafts responses for one opportunity: on
// request from the detail page, or ahead of time for featured items.
type GenerateDraftsArgs struct {
	WorkspaceID   uuid.UUID  `json:"workspace_id"`
	OpportunityID uuid.UUID  `json:"opportunity_id"`
	DraftKind     string     `json:"kind,omitempty"`
	Instruction   string     `json:"instruction,omitempty"`
	UserID        *uuid.UUID `json:"user_id,omitempty"`
}

func (GenerateDraftsArgs) Kind() string { return "generate_drafts" }

// draftUniqueStates dedupes identical requests while one is still pending,
// but allows the same request again once it has finished.
var draftUniqueStates = []rivertype.JobState{
	rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
	rivertype.JobStateRetryable, rivertype.JobStateScheduled,
}

// DraftJobOpts are the insert options for a draft job. Metadata carries the
// ids so the API can report progress per opportunity.
func DraftJobOpts(ws, opp uuid.UUID, priority int) *river.InsertOpts {
	return &river.InsertOpts{
		Queue: QueuePipeline, Priority: priority, MaxAttempts: 5,
		Metadata:   DraftJobMetadata(ws, opp),
		UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: draftUniqueStates},
	}
}

// DraftJobMetadata is the metadata fragment that identifies an
// opportunity's draft jobs.
func DraftJobMetadata(ws, opp uuid.UUID) []byte {
	return fmt.Appendf(nil, `{"workspace_id":%q,"opportunity_id":%q}`, ws, opp)
}

type GenerateDraftsWorker struct {
	river.WorkerDefaults[GenerateDraftsArgs]
	Deps
}

func (w *GenerateDraftsWorker) Timeout(*river.Job[GenerateDraftsArgs]) time.Duration {
	return 5 * time.Minute
}

func (w *GenerateDraftsWorker) Work(ctx context.Context, job *river.Job[GenerateDraftsArgs]) error {
	if w.Pipeline == nil {
		return river.JobCancel(errors.New("pipeline is not configured"))
	}
	a := job.Args
	drafts, err := w.Pipeline.Draft(ctx, pipeline.DraftRequest{Workspace: a.WorkspaceID, Opportunity: a.OpportunityID, Kind: a.DraftKind, Instruction: a.Instruction, UserID: a.UserID})
	switch {
	case errors.Is(err, pipeline.ErrNoOpportunity), errors.Is(err, pipeline.ErrBelowMinScore), errors.Is(err, pipeline.ErrUnknownKind):
		return river.JobCancel(err)
	case err != nil:
		return err
	}
	w.Log.InfoContext(ctx, "drafts generated", "workspace_id", a.WorkspaceID, "opportunity_id", a.OpportunityID, "count", len(drafts))
	return nil
}

// enqueueEagerDrafts queues drafts for today's featured opportunities that
// have none, in the transaction that featured them.
func enqueueEagerDrafts(ctx context.Context, tx pgx.Tx, ws uuid.UUID, today pgtype.Date) (int, error) {
	client, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err != nil {
		return 0, nil // outside a worker (tests calling FeatureToday directly)
	}
	ids, err := store.New(tx).FeaturedWithoutDrafts(ctx, store.FeaturedWithoutDraftsParams{WorkspaceID: ws, Today: today})
	if err != nil {
		return 0, err
	}
	params := make([]river.InsertManyParams, 0, len(ids))
	for _, id := range ids {
		params = append(params, river.InsertManyParams{Args: GenerateDraftsArgs{WorkspaceID: ws, OpportunityID: id}, InsertOpts: DraftJobOpts(ws, id, PriorityEager)})
	}
	if len(params) == 0 {
		return 0, nil
	}
	if _, err := client.InsertManyTx(ctx, tx, params); err != nil {
		return 0, fmt.Errorf("enqueue drafts: %w", err)
	}
	return len(params), nil
}
