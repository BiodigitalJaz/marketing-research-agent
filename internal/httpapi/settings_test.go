package httpapi_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/httpapi"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm/fake"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm/router"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/mcp"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/secrets"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/testutil/pgtest"
)

const pastedKey = "sk-ant-super-secret-0000-WXYZ"

type settingsEnv struct {
	*env
	admin *client
	fakes map[string]*fake.Provider
}

func newSettingsEnv(t *testing.T) *settingsEnv {
	t.Helper()
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	box, _ := secrets.FromEnv(func(n string) string {
		if n == secrets.EnvKey {
			return base64.StdEncoding.EncodeToString(k)
		}
		return ""
	})
	fakes := map[string]*fake.Provider{}
	e := newEnvWith(t, func(pool *pgxpool.Pool, d *httpapi.Deps) {
		r := router.New(pool, box, func(string) string { return "" }, pgtest.DiscardLogger())
		r.Build = func(row store.ModelProvider, key string) (llm.Provider, error) {
			if f, ok := fakes[row.ID]; ok {
				return f, nil
			}
			return router.BuildProvider(row, key)
		}
		d.Router, d.Secrets = r, box
		d.MCPConnect = func(ctx context.Context, cfg mcp.ServerConfig) (*mcp.Conn, error) {
			return mcp.ConnectTransport(ctx, cfg, testMCPServer(t))
		}
	})
	ws := e.workspace("Benchwright")
	e.user("admin@a.com", ws, store.MemberRoleAdmin)
	e.user("editor@a.com", ws, store.MemberRoleEditor)
	admin := e.client()
	admin.login("admin@a.com")
	return &settingsEnv{env: e, admin: admin, fakes: fakes}
}

func testMCPServer(t *testing.T) sdk.Transport {
	t.Helper()
	s := sdk.NewServer(&sdk.Implementation{Name: "kb", Version: "1"}, nil)
	type in struct {
		Query string `json:"query"`
	}
	sdk.AddTool(s, &sdk.Tool{Name: "search_docs", Description: "Search docs", Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}},
		func(_ context.Context, _ *sdk.CallToolRequest, a in) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "doc about " + a.Query}}}, nil, nil
		})
	sdk.AddTool(s, &sdk.Tool{Name: "send_email", Annotations: &sdk.ToolAnnotations{ReadOnlyHint: false}},
		func(_ context.Context, _ *sdk.CallToolRequest, _ in) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "sent"}}}, nil, nil
		})
	st, ct := sdk.NewInMemoryTransports()
	if _, err := s.Connect(context.Background(), st, nil); err != nil {
		t.Fatal(err)
	}
	return ct
}

// mustOK fails unless code is in want; returns body.
func (s *settingsEnv) call(method, path string, body any, want ...int) []byte {
	s.t.Helper()
	code, out := s.admin.do(method, path, body)
	for _, w := range want {
		if code == w {
			if bytes.Contains(out, []byte(pastedKey)) {
				s.t.Fatalf("%s %s leaked the secret value", method, path)
			}
			return out
		}
	}
	s.t.Fatalf("%s %s = %d %s (want %v)", method, path, code, out, want)
	return nil
}

func TestSettingsRequireAdmin(t *testing.T) {
	s := newSettingsEnv(t)
	ed := s.client()
	ed.login("editor@a.com")
	for _, path := range []string{"/settings/providers", "/settings/secrets", "/settings/routes", "/settings/workspace"} {
		if code, _ := ed.do(http.MethodGet, path, nil); code != http.StatusForbidden {
			t.Errorf("editor GET %s = %d, want 403", path, code)
		}
	}
}

