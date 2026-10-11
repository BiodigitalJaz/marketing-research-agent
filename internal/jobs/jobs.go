// Package jobs defines River job args and workers, and builds the River
// client used by the worker (to work jobs) and the API (to insert them).
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/collect"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/embed"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/knowledge"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/pipeline"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/research"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/secrets"
)

// EmbedderFor returns the embedder configured for a workspace, or
// embed.ErrNotConfigured.
type EmbedderFor func(ctx context.Context, ws uuid.UUID) (embed.Embedder, error)

// Deps are shared by workers.
type Deps struct {
	Log      *slog.Logger
	Pool     *pgxpool.Pool
	Embedder EmbedderFor
	Secrets  *secrets.Box // nil or keyless: rotation is a no-op
	Collect  *collect.Service
	Pipeline *pipeline.Service
	Research *research.Service
	// Knowledge syncs internal docs and builds the memory bank.
	Knowledge *knowledge.Service
	// Location is the timezone for the daily inbox (default UTC).
	Location *time.Location
}

// StaticEmbedder adapts a fixed embedder (tests, single-workspace tools).
func StaticEmbedder(e embed.Embedder) EmbedderFor {
	return func(context.Context, uuid.UUID) (embed.Embedder, error) {
		if e == nil {
			return nil, embed.ErrNotConfigured
		}
		return e, nil
	}
}

// PingArgs is a no-op job used to verify the queue end to end.
type PingArgs struct {
	Note string `json:"note"`
}

func (PingArgs) Kind() string { return "ping" }

type PingWorker struct {
	river.WorkerDefaults[PingArgs]
	Log *slog.Logger
}

func (w *PingWorker) Work(ctx context.Context, job *river.Job[PingArgs]) error {
	w.Log.InfoContext(ctx, "ping job", "job_id", job.ID, "note", job.Args.Note)
	return nil
}

// Workers registers every worker. Add new jobs here.
func Workers(d Deps) (*river.Workers, error) {
	ws := river.NewWorkers()
	for _, add := range []func() error{
		func() error { return river.AddWorkerSafely(ws, &PingWorker{Log: d.Log}) },
		func() error { return river.AddWorkerSafely(ws, &EmbedEntityWorker{Deps: d}) },
		func() error { return river.AddWorkerSafely(ws, &PurgeSessionsWorker{Deps: d}) },
		func() error { return river.AddWorkerSafely(ws, &RotateSecretsWorker{Deps: d}) },
		func() error { return river.AddWorkerSafely(ws, &CollectMonitorWorker{Deps: d}) },
		func() error { return river.AddWorkerSafely(ws, &ProcessItemWorker{Deps: d}) },
		func() error { return river.AddWorkerSafely(ws, &ScheduleMonitorsWorker{Deps: d}) },
		func() error { return river.AddWorkerSafely(ws, &BuildDailyInboxWorker{Deps: d}) },
		func() error { return river.AddWorkerSafely(ws, &ExpireOpportunitiesWorker{Deps: d}) },
		func() error { return river.AddWorkerSafely(ws, &GenerateDraftsWorker{Deps: d}) },
		func() error { return river.AddWorkerSafely(ws, &ResearchWorker{Deps: d}) },
		func() error { return river.AddWorkerSafely(ws, &SyncKnowledgeWorker{Deps: d}) },
		func() error { return river.AddWorkerSafely(ws, &BuildMemoryBankWorker{Deps: d}) },
		func() error { return river.AddWorkerSafely(ws, &RetentionSweepWorker{Deps: d}) },
	} {
		if err := add(); err != nil {
			return nil, fmt.Errorf("register worker: %w", err)
		}
	}
	return ws, nil
}

func periodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{
		river.NewPeriodicJob(river.PeriodicInterval(6*time.Hour),
			func() (river.JobArgs, *river.InsertOpts) { return PurgeSessionsArgs{}, nil },
			&river.PeriodicJobOpts{RunOnStart: true}),
		river.NewPeriodicJob(river.PeriodicInterval(24*time.Hour),
			func() (river.JobArgs, *river.InsertOpts) { return RotateSecretsArgs{}, nil },
			&river.PeriodicJobOpts{RunOnStart: true}),
		river.NewPeriodicJob(river.PeriodicInterval(24*time.Hour),
			func() (river.JobArgs, *river.InsertOpts) { return RetentionSweepArgs{}, nil },
			&river.PeriodicJobOpts{RunOnStart: true}),
		river.NewPeriodicJob(river.PeriodicInterval(time.Minute),
			func() (river.JobArgs, *river.InsertOpts) { return ScheduleMonitorsArgs{}, nil },
			&river.PeriodicJobOpts{RunOnStart: true}),
		river.NewPeriodicJob(river.PeriodicInterval(time.Hour),
			func() (river.JobArgs, *river.InsertOpts) { return BuildDailyInboxArgs{}, nil },
			&river.PeriodicJobOpts{RunOnStart: true}),
		river.NewPeriodicJob(river.PeriodicInterval(time.Hour),
			func() (river.JobArgs, *river.InsertOpts) { return ExpireOpportunitiesArgs{}, nil },
			&river.PeriodicJobOpts{RunOnStart: true}),
	}
}

// NewWorkerClient builds a River client that works jobs.
func NewWorkerClient(d Deps, maxWorkers int) (*river.Client[pgx.Tx], error) {
	return NewWorkerClientWithPipeline(d, maxWorkers, 2)
}

// NewWorkerClientWithPipeline also sizes the pipeline queue.
func NewWorkerClientWithPipeline(d Deps, maxWorkers, pipelineWorkers int) (*river.Client[pgx.Tx], error) {
	if pipelineWorkers < 1 {
		pipelineWorkers = 1
	}
	ws, err := Workers(d)
	if err != nil {
		return nil, err
	}
	c, err := river.NewClient(riverpgxv5.New(d.Pool), &river.Config{
		Logger: d.Log,
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: maxWorkers},
			QueuePipeline:      {MaxWorkers: pipelineWorkers},
			QueueResearch:      {MaxWorkers: researchWorkers},
		},
		Workers:              ws,
		PeriodicJobs:         periodicJobs(),
		RescueStuckJobsAfter: RescueStuckJobsAfter,
	})
	if err != nil {
		return nil, fmt.Errorf("create river client: %w", err)
	}
	return c, nil
}

// RescueStuckJobsAfter is how long a job may stay running before River
// requeues it. It must exceed the longest worker Timeout (research, ~41m).
// A job can be orphaned in the running state when the producer's fetch
// commits but the reply times out (seen during a DB I/O stall); River's 1h
// default left such jobs idle far longer than needed.
const RescueStuckJobsAfter = 50 * time.Minute

// researchWorkers bounds concurrent research jobs per worker process.
const researchWorkers = 2

// NewInsertClient builds an insert-only River client (no queues), for the API.
func NewInsertClient(pool *pgxpool.Pool, log *slog.Logger) (*river.Client[pgx.Tx], error) {
	c, err := river.NewClient(riverpgxv5.New(pool), &river.Config{Logger: log})
	if err != nil {
		return nil, fmt.Errorf("create river insert client: %w", err)
	}
	return c, nil
}
