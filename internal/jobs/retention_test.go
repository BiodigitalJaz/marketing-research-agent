package jobs_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/jobs"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/testutil/pgtest"
)

func TestRetentionSweep(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.NewMigratedDB(t)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	var ws, src uuid.UUID
	if err := pool.QueryRow(ctx, "INSERT INTO workspaces (name) VALUES ('W') RETURNING id").Scan(&ws); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "INSERT INTO sources (workspace_id, connector, name) VALUES ($1, 'rss', 'Feed') RETURNING id", ws).Scan(&src); err != nil {
		t.Fatal(err)
	}
	item := func(name, expires string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO raw_items (workspace_id, source_id, external_id, url, title, body, content_hash, expires_at)
			VALUES ($1, $2, $3, 'https://example.com/'||$3, $3, 'body', sha256($3::bytea), now() + $4::interval) RETURNING id`, ws, src, name, expires).Scan(&id); err != nil {
			t.Fatal(err)
		}
		exec("INSERT INTO embeddings (entity_type, entity_id, chunk, model, dims, embedding, workspace_id) VALUES ('raw_item', $1, 0, 'm', 3, '[1,0,0]', $2)", id, ws)
		return id
	}
	opp := func(raw uuid.UUID, status string) {
		t.Helper()
		exec(`INSERT INTO opportunities (workspace_id, raw_item_id, lane, opp_type, score, score_breakdown, explanation, status)
			VALUES ($1, $2, 'inbound', 'pain_point', 0.8, '{}', 'x', $3::opp_status)`, ws, raw, status)
	}

	expired := item("expired", "-1 day")
	expiredNewOpp := item("expired-new-opp", "-1 day")
	opp(expiredNewOpp, "new")
	expiredActed := item("expired-acted", "-1 day")
	opp(expiredActed, "acted")
	expiredSaved := item("expired-saved", "-1 day")
	opp(expiredSaved, "saved")
	expiredCited := item("expired-cited", "-1 day")
	var report uuid.UUID
	if err := pool.QueryRow(ctx, "INSERT INTO reports (workspace_id, title, markdown) VALUES ($1, 'R', 'x') RETURNING id", ws).Scan(&report); err != nil {
		t.Fatal(err)
	}
	exec("INSERT INTO citations (report_id, marker, raw_item_id, url) VALUES ($1, '[1]', $2, 'https://example.com')", report, expiredCited)
	fresh := item("fresh", "30 days")

	w := &jobs.RetentionSweepWorker{Deps: jobs.Deps{Pool: pool, Log: pgtest.DiscardLogger()}}
	if err := w.Work(ctx, &river.Job[jobs.RetentionSweepArgs]{}); err != nil {
		t.Fatal(err)
	}

	exists := func(id uuid.UUID) bool {
		var n int
		_ = pool.QueryRow(ctx, "SELECT count(*) FROM raw_items WHERE id = $1", id).Scan(&n)
		return n == 1
	}
	for _, tt := range []struct {
		name string
		id   uuid.UUID
		keep bool
	}{
		{"expired", expired, false},
		{"expired with a new opportunity", expiredNewOpp, false},
		{"expired but acted on", expiredActed, true},
		{"expired but saved", expiredSaved, true},
		{"expired but cited", expiredCited, true},
		{"not expired", fresh, true},
	} {
		if got := exists(tt.id); got != tt.keep {
			t.Errorf("%s: kept = %v, want %v", tt.name, got, tt.keep)
		}
	}
	var opps, embs int
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM opportunities").Scan(&opps)
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM embeddings").Scan(&embs)
	if opps != 2 {
		t.Errorf("opportunities = %d, want 2 (the new one goes with its item)", opps)
	}
	if embs != 4 {
		t.Errorf("embeddings = %d, want 4 (orphans of deleted items removed)", embs)
	}
}
