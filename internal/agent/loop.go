package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm/router"
)

// Step kinds recorded by the loop (research_steps.kind).
const (
	StepToolCall   = "tool_call"
	StepToolResult = "tool_result"
	StepThought    = "thought_summary"
	StepAssistant  = "assistant"
	StepLimit      = "limit"
)

// Stop reasons when a loop ends before the model finishes on its own.
const (
	StopMaxSteps = "max_steps"
	StopDeadline = "time_limit"
	StopBudget   = "token_budget"
)

// ErrCancelled is returned when the run was cancelled between steps.
var ErrCancelled = errors.New("cancelled")

// Chatter is the router surface the loop needs.
type Chatter interface {
	ChatTargets(ctx context.Context, call router.Call, targets []router.Target, req llm.ChatRequest) (*llm.ChatResponse, []router.Attempt, error)
}

// Budget is a token budget shared by every call in a job.
type Budget struct {
	Limit int // 0 = unlimited
	used  atomic.Int64
}

func (b *Budget) Add(u llm.Usage) {
	if b != nil {
		b.used.Add(int64(u.InputTokens + u.OutputTokens + u.ThinkingTokens))
	}
}
func (b *Budget) Used() int {
	if b == nil {
		return 0
	}
	return int(b.used.Load())
}
func (b *Budget) Exhausted() bool { return b != nil && b.Limit > 0 && b.Used() >= b.Limit }

// Loop runs a model with tools until it answers or hits a limit.
type Loop struct {
	Router   Chatter
	Call     router.Call
	Targets  []router.Target
	Tools    *Registry
	MaxSteps int
	Deadline time.Time // zero: none
	Budget   *Budget
	// MaxContext (tokens) triggers compaction of old tool results at 60%,
	// and of the latest results if a request would not fit at all.
	MaxContext int
	// OutputReserve is the room kept for the reply (the request's max
	// tokens); default 2048.
	OutputReserve int
	// Summarize condenses an old tool result; nil truncates instead.
	Summarize func(ctx context.Context, text string) (string, error)
	// Record persists a progress step; runID links the model call.
	Record func(ctx context.Context, kind string, payload any, runID *uuid.UUID)
	// Cancelled is checked between steps.
	Cancelled func(ctx context.Context) bool
	// RequireTool nudges the model once when it answers without using any
	// tool, so answers rest on evidence rather than memory.
	RequireTool bool
	ToolTimeout time.Duration // per call; default 30s
	Parallel    int           // concurrent tool calls; default 4
	MaxResult   int           // characters of a tool result kept; default 12000
}

// Result is the loop's outcome.
type Result struct {
	Text     string
	Messages []llm.Message
	Steps    int
	Stopped  string // "" when the model finished on its own
	RunID    *uuid.UUID
}

func (l *Loop) record(ctx context.Context, kind string, payload any, runID *uuid.UUID) {
	if l.Record != nil {
		l.Record(ctx, kind, payload, runID)
	}
}

// Run executes the loop. On a limit it makes one final call with tools
// disabled so the model answers from what it has gathered.
func (l *Loop) Run(ctx context.Context, system string, msgs []llm.Message) (*Result, error) {
	if l.ToolTimeout == 0 {
		l.ToolTimeout = 30 * time.Second
	}
	if l.Parallel == 0 {
		l.Parallel = 4
	}
	if l.MaxResult == 0 {
		l.MaxResult = 12000
	}
	if l.MaxSteps <= 0 {
		l.MaxSteps = 8
	}
	tools := l.Tools
	if tools == nil {
		tools = NewRegistry()
	}
	res := &Result{}
	usedTool, nudged := false, false
	seen := map[string]bool{} // tool calls already made in this run
	for step := 0; ; step++ {
		if l.Cancelled != nil && l.Cancelled(ctx) {
			return nil, ErrCancelled
		}
		switch {
		case step >= l.MaxSteps:
			res.Stopped = StopMaxSteps
		case !l.Deadline.IsZero() && time.Now().After(l.Deadline):
			res.Stopped = StopDeadline
		case l.Budget.Exhausted():
			res.Stopped = StopBudget
		}
		if res.Stopped != "" {
			break
		}
		l.compact(ctx, system, tools.Defs(), msgs)
		resp, attempts, err := l.Router.ChatTargets(ctx, l.Call, l.Targets, llm.ChatRequest{System: system, Messages: msgs, Tools: tools.Defs()})
		if err != nil {
			return nil, err
		}
		runID := lastRun(attempts)
		res.RunID, res.Steps = runID, step+1
		l.Budget.Add(resp.Usage)
		l.recordThinking(ctx, resp, runID)
		msgs = append(msgs, resp.AssistantMessage())
		if len(resp.ToolCalls) == 0 {
			if l.RequireTool && !usedTool && !nudged && tools.Len() > 0 && step+1 < l.MaxSteps {
				nudged = true
				l.record(ctx, StepAssistant, map[string]any{"text": "Answered without looking anything up; asked to search first."}, runID)
				msgs = append(msgs, llm.UserText("You have not used any tools yet. Do not answer from memory: search for evidence with the tools first, then answer citing only the source numbers the tools show."))
				continue
			}
			res.Text, res.Messages = strings.TrimSpace(resp.Text()), msgs
			return res, nil
		}
		usedTool = true
		if t := strings.TrimSpace(resp.Text()); t != "" {
			l.record(ctx, StepAssistant, map[string]any{"text": clip(t, 1200)}, runID)
		}
		msgs = append(msgs, l.runTools(ctx, tools, resp.ToolCalls, runID, seen)...)
	}

	l.record(ctx, StepLimit, map[string]any{"reason": res.Stopped, "steps": res.Steps, "tokens": l.Budget.Used()}, nil)
	msgs = append(msgs, llm.UserText("You have reached the limit for this task. Do not call any more tools. Using only what you have gathered, give your final answer now."))
	resp, attempts, err := l.Router.ChatTargets(ctx, l.Call, l.Targets, llm.ChatRequest{System: system, Messages: msgs, Tools: tools.Defs(), ToolChoice: llm.ToolChoiceNone})
	if err != nil {
		return nil, err
	}
	l.Budget.Add(resp.Usage)
	res.RunID = lastRun(attempts)
	res.Text, res.Messages = strings.TrimSpace(resp.Text()), append(msgs, resp.AssistantMessage())
	return res, nil
}

