-- ---- sources ----

-- name: ListKnowledgeSources :many
SELECT * FROM knowledge_sources WHERE workspace_id = $1 ORDER BY created_at;

-- name: ListEnabledKnowledgeSources :many
SELECT * FROM knowledge_sources WHERE enabled ORDER BY created_at;

-- name: GetKnowledgeSource :one
SELECT * FROM knowledge_sources WHERE workspace_id = $1 AND id = $2;

-- name: CreateKnowledgeSource :one
INSERT INTO knowledge_sources (workspace_id, name, repo, branch, path_prefix, exclude, token_secret_id, token_env, enabled)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: UpdateKnowledgeSource :one
UPDATE knowledge_sources SET name = $3, repo = $4, branch = $5, path_prefix = $6, exclude = $7,
  token_secret_id = $8, token_env = $9, enabled = $10,
  -- a changed location must be re-read from scratch
  last_commit = CASE WHEN repo = $4 AND branch = $5 AND path_prefix = $6 AND exclude = $7 THEN last_commit ELSE NULL END
WHERE workspace_id = $1 AND id = $2
RETURNING *;

-- name: DeleteKnowledgeSource :execrows
DELETE FROM knowledge_sources WHERE workspace_id = $1 AND id = $2;

-- name: SetKnowledgeSourceStatus :exec
UPDATE knowledge_sources SET status = $3, last_error = sqlc.narg(last_error) WHERE workspace_id = $1 AND id = $2;

-- name: RecordKnowledgeSync :exec
UPDATE knowledge_sources SET last_commit = $3, last_synced_at = now(), status = $4, last_error = sqlc.narg(last_error)
WHERE workspace_id = $1 AND id = $2;

-- ---- docs ----

-- name: ListKnowledgeDocs :many
SELECT d.id, d.path, d.blob_sha, d.commit_sha, d.title, d.informs, d.digest IS NOT NULL AND d.digest_sha = d.blob_sha AS digested, d.updated_at,
  (SELECT count(*) FROM knowledge_chunks c WHERE c.doc_id = d.id)::int AS chunks
FROM knowledge_docs d
WHERE d.workspace_id = $1 AND d.source_id = $2
ORDER BY d.path;

-- name: KnowledgeDocBlobs :many
SELECT path, blob_sha FROM knowledge_docs WHERE workspace_id = $1 AND source_id = $2;

-- name: UpsertKnowledgeDoc :one
INSERT INTO knowledge_docs (workspace_id, source_id, path, blob_sha, commit_sha, title, content)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (source_id, path) DO UPDATE
  SET blob_sha = EXCLUDED.blob_sha, commit_sha = EXCLUDED.commit_sha, title = EXCLUDED.title, content = EXCLUDED.content, updated_at = now()
RETURNING id;

-- name: TouchKnowledgeDocCommit :exec
-- Unchanged docs keep their content but link to the latest commit.
UPDATE knowledge_docs SET commit_sha = $3 WHERE workspace_id = $1 AND source_id = $2;

-- name: DeleteKnowledgeDocsNotIn :many
DELETE FROM knowledge_docs
WHERE workspace_id = $1 AND source_id = $2 AND NOT (path = ANY(sqlc.arg(keep)::text[]))
RETURNING id;

-- name: DocsNeedingDigest :many
SELECT id, path, title, blob_sha FROM knowledge_docs
WHERE workspace_id = $1 AND (digest IS NULL OR digest_sha IS DISTINCT FROM blob_sha)
ORDER BY path;

-- name: SetKnowledgeDocDigest :exec
UPDATE knowledge_docs SET digest = $3, digest_sha = $4, informs = $5 WHERE workspace_id = $1 AND id = $2;

-- name: DigestedDocs :many
SELECT d.path, d.title, d.blob_sha, d.commit_sha, d.digest, d.informs FROM knowledge_docs d
WHERE d.workspace_id = $1 AND d.digest IS NOT NULL AND d.digest_sha = d.blob_sha
ORDER BY d.path;

-- name: KnowledgeLatestCommit :one
SELECT coalesce(max(commit_sha), '')::text FROM knowledge_docs WHERE workspace_id = $1;

-- ---- chunks ----

-- name: DeleteKnowledgeChunks :many
DELETE FROM knowledge_chunks WHERE workspace_id = $1 AND doc_id = $2 RETURNING id;

-- name: InsertKnowledgeChunk :exec
INSERT INTO knowledge_chunks (workspace_id, doc_id, ord, heading, anchor, content)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ChunksMissingSummary :many
-- Chunks of docs whose digest is stale, in document order.
SELECT c.id, c.heading, c.content FROM knowledge_chunks c
JOIN knowledge_docs d ON d.id = c.doc_id
WHERE c.workspace_id = $1 AND c.summary IS NULL AND (d.digest IS NULL OR d.digest_sha IS DISTINCT FROM d.blob_sha)
ORDER BY d.path, c.ord
LIMIT $2;

-- name: SetKnowledgeChunkSummary :exec
UPDATE knowledge_chunks SET summary = $3 WHERE workspace_id = $1 AND id = $2;

