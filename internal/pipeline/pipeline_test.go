package pipeline_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/embed"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/jobs"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm/router"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/pipeline"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/prompts"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/testutil/pgtest"
)

// fakeRouter answers classify/score from functions and records requests.
type fakeRouter struct {
	mu       sync.Mutex
	classify func(msg string) string
	score    func(msg string) string
	draft    func(req llm.ChatRequest) string // generation and revisions
	check    func(msg string) string
	fail     error
	calls    []string
	messages []string
}

func (f *fakeRouter) Chat(_ context.Context, call router.Call, req llm.ChatRequest) (*llm.ChatResponse, []router.Attempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call.Task)
	msg := req.Messages[0].Parts[0].Text
	f.messages = append(f.messages, call.Task+": "+msg)
	if f.fail != nil {
		return nil, nil, f.fail
	}
	if req.JSONSchema == nil || call.PromptID == nil || !strings.Contains(req.System, "Benchwright") {
		panic("pipeline must send a schema, the active prompt id, and a rendered brand prompt")
	}
	var text string
	switch {
	case call.Task == "draft" && strings.Contains(req.System, "You review draft"):
		text = f.check(msg)
	case call.Task == "draft":
		text = f.draft(req)
	case call.Task == "score":
		text = f.score(msg)
	default:
		text = f.classify(msg)
	}
	// No RunID: real runs are recorded by the router, which this fake replaces.
	return &llm.ChatResponse{Parts: []llm.Part{llm.TextPart(text)}}, []router.Attempt{{}}, nil
}

type fakeEmbedder struct{}

func (fakeEmbedder) Model() string { return "fake-embed" }
func (fakeEmbedder) Dims() int     { return 32 }

// Embed returns zero-centered hash vectors (near-orthogonal for different
// texts); every text containing "twin" gets the same vector.
func (fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		seed := t
		if strings.Contains(t, "twin") {
			seed = "twin"
		}
		h := sha256.Sum256([]byte(seed))
		v := make([]float32, 32)
		for j := range v {
			v[j] = float32(h[j]) - 127.5
		}
		out[i] = v
	}
	return out, nil
}

type env struct {
	t   *testing.T
	ctx context.Context
	q   *store.Queries
	ws  uuid.UUID
	rss uuid.UUID
	hn  uuid.UUID
	mon uuid.UUID
	svc *pipeline.Service
	fr  *fakeRouter
}

