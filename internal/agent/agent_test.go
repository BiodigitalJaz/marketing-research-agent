package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm/router"
)

func TestNotebookCite(t *testing.T) {
	nb := NewNotebook()
	id := uuid.New()
	a := nb.Cite(Source{Kind: "web", URL: "https://www.Example.com/a/#top", Title: "A"})
	b := nb.Cite(Source{Kind: "corpus", RawItemID: &id, URL: "https://x.com/b"})
	if a != 1 || b != 2 {
		t.Fatalf("numbers = %d %d", a, b)
	}
	// Same URL (normalized) keeps its number and gains the stored page id.
	page := uuid.New()
	if n := nb.Cite(Source{Kind: "page", RawItemID: &page, URL: "https://example.com/a"}); n != 1 {
		t.Fatalf("refetch = %d, want 1", n)
	}
	if s, _ := nb.Source(1); s.RawItemID == nil || *s.RawItemID != page || s.Kind != "page" {
		t.Errorf("source 1 not upgraded: %+v", s)
	}
	if n := nb.Cite(Source{RawItemID: &id, URL: "https://other.example/b"}); n != 2 {
		t.Errorf("same item, different URL = %d, want 2", n)
	}
	if _, ok := nb.Source(9); ok {
		t.Error("unknown source")
	}
}

func TestMakeChart(t *testing.T) {
	nb := NewNotebook()
	d := nb.AddDataset("opportunity_stats(lane)", []string{"key", "count"}, []map[string]any{{"key": "inbound", "count": 3}, {"key": "media", "count": 1}})
	c, err := nb.MakeChart(d, "bar", "By lane", "key", []string{"count"}, 1)
	if err != nil || c.ID != "c1" || len(c.Data) != 1 || c.Data[0]["key"] != "inbound" {
		t.Fatalf("chart = %+v, %v", c, err)
	}
	for _, tt := range []struct {
		ds, typ, x string
		y          []string
	}{
		{"d9", "bar", "key", []string{"count"}},
		{d, "radar", "key", []string{"count"}},
		{d, "bar", "nope", []string{"count"}},
		{d, "bar", "key", []string{"invented"}},
		{d, "line", "key", nil},
	} {
		if _, err := nb.MakeChart(tt.ds, tt.typ, "t", tt.x, tt.y, 0); err == nil {
			t.Errorf("MakeChart(%v) should fail", tt)
		}
	}
}

func TestFinalize(t *testing.T) {
	id := uuid.New()
	sources := []Source{{N: 1, URL: "https://a"}, {N: 2, URL: "https://b", RawItemID: &id}, {N: 3, URL: "https://c"}}
	charts := []Chart{{ID: "c1", Title: "Lanes"}, {ID: "c2"}}
	in := "Intro cites [3] then [1, 2] and [3] again [7] [d2].\n\n{{chart:c2}}\n\n{{ chart:c9 }}\n\nSee [the docs](https://x) and [2](https://y).\n\n```\nkeep [5] {{chart:c1}}\n```"
	f := Finalize(in, sources, charts)
	want := "Intro cites [1] then [2][3] and [1] again.\n\n{{chart:c2}}\n\n\n\nSee [the docs](https://x) and [2](https://y).\n\n```\nkeep [5] {{chart:c1}}\n```"
	if f.Text != want {
		t.Errorf("text:\n%s\nwant:\n%s", f.Text, want)
	}
	if len(f.Cited) != 3 || f.Cited[0].Marker != "[1]" || f.Cited[0].Source.URL != "https://c" || f.Cited[1].Source.URL != "https://a" || f.Cited[2].Source.RawItemID == nil {
		t.Errorf("cited = %+v", f.Cited)
	}
	if len(f.Charts) != 1 || f.Charts[0].ID != "c2" {
		t.Errorf("charts = %+v (only referenced charts are kept)", f.Charts)
	}
	if strings.Join(f.Dropped, " ") != "[d2] [7] {{ chart:c9 }}" {
		t.Errorf("dropped = %q", f.Dropped)
	}
	if !strings.Contains(SourceList(f.Cited), "[1] https://c — https://c") {
		t.Errorf("source list: %s", SourceList(f.Cited))
	}
}

// scripted answers each call from a list of responses.
type scripted struct {
	mu    sync.Mutex
	resps []*llm.ChatResponse
	reqs  []llm.ChatRequest
}

func (s *scripted) ChatTargets(_ context.Context, _ router.Call, _ []router.Target, req llm.ChatRequest) (*llm.ChatResponse, []router.Attempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, req)
	if len(s.resps) == 0 {
		return nil, nil, errors.New("no more responses")
	}
	r := s.resps[0]
	s.resps = s.resps[1:]
	return r, []router.Attempt{{RunID: uuid.New()}}, nil
}

