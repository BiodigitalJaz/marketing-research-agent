package research_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/agent"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/fetch"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm/router"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/prompts"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/research"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/search"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/testutil/pgtest"
)

// fakeRouter answers by phase; tests script the gather/chat turns.
type fakeRouter struct {
	mu         sync.Mutex
	resolveErr error
	handle     func(req llm.ChatRequest) *llm.ChatResponse
	reqs       []llm.ChatRequest
	caps       llm.Capabilities
}

func (f *fakeRouter) Resolve(context.Context, uuid.UUID, string) (*router.Resolved, error) {
	if f.resolveErr != nil {
		return nil, f.resolveErr
	}
	return &router.Resolved{Targets: []router.Target{{ProviderID: "fake", Model: "m", Caps: f.caps}}}, nil
}

func (f *fakeRouter) ChatTargets(_ context.Context, _ router.Call, _ []router.Target, req llm.ChatRequest) (*llm.ChatResponse, []router.Attempt, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	resp := f.handle(req)
	if resp == nil {
		return nil, nil, errors.New("unexpected call")
	}
	return resp, []router.Attempt{{}}, nil
}

func text(s string) *llm.ChatResponse {
	return &llm.ChatResponse{Parts: []llm.Part{llm.TextPart(s)}, StopReason: llm.StopEnd, Usage: llm.Usage{InputTokens: 10, OutputTokens: 10}}
}

func calls(cs ...llm.ToolCall) *llm.ChatResponse {
	return &llm.ChatResponse{ToolCalls: cs, StopReason: llm.StopToolUse}
}

func call(id, name, args string) llm.ToolCall {
	return llm.ToolCall{ID: id, Name: name, Args: json.RawMessage(args)}
}

func toolNames(req llm.ChatRequest) []string {
	var out []string
	for _, t := range req.Tools {
		out = append(out, t.Name)
	}
	return out
}

func lastToolResult(req llm.ChatRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if tr := req.Messages[i].ToolResult; tr != nil {
			return tr.Content
		}
	}
	return ""
}

type fakeFetch struct{}

func (fakeFetch) Article(_ context.Context, u string) (*fetch.Page, error) {
	return &fetch.Page{URL: u, Title: "Vendor API limits explained", Text: "Read-only vendor APIs push builders to scrape. Approval mode helps."}, nil
}
func (fakeFetch) Image(context.Context, string) ([]byte, string, error) {
	return nil, "", errors.New("no images")
}

type env struct {
	t    *testing.T
	ctx  context.Context
	pool *pgxpool.Pool
	q    *store.Queries
	ws   uuid.UUID
	user uuid.UUID
	fr   *fakeRouter
	svc  *research.Service
}

