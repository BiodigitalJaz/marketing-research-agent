-- +goose Up
-- emb_nomic_768 was never used: it indexed embedding::vector(768) for one
-- model name, and no query casts that way. Similarity search is exact,
-- filtered by workspace and entity type (embeddings_ws), which is correct
-- and fast at this scale; the index only slowed writes.
DROP INDEX IF EXISTS emb_nomic_768;

-- +goose Down
CREATE INDEX emb_nomic_768 ON embeddings
  USING hnsw ((embedding::vector(768)) vector_cosine_ops)
  WHERE model = 'nomic-embed-text';