func toolCall(id, name, args string) *llm.ChatResponse {
	return &llm.ChatResponse{ToolCalls: []llm.ToolCall{{ID: id, Name: name, Args: json.RawMessage(args)}}, StopReason: llm.StopToolUse, Usage: llm.Usage{InputTokens: 100, OutputTokens: 10}}
}

func answer(text string) *llm.ChatResponse {
	return &llm.ChatResponse{Parts: []llm.Part{llm.TextPart(text)}, StopReason: llm.StopEnd, Usage: llm.Usage{InputTokens: 100, OutputTokens: 50}}
}

func echoTools() *Registry {
	r := NewRegistry()
	r.Add(Tool{Source: "builtin", Def: llm.ToolDef{Name: "echo", Schema: json.RawMessage(`{"type":"object"}`)},
		Handler: func(_ context.Context, args json.RawMessage) (Output, error) {
			return Output{Text: "echo " + string(args), Summary: "echoed"}, nil
		}})
	r.Add(Tool{Source: "builtin", Def: llm.ToolDef{Name: "fail"}, Handler: func(context.Context, json.RawMessage) (Output, error) { return Output{}, errors.New("boom") }})
	r.Add(Tool{Source: "builtin", Def: llm.ToolDef{Name: "slow"}, Handler: func(ctx context.Context, _ json.RawMessage) (Output, error) {
		<-ctx.Done()
		return Output{}, ctx.Err()
	}})
	r.Add(Tool{Source: "builtin", Def: llm.ToolDef{Name: "look"}, Vision: true, Handler: func(context.Context, json.RawMessage) (Output, error) {
		return Output{Text: "image attached", Images: []llm.Image{{MediaType: "image/png", Data: []byte{1}}}}, nil
	}})
	r.Add(Tool{Source: "builtin", Def: llm.ToolDef{Name: "web"}, Web: true, Handler: func(context.Context, json.RawMessage) (Output, error) { return Output{Text: "web"}, nil }})
	r.Add(Tool{Source: "mcp:docs", Def: llm.ToolDef{Name: "mcp__docs__search"}, Handler: func(context.Context, json.RawMessage) (Output, error) { return Output{Text: "mcp"}, nil }})
	return r
}

func TestRegistryFilter(t *testing.T) {
	r := echoTools()
	got := r.Filter(Policy{Builtins: []string{"echo", "web", "look"}, UseWeb: false, Vision: false}).Names()
	if strings.Join(got, ",") != "echo,mcp__docs__search" {
		t.Errorf("filtered = %v", got)
	}
	if n := r.Filter(Policy{UseWeb: true, Vision: true}).Len(); n != r.Len() {
		t.Errorf("empty allowlist keeps every builtin: %d of %d", n, r.Len())
	}
	if got := r.Filter(Policy{UseWeb: true, Vision: true, Exclude: []string{"echo", "mcp__docs__search"}}).Names(); slices.Contains(got, "echo") || !slices.Contains(got, "mcp__docs__search") {
		t.Errorf("exclude removes built-ins only: %v", got)
	}
}

type recorder struct {
	mu    sync.Mutex
	kinds []string
}

func (r *recorder) record(_ context.Context, kind string, _ any, _ *uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kinds = append(r.kinds, kind)
}

func TestLoopToolsAndAnswer(t *testing.T) {
	s := &scripted{resps: []*llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{ID: "1", Name: "echo", Args: json.RawMessage(`{"q":"a"}`)}, {ID: "2", Name: "fail"}, {ID: "3", Name: "nope"}, {ID: "4", Name: "look"}, {ID: "5", Name: "slow"}},
			Parts: []llm.Part{{Kind: llm.PartThinking, Text: "I should look things up."}, llm.TextPart("Checking.")}, StopReason: llm.StopToolUse},
		answer("Done [1]."),
	}}
	rec := &recorder{}
	l := &Loop{Router: s, Tools: echoTools(), MaxSteps: 4, Record: rec.record, ToolTimeout: 50 * time.Millisecond}
	res, err := l.Run(context.Background(), "sys", []llm.Message{llm.UserText("q")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Done [1]." || res.Stopped != "" || res.Steps != 2 || res.RunID == nil {
		t.Fatalf("result = %+v", res)
	}
	second := s.reqs[1].Messages
	// user, assistant (with thinking kept), 5 tool results in order, user with image
	if len(second) != 8 || second[1].Parts[0].Kind != llm.PartThinking || second[7].Parts[1].Kind != llm.PartImage {
		t.Fatalf("history = %d messages: %+v", len(second), second)
	}
	for i, want := range []struct {
		id    string
		isErr bool
		has   string
	}{{"1", false, `echo {"q":"a"}`}, {"2", true, "boom"}, {"3", true, "Unknown tool"}, {"4", false, "image attached"}, {"5", true, "deadline"}} {
		tr := second[2+i].ToolResult
		if tr.CallID != want.id || tr.IsError != want.isErr || !strings.Contains(tr.Content, want.has) {
			t.Errorf("result %d = %+v", i, tr)
		}
	}
	joined := strings.Join(rec.kinds, ",")
	if !strings.HasPrefix(joined, "thought_summary,assistant,tool_call") || strings.Count(joined, "tool_result") != 5 {
		t.Errorf("steps = %s", joined)
	}
}

