package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/config"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/httpapi"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/testutil/pgtest"
)

func TestFirstRunSetup(t *testing.T) {
	cfg, err := config.Load("../../config.example.yaml", func() []string { return nil })
	if err != nil {
		t.Fatal(err)
	}
	e := newEnvWith(t, func(_ *pgxpool.Pool, d *httpapi.Deps) { d.Config = cfg })
	ctx := context.Background()
	c := e.client()

	code, body := c.do(http.MethodGet, "/setup/status", nil)
	var st struct {
		NeedsSetup    bool     `json:"needs_setup"`
		ExampleBrands []string `json:"example_brands"`
	}
	_ = json.Unmarshal(body, &st)
	if code != http.StatusOK || !st.NeedsSetup || len(st.ExampleBrands) == 0 {
		t.Fatalf("status = %d %s", code, body)
	}

	if err := httpapi.PrepareSetup(ctx, e.pool, "the-setup-token", "http://localhost:3000", pgtest.DiscardLogger()); err != nil {
		t.Fatal(err)
	}
	req := map[string]string{"token": "wrong", "email": "Owner@Example.com", "name": "Owner", "password": testPassword, "workspace": "Benchwright", "example_brand": "benchwright"}
	if code, body := c.do(http.MethodPost, "/setup", req); code != http.StatusUnauthorized || errCode(body) != "invalid_setup_token" {
		t.Fatalf("wrong token = %d %s", code, body)
	}
	req["token"] = "the-setup-token"
	req["password"] = "short"
	if code, body := c.do(http.MethodPost, "/setup", req); code != http.StatusBadRequest {
		t.Fatalf("short password = %d %s", code, body)
	}
	req["password"] = testPassword
	code, body = c.do(http.MethodPost, "/setup", req)
	if code != http.StatusOK {
		t.Fatalf("setup = %d %s", code, body)
	}
	var sess struct {
		User       struct{ Email string }
		Workspaces []struct{ Name, Role string }
		CSRFToken  string `json:"csrf_token"`
	}
	_ = json.Unmarshal(body, &sess)
	if sess.User.Email != "owner@example.com" || len(sess.Workspaces) != 1 || sess.Workspaces[0].Name != "Benchwright" || sess.Workspaces[0].Role != "admin" {
		t.Fatalf("session = %+v", sess)
	}
	c.csrf = sess.CSRFToken
	if code, _ := c.do(http.MethodGet, "/auth/me", nil); code != http.StatusOK {
		t.Fatalf("setup should sign the admin in: me = %d", code)
	}

	// The install is claimed: the token is gone and setup refuses.
	var tokens int
	_ = e.pool.QueryRow(ctx, "SELECT count(*) FROM setup_token").Scan(&tokens)
	if tokens != 0 {
		t.Error("setup token should be deleted after setup")
	}
	if code, body := e.client().do(http.MethodPost, "/setup", req); code != http.StatusConflict || errCode(body) != "already_set_up" {
		t.Fatalf("second setup = %d %s", code, body)
	}
	_, body = c.do(http.MethodGet, "/setup/status", nil)
	_ = json.Unmarshal(body, &st)
	if st.NeedsSetup {
		t.Error("status still says setup is needed")
	}
	if err := httpapi.PrepareSetup(ctx, e.pool, "", "", pgtest.DiscardLogger()); err != nil {
		t.Fatal(err)
	}
	_ = e.pool.QueryRow(ctx, "SELECT count(*) FROM setup_token").Scan(&tokens)
	if tokens != 0 {
		t.Error("PrepareSetup must do nothing once a user exists")
	}

	// The checklist reflects the imported example brand and the disabled
	// starter monitor.
	code, body = c.do(http.MethodGet, "/setup/checklist", nil)
	var cl struct {
		Items []struct {
			ID   string
			Done bool
		}
		Total int
	}
	_ = json.Unmarshal(body, &cl)
	if code != http.StatusOK || cl.Total != 6 {
		t.Fatalf("checklist = %d %s", code, body)
	}
	done := map[string]bool{}
	for _, it := range cl.Items {
		done[it.ID] = it.Done
	}
	if !done["brand"] || !done["keywords"] || done["monitor"] || !done["search"] || done["llm"] || done["embeddings"] {
		t.Errorf("checklist = %v (llm and embeddings need a real call first)", done)
	}

	// A call to some other model doesn't complete the LLM step; calls to the
	// models the routes use do. (The example config's routes fall back to a
	// keyless local provider: qwen3:14b and qwen3:32b.)
	w, err := e.q.GetWorkspaceByName(ctx, "Benchwright")
	if err != nil {
		t.Fatal(err)
	}
	llmDone := func() bool {
		_, body := c.do(http.MethodGet, "/setup/checklist", nil)
		_ = json.Unmarshal(body, &cl)
		for _, it := range cl.Items {
			if it.ID == "llm" {
				return it.Done
			}
		}
		t.Fatal("no llm step")
		return false
	}
	run := func(model string) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, "INSERT INTO model_runs (workspace_id, task, provider_id, model) VALUES ($1, 'playground', 'local', $2)", w.ID, model); err != nil {
			t.Fatal(err)
		}
	}
	run("some-other-model")
	if llmDone() {
		t.Error("a playground call to an unrelated model must not complete the llm step")
	}
	run("qwen3:14b")
	if llmDone() {
		t.Error("qwen3:32b (draft, research) has not answered yet")
	}
	run("qwen3:32b")
	if !llmDone() {
		_, body := c.do(http.MethodGet, "/setup/checklist", nil)
		t.Errorf("llm step should be done once every routed model has answered: %s", body)
	}
}

func TestSetupWithoutToken(t *testing.T) {
	cfg, err := config.Load("../../config.example.yaml", func() []string { return nil })
	if err != nil {
		t.Fatal(err)
	}
	e := newEnvWith(t, func(_ *pgxpool.Pool, d *httpapi.Deps) { d.Config = cfg })
	// Without PrepareSetup there is no token, so no token is accepted.
	req := map[string]string{"token": "anything", "email": "a@example.com", "password": testPassword, "workspace": "W"}
	if code, body := e.client().do(http.MethodPost, "/setup", req); code != http.StatusUnauthorized || errCode(body) != "invalid_setup_token" {
		t.Fatalf("setup without a prepared token = %d %s", code, body)
	}
}