-- name: DocChunks :many
SELECT id, ord, heading, anchor, content, summary FROM knowledge_chunks
WHERE workspace_id = $1 AND doc_id = $2 ORDER BY ord;

-- name: ChunksMissingEmbedding :many
SELECT c.id, c.heading, c.content FROM knowledge_chunks c
WHERE c.workspace_id = $1 AND NOT EXISTS (
  SELECT 1 FROM embeddings e WHERE e.workspace_id = c.workspace_id AND e.entity_type = 'knowledge_chunk' AND e.entity_id = c.id AND e.model = sqlc.arg(model)::text)
ORDER BY c.id
LIMIT sqlc.arg(lim);

-- name: KnowledgeCounts :one
SELECT
  (SELECT count(*) FROM knowledge_docs d WHERE d.workspace_id = $1)::int AS docs,
  (SELECT count(*) FROM knowledge_chunks c WHERE c.workspace_id = $1)::int AS chunks,
  (SELECT count(*) FROM embeddings e WHERE e.workspace_id = $1 AND e.entity_type = 'knowledge_chunk' AND e.model = sqlc.arg(model)::text)::int AS embedded,
  (SELECT count(*) FROM knowledge_docs d WHERE d.workspace_id = $1 AND d.digest IS NOT NULL AND d.digest_sha = d.blob_sha)::int AS digested;

-- name: GetKnowledgeChunk :one
SELECT c.id, c.heading, c.anchor, c.content, d.path, d.commit_sha, s.repo
FROM knowledge_chunks c
JOIN knowledge_docs d ON d.id = c.doc_id
JOIN knowledge_sources s ON s.id = d.source_id
WHERE c.workspace_id = $1 AND c.id = $2;

-- name: ChunksByDocPath :many
SELECT c.id, c.ord, c.heading, c.anchor, c.content, d.path, d.commit_sha, s.repo
FROM knowledge_chunks c
JOIN knowledge_docs d ON d.id = c.doc_id
JOIN knowledge_sources s ON s.id = d.source_id
WHERE c.workspace_id = $1 AND (d.path = sqlc.arg(path)::text OR d.path LIKE '%/' || sqlc.arg(path)::text)
ORDER BY d.path, c.ord;

-- name: SearchKnowledgeText :many
SELECT c.id, c.heading, c.anchor, left(c.content, 600)::text AS snippet, d.path, d.commit_sha, s.repo,
  greatest(ts_rank(to_tsvector('english', c.heading || ' ' || c.content), websearch_to_tsquery('english', sqlc.arg(query)::text)),
           word_similarity(sqlc.arg(query)::text, c.heading || ' ' || left(c.content, 4000)))::float8 AS rank
FROM knowledge_chunks c
JOIN knowledge_docs d ON d.id = c.doc_id
JOIN knowledge_sources s ON s.id = d.source_id
WHERE c.workspace_id = sqlc.arg(workspace_id)
  AND (to_tsvector('english', c.heading || ' ' || c.content) @@ websearch_to_tsquery('english', sqlc.arg(query)::text)
       OR word_similarity(sqlc.arg(query)::text, c.heading || ' ' || left(c.content, 4000)) > 0.4)
ORDER BY rank DESC
LIMIT sqlc.arg(lim);

-- name: SearchKnowledgeVector :many
SELECT c.id, c.heading, c.anchor, left(c.content, 600)::text AS snippet, d.path, d.commit_sha, s.repo,
  (1 - (e.embedding <=> sqlc.arg(vec)::vector))::float8 AS rank
FROM embeddings e
JOIN knowledge_chunks c ON c.id = e.entity_id AND c.workspace_id = e.workspace_id
JOIN knowledge_docs d ON d.id = c.doc_id
JOIN knowledge_sources s ON s.id = d.source_id
WHERE e.workspace_id = sqlc.arg(workspace_id) AND e.entity_type = 'knowledge_chunk' AND e.model = sqlc.arg(model) AND e.dims = sqlc.arg(dims)
ORDER BY e.embedding <=> sqlc.arg(vec)::vector
LIMIT sqlc.arg(lim);

-- ---- memory bank ----

-- name: ListMemoryBank :many
SELECT * FROM memory_bank WHERE workspace_id = $1 ORDER BY name;

-- name: GetMemoryBankFile :one
SELECT * FROM memory_bank WHERE workspace_id = $1 AND name = $2;

-- name: UpsertMemoryBankFile :exec
INSERT INTO memory_bank (workspace_id, name, content, sources, commit_sha, model_run_id, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, now())
ON CONFLICT (workspace_id, name) DO UPDATE
  SET content = EXCLUDED.content, sources = EXCLUDED.sources, commit_sha = EXCLUDED.commit_sha, model_run_id = EXCLUDED.model_run_id, updated_at = now();

-- name: DeleteOrphanKnowledgeEmbeddings :exec
DELETE FROM embeddings e
WHERE e.workspace_id = $1 AND e.entity_type = 'knowledge_chunk'
  AND NOT EXISTS (SELECT 1 FROM knowledge_chunks c WHERE c.id = e.entity_id);
