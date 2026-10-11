package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/secrets"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

// HeaderRef is a stored MCP header: a secret reference or an env var name.
type HeaderRef struct {
	SecretID *uuid.UUID `json:"secret_id,omitempty"`
	Env      string     `json:"env,omitempty"`
}

// Loader turns stored server rows into live sessions, resolving header
// secrets. The API (playground, tests) and the worker (research) share it.
type Loader struct {
	Pool    *pgxpool.Pool
	Box     *secrets.Box
	Env     func(string) string
	Connect func(context.Context, ServerConfig) (*Conn, error) // nil: Connect
}

// Config resolves a server row's header references into a live config.
func (l *Loader) Config(ctx context.Context, q *store.Queries, ws uuid.UUID, r store.McpServer) (ServerConfig, error) {
	cfg := ServerConfig{Name: r.Name, Transport: r.Transport, Args: r.Args, AllowedTools: r.AllowedTools, Headers: map[string]string{}}
	if r.Url != nil {
		cfg.URL = *r.Url
	}
	if r.Command != nil {
		cfg.Command = *r.Command
	}
	var refs map[string]HeaderRef
	if len(r.HeaderSecrets) > 0 {
		_ = json.Unmarshal(r.HeaderSecrets, &refs)
	}
	for name, ref := range refs {
		switch {
		case ref.SecretID != nil:
			sec, err := q.GetSecretAnyScope(ctx, store.GetSecretAnyScopeParams{ID: *ref.SecretID, WorkspaceID: ws})
			if err != nil {
				return cfg, fmt.Errorf("header %s: secret not found", name)
			}
			v, err := l.Box.Open(secrets.Sealed{Ciphertext: sec.Ciphertext, Nonce: sec.Nonce, KeyID: sec.KeyID}, sec.ID, sec.WorkspaceID)
			if err != nil {
				return cfg, fmt.Errorf("header %s: %w", name, err)
			}
			cfg.Headers[name] = v
		case ref.Env != "" && l.Env != nil:
			cfg.Headers[name] = l.Env(ref.Env)
		}
	}
	return cfg, nil
}

// Open connects to the given servers and exposes their allowlisted,
// read-only tools. With no ids it returns an empty toolset.
func (l *Loader) Open(ctx context.Context, ws uuid.UUID, ids []uuid.UUID) (*Toolset, error) {
	if len(ids) == 0 {
		return &Toolset{}, nil
	}
	q := store.New(l.Pool)
	rows, err := q.GetMCPServersByIDs(ctx, store.GetMCPServersByIDsParams{WorkspaceID: ws, Ids: ids})
	if err != nil {
		return nil, err
	}
	connect := l.Connect
	if connect == nil {
		connect = Connect
	}
	var conns []*Conn
	for _, r := range rows {
		cfg, err := l.Config(ctx, q, ws, r)
		if err != nil {
			closeAll(conns)
			return nil, err
		}
		conn, err := connect(ctx, cfg)
		if err != nil {
			closeAll(conns)
			return nil, err
		}
		conns = append(conns, conn)
	}
	return NewToolset(ctx, conns)
}

func closeAll(conns []*Conn) {
	for _, c := range conns {
		_ = c.Close()
	}
}
