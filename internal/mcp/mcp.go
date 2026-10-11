// Package mcp connects to configured MCP servers and exposes their
// allowlisted, read-only tools to the agent.
//
// Safety: a tool is exposed only if an admin allowlisted it AND the server
// annotates it readOnlyHint=true and not destructive. Annotations are
// re-checked on every connect, so a server that later changes a tool's
// annotations loses access to it. Results are wrapped as external content.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/prompts"
)

// Transports.
const (
	TransportHTTP  = "streamable_http"
	TransportStdio = "stdio"
)

// ServerConfig is a server with secrets already resolved.
type ServerConfig struct {
	Name         string
	Transport    string
	URL          string
	Command      string
	Args         []string
	Headers      map[string]string // resolved values; never logged
	AllowedTools []string
}

// ToolInfo describes a server tool and whether it may be allowlisted.
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	ReadOnly    bool            `json:"read_only"`
	Destructive bool            `json:"destructive"`
	Allowable   bool            `json:"allowable"`
}

// Allowable reports whether annotations permit allowlisting: read-only and
// explicitly or implicitly non-destructive. (The MCP default for
// destructiveHint is true, but it only applies when readOnlyHint is false.)
func Allowable(a *sdk.ToolAnnotations) bool {
	if a == nil || !a.ReadOnlyHint {
		return false
	}
	return a.DestructiveHint == nil || !*a.DestructiveHint
}

// Conn is a live session to one server.
type Conn struct {
	cfg     ServerConfig
	session *sdk.ClientSession
}

// Connect opens a session using the configured transport.
func Connect(ctx context.Context, cfg ServerConfig) (*Conn, error) {
	var t sdk.Transport
	switch cfg.Transport {
	case TransportHTTP:
		if cfg.URL == "" {
			return nil, errors.New("streamable_http server needs a url")
		}
		t = &sdk.StreamableClientTransport{
			Endpoint:   cfg.URL,
			HTTPClient: &http.Client{Timeout: 60 * time.Second, Transport: headerRoundTripper{headers: cfg.Headers, next: http.DefaultTransport}},
			MaxRetries: 2,
		}
	case TransportStdio:
		if cfg.Command == "" {
			return nil, errors.New("stdio server needs a command")
		}
		// #nosec G204 -- admin-configured MCP server command (Settings, admin only)
		t = &sdk.CommandTransport{Command: exec.CommandContext(context.WithoutCancel(ctx), cfg.Command, cfg.Args...)}
	default:
		return nil, fmt.Errorf("unknown transport %q", cfg.Transport)
	}
	return ConnectTransport(ctx, cfg, t)
}

// ConnectTransport opens a session over a given transport (tests use
// in-memory transports).
func ConnectTransport(ctx context.Context, cfg ServerConfig, t sdk.Transport) (*Conn, error) {
	client := sdk.NewClient(&sdk.Implementation{Name: "marketing-research-agent", Version: "0.1"}, nil)
	session, err := client.Connect(ctx, t, nil)
	if err != nil {
		return nil, fmt.Errorf("connect mcp server %s: %w", cfg.Name, err)
	}
	return &Conn{cfg: cfg, session: session}, nil
}

func (c *Conn) Close() error { return c.session.Close() }

