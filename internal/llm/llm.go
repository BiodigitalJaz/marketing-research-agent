// Package llm is the provider-agnostic interface every caller uses. Only
// internal/llm/anthropic and internal/llm/openaicompat import vendor SDKs.
//
// Hosted and local providers are peers: tool calls, thinking, vision, and
// structured output travel through the same types for every provider. Model
// capabilities (Capabilities) are declared in Settings and used to validate
// configuration, never to select a degraded code path.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Part kinds.
const (
	PartText             = "text"
	PartImage            = "image"
	PartThinking         = "thinking"
	PartRedactedThinking = "redacted_thinking"
)

// Part is one content block. Thinking parts must be sent back unchanged
// (including Signature) when continuing a conversation on the same model.
type Part struct {
	Kind      string `json:"kind"`
	Text      string `json:"text,omitempty"`
	Image     *Image `json:"image,omitempty"`
	Signature string `json:"signature,omitempty"` // thinking: opaque provider token
	Data      string `json:"data,omitempty"`      // redacted_thinking: opaque payload
}

// Image is vision input: inline bytes or a URL.
type Image struct {
	MediaType string `json:"media_type,omitempty"`
	Data      []byte `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

func TextPart(s string) Part { return Part{Kind: PartText, Text: s} }

type Message struct {
	Role       Role        `json:"role"`
	Parts      []Part      `json:"parts,omitempty"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`  // assistant -> tools
	ToolResult *ToolResult `json:"tool_result,omitempty"` // tool -> assistant
}

func UserText(s string) Message { return Message{Role: RoleUser, Parts: []Part{TextPart(s)}} }

type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"` // JSON Schema for arguments
}

type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type ToolResult struct {
	CallID  string `json:"call_id"`
	Content string `json:"content"`
	IsError bool   `json:"is_error,omitempty"`
}

