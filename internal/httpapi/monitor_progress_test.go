package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

func TestMonitorProgress(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ws := e.workspace("Benchwright")
	e.user("ed@example.com", ws, store.MemberRoleEditor)
	c := e.client()
	c.login("ed@example.com")

	src, err := e.q.CreateSource(ctx, store.CreateSourceParams{WorkspaceID: ws, Connector: "rss", Name: "Feed", Config: []byte(`{"feed_url":"https://example.com/f"}`), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	mon, err := e.q.CreateMonitor(ctx, store.CreateMonitorParams{WorkspaceID: ws, Name: "M", SourceIds: []uuid.UUID{src.ID},
		Lanes: []store.Lane{store.LaneInbound}, ScheduleCron: "@hourly", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	type progress struct {
		Phase                               string
		SourcesDone                         int `json:"sources_done"`
		SourcesTotal                        int `json:"sources_total"`
		Collected, Processed, Opportunities int
	}
	get := func() progress {
		t.Helper()
		code, body := c.do(http.MethodGet, "/monitors/"+mon.ID.String()+"/progress", nil)
		if code != http.StatusOK {
			t.Fatalf("progress = %d %s", code, body)
		}
		var p progress
		_ = json.Unmarshal(body, &p)
		return p
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}

	if p := get(); p.Phase != "idle" {
		t.Fatalf("before any run: %+v", p)
	}
	if code, _ := c.do(http.MethodPost, "/monitors/"+mon.ID.String()+"/run", nil); code != http.StatusAccepted {
		t.Fatalf("run = %d", code)
	}
	if p := get(); p.Phase != "queued" {
		t.Fatalf("after Run now: %+v", p)
	}

	// The worker picks up the job, starts the run, and finishes one of two
	// sources.
	exec("UPDATE river_job SET state = 'completed', finalized_at = now() WHERE kind = 'collect_monitor'")
	exec("UPDATE monitors SET run_started_at = now() - interval '1 minute', run_sources_total = 2, run_sources_done = 1 WHERE id = $1", mon.ID)
	var item uuid.UUID
	if err := e.pool.QueryRow(ctx, `INSERT INTO raw_items (workspace_id, source_id, external_id, url, title, body, content_hash)
		VALUES ($1, $2, 'x', 'https://example.com/x', 'x', 'body', sha256('x'::bytea)) RETURNING id`, ws, src.ID).Scan(&item); err != nil {
		t.Fatal(err)
	}
	if p := get(); p.Phase != "collecting" || p.SourcesDone != 1 || p.SourcesTotal != 2 || p.Collected != 1 {
		t.Fatalf("collecting: %+v", p)
	}

	// Collection ends; the item waits for scoring.
	exec("UPDATE monitors SET run_sources_done = 2, last_run_at = now() WHERE id = $1", mon.ID)
	if p := get(); p.Phase != "scoring" || p.Collected != 1 || p.Processed != 0 {
		t.Fatalf("scoring: %+v", p)
	}

	// The pipeline processes it into an opportunity.
	exec("UPDATE raw_items SET processed_at = now() WHERE id = $1", item)
	exec(`INSERT INTO opportunities (workspace_id, raw_item_id, lane, opp_type, score, score_breakdown, explanation)
		VALUES ($1, $2, 'inbound', 'pain_point', 0.8, '{}', 'x')`, ws, item)
	if p := get(); p.Phase != "done" || p.Processed != 1 || p.Opportunities != 1 {
		t.Fatalf("done: %+v", p)
	}
}
