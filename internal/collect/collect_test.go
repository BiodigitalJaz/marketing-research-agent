package collect_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/collect"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/fetch"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/jobs"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/search"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/testutil/pgtest"
)

type paidBackend struct{ calls atomic.Int32 }

func (b *paidBackend) Name() string { return "brave" }
func (b *paidBackend) Paid() bool   { return true }
func (b *paidBackend) Search(_ context.Context, q search.Query) ([]search.Result, error) {
	n := b.calls.Add(1)
	return []search.Result{{URL: fmt.Sprintf("https://forum.example/t/%d", n), Title: "Thread about " + q.Q, Snippet: "Discussion of AI planning bot safety on small accounts."}}, nil
}

func fixtures(t *testing.T) (*httptest.Server, *httptest.Server) {
	t.Helper()
	hn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0/newstories.json":
			_, _ = w.Write([]byte(`[3,2,1]`))
		case "/v0/item/1.json":
			_, _ = w.Write([]byte(`{"id":1,"type":"story","by":"a","time":` + fmt.Sprint(time.Now().Unix()) + `,"title":"Ask HN: AI planning bot for Trello?","text":"Is it safe?","score":10}`))
		case "/v0/item/2.json":
			_, _ = w.Write([]byte(`{"id":2,"type":"story","by":"b","time":` + fmt.Sprint(time.Now().Unix()) + `,"title":"Unrelated compiler post","score":10}`))
		case "/v0/item/3.json":
			_, _ = w.Write([]byte(`{"id":3,"type":"story","by":"c","time":` + fmt.Sprint(time.Now().Unix()) + `,"title":"AI planning bot telegram signals","score":10}`))
		default:
			http.NotFound(w, r)
		}
	}))
	rss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<rss version="2.0"><channel><title>Forum</title>
		  <item><guid>se-1</guid><title>Trello stop loss on fractional shares</title><link>https://forum.example/q/1</link>
		  <description>Does an AI planning bot respect stop losses on fractional shares?</description><pubDate>` + time.Now().Format(time.RFC1123Z) + `</pubDate></item>
		</channel></rss>`))
	}))
	t.Cleanup(hn.Close)
	t.Cleanup(rss.Close)
	return hn, rss
}

func TestRunMonitorDedupesAndEnqueues(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.NewMigratedDB(t)
	log := pgtest.DiscardLogger()
	q := store.New(pool)
	ws, _ := q.CreateWorkspace(ctx, "A")
	for _, k := range []struct{ term, kind string }{{"AI planning bot", "track"}, {"telegram", "negative"}} {
		if _, err := q.CreateKeyword(ctx, store.CreateKeywordParams{WorkspaceID: ws.ID, Term: k.term, Kind: k.kind}); err != nil {
			t.Fatal(err)
		}
	}
	hnSrv, rssSrv := fixtures(t)
	mk := func(connector, name, cfg string, enabled bool) uuid.UUID {
		s, err := q.CreateSource(ctx, store.CreateSourceParams{WorkspaceID: ws.ID, Connector: connector, Name: name, Config: []byte(cfg), Enabled: enabled})
		if err != nil {
			t.Fatal(err)
		}
		return s.ID
	}
	hnID := mk("hn", "HN", `{"base_url":"`+hnSrv.URL+`/v0","lists":["newstories"]}`, true)
	rssID := mk("rss", "Forum", `{"feed_url":"`+rssSrv.URL+`"}`, true)
	searchID := mk("search", "Web", `{"max_queries":3}`, true)
	brokenID := mk("rss", "Broken", `{"feed_url":"http://127.0.0.1:1/nope"}`, true)
	disabledID := mk("rss", "Disabled", `{"feed_url":"`+rssSrv.URL+`"}`, false)
	mon, err := q.CreateMonitor(ctx, store.CreateMonitorParams{WorkspaceID: ws.ID, Name: "Inbound", SourceIds: []uuid.UUID{hnID, rssID, searchID, brokenID, disabledID},
		Query: "", Lanes: []store.Lane{store.LaneInbound}, ScheduleCron: "*/30 * * * *", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	// Budget: 2 paid queries per month.
	if _, err := q.UpsertWorkspaceSettings(ctx, store.UpsertWorkspaceSettingsParams{WorkspaceID: ws.ID, Embeddings: []byte("{}"), Search: []byte(`{"backend":"brave","monthly_budget":2}`)}); err != nil {
		t.Fatal(err)
	}

	ins, _ := jobs.NewInsertClient(pool, log)
	backend := &paidBackend{}
	svc := &collect.Service{
		Pool: pool, Log: log, RetentionDays: 90,
		Fetch:      fetch.New(fetch.Options{AllowPrivate: true, PerHostRate: 1000}),
		NewBackend: func(context.Context, search.Settings) (search.Backend, error) { return backend, nil },
		Enqueue: func(ctx context.Context, tx pgx.Tx, ws, id uuid.UUID) error {
			_, err := ins.InsertTx(ctx, tx, jobs.ProcessItemArgs{WorkspaceID: ws, RawItemID: id}, nil)
			return err
		},
	}

	sum, err := svc.Run(ctx, ws.ID, mon.ID)
	if err != nil {
		t.Fatal(err)
	}
	// HN: only item 1 matches (2 unrelated, 3 negative). RSS: 1. Search: 1 query (track keyword), 1 result.
	if sum.New != 3 {
		t.Fatalf("first run new = %d, summary = %+v", sum.New, sum)
	}
	var broken collect.SourceRun
	for _, s := range sum.Sources {
		if s.SourceID == disabledID {
			t.Fatal("disabled source must be skipped")
		}
		if s.SourceID == brokenID {
			broken = s
		}
	}
	if broken.Error == "" {
		t.Fatal("broken source should report an error without failing the run")
	}

	countJobs := func() int {
		var n int
		_ = pool.QueryRow(ctx, "SELECT count(*) FROM river_job WHERE kind = 'process_item'").Scan(&n)
		return n
	}
	if countJobs() != 3 {
		t.Fatalf("process_item jobs = %d, want one per new item", countJobs())
	}

	// Second run: HN cursor advanced, RSS/search duplicates dropped on insert.
	sum2, err := svc.Run(ctx, ws.ID, mon.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sum2.New != 1 { // search backend returns a new URL each call
		t.Fatalf("second run new = %d, want 1 (only the fresh search result)", sum2.New)
	}
	var total int
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM raw_items WHERE workspace_id = $1", ws.ID).Scan(&total)
	if total != 4 || countJobs() != 4 {
		t.Fatalf("raw_items = %d jobs = %d", total, countJobs())
	}

	// Third run: budget (2/month) exhausted, so the search source reports it.
	sum3, _ := svc.Run(ctx, ws.ID, mon.ID)
	for _, s := range sum3.Sources {
		if s.SourceID == searchID && !strings.Contains(s.Error, "budget") {
			t.Fatalf("search source should hit the budget: %+v", s)
		}
	}
	if backend.calls.Load() != 2 {
		t.Fatalf("paid queries = %d, want 2", backend.calls.Load())
	}

	src, _ := q.GetSource(ctx, store.GetSourceParams{WorkspaceID: ws.ID, ID: hnID})
	var st struct {
		LastMaxID int64 `json:"last_max_id"`
	}
	_ = json.Unmarshal(src.State, &st)
	if src.Health != "ok" || st.LastMaxID != 3 || src.ItemCount != 1 || src.LastRunAt == nil {
		t.Fatalf("hn source = health %s state %s count %d", src.Health, src.State, src.ItemCount)
	}
	bad, _ := q.GetSource(ctx, store.GetSourceParams{WorkspaceID: ws.ID, ID: brokenID})
	if bad.Health != "failing" || bad.LastError == nil {
		t.Fatalf("broken source health = %s", bad.Health)
	}
	m, _ := q.GetMonitor(ctx, store.GetMonitorParams{WorkspaceID: ws.ID, ID: mon.ID})
	if m.LastRunAt == nil || m.LastError == nil {
		t.Fatalf("monitor run not recorded: %+v", m)
	}
	var item struct {
		Title, Community string
		Expires          *time.Time
	}
	_ = pool.QueryRow(ctx, "SELECT title, community, expires_at FROM raw_items WHERE source_id = $1", hnID).Scan(&item.Title, &item.Community, &item.Expires)
	if item.Community != "Hacker News" || item.Expires == nil || item.Expires.Before(time.Now().AddDate(0, 0, 89)) {
		t.Fatalf("hn raw item = %+v", item)
	}
}

func TestNextRun(t *testing.T) {
	base := time.Date(2026, 10, 4, 10, 7, 0, 0, time.UTC)
	next, err := jobs.NextRun("*/30 * * * *", base)
	if err != nil || !next.Equal(time.Date(2026, 10, 4, 10, 30, 0, 0, time.UTC)) {
		t.Fatalf("next = %v err = %v", next, err)
	}
	if _, err := jobs.NextRun("every tuesday", base); err == nil {
		t.Fatal("invalid cron must error")
	}
	if n, err := jobs.NextRun("@hourly", base); err != nil || n.Minute() != 0 {
		t.Fatalf("@hourly: %v %v", n, err)
	}
}

// A source added to a monitor that already ran must backfill its first-run
// window instead of inheriting the monitor's last-run cutoff.
func TestNewSourceOnExistingMonitorBackfills(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.NewMigratedDB(t)
	log := pgtest.DiscardLogger()
	q := store.New(pool)
	ws, _ := q.CreateWorkspace(ctx, "A")

	published := time.Now().Add(-48 * time.Hour).Format(time.RFC1123Z)
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<rss version="2.0"><channel><title>Alert</title>
		  <item><guid>x1</guid><title>AI planning bot review</title><link>https://example.com/x1</link>
		  <description>Two days old.</description><pubDate>` + published + `</pubDate></item></channel></rss>`))
	}))
	defer feed.Close()
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<rss version="2.0"><channel><title>Empty</title></channel></rss>`))
	}))
	defer empty.Close()

	first, _ := q.CreateSource(ctx, store.CreateSourceParams{WorkspaceID: ws.ID, Connector: "rss", Name: "First", Config: []byte(`{"feed_url":"` + empty.URL + `","match_all":true}`), Enabled: true})
	mon, _ := q.CreateMonitor(ctx, store.CreateMonitorParams{WorkspaceID: ws.ID, Name: "M", SourceIds: []uuid.UUID{first.ID}, Lanes: []store.Lane{store.LaneInbound}, ScheduleCron: "@hourly", Enabled: true})
	svc := &collect.Service{Pool: pool, Log: log, RetentionDays: 90, Fetch: fetch.New(fetch.Options{AllowPrivate: true, PerHostRate: 1000})}
	if _, err := svc.Run(ctx, ws.ID, mon.ID); err != nil {
		t.Fatal(err)
	}

	// Later, a new source joins the monitor.
	added, _ := q.CreateSource(ctx, store.CreateSourceParams{WorkspaceID: ws.ID, Connector: "rss", Name: "Alert", Config: []byte(`{"feed_url":"` + feed.URL + `","match_all":true}`), Enabled: true})
	if _, err := q.UpdateMonitor(ctx, store.UpdateMonitorParams{WorkspaceID: ws.ID, ID: mon.ID, Name: "M", SourceIds: []uuid.UUID{first.ID, added.ID},
		Lanes: []store.Lane{store.LaneInbound}, ScheduleCron: "@hourly", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	sum, err := svc.Run(ctx, ws.ID, mon.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sum.New != 1 {
		t.Fatalf("new source should backfill its 2-day-old item, got %d new", sum.New)
	}
}