func setup(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	pool := pgtest.NewMigratedDB(t)
	q := store.New(pool)
	ws, _ := q.CreateWorkspace(ctx, "Benchwright")
	u, err := q.CreateUser(ctx, store.CreateUserParams{Email: "r@example.com", Name: "R", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	defs, _ := prompts.Defaults()
	for _, d := range defs {
		if err := q.InsertDefaultPromptIfMissing(ctx, store.InsertDefaultPromptIfMissingParams{Task: d.Task, Version: d.Version, Body: d.Body}); err != nil {
			t.Fatal(err)
		}
	}
	var n pgtype.Numeric
	_ = n.Scan("0.5")
	for _, l := range []store.Lane{store.LaneInbound, store.LaneThoughtLeadership} {
		_ = q.InsertLaneConfigIfMissing(ctx, store.InsertLaneConfigIfMissingParams{WorkspaceID: ws.ID, Lane: l, Weights: []byte(`{"intent":1}`), DailyQuota: 3, FreshnessHours: 48, MinScore: n, DraftKind: "reply"})
	}
	src, _ := q.CreateSource(ctx, store.CreateSourceParams{WorkspaceID: ws.ID, Connector: "rss", Name: "Forum", Config: []byte(`{"feed_url":"https://x"}`), Enabled: true})
	for i, it := range []struct{ title, body string }{
		{"Is a Trello planning bot safe?", "I want an AI agent that moves cards on Trello but I am worried it will go rogue."},
		{"Trello API read only", "The official Trello API cannot place orders, so people scrape."},
		{"Unrelated cooking post", "How long do I boil an egg?"},
	} {
		sum := sha256.Sum256([]byte(it.title))
		title, now := it.title, time.Now()
		c := "forum.example"
		rid, err := q.InsertRawItem(ctx, store.InsertRawItemParams{WorkspaceID: ws.ID, SourceID: src.ID, ExternalID: it.title, Url: "https://forum.example/q/" + string(rune('a'+i)),
			Title: &title, Body: it.body, Community: &c, PublishedAt: &now, Metrics: []byte(`{}`), ContentHash: sum[:]})
		if err != nil {
			t.Fatal(err)
		}
		if i < 2 {
			var s pgtype.Numeric
			_ = s.Scan("0.8")
			lane := store.LaneInbound
			if i == 1 {
				lane = store.LaneThoughtLeadership
			}
			if _, err := q.InsertOpportunity(ctx, store.InsertOpportunityParams{WorkspaceID: ws.ID, RawItemID: rid, Lane: lane, OppType: "pain_point", Score: s, ScoreBreakdown: []byte(`{}`), Explanation: "x"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	fr := &fakeRouter{caps: llm.Capabilities{Tools: true}}
	svc := &research.Service{Pool: pool, Router: fr, Fetch: fakeFetch{}, Log: pgtest.DiscardLogger(), RetentionDays: 90, PollInterval: 20 * time.Millisecond,
		Search: func(_ context.Context, _ uuid.UUID, q search.Query) ([]search.Result, error) {
			return []search.Result{{URL: "https://blog.example/vendor-apis", Title: "Vendor API limits", Snippet: "Why read-only APIs matter"}, {URL: "https://news.example/a", Title: "News"}}, nil
		}}
	return &env{t: t, ctx: ctx, pool: pool, q: q, ws: ws.ID, user: u.ID, fr: fr, svc: svc}
}

func (e *env) job(mode string, useWeb bool, question string) uuid.UUID {
	e.t.Helper()
	tpl := "pain_points"
	j, err := e.q.CreateResearchJob(e.ctx, store.CreateResearchJobParams{WorkspaceID: e.ws, CreatedBy: e.user, Mode: mode, Template: &tpl, Question: question, UseWeb: useWeb})
	if err != nil {
		e.t.Fatal(err)
	}
	if mode == "chat" {
		if _, err := e.q.InsertChatMessage(e.ctx, store.InsertChatMessageParams{WorkspaceID: e.ws, JobID: j.ID, Role: "user", Content: question, Sources: []byte(`[]`)}); err != nil {
			e.t.Fatal(err)
		}
	}
	return j.ID
}

func (e *env) steps(job uuid.UUID) []store.ResearchStep {
	e.t.Helper()
	s, err := e.q.ListResearchSteps(e.ctx, store.ListResearchStepsParams{WorkspaceID: e.ws, JobID: job, AfterSeq: 0})
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

func kinds(steps []store.ResearchStep) []string {
	var out []string
	for i, s := range steps {
		if int(s.Seq) != i+1 {
			panic("steps must be numbered 1..n")
		}
		out = append(out, s.Kind)
	}
	return out
}

var markerRe = regexp.MustCompile(`\[(\d+)\]`)

func toolResults(req llm.ChatRequest) string {
	var b strings.Builder
	for _, m := range req.Messages {
		if m.ToolResult != nil {
			b.WriteString(m.ToolResult.Content + "\n")
		}
	}
	return b.String()
}

// numberOf finds the source number a tool result gave a title.
func numberOf(t *testing.T, results, title string) string {
	t.Helper()
	m := regexp.MustCompile(`\[(\d+)\] ` + regexp.QuoteMeta(title)).FindStringSubmatch(results)
	if m == nil {
		t.Fatalf("no source titled %q in:\n%s", title, results)
	}
	return m[1]
}

func TestDeepResearch(t *testing.T) {
	e := setup(t)
	job := e.job("deep", true, "What worries Trello users about AI planning agents?")
	gather := 0
	var corpusN, blogN string
	e.fr.handle = func(req llm.ChatRequest) *llm.ChatResponse {
		switch {
		case req.SchemaName == "research_plan":
			return text(`{"title":"AI planning agent worries","sub_questions":["What do users fear?","What do vendor APIs allow?"]}`)
		case req.SchemaName == "research_verify":
			// The verifier sees what each cited source said, not just its title.
			in := req.Messages[0].Parts[0].Text
			if !strings.Contains(in, "Sources and excerpts:") || !strings.Contains(in, "Read-only vendor APIs push builders to scrape.") || !strings.Contains(in, "go rogue") {
				t.Errorf("verify input lacks source excerpts:\n%s", in)
			}
			return text(`{"claims":[{"claim":"Users fear a rogue agent","sources":[` + corpusN + `]},{"claim":"Read-only APIs push scraping","sources":[` + blogN + `,99]},{"claim":"Invented","sources":[42]}],` +
				`"unsupported":[{"claim":"Vendors will open write APIs next year","sources":[` + blogN + `],"note":"the page says nothing about plans"}]}`)
		case strings.Contains(req.System, "You write the final research report"):
			if !strings.Contains(req.Messages[0].Parts[0].Text, "{{chart:c1}}") || !strings.Contains(req.Messages[0].Parts[0].Text, "(single source)") {
				t.Errorf("synthesis input lacks charts or single-source marks:\n%s", req.Messages[0].Parts[0].Text)
			}
			if !strings.Contains(req.Messages[0].Parts[0].Text, "Not supported by their sources:\n- Vendors will open write APIs next year") {
				t.Errorf("synthesis input lacks unsupported claims:\n%s", req.Messages[0].Parts[0].Text)
			}
			if len(req.Messages) == 1 { // cut off by the output limit, then continued
				return &llm.ChatResponse{Parts: []llm.Part{llm.TextPart("Users fear rogue agents [" + corpusN + "], single source. APIs are ")}, StopReason: llm.StopMaxTokens}
			}
			if !strings.Contains(req.Messages[2].Parts[0].Text, "Continue exactly") {
				t.Errorf("continuation prompt: %+v", req.Messages[2])
			}
			return text("read-only [" + blogN + "].\n\n{{chart:c1}}\n\nUnbacked [77]. {{chart:c9}}")
		}
		// Gather turns.
		gather++
		switch gather {
		case 1:
			if !slices.Contains(toolNames(req), "web_search") || !slices.Contains(toolNames(req), "fetch_url") {
				t.Errorf("web tools must be offered when use_web is on: %v", toolNames(req))
			}
			if slices.Contains(toolNames(req), "view_image") {
				t.Error("view_image needs a vision model")
			}
			return calls(call("a", "search_corpus", `{"query":"trello agent"}`), call("b", "opportunity_stats", `{"group_by":"lane"}`))
		case 2:
			ds := regexp.MustCompile(`Dataset (d\d+), last`).FindStringSubmatch(lastToolResult(req))
			corpusN = numberOf(t, toolResults(req), "Is a Trello planning bot safe?")
			if ds == nil {
				t.Fatalf("stats result: %s", lastToolResult(req))
			}
			return calls(call("c", "make_chart", `{"dataset":"`+ds[1]+`","type":"bar","title":"Opportunities by lane","x":"lane","y":["opportunities"]}`))
		case 3:
			return text("Users fear rogue agents [" + corpusN + "]. {{chart:c1}}")
		case 4:
			return calls(call("d", "web_search", `{"query":"vendor api read only"}`))
		case 5:
			blogN = numberOf(t, toolResults(req), "Vendor API limits")
			return calls(call("e", "fetch_url", `{"source":`+blogN+`}`))
		case 6:
			if !strings.Contains(lastToolResult(req), "["+blogN+"] Vendor API limits explained") {
				t.Errorf("fetched page keeps its source number: %s", lastToolResult(req))
			}
			return text("Read-only APIs push scraping [" + blogN + "].")
		}
		return nil
	}
	if err := e.svc.Run(e.ctx, e.ws, job); err != nil {
		t.Fatal(err)
	}
	j, _ := e.q.GetResearchJob(e.ctx, store.GetResearchJobParams{WorkspaceID: e.ws, ID: job})
	if j.Status != store.JobStatusSucceeded || j.FinishedAt == nil {
		t.Fatalf("job = %s %v err=%v", j.Status, j.FinishedAt, j.Error)
	}
	got := strings.Join(kinds(e.steps(job)), ",")
	for _, want := range []string{"plan", "phase", "tool_call", "tool_result", "note", "verify", "report", "done"} {
		if !strings.Contains(got, want) {
			t.Errorf("steps lack %q: %s", want, got)
		}
	}
	reps, _ := e.q.ListReports(e.ctx, store.ListReportsParams{WorkspaceID: e.ws, Limit: 5})
	if len(reps) != 1 || reps[0].Title != "AI planning agent worries" {
		t.Fatalf("reports = %+v", reps)
	}
	rep, _ := e.q.GetReport(e.ctx, store.GetReportParams{WorkspaceID: e.ws, ID: reps[0].ID})
	cites, _ := e.q.ListCitations(e.ctx, store.ListCitationsParams{WorkspaceID: e.ws, ReportID: rep.ID})
	known := map[string]store.Citation{}
	for _, c := range cites {
		known[c.Marker] = c
	}
	// Every [n] in the report resolves to a stored citation.
	for _, m := range markerRe.FindAllString(rep.Markdown, -1) {
		if _, ok := known[m]; !ok {
			t.Errorf("marker %s has no citation:\n%s", m, rep.Markdown)
		}
	}
	if !strings.Contains(rep.Markdown, "APIs are read-only") {
		t.Errorf("a cut-off report must be continued:\n%s", rep.Markdown)
	}
	if strings.Contains(rep.Markdown, "[77]") || strings.Contains(rep.Markdown, "chart:c9") || !strings.HasPrefix(rep.Markdown, "# AI planning agent worries") {
		t.Errorf("report:\n%s", rep.Markdown)
	}
	if !strings.Contains(rep.Markdown, "## Key claims and evidence") || !strings.Contains(rep.Markdown, "*single source*") || strings.Contains(rep.Markdown, "Invented") {
		t.Errorf("claims section:\n%s", rep.Markdown)
	}
	if !strings.Contains(rep.Markdown, "## Not supported by the cited sources") || !regexp.MustCompile(`Vendors will open write APIs next year \(cited \[\d+\]\) — the page says nothing about plans`).MatchString(rep.Markdown) {
		t.Errorf("unsupported section:\n%s", rep.Markdown)
	}
	if len(cites) != 2 {
		t.Errorf("citations = %d, want 2 (corpus item, fetched page)", len(cites))
	}
	var fetched bool
	for _, c := range cites {
		if c.Url == "https://blog.example/vendor-apis" && c.RawItemID != nil {
			fetched = true
		}
	}
	if !fetched {
		t.Error("the fetched page must be cited by its stored raw item")
	}
	// Chart data came from the tool result (the database), not the model.
	var charts []struct {
		ID   string           `json:"id"`
		Data []map[string]any `json:"data"`
	}
	_ = json.Unmarshal(rep.Charts, &charts)
	if len(charts) != 1 || charts[0].ID != "c1" || len(charts[0].Data) != 2 {
		t.Fatalf("charts = %s", rep.Charts)
	}
	var stored int
	_ = e.pool.QueryRow(e.ctx, `SELECT count(*) FROM raw_items r JOIN sources s ON s.id = r.source_id WHERE s.connector = 'research_fetch' AND r.processed_at IS NOT NULL AND r.filter_reason = 'research'`).Scan(&stored)
	if stored != 1 {
		t.Errorf("fetched pages stored = %d, want 1 (and kept out of the pipeline)", stored)
	}
}

func TestChatResearch(t *testing.T) {
	e := setup(t)
	job := e.job("chat", false, "Are people worried about planning bots?")
	turn := 0
	e.fr.handle = func(req llm.ChatRequest) *llm.ChatResponse {
		if slices.Contains(toolNames(req), "web_search") || slices.Contains(toolNames(req), "fetch_url") {
			t.Errorf("web tools offered with use_web off: %v", toolNames(req))
		}
		turn++
		switch turn {
		case 1:
			return calls(call("a", "search_corpus", `{"query":"trello bot"}`))
		case 2:
			return text("Yes, one user worries the agent will go rogue [1] [5].")
		case 3:
			if len(req.Messages) != 3 || req.Messages[1].Role != llm.RoleAssistant {
				t.Errorf("follow-up must include the history: %d messages", len(req.Messages))
			}
			return calls(call("b", "search_corpus", `{"query":"bot worries"}`))
		case 4:
			return text("No new evidence.")
		}
		return nil
	}
	if err := e.svc.Run(e.ctx, e.ws, job); err != nil {
		t.Fatal(err)
	}
	msgs, _ := e.q.ListChatMessages(e.ctx, store.ListChatMessagesParams{WorkspaceID: e.ws, JobID: job})
	if len(msgs) != 2 || msgs[1].Role != "assistant" || strings.Contains(msgs[1].Content, "[5]") || !strings.Contains(msgs[1].Content, "[1]") {
		t.Fatalf("messages = %+v", msgs)
	}
	var sources []map[string]any
	_ = json.Unmarshal(msgs[1].Sources, &sources)
	if len(sources) != 1 || sources[0]["marker"] != "[1]" {
		t.Errorf("sources = %s", msgs[1].Sources)
	}
	// A follow-up turn.
	if _, err := e.q.RequeueResearchJob(e.ctx, store.RequeueResearchJobParams{WorkspaceID: e.ws, ID: job}); err != nil {
		t.Fatal(err)
	}
	_, _ = e.q.InsertChatMessage(e.ctx, store.InsertChatMessageParams{WorkspaceID: e.ws, JobID: job, Role: "user", Content: "More?", Sources: []byte(`[]`)})
	if err := e.svc.Run(e.ctx, e.ws, job); err != nil {
		t.Fatal(err)
	}
	msgs, _ = e.q.ListChatMessages(e.ctx, store.ListChatMessagesParams{WorkspaceID: e.ws, JobID: job})
	if len(msgs) != 4 || msgs[3].Content != "No new evidence." {
		t.Fatalf("after follow-up: %+v", msgs)
	}
}

func TestResearchCancelAndFailure(t *testing.T) {
	e := setup(t)
	t.Run("cancelled before start", func(t *testing.T) {
		job := e.job("chat", true, "q")
		_, _ = e.q.CancelResearchJob(e.ctx, store.CancelResearchJobParams{WorkspaceID: e.ws, ID: job})
		e.fr.handle = func(llm.ChatRequest) *llm.ChatResponse { t.Error("a cancelled job must not run"); return nil }
		if err := e.svc.Run(e.ctx, e.ws, job); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("cancelled while running", func(t *testing.T) {
		job := e.job("chat", true, "q")
		e.fr.handle = func(llm.ChatRequest) *llm.ChatResponse {
			_, _ = e.q.CancelResearchJob(e.ctx, store.CancelResearchJobParams{WorkspaceID: e.ws, ID: job})
			time.Sleep(60 * time.Millisecond)
			return calls(call("a", "search_corpus", `{"query":"x"}`))
		}
		if err := e.svc.Run(e.ctx, e.ws, job); err != nil {
			t.Fatal(err)
		}
		j, _ := e.q.GetResearchJob(e.ctx, store.GetResearchJobParams{WorkspaceID: e.ws, ID: job})
		steps := kinds(e.steps(job))
		if j.Status != store.JobStatusCancelled || steps[len(steps)-1] != "done" {
			t.Errorf("status = %s steps = %v", j.Status, steps)
		}
	})
	t.Run("no route", func(t *testing.T) {
		job := e.job("deep", true, "q")
		e.fr.resolveErr = router.ErrNoRoute
		defer func() { e.fr.resolveErr = nil }()
		if err := e.svc.Run(e.ctx, e.ws, job); err != nil {
			t.Fatal(err)
		}
		j, _ := e.q.GetResearchJob(e.ctx, store.GetResearchJobParams{WorkspaceID: e.ws, ID: job})
		if j.Status != store.JobStatusFailed || j.Error == nil {
			t.Errorf("job = %s %v", j.Status, j.Error)
		}
	})
	t.Run("limits force a final answer", func(t *testing.T) {
		job := e.job("chat", false, "q")
		n := 0
		e.fr.handle = func(req llm.ChatRequest) *llm.ChatResponse {
			n++
			if req.ToolChoice == llm.ToolChoiceNone {
				return text("Final from what I have.")
			}
			return calls(call("x", "search_corpus", `{"query":"trello"}`))
		}
		if err := e.svc.Run(e.ctx, e.ws, job); err != nil {
			t.Fatal(err)
		}
		msgs, _ := e.q.ListChatMessages(e.ctx, store.ListChatMessagesParams{WorkspaceID: e.ws, JobID: job})
		if msgs[len(msgs)-1].Content != "Final from what I have." || n != research.ChatMaxSteps+1 {
			t.Errorf("calls = %d, last = %q", n, msgs[len(msgs)-1].Content)
		}
		if !slices.Contains(kinds(e.steps(job)), "limit") {
			t.Error("a limit step must be recorded")
		}
	})
}

// With a small context window, verify and synthesis inputs must be
// condensed to fit (the 8k-window failure seen with a 27B local model).
func TestDeepResearchFitsContext(t *testing.T) {
	e := setup(t)
	e.fr.caps = llm.Capabilities{Tools: true, MaxContext: 6000}
	job := e.job("deep", false, "What worries Trello users about AI planning agents?")
	long := strings.Repeat("Users say an agent could move cards they did not approve, which worries them a lot. ", 120)
	var corpusN string
	gather := 0
	var condensed int
	fits := func(req llm.ChatRequest) {
		t.Helper()
		size := agent.EstimateText(req.System)
		for _, m := range req.Messages {
			for _, p := range m.Parts {
				size += agent.EstimateText(p.Text)
			}
		}
		if limit := 6000 - 2048 + 300; size > limit {
			t.Errorf("%s request is %d tokens, over %d", req.SchemaName, size, limit)
		}
	}
	e.fr.handle = func(req llm.ChatRequest) *llm.ChatResponse {
		switch {
		case strings.HasPrefix(req.System, "You condense research notes"):
			condensed++
			return text("Users fear unapproved changes [" + corpusN + "].")
		case req.SchemaName == "research_plan":
			return text(`{"title":"Worries","sub_questions":["A?","B?","C?"]}`)
		case req.SchemaName == "research_verify":
			fits(req)
			return text(`{"claims":[{"claim":"Users fear unapproved changes","sources":[` + corpusN + `]}],"unsupported":[]}`)
		case strings.Contains(req.System, "You write the final research report"):
			fits(req)
			return text("Users fear unapproved changes [" + corpusN + "].")
		}
		gather++
		if gather%2 == 1 {
			return calls(call("s", "search_corpus", `{"query":"trello bot"}`))
		}
		corpusN = numberOf(t, toolResults(req), "Is a Trello planning bot safe?")
		return text(long + " [" + corpusN + "]")
	}
	if err := e.svc.Run(e.ctx, e.ws, job); err != nil {
		t.Fatal(err)
	}
	j, _ := e.q.GetResearchJob(e.ctx, store.GetResearchJobParams{WorkspaceID: e.ws, ID: job})
	if j.Status != store.JobStatusSucceeded {
		t.Fatalf("job = %s %v", j.Status, j.Error)
	}
	if condensed == 0 {
		t.Error("long notes must be condensed")
	}
}
