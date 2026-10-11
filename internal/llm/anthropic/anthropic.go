// Package anthropic adapts the Claude Messages API (official Go SDK) to the
// llm.Provider interface.
package anthropic

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm"
)

// DefaultMaxTokens applies when a request sets none.
const DefaultMaxTokens = 16000

// Config configures a provider instance.
type Config struct {
	ID         string // model_providers.id
	APIKey     string
	BaseURL    string // optional; tests and proxies
	MaxRetries int    // SDK retries on 408/409/429/5xx and connection errors (honors retry-after); 0 = 3, <0 = none
	HTTPClient *http.Client
}

type Provider struct {
	id     string
	client sdk.Client
	hasKey bool
}

// New builds a provider. A missing key is reported on first use, not here,
// so the router can surface it per task.
func New(cfg Config) *Provider {
	retries := cfg.MaxRetries
	switch {
	case retries == 0:
		retries = 3
	case retries < 0:
		retries = 0
	}
	opts := []option.RequestOption{option.WithAPIKey(cfg.APIKey), option.WithMaxRetries(retries)}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	if cfg.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
	}
	return &Provider{id: cfg.ID, client: sdk.NewClient(opts...), hasKey: cfg.APIKey != ""}
}

func (p *Provider) ID() string { return p.id }

func (p *Provider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	params, opts, err := p.build(req)
	if err != nil {
		return nil, err
	}
	msg, err := p.client.Messages.New(ctx, params, opts...)
	if err != nil {
		return nil, p.wrap(err)
	}
	return fromMessage(msg), nil
}

func (p *Provider) Stream(ctx context.Context, req llm.ChatRequest) (<-chan llm.Event, error) {
	params, opts, err := p.build(req)
	if err != nil {
		return nil, err
	}
	stream := p.client.Messages.NewStreaming(ctx, params, opts...)
	out := make(chan llm.Event, 16)
	go func() {
		defer close(out)
		defer func() { _ = stream.Close() }()
		var msg sdk.Message
		for stream.Next() {
			ev := stream.Current()
			if err := msg.Accumulate(ev); err != nil {
				out <- llm.Event{Kind: llm.EventError, Err: fmt.Errorf("accumulate stream: %w", err)}
				return
			}
			if d, ok := ev.AsAny().(sdk.ContentBlockDeltaEvent); ok {
				switch dv := d.Delta.AsAny().(type) {
				case sdk.TextDelta:
					out <- llm.Event{Kind: llm.EventTextDelta, Text: dv.Text}
				case sdk.ThinkingDelta:
					out <- llm.Event{Kind: llm.EventThinkingDelta, Text: dv.Thinking}
				}
			}
		}
		if err := stream.Err(); err != nil {
			out <- llm.Event{Kind: llm.EventError, Err: p.wrap(err)}
			return
		}
		resp := fromMessage(&msg)
		for i := range resp.ToolCalls {
			out <- llm.Event{Kind: llm.EventToolCall, Call: &resp.ToolCalls[i]}
		}
		out <- llm.Event{Kind: llm.EventDone, Resp: resp}
	}()
	return out, nil
}

func (p *Provider) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	if !p.hasKey {
		return nil, p.noKey()
	}
	var out []llm.ModelInfo
	iter := p.client.Models.ListAutoPaging(ctx, sdk.ModelListParams{})
	for iter.Next() {
		m := iter.Current()
		caps := discoveredCaps(m)
		out = append(out, llm.ModelInfo{ID: m.ID, DisplayName: m.DisplayName, Capabilities: &caps})
	}
	if err := iter.Err(); err != nil {
		return nil, p.wrap(err)
	}
	return out, nil
}

// discoveredCaps maps the Models API capability report to our declared
// capabilities. Sampling is not reported; effort-era models reject it.
func discoveredCaps(m sdk.ModelInfo) llm.Capabilities {
	c := m.Capabilities
	caps := llm.Capabilities{
		Tools:      true,
		Vision:     c.ImageInput.Supported,
		JSONSchema: c.StructuredOutputs.Supported,
		MaxContext: int(m.MaxInputTokens),
		MaxOutput:  int(m.MaxTokens),
	}
	switch {
	case c.Effort.Supported:
		caps.Thinking = "effort"
	case c.Thinking.Supported:
		caps.Thinking = "budget"
		caps.Sampling = true
	default:
		caps.Sampling = true
	}
	return caps
}

