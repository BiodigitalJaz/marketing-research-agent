// Command api serves the HTTP API.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/app"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/collect"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/config"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/db"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/fetch"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/httpapi"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/jobs"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm/router"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/secrets"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/seed"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/sources/hn"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, log, err := app.Bootstrap("api")
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

	inserter, err := jobs.NewInsertClient(pool, log)
	if err != nil {
		return err
	}
	box, err := secrets.FromEnv(os.Getenv)
	if err != nil {
		return err
	}
	if !box.Enabled() {
		log.Warn("secrets master key not set; entering API keys in Settings is disabled", "env", secrets.EnvKey)
	}

	// Shipped prompt versions are installed on every start, so upgrades
	// bring new versions without re-running the seed.
	if err := seed.InstallPrompts(ctx, store.New(pool)); err != nil {
		return fmt.Errorf("install prompts: %w", err)
	}
	if err := httpapi.PrepareSetup(ctx, pool, config.Getenv(os.Getenv, "SETUP_TOKEN"), cfg.Server.BaseURL, log); err != nil {
		return fmt.Errorf("prepare setup: %w", err)
	}

	loc, err := time.LoadLocation(cfg.Inbox.Timezone)
	if err != nil {
		return err
	}
	if cfg.Log.Level != "debug" {
		gin.SetMode(gin.ReleaseMode)
	}
	srv := &http.Server{
		Addr: cfg.Server.Addr,
		Handler: httpapi.NewRouter(httpapi.Deps{
			Log: log, DB: pool, Pool: pool, Jobs: inserter,
			SecureCookies: app.SecureCookies(cfg),
			Router:        router.New(pool, box, os.Getenv, log), Secrets: box, Env: os.Getenv,
			Collect: &collect.Service{Pool: pool, Secrets: box, Env: os.Getenv, Log: log, RetentionDays: cfg.Retention.RawItemsDays,
				Fetch: fetch.New(fetch.Options{HostRates: map[string]rate.Limit{hn.APIHost: 20}})},
			Location: loc,
			Config:   cfg,
		}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("api listening", "addr", cfg.Server.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("listen: %w", err)
		}
	case <-ctx.Done():
	}

	log.Info("api shutting down", "timeout", cfg.Server.ShutdownTimeout)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	log.Info("api stopped")
	return nil
}
