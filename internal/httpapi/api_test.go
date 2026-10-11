package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/auth"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/httpapi"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/jobs"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm/router"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/testutil/pgtest"
)

func init() { gin.SetMode(gin.TestMode) }

const testPassword = "correct horse battery staple"

type env struct {
	t    *testing.T
	pool *pgxpool.Pool
	srv  *httptest.Server
	q    *store.Queries
}

func newEnv(t *testing.T) *env { return newEnvWith(t, nil) }

// newEnvWith builds the API; configure may adjust deps (secrets box, router
// provider factory, MCP connector) before the router is built.
func newEnvWith(t *testing.T, configure func(pool *pgxpool.Pool, d *httpapi.Deps)) *env {
	t.Helper()
	pool := pgtest.NewMigratedDB(t)
	log := pgtest.DiscardLogger()
	ins, err := jobs.NewInsertClient(pool, log)
	if err != nil {
		t.Fatal(err)
	}
	d := httpapi.Deps{Log: log, DB: pool, Pool: pool, Jobs: ins,
		Router: router.New(pool, nil, func(string) string { return "" }, log)}
	if configure != nil {
		configure(pool, &d)
	}
	h := httpapi.NewRouter(d)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &env{t: t, pool: pool, srv: srv, q: store.New(pool)}
}

