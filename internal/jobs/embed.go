package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
	"github.com/riverqueue/river"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/brand"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/embed"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

// Entity types stored in embeddings.entity_type.
const (
	EntityPortfolioItem = "portfolio_item"
	EntityICP           = "icp"
)

// EmbedEntityArgs (re)computes the embedding for one brand entity. It is
// enqueued in the same transaction as the write that changed the entity.
type EmbedEntityArgs struct {
	WorkspaceID uuid.UUID `json:"workspace_id"`
	EntityType  string    `json:"entity_type"`
	EntityID    uuid.UUID `json:"entity_id"`
}

func (EmbedEntityArgs) Kind() string { return "embed_entity" }

type EmbedEntityWorker struct {
	river.WorkerDefaults[EmbedEntityArgs]
	Deps
}

func (w *EmbedEntityWorker) Work(ctx context.Context, job *river.Job[EmbedEntityArgs]) error {
	a := job.Args
	if w.Embedder == nil {
		return river.JobCancel(embed.ErrNotConfigured)
	}
	embedder, err := w.Embedder(ctx, a.WorkspaceID)
	if errors.Is(err, embed.ErrNotConfigured) {
		return river.JobCancel(err)
	}
	if err != nil {
		return fmt.Errorf("embedder: %w", err)
	}
	q := store.New(w.Pool)

	text, err := entityText(ctx, q, a)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // deleted since enqueue
	}
	if err != nil {
		return err
	}
	vecs, err := embedder.Embed(ctx, []string{text})
	if err != nil {
		return fmt.Errorf("embed %s %s: %w", a.EntityType, a.EntityID, err)
	}
	err = q.UpsertEmbedding(ctx, store.UpsertEmbeddingParams{
		EntityType:  a.EntityType,
		EntityID:    a.EntityID,
		Chunk:       0,
		Model:       embedder.Model(),
		Dims:        int32(embedder.Dims()), // #nosec G115 -- dims are small
		Embedding:   pgvector.NewVector(vecs[0]),
		WorkspaceID: a.WorkspaceID,
	})
	if err != nil {
		return fmt.Errorf("store embedding: %w", err)
	}
	w.Log.InfoContext(ctx, "embedded entity", "workspace_id", a.WorkspaceID,
		"entity_type", a.EntityType, "entity_id", a.EntityID, "model", embedder.Model())
	return nil
}

func entityText(ctx context.Context, q *store.Queries, a EmbedEntityArgs) (string, error) {
	switch a.EntityType {
	case EntityPortfolioItem:
		p, err := q.GetPortfolioItem(ctx, store.GetPortfolioItemParams{WorkspaceID: a.WorkspaceID, ID: a.EntityID})
		if err != nil {
			return "", err
		}
		var tags brand.PortfolioTags
		if err := json.Unmarshal(p.Tags, &tags); err != nil {
			return "", fmt.Errorf("decode portfolio tags: %w", err)
		}
		return brand.PortfolioText(p.Title, p.Summary, tags), nil
	case EntityICP:
		i, err := q.GetICP(ctx, store.GetICPParams{WorkspaceID: a.WorkspaceID, ID: a.EntityID})
		if err != nil {
			return "", err
		}
		var d brand.ICPDefinition
		if err := json.Unmarshal(i.Definition, &d); err != nil {
			return "", fmt.Errorf("decode icp definition: %w", err)
		}
		return brand.ICPText(i.Name, d), nil
	}
	return "", river.JobCancel(fmt.Errorf("unknown entity type %q", a.EntityType))
}

// PurgeSessionsArgs deletes expired sessions (periodic).
type PurgeSessionsArgs struct{}

func (PurgeSessionsArgs) Kind() string { return "purge_sessions" }

type PurgeSessionsWorker struct {
	river.WorkerDefaults[PurgeSessionsArgs]
	Deps
}

func (w *PurgeSessionsWorker) Work(ctx context.Context, _ *river.Job[PurgeSessionsArgs]) error {
	n, err := store.New(w.Pool).DeleteExpiredSessions(ctx)
	if err != nil {
		return fmt.Errorf("purge sessions: %w", err)
	}
	if n > 0 {
		w.Log.InfoContext(ctx, "purged expired sessions", "count", n)
	}
	return nil
}
