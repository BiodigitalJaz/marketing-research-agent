package db_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/db"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/testutil/pgtest"
	"github.com/BiodigitalJaz/marketing-research-agent/migrations"
)

var wantTables = []string{
	"workspaces", "users", "memberships", "sessions",
	"brand_profiles", "portfolio_items", "icps", "keywords", "competitors",
	"sources", "monitors", "raw_items",
	"model_providers", "task_routes", "prompts", "model_runs",
	"lane_configs", "opportunities", "drafts", "opportunity_events",
	"research_jobs", "research_steps", "chat_messages", "reports", "citations",
	"embeddings", "audit_log",
	"secrets", "provider_models", "mcp_servers", "workspace_settings",
	"river_job", "river_leader", "river_migration",
}

func TestMigrateCleanAndIdempotent(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.NewDB(t)
	log := pgtest.DiscardLogger()

	if err := db.Migrate(ctx, pool, log); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	for _, tbl := range wantTables {
		var exists bool
		err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)`,
			tbl).Scan(&exists)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("table %s missing after migrate", tbl)
		}
	}

	// Second run must be a no-op.
	if err := db.Migrate(ctx, pool, log); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	provider := gooseProvider(t, pool)
	cur, err := provider.GetDBVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sources := provider.ListSources()
	if want := sources[len(sources)-1].Version; cur != want {
		t.Errorf("db version = %d, want %d", cur, want)
	}
	if pending, err := provider.HasPending(ctx); err != nil || pending {
		t.Errorf("pending after second migrate: %v %v", pending, err)
	}
}

func TestMigrationsRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.NewDB(t)
	provider := gooseProvider(t, pool)

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	if _, err := provider.DownTo(ctx, 0); err != nil {
		t.Fatalf("down to 0: %v", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
}

func TestEmbeddingIndexUsable(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.NewMigratedDB(t)
	// The partial HNSW index requires the cast; a 768-dim vector must insert
	// and be queryable with the documented query shape.
	vec := "[" + strings.Repeat("0.1,", 767) + "0.1]"
	_, err := pool.Exec(ctx, `INSERT INTO embeddings (entity_type, entity_id, model, dims, embedding, workspace_id)
		VALUES ('raw_item', gen_random_uuid(), 'nomic-embed-text', 768, $1::vector, gen_random_uuid())`, vec)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM (
		SELECT entity_id FROM embeddings WHERE model = $1
		ORDER BY embedding::vector(768) <=> $2::vector(768) LIMIT 5) s`, "nomic-embed-text", vec).Scan(&n)
	if err != nil || n != 1 {
		t.Fatalf("similarity query: n=%d err=%v", n, err)
	}
}

func gooseProvider(t *testing.T, pool *pgxpool.Pool) *goose.Provider {
	t.Helper()
	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = sqlDB.Close() })
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestWaitForMigrations(t *testing.T) {
	if v, err := db.LatestVersion(); err != nil || v < 18 {
		t.Fatalf("LatestVersion = %d, %v", v, err)
	}
	pool := pgtest.NewDB(t)
	log := pgtest.DiscardLogger()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := db.WaitForMigrations(ctx, pool, 50*time.Millisecond, log); err == nil {
		t.Fatal("an unmigrated database should keep the wait going until the deadline")
	}

	done := make(chan error, 1)
	go func() { done <- db.WaitForMigrations(context.Background(), pool, 50*time.Millisecond, log) }()
	if err := db.Migrate(context.Background(), pool, log); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("wait did not end after migrating")
	}
}