func TestLoopLimits(t *testing.T) {
	loopFor := func(s *scripted) *Loop { return &Loop{Router: s, Tools: echoTools(), MaxSteps: 2} }
	t.Run("max steps forces a final answer without tools", func(t *testing.T) {
		s := &scripted{resps: []*llm.ChatResponse{toolCall("1", "echo", `{}`), toolCall("2", "echo", `{}`), answer("Final.")}}
		res, err := loopFor(s).Run(context.Background(), "sys", []llm.Message{llm.UserText("q")})
		if err != nil || res.Stopped != StopMaxSteps || res.Text != "Final." {
			t.Fatalf("res = %+v err = %v", res, err)
		}
		last := s.reqs[len(s.reqs)-1]
		if last.ToolChoice != llm.ToolChoiceNone || len(last.Tools) == 0 {
			t.Error("the final call must disable tool use (tools stay defined for the history)")
		}
	})
	t.Run("token budget", func(t *testing.T) {
		s := &scripted{resps: []*llm.ChatResponse{toolCall("1", "echo", `{}`), answer("Final.")}}
		l := loopFor(s)
		l.Budget = &Budget{Limit: 50}
		res, err := l.Run(context.Background(), "sys", []llm.Message{llm.UserText("q")})
		if err != nil || res.Stopped != StopBudget || l.Budget.Used() == 0 {
			t.Fatalf("res = %+v err = %v", res, err)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		s := &scripted{resps: []*llm.ChatResponse{answer("Final.")}}
		l := loopFor(s)
		l.Deadline = time.Now().Add(-time.Second)
		res, err := l.Run(context.Background(), "sys", []llm.Message{llm.UserText("q")})
		if err != nil || res.Stopped != StopDeadline || len(s.reqs) != 1 {
			t.Fatalf("res = %+v err = %v calls=%d", res, err, len(s.reqs))
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		l := loopFor(&scripted{})
		l.Cancelled = func(context.Context) bool { return true }
		if _, err := l.Run(context.Background(), "sys", nil); !errors.Is(err, ErrCancelled) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestCompact(t *testing.T) {
	big := strings.Repeat("evidence [1] ", 400)
	msgs := []llm.Message{
		llm.UserText("q"),
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "1", Name: "echo"}}},
		{Role: llm.RoleTool, ToolResult: &llm.ToolResult{CallID: "1", Content: big}},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "2", Name: "echo"}}},
		{Role: llm.RoleTool, ToolResult: &llm.ToolResult{CallID: "2", Content: big}},
	}
	var summarized int
	l := &Loop{MaxContext: 4000, OutputReserve: 1000, Summarize: func(_ context.Context, s string) (string, error) { summarized++; return "summary keeping [1]", nil }}
	l.compact(context.Background(), "sys", nil, msgs)
	if summarized != 1 || !strings.HasPrefix(msgs[2].ToolResult.Content, compactedPrefix+"summary") {
		t.Errorf("older result not condensed: %q", msgs[2].ToolResult.Content[:60])
	}
	if msgs[4].ToolResult.Content != big {
		t.Error("the latest turn's results are kept while the request fits")
	}
}

