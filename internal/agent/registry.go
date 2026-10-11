// Package agent runs a tool-using model loop:
// a tool registry, a per-run notebook of citable sources and chart data,
// and the loop itself. It knows nothing about specific tools.
package agent

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm"
)

// Output is what a tool returns.
type Output struct {
	Text    string      // returned to the model
	Images  []llm.Image // vision input, sent to the model after the tool results
	Summary string      // short label for the progress timeline, e.g. "8 results"
}

// Tool is a callable tool. Content a tool returns never changes which
// tools are available.
type Tool struct {
	Def     llm.ToolDef
	Handler func(ctx context.Context, args json.RawMessage) (Output, error)
	Web     bool   // reaches the public web; removed when a job has use_web = false
	Source  string // builtin | mcp:<server>
	Vision  bool   // returns images; only offered to models that declare vision
}

// Registry is an ordered set of tools.
type Registry struct {
	tools map[string]Tool
	order []string
}

func NewRegistry() *Registry { return &Registry{tools: map[string]Tool{}} }

// Add registers a tool, replacing one with the same name.
func (r *Registry) Add(t Tool) {
	if _, ok := r.tools[t.Def.Name]; !ok {
		r.order = append(r.order, t.Def.Name)
	}
	r.tools[t.Def.Name] = t
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Names lists the tools in registration order.
func (r *Registry) Names() []string { return slices.Clone(r.order) }

// Defs returns the tool definitions for a request.
func (r *Registry) Defs() []llm.ToolDef {
	out := make([]llm.ToolDef, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.tools[n].Def)
	}
	return out
}

// Len is the number of tools.
func (r *Registry) Len() int { return len(r.order) }

// Policy selects which tools a run may use.
type Policy struct {
	// Builtins allowlists built-in tools by name; empty means all of them.
	// MCP tools are chosen by the route's MCP servers and are not filtered here.
	Builtins []string
	UseWeb   bool // false removes tools that reach the public web
	Vision   bool // false removes tools that return images
	// Exclude removes built-in tools that have nothing to work on (for
	// example knowledge tools in a workspace without internal docs).
	Exclude []string
}

// Filter returns the tools the policy allows.
func (r *Registry) Filter(p Policy) *Registry {
	out := NewRegistry()
	for _, n := range r.order {
		t := r.tools[n]
		switch {
		case t.Source == "builtin" && len(p.Builtins) > 0 && !slices.Contains(p.Builtins, n):
		case t.Web && !p.UseWeb:
		case t.Vision && !p.Vision:
		case t.Source == "builtin" && slices.Contains(p.Exclude, n):
		default:
			out.Add(t)
		}
	}
	return out
}