func setup(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	pool := pgtest.NewMigratedDB(t)
	q := store.New(pool)
	ws, _ := q.CreateWorkspace(ctx, "Benchwright")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, lane := range []struct {
		l       store.Lane
		w       string
		fresh   int32
		minimum string
	}{
		{store.LaneInbound, `{"relevance":0.2,"intent":0.35,"fit":0.2,"recency":0.2,"reach":0.05}`, 48, "0.5"},
		{store.LaneMedia, `{"relevance":0.2,"intent":0.1,"fit":0.3,"recency":0.05,"reach":0.1,"deadline":0.25}`, 336, "0.5"},
		{store.LaneThoughtLeadership, `{"relevance":0.25,"intent":0.05,"fit":0.2,"recency":0.15,"reach":0.35}`, 96, "0.5"},
	} {
		var n pgtype.Numeric
		_ = n.Scan(lane.minimum)
		must(q.InsertLaneConfigIfMissing(ctx, store.InsertLaneConfigIfMissingParams{WorkspaceID: ws.ID, Lane: lane.l, Weights: []byte(lane.w),
			DailyQuota: 2, FreshnessHours: lane.fresh, MinScore: n, DraftKind: "reply"}))
	}
	defs, _ := prompts.Defaults()
	for _, d := range defs {
		must(q.InsertDefaultPromptIfMissing(ctx, store.InsertDefaultPromptIfMissingParams{Task: d.Task, Version: d.Version, Body: d.Body}))
	}
	_, err := q.UpsertBrandProfile(ctx, store.UpsertBrandProfileParams{WorkspaceID: ws.ID, Profile: []byte(`{"services":["Planning agent with a risk engine"],
		"targeting":{"excluded_venues":["Hacker News"],"avoid":["job seekers"]},"tone":{"dont":["guaranteed"]}}`)})
	must(err)
	_, err = q.CreateKeyword(ctx, store.CreateKeywordParams{WorkspaceID: ws.ID, Term: "telegram", Kind: "negative"})
	must(err)
	rss, _ := q.CreateSource(ctx, store.CreateSourceParams{WorkspaceID: ws.ID, Connector: "rss", Name: "Forum", Config: []byte(`{"feed_url":"https://x"}`), Enabled: true})
	hn, _ := q.CreateSource(ctx, store.CreateSourceParams{WorkspaceID: ws.ID, Connector: "hn", Name: "Hacker News", Config: []byte(`{}`), Enabled: true})
	mon, _ := q.CreateMonitor(ctx, store.CreateMonitorParams{WorkspaceID: ws.ID, Name: "M", SourceIds: []uuid.UUID{rss.ID, hn.ID},
		Lanes: []store.Lane{store.LaneInbound, store.LaneThoughtLeadership}, ScheduleCron: "@hourly"})

	fr := &fakeRouter{
		classify: func(string) string {
			return `{"candidates":[{"lane":"inbound","opp_type":"recommendation_request","deadline":null,"rationale":"Asks for a safe AI planning tool."}],"not_relevant_reason":null}`
		},
		score: func(string) string {
			return `{"relevance":0.9,"intent":0.9,"fit":0.8,"flags":["competitor_mention"],"explanation":"A Trello user asks whether AI planning is safe."}`
		},
	}
	svc := &pipeline.Service{Pool: pool, Router: fr, Log: pgtest.DiscardLogger(),
		Embedder: func(context.Context, uuid.UUID) (embed.Embedder, error) { return fakeEmbedder{}, nil }}
	return &env{t: t, ctx: ctx, q: q, ws: ws.ID, rss: rss.ID, hn: hn.ID, mon: mon.ID, svc: svc, fr: fr}
}