func TestProviderWithPastedKeyIsEncryptedAndWriteOnly(t *testing.T) {
	s := newSettingsEnv(t)
	ctx := context.Background()
	out := s.call(http.MethodPost, "/settings/providers", map[string]any{
		"id": "claude", "name": "Claude", "kind": "anthropic", "api_key": pastedKey,
	}, http.StatusCreated)
	var p struct {
		APIKey struct {
			Source string `json:"source"`
			Last4  string `json:"last4"`
		} `json:"api_key"`
	}
	_ = json.Unmarshal(out, &p)
	if p.APIKey.Source != "secret" || p.APIKey.Last4 != "WXYZ" {
		t.Fatalf("api_key status = %+v", p.APIKey)
	}
	var ct []byte
	if err := s.pool.QueryRow(ctx, "SELECT ciphertext FROM secrets LIMIT 1").Scan(&ct); err != nil || bytes.Contains(ct, []byte("super-secret")) {
		t.Fatalf("secret not encrypted at rest: %v", err)
	}
	s.call(http.MethodGet, "/settings/providers", nil, http.StatusOK)
	s.call(http.MethodGet, "/settings/secrets", nil, http.StatusOK)
	var audit string
	_ = s.pool.QueryRow(ctx, "SELECT string_agg(meta::text, ' ') FROM audit_log").Scan(&audit)
	if strings.Contains(audit, pastedKey) {
		t.Fatal("secret leaked into audit log")
	}
	// Headers may not carry credentials.
	s.call(http.MethodPost, "/settings/providers", map[string]any{"id": "x", "kind": "openai_compat", "base_url": "http://h/v1", "headers": map[string]string{"Authorization": "Bearer y"}}, http.StatusBadRequest)
}

func TestSecretsDisabledWithoutMasterKey(t *testing.T) {
	e := newEnv(t)
	ws := e.workspace("A")
	e.user("admin@a.com", ws, store.MemberRoleAdmin)
	c := e.client()
	c.login("admin@a.com")
	code, body := c.do(http.MethodPost, "/settings/secrets", map[string]any{"name": "k", "value": "v"})
	if code != http.StatusConflict || errCode(body) != "encryption_disabled" {
		t.Fatalf("got %d %s", code, body)
	}
}

func TestRouteValidationAgainstCapabilities(t *testing.T) {
	s := newSettingsEnv(t)
	s.call(http.MethodPost, "/settings/providers", map[string]any{"id": "local", "kind": "openai_compat", "base_url": "http://192.168.0.10:1234/v1", "is_local": true}, http.StatusCreated)
	s.call(http.MethodPut, "/settings/routes/draft", map[string]any{"provider_id": "local", "model": "undeclared"}, http.StatusBadRequest)
	s.call(http.MethodPost, "/settings/models", map[string]any{"provider_id": "local", "model": "plain", "capabilities": map[string]any{}, "enabled": true}, http.StatusOK)
	s.call(http.MethodPut, "/settings/routes/research", map[string]any{"provider_id": "local", "model": "plain"}, http.StatusBadRequest) // no tools
	s.call(http.MethodPut, "/settings/routes/draft", map[string]any{"provider_id": "local", "model": "plain", "params": map[string]any{"temperature": 0.4}}, http.StatusBadRequest)
	s.call(http.MethodPut, "/settings/routes/draft", map[string]any{"provider_id": "local", "model": "plain", "params": map[string]any{"thinking": map[string]any{"effort": "high"}}}, http.StatusBadRequest)
	s.call(http.MethodPost, "/settings/models", map[string]any{"provider_id": "local", "model": "local-27b",
		"capabilities": map[string]any{"tools": true, "sampling": true, "thinking": "effort", "json_schema": true, "vision": true}, "enabled": true}, http.StatusOK)
	s.call(http.MethodPut, "/settings/routes/research", map[string]any{"provider_id": "local", "model": "local-27b",
		"params": map[string]any{"temperature": 0.4, "thinking": map[string]any{"effort": "high"}, "max_steps": 40}}, http.StatusOK)
	out := s.call(http.MethodGet, "/settings/routes", nil, http.StatusOK)
	var routes struct {
		Items []struct {
			Task      string   `json:"task"`
			Usable    bool     `json:"usable"`
			Effective []string `json:"effective"`
		} `json:"items"`
	}
	_ = json.Unmarshal(out, &routes)
	for _, r := range routes.Items {
		if r.Task == "research" && (!r.Usable || len(r.Effective) != 1 || r.Effective[0] != "local/local-27b") {
			t.Fatalf("research route status = %+v", r)
		}
	}
}

