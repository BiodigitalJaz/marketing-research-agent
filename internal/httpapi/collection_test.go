package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/collect"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/fetch"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/httpapi"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/testutil/pgtest"
)

const feedXML = `<rss version="2.0"><channel><title>Forum</title>
  <item><guid>a1</guid><title>Is an AI planning bot safe on Trello?</title><link>https://forum.example/q/a1</link><description>Question about AI planning bot safety.</description></item>
</channel></rss>`

func TestSourcesMonitorsAndItems(t *testing.T) {
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(feedXML)) }))
	defer feed.Close()
	var svc *collect.Service
	e := newEnvWith(t, func(pool *pgxpool.Pool, d *httpapi.Deps) {
		svc = &collect.Service{Pool: pool, Log: pgtest.DiscardLogger(), RetentionDays: 90, Fetch: fetch.New(fetch.Options{AllowPrivate: true, PerHostRate: 1000})}
		d.Collect = svc
	})
	ws := e.workspace("A")
	e.user("admin@a.com", ws, store.MemberRoleAdmin)
	e.user("editor@a.com", ws, store.MemberRoleEditor)
	other := e.workspace("B")
	e.user("b@b.com", other, store.MemberRoleAdmin)
	ctx := context.Background()
	if _, err := e.q.CreateKeyword(ctx, store.CreateKeywordParams{WorkspaceID: ws, Term: "AI planning bot", Kind: "track"}); err != nil {
		t.Fatal(err)
	}

	admin := e.client()
	admin.login("admin@a.com")
	ed := e.client()
	ed.login("editor@a.com")

	if code, _ := ed.do(http.MethodPost, "/sources", map[string]any{"connector": "rss", "name": "x", "config": map[string]any{"feed_url": feed.URL}}); code != http.StatusForbidden {
		t.Fatalf("editor create source = %d, want 403", code)
	}
	for _, bad := range []map[string]any{
		{"connector": "twitter", "name": "x"},
		{"connector": "rss", "name": "x", "config": map[string]any{"feed_url": "not a url"}},
		{"connector": "hn", "name": "x", "config": map[string]any{"lists": []string{"frontpage"}}},
	} {
		if code, body := admin.do(http.MethodPost, "/sources", bad); code != http.StatusBadRequest {
			t.Errorf("bad source %v = %d %s", bad, code, body)
		}
	}
	code, body := admin.do(http.MethodPost, "/sources", map[string]any{"connector": "rss", "name": "Forum", "config": map[string]any{"feed_url": feed.URL}})
	if code != http.StatusCreated {
		t.Fatalf("create source = %d %s", code, body)
	}
	var src struct{ ID string }
	_ = json.Unmarshal(body, &src)

	// Test: returns items, writes nothing.
	code, body = admin.do(http.MethodPost, "/sources/"+src.ID+"/test", nil)
	var test struct {
		OK    bool                     `json:"ok"`
		Items []struct{ Title string } `json:"items"`
	}
	_ = json.Unmarshal(body, &test)
	if code != http.StatusOK || !test.OK || len(test.Items) != 1 {
		t.Fatalf("test source = %d %s", code, body)
	}
	if n, _ := e.q.CountRawItems(ctx, ws); n != 0 {
		t.Fatalf("test wrote %d items", n)
	}

	// Monitors: editors can manage them; cron and sources validated.
	if code, _ := ed.do(http.MethodPost, "/monitors", map[string]any{"name": "M", "source_ids": []string{src.ID}, "schedule_cron": "every so often"}); code != http.StatusBadRequest {
		t.Fatalf("bad cron = %d", code)
	}
	b := e.client()
	b.login("b@b.com")
	if code, _ := b.do(http.MethodPost, "/monitors", map[string]any{"name": "M", "source_ids": []string{src.ID}}); code != http.StatusBadRequest {
		t.Fatalf("foreign source = %d, want 400", code)
	}
	code, body = ed.do(http.MethodPost, "/monitors", map[string]any{"name": "Inbound", "source_ids": []string{src.ID}, "lanes": []string{"inbound"}, "schedule_cron": "@hourly"})
	if code != http.StatusCreated {
		t.Fatalf("create monitor = %d %s", code, body)
	}
	var mon struct {
		ID        string  `json:"id"`
		NextRunAt *string `json:"next_run_at"`
	}
	_ = json.Unmarshal(body, &mon)
	if mon.NextRunAt == nil {
		t.Fatal("enabled monitor should be due immediately")
	}
	if code, _ := ed.do(http.MethodPost, "/monitors/"+mon.ID+"/run", nil); code != http.StatusAccepted {
		t.Fatalf("run now = %d", code)
	}
	var jobs int
	_ = e.pool.QueryRow(ctx, "SELECT count(*) FROM river_job WHERE kind = 'collect_monitor'").Scan(&jobs)
	if jobs != 1 {
		t.Fatalf("collect jobs = %d", jobs)
	}

	// Run the collection directly (as the worker would) and list items.
	m, _ := e.q.ListMonitors(ctx, ws)
	if _, err := svc.Run(ctx, ws, m[0].ID); err != nil {
		t.Fatal(err)
	}
	code, body = ed.do(http.MethodGet, "/items?source_id="+src.ID, nil)
	var items struct {
		Items []struct {
			Title      *string `json:"title"`
			SourceName string  `json:"source_name"`
		} `json:"items"`
		Total int `json:"total"`
	}
	_ = json.Unmarshal(body, &items)
	if code != http.StatusOK || items.Total != 1 || len(items.Items) != 1 || items.Items[0].SourceName != "Forum" {
		t.Fatalf("items = %d %s", code, body)
	}
	if code, body := b.do(http.MethodGet, "/items", nil); code != http.StatusOK || !json.Valid(body) {
		t.Fatalf("other workspace items = %d", code)
	} else {
		var other struct{ Total int }
		_ = json.Unmarshal(body, &other)
		if other.Total != 0 {
			t.Fatal("items leaked across workspaces")
		}
	}
}