func (l *Loop) recordThinking(ctx context.Context, resp *llm.ChatResponse, runID *uuid.UUID) {
	var b strings.Builder
	for _, p := range resp.Parts {
		if p.Kind == llm.PartThinking && p.Text != "" {
			b.WriteString(p.Text)
			b.WriteString("\n")
		}
	}
	if b.Len() > 0 {
		// A short summary for the timeline; raw thinking never goes into reports.
		l.record(ctx, StepThought, map[string]any{"text": clip(strings.TrimSpace(b.String()), 600)}, runID)
	}
}

// runTools executes calls concurrently and returns their result messages in
// call order, followed by one user message carrying any images.
func (l *Loop) runTools(ctx context.Context, tools *Registry, calls []llm.ToolCall, runID *uuid.UUID, seen map[string]bool) []llm.Message {
	results := make([]llm.Message, len(calls))
	var mu sync.Mutex
	var images []llm.Image
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(l.Parallel)
	for i, call := range calls {
		l.record(ctx, StepToolCall, map[string]any{"id": call.ID, "name": call.Name, "args": orObject(call.Args)}, runID)
		// An identical call already ran in this task: answer from that
		// instead of running it again (models can loop on empty results).
		key := callKey(call)
		if seen[key] {
			results[i] = llm.Message{Role: llm.RoleTool, ToolResult: &llm.ToolResult{CallID: call.ID, Content: repeatNotice}}
			l.record(ctx, StepToolResult, map[string]any{"id": call.ID, "name": call.Name, "summary": "repeat skipped", "is_error": false, "preview": repeatNotice}, nil)
			continue
		}
		seen[key] = true
		g.Go(func() error {
			out, isErr := l.callTool(gctx, tools, call)
			text := clip(out.Text, l.MaxResult)
			results[i] = llm.Message{Role: llm.RoleTool, ToolResult: &llm.ToolResult{CallID: call.ID, Content: text, IsError: isErr}}
			summary := out.Summary
			if isErr {
				summary = clip(out.Text, 300)
			}
			l.record(ctx, StepToolResult, map[string]any{"id": call.ID, "name": call.Name, "summary": summary, "is_error": isErr, "preview": clip(text, 600)}, nil)
			if len(out.Images) > 0 {
				mu.Lock()
				images = append(images, out.Images...)
				mu.Unlock()
			}
			return nil
		})
	}
	_ = g.Wait()
	if len(images) > 0 {
		parts := []llm.Part{llm.TextPart("Images requested with view_image, in order:")}
		for i := range images {
			parts = append(parts, llm.Part{Kind: llm.PartImage, Image: &images[i]})
		}
		results = append(results, llm.Message{Role: llm.RoleUser, Parts: parts})
	}
	return results
}

func (l *Loop) callTool(ctx context.Context, tools *Registry, call llm.ToolCall) (out Output, isErr bool) {
	t, ok := tools.Get(call.Name)
	if !ok {
		return Output{Text: fmt.Sprintf("Unknown tool %q. Available tools: %s.", call.Name, strings.Join(tools.Names(), ", "))}, true
	}
	ctx, cancel := context.WithTimeout(ctx, l.ToolTimeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			out, isErr = Output{Text: fmt.Sprintf("tool %s failed", call.Name)}, true
		}
	}()
	out, err := t.Handler(ctx, orObject(call.Args))
	if err != nil {
		return Output{Text: "Error: " + err.Error()}, true
	}
	return out, false
}

