package jobs

import (
	"context"
	"fmt"

	"github.com/riverqueue/river"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

// retentionBatch bounds each delete so a large backlog never holds long locks.
const retentionBatch = 500

// RetentionSweepArgs deletes collected items past their retention window
// (retention.raw_items_days), keeping items behind an opportunity someone
// saved, snoozed, or acted on and items a report cites (periodic, daily).
type RetentionSweepArgs struct{}

func (RetentionSweepArgs) Kind() string { return "retention_sweep" }

type RetentionSweepWorker struct {
	river.WorkerDefaults[RetentionSweepArgs]
	Deps
}

func (w *RetentionSweepWorker) Work(ctx context.Context, _ *river.Job[RetentionSweepArgs]) error {
	q := store.New(w.Pool)
	wss, err := q.ListWorkspaceIDs(ctx)
	if err != nil {
		return fmt.Errorf("retention: %w", err)
	}
	for _, ws := range wss {
		var items int64
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			n, err := q.DeleteExpiredRawItems(ctx, store.DeleteExpiredRawItemsParams{WorkspaceID: ws, Lim: retentionBatch})
			if err != nil {
				return fmt.Errorf("retention: delete items: %w", err)
			}
			items += n
			if n < retentionBatch {
				break
			}
		}
		emb, err := q.DeleteOrphanItemEmbeddings(ctx, ws)
		if err != nil {
			return fmt.Errorf("retention: delete embeddings: %w", err)
		}
		if items > 0 || emb > 0 {
			w.Log.InfoContext(ctx, "retention sweep", "workspace_id", ws, "items_deleted", items, "embeddings_deleted", emb)
		}
	}
	return nil
}