func TestCompactHardLimit(t *testing.T) {
	huge := strings.Repeat("evidence [2] ", 3000) // ~13k tokens
	msgs := []llm.Message{
		llm.UserText("q"),
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "1", Name: "echo"}, {ID: "2", Name: "echo"}}},
		{Role: llm.RoleTool, ToolResult: &llm.ToolResult{CallID: "1", Content: huge}},
		{Role: llm.RoleTool, ToolResult: &llm.ToolResult{CallID: "2", Content: "small"}},
	}
	l := &Loop{MaxContext: 8192, OutputReserve: 3072}
	defs := echoTools().Defs()
	l.compact(context.Background(), strings.Repeat("s", 3000), defs, msgs)
	total := EstimateText(strings.Repeat("s", 3000)) + EstimateDefs(defs) + EstimateTokens(msgs)
	if total > 8192-3072-256 {
		t.Errorf("request still %d tokens after compaction", total)
	}
	if !strings.HasPrefix(msgs[2].ToolResult.Content, compactedPrefix+"evidence [2]") || msgs[3].ToolResult.Content != "small" {
		t.Errorf("the oversized latest result must be trimmed, keeping its start: %q / %q", msgs[2].ToolResult.Content[:50], msgs[3].ToolResult.Content)
	}
}

func TestRequireTool(t *testing.T) {
	s := &scripted{resps: []*llm.ChatResponse{answer("From memory [4]."), toolCall("1", "echo", `{}`), answer("With evidence [1].")}}
	l := &Loop{Router: s, Tools: echoTools(), MaxSteps: 5, RequireTool: true}
	res, err := l.Run(context.Background(), "sys", []llm.Message{llm.UserText("q")})
	if err != nil || res.Text != "With evidence [1]." || len(s.reqs) != 3 {
		t.Fatalf("res = %+v err = %v calls = %d", res, err, len(s.reqs))
	}
	if !strings.Contains(s.reqs[1].Messages[len(s.reqs[1].Messages)-1].Parts[0].Text, "not used any tools") {
		t.Error("the model must be nudged to search")
	}
	// Only once: a second tool-less answer is accepted.
	s = &scripted{resps: []*llm.ChatResponse{answer("a"), answer("b")}}
	l = &Loop{Router: s, Tools: echoTools(), MaxSteps: 5, RequireTool: true}
	if res, _ := l.Run(context.Background(), "sys", nil); res.Text != "b" {
		t.Errorf("text = %q", res.Text)
	}
}

func TestDropUnknown(t *testing.T) {
	got, n := DropUnknown("A [1], B [4] and [1, 9] see [2](https://x). Counts [d3][d5].", []Source{{N: 1}, {N: 2}})
	if got != "A [1], B and [1] see [2](https://x). Counts." || n != 4 {
		t.Errorf("got %q, dropped %d", got, n)
	}
}

// Regression: when trimming every result still leaves the request too big
// (large system prompt and tool definitions), compaction must end rather
// than re-trim the same result forever.
func TestCompactTerminatesWhenNothingFits(t *testing.T) {
	var msgs []llm.Message
	for i := 0; i < 12; i++ {
		id := string(rune('a' + i))
		msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: "echo"}}},
			llm.Message{Role: llm.RoleTool, ToolResult: &llm.ToolResult{CallID: id, Content: strings.Repeat("result text ", 300)}})
	}
	l := &Loop{MaxContext: 8192, OutputReserve: 3072}
	done := make(chan struct{})
	go func() {
		l.compact(context.Background(), strings.Repeat("system prompt ", 1500), echoTools().Defs(), msgs)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("compaction did not terminate")
	}
	for _, m := range msgs {
		if m.ToolResult != nil && utf8.RuneCountInString(resultBody(m.ToolResult.Content)) > 200 {
			t.Errorf("result not trimmed to the tightest level: %d", len(m.ToolResult.Content))
		}
	}
}

func TestRepeatedCallsAreNotRerun(t *testing.T) {
	var runs int
	reg := NewRegistry()
	reg.Add(Tool{Source: "builtin", Def: llm.ToolDef{Name: "find"}, Handler: func(context.Context, json.RawMessage) (Output, error) { runs++; return Output{Text: "nothing"}, nil }})
	s := &scripted{resps: []*llm.ChatResponse{
		toolCall("1", "find", `{"q":"x","n":1}`),
		toolCall("2", "find", `{"n":1, "q":"x"}`), // same call, different key order
		toolCall("3", "find", `{"q":"y"}`),
		answer("done"),
	}}
	l := &Loop{Router: s, Tools: reg, MaxSteps: 6}
	if _, err := l.Run(context.Background(), "sys", []llm.Message{llm.UserText("q")}); err != nil {
		t.Fatal(err)
	}
	if runs != 2 {
		t.Errorf("tool ran %d times, want 2 (the repeat is answered without running)", runs)
	}
	if got := s.reqs[2].Messages[len(s.reqs[2].Messages)-1].ToolResult.Content; got != repeatNotice {
		t.Errorf("repeat result = %q", got)
	}
}
