// Command worker runs background jobs via River.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/time/rate"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/app"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/collect"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/db"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/fetch"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/jobs"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/knowledge"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm/router"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/mcp"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/pipeline"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/research"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/secrets"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/sources/hn"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, log, err := app.Bootstrap("worker")
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.Database.URL)
	if err != nil {
		return err
	}
	defer pool.Close()

	box, err := secrets.FromEnv(os.Getenv)
	if err != nil {
		return err
	}
	rt := router.New(pool, box, os.Getenv, log)
	loc, err := time.LoadLocation(cfg.Inbox.Timezone)
	if err != nil {
		return err
	}
	collector := &collect.Service{
		Pool: pool, Secrets: box, Env: os.Getenv, Log: log, RetentionDays: cfg.Retention.RawItemsDays,
		Fetch:   fetch.New(fetch.Options{HostRates: map[string]rate.Limit{hn.APIHost: 20, "api.github.com": 5}}),
		Enqueue: jobs.EnqueueProcessItem,
	}
	researcher := &research.Service{Pool: pool, Router: rt, Embedder: rt.Embedder, Search: collector.Search, Fetch: collector.Fetch,
		MCP: &mcp.Loader{Pool: pool, Box: box, Env: os.Getenv}, Log: log, RetentionDays: cfg.Retention.RawItemsDays}
	kb := &knowledge.Service{Pool: pool, Get: collector.Fetch, Secrets: box, Env: os.Getenv, Embedder: rt.Embedder, Router: rt, Log: log}
	client, err := jobs.NewWorkerClientWithPipeline(jobs.Deps{Log: log, Pool: pool, Embedder: rt.Embedder, Secrets: box, Collect: collector,
		Pipeline: &pipeline.Service{Pool: pool, Router: rt, Embedder: rt.Embedder, Log: log}, Research: researcher, Knowledge: kb, Location: loc},
		cfg.Worker.MaxWorkers, cfg.Worker.PipelineWorkers)
	if err != nil {
		return err
	}
	// On a fresh database the api's migrate step may still be running.
	if err := db.WaitForMigrations(ctx, pool, 2*time.Second, log); err != nil {
		return err
	}
	// Start with a context that outlives the signal so in-flight jobs are
	// drained by Stop below rather than cancelled immediately.
	if err := client.Start(context.WithoutCancel(ctx)); err != nil {
		return fmt.Errorf("start river: %w", err)
	}
	log.Info("worker started", "max_workers", cfg.Worker.MaxWorkers)

	<-ctx.Done()
	log.Info("worker draining", "timeout", cfg.Worker.ShutdownTimeout)

	drainCtx, cancel := context.WithTimeout(context.Background(), cfg.Worker.ShutdownTimeout)
	defer cancel()
	if err := client.Stop(drainCtx); err != nil {
		log.Warn("drain timed out; cancelling in-flight jobs", "err", err)
		hardCtx, hardCancel := context.WithTimeout(context.Background(), cfg.Worker.ShutdownTimeout)
		defer hardCancel()
		if err := client.StopAndCancel(hardCtx); err != nil {
			return fmt.Errorf("stop river: %w", err)
		}
	}
	log.Info("worker stopped")
	return nil
}