// ListTools lists the server's tools with allowability.
func (c *Conn) ListTools(ctx context.Context) ([]ToolInfo, error) {
	res, err := c.session.ListTools(ctx, &sdk.ListToolsParams{})
	if err != nil {
		return nil, fmt.Errorf("list tools on %s: %w", c.cfg.Name, err)
	}
	out := make([]ToolInfo, 0, len(res.Tools))
	for _, t := range res.Tools {
		schema, err := json.Marshal(t.InputSchema)
		if err != nil || string(schema) == "null" {
			schema = []byte(`{"type":"object"}`)
		}
		info := ToolInfo{Name: t.Name, Description: t.Description, InputSchema: schema, Allowable: Allowable(t.Annotations)}
		if t.Annotations != nil {
			info.ReadOnly = t.Annotations.ReadOnlyHint
			info.Destructive = t.Annotations.DestructiveHint == nil || *t.Annotations.DestructiveHint
			if info.ReadOnly && t.Annotations.DestructiveHint == nil {
				info.Destructive = false
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// Call invokes a tool and returns its text content.
func (c *Conn) Call(ctx context.Context, tool string, args json.RawMessage) (string, bool, error) {
	var a any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return "", false, fmt.Errorf("tool args: %w", err)
		}
	}
	res, err := c.session.CallTool(ctx, &sdk.CallToolParams{Name: tool, Arguments: a})
	if err != nil {
		return "", false, fmt.Errorf("call %s on %s: %w", tool, c.cfg.Name, err)
	}
	var b strings.Builder
	for _, content := range res.Content {
		switch v := content.(type) {
		case *sdk.TextContent:
			b.WriteString(v.Text)
		default:
			fmt.Fprintf(&b, "[%T content omitted]", content)
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String()), res.IsError, nil
}

type headerRoundTripper struct {
	headers map[string]string
	next    http.RoundTripper
}

func (h headerRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	if len(h.headers) > 0 {
		r = r.Clone(r.Context())
		for k, v := range h.headers {
			r.Header.Set(k, v)
		}
	}
	return h.next.RoundTrip(r)
}

// Toolset exposes allowlisted tools from several servers under namespaced
// names: mcp__<server>__<tool>.
type Toolset struct {
	conns map[string]*Conn // by namespaced tool name
	tools map[string]string
	defs  []llm.ToolDef
	all   []*Conn
}

var nameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// ToolName builds the namespaced tool name (<= 64 chars, API-safe).
func ToolName(server, tool string) string {
	n := "mcp__" + strings.ToLower(nameSanitizer.ReplaceAllString(server, "_")) + "__" + nameSanitizer.ReplaceAllString(tool, "_")
	if len(n) > 64 {
		n = n[:64]
	}
	return n
}

// NewToolset filters each connection's tools to those that are both
// allowlisted and allowable. It takes ownership of conns.
func NewToolset(ctx context.Context, conns []*Conn) (*Toolset, error) {
	ts := &Toolset{conns: map[string]*Conn{}, tools: map[string]string{}, all: conns}
	for _, c := range conns {
		allowed := map[string]bool{}
		for _, t := range c.cfg.AllowedTools {
			allowed[t] = true
		}
		infos, err := c.ListTools(ctx)
		if err != nil {
			ts.Close()
			return nil, err
		}
		for _, info := range infos {
			if !allowed[info.Name] || !info.Allowable {
				continue
			}
			name := ToolName(c.cfg.Name, info.Name)
			ts.conns[name] = c
			ts.tools[name] = info.Name
			ts.defs = append(ts.defs, llm.ToolDef{
				Name:        name,
				Description: fmt.Sprintf("[MCP server %s] %s", c.cfg.Name, info.Description),
				Schema:      info.InputSchema,
			})
		}
	}
	return ts, nil
}

// Defs returns the exposed tool definitions.
func (ts *Toolset) Defs() []llm.ToolDef { return ts.defs }

// Has reports whether name is one of this toolset's tools.
func (ts *Toolset) Has(name string) bool { _, ok := ts.conns[name]; return ok }

// Call runs a namespaced tool. The result is wrapped as external content.
func (ts *Toolset) Call(ctx context.Context, name string, args json.RawMessage) (string, bool, error) {
	c, ok := ts.conns[name]
	if !ok {
		return "", true, fmt.Errorf("tool %s is not available", name)
	}
	text, isErr, err := c.Call(ctx, ts.tools[name], args)
	if err != nil {
		return "", true, err
	}
	return prompts.ExternalContent("mcp:"+c.cfg.Name+"/"+ts.tools[name], text), isErr, nil
}

// Close closes every session.
func (ts *Toolset) Close() {
	for _, c := range ts.all {
		_ = c.Close()
	}
}
