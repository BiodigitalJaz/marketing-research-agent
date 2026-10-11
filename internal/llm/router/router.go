// Package router resolves task -> provider/model routes from the database
// (edited in Settings), applies params, enforces local-only mode, fails over
// between primary and fallback, and records every attempt in model_runs.
package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm/anthropic"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm/openaicompat"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/secrets"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

var (
	// ErrNoRoute means the task has no usable provider (unset route,
	// disabled provider or model, or every candidate failed validation).
	ErrNoRoute = errors.New("no usable provider for this task")
	// ErrNoLocalRoute means local-only mode is on and neither the primary
	// nor the fallback is a local provider.
	ErrNoLocalRoute = errors.New("local-only mode is on and this task has no local provider")
)

// Target is one concrete provider/model candidate.
type Target struct {
	ProviderID  string
	Concurrency int // max in-flight calls to this provider in this process; 0 = unlimited
	Model       string
	Local       bool
	Fallback    bool
	Caps        llm.Capabilities
	Params      Params
	Price       Price
	Provider    llm.Provider
}

// Resolved is the routing decision for a task.
type Resolved struct {
	Task         string
	Targets      []Target // in try order
	Tools        []string
	MCPServerIDs []uuid.UUID
	LocalOnly    bool
	Skipped      []string // human-readable reasons candidates were dropped
}

// Router is safe for concurrent use.
type Router struct {
	pool *pgxpool.Pool
	box  *secrets.Box
	env  func(string) string
	log  *slog.Logger
	// Build constructs a provider from its DB row and resolved key.
	// Tests replace it to return fakes.
	Build func(row store.ModelProvider, apiKey string) (llm.Provider, error)

	semMu sync.Mutex
	sems  map[string]chan struct{}
}