func TestPromptVersioning(t *testing.T) {
	s := newSettingsEnv(t)
	ctx := context.Background()
	if err := store.New(s.pool).InsertDefaultPromptIfMissing(ctx, store.InsertDefaultPromptIfMissingParams{Task: "summarize", Version: 1, Body: "default v1"}); err != nil {
		t.Fatal(err)
	}
	s.call(http.MethodPost, "/settings/prompts", map[string]any{"task": "summarize", "body": "custom v2", "activate": true}, http.StatusCreated)
	list := func() map[int]bool {
		var r struct {
			Items []struct {
				ID      string `json:"id"`
				Version int    `json:"version"`
				Active  bool   `json:"active"`
			} `json:"items"`
		}
		_ = json.Unmarshal(s.call(http.MethodGet, "/settings/prompts?task=summarize", nil, http.StatusOK), &r)
		out := map[int]bool{}
		for _, i := range r.Items {
			out[i.Version] = i.Active
		}
		return out
	}
	if got := list(); !got[2] || got[1] {
		t.Fatalf("after activating v2: %v", got)
	}
	var defID string
	_ = s.pool.QueryRow(ctx, "SELECT id::text FROM prompts WHERE workspace_id IS NULL").Scan(&defID)
	s.call(http.MethodPost, "/settings/prompts/"+defID+"/activate", nil, http.StatusNoContent)
	if got := list(); !got[1] || got[2] {
		t.Fatalf("after reverting to default: %v", got)
	}
}

func TestWorkspaceSettingsLocalOnlyAndReembed(t *testing.T) {
	s := newSettingsEnv(t)
	s.call(http.MethodPost, "/settings/providers", map[string]any{"id": "openai", "kind": "openai_compat", "base_url": "https://api.openai.com/v1"}, http.StatusCreated)
	s.call(http.MethodPost, "/settings/providers", map[string]any{"id": "local", "kind": "openai_compat", "base_url": "http://192.168.0.10:1234/v1", "is_local": true}, http.StatusCreated)
	s.call(http.MethodPost, "/portfolio", map[string]any{"kind": "talk", "title": "T", "summary": "S"}, http.StatusCreated)

	code, body := s.admin.do(http.MethodPut, "/settings/workspace", map[string]any{"local_only": true,
		"embeddings": map[string]any{"provider_id": "openai", "model": "text-embedding-3-small", "dimensions": 1536}})
	if code != http.StatusConflict || errCode(body) != "hosted_embeddings" {
		t.Fatalf("local-only with hosted embeddings = %d %s", code, body)
	}
	before := s.embedJobs()
	s.call(http.MethodPut, "/settings/workspace", map[string]any{"local_only": true,
		"embeddings": map[string]any{"provider_id": "local", "model": "text-embedding-nomic-embed-text-v1.5", "dimensions": 768}}, http.StatusOK)
	if s.embedJobs() != before+1 {
		t.Fatalf("changing the embedding model must re-embed; jobs %d -> %d", before, s.embedJobs())
	}
}

func (s *settingsEnv) embedJobs() int {
	var n int
	_ = s.pool.QueryRow(context.Background(), "SELECT count(*) FROM river_job WHERE kind = 'embed_entity'").Scan(&n)
	return n
}

func TestMCPAllowlistEnforced(t *testing.T) {
	s := newSettingsEnv(t)
	body := map[string]any{"name": "kb", "transport": "streamable_http", "url": "https://mcp.example/mcp", "allowed_tools": []string{"search_docs", "send_email"}}
	code, out := s.admin.do(http.MethodPost, "/settings/mcp-servers", body)
	if code != http.StatusBadRequest || !strings.Contains(string(out), "send_email") {
		t.Fatalf("write tool allowlisted: %d %s", code, out)
	}
	body["allowed_tools"] = []string{"search_docs"}
	out = s.call(http.MethodPost, "/settings/mcp-servers", body, http.StatusCreated)
	var srv struct{ ID string }
	_ = json.Unmarshal(out, &srv)
	out = s.call(http.MethodPost, "/settings/mcp-servers/"+srv.ID+"/test", nil, http.StatusOK)
	if !strings.Contains(string(out), `"name":"send_email"`) || !strings.Contains(string(out), `"allowable":false`) {
		t.Fatalf("test should list tools with allowability: %s", out)
	}
}