func (p *Provider) noKey() error {
	return &llm.APIError{Provider: p.id, Status: http.StatusUnauthorized, Message: llm.ErrNoAPIKey.Error(), Err: llm.ErrNoAPIKey}
}

func (p *Provider) build(req llm.ChatRequest) (sdk.MessageNewParams, []option.RequestOption, error) {
	if !p.hasKey {
		return sdk.MessageNewParams{}, nil, p.noKey()
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	msgs, err := toMessages(req.Messages)
	if err != nil {
		return sdk.MessageNewParams{}, nil, err
	}
	params := sdk.MessageNewParams{
		Model:     req.Model,
		MaxTokens: int64(maxTokens),
		Messages:  msgs,
	}
	if req.System != "" {
		params.System = []sdk.TextBlockParam{{Text: req.System}}
	}
	for _, t := range req.Tools {
		tp, err := toTool(t)
		if err != nil {
			return sdk.MessageNewParams{}, nil, err
		}
		params.Tools = append(params.Tools, sdk.ToolUnionParam{OfTool: &tp})
	}
	if req.ToolChoice == llm.ToolChoiceNone && len(req.Tools) > 0 {
		params.ToolChoice = sdk.ToolChoiceUnionParam{OfNone: &sdk.ToolChoiceNoneParam{}}
	}
	if req.JSONSchema != nil {
		var schema map[string]any
		if err := json.Unmarshal(req.JSONSchema, &schema); err != nil {
			return sdk.MessageNewParams{}, nil, fmt.Errorf("json schema: %w", err)
		}
		params.OutputConfig.Format = sdk.JSONOutputFormatParam{Schema: schema}
	}
	if t := req.Thinking; t != nil {
		switch {
		case t.BudgetTokens > 0:
			params.Thinking = sdk.ThinkingConfigParamOfEnabled(int64(t.BudgetTokens))
		default:
			adaptive := sdk.ThinkingConfigAdaptiveParam{}
			if t.Summarized {
				adaptive.Display = sdk.ThinkingConfigAdaptiveDisplaySummarized
			}
			params.Thinking = sdk.ThinkingConfigParamUnion{OfAdaptive: &adaptive}
			if t.Effort != "" {
				params.OutputConfig.Effort = sdk.OutputConfigEffort(t.Effort)
			}
		}
	}
	if req.Temperature != nil {
		params.Temperature = sdk.Float(*req.Temperature)
	}
	if req.TopP != nil {
		params.TopP = sdk.Float(*req.TopP)
	}
	var opts []option.RequestOption
	for k, v := range req.Extra {
		opts = append(opts, option.WithJSONSet(k, v))
	}
	return params, opts, nil
}

func toTool(t llm.ToolDef) (sdk.ToolParam, error) {
	var schema map[string]any
	if len(t.Schema) > 0 {
		if err := json.Unmarshal(t.Schema, &schema); err != nil {
			return sdk.ToolParam{}, fmt.Errorf("tool %s schema: %w", t.Name, err)
		}
	}
	in := sdk.ToolInputSchemaParam{ExtraFields: map[string]any{}}
	for k, v := range schema {
		switch k {
		case "type":
		case "properties":
			in.Properties = v
		case "required":
			if arr, ok := v.([]any); ok {
				for _, r := range arr {
					if s, ok := r.(string); ok {
						in.Required = append(in.Required, s)
					}
				}
			}
		default:
			in.ExtraFields[k] = v
		}
	}
	tp := sdk.ToolParam{Name: t.Name, InputSchema: in}
	if t.Description != "" {
		tp.Description = sdk.String(t.Description)
	}
	return tp, nil
}

// toMessages converts history. Consecutive tool results are merged into a
// single user message, as the API expects for parallel tool calls.
func toMessages(in []llm.Message) ([]sdk.MessageParam, error) {
	var out []sdk.MessageParam
	var pendingResults []sdk.ContentBlockParamUnion
	flush := func() {
		if len(pendingResults) > 0 {
			out = append(out, sdk.NewUserMessage(pendingResults...))
			pendingResults = nil
		}
	}
	for _, m := range in {
		switch m.Role {
		case llm.RoleTool:
			if m.ToolResult == nil {
				return nil, errors.New("tool message without result")
			}
			r := m.ToolResult
			pendingResults = append(pendingResults, sdk.NewToolResultBlock(r.CallID, r.Content, r.IsError))
		case llm.RoleUser:
			flush()
			blocks, err := toBlocks(m.Parts)
			if err != nil {
				return nil, err
			}
			out = append(out, sdk.NewUserMessage(blocks...))
		case llm.RoleAssistant:
			flush()
			blocks, err := toBlocks(m.Parts)
			if err != nil {
				return nil, err
			}
			for _, c := range m.ToolCalls {
				var input any = map[string]any{}
				if len(c.Args) > 0 {
					input = c.Args
				}
				blocks = append(blocks, sdk.NewToolUseBlock(c.ID, input, c.Name))
			}
			out = append(out, sdk.NewAssistantMessage(blocks...))
		default:
			return nil, fmt.Errorf("unknown role %q", m.Role)
		}
	}
	flush()
	return out, nil
}

func toBlocks(parts []llm.Part) ([]sdk.ContentBlockParamUnion, error) {
	var blocks []sdk.ContentBlockParamUnion
	for _, p := range parts {
		switch p.Kind {
		case llm.PartText:
			if p.Text != "" {
				blocks = append(blocks, sdk.NewTextBlock(p.Text))
			}
		case llm.PartImage:
			if p.Image == nil {
				return nil, errors.New("image part without image")
			}
			if p.Image.URL != "" {
				blocks = append(blocks, sdk.NewImageBlock(sdk.URLImageSourceParam{URL: p.Image.URL}))
			} else {
				blocks = append(blocks, sdk.NewImageBlockBase64(p.Image.MediaType, base64.StdEncoding.EncodeToString(p.Image.Data)))
			}
		case llm.PartThinking:
			blocks = append(blocks, sdk.NewThinkingBlock(p.Signature, p.Text))
		case llm.PartRedactedThinking:
			blocks = append(blocks, sdk.NewRedactedThinkingBlock(p.Data))
		default:
			return nil, fmt.Errorf("unknown part kind %q", p.Kind)
		}
	}
	return blocks, nil
}

func fromMessage(m *sdk.Message) *llm.ChatResponse {
	resp := &llm.ChatResponse{
		Model: m.Model,
		Usage: llm.Usage{
			InputTokens:  int(m.Usage.InputTokens + m.Usage.CacheCreationInputTokens + m.Usage.CacheReadInputTokens),
			OutputTokens: int(m.Usage.OutputTokens),
		},
	}
	for _, block := range m.Content {
		switch b := block.AsAny().(type) {
		case sdk.TextBlock:
			resp.Parts = append(resp.Parts, llm.TextPart(b.Text))
		case sdk.ThinkingBlock:
			resp.Parts = append(resp.Parts, llm.Part{Kind: llm.PartThinking, Text: b.Thinking, Signature: b.Signature})
		case sdk.RedactedThinkingBlock:
			resp.Parts = append(resp.Parts, llm.Part{Kind: llm.PartRedactedThinking, Data: b.Data})
		case sdk.ToolUseBlock:
			args := json.RawMessage(b.JSON.Input.Raw())
			if len(args) == 0 {
				args = json.RawMessage(`{}`)
			}
			resp.ToolCalls = append(resp.ToolCalls, llm.ToolCall{ID: b.ID, Name: b.Name, Args: args})
		}
	}
	switch m.StopReason {
	case sdk.StopReasonToolUse:
		resp.StopReason = llm.StopToolUse
	case sdk.StopReasonMaxTokens:
		resp.StopReason = llm.StopMaxTokens
	case sdk.StopReasonRefusal:
		resp.StopReason = llm.StopRefusal
	default:
		resp.StopReason = llm.StopEnd
	}
	return resp
}

func (p *Provider) wrap(err error) error {
	var apiErr *sdk.Error
	if errors.As(err, &apiErr) {
		e := &llm.APIError{Provider: p.id, Status: apiErr.StatusCode, Message: apiErr.Error(), Err: err}
		if apiErr.Response != nil {
			if s, perr := strconv.Atoi(apiErr.Response.Header.Get("retry-after")); perr == nil {
				e.RetryAfter = time.Duration(s) * time.Second
			}
		}
		return e
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &llm.APIError{Provider: p.id, Message: err.Error(), Err: err}
}
