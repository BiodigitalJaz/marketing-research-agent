package db

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/BiodigitalJaz/marketing-research-agent/migrations"
)

// Migrate applies all pending goose migrations, then River's own
// migrations. Both are idempotent: running Migrate on an up-to-date
// database is a no-op. Goose holds a Postgres advisory lock while it runs,
// so concurrent invocations are safe.
func Migrate(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() { _ = sqlDB.Close() }()

	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return fmt.Errorf("create goose locker: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS,
		goose.WithSessionLocker(locker))
	if err != nil {
		return fmt.Errorf("create goose provider: %w", err)
	}
	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	for _, r := range results {
		log.InfoContext(ctx, "applied migration", "source", r.Source.Path, "duration", r.Duration)
	}

	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return fmt.Errorf("create river migrator: %w", err)
	}
	res, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		return fmt.Errorf("river migrate: %w", err)
	}
	for _, v := range res.Versions {
		log.InfoContext(ctx, "applied river migration", "version", v.Version)
	}
	return nil
}

// LatestVersion is the newest goose migration embedded in the binary.
func LatestVersion() (int64, error) {
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return 0, err
	}
	var latest int64
	for _, e := range entries {
		n, _, ok := strings.Cut(e.Name(), "_")
		if !ok || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		if v, err := strconv.ParseInt(n, 10, 64); err == nil && v > latest {
			latest = v
		}
	}
	return latest, nil
}

// WaitForMigrations blocks until the schema is at LatestVersion and River's
// tables exist, polling every interval. Processes that don't migrate (the
// worker) call it at startup, so on a fresh database they wait for the
// api's migrate step instead of failing. It only reads.
func WaitForMigrations(ctx context.Context, pool *pgxpool.Pool, interval time.Duration, log *slog.Logger) error {
	want, err := LatestVersion()
	if err != nil {
		return err
	}
	logged := false
	for {
		var ready bool
		err := pool.QueryRow(ctx, `SELECT to_regclass('river_queue') IS NOT NULL
			AND to_regclass('goose_db_version') IS NOT NULL
			AND coalesce((SELECT max(version_id) FROM goose_db_version WHERE is_applied), 0) >= $1`, want).Scan(&ready)
		if err == nil && ready {
			return nil
		}
		if err != nil && !logged {
			log.InfoContext(ctx, "waiting for the database", "err", err)
			logged = true
		} else if !logged {
			log.InfoContext(ctx, "waiting for database migrations", "want_version", want)
			logged = true
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for migrations: %w", ctx.Err())
		case <-time.After(interval):
		}
	}
}
