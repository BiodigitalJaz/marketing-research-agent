// Package contract is the shared behavioral test suite every llm.Provider
// adapter must pass. Each adapter supplies a fake HTTP server per scenario
// that asserts the wire request it received and replies with that
// provider's wire format; the suite asserts provider-neutral results.
package contract

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm"
)

// Scenario names. Fixtures for each are provider-specific.
const (
	Text          = "text"           // plain chat with a system prompt
	ParallelTools = "parallel_tools" // two tool calls in one turn
	ToolResults   = "tool_results"   // history with tool calls + results is sent correctly
	Thinking      = "thinking"       // thinking returned, then round-tripped unchanged
	Vision        = "vision"         // image input
	Structured    = "structured"     // JSON schema output
	StreamText    = "stream_text"    // streamed text deltas
	StreamTools   = "stream_tools"   // streamed tool call
	RateLimited   = "rate_limited"   // 429 surfaces as APIError with Failover
	Refusal       = "refusal"        // refusal stop reason (where the provider has one)
)

// Fixtures returns, for a scenario, an http.Handler that validates the
// request (calling t.Errorf on mismatch) and writes the wire response.
// Returning nil skips the scenario for that provider.
type Fixtures func(t *testing.T, scenario string) http.Handler

// NewProvider builds the adapter pointed at baseURL.
type NewProvider func(baseURL string) llm.Provider

// SchemaJSON is the structured-output schema used by the Structured scenario.
const SchemaJSON = `{"type":"object","required":["answer"],"additionalProperties":false,"properties":{"answer":{"type":"string"}}}`

// WeatherTool is the tool definition fixtures expect.
var WeatherTool = llm.ToolDef{
	Name:        "get_weather",
	Description: "Get the weather for a city",
	Schema:      json.RawMessage(`{"type":"object","required":["city"],"properties":{"city":{"type":"string"}}}`),
}

