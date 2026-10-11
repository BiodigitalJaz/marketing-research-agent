// Package config loads and validates application configuration from a YAML
// file with environment overrides.
//
// Loading order:
//  1. The YAML file is read and ${VAR} references are expanded from the
//     environment. Unset variables expand to "", or to the default given
//     with ${VAR:-default}.
//  2. Variables prefixed with RESEARCH_AGENT_ override individual keys. The
//     first underscore after the prefix separates the top-level section; any
//     further nesting uses a double underscore. Examples:
//     RESEARCH_AGENT_DATABASE_URL            -> database.url
//     RESEARCH_AGENT_INBOX_TIMEZONE          -> inbox.timezone
//     RESEARCH_AGENT_SEARCH_BRAVE__API_KEY_ENV -> search.brave.api_key_env
//     List-valued sections (providers) and routes can only be set in YAML.
//
// Validate is called by Load; a process must refuse to start on error.
package config

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/rawbytes"
	"github.com/knadh/koanf/v2"
)

// EnvPrefix is the prefix for environment variables: config overrides and
// the variables in DirectEnv.
const EnvPrefix = "RESEARCH_AGENT_"

// DirectEnv lists variables under the prefix that binaries read directly
// (with Getenv) instead of mapping onto config keys.
var DirectEnv = []string{"CONFIG", "SECRETS_KEY", "SECRETS_KEY_PREVIOUS", "ADMIN_EMAIL", "ADMIN_NAME", "ADMIN_PASSWORD", "SETUP_TOKEN"}

// Getenv returns the variable EnvPrefix+name.
func Getenv(lookup func(string) string, name string) string {
	return lookup(EnvPrefix + name)
}

// deprecatedKeys are accepted in YAML but no longer do anything.
var deprecatedKeys = []string{"server.session_secret", "sources"}

// Provider kinds.
const (
	KindAnthropic    = "anthropic"
	KindOpenAICompat = "openai_compat"
)

// Tasks that may appear in routes.
var KnownTasks = []string{"classify", "score", "draft", "research", "summarize"}

// PromptTasks have editable prompts: every routed task, plus prompts that
// run on another task's route (draft_check on draft; research_* on research).
var PromptTasks = append(append([]string{}, KnownTasks...), "draft_check", "research_plan", "research_verify", "research_synthesize", "knowledge_digest", "memory_bank")

type Config struct {
	Server     Server           `koanf:"server"`
	Database   Database         `koanf:"database"`
	Worker     Worker           `koanf:"worker"`
	Log        Log              `koanf:"log"`
	Mode       Mode             `koanf:"mode"`
	Providers  []Provider       `koanf:"providers"`
	Routes     map[string]Route `koanf:"routes"`
	Embeddings Embeddings       `koanf:"embeddings"`
	Search     Search           `koanf:"search"`
	Retention  Retention        `koanf:"retention"`
	Inbox      Inbox            `koanf:"inbox"`

	env     map[string]string // environment snapshot used to resolve *_env references
	notices []string          // load-time warnings (unknown or deprecated settings)
}

type Server struct {
	Addr            string        `koanf:"addr"`
	BaseURL         string        `koanf:"base_url"`
	ShutdownTimeout time.Duration `koanf:"shutdown_timeout"`
}

type Database struct {
	URL string `koanf:"url"`
}

type Worker struct {
	// MaxWorkers is the concurrency of the default River queue.
	MaxWorkers int `koanf:"max_workers"`
	// PipelineWorkers is the concurrency of LLM-bound pipeline jobs.
	PipelineWorkers int           `koanf:"pipeline_workers"`
	ShutdownTimeout time.Duration `koanf:"shutdown_timeout"`
}

type Log struct {
	Level  string `koanf:"level"`  // debug | info | warn | error
	Format string `koanf:"format"` // json | text
}

