// Package openaicompat adapts any OpenAI-compatible Chat Completions
// endpoint (OpenAI, vLLM, llama.cpp server, LM Studio, Ollama, SGLang) to the
// llm.Provider interface using the official openai-go SDK.
package openaicompat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/respjson"
	"github.com/openai/openai-go/v3/shared"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm"
)

// Reasoning styles: how thinking is requested from the endpoint.
const (
	// ReasoningEffort sends reasoning_effort (OpenAI, Ollama, LM Studio).
	ReasoningEffort = "reasoning_effort"
	// ReasoningChatTemplate sends chat_template_kwargs.enable_thinking (vLLM,
	// SGLang, llama.cpp with Qwen-style templates).
	ReasoningChatTemplate = "chat_template"
)

type Config struct {
	ID             string
	BaseURL        string
	APIKey         string // optional for local servers
	Local          bool
	ReasoningStyle string // ReasoningEffort (default) | ReasoningChatTemplate
	// MaxTokensParam is "max_completion_tokens" or "max_tokens". Default:
	// max_completion_tokens for api.openai.com, max_tokens elsewhere (the
	// field every local server understands).
	MaxTokensParam string
	MaxRetries     int // 0 = 3, <0 = none
	Headers        map[string]string
	HTTPClient     *http.Client
}

type Provider struct {
	cfg    Config
	client openai.Client
}

func New(cfg Config) *Provider {
	retries := cfg.MaxRetries
	switch {
	case retries == 0:
		retries = 3
	case retries < 0:
		retries = 0
	}
	key := cfg.APIKey
	if key == "" {
		key = "not-needed" // local servers ignore it; the SDK requires a value
	}
	opts := []option.RequestOption{option.WithBaseURL(cfg.BaseURL), option.WithAPIKey(key), option.WithMaxRetries(retries),
		option.WithMiddleware(normalizeErrorBody)}
	for k, v := range cfg.Headers {
		opts = append(opts, option.WithHeader(k, v))
	}
	if cfg.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
	}
	if cfg.MaxTokensParam == "" {
		cfg.MaxTokensParam = "max_tokens"
		if strings.Contains(cfg.BaseURL, "api.openai.com") {
			cfg.MaxTokensParam = "max_completion_tokens"
		}
	}
	return &Provider{cfg: cfg, client: openai.NewClient(opts...)}
}

func (p *Provider) ID() string { return p.cfg.ID }

func (p *Provider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	params, opts, err := p.build(req)
	if err != nil {
		return nil, err
	}
	cc, err := p.client.Chat.Completions.New(ctx, params, opts...)
	if err != nil {
		return nil, p.wrap(err)
	}
	if len(cc.Choices) == 0 {
		return nil, &llm.APIError{Provider: p.cfg.ID, Status: 0, Message: "response has no choices"}
	}
	ch := cc.Choices[0]
	resp := &llm.ChatResponse{
		Model: cc.Model,
		Usage: llm.Usage{InputTokens: int(cc.Usage.PromptTokens), OutputTokens: int(cc.Usage.CompletionTokens),
			ThinkingTokens: int(cc.Usage.CompletionTokensDetails.ReasoningTokens)},
	}
	if r := reasoningText(ch.Message.JSON.ExtraFields); r != "" {
		resp.Parts = append(resp.Parts, llm.Part{Kind: llm.PartThinking, Text: r})
	}
	if ch.Message.Content != "" {
		resp.Parts = append(resp.Parts, llm.TextPart(ch.Message.Content))
	}
	for _, tc := range ch.Message.ToolCalls {
		resp.ToolCalls = append(resp.ToolCalls, llm.ToolCall{ID: tc.ID, Name: tc.Function.Name, Args: args(tc.Function.Arguments)})
	}
	resp.StopReason = stopReason(ch.FinishReason, ch.Message.Refusal, len(resp.ToolCalls))
	return resp, nil
}

