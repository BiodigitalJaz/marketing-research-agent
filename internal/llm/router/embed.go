package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/embed"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

// EmbeddingSettings is workspace_settings.embeddings.
type EmbeddingSettings struct {
	ProviderID string `json:"provider_id"`
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
}

// EmbeddingSettingsFor loads a workspace's embedding settings.
func EmbeddingSettingsFor(ctx context.Context, q *store.Queries, ws uuid.UUID) (EmbeddingSettings, bool, error) {
	var es EmbeddingSettings
	st, err := q.GetWorkspaceSettings(ctx, ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return es, false, nil
	}
	if err != nil {
		return es, false, err
	}
	if err := json.Unmarshal(orEmpty(st.Embeddings), &es); err != nil {
		return es, st.LocalOnly, fmt.Errorf("decode embedding settings: %w", err)
	}
	return es, st.LocalOnly, nil
}

// Embedder builds the workspace's embedder from Settings. In local-only
// mode a hosted embeddings provider is refused.
func (r *Router) Embedder(ctx context.Context, ws uuid.UUID) (embed.Embedder, error) {
	q := store.New(r.pool)
	es, localOnly, err := EmbeddingSettingsFor(ctx, q, ws)
	if err != nil {
		return nil, err
	}
	if es.ProviderID == "" || es.Model == "" || es.Dimensions <= 0 {
		return nil, embed.ErrNotConfigured
	}
	row, err := q.GetProvider(ctx, es.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("embeddings provider %s: %w", es.ProviderID, err)
	}
	if !row.Enabled {
		return nil, fmt.Errorf("embeddings provider %s is disabled", row.ID)
	}
	if localOnly && !row.IsLocal {
		return nil, fmt.Errorf("%w: embeddings provider %s is hosted", ErrNoLocalRoute, row.ID)
	}
	key, err := r.APIKey(ctx, q, row)
	if err != nil {
		return nil, err
	}
	base := ""
	if row.BaseUrl != nil {
		base = *row.BaseUrl
	}
	return embed.New(embed.ProviderConfig{BaseURL: base, APIKey: key, Kind: row.Kind}, es.Model, es.Dimensions)
}

// ProbeEmbedding embeds one short text with providerID's model and returns
// the vector length, so Settings can fill in the dimensions. It refuses a
// hosted provider when the workspace is in local-only mode.
func (r *Router) ProbeEmbedding(ctx context.Context, ws uuid.UUID, providerID, model string) (int, error) {
	q := store.New(r.pool)
	row, err := q.GetProvider(ctx, providerID)
	if err != nil {
		return 0, fmt.Errorf("embeddings provider %s: %w", providerID, err)
	}
	if _, localOnly, err := EmbeddingSettingsFor(ctx, q, ws); err != nil {
		return 0, err
	} else if localOnly && !row.IsLocal {
		return 0, fmt.Errorf("%w: embeddings provider %s is hosted", ErrNoLocalRoute, row.ID)
	}
	key, err := r.APIKey(ctx, q, row)
	if err != nil {
		return 0, err
	}
	base := ""
	if row.BaseUrl != nil {
		base = *row.BaseUrl
	}
	e, err := embed.New(embed.ProviderConfig{BaseURL: base, APIKey: key, Kind: row.Kind}, model, 0)
	if err != nil {
		return 0, err
	}
	vecs, err := e.Embed(ctx, []string{"dimension probe"})
	if err != nil {
		return 0, err
	}
	return len(vecs[0]), nil
}
