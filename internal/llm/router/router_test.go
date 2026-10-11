package router_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm/fake"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm/router"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/secrets"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/testutil/pgtest"
)

type env struct {
	t     *testing.T
	pool  *pgxpool.Pool
	q     *store.Queries
	ws    uuid.UUID
	r     *router.Router
	fakes map[string]*fake.Provider
	keys  map[string]string // provider id -> key the factory received
	box   *secrets.Box
}

func setup(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	pool := pgtest.NewMigratedDB(t)
	q := store.New(pool)
	ws, _ := q.CreateWorkspace(ctx, "A")
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	box, _ := secrets.FromEnv(func(n string) string {
		if n == secrets.EnvKey {
			return base64.StdEncoding.EncodeToString(k)
		}
		return ""
	})
	e := &env{t: t, pool: pool, q: q, ws: ws.ID, fakes: map[string]*fake.Provider{}, keys: map[string]string{}, box: box}
	e.r = router.New(pool, box, func(n string) string {
		if n == "CLAUDE_KEY" {
			return "env-key"
		}
		return ""
	}, pgtest.DiscardLogger())
	e.r.Build = func(row store.ModelProvider, key string) (llm.Provider, error) {
		e.keys[row.ID] = key
		f, ok := e.fakes[row.ID]
		if !ok {
			return nil, errors.New("no fake for " + row.ID)
		}
		return f, nil
	}
	for _, p := range []struct {
		id    string
		local bool
	}{{"claude", false}, {"openai", false}, {"local", true}} {
		env := "CLAUDE_KEY"
		if _, err := q.CreateProvider(ctx, store.CreateProviderParams{ID: p.id, Kind: "openai_compat", BaseUrl: strp("http://x"), IsLocal: p.local,
			ApiKeyEnv: &env, Headers: []byte("{}"), Options: []byte("{}"), Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	e.model("claude", "big", `{"tools":true,"thinking":"effort"}`, `{"max_tokens":1000}`, `{"input_per_mtok":4,"output_per_mtok":20}`)
	e.model("local", "qwen", `{"tools":true,"sampling":true}`, `{}`, `{"input_per_mtok":99,"output_per_mtok":99}`)
	return e
}

func strp(s string) *string { return &s }

func (e *env) model(provider, model, caps, params, price string) {
	if _, err := e.q.UpsertProviderModel(context.Background(), store.UpsertProviderModelParams{
		ProviderID: provider, Model: model, Capabilities: []byte(caps), DefaultParams: []byte(params), Price: []byte(price), Enabled: true}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) route(primary, model string, fbProvider, fbModel *string, params string) {
	if _, err := e.q.UpsertTaskRoute(context.Background(), store.UpsertTaskRouteParams{
		WorkspaceID: e.ws, Task: "draft", ProviderID: primary, Model: model, FallbackProviderID: fbProvider, FallbackModel: fbModel,
		Params: []byte(params), Tools: []string{}, McpServerIds: []uuid.UUID{}}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) runs() []store.ModelRun {
	rows, err := e.q.ListRecentModelRuns(context.Background(), store.ListRecentModelRunsParams{WorkspaceID: e.ws, Limit: 50})
	if err != nil {
		e.t.Fatal(err)
	}
	// newest first -> oldest first
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows
}

var call = func(ws uuid.UUID) router.Call { return router.Call{Workspace: ws, Task: "draft"} }

func TestPrimarySuccessRecordsRunWithCostAndParams(t *testing.T) {
	e := setup(t)
	e.fakes["claude"] = fake.New("claude", fake.Text("hi"))
	e.route("claude", "big", nil, nil, `{"thinking":{"effort":"high"}}`)

	resp, attempts, err := e.r.Chat(context.Background(), call(e.ws), llm.ChatRequest{Messages: []llm.Message{llm.UserText("x")}})
	if err != nil || resp.Text() != "hi" || len(attempts) != 1 {
		t.Fatalf("resp=%v attempts=%v err=%v", resp, attempts, err)
	}
	req := e.fakes["claude"].Requests()[0]
	if req.Model != "big" || req.MaxTokens != 1000 || req.Thinking == nil || req.Thinking.Effort != "high" {
		t.Fatalf("params not applied: %+v", req)
	}
	if e.keys["claude"] != "env-key" {
		t.Fatalf("env key not resolved: %q", e.keys["claude"])
	}
	runs := e.runs()
	if len(runs) != 1 || runs[0].ProviderID != "claude" || *runs[0].InputTokens != 10 || runs[0].Error != nil || runs[0].Fallback {
		t.Fatalf("runs = %+v", runs)
	}
	cost, _ := runs[0].CostUsd.Float64Value()
	if want := 10.0/1e6*4 + 5.0/1e6*20; cost.Float64 < want-1e-9 || cost.Float64 > want+1e-9 {
		t.Fatalf("cost = %v want %v", cost.Float64, want)
	}
}

func TestFailoverOn429ButNotOn400(t *testing.T) {
	e := setup(t)
	e.fakes["claude"] = fake.New("claude", fake.Fail(http.StatusTooManyRequests), fake.Fail(http.StatusBadRequest))
	e.fakes["local"] = fake.New("local", fake.Text("from local"))
	e.route("claude", "big", strp("local"), strp("qwen"), `{}`)

	resp, attempts, err := e.r.Chat(context.Background(), call(e.ws), llm.ChatRequest{Messages: []llm.Message{llm.UserText("x")}})
	if err != nil || resp.Text() != "from local" || len(attempts) != 2 || !attempts[1].Fallback {
		t.Fatalf("resp=%v attempts=%+v err=%v", resp, attempts, err)
	}
	runs := e.runs()
	if len(runs) != 2 || runs[0].Error == nil || !runs[1].Fallback {
		t.Fatalf("runs = %+v", runs)
	}
	if c, _ := runs[1].CostUsd.Float64Value(); c.Float64 != 0 {
		t.Fatalf("local runs must cost 0, got %v", c.Float64)
	}

	_, _, err = e.r.Chat(context.Background(), call(e.ws), llm.ChatRequest{Messages: []llm.Message{llm.UserText("x")}})
	var apiErr *llm.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("400 must not fail over: %v", err)
	}
	if len(e.fakes["local"].Requests()) != 1 {
		t.Fatal("fallback was called for a 400")
	}
}

func TestLocalOnlyMode(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.q.UpsertWorkspaceSettings(ctx, store.UpsertWorkspaceSettingsParams{WorkspaceID: e.ws, LocalOnly: true, Embeddings: []byte("{}"), Search: []byte("{}")}); err != nil {
		t.Fatal(err)
	}
	e.fakes["claude"] = fake.New("claude", fake.Text("hosted"))
	e.fakes["local"] = fake.New("local", fake.Text("local"))

	e.route("claude", "big", strp("local"), strp("qwen"), `{}`)
	resp, _, err := e.r.Chat(ctx, call(e.ws), llm.ChatRequest{Messages: []llm.Message{llm.UserText("x")}})
	if err != nil || resp.Text() != "local" || len(e.fakes["claude"].Requests()) != 0 {
		t.Fatalf("local-only should skip hosted primary: %v %v", resp, err)
	}

	e.route("claude", "big", nil, nil, `{}`)
	_, _, err = e.r.Chat(ctx, call(e.ws), llm.ChatRequest{Messages: []llm.Message{llm.UserText("x")}})
	if !errors.Is(err, router.ErrNoLocalRoute) {
		t.Fatalf("want ErrNoLocalRoute, got %v", err)
	}
	for _, run := range e.runs() {
		if run.ProviderID != "local" {
			t.Fatalf("hosted provider called in local-only mode: %+v", run)
		}
	}
}

func TestCapabilityValidationSkipsToFallback(t *testing.T) {
	e := setup(t)
	e.fakes["claude"] = fake.New("claude")
	e.fakes["local"] = fake.New("local", fake.Text("ok"))
	// claude/big does not declare sampling; the route sets temperature.
	e.route("claude", "big", strp("local"), strp("qwen"), `{"temperature":0.3}`)
	resp, attempts, err := e.r.Chat(context.Background(), call(e.ws), llm.ChatRequest{Messages: []llm.Message{llm.UserText("x")}})
	if err != nil || resp.Text() != "ok" || attempts[0].Err == nil || len(e.fakes["claude"].Requests()) != 0 {
		t.Fatalf("resp=%v attempts=%+v err=%v", resp, attempts, err)
	}
}

func TestRouteChangeAffectsNextCall(t *testing.T) {
	e := setup(t)
	e.fakes["claude"] = fake.New("claude", fake.Text("claude"))
	e.fakes["local"] = fake.New("local", fake.Text("local"))
	e.route("claude", "big", nil, nil, `{}`)
	r1, _, _ := e.r.Chat(context.Background(), call(e.ws), llm.ChatRequest{Messages: []llm.Message{llm.UserText("x")}})
	e.route("local", "qwen", nil, nil, `{}`) // as the Settings UI would
	r2, _, _ := e.r.Chat(context.Background(), call(e.ws), llm.ChatRequest{Messages: []llm.Message{llm.UserText("x")}})
	if r1.Text() != "claude" || r2.Text() != "local" {
		t.Fatalf("got %q then %q", r1.Text(), r2.Text())
	}
}

func TestUISecretWinsOverEnv(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	id := uuid.New()
	sealed, err := e.box.Seal("sk-from-ui", id, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.q.CreateSecret(ctx, store.CreateSecretParams{ID: id, Name: "Claude", Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, KeyID: sealed.KeyID, Last4: secrets.Last4("sk-from-ui")}); err != nil {
		t.Fatal(err)
	}
	row, _ := e.q.GetProvider(ctx, "claude")
	if _, err := e.q.UpdateProvider(ctx, store.UpdateProviderParams{ID: row.ID, Kind: row.Kind, BaseUrl: row.BaseUrl, IsLocal: row.IsLocal,
		ApiKeySecretID: &id, ApiKeyEnv: row.ApiKeyEnv, Headers: row.Headers, Options: row.Options, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	e.fakes["claude"] = fake.New("claude", fake.Text("ok"))
	e.route("claude", "big", nil, nil, `{}`)
	if _, _, err := e.r.Chat(ctx, call(e.ws), llm.ChatRequest{Messages: []llm.Message{llm.UserText("x")}}); err != nil {
		t.Fatal(err)
	}
	if e.keys["claude"] != "sk-from-ui" {
		t.Fatalf("factory got %q, want the UI secret", e.keys["claude"])
	}
	for _, run := range e.runs() {
		if run.Error != nil && strings.Contains(*run.Error, "sk-from-ui") {
			t.Fatal("secret leaked into model_runs")
		}
	}
}

func TestStreamFailsOverBeforeOutput(t *testing.T) {
	e := setup(t)
	e.fakes["claude"] = fake.New("claude", fake.Fail(http.StatusServiceUnavailable))
	e.fakes["local"] = fake.New("local", fake.Text("streamed"))
	e.route("claude", "big", strp("local"), strp("qwen"), `{}`)
	res, err := e.r.Resolve(context.Background(), e.ws, "draft")
	if err != nil {
		t.Fatal(err)
	}
	ch, _ := e.r.Stream(context.Background(), call(e.ws), res.Targets, llm.ChatRequest{Messages: []llm.Message{llm.UserText("x")}})
	resp, err := llm.Collect(ch)
	if err != nil || resp.Text() != "streamed" {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
}

// slowProvider records the peak number of simultaneous calls.
type slowProvider struct {
	mu        sync.Mutex
	cur, peak int
}

func (p *slowProvider) ID() string { return "local" }
func (p *slowProvider) Chat(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
	p.mu.Lock()
	p.cur++
	p.peak = max(p.peak, p.cur)
	p.mu.Unlock()
	time.Sleep(30 * time.Millisecond)
	p.mu.Lock()
	p.cur--
	p.mu.Unlock()
	return &llm.ChatResponse{Parts: []llm.Part{llm.TextPart("ok")}}, nil
}
func (p *slowProvider) Stream(context.Context, llm.ChatRequest) (<-chan llm.Event, error) {
	return nil, nil
}
func (p *slowProvider) ListModels(context.Context) ([]llm.ModelInfo, error) { return nil, nil }

func TestProviderConcurrencyLimit(t *testing.T) {
	e := setup(t)
	sp := &slowProvider{}
	e.r.Build = func(store.ModelProvider, string) (llm.Provider, error) { return sp, nil }
	row, _ := e.q.GetProvider(context.Background(), "local")
	if _, err := e.q.UpdateProvider(context.Background(), store.UpdateProviderParams{ID: row.ID, Kind: row.Kind, BaseUrl: row.BaseUrl, IsLocal: true,
		ApiKeyEnv: row.ApiKeyEnv, Headers: row.Headers, Options: []byte(`{"max_concurrency":1}`), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	e.route("local", "qwen", nil, nil, `{}`)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := e.r.Chat(context.Background(), call(e.ws), llm.ChatRequest{Messages: []llm.Message{llm.UserText("x")}}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if sp.peak != 1 {
		t.Fatalf("peak concurrency = %d, want 1", sp.peak)
	}
}