// Run executes every scenario.
func Run(t *testing.T, newProvider NewProvider, fixtures Fixtures) {
	t.Helper()
	run := func(name string, fn func(t *testing.T, p llm.Provider)) {
		t.Run(name, func(t *testing.T) {
			h := fixtures(t, name)
			if h == nil {
				t.Skip("scenario not applicable to this provider")
			}
			srv := httptest.NewServer(h)
			defer srv.Close()
			fn(t, newProvider(srv.URL))
		})
	}
	ctx := func(t *testing.T) context.Context {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		t.Cleanup(cancel)
		return c
	}

	run(Text, func(t *testing.T, p llm.Provider) {
		resp, err := p.Chat(ctx(t), llm.ChatRequest{Model: "m", System: "Be brief.", Messages: []llm.Message{llm.UserText("Hello")}, MaxTokens: 100})
		mustNoErr(t, err)
		if resp.Text() != "Hi there." || resp.StopReason != llm.StopEnd {
			t.Errorf("text = %q stop = %q", resp.Text(), resp.StopReason)
		}
		if resp.Usage.InputTokens == 0 || resp.Usage.OutputTokens == 0 {
			t.Errorf("usage not reported: %+v", resp.Usage)
		}
	})

	run(ParallelTools, func(t *testing.T, p llm.Provider) {
		resp, err := p.Chat(ctx(t), llm.ChatRequest{Model: "m", Messages: []llm.Message{llm.UserText("Weather in Paris and Rome?")}, Tools: []llm.ToolDef{WeatherTool}})
		mustNoErr(t, err)
		if resp.StopReason != llm.StopToolUse || len(resp.ToolCalls) != 2 {
			t.Fatalf("stop = %q calls = %+v", resp.StopReason, resp.ToolCalls)
		}
		cities := []string{}
		for _, c := range resp.ToolCalls {
			var a struct{ City string }
			if err := json.Unmarshal(c.Args, &a); err != nil || c.Name != "get_weather" || c.ID == "" {
				t.Errorf("bad call %+v: %v", c, err)
			}
			cities = append(cities, a.City)
		}
		if strings.Join(cities, ",") != "Paris,Rome" {
			t.Errorf("cities = %v", cities)
		}
	})

	run(ToolResults, func(t *testing.T, p llm.Provider) {
		history := []llm.Message{
			llm.UserText("Weather in Paris and Rome?"),
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
				{ID: "call_1", Name: "get_weather", Args: json.RawMessage(`{"city":"Paris"}`)},
				{ID: "call_2", Name: "get_weather", Args: json.RawMessage(`{"city":"Rome"}`)},
			}},
			{Role: llm.RoleTool, ToolResult: &llm.ToolResult{CallID: "call_1", Content: "18C"}},
			{Role: llm.RoleTool, ToolResult: &llm.ToolResult{CallID: "call_2", Content: "unavailable", IsError: true}},
		}
		resp, err := p.Chat(ctx(t), llm.ChatRequest{Model: "m", Messages: history, Tools: []llm.ToolDef{WeatherTool}})
		mustNoErr(t, err)
		if resp.Text() != "Paris is 18C." {
			t.Errorf("text = %q", resp.Text())
		}
	})

	run(Thinking, func(t *testing.T, p llm.Provider) {
		req := llm.ChatRequest{Model: "m", Messages: []llm.Message{llm.UserText("Weather in Paris?")}, Tools: []llm.ToolDef{WeatherTool}, Thinking: &llm.Thinking{Effort: "high", Summarized: true}}
		resp, err := p.Chat(ctx(t), req)
		mustNoErr(t, err)
		var th *llm.Part
		for i := range resp.Parts {
			if resp.Parts[i].Kind == llm.PartThinking {
				th = &resp.Parts[i]
			}
		}
		if th == nil || th.Text == "" {
			t.Fatalf("no thinking part in %+v", resp.Parts)
		}
		// Second turn: thinking must be sent back unchanged (fixture asserts).
		req.Messages = append(req.Messages, resp.AssistantMessage(),
			llm.Message{Role: llm.RoleTool, ToolResult: &llm.ToolResult{CallID: resp.ToolCalls[0].ID, Content: "18C"}})
		resp2, err := p.Chat(ctx(t), req)
		mustNoErr(t, err)
		if resp2.Text() != "18C in Paris." {
			t.Errorf("second turn text = %q", resp2.Text())
		}
	})

	run(Vision, func(t *testing.T, p llm.Provider) {
		msg := llm.Message{Role: llm.RoleUser, Parts: []llm.Part{
			{Kind: llm.PartImage, Image: &llm.Image{MediaType: "image/png", Data: []byte("\x89PNG-fake")}},
			llm.TextPart("What is this?"),
		}}
		resp, err := p.Chat(ctx(t), llm.ChatRequest{Model: "m", Messages: []llm.Message{msg}})
		mustNoErr(t, err)
		if resp.Text() != "A chart." {
			t.Errorf("text = %q", resp.Text())
		}
	})

	run(Structured, func(t *testing.T, p llm.Provider) {
		resp, err := p.Chat(ctx(t), llm.ChatRequest{Model: "m", Messages: []llm.Message{llm.UserText("Answer in JSON")}, JSONSchema: json.RawMessage(SchemaJSON), SchemaName: "answer"})
		mustNoErr(t, err)
		var out struct{ Answer string }
		if err := json.Unmarshal([]byte(resp.Text()), &out); err != nil || out.Answer != "42" {
			t.Errorf("structured text = %q: %v", resp.Text(), err)
		}
	})

	run(StreamText, func(t *testing.T, p llm.Provider) {
		ch, err := p.Stream(ctx(t), llm.ChatRequest{Model: "m", Messages: []llm.Message{llm.UserText("Hello")}})
		mustNoErr(t, err)
		var deltas []string
		var final *llm.ChatResponse
		for ev := range ch {
			switch ev.Kind {
			case llm.EventTextDelta:
				deltas = append(deltas, ev.Text)
			case llm.EventDone:
				final = ev.Resp
			case llm.EventError:
				t.Fatal(ev.Err)
			}
		}
		if len(deltas) < 2 || strings.Join(deltas, "") != "Hi there." || final == nil || final.Text() != "Hi there." {
			t.Errorf("deltas = %q final = %+v", deltas, final)
		}
	})

	run(StreamTools, func(t *testing.T, p llm.Provider) {
		ch, err := p.Stream(ctx(t), llm.ChatRequest{Model: "m", Messages: []llm.Message{llm.UserText("Weather in Paris?")}, Tools: []llm.ToolDef{WeatherTool}})
		mustNoErr(t, err)
		var calls []llm.ToolCall
		var final *llm.ChatResponse
		for ev := range ch {
			switch ev.Kind {
			case llm.EventToolCall:
				calls = append(calls, *ev.Call)
			case llm.EventDone:
				final = ev.Resp
			case llm.EventError:
				t.Fatal(ev.Err)
			}
		}
		if len(calls) != 1 || calls[0].Name != "get_weather" || final == nil || final.StopReason != llm.StopToolUse {
			t.Fatalf("calls = %+v final = %+v", calls, final)
		}
		var a struct{ City string }
		if err := json.Unmarshal(calls[0].Args, &a); err != nil || a.City != "Paris" {
			t.Errorf("streamed args %s: %v", calls[0].Args, err)
		}
	})

	run(RateLimited, func(t *testing.T, p llm.Provider) {
		_, err := p.Chat(ctx(t), llm.ChatRequest{Model: "m", Messages: []llm.Message{llm.UserText("Hello")}})
		var apiErr *llm.APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests || !apiErr.Failover() {
			t.Fatalf("want 429 APIError, got %v", err)
		}
	})

	run(Refusal, func(t *testing.T, p llm.Provider) {
		resp, err := p.Chat(ctx(t), llm.ChatRequest{Model: "m", Messages: []llm.Message{llm.UserText("x")}})
		mustNoErr(t, err)
		if resp.StopReason != llm.StopRefusal {
			t.Errorf("stop = %q, want refusal", resp.StopReason)
		}
	})
}

func mustNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// DecodeBody reads a JSON request body into a generic map for assertions.
func DecodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		t.Errorf("decode request body: %v", err)
	}
	return m
}

// WriteJSON writes v as a JSON response.
func WriteJSON(w http.ResponseWriter, v string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(v))
}

// WriteSSE writes server-sent events, each a (event, data) pair; event may be "".
func WriteSSE(w http.ResponseWriter, events [][2]string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		if e[0] != "" {
			_, _ = w.Write([]byte("event: " + e[0] + "\n"))
		}
		_, _ = w.Write([]byte("data: " + e[1] + "\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

// Path returns the dotted path's value in a decoded JSON map (array indexes
// as numbers), or nil.
func Path(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			v = m[k]
		case int:
			a, ok := v.([]any)
			if !ok || k >= len(a) {
				return nil
			}
			v = a[k]
		}
	}
	return v
}