// Mode.Offline is local-only LLM mode: only providers marked local may be
// called. It does not imply loss of internet; collection and web search run.
type Mode struct {
	Offline bool `koanf:"offline"`
}

// Provider seeds model_providers. Secrets are referenced by env var name
// (APIKeyEnv) so the same shape can live in the DB and the Settings UI
// without ever storing a key.
type Provider struct {
	ID        string `koanf:"id"`
	Kind      string `koanf:"kind"`
	BaseURL   string `koanf:"base_url"`
	APIKeyEnv string `koanf:"api_key_env"`
	Local     bool   `koanf:"local"`
}

type Route struct {
	Provider string    `koanf:"provider"`
	Model    string    `koanf:"model"`
	Fallback *Fallback `koanf:"fallback"`
}

type Fallback struct {
	Provider string `koanf:"provider"`
	Model    string `koanf:"model"`
}

type Embeddings struct {
	Provider   string `koanf:"provider"`
	Model      string `koanf:"model"`
	Dimensions int    `koanf:"dimensions"`
}

type Search struct {
	// Backend is the default search backend seeded into workspace settings:
	// searxng (default), google_pse, or brave.
	Backend    string `koanf:"backend"`
	SearXNGURL string `koanf:"searxng_url"`
	Brave      Brave  `koanf:"brave"`
}

type Brave struct {
	APIKeyEnv string `koanf:"api_key_env"`
}

type Inbox struct {
	// Timezone (IANA) for the daily inbox, built from 06:00 local time.
	Timezone string `koanf:"timezone"`
}

type Retention struct {
	RawItemsDays int `koanf:"raw_items_days"`
}

// Defaults applied before the file and env are loaded.
func defaults() map[string]any {
	return map[string]any{
		"server.addr":              ":8080",
		"server.base_url":          "http://localhost:3000",
		"server.shutdown_timeout":  "15s",
		"worker.max_workers":       10,
		"worker.pipeline_workers":  2,
		"worker.shutdown_timeout":  "30s",
		"log.level":                "info",
		"log.format":               "json",
		"retention.raw_items_days": 90,
		"search.backend":           "searxng",
		"inbox.timezone":           "UTC",
		"search.searxng_url":       "http://searxng:8080",
	}
}

// Load reads the YAML file at path, applies env overrides from environ
// (typically os.Environ), and validates the result. A missing file is an
// error: config must be explicit.
func Load(path string, environ func() []string) (*Config, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- operator-supplied config path
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return parse(raw, environ)
}