func (p *Provider) Stream(ctx context.Context, req llm.ChatRequest) (<-chan llm.Event, error) {
	params, opts, err := p.build(req)
	if err != nil {
		return nil, err
	}
	params.StreamOptions = openai.ChatCompletionStreamOptionsParam{IncludeUsage: openai.Bool(true)}
	stream := p.client.Chat.Completions.NewStreaming(ctx, params, opts...)
	out := make(chan llm.Event, 16)
	go func() {
		defer close(out)
		defer func() { _ = stream.Close() }()
		acc := openai.ChatCompletionAccumulator{}
		var reasoning strings.Builder
		for stream.Next() {
			chunk := stream.Current()
			acc.AddChunk(chunk)
			if len(chunk.Choices) == 0 {
				continue
			}
			d := chunk.Choices[0].Delta
			if r := reasoningText(d.JSON.ExtraFields); r != "" {
				reasoning.WriteString(r)
				out <- llm.Event{Kind: llm.EventThinkingDelta, Text: r}
			}
			if d.Content != "" {
				out <- llm.Event{Kind: llm.EventTextDelta, Text: d.Content}
			}
		}
		if err := stream.Err(); err != nil {
			out <- llm.Event{Kind: llm.EventError, Err: p.wrap(err)}
			return
		}
		resp := &llm.ChatResponse{Model: acc.Model, Usage: llm.Usage{
			InputTokens: int(acc.Usage.PromptTokens), OutputTokens: int(acc.Usage.CompletionTokens),
			ThinkingTokens: int(acc.Usage.CompletionTokensDetails.ReasoningTokens)}}
		if reasoning.Len() > 0 {
			resp.Parts = append(resp.Parts, llm.Part{Kind: llm.PartThinking, Text: reasoning.String()})
		}
		var finish, refusal string
		if len(acc.Choices) > 0 {
			m := acc.Choices[0].Message
			finish, refusal = acc.Choices[0].FinishReason, m.Refusal
			if m.Content != "" {
				resp.Parts = append(resp.Parts, llm.TextPart(m.Content))
			}
			for _, tc := range m.ToolCalls {
				resp.ToolCalls = append(resp.ToolCalls, llm.ToolCall{ID: tc.ID, Name: tc.Function.Name, Args: args(tc.Function.Arguments)})
			}
		}
		resp.StopReason = stopReason(finish, refusal, len(resp.ToolCalls))
		for i := range resp.ToolCalls {
			out <- llm.Event{Kind: llm.EventToolCall, Call: &resp.ToolCalls[i]}
		}
		out <- llm.Event{Kind: llm.EventDone, Resp: resp}
	}()
	return out, nil
}

func (p *Provider) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	var out []llm.ModelInfo
	iter := p.client.Models.ListAutoPaging(ctx)
	for iter.Next() {
		out = append(out, llm.ModelInfo{ID: iter.Current().ID})
	}
	if err := iter.Err(); err != nil {
		return nil, p.wrap(err)
	}
	return out, nil
}

func (p *Provider) build(req llm.ChatRequest) (openai.ChatCompletionNewParams, []option.RequestOption, error) {
	msgs, err := toMessages(req.System, req.Messages)
	if err != nil {
		return openai.ChatCompletionNewParams{}, nil, err
	}
	params := openai.ChatCompletionNewParams{Model: req.Model, Messages: msgs}
	var opts []option.RequestOption
	if req.MaxTokens > 0 {
		if p.cfg.MaxTokensParam == "max_completion_tokens" {
			params.MaxCompletionTokens = openai.Int(int64(req.MaxTokens))
		} else {
			params.MaxTokens = openai.Int(int64(req.MaxTokens))
		}
	}
	for _, t := range req.Tools {
		var schema map[string]any
		if len(t.Schema) > 0 {
			if err := json.Unmarshal(t.Schema, &schema); err != nil {
				return params, nil, fmt.Errorf("tool %s schema: %w", t.Name, err)
			}
		}
		fn := shared.FunctionDefinitionParam{Name: t.Name, Parameters: schema}
		if t.Description != "" {
			fn.Description = openai.String(t.Description)
		}
		params.Tools = append(params.Tools, openai.ChatCompletionFunctionTool(fn))
	}
	if req.ToolChoice == llm.ToolChoiceNone && len(req.Tools) > 0 {
		params.ToolChoice = openai.ChatCompletionToolChoiceOptionUnionParam{OfAuto: openai.String(string(openai.ChatCompletionToolChoiceOptionAutoNone))}
	}
	if req.JSONSchema != nil {
		var schema map[string]any
		if err := json.Unmarshal(req.JSONSchema, &schema); err != nil {
			return params, nil, fmt.Errorf("json schema: %w", err)
		}
		name := req.SchemaName
		if name == "" {
			name = "output"
		}
		params.ResponseFormat = openai.ChatCompletionNewParamsResponseFormatUnion{OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{
			JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{Name: name, Schema: schema, Strict: openai.Bool(true)},
		}}
	}
	if t := req.Thinking; t != nil {
		switch p.cfg.ReasoningStyle {
		case ReasoningChatTemplate:
			opts = append(opts, option.WithJSONSet("chat_template_kwargs", map[string]any{"enable_thinking": true}))
		default:
			effort := t.Effort
			if effort == "" {
				effort = "medium"
			}
			params.ReasoningEffort = shared.ReasoningEffort(effort)
		}
	}
	if req.Temperature != nil {
		params.Temperature = openai.Float(*req.Temperature)
	}
	if req.TopP != nil {
		params.TopP = openai.Float(*req.TopP)
	}
	for k, v := range req.Extra {
		opts = append(opts, option.WithJSONSet(k, v))
	}
	return params, opts, nil
}