func (e *env) item(source uuid.UUID, ext, title, body string, published time.Time, community string) uuid.UUID {
	e.t.Helper()
	sum := sha256.Sum256([]byte(strings.ToLower(title + body)))
	c := community
	id, err := e.q.InsertRawItem(e.ctx, store.InsertRawItemParams{WorkspaceID: e.ws, SourceID: source, MonitorID: &e.mon, ExternalID: ext,
		Url: "https://example.com/" + ext, Title: &title, Body: body, Community: &c, PublishedAt: &published, Metrics: []byte(`{}`), ContentHash: sum[:]})
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

// reason returns raw_items.filter_reason, or "<unprocessed>".
func (e *env) reason(id uuid.UUID) string {
	var processed bool
	var reason *string
	if err := e.svc.Pool.QueryRow(e.ctx, "SELECT processed_at IS NOT NULL, filter_reason FROM raw_items WHERE id = $1", id).Scan(&processed, &reason); err != nil {
		e.t.Fatal(err)
	}
	switch {
	case !processed:
		return "<unprocessed>"
	case reason == nil:
		return ""
	}
	return *reason
}

func TestPipeline(t *testing.T) {
	e := setup(t)
	now := time.Now()
	long := "Is it safe to let an AI agent place trades in my Trello account? I have about $5k and want to approve every order."

	// Pre-filter drops.
	neg := e.item(e.rss, "neg", "Join our telegram group", long, now, "Forum")
	stale := e.item(e.rss, "stale", "Old question", long, now.Add(-30*24*time.Hour), "Forum")
	short := e.item(e.rss, "short", "Hi", "ok", now, "Forum")
	for id, want := range map[uuid.UUID]string{neg: pipeline.ReasonNegative, stale: pipeline.ReasonStale, short: pipeline.ReasonTooShort} {
		res, err := e.svc.Process(e.ctx, e.ws, id)
		if err != nil || res.Reason != want {
			t.Fatalf("item %s: res=%+v err=%v, want %s", id, res, err, want)
		}
	}
	if len(e.fr.calls) != 0 {
		t.Fatalf("pre-filtered items must not reach the LLM: %v", e.fr.calls)
	}

	// Happy path: an inbound opportunity with blended factors.
	good := e.item(e.rss, "good", "Safe AI planning for Trello?", long, now.Add(-2*time.Hour), "Forum")
	res, err := e.svc.Process(e.ctx, e.ws, good)
	if err != nil || res.Opportunities != 1 {
		t.Fatalf("good: res=%+v err=%v", res, err)
	}
	opps, _ := e.q.ListOpportunities(e.ctx, store.ListOpportunitiesParams{WorkspaceID: e.ws, Lim: 10, MinScore: num(0), Today: date(now)})
	if len(opps) != 1 || opps[0].Lane != store.LaneInbound || opps[0].Explanation == "" {
		t.Fatalf("opportunities = %+v", opps)
	}
	var bd struct {
		Factors map[string]float64 `json:"factors"`
		Flags   []string           `json:"flags"`
	}
	_ = json.Unmarshal(opps[0].ScoreBreakdown, &bd)
	if bd.Factors["intent"] != 0.9 || bd.Factors["recency"] < 0.8 || bd.Factors["reach"] != 0.4 || len(bd.Flags) != 1 {
		t.Fatalf("breakdown = %+v", bd)
	}
	if !strings.Contains(e.fr.messages[0], "<external_content") || strings.Contains(e.fr.messages[0], "author") {
		t.Fatalf("item must be wrapped as external content: %s", e.fr.messages[0])
	}
	// Idempotent.
	if res, _ := e.svc.Process(e.ctx, e.ws, good); res.Reason != "already_processed" {
		t.Fatalf("reprocess = %+v", res)
	}

	// Exact duplicate (same content, different external id) joins the cluster.
	dup := e.item(e.rss, "dup", "Safe AI planning for Trello?", long, now.Add(-time.Hour), "Forum")
	if res, _ := e.svc.Process(e.ctx, e.ws, dup); res.Reason != pipeline.ReasonDuplicate {
		t.Fatalf("dup = %+v", res)
	}

	// Near duplicate by embedding.
	a := e.item(e.rss, "twin-a", "First twin post about AI planning", long+" A", now, "Forum")
	b := e.item(e.rss, "twin-b", "Second twin post, reworded", long+" B", now, "Forum")
	if _, err := e.svc.Process(e.ctx, e.ws, a); err != nil {
		t.Fatal(err)
	}
	if res, _ := e.svc.Process(e.ctx, e.ws, b); res.Reason != pipeline.ReasonNearDuplicate {
		t.Fatalf("near dup = %+v", res)
	}

	// Excluded venue (HN): the classifier is never offered the inbound lane.
	before := len(e.fr.messages)
	hnItem := e.item(e.hn, "hn1", "Ask HN: AI agents planning real money?", long+" HN", now, "Hacker News")
	if _, err := e.svc.Process(e.ctx, e.ws, hnItem); err != nil {
		t.Fatal(err)
	}
	cls := e.fr.messages[before]
	if !strings.Contains(cls, "Allowed lanes for this item: thought_leadership") || strings.Contains(cls, "inbound") {
		t.Fatalf("HN classify message = %s", cls)
	}

	// Below min score.
	e.fr.score = func(string) string {
		return `{"relevance":0.1,"intent":0.1,"fit":0.1,"flags":[],"explanation":"Unrelated."}`
	}
	low := e.item(e.rss, "low", "Tax question about IRAs", long+" low", now, "Forum")
	if res, _ := e.svc.Process(e.ctx, e.ws, low); res.Reason != pipeline.ReasonBelowMin {
		t.Fatalf("low = %+v", res)
	}

	// LLM failure: error returned, item stays unprocessed for the retry.
	e.fr.fail = errors.New("no usable provider")
	fail := e.item(e.rss, "fail", "Another AI planning question", long+" fail", now, "Forum")
	if _, err := e.svc.Process(e.ctx, e.ws, fail); err == nil {
		t.Fatal("expected error")
	}
	if r := e.reason(fail); r != "<unprocessed>" {
		t.Fatalf("failed item should stay unprocessed, got %q", r)
	}
}

func TestFeatureAndExpire(t *testing.T) {
	e := setup(t)
	now := time.Now()
	long := "Is it safe to let an AI agent place trades in my Trello account? Asking before I sign up for anything."
	for i, age := range []time.Duration{1 * time.Hour, 2 * time.Hour, 3 * time.Hour, 4 * time.Hour} {
		id := e.item(e.rss, "f"+string(rune('a'+i)), "AI planning question "+string(rune('A'+i)), long+string(rune('a'+i)), now.Add(-age), "Forum")
		if _, err := e.svc.Process(e.ctx, e.ws, id); err != nil {
			t.Fatal(err)
		}
	}
	today := date(now)
	if err := jobs.FeatureToday(e.ctx, e.q, e.ws, today); err != nil {
		t.Fatal(err)
	}
	if err := jobs.FeatureToday(e.ctx, e.q, e.ws, today); err != nil { // idempotent
		t.Fatal(err)
	}
	counts, _ := e.q.CountOpportunitiesByLane(e.ctx, store.CountOpportunitiesByLaneParams{WorkspaceID: e.ws, Today: today})
	for _, c := range counts {
		if c.Lane == store.LaneInbound && (c.FeaturedCount != 2 || c.NewCount != 4) {
			t.Fatalf("inbound featured=%d new=%d, want 2 (quota) of 4", c.FeaturedCount, c.NewCount)
		}
	}
	// Featured items come first in the listing.
	opps, _ := e.q.ListOpportunities(e.ctx, store.ListOpportunitiesParams{WorkspaceID: e.ws, Lim: 10, MinScore: num(0), Today: today})
	if !opps[0].FeaturedOn.Valid || !opps[1].FeaturedOn.Valid || opps[2].FeaturedOn.Valid {
		t.Fatal("featured items must sort first")
	}
	// Expire: shrink inbound freshness so everything is stale.
	var n pgtype.Numeric
	_ = n.Scan("0.5")
	if _, err := e.q.UpdateLaneConfig(e.ctx, store.UpdateLaneConfigParams{WorkspaceID: e.ws, Lane: store.LaneInbound, Weights: []byte(`{"intent":1}`),
		DailyQuota: 2, FreshnessHours: 1, MinScore: n, DraftKind: "reply"}); err != nil {
		t.Fatal(err)
	}
	expired, err := e.q.ExpireOpportunities(e.ctx, e.ws)
	if err != nil || expired != 4 {
		t.Fatalf("expired = %d err = %v", expired, err)
	}
}

func TestInboxDay(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	d, ready := jobs.InboxDay(time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC), ny) // 05:00 in New York
	if ready || d.Time.Day() != 5 {
		t.Fatalf("day=%v ready=%v", d.Time, ready)
	}
	if _, ready := jobs.InboxDay(time.Date(2026, 10, 5, 11, 0, 0, 0, time.UTC), ny); !ready {
		t.Fatal("07:00 local should be ready")
	}
}

func num(f float64) pgtype.Numeric {
	var n pgtype.Numeric
	_ = n.Scan(strconv.FormatFloat(f, 'f', 3, 64))
	return n
}

func date(t time.Time) pgtype.Date {
	return pgtype.Date{Time: time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), Valid: true}
}
