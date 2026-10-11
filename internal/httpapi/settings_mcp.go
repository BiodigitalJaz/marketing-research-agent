package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm/router"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm/structured"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/mcp"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/prompts"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

// headerRef is a stored MCP header: a secret reference or an env var name.
type headerRef = mcp.HeaderRef

type mcpServerDTO struct {
	ID           uuid.UUID            `json:"id"`
	Name         string               `json:"name"`
	Transport    string               `json:"transport"`
	URL          *string              `json:"url"`
	Command      *string              `json:"command"`
	Args         []string             `json:"args"`
	Headers      map[string]headerRef `json:"headers"`
	AllowedTools []string             `json:"allowed_tools"`
	Enabled      bool                 `json:"enabled"`
}

func toMCPDTO(r store.McpServer) mcpServerDTO {
	d := mcpServerDTO{ID: r.ID, Name: r.Name, Transport: r.Transport, URL: r.Url, Command: r.Command, Args: r.Args,
		Headers: map[string]headerRef{}, AllowedTools: r.AllowedTools, Enabled: r.Enabled}
	_ = json.Unmarshal(orEmptyJSON(r.HeaderSecrets), &d.Headers)
	if d.Args == nil {
		d.Args = []string{}
	}
	if d.AllowedTools == nil {
		d.AllowedTools = []string{}
	}
	return d
}

func (s *server) mcpLoader() *mcp.Loader {
	return &mcp.Loader{Pool: s.pool, Box: s.box, Env: s.env, Connect: s.mcpConnect}
}

// mcpConfig resolves a server row's header references into a live config.
func (s *server) mcpConfig(ctx context.Context, q *store.Queries, ws uuid.UUID, r store.McpServer) (mcp.ServerConfig, error) {
	return s.mcpLoader().Config(ctx, q, ws, r)
}