// compact keeps the request inside the model's context window. Older tool
// results are summarized once the history passes 60% of the window; if the
// request still would not fit next to the output reserve, the latest
// results are trimmed too (largest first). Source numbers in the kept text
// stay citable.
func (l *Loop) compact(ctx context.Context, system string, defs []llm.ToolDef, msgs []llm.Message) {
	if l.MaxContext <= 0 {
		return
	}
	reserve := l.OutputReserve
	if reserve <= 0 {
		reserve = 2048
	}
	reserve = min(reserve, l.MaxContext/2)
	base := EstimateText(system) + EstimateDefs(defs)
	size := func() int { return base + EstimateTokens(msgs) }
	soft := l.MaxContext * 6 / 10
	hard := l.MaxContext - reserve - 256
	if size() <= soft {
		return
	}
	lastAssistant := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleAssistant {
			lastAssistant = i
			break
		}
	}
	// 1. Older turns: summarize.
	for i := range msgs {
		if i >= lastAssistant || size() <= soft {
			break
		}
		tr := msgs[i].ToolResult
		if tr == nil || strings.HasPrefix(tr.Content, compactedPrefix) || len(tr.Content) < 600 {
			continue
		}
		short := clip(tr.Content, 400)
		if l.Summarize != nil {
			if s, err := l.Summarize(ctx, tr.Content); err == nil && strings.TrimSpace(s) != "" {
				short = clip(strings.TrimSpace(s), 1200)
			}
		}
		setResult(msgs, i, compactedPrefix+short)
	}
	// 2. Still too big to send: trim results, largest first, harder each
	// pass. Each result is trimmed at most once per pass, so this always
	// ends; if even the tightest pass is not enough, the request is sent as
	// is and the provider reports the overflow.
	for _, keep := range []int{1500, 600, 200} {
		for size() > hard && ctx.Err() == nil {
			big, n := -1, keep
			for i := range msgs {
				if tr := msgs[i].ToolResult; tr != nil {
					if body := resultBody(tr.Content); utf8.RuneCountInString(body) > n {
						big, n = i, utf8.RuneCountInString(body)
					}
				}
			}
			if big < 0 {
				break // every result is already within keep
			}
			setResult(msgs, big, compactedPrefix+clip(resultBody(msgs[big].ToolResult.Content), keep))
		}
	}
}

// resultBody is a tool result without the condensed marker or the trailing
// ellipsis clip adds.
func resultBody(s string) string {
	return strings.TrimSuffix(strings.TrimPrefix(s, compactedPrefix), "…")
}

func setResult(msgs []llm.Message, i int, content string) {
	c := *msgs[i].ToolResult
	c.Content = content
	msgs[i].ToolResult = &c
}

const compactedPrefix = "[earlier result, condensed] "

const repeatNotice = "You already made this exact call in this task, and its result has not changed. Use what you found, try a different query, or answer with what you have."

// callKey identifies a tool call by name and canonical arguments.
func callKey(c llm.ToolCall) string {
	var v any
	if json.Unmarshal(c.Args, &v) == nil {
		if b, err := json.Marshal(v); err == nil { // map keys are sorted
			return c.Name + " " + string(b)
		}
	}
	return c.Name + " " + strings.TrimSpace(string(c.Args))
}

// EstimateText approximates a text's tokens conservatively (3 characters
// per token; URLs, JSON, and non-English text tokenize worse than prose).
func EstimateText(s string) int { return len(s)/3 + 1 }

// EstimateDefs approximates the tokens tool definitions add to a request.
func EstimateDefs(defs []llm.ToolDef) int {
	n := 0
	for _, d := range defs {
		n += EstimateText(d.Name+d.Description+string(d.Schema)) + 10
	}
	return n
}

// EstimateTokens approximates a history's size.
func EstimateTokens(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		n += 6 // per-message framing
		for _, p := range m.Parts {
			n += EstimateText(p.Text)
			if p.Image != nil {
				n += 1000
			}
		}
		for _, c := range m.ToolCalls {
			n += EstimateText(string(c.Args)+c.Name) + 10
		}
		if m.ToolResult != nil {
			n += EstimateText(m.ToolResult.Content)
		}
	}
	return n
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func orObject(b json.RawMessage) json.RawMessage {
	if len(strings.TrimSpace(string(b))) == 0 {
		return json.RawMessage(`{}`)
	}
	return b
}

func lastRun(attempts []router.Attempt) *uuid.UUID {
	for i := len(attempts) - 1; i >= 0; i-- {
		if attempts[i].Err == nil && attempts[i].RunID != uuid.Nil {
			id := attempts[i].RunID
			return &id
		}
	}
	return nil
}