// acquire waits for a slot on a provider with a concurrency limit and
// returns the release func. Limits are per process (api and worker each).
func (r *Router) acquire(ctx context.Context, t Target) (func(), error) {
	if t.Concurrency <= 0 {
		return func() {}, nil
	}
	key := fmt.Sprintf("%s/%d", t.ProviderID, t.Concurrency)
	r.semMu.Lock()
	if r.sems == nil {
		r.sems = map[string]chan struct{}{}
	}
	sem, ok := r.sems[key]
	if !ok {
		sem = make(chan struct{}, t.Concurrency)
		r.sems[key] = sem
	}
	r.semMu.Unlock()
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func New(pool *pgxpool.Pool, box *secrets.Box, env func(string) string, log *slog.Logger) *Router {
	return &Router{pool: pool, box: box, env: env, log: log, Build: BuildProvider}
}

// ProviderOptions is model_providers.options.
type ProviderOptions struct {
	ReasoningStyle string `json:"reasoning_style,omitempty"`
	MaxTokensParam string `json:"max_tokens_param,omitempty"`
	MaxRetries     int    `json:"max_retries,omitempty"`
	// MaxConcurrency limits simultaneous calls (per process); 0 = unlimited.
	// Local servers usually serve one request at a time.
	MaxConcurrency int `json:"max_concurrency,omitempty"`
}

// BuildProvider is the production provider factory.
func BuildProvider(row store.ModelProvider, apiKey string) (llm.Provider, error) {
	var opts ProviderOptions
	if len(row.Options) > 0 {
		if err := json.Unmarshal(row.Options, &opts); err != nil {
			return nil, fmt.Errorf("provider %s options: %w", row.ID, err)
		}
	}
	var headers map[string]string
	if len(row.Headers) > 0 {
		if err := json.Unmarshal(row.Headers, &headers); err != nil {
			return nil, fmt.Errorf("provider %s headers: %w", row.ID, err)
		}
	}
	base := ""
	if row.BaseUrl != nil {
		base = *row.BaseUrl
	}
	switch row.Kind {
	case "anthropic":
		return anthropic.New(anthropic.Config{ID: row.ID, APIKey: apiKey, BaseURL: base, MaxRetries: opts.MaxRetries}), nil
	case "openai_compat":
		if base == "" {
			return nil, fmt.Errorf("provider %s: base_url is required", row.ID)
		}
		return openaicompat.New(openaicompat.Config{
			ID: row.ID, BaseURL: base, APIKey: apiKey, Local: row.IsLocal, Headers: headers,
			ReasoningStyle: opts.ReasoningStyle, MaxTokensParam: opts.MaxTokensParam, MaxRetries: opts.MaxRetries,
		}), nil
	}
	return nil, fmt.Errorf("provider %s: unknown kind %q", row.ID, row.Kind)
}

// APIKey resolves a provider's key: a UI-entered secret wins over the env
// var reference. The value must never be logged.
func (r *Router) APIKey(ctx context.Context, q *store.Queries, row store.ModelProvider) (string, error) {
	if row.ApiKeySecretID != nil {
		s, err := q.GetSecret(ctx, store.GetSecretParams{ID: *row.ApiKeySecretID})
		if err != nil {
			return "", fmt.Errorf("provider %s secret: %w", row.ID, err)
		}
		v, err := r.box.Open(secrets.Sealed{Ciphertext: s.Ciphertext, Nonce: s.Nonce, KeyID: s.KeyID}, s.ID, s.WorkspaceID)
		if err != nil {
			return "", fmt.Errorf("provider %s secret: %w", row.ID, err)
		}
		return v, nil
	}
	if row.ApiKeyEnv != nil && *row.ApiKeyEnv != "" {
		return r.env(*row.ApiKeyEnv), nil
	}
	return "", nil
}

// Target builds a single target for a provider/model, used by the
// playground and connection tests.
func (r *Router) Target(ctx context.Context, providerID, model string) (Target, error) {
	q := store.New(r.pool)
	row, err := q.GetProvider(ctx, providerID)
	if err != nil {
		return Target{}, fmt.Errorf("provider %s: %w", providerID, err)
	}
	return r.target(ctx, q, row, model, false)
}

func (r *Router) target(ctx context.Context, q *store.Queries, row store.ModelProvider, model string, fallback bool) (Target, error) {
	key, err := r.APIKey(ctx, q, row)
	if err != nil {
		return Target{}, err
	}
	p, err := r.Build(row, key)
	if err != nil {
		return Target{}, err
	}
	var opts ProviderOptions
	_ = json.Unmarshal(orEmpty(row.Options), &opts)
	t := Target{ProviderID: row.ID, Model: model, Local: row.IsLocal, Fallback: fallback, Provider: p, Concurrency: opts.MaxConcurrency}
	if model == "" {
		return t, nil
	}
	pm, err := q.GetProviderModel(ctx, store.GetProviderModelParams{ProviderID: row.ID, Model: model})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Undeclared model: no capabilities declared, so only plain chat validates.
	case err != nil:
		return Target{}, err
	default:
		if !pm.Enabled {
			return Target{}, fmt.Errorf("model %s/%s is disabled", row.ID, model)
		}
		if err := json.Unmarshal(orEmpty(pm.Capabilities), &t.Caps); err != nil {
			return Target{}, fmt.Errorf("model %s/%s capabilities: %w", row.ID, model, err)
		}
		if t.Params, err = ParseParams(pm.DefaultParams); err != nil {
			return Target{}, err
		}
		if err := json.Unmarshal(orEmpty(pm.Price), &t.Price); err != nil {
			return Target{}, fmt.Errorf("model %s/%s price: %w", row.ID, model, err)
		}
	}
	if row.IsLocal {
		t.Price = Price{} // local inference is free
	}
	return t, nil
}

func orEmpty(b []byte) []byte {
	if len(b) == 0 {
		return []byte("{}")
	}
	return b
}

