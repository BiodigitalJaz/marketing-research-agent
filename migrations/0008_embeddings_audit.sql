-- +goose Up
CREATE TABLE embeddings (
  entity_type   text NOT NULL,   -- raw_item | portfolio_item | icp | report_chunk
  entity_id     uuid NOT NULL,
  chunk         int NOT NULL DEFAULT 0,
  model         text NOT NULL,
  dims          int NOT NULL,
  embedding     vector NOT NULL, -- untyped; indexed per model via expression index
  workspace_id  uuid NOT NULL,
  PRIMARY KEY (entity_type, entity_id, chunk, model)
);
CREATE INDEX embeddings_ws ON embeddings (workspace_id, entity_type);
-- One HNSW index per embedding model in use. Similarity queries must cast and
-- filter identically to hit it:
--   WHERE model = $1 ORDER BY embedding::vector(768) <=> $2::vector(768)
CREATE INDEX emb_nomic_768 ON embeddings
  USING hnsw ((embedding::vector(768)) vector_cosine_ops)
  WHERE model = 'nomic-embed-text';

CREATE TABLE audit_log (
  id            bigserial PRIMARY KEY,
  workspace_id  uuid NOT NULL,
  user_id       uuid,
  action        text NOT NULL,
  target_type   text,
  target_id     text,
  meta          jsonb NOT NULL DEFAULT '{}',
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_log_ws_created ON audit_log (workspace_id, created_at DESC);

-- +goose Down
DROP TABLE audit_log;
DROP TABLE embeddings;