func (s *server) handleListMCPServers(c *gin.Context) {
	rows, err := store.New(s.pool).ListMCPServers(c.Request.Context(), memberOf(c).WorkspaceID)
	if err != nil {
		s.fail(c, err)
		return
	}
	out := make([]mcpServerDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, toMCPDTO(r))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

type mcpHeaderInput struct {
	SecretID *uuid.UUID `json:"secret_id"`
	Env      string     `json:"env"`
	Value    *string    `json:"value"` // paste a value: stored as an encrypted workspace secret
}

type mcpServerInput struct {
	Name         string                    `json:"name"`
	Transport    string                    `json:"transport"`
	URL          string                    `json:"url"`
	Command      string                    `json:"command"`
	Args         []string                  `json:"args"`
	Headers      map[string]mcpHeaderInput `json:"headers"`
	AllowedTools []string                  `json:"allowed_tools"`
	Enabled      *bool                     `json:"enabled"`
}

func (s *server) handleSaveMCPServer(c *gin.Context) {
	m, u := memberOf(c), userOf(c)
	creating := c.Request.Method == http.MethodPost
	var id uuid.UUID
	if !creating {
		var err error
		if id, err = pathUUID(c, "id"); err != nil {
			s.fail(c, err)
			return
		}
	}
	var in mcpServerInput
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	if err := reqString("name", &in.Name, 60); err != nil {
		s.fail(c, err)
		return
	}
	if err := oneOf("transport", in.Transport, mcp.TransportHTTP, mcp.TransportStdio); err != nil {
		s.fail(c, err)
		return
	}
	var urlPtr, cmdPtr *string
	switch in.Transport {
	case mcp.TransportHTTP:
		u, err := optURL("url", &in.URL)
		if err != nil || u == nil {
			s.fail(c, badRequest("url must be an absolute http(s) URL"))
			return
		}
		urlPtr = u
	case mcp.TransportStdio:
		if err := reqString("command", &in.Command, 500); err != nil {
			s.fail(c, err)
			return
		}
		cmdPtr = &in.Command
	}
	allowed, err := cleanList("allowed_tools", in.AllowedTools, 200, 128)
	if err != nil {
		s.fail(c, err)
		return
	}
	enabled := in.Enabled == nil || *in.Enabled
	ctx := c.Request.Context()
	var saved store.McpServer
	err = s.inTx(ctx, func(_ pgx.Tx, q *store.Queries) error {
		var prevRefs map[string]headerRef
		if !creating {
			prev, err := q.GetMCPServer(ctx, store.GetMCPServerParams{WorkspaceID: m.WorkspaceID, ID: id})
			if err != nil {
				return err
			}
			_ = json.Unmarshal(orEmptyJSON(prev.HeaderSecrets), &prevRefs)
		}
		refs := map[string]headerRef{}
		for name, h := range in.Headers {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			switch {
			case h.Value != nil && strings.TrimSpace(*h.Value) != "":
				sid, err := s.createSecret(ctx, q, fmt.Sprintf("MCP %s %s (%s)", in.Name, name, time.Now().UTC().Format("2006-01-02 15:04:05")), *h.Value, &m.WorkspaceID, u.ID)
				if err != nil {
					return err
				}
				refs[name] = headerRef{SecretID: &sid}
			case h.SecretID != nil:
				refs[name] = headerRef{SecretID: h.SecretID}
			case h.Env != "":
				if !envNameRe.MatchString(h.Env) {
					return badRequest("header env must be an env var name")
				}
				refs[name] = headerRef{Env: h.Env}
			default:
				if prev, ok := prevRefs[name]; ok {
					refs[name] = prev
				}
			}
		}
		hb, _ := json.Marshal(refs)
		args := in.Args
		if args == nil {
			args = []string{}
		}
		if creating {
			saved, err = q.CreateMCPServer(ctx, store.CreateMCPServerParams{WorkspaceID: m.WorkspaceID, Name: in.Name, Transport: in.Transport,
				Url: urlPtr, Command: cmdPtr, Args: args, HeaderSecrets: hb, AllowedTools: allowed, Enabled: enabled})
		} else {
			saved, err = q.UpdateMCPServer(ctx, store.UpdateMCPServerParams{WorkspaceID: m.WorkspaceID, ID: id, Name: in.Name, Transport: in.Transport,
				Url: urlPtr, Command: cmdPtr, Args: args, HeaderSecrets: hb, AllowedTools: allowed, Enabled: enabled})
		}
		if err != nil {
			return err
		}
		// Verify every allowlisted tool against the server's live annotations.
		if len(allowed) > 0 {
			if err := s.verifyAllowlist(ctx, q, m.WorkspaceID, saved); err != nil {
				return err
			}
		}
		s.audit(ctx, q, m.WorkspaceID, &u.ID, "mcp_server.save", "mcp_server", saved.ID.String(),
			map[string]any{"name": in.Name, "transport": in.Transport, "allowed_tools": allowed})
		return nil
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	status := http.StatusOK
	if creating {
		status = http.StatusCreated
	}
	c.JSON(status, toMCPDTO(saved))
}

func (s *server) verifyAllowlist(ctx context.Context, q *store.Queries, ws uuid.UUID, r store.McpServer) error {
	cfg, err := s.mcpConfig(ctx, q, ws, r)
	if err != nil {
		return badRequest(err.Error())
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	conn, err := s.mcpConnect(cctx, cfg)
	if err != nil {
		return &apiError{http.StatusUnprocessableEntity, "mcp_unreachable", "cannot verify the allowlist: " + err.Error()}
	}
	defer func() { _ = conn.Close() }()
	tools, err := conn.ListTools(cctx)
	if err != nil {
		return &apiError{http.StatusUnprocessableEntity, "mcp_unreachable", "cannot verify the allowlist: " + err.Error()}
	}
	byName := map[string]mcp.ToolInfo{}
	for _, t := range tools {
		byName[t.Name] = t
	}
	for _, name := range r.AllowedTools {
		t, ok := byName[name]
		switch {
		case !ok:
			return badRequest(fmt.Sprintf("tool %q is not offered by this server", name))
		case !t.Allowable:
			return badRequest(fmt.Sprintf("tool %q is not annotated read-only and non-destructive, so it cannot be enabled", name))
		}
	}
	return nil
}

func (s *server) handleDeleteMCPServer(c *gin.Context) {
	s.deleteSimple(c, "mcp_server", func(ctx context.Context, q *store.Queries, ws, id uuid.UUID) (int64, error) {
		return q.DeleteMCPServer(ctx, store.DeleteMCPServerParams{WorkspaceID: ws, ID: id})
	})
}

func (s *server) handleTestMCPServer(c *gin.Context) {
	m := memberOf(c)
	id, err := pathUUID(c, "id")
	if err != nil {
		s.fail(c, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	q := store.New(s.pool)
	row, err := q.GetMCPServer(ctx, store.GetMCPServerParams{WorkspaceID: m.WorkspaceID, ID: id})
	if err != nil {
		s.fail(c, err)
		return
	}
	cfg, err := s.mcpConfig(ctx, q, m.WorkspaceID, row)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error(), "tools": []mcp.ToolInfo{}})
		return
	}
	conn, err := s.mcpConnect(ctx, cfg)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error(), "tools": []mcp.ToolInfo{}})
		return
	}
	defer func() { _ = conn.Close() }()
	tools, err := conn.ListTools(ctx)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error(), "tools": []mcp.ToolInfo{}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "tools": tools})
}