// Resolve loads the route for (workspace, task) and builds its targets.
func (r *Router) Resolve(ctx context.Context, ws uuid.UUID, task string) (*Resolved, error) {
	q := store.New(r.pool)
	route, err := q.GetTaskRoute(ctx, store.GetTaskRouteParams{WorkspaceID: ws, Task: task})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: no route configured for task %q", ErrNoRoute, task)
	}
	if err != nil {
		return nil, err
	}
	localOnly := false
	if st, err := q.GetWorkspaceSettings(ctx, ws); err == nil {
		localOnly = st.LocalOnly
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	routeParams, err := ParseParams(route.Params)
	if err != nil {
		return nil, err
	}
	res := &Resolved{Task: task, Tools: route.Tools, MCPServerIDs: route.McpServerIds, LocalOnly: localOnly}

	type cand struct {
		provider, model string
		fallback        bool
	}
	cands := []cand{{route.ProviderID, route.Model, false}}
	if route.FallbackProviderID != nil && route.FallbackModel != nil {
		cands = append(cands, cand{*route.FallbackProviderID, *route.FallbackModel, true})
	}
	for _, c := range cands {
		row, err := q.GetProvider(ctx, c.provider)
		if err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s/%s: provider not found", c.provider, c.model))
			continue
		}
		if !row.Enabled {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s/%s: provider disabled", c.provider, c.model))
			continue
		}
		if localOnly && !row.IsLocal {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s/%s: hosted provider skipped in local-only mode", c.provider, c.model))
			continue
		}
		t, err := r.target(ctx, q, row, c.model, c.fallback)
		if err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s/%s: %v", c.provider, c.model, err))
			continue
		}
		t.Params = t.Params.Merge(routeParams)
		res.Targets = append(res.Targets, t)
	}
	if len(res.Targets) == 0 {
		base := ErrNoRoute
		if localOnly {
			base = ErrNoLocalRoute
		}
		return res, fmt.Errorf("%w: %s", base, strings.Join(res.Skipped, "; "))
	}
	return res, nil
}

// Call identifies a routed request for logging.
type Call struct {
	Workspace uuid.UUID
	Task      string
	PromptID  *uuid.UUID
}

// Attempt describes one provider call within a routed request.
type Attempt struct {
	ProviderID string
	Model      string
	Fallback   bool
	RunID      uuid.UUID
	Err        error
}

// Chat routes a request for a task, failing over on provider errors.
func (r *Router) Chat(ctx context.Context, call Call, req llm.ChatRequest) (*llm.ChatResponse, []Attempt, error) {
	res, err := r.Resolve(ctx, call.Workspace, call.Task)
	if err != nil {
		return nil, nil, err
	}
	return r.ChatTargets(ctx, call, res.Targets, req)
}

// ChatTargets tries targets in order. Each attempt writes a model_runs row.
func (r *Router) ChatTargets(ctx context.Context, call Call, targets []Target, req llm.ChatRequest) (*llm.ChatResponse, []Attempt, error) {
	var attempts []Attempt
	var lastErr error
	for _, t := range targets {
		treq := t.Params.Apply(req)
		treq.Model = t.Model
		if err := t.Caps.Validate(treq); err != nil {
			lastErr = fmt.Errorf("%s/%s: %w", t.ProviderID, t.Model, err)
			attempts = append(attempts, Attempt{ProviderID: t.ProviderID, Model: t.Model, Fallback: t.Fallback, Err: lastErr})
			continue
		}
		release, err := r.acquire(ctx, t)
		if err != nil {
			return nil, attempts, err
		}
		start := time.Now()
		resp, err := t.Provider.Chat(ctx, treq)
		release()
		runID := r.record(ctx, call, t, resp, time.Since(start), err)
		attempts = append(attempts, Attempt{ProviderID: t.ProviderID, Model: t.Model, Fallback: t.Fallback, RunID: runID, Err: err})
		if err == nil {
			return resp, attempts, nil
		}
		lastErr = err
		var apiErr *llm.APIError
		if !errors.As(err, &apiErr) || !apiErr.Failover() {
			return nil, attempts, err
		}
		r.log.WarnContext(ctx, "llm call failed; trying next target", "task", call.Task,
			"provider", t.ProviderID, "model", t.Model, "status", apiErr.Status)
	}
	if lastErr == nil {
		lastErr = ErrNoRoute
	}
	return nil, attempts, lastErr
}

