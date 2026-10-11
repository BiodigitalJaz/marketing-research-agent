package router

import (
	"math"
	"testing"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm"
)

func f(v float64) *float64 { return &v }

func TestMergeAndApply(t *testing.T) {
	model := Params{Temperature: f(0.7), MaxTokens: 4000, Extra: map[string]any{"a": 1, "b": 1}}
	route := Params{MaxTokens: 8000, Thinking: &llm.Thinking{Effort: "high"}, Extra: map[string]any{"b": 2}, MaxSteps: 8}
	m := model.Merge(route)
	if *m.Temperature != 0.7 || m.MaxTokens != 8000 || m.Thinking.Effort != "high" || m.Extra["a"] != 1 || m.Extra["b"] != 2 || m.MaxSteps != 8 {
		t.Fatalf("merge = %+v", m)
	}
	// Merging must not alias the route's thinking struct.
	route.Thinking.Effort = "low"
	if m.Thinking.Effort != "high" {
		t.Fatal("merge aliased thinking")
	}

	req := m.Apply(llm.ChatRequest{MaxTokens: 100, Extra: map[string]any{"b": 3}})
	if req.MaxTokens != 100 || *req.Temperature != 0.7 || req.Thinking.Effort != "high" || req.Extra["b"] != 3 || req.Extra["a"] != 1 {
		t.Fatalf("apply = %+v", req)
	}
}

func TestParseParams(t *testing.T) {
	p, err := ParseParams([]byte(`{"temperature":0.1,"thinking":{"budget_tokens":2048},"max_steps":40}`))
	if err != nil || *p.Temperature != 0.1 || p.Thinking.BudgetTokens != 2048 || p.MaxSteps != 40 {
		t.Fatalf("parse = %+v %v", p, err)
	}
	if _, err := ParseParams([]byte(`{"temperature":"hot"}`)); err == nil {
		t.Fatal("expected error")
	}
	if p, err := ParseParams(nil); err != nil || p.MaxTokens != 0 {
		t.Fatal("empty params")
	}
}

func TestPriceCost(t *testing.T) {
	c := Price{InputPerMTok: 4, OutputPerMTok: 20}.Cost(llm.Usage{InputTokens: 1_000_000, OutputTokens: 500_000})
	if math.Abs(c-14) > 1e-9 {
		t.Fatalf("cost = %v", c)
	}
}
