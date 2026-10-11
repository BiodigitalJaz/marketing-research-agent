-- name: UpsertEmbedding :exec
INSERT INTO embeddings (entity_type, entity_id, chunk, model, dims, embedding, workspace_id)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (entity_type, entity_id, chunk, model)
DO UPDATE SET embedding = EXCLUDED.embedding, dims = EXCLUDED.dims;

-- name: DeleteEmbeddingsForEntity :exec
DELETE FROM embeddings WHERE workspace_id = $1 AND entity_type = $2 AND entity_id = $3;

-- name: CountEmbeddings :one
SELECT count(*) FROM embeddings
WHERE workspace_id = $1 AND entity_type = $2 AND model = $3;