func TestPlaygroundRunsMCPToolLoop(t *testing.T) {
	s := newSettingsEnv(t)
	s.call(http.MethodPost, "/settings/providers", map[string]any{"id": "local", "kind": "openai_compat", "base_url": "http://192.168.0.10:1234/v1", "is_local": true}, http.StatusCreated)
	s.call(http.MethodPost, "/settings/models", map[string]any{"provider_id": "local", "model": "qwen", "capabilities": map[string]any{"tools": true}, "enabled": true}, http.StatusOK)
	out := s.call(http.MethodPost, "/settings/mcp-servers", map[string]any{"name": "kb", "transport": "streamable_http", "url": "https://mcp.example/mcp", "allowed_tools": []string{"search_docs"}}, http.StatusCreated)
	var srv struct{ ID string }
	_ = json.Unmarshal(out, &srv)

	s.fakes["local"] = fake.New("local",
		fake.Reply{Resp: &llm.ChatResponse{StopReason: llm.StopToolUse, Usage: llm.Usage{InputTokens: 5, OutputTokens: 3},
			ToolCalls: []llm.ToolCall{{ID: "c1", Name: "mcp__kb__search_docs", Args: json.RawMessage(`{"query":"pricing"}`)}}}},
		fake.Text("Pricing is documented."),
	)
	code, body := s.admin.do(http.MethodPost, "/settings/playground", map[string]any{
		"provider_id": "local", "model": "qwen", "mcp_server_ids": []string{srv.ID},
		"messages": []map[string]any{{"role": "user", "text": "What does the pricing doc say?"}},
	})
	if code != http.StatusOK {
		t.Fatalf("playground = %d %s", code, body)
	}
	events := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(body))
	var ev string
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event: ") {
			ev = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			events[ev] += strings.TrimPrefix(line, "data: ")
		}
	}
	if !strings.Contains(events["tool_result"], "doc about pricing") || !strings.Contains(events["tool_result"], "external_content") || events["done"] == "" {
		t.Fatalf("events = %v", events)
	}
	reqs := s.fakes["local"].Requests()
	if len(reqs) != 2 || len(reqs[0].Tools) != 1 || reqs[0].Tools[0].Name != "mcp__kb__search_docs" || !strings.Contains(reqs[0].System, "external_content") {
		t.Fatalf("model requests = %+v", reqs)
	}
	var runs int
	_ = s.pool.QueryRow(context.Background(), "SELECT count(*) FROM model_runs WHERE task = 'playground'").Scan(&runs)
	if runs != 2 {
		t.Fatalf("playground model_runs = %d, want 2", runs)
	}
}

func TestPlaygroundRejectsMalformedMessages(t *testing.T) {
	s := newSettingsEnv(t)
	s.call(http.MethodPost, "/settings/providers", map[string]any{"id": "local", "kind": "openai_compat", "base_url": "http://192.168.0.10:1234/v1", "is_local": true}, http.StatusCreated)
	s.call(http.MethodPost, "/settings/models", map[string]any{"provider_id": "local", "model": "qwen", "enabled": true}, http.StatusOK)
	s.fakes["local"] = fake.New("local", fake.Text("unused"))
	tests := []struct {
		name string
		msg  map[string]any
		want string
	}{
		{"content instead of text", map[string]any{"role": "user", "content": "hi"}, `needs text`},
		{"blank text", map[string]any{"role": "user", "text": "   "}, `needs text`},
		{"unknown role", map[string]any{"role": "system", "text": "hi"}, `role must be user or assistant`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, body := s.admin.do(http.MethodPost, "/settings/playground", map[string]any{"provider_id": "local", "model": "qwen", "messages": []map[string]any{tt.msg}})
			if code != http.StatusBadRequest || !strings.Contains(string(body), tt.want) {
				t.Errorf("playground = %d %s, want 400 mentioning %q", code, body, tt.want)
			}
		})
	}
	if n := len(s.fakes["local"].Requests()); n != 0 {
		t.Errorf("malformed requests must not reach the model; it got %d", n)
	}
}