func parse(raw []byte, environ func() []string) (*Config, error) {
	lookup := envLookup(environ)
	expanded := os.Expand(string(raw), func(k string) string {
		name, def, hasDef := strings.Cut(k, ":-")
		if v := lookup[name]; v != "" || !hasDef {
			return v
		}
		return def
	})

	k := koanf.New(".")
	for key, v := range defaults() {
		if err := k.Set(key, v); err != nil {
			return nil, fmt.Errorf("set default %s: %w", key, err)
		}
	}
	if err := k.Load(rawbytes.Provider([]byte(expanded)), yaml.Parser()); err != nil {
		return nil, fmt.Errorf("parse config yaml: %w", err)
	}
	var notices []string
	for _, key := range deprecatedKeys {
		if k.Exists(key) {
			notices = append(notices, fmt.Sprintf("%s is no longer used; remove it from the config file", key))
		}
	}
	if err := k.Load(env.Provider(".", env.Opt{
		Prefix:        EnvPrefix,
		TransformFunc: func(name, v string) (string, any) { return envKey(name), v },
		EnvironFunc:   environ,
	}), nil); err != nil {
		return nil, fmt.Errorf("load env overrides: %w", err)
	}

	var cfg Config
	if err := k.UnmarshalWithConf("", &cfg, koanf.UnmarshalConf{Tag: "koanf"}); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	cfg.env = lookup
	cfg.notices = append(notices, envNotices(lookup)...)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// envKey maps RESEARCH_AGENT_SECTION_REST__NESTED to section.rest.nested. It returns "" for names that are not config keys,
// which koanf ignores.
func envKey(name string) string {
	suffix, ok := strings.CutPrefix(name, EnvPrefix)
	if !ok || isDirectEnv(suffix) {
		return ""
	}
	section, rest, ok := strings.Cut(strings.ToLower(suffix), "_")
	if !ok || rest == "" {
		return ""
	}
	return section + "." + strings.ReplaceAll(rest, "__", ".")
}

func isDirectEnv(suffix string) bool {
	for _, d := range DirectEnv {
		if d == suffix {
			return true
		}
	}
	return false
}

// envNotices reports prefixed variables that set nothing (usually a single
// underscore where nesting needs a double one).
func envNotices(lookup map[string]string) []string {
	valid := map[string]bool{}
	leafKeys(reflect.TypeFor[Config](), "", valid)
	var unknown []string
	for name := range lookup {
		suffix, ok := strings.CutPrefix(name, EnvPrefix)
		if !ok {
			continue
		}
		if !isDirectEnv(suffix) && !valid[envKey(name)] {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	var w []string
	for _, name := range unknown {
		w = append(w, fmt.Sprintf("env var %s does not match any config key and is ignored (nested keys use a double underscore, e.g. %sSEARCH_BRAVE__API_KEY_ENV)", name, EnvPrefix))
	}
	return w
}

// leafKeys collects the dotted keys that env overrides can set: scalar
// fields reachable through nested structs. Lists and maps are YAML-only.
func leafKeys(t reflect.Type, prefix string, out map[string]bool) {
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("koanf")
		if tag == "" {
			continue
		}
		switch f.Type.Kind() {
		case reflect.Struct:
			leafKeys(f.Type, prefix+tag+".", out)
		case reflect.Slice, reflect.Map, reflect.Pointer:
		default:
			out[prefix+tag] = true
		}
	}
}

func envLookup(environ func() []string) map[string]string {
	m := map[string]string{}
	if environ == nil {
		return m
	}
	for _, kv := range environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

// Secret resolves an env var reference (e.g. Provider.APIKeyEnv) against the
// environment captured at load time. Empty name or unset var yields "".
func (c *Config) Secret(envName string) string {
	if envName == "" {
		return ""
	}
	return c.env[envName]
}

var envName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// Provider returns the provider with the given id.
func (c *Config) Provider(id string) (Provider, bool) {
	for _, p := range c.Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// Validate checks structural correctness. It returns all problems found,
// joined, so an operator can fix them in one pass.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if c.Server.Addr == "" {
		add("server.addr is required")
	}
	if c.Database.URL == "" {
		add("database.url is required")
	}
	if c.Worker.MaxWorkers < 1 {
		add("worker.max_workers must be >= 1")
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		add("log.level %q must be one of debug, info, warn, error", c.Log.Level)
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		add("log.format %q must be json or text", c.Log.Format)
	}

	seen := map[string]bool{}
	for i, p := range c.Providers {
		if p.ID == "" {
			add("providers[%d].id is required", i)
			continue
		}
		if seen[p.ID] {
			add("providers: duplicate id %q", p.ID)
		}
		seen[p.ID] = true
		if p.APIKeyEnv != "" && !envName.MatchString(p.APIKeyEnv) {
			add("provider %q: api_key_env %q is not a valid env var name", p.ID, p.APIKeyEnv)
		}
		switch p.Kind {
		case KindAnthropic:
			if p.Local {
				add("provider %q: kind anthropic cannot be local", p.ID)
			}
		case KindOpenAICompat:
			if p.BaseURL == "" {
				add("provider %q: base_url is required for openai_compat", p.ID)
			}
		default:
			add("provider %q: unknown kind %q", p.ID, p.Kind)
		}
	}

	for task, r := range c.Routes {
		if !isKnownTask(task) {
			add("routes: unknown task %q", task)
		}
		if _, ok := c.Provider(r.Provider); !ok {
			add("routes.%s: unknown provider %q", task, r.Provider)
		}
		if r.Model == "" {
			add("routes.%s: model is required", task)
		}
		// A fallback may be any provider, hosted or local.
		if r.Fallback != nil {
			if _, ok := c.Provider(r.Fallback.Provider); !ok {
				add("routes.%s.fallback: unknown provider %q", task, r.Fallback.Provider)
			}
			if r.Fallback.Provider == r.Provider && r.Fallback.Model == r.Model {
				add("routes.%s.fallback: identical to the primary", task)
			}
			if r.Fallback.Model == "" {
				add("routes.%s.fallback: model is required", task)
			}
		}
	}

	if c.Embeddings.Provider != "" {
		if _, ok := c.Provider(c.Embeddings.Provider); !ok {
			add("embeddings: unknown provider %q", c.Embeddings.Provider)
		}
		if c.Embeddings.Model == "" {
			add("embeddings.model is required")
		}
		if c.Embeddings.Dimensions <= 0 {
			add("embeddings.dimensions must be > 0")
		}
	}

	if b := c.Search.Brave.APIKeyEnv; b != "" && !envName.MatchString(b) {
		add("search.brave.api_key_env %q is not a valid env var name", b)
	}
	switch c.Search.Backend {
	case "", "searxng", "google_pse", "brave":
	default:
		add("search.backend %q must be searxng, google_pse, or brave", c.Search.Backend)
	}
	if c.Search.Backend == "brave" && c.Search.Brave.APIKeyEnv == "" {
		add("search.backend brave needs search.brave.api_key_env")
	}

	if _, err := time.LoadLocation(c.Inbox.Timezone); err != nil {
		add("inbox.timezone %q is not a valid IANA timezone", c.Inbox.Timezone)
	}
	if c.Retention.RawItemsDays < 1 {
		add("retention.raw_items_days must be >= 1")
	}

	if len(errs) > 0 {
		return fmt.Errorf("invalid config: %w", errors.Join(errs...))
	}
	return nil
}

// Warnings returns non-fatal issues worth logging at startup, such as a
// provider whose API key env var is unset. The LLM router treats such
// providers as unavailable rather than refusing to boot.
func (c *Config) Warnings() []string {
	w := append([]string{}, c.notices...)
	for _, p := range c.Providers {
		if p.APIKeyEnv != "" && c.Secret(p.APIKeyEnv) == "" {
			w = append(w, fmt.Sprintf("provider %q: env var %s is not set; calls to it will fail", p.ID, p.APIKeyEnv))
		}
	}
	if c.Search.Backend == "brave" && c.Secret(c.Search.Brave.APIKeyEnv) == "" {
		w = append(w, fmt.Sprintf("search: env var %s is not set; Brave search is unavailable", c.Search.Brave.APIKeyEnv))
	}
	if c.Mode.Offline {
		isLocal := func(id string) bool { p, ok := c.Provider(id); return ok && p.Local }
		tasks := make([]string, 0, len(c.Routes))
		for t := range c.Routes {
			tasks = append(tasks, t)
		}
		sort.Strings(tasks)
		for _, t := range tasks {
			r := c.Routes[t]
			if !isLocal(r.Provider) && (r.Fallback == nil || !isLocal(r.Fallback.Provider)) {
				w = append(w, fmt.Sprintf("local-only mode: route %q has no local provider and will be unavailable", t))
			}
		}
		if c.Embeddings.Provider != "" && !isLocal(c.Embeddings.Provider) {
			w = append(w, "local-only mode: the embeddings provider is hosted")
		}
	}
	return w
}

func isKnownTask(t string) bool {
	for _, k := range KnownTasks {
		if k == t {
			return true
		}
	}
	return false
}
