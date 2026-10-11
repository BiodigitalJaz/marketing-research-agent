package router

import (
	"encoding/json"
	"fmt"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm"
)

// Params are the tunable request settings stored as JSON in
// provider_models.default_params and task_routes.params. Route params
// override model defaults field by field.
type Params struct {
	Temperature *float64       `json:"temperature,omitempty"`
	TopP        *float64       `json:"top_p,omitempty"`
	MaxTokens   int            `json:"max_tokens,omitempty"`
	Thinking    *llm.Thinking  `json:"thinking,omitempty"`
	Extra       map[string]any `json:"extra,omitempty"`
	// Agent limits (research/chat loops).
	MaxSteps    int `json:"max_steps,omitempty"`
	TimeoutSec  int `json:"timeout_s,omitempty"`
	TokenBudget int `json:"token_budget,omitempty"`
}

// ParseParams decodes stored params; empty input yields zero Params.
func ParseParams(raw []byte) (Params, error) {
	var p Params
	if len(raw) == 0 {
		return p, nil
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, fmt.Errorf("decode params: %w", err)
	}
	return p, nil
}

// Merge overlays o onto p: every field set in o wins.
func (p Params) Merge(o Params) Params {
	if o.Temperature != nil {
		p.Temperature = o.Temperature
	}
	if o.TopP != nil {
		p.TopP = o.TopP
	}
	if o.MaxTokens > 0 {
		p.MaxTokens = o.MaxTokens
	}
	if o.Thinking != nil {
		t := *o.Thinking
		p.Thinking = &t
	}
	if len(o.Extra) > 0 {
		merged := map[string]any{}
		for k, v := range p.Extra {
			merged[k] = v
		}
		for k, v := range o.Extra {
			merged[k] = v
		}
		p.Extra = merged
	}
	if o.MaxSteps > 0 {
		p.MaxSteps = o.MaxSteps
	}
	if o.TimeoutSec > 0 {
		p.TimeoutSec = o.TimeoutSec
	}
	if o.TokenBudget > 0 {
		p.TokenBudget = o.TokenBudget
	}
	return p
}

// Apply sets request fields from params where the request leaves them unset.
func (p Params) Apply(req llm.ChatRequest) llm.ChatRequest {
	if req.Temperature == nil {
		req.Temperature = p.Temperature
	}
	if req.TopP == nil {
		req.TopP = p.TopP
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = p.MaxTokens
	}
	if req.Thinking == nil && p.Thinking != nil {
		t := *p.Thinking
		req.Thinking = &t
	}
	if len(p.Extra) > 0 {
		extra := map[string]any{}
		for k, v := range p.Extra {
			extra[k] = v
		}
		for k, v := range req.Extra {
			extra[k] = v
		}
		req.Extra = extra
	}
	return req
}

// Price is provider_models.price (USD per million tokens).
type Price struct {
	InputPerMTok  float64 `json:"input_per_mtok"`
	OutputPerMTok float64 `json:"output_per_mtok"`
}

// Cost computes the USD cost of a call. Thinking tokens bill as output and
// are already included in OutputTokens by both adapters' usage reports.
func (p Price) Cost(u llm.Usage) float64 {
	return float64(u.InputTokens)/1e6*p.InputPerMTok + float64(u.OutputTokens)/1e6*p.OutputPerMTok
}
