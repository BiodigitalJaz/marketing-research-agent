package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/robfig/cron/v3"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/collect"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

// CollectMonitorArgs runs one monitor's sources.
type CollectMonitorArgs struct {
	WorkspaceID uuid.UUID `json:"workspace_id"`
	MonitorID   uuid.UUID `json:"monitor_id"`
}

func (CollectMonitorArgs) Kind() string { return "collect_monitor" }

// InsertOpts dedupes concurrent runs of the same monitor.
func (CollectMonitorArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true, ByPeriod: time.Minute}}
}

type CollectMonitorWorker struct {
	river.WorkerDefaults[CollectMonitorArgs]
	Deps
}

func (w *CollectMonitorWorker) Timeout(*river.Job[CollectMonitorArgs]) time.Duration {
	return 10 * time.Minute
}

func (w *CollectMonitorWorker) Work(ctx context.Context, job *river.Job[CollectMonitorArgs]) error {
	if w.Collect == nil {
		return river.JobCancel(fmt.Errorf("collection is not configured"))
	}
	_, err := w.Collect.Run(ctx, job.Args.WorkspaceID, job.Args.MonitorID)
	return err
}

// EnqueueProcessItem is collect.Enqueue for workers: it inserts ProcessItem
// in the collection transaction using the worker's River client.
func EnqueueProcessItem(ctx context.Context, tx pgx.Tx, ws, rawItemID uuid.UUID) error {
	client := river.ClientFromContext[pgx.Tx](ctx)
	_, err := client.InsertTx(ctx, tx, ProcessItemArgs{WorkspaceID: ws, RawItemID: rawItemID}, nil)
	return err
}

var _ collect.Enqueue = EnqueueProcessItem

// ProcessItemArgs runs the opportunity pipeline for a new raw item.
type ProcessItemArgs struct {
	WorkspaceID uuid.UUID `json:"workspace_id"`
	RawItemID   uuid.UUID `json:"raw_item_id"`
}

func (ProcessItemArgs) Kind() string { return "process_item" }

// QueuePipeline isolates LLM-bound pipeline jobs so their concurrency can be
// sized to the models (a local GPU serves one request at a time).
const QueuePipeline = "pipeline"

// Pipeline queue priorities (1 runs first): drafts a person asked for, then
// scoring, then drafts prepared ahead of time for the daily inbox.
const (
	PriorityRequested = 1
	PriorityScoring   = 2
	PriorityEager     = 3
)

func (ProcessItemArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueuePipeline, Priority: PriorityScoring}
}

// ProcessItemWorker runs the opportunity pipeline. LLM failures return an
// error so River retries with backoff (e.g. until a model is reachable).
type ProcessItemWorker struct {
	river.WorkerDefaults[ProcessItemArgs]
	Deps
}

func (w *ProcessItemWorker) Timeout(*river.Job[ProcessItemArgs]) time.Duration {
	return 5 * time.Minute
}

func (w *ProcessItemWorker) Work(ctx context.Context, job *river.Job[ProcessItemArgs]) error {
	if w.Pipeline == nil {
		return river.JobCancel(fmt.Errorf("pipeline is not configured"))
	}
	res, err := w.Pipeline.Process(ctx, job.Args.WorkspaceID, job.Args.RawItemID)
	if err != nil {
		return err
	}
	w.Log.InfoContext(ctx, "item processed", "workspace_id", job.Args.WorkspaceID, "raw_item_id", job.Args.RawItemID,
		"opportunities", res.Opportunities, "reason", res.Reason)
	return nil
}

// ScheduleMonitorsArgs enqueues collection for monitors whose cron is due.
type ScheduleMonitorsArgs struct{}

func (ScheduleMonitorsArgs) Kind() string { return "schedule_monitors" }

type ScheduleMonitorsWorker struct {
	river.WorkerDefaults[ScheduleMonitorsArgs]
	Deps
}

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// NextRun parses a monitor's cron expression and returns its next run after t.
func NextRun(expr string, t time.Time) (time.Time, error) {
	sched, err := cronParser.Parse(expr)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid schedule %q: %w", expr, err)
	}
	return sched.Next(t), nil
}

func (w *ScheduleMonitorsWorker) Work(ctx context.Context, _ *river.Job[ScheduleMonitorsArgs]) error {
	q := store.New(w.Pool)
	due, err := q.ListDueMonitors(ctx)
	if err != nil {
		return fmt.Errorf("list due monitors: %w", err)
	}
	client := river.ClientFromContext[pgx.Tx](ctx)
	now := time.Now()
	for _, m := range due {
		next, err := NextRun(m.ScheduleCron, now)
		if err != nil {
			msg := err.Error()
			_ = q.RecordMonitorRun(ctx, store.RecordMonitorRunParams{WorkspaceID: m.WorkspaceID, ID: m.ID, LastError: &msg})
			next = now.Add(time.Hour)
		} else if _, err := client.Insert(ctx, CollectMonitorArgs{WorkspaceID: m.WorkspaceID, MonitorID: m.ID}, nil); err != nil {
			return fmt.Errorf("enqueue monitor %s: %w", m.ID, err)
		}
		if err := q.SetMonitorNextRun(ctx, store.SetMonitorNextRunParams{WorkspaceID: m.WorkspaceID, ID: m.ID, NextRunAt: &next}); err != nil {
			return err
		}
	}
	return nil
}
