package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCapabilitiesValidate(t *testing.T) {
	temp := 0.2
	img := Message{Role: RoleUser, Parts: []Part{{Kind: PartImage, Image: &Image{URL: "https://x/y.png"}}}}
	full := Capabilities{Tools: true, Vision: true, JSONSchema: true, Thinking: "effort", Sampling: true}
	tests := []struct {
		name string
		caps Capabilities
		req  ChatRequest
		want string // substring; "" = ok
	}{
		{"all supported", full, ChatRequest{Tools: []ToolDef{{Name: "t"}}, JSONSchema: json.RawMessage(`{}`), Thinking: &Thinking{Effort: "high"}, Temperature: &temp, Messages: []Message{img}}, ""},
		{"no tools", Capabilities{}, ChatRequest{Tools: []ToolDef{{Name: "t"}}}, "tool support"},
		{"no schema", Capabilities{}, ChatRequest{JSONSchema: json.RawMessage(`{}`)}, "structured output"},
		{"no thinking", Capabilities{}, ChatRequest{Thinking: &Thinking{Effort: "low"}}, "thinking support"},
		{"budget on effort model", full, ChatRequest{Thinking: &Thinking{BudgetTokens: 2048}}, "not a token budget"},
		{"effort on budget model", Capabilities{Thinking: "budget"}, ChatRequest{Thinking: &Thinking{Effort: "low"}}, "not effort"},
		{"sampling rejected", Capabilities{}, ChatRequest{Temperature: &temp}, "temperature"},
		{"no vision", Capabilities{}, ChatRequest{Messages: []Message{img}}, "vision"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.caps.Validate(tt.req)
			if tt.want == "" && err != nil {
				t.Fatalf("unexpected: %v", err)
			}
			if tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("want %q, got %v", tt.want, err)
			}
		})
	}
}

func TestAPIErrorFailover(t *testing.T) {
	for status, want := range map[int]bool{0: true, 400: false, 401: true, 404: true, 422: false, 429: true, 500: true, 529: true} {
		if got := (&APIError{Status: status}).Failover(); got != want {
			t.Errorf("status %d failover = %v", status, got)
		}
	}
}