// openToolset connects to the given servers and exposes their allowlisted tools.
func (s *server) openToolset(ctx context.Context, ws uuid.UUID, ids []uuid.UUID) (*mcp.Toolset, error) {
	return s.mcpLoader().Open(ctx, ws, ids)
}

// ---- playground ----

type playgroundImage struct {
	MediaType string `json:"media_type"`
	Data      string `json:"data"` // base64
}

type playgroundMessage struct {
	Role   string            `json:"role"` // user | assistant
	Text   string            `json:"text"`
	Images []playgroundImage `json:"images"`
}

type playgroundInput struct {
	Task         string              `json:"task"` // route mode, or:
	ProviderID   string              `json:"provider_id"`
	Model        string              `json:"model"`
	System       string              `json:"system"`
	PromptID     *uuid.UUID          `json:"prompt_id"`
	Messages     []playgroundMessage `json:"messages"`
	Params       router.Params       `json:"params"`
	JSONSchema   json.RawMessage     `json:"json_schema"`
	MCPServerIDs []uuid.UUID         `json:"mcp_server_ids"`
	MaxSteps     int                 `json:"max_steps"`
}

func sse(c *gin.Context, event string, v any) {
	b, _ := json.Marshal(v)
	_, _ = fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, b)
	c.Writer.Flush()
}