// Thinking configures reasoning. Effort-style models take Effort
// (low|medium|high|xhigh|max); budget-style models take BudgetTokens.
// Which one a model accepts is declared in its capabilities.
type Thinking struct {
	Effort       string `json:"effort,omitempty"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
	// Summarized asks for readable thinking summaries where supported.
	Summarized bool `json:"summarized,omitempty"`
}

// Tool choice values. Forced choices are deliberately absent: current
// models reject them; steer from the prompt and validate instead.
const (
	ToolChoiceAuto = "auto"
	ToolChoiceNone = "none"
)

type ChatRequest struct {
	Model       string
	System      string
	Messages    []Message
	Tools       []ToolDef
	ToolChoice  string          // "" (auto when tools present) | auto | none
	JSONSchema  json.RawMessage // request structured output when non-nil
	SchemaName  string          // name for the schema where the API needs one
	Thinking    *Thinking
	MaxTokens   int
	Temperature *float64
	TopP        *float64
	Extra       map[string]any // provider-specific request fields set in Settings
}

type Usage struct {
	InputTokens    int `json:"input_tokens"`
	OutputTokens   int `json:"output_tokens"`
	ThinkingTokens int `json:"thinking_tokens,omitempty"`
}

// Stop reasons.
const (
	StopEnd       = "end"
	StopToolUse   = "tool_use"
	StopMaxTokens = "max_tokens"
	StopRefusal   = "refusal"
)

type ChatResponse struct {
	Model      string     `json:"model"`
	Parts      []Part     `json:"parts"` // text and thinking blocks, in order
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	StopReason string     `json:"stop_reason"`
	Usage      Usage      `json:"usage"`
}

// Text concatenates the text parts.
func (r *ChatResponse) Text() string {
	var b strings.Builder
	for _, p := range r.Parts {
		if p.Kind == PartText {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// AssistantMessage converts a response into the history entry to append
// before sending tool results (thinking parts included, unchanged).
func (r *ChatResponse) AssistantMessage() Message {
	return Message{Role: RoleAssistant, Parts: r.Parts, ToolCalls: r.ToolCalls}
}

// Event kinds for streaming.
const (
	EventTextDelta     = "text_delta"
	EventThinkingDelta = "thinking_delta"
	EventToolCall      = "tool_call"
	EventDone          = "done"
	EventError         = "error"
)

type Event struct {
	Kind string
	Text string
	Call *ToolCall
	Resp *ChatResponse // on done
	Err  error         // on error
}

// Capabilities are declared per model in Settings.
type Capabilities struct {
	Tools      bool `json:"tools"`
	Vision     bool `json:"vision"`
	JSONSchema bool `json:"json_schema"`
	// Thinking: "" (none) | "effort" | "budget".
	Thinking string `json:"thinking,omitempty"`
	// Sampling: whether temperature/top_p are accepted.
	Sampling   bool `json:"sampling"`
	MaxContext int  `json:"max_context,omitempty"`
	MaxOutput  int  `json:"max_output,omitempty"`
}

// Validate reports request fields the model has not declared support for.
func (c Capabilities) Validate(req ChatRequest) error {
	var errs []error
	if len(req.Tools) > 0 && !c.Tools {
		errs = append(errs, errors.New("model does not declare tool support"))
	}
	if req.JSONSchema != nil && !c.JSONSchema {
		errs = append(errs, errors.New("model does not declare structured output support"))
	}
	if req.Thinking != nil {
		switch {
		case c.Thinking == "":
			errs = append(errs, errors.New("model does not declare thinking support"))
		case c.Thinking == "effort" && req.Thinking.BudgetTokens > 0:
			errs = append(errs, errors.New("model takes thinking effort, not a token budget"))
		case c.Thinking == "budget" && req.Thinking.Effort != "":
			errs = append(errs, errors.New("model takes a thinking token budget, not effort"))
		}
	}
	if (req.Temperature != nil || req.TopP != nil) && !c.Sampling {
		errs = append(errs, errors.New("model does not accept temperature/top_p"))
	}
	if !c.Vision {
		for _, m := range req.Messages {
			for _, p := range m.Parts {
				if p.Kind == PartImage {
					errs = append(errs, errors.New("model does not declare vision support"))
					return errors.Join(errs...)
				}
			}
		}
	}
	return errors.Join(errs...)
}

// Provider is implemented by each adapter.
type Provider interface {
	ID() string
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
	Stream(ctx context.Context, req ChatRequest) (<-chan Event, error)
	ListModels(ctx context.Context) ([]ModelInfo, error)
}

// ModelInfo is returned by model discovery. Capabilities are a best-effort
// default the admin confirms in Settings.
type ModelInfo struct {
	ID           string        `json:"id"`
	DisplayName  string        `json:"display_name,omitempty"`
	Capabilities *Capabilities `json:"capabilities,omitempty"`
}

// APIError is a provider error normalized across adapters.
type APIError struct {
	Provider   string
	Status     int // HTTP status; 0 for transport errors
	Message    string
	RetryAfter time.Duration
	Err        error
}

func (e *APIError) Error() string {
	if e.Status == 0 {
		return fmt.Sprintf("%s: %s", e.Provider, e.Message)
	}
	return fmt.Sprintf("%s: HTTP %d: %s", e.Provider, e.Status, e.Message)
}

func (e *APIError) Unwrap() error { return e.Err }

// Failover reports whether another provider might succeed: transport
// errors, auth problems, unknown model (404), timeouts, rate limits, and
// server errors. Malformed requests (400, 413, 422) would fail anywhere.
func (e *APIError) Failover() bool {
	switch {
	case e.Status == 0, e.Status == 401, e.Status == 403, e.Status == 404, e.Status == 408, e.Status == 429, e.Status >= 500:
		return true
	}
	return false
}

// ErrNoAPIKey is returned when a hosted provider has no key configured.
var ErrNoAPIKey = errors.New("no API key configured")

// Collect drains a stream into a response; useful in tests and callers that
// only want the final message.
func Collect(ch <-chan Event) (*ChatResponse, error) {
	var resp *ChatResponse
	for ev := range ch {
		switch ev.Kind {
		case EventError:
			return nil, ev.Err
		case EventDone:
			resp = ev.Resp
		}
	}
	if resp == nil {
		return nil, errors.New("stream ended without a response")
	}
	return resp, nil
}
