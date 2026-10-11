// Package fake provides a scripted llm.Provider for tests.
package fake

import (
	"context"
	"errors"
	"sync"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm"
)

// Provider returns scripted responses in order and records every request.
// When the script runs out, Respond (if set) is used.
type Provider struct {
	Name    string
	mu      sync.Mutex
	script  []Reply
	Respond func(llm.ChatRequest) Reply
	reqs    []llm.ChatRequest
	Models  []llm.ModelInfo
}

// Reply is one scripted outcome.
type Reply struct {
	Resp *llm.ChatResponse
	Err  error
}

func New(name string, replies ...Reply) *Provider { return &Provider{Name: name, script: replies} }

// Text is a convenience reply with a text response.
func Text(s string) Reply {
	return Reply{Resp: &llm.ChatResponse{Parts: []llm.Part{llm.TextPart(s)}, StopReason: llm.StopEnd, Usage: llm.Usage{InputTokens: 10, OutputTokens: 5}}}
}

// Fail is a convenience reply with an API error of the given status.
func Fail(status int) Reply {
	return Reply{Err: &llm.APIError{Provider: "fake", Status: status, Message: "scripted failure"}}
}

func (p *Provider) ID() string { return p.Name }

func (p *Provider) next(req llm.ChatRequest) Reply {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reqs = append(p.reqs, req)
	if len(p.script) > 0 {
		r := p.script[0]
		p.script = p.script[1:]
		return r
	}
	if p.Respond != nil {
		return p.Respond(req)
	}
	return Reply{Err: errors.New("fake: no scripted reply left")}
}

func (p *Provider) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	r := p.next(req)
	if r.Resp != nil && r.Resp.Model == "" {
		r.Resp.Model = req.Model
	}
	return r.Resp, r.Err
}

func (p *Provider) Stream(ctx context.Context, req llm.ChatRequest) (<-chan llm.Event, error) {
	resp, err := p.Chat(ctx, req)
	ch := make(chan llm.Event, 4)
	go func() {
		defer close(ch)
		if err != nil {
			ch <- llm.Event{Kind: llm.EventError, Err: err}
			return
		}
		if t := resp.Text(); t != "" {
			ch <- llm.Event{Kind: llm.EventTextDelta, Text: t}
		}
		for i := range resp.ToolCalls {
			ch <- llm.Event{Kind: llm.EventToolCall, Call: &resp.ToolCalls[i]}
		}
		ch <- llm.Event{Kind: llm.EventDone, Resp: resp}
	}()
	return ch, nil
}

func (p *Provider) ListModels(context.Context) ([]llm.ModelInfo, error) { return p.Models, nil }

// Requests returns a copy of the recorded requests.
func (p *Provider) Requests() []llm.ChatRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.ChatRequest(nil), p.reqs...)
}
