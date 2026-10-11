package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func environ(kv ...string) func() []string {
	return func() []string { return kv }
}

func TestLoadExampleConfig(t *testing.T) {
	path := filepath.Join("..", "..", "config.example.yaml")
	cfg, err := Load(path, environ("ANTHROPIC_API_KEY=sk-test", "OPENAI_API_KEY=sk-test"))
	if err != nil {
		t.Fatalf("example config must load: %v", err)
	}
	if got := cfg.Routes["draft"].Fallback.Provider; got != "local" {
		t.Errorf("draft fallback provider = %q, want local", got)
	}
	if cfg.Server.ShutdownTimeout != 15*time.Second {
		t.Errorf("shutdown timeout = %v", cfg.Server.ShutdownTimeout)
	}
	p, ok := cfg.Provider("claude")
	if !ok || cfg.Secret(p.APIKeyEnv) != "sk-test" {
		t.Errorf("claude provider key not resolved from env: %+v", p)
	}
	if cfg.Search.Backend != "searxng" || cfg.Search.SearXNGURL == "" {
		t.Errorf("search not configured: %+v", cfg.Search)
	}
	if w := cfg.Warnings(); len(w) != 0 {
		t.Errorf("example config should load without warnings: %q", w)
	}
}

func TestExpandDefaults(t *testing.T) {
	path := filepath.Join("..", "..", "config.example.yaml")
	tests := []struct {
		name string
		env  []string
		want string
	}{
		{"default used when unset", nil, "http://localhost:11434/v1"},
		{"env wins over default", []string{"LOCAL_LLM_BASE_URL=http://ollama:11434/v1"}, "http://ollama:11434/v1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(path, environ(tt.env...))
			if err != nil {
				t.Fatal(err)
			}
			p, _ := cfg.Provider("local")
			if p.BaseURL != tt.want {
				t.Errorf("base_url = %q, want %q", p.BaseURL, tt.want)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml"), nil); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestEnvOverrides(t *testing.T) {
	raw := []byte(minimal)
	cfg, err := parse(raw, environ(
		"RESEARCH_AGENT_DATABASE_URL=postgres://override",
		"RESEARCH_AGENT_MODE_OFFLINE=true",
		"RESEARCH_AGENT_RETENTION_RAW_ITEMS_DAYS=30",
		"RESEARCH_AGENT_SEARCH_BRAVE__API_KEY_ENV=MY_BRAVE_KEY",
		"RESEARCH_AGENT_SECRETS_KEY=read-directly",
		"OTHER_INBOX_TIMEZONE=America/Chicago",
		"UNRELATED=1",
	))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.URL != "postgres://override" {
		t.Errorf("database.url = %q", cfg.Database.URL)
	}
	if cfg.Inbox.Timezone != "UTC" {
		t.Errorf("a variable without the prefix must be ignored: timezone = %q", cfg.Inbox.Timezone)
	}
	if !cfg.Mode.Offline {
		t.Error("mode.offline not overridden")
	}
	if cfg.Retention.RawItemsDays != 30 {
		t.Errorf("retention = %d", cfg.Retention.RawItemsDays)
	}
	if cfg.Search.Brave.APIKeyEnv != "MY_BRAVE_KEY" {
		t.Errorf("nested override failed: %q", cfg.Search.Brave.APIKeyEnv)
	}
}

func TestEnvKey(t *testing.T) {
	tests := []struct{ in, want string }{
		{"RESEARCH_AGENT_DATABASE_URL", "database.url"},
		{"RESEARCH_AGENT_SERVER_BASE_URL", "server.base_url"},
		{"RESEARCH_AGENT_SEARCH_BRAVE__API_KEY_ENV", "search.brave.api_key_env"},
		{"OTHER_SEARCH_BRAVE__API_KEY_ENV", ""},
		{"RESEARCH_AGENT_MODE", ""},
		{"RESEARCH_AGENT_SECRETS_KEY", ""},
		{"OTHER_DATABASE_URL", ""},
	}
	for _, tt := range tests {
		if got := envKey(tt.in); got != tt.want {
			t.Errorf("envKey(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestGetenv(t *testing.T) {
	env := map[string]string{"SECRETS_KEY": "unprefixed", "RESEARCH_AGENT_ADMIN_EMAIL": "b@x"}
	get := func(k string) string { return env[k] }
	if got := Getenv(get, "ADMIN_EMAIL"); got != "b@x" {
		t.Errorf("ADMIN_EMAIL = %q", got)
	}
	if got := Getenv(get, "SECRETS_KEY"); got != "" {
		t.Errorf("only the prefixed name counts, got %q", got)
	}
	if got := Getenv(get, "CONFIG"); got != "" {
		t.Errorf("unset = %q", got)
	}
}

func TestNotices(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		env  []string
		want []string // substrings, in order
	}{
		{"clean", minimal, []string{"ANTHROPIC_API_KEY=x", "RESEARCH_AGENT_CONFIG=/c.yaml", "RESEARCH_AGENT_LOG_LEVEL=debug"}, nil},
		{
			"single underscore instead of nesting",
			minimal,
			[]string{"ANTHROPIC_API_KEY=x", "RESEARCH_AGENT_SEARCH_BRAVE_API_KEY_ENV=K"},
			[]string{"RESEARCH_AGENT_SEARCH_BRAVE_API_KEY_ENV does not match any config key"},
		},
		{
			"removed keys in yaml",
			minimal + "server: { session_secret: abc }\nsources: { reddit: { enabled: true } }\n",
			[]string{"ANTHROPIC_API_KEY=x"},
			[]string{"server.session_secret is no longer used", "sources is no longer used"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := parse([]byte(tt.yaml), environ(tt.env...))
			if err != nil {
				t.Fatal(err)
			}
			w := cfg.Warnings()
			if len(w) != len(tt.want) {
				t.Fatalf("warnings = %q, want %d", w, len(tt.want))
			}
			for i, sub := range tt.want {
				if !strings.Contains(w[i], sub) {
					t.Errorf("warning[%d] = %q, want substring %q", i, w[i], sub)
				}
			}
		})
	}
}

const minimal = `
database: { url: "postgres://x" }
providers:
  - { id: claude, kind: anthropic, api_key_env: ANTHROPIC_API_KEY }
  - { id: local, kind: openai_compat, base_url: "http://localhost:11434/v1", local: true }
routes:
  classify: { provider: claude, model: m, fallback: { provider: local, model: l } }
`

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string // substring; empty = valid
	}{
		{"minimal valid", minimal, ""},
		{"missing db", strings.Replace(minimal, `database: { url: "postgres://x" }`, "", 1), "database.url"},
		{"unknown route provider", minimal + "  score: { provider: nope, model: m }\n", `unknown provider "nope"`},
		{"unknown task", minimal + "  bogus: { provider: claude, model: m }\n", `unknown task "bogus"`},
		{"hosted fallback for local primary", minimal + "  draft: { provider: local, model: m, fallback: { provider: claude, model: m } }\n", ""},
		{"fallback identical to primary", minimal + "  draft: { provider: claude, model: m, fallback: { provider: claude, model: m } }\n", "identical to the primary"},
		{"unknown fallback provider", minimal + "  draft: { provider: claude, model: m, fallback: { provider: nope, model: m } }\n", `fallback: unknown provider "nope"`},
		{"bad api_key_env", strings.Replace(minimal, "api_key_env: ANTHROPIC_API_KEY", "api_key_env: sk-live-123", 1), "not a valid env var name"},
		{"brave backend without key env", minimal + "search: { backend: brave }\n", "needs search.brave.api_key_env"},
		{"brave backend with key env", minimal + "search: { backend: brave, brave: { api_key_env: BRAVE_API_KEY } }\n", ""},
		{"unknown search backend", minimal + "search: { backend: bing }\n", "must be searxng"},
		{"route without model", minimal + "  draft: { provider: claude }\n", "routes.draft: model is required"},
		{"bad kind", strings.Replace(minimal, "kind: anthropic", "kind: gemini", 1), `unknown kind "gemini"`},
		{"openai_compat without base_url", strings.Replace(minimal, `base_url: "http://localhost:11434/v1", `, "", 1), "base_url is required"},
		{"duplicate provider", strings.Replace(minimal, "id: local", "id: claude", 1), "duplicate id"},
		{"embeddings unknown provider", minimal + "embeddings: { provider: nope, model: m, dimensions: 768 }\n", "embeddings: unknown provider"},
		{"bad log level", minimal + "log: { level: loud }\n", "log.level"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parse([]byte(tt.yaml), nil)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("expected error containing %q", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestWarnings(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		env  []string
		want []string // substrings, in order
	}{
		{"key set", minimal, []string{"ANTHROPIC_API_KEY=x"}, nil},
		{"key unset", minimal, nil, []string{`provider "claude": env var ANTHROPIC_API_KEY is not set`}},
		{
			"brave key unset",
			minimal + "search: { backend: brave, brave: { api_key_env: BRAVE_API_KEY } }\n",
			[]string{"ANTHROPIC_API_KEY=x"},
			[]string{"env var BRAVE_API_KEY is not set"},
		},
		{
			"local-only with local fallback is fine",
			minimal + "mode: { offline: true }\n",
			[]string{"ANTHROPIC_API_KEY=x"},
			nil,
		},
		{
			"local-only route without local provider",
			minimal + "  draft: { provider: claude, model: m }\nmode: { offline: true }\nembeddings: { provider: claude, model: e, dimensions: 8 }\n",
			[]string{"ANTHROPIC_API_KEY=x"},
			[]string{`route "draft" has no local provider`, "embeddings provider is hosted"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := parse([]byte(tt.yaml), environ(tt.env...))
			if err != nil {
				t.Fatal(err)
			}
			w := cfg.Warnings()
			if len(w) != len(tt.want) {
				t.Fatalf("warnings = %q, want %d", w, len(tt.want))
			}
			for i, sub := range tt.want {
				if !strings.Contains(w[i], sub) {
					t.Errorf("warning[%d] = %q, want substring %q", i, w[i], sub)
				}
			}
		})
	}
}