func (s *server) handlePlayground(c *gin.Context) {
	m := memberOf(c)
	var in playgroundInput
	if err := bindJSON(c, &in); err != nil {
		s.fail(c, err)
		return
	}
	if len(in.Messages) == 0 {
		s.fail(c, badRequest("messages are required"))
		return
	}
	if in.MaxSteps <= 0 || in.MaxSteps > 8 {
		in.MaxSteps = 6
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Minute)
	defer cancel()
	q := store.New(s.pool)

	var targets []router.Target
	var call router.Call
	switch {
	case in.Task != "":
		res, err := s.router.Resolve(ctx, m.WorkspaceID, in.Task)
		if err != nil {
			s.fail(c, &apiError{http.StatusConflict, "no_route", err.Error()})
			return
		}
		targets = res.Targets
		call = router.Call{Workspace: m.WorkspaceID, Task: in.Task}
	case in.ProviderID != "" && in.Model != "":
		t, err := s.router.Target(ctx, in.ProviderID, in.Model)
		if err != nil {
			s.fail(c, badRequest(err.Error()))
			return
		}
		// Local-only mode covers every model call, including the playground.
		if st, err := s.loadWorkspaceSettings(ctx, store.New(s.pool), m.WorkspaceID); err != nil {
			s.fail(c, err)
			return
		} else if st.LocalOnly && !t.Local {
			s.fail(c, &apiError{http.StatusConflict, "local_only", "local-only mode is on: choose a local provider's model"})
			return
		}
		targets = []router.Target{t}
		call = router.Call{Workspace: m.WorkspaceID, Task: "playground"}
	default:
		s.fail(c, badRequest("set task, or provider_id and model"))
		return
	}
	for i := range targets {
		if err := validateParams(in.Params, targets[i].Caps); err != nil && in.Task == "" {
			s.fail(c, err)
			return
		}
		targets[i].Params = targets[i].Params.Merge(in.Params)
	}

	system := in.System
	if in.PromptID != nil {
		p, err := q.GetPrompt(ctx, store.GetPromptParams{ID: *in.PromptID, WorkspaceID: &m.WorkspaceID})
		if err != nil {
			s.fail(c, err)
			return
		}
		system = p.Body
		call.PromptID = &p.ID
	}
	var schema *structured.Schema
	if len(in.JSONSchema) > 0 && string(in.JSONSchema) != "null" {
		sc, err := structured.Compile("playground", in.JSONSchema)
		if err != nil {
			s.fail(c, badRequest(err.Error()))
			return
		}
		schema = sc
	}
	tools, err := s.openToolset(ctx, m.WorkspaceID, in.MCPServerIDs)
	if err != nil {
		s.fail(c, &apiError{http.StatusBadGateway, "mcp_error", err.Error()})
		return
	}
	defer tools.Close()
	if len(tools.Defs()) > 0 {
		system = strings.TrimSpace(system + "\n\n" + prompts.ExternalContentRule)
	}

	msgs := make([]llm.Message, 0, len(in.Messages))
	for _, pm := range in.Messages {
		role := llm.RoleUser
		if pm.Role == "assistant" {
			role = llm.RoleAssistant
		}
		msg := llm.Message{Role: role}
		for _, img := range pm.Images {
			data, err := base64.StdEncoding.DecodeString(img.Data)
			if err != nil || !slices.Contains([]string{"image/png", "image/jpeg", "image/gif", "image/webp"}, img.MediaType) {
				s.fail(c, badRequest("images must be base64 png, jpeg, gif, or webp"))
				return
			}
			msg.Parts = append(msg.Parts, llm.Part{Kind: llm.PartImage, Image: &llm.Image{MediaType: img.MediaType, Data: data}})
		}
		if pm.Text != "" {
			msg.Parts = append(msg.Parts, llm.TextPart(pm.Text))
		}
		msgs = append(msgs, msg)
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache, no-transform") // no-transform: proxies must not compress (and so buffer) the stream
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	req := llm.ChatRequest{System: system, Messages: msgs, Tools: tools.Defs()}
	if schema != nil {
		req.JSONSchema, req.SchemaName = schema.Raw, "playground"
	}
	for step := 0; step < in.MaxSteps; step++ {
		ch, _ := s.router.Stream(ctx, call, targets, req)
		var final *llm.ChatResponse
		for ev := range ch {
			switch ev.Kind {
			case llm.EventTextDelta:
				sse(c, "text", gin.H{"text": ev.Text})
			case llm.EventThinkingDelta:
				sse(c, "thinking", gin.H{"text": ev.Text})
			case llm.EventToolCall:
				sse(c, "tool_call", ev.Call)
			case llm.EventDone:
				final = ev.Resp
			case llm.EventError:
				sse(c, "error", gin.H{"message": ev.Err.Error()})
				return
			}
		}
		if final == nil {
			sse(c, "error", gin.H{"message": "no response"})
			return
		}
		if len(final.ToolCalls) == 0 || final.StopReason != llm.StopToolUse {
			done := gin.H{"model": final.Model, "stop_reason": final.StopReason, "usage": final.Usage, "steps": step + 1}
			if schema != nil {
				if _, err := schema.Validate(final.Text()); err != nil {
					done["schema_error"] = err.Error()
				} else {
					done["schema_valid"] = true
				}
			}
			sse(c, "done", done)
			return
		}
		req.Messages = append(req.Messages, final.AssistantMessage())
		for _, tc := range final.ToolCalls {
			out, isErr, err := tools.Call(ctx, tc.Name, tc.Args)
			if err != nil {
				out, isErr = err.Error(), true
			}
			sse(c, "tool_result", gin.H{"call_id": tc.ID, "name": tc.Name, "content": out, "is_error": isErr})
			req.Messages = append(req.Messages, llm.Message{Role: llm.RoleTool, ToolResult: &llm.ToolResult{CallID: tc.ID, Content: out, IsError: isErr}})
		}
	}
	sse(c, "error", gin.H{"message": fmt.Sprintf("stopped after %d steps", in.MaxSteps)})
}