// Stream routes a streaming request. Failover happens only if a target
// fails before producing any output; after that, errors are surfaced.
func (r *Router) Stream(ctx context.Context, call Call, targets []Target, req llm.ChatRequest) (<-chan llm.Event, error) {
	out := make(chan llm.Event, 16)
	go func() {
		defer close(out)
		var lastErr error
		for _, t := range targets {
			treq := t.Params.Apply(req)
			treq.Model = t.Model
			if err := t.Caps.Validate(treq); err != nil {
				lastErr = fmt.Errorf("%s/%s: %w", t.ProviderID, t.Model, err)
				continue
			}
			release, err := r.acquire(ctx, t)
			if err != nil {
				out <- llm.Event{Kind: llm.EventError, Err: err}
				return
			}
			start := time.Now()
			ch, err := t.Provider.Stream(ctx, treq)
			if err != nil {
				release()
				r.record(ctx, call, t, nil, time.Since(start), err)
				lastErr = err
				if canFailover(err) {
					continue
				}
				break
			}
			emitted := false
			failedEarly := false
			for ev := range ch {
				switch ev.Kind {
				case llm.EventError:
					r.record(ctx, call, t, nil, time.Since(start), ev.Err)
					lastErr = ev.Err
					if !emitted && canFailover(ev.Err) {
						failedEarly = true
						continue
					}
					release()
					out <- ev
					return
				case llm.EventDone:
					release()
					r.record(ctx, call, t, ev.Resp, time.Since(start), nil)
					out <- ev
					return
				default:
					emitted = true
					out <- ev
				}
			}
			release()
			if !failedEarly {
				return
			}
		}
		if lastErr == nil {
			lastErr = ErrNoRoute
		}
		out <- llm.Event{Kind: llm.EventError, Err: lastErr}
	}()
	return out, nil
}

func canFailover(err error) bool {
	var apiErr *llm.APIError
	return errors.As(err, &apiErr) && apiErr.Failover()
}

// record writes a model_runs row. Prompt text is never stored here.
func (r *Router) record(ctx context.Context, call Call, t Target, resp *llm.ChatResponse, d time.Duration, callErr error) uuid.UUID {
	p := store.InsertModelRunParams{
		WorkspaceID: call.Workspace, Task: call.Task, ProviderID: t.ProviderID, Model: t.Model,
		PromptID: call.PromptID, LatencyMs: i32(int(d.Milliseconds())), Fallback: t.Fallback,
	}
	if resp != nil {
		p.InputTokens = i32(resp.Usage.InputTokens)
		p.OutputTokens = i32(resp.Usage.OutputTokens)
		if resp.Usage.ThinkingTokens > 0 {
			p.ThinkingTokens = i32(resp.Usage.ThinkingTokens)
		}
		_ = p.CostUsd.Scan(fmt.Sprintf("%.6f", t.Price.Cost(resp.Usage)))
	} else {
		p.CostUsd = pgtype.Numeric{}
	}
	if callErr != nil {
		msg := callErr.Error()
		if len(msg) > 1000 {
			msg = msg[:1000]
		}
		p.Error = &msg
	}
	// Use a detached context so a cancelled request still records its run.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	id, err := store.New(r.pool).InsertModelRun(rctx, p)
	if err != nil {
		r.log.ErrorContext(ctx, "record model run failed", "err", err)
	}
	return id
}

func i32(v int) *int32 {
	x := int32(v) // #nosec G115 -- token counts and latencies fit in int32
	return &x
}