// toMessages converts history. Thinking parts are not sent back: the Chat
// Completions protocol has no field for them.
func toMessages(system string, in []llm.Message) ([]openai.ChatCompletionMessageParamUnion, error) {
	var out []openai.ChatCompletionMessageParamUnion
	if system != "" {
		out = append(out, openai.SystemMessage(system))
	}
	for _, m := range in {
		switch m.Role {
		case llm.RoleUser:
			var parts []openai.ChatCompletionContentPartUnionParam
			for _, p := range m.Parts {
				switch p.Kind {
				case llm.PartText:
					parts = append(parts, openai.TextContentPart(p.Text))
				case llm.PartImage:
					if p.Image == nil {
						return nil, errors.New("image part without image")
					}
					url := p.Image.URL
					if url == "" {
						url = "data:" + p.Image.MediaType + ";base64," + base64.StdEncoding.EncodeToString(p.Image.Data)
					}
					parts = append(parts, openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{URL: url}))
				}
			}
			out = append(out, openai.UserMessage(parts))
		case llm.RoleAssistant:
			var text strings.Builder
			for _, p := range m.Parts {
				if p.Kind == llm.PartText {
					text.WriteString(p.Text)
				}
			}
			am := openai.ChatCompletionAssistantMessageParam{}
			if text.Len() > 0 {
				am.Content.OfString = openai.String(text.String())
			}
			for _, c := range m.ToolCalls {
				a := string(c.Args)
				if a == "" {
					a = "{}"
				}
				am.ToolCalls = append(am.ToolCalls, openai.ChatCompletionMessageToolCallUnionParam{
					OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
						ID:       c.ID,
						Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{Name: c.Name, Arguments: a},
					},
				})
			}
			out = append(out, openai.ChatCompletionMessageParamUnion{OfAssistant: &am})
		case llm.RoleTool:
			if m.ToolResult == nil {
				return nil, errors.New("tool message without result")
			}
			content := m.ToolResult.Content
			if m.ToolResult.IsError {
				content = "Error: " + content
			}
			out = append(out, openai.ToolMessage(content, m.ToolResult.CallID))
		default:
			return nil, fmt.Errorf("unknown role %q", m.Role)
		}
	}
	return out, nil
}

// normalizeErrorBody rewrites non-OpenAI error bodies (LM Studio sends
// {"error": "message"}, others plain text) into OpenAI's
// {"error": {"message": ...}} shape so the SDK reports the real message.
func normalizeErrorBody(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	resp, err := next(req)
	if err != nil || resp.StatusCode < 400 {
		return resp, err
	}
	raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 8192))
	_ = resp.Body.Close()
	if rerr != nil {
		raw = nil
	}
	msg := ""
	var shaped struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &shaped) == nil && len(shaped.Error) > 0 {
		var obj struct {
			Message string `json:"message"`
		}
		var str string
		switch {
		case json.Unmarshal(shaped.Error, &obj) == nil && obj.Message != "":
			resp.Body = io.NopCloser(bytes.NewReader(raw)) // already OpenAI-shaped
			return resp, nil
		case json.Unmarshal(shaped.Error, &str) == nil:
			msg = str
		}
	}
	if msg == "" {
		msg = strings.TrimSpace(string(raw))
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
	}
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": "provider_error"}})
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Type", "application/json")
	return resp, nil
}

// reasoningText extracts non-standard reasoning fields (reasoning_content
// from vLLM/DeepSeek-style servers, reasoning from Ollama/LM Studio).
func reasoningText(extra map[string]respjson.Field) string {
	for _, k := range []string{"reasoning_content", "reasoning"} {
		f, ok := extra[k]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal([]byte(f.Raw()), &s); err == nil && s != "" {
			return s
		}
	}
	return ""
}

func args(s string) json.RawMessage {
	if strings.TrimSpace(s) == "" {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(s)
}

func stopReason(finish, refusal string, calls int) string {
	switch {
	case refusal != "" || finish == "content_filter":
		return llm.StopRefusal
	case calls > 0 || finish == "tool_calls":
		return llm.StopToolUse
	case finish == "length":
		return llm.StopMaxTokens
	}
	return llm.StopEnd
}

func (p *Provider) wrap(err error) error {
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		e := &llm.APIError{Provider: p.cfg.ID, Status: apiErr.StatusCode, Message: apiErr.Message, Err: err}
		if e.Message == "" {
			e.Message = apiErr.Error()
		}
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
	return &llm.APIError{Provider: p.cfg.ID, Message: err.Error(), Err: err}
}