// user creates a user with the given role in ws.
func (e *env) user(email string, ws uuid.UUID, role store.MemberRole) {
	e.t.Helper()
	hash, err := auth.HashPassword(testPassword, auth.Argon2Params{Memory: 1024, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32})
	if err != nil {
		e.t.Fatal(err)
	}
	u, err := e.q.CreateUser(context.Background(), store.CreateUserParams{Email: email, Name: email, PasswordHash: hash})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.q.AddMembership(context.Background(), store.AddMembershipParams{WorkspaceID: ws, UserID: u.ID, Role: role}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) workspace(name string) uuid.UUID {
	e.t.Helper()
	w, err := e.q.CreateWorkspace(context.Background(), name)
	if err != nil {
		e.t.Fatal(err)
	}
	return w.ID
}

type client struct {
	e    *env
	http *http.Client
	csrf string
	ws   string
}

func (e *env) client() *client {
	jar, _ := cookiejar.New(nil)
	return &client{e: e, http: &http.Client{Jar: jar}}
}

func (c *client) do(method, path string, body any) (int, []byte) {
	c.e.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.e.srv.URL+"/api/v1"+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.csrf != "" {
		req.Header.Set(httpapi.CSRFHeader, c.csrf)
	}
	if c.ws != "" {
		req.Header.Set(httpapi.WorkspaceHeader, c.ws)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func (c *client) login(email string) {
	c.e.t.Helper()
	code, body := c.do(http.MethodPost, "/auth/login", map[string]string{"email": email, "password": testPassword})
	if code != http.StatusOK {
		c.e.t.Fatalf("login %s: %d %s", email, code, body)
	}
	var s struct {
		CSRFToken string `json:"csrf_token"`
	}
	_ = json.Unmarshal(body, &s)
	c.csrf = s.CSRFToken
}

func errCode(body []byte) string {
	var e struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	return e.Error.Code
}

func TestLoginFlow(t *testing.T) {
	e := newEnv(t)
	ws := e.workspace("Benchwright")
	e.user("admin@example.com", ws, store.MemberRoleAdmin)

	c := e.client()
	if code, _ := c.do(http.MethodGet, "/auth/me", nil); code != http.StatusUnauthorized {
		t.Fatalf("me before login = %d", code)
	}
	code, body := c.do(http.MethodPost, "/auth/login", map[string]string{"email": "ADMIN@example.com", "password": "wrong"})
	if code != http.StatusUnauthorized || errCode(body) != "invalid_credentials" {
		t.Fatalf("bad password = %d %s", code, body)
	}
	code, body = c.do(http.MethodPost, "/auth/login", map[string]string{"email": "nobody@example.com", "password": "x"})
	if code != http.StatusUnauthorized || errCode(body) != "invalid_credentials" {
		t.Fatalf("unknown user = %d %s", code, body)
	}

	c.login("Admin@Example.com") // case-insensitive
	code, body = c.do(http.MethodGet, "/auth/me", nil)
	if code != http.StatusOK {
		t.Fatalf("me = %d %s", code, body)
	}
	var me struct {
		User       struct{ Email string }
		Workspaces []struct{ Name, Role string }
		CSRFToken  string `json:"csrf_token"`
	}
	_ = json.Unmarshal(body, &me)
	if me.User.Email != "admin@example.com" || len(me.Workspaces) != 1 || me.Workspaces[0].Role != "admin" || me.CSRFToken != c.csrf {
		t.Fatalf("me = %+v", me)
	}

	// Session id in the DB is a hash, not the cookie value.
	u, _ := url.Parse(e.srv.URL)
	for _, ck := range c.http.Jar.Cookies(u) {
		if ck.Name == "session" {
			var n int
			_ = e.pool.QueryRow(context.Background(), "SELECT count(*) FROM sessions WHERE id = $1", ck.Value).Scan(&n)
			if n != 0 {
				t.Fatal("raw session token stored in DB")
			}
		}
	}

	// Logout requires CSRF, then invalidates the session.
	saved := c.csrf
	c.csrf = ""
	if code, body := c.do(http.MethodPost, "/auth/logout", nil); code != http.StatusForbidden || errCode(body) != "csrf_failed" {
		t.Fatalf("logout without csrf = %d %s", code, body)
	}
	c.csrf = saved
	if code, _ := c.do(http.MethodPost, "/auth/logout", nil); code != http.StatusNoContent {
		t.Fatalf("logout = %d", code)
	}
	if code, _ := c.do(http.MethodGet, "/auth/me", nil); code != http.StatusUnauthorized {
		t.Fatalf("me after logout = %d", code)
	}
	var audits int
	_ = e.pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_log WHERE workspace_id = $1 AND action IN ('login','logout')", ws).Scan(&audits)
	if audits != 2 {
		t.Errorf("login/logout audit rows = %d, want 2", audits)
	}
}

func TestLoginRejectsNonJSONAndRateLimits(t *testing.T) {
	e := newEnv(t)
	ws := e.workspace("Benchwright")
	e.user("a@example.com", ws, store.MemberRoleAdmin)

	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/api/v1/auth/login", bytes.NewBufferString("email=a@example.com&password=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("form login = %d, want 415", resp.StatusCode)
	}

	c := e.client()
	var last int
	for range 7 {
		last, _ = c.do(http.MethodPost, "/auth/login", map[string]string{"email": "a@example.com", "password": "wrong"})
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("after 7 failures = %d, want 429", last)
	}
}

func TestRBACAndWorkspaceIsolation(t *testing.T) {
	e := newEnv(t)
	wsA, wsB := e.workspace("A"), e.workspace("B")
	e.user("editor@a.com", wsA, store.MemberRoleEditor)
	e.user("viewer@a.com", wsA, store.MemberRoleViewer)
	e.user("editor@b.com", wsB, store.MemberRoleEditor)

	ed := e.client()
	ed.login("editor@a.com")
	code, body := ed.do(http.MethodPost, "/portfolio", map[string]any{"kind": "blog_post", "title": "T", "summary": "S", "url": "https://example.com/x"})
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
	var item struct{ ID string }
	_ = json.Unmarshal(body, &item)

	v := e.client()
	v.login("viewer@a.com")
	if code, _ := v.do(http.MethodGet, "/portfolio", nil); code != http.StatusOK {
		t.Fatalf("viewer list = %d", code)
	}
	if code, body := v.do(http.MethodPut, "/brand/profile", map[string]any{}); code != http.StatusForbidden {
		t.Fatalf("viewer put profile = %d %s", code, body)
	}

	b := e.client()
	b.login("editor@b.com")
	if code, _ := b.do(http.MethodGet, "/portfolio/"+item.ID, nil); code != http.StatusNotFound {
		t.Fatalf("cross-workspace get = %d, want 404", code)
	}
	if code, _ := b.do(http.MethodDelete, "/portfolio/"+item.ID, nil); code != http.StatusNotFound {
		t.Fatalf("cross-workspace delete = %d, want 404", code)
	}
	b.ws = wsA.String()
	if code, _ := b.do(http.MethodGet, "/portfolio", nil); code != http.StatusForbidden {
		t.Fatalf("foreign workspace header = %d, want 403", code)
	}
}

func TestPortfolioWritesEnqueueEmbedAndAudit(t *testing.T) {
	e := newEnv(t)
	ws := e.workspace("A")
	e.user("ed@a.com", ws, store.MemberRoleEditor)
	c := e.client()
	c.login("ed@a.com")

	for _, bad := range []map[string]any{
		{"kind": "nope", "title": "T", "summary": "S"},
		{"kind": "blog_post", "title": " ", "summary": "S"},
		{"kind": "blog_post", "title": "T", "summary": "S", "url": "javascript:alert(1)"},
	} {
		if code, body := c.do(http.MethodPost, "/portfolio", bad); code != http.StatusBadRequest {
			t.Errorf("invalid %v = %d %s", bad, code, body)
		}
	}

	_, body := c.do(http.MethodPost, "/portfolio", map[string]any{"kind": "talk", "title": "  Talk ", "summary": "About guardrails", "tags": map[string]any{"problems": []string{"a", "A", " "}}})
	var item struct {
		ID    string
		Title string
		Tags  struct{ Problems []string }
	}
	_ = json.Unmarshal(body, &item)
	if item.Title != "Talk" || len(item.Tags.Problems) != 1 {
		t.Fatalf("not normalized: %+v", item)
	}
	var jobsN, auditN int
	ctx := context.Background()
	_ = e.pool.QueryRow(ctx, "SELECT count(*) FROM river_job WHERE kind = 'embed_entity' AND args->>'entity_id' = $1", item.ID).Scan(&jobsN)
	_ = e.pool.QueryRow(ctx, "SELECT count(*) FROM audit_log WHERE target_id = $1 AND action = 'portfolio.create'", item.ID).Scan(&auditN)
	if jobsN != 1 || auditN != 1 {
		t.Fatalf("embed jobs = %d, audit = %d", jobsN, auditN)
	}

	if code, _ := c.do(http.MethodPut, "/portfolio/"+item.ID, map[string]any{"kind": "talk", "title": "Talk v2", "summary": "S"}); code != http.StatusOK {
		t.Fatalf("update = %d", code)
	}
	_ = e.pool.QueryRow(ctx, "SELECT count(*) FROM river_job WHERE kind = 'embed_entity' AND args->>'entity_id' = $1", item.ID).Scan(&jobsN)
	if jobsN != 2 {
		t.Fatalf("update should re-enqueue embed; jobs = %d", jobsN)
	}
	if code, _ := c.do(http.MethodDelete, "/portfolio/"+item.ID, nil); code != http.StatusNoContent {
		t.Fatalf("delete = %d", code)
	}
}

func TestKeywordsAndCompetitors(t *testing.T) {
	e := newEnv(t)
	ws := e.workspace("A")
	e.user("ed@a.com", ws, store.MemberRoleEditor)
	c := e.client()
	c.login("ed@a.com")

	kw := map[string]any{"term": "AI planning bot", "kind": "track", "lane": "inbound"}
	if code, body := c.do(http.MethodPost, "/keywords", kw); code != http.StatusCreated {
		t.Fatalf("create keyword = %d %s", code, body)
	}
	if code, body := c.do(http.MethodPost, "/keywords", kw); code != http.StatusConflict {
		t.Fatalf("duplicate keyword = %d %s", code, body)
	}
	if code, _ := c.do(http.MethodPost, "/keywords", map[string]any{"term": "x", "kind": "track", "lane": "bogus"}); code != http.StatusBadRequest {
		t.Fatalf("bad lane = %d", code)
	}
	if code, body := c.do(http.MethodPost, "/competitors", map[string]any{"name": "Basecamp", "domains": []string{"basecamp.com"}}); code != http.StatusCreated {
		t.Fatalf("create competitor = %d %s", code, body)
	}
	code, body := c.do(http.MethodPut, "/brand/profile", map[string]any{"services": []string{"Agent"}, "tone": map[string]any{"voice": "Blunt", "dont": []string{"guaranteed"}}})
	if code != http.StatusOK {
		t.Fatalf("put profile = %d %s", code, body)
	}
	_, body = c.do(http.MethodGet, "/brand/profile", nil)
	var p struct {
		Profile struct {
			Tone struct{ Dont []string }
		}
		UpdatedAt *time.Time `json:"updated_at"`
	}
	_ = json.Unmarshal(body, &p)
	if len(p.Profile.Tone.Dont) != 1 || p.UpdatedAt == nil {
		t.Fatalf("profile round trip: %s", body)
	}
}
