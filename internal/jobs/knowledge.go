package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/knowledge"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

// knowledgeActive are the states in which a knowledge job is queued or
// working; one at a time per workspace.
var knowledgeActive = []rivertype.JobState{
	rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled,
}

// SyncKnowledgeArgs syncs one knowledge source from its branch, on request
// (there is no schedule). Force also rebuilds the memory bank when nothing
// changed.
type SyncKnowledgeArgs struct {
	WorkspaceID uuid.UUID `json:"workspace_id"`
	SourceID    uuid.UUID `json:"source_id"`
	Force       bool      `json:"force,omitempty"`
}

func (SyncKnowledgeArgs) Kind() string { return "sync_knowledge" }

func (a SyncKnowledgeArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueResearch, MaxAttempts: 5,
		UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: knowledgeActive}}
}

// busyRetry is how long a knowledge job waits when the other one runs.
const busyRetry = 30 * time.Second

type SyncKnowledgeWorker struct {
	river.WorkerDefaults[SyncKnowledgeArgs]
	Deps
}

func (w *SyncKnowledgeWorker) Timeout(*river.Job[SyncKnowledgeArgs]) time.Duration {
	return 10 * time.Minute
}

func (w *SyncKnowledgeWorker) Work(ctx context.Context, job *river.Job[SyncKnowledgeArgs]) error {
	if w.Knowledge == nil {
		return river.JobCancel(errors.New("knowledge is not configured"))
	}
	a := job.Args
	src, err := store.New(w.Pool).GetKnowledgeSource(ctx, store.GetKnowledgeSourceParams{WorkspaceID: a.WorkspaceID, ID: a.SourceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	res, err := w.Knowledge.Sync(ctx, src)
	if errors.Is(err, knowledge.ErrBusy) {
		return river.JobSnooze(busyRetry) // a build is running; sync after it
	}
	if err != nil {
		// Recorded on the source; Settings shows it. Retrying won't fix a bad token.
		w.Log.WarnContext(ctx, "knowledge sync failed", "workspace_id", src.WorkspaceID, "source", src.Name, "err", err)
		return river.JobCancel(err)
	}
	w.Log.InfoContext(ctx, "knowledge synced", "workspace_id", src.WorkspaceID, "source", src.Name, "commit", short(res.Commit),
		"changed", res.Changed, "removed", res.Removed, "embedded", res.Embedded)
	if res.Changed == 0 && res.Removed == 0 && !a.Force {
		return nil
	}
	client, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err != nil {
		return nil
	}
	if _, err := client.Insert(ctx, BuildMemoryBankArgs{WorkspaceID: src.WorkspaceID}, nil); err != nil {
		return fmt.Errorf("enqueue memory bank build: %w", err)
	}
	w.Knowledge.MarkPending(ctx, src.WorkspaceID)
	return nil
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// BuildMemoryBankArgs brings a workspace's memory bank up to date with its
// knowledge docs. Long builds continue across runs (snoozed, not retried).
type BuildMemoryBankArgs struct {
	WorkspaceID uuid.UUID `json:"workspace_id"`
}

func (BuildMemoryBankArgs) Kind() string { return "build_memory_bank" }

func (BuildMemoryBankArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueResearch, MaxAttempts: 5,
		UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: knowledgeActive}}
}

// memoryBankSlice is how long one run works before handing over to the next.
const memoryBankSlice = 12 * time.Minute

type BuildMemoryBankWorker struct {
	river.WorkerDefaults[BuildMemoryBankArgs]
	Deps
}

func (w *BuildMemoryBankWorker) Timeout(*river.Job[BuildMemoryBankArgs]) time.Duration {
	return memoryBankSlice + 8*time.Minute
}

func (w *BuildMemoryBankWorker) Work(ctx context.Context, job *river.Job[BuildMemoryBankArgs]) error {
	if w.Knowledge == nil {
		return river.JobCancel(errors.New("knowledge is not configured"))
	}
	ws := job.Args.WorkspaceID
	done, err := w.Knowledge.BuildMemoryBank(ctx, ws, time.Now().Add(memoryBankSlice))
	switch {
	case errors.Is(err, knowledge.ErrBusy):
		return river.JobSnooze(busyRetry)
	case errors.Is(err, knowledge.ErrNoRouter):
		return river.JobCancel(err)
	case err != nil:
		return err
	case !done:
		return river.JobSnooze(time.Second) // more to do; continue in a fresh run
	}
	w.Log.InfoContext(ctx, "memory bank up to date", "workspace_id", ws)
	return nil
}

// KnowledgeBusy reports whether a sync or build is queued or running for a
// workspace (the API refuses to start another meanwhile).
func KnowledgeBusy(ctx context.Context, list func(context.Context, *river.JobListParams) (*river.JobListResult, error), ws uuid.UUID) (bool, error) {
	res, err := list(ctx, river.NewJobListParams().Kinds(SyncKnowledgeArgs{}.Kind(), BuildMemoryBankArgs{}.Kind()).States(knowledgeActive...).
		Where("args->>'workspace_id' = @ws", river.NamedArgs{"ws": ws.String()}).First(1))
	if err != nil {
		return false, err
	}
	return len(res.Jobs) > 0, nil
}
