-- First-run setup. These tables hold no workspace data.

-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: SetSetupToken :exec
INSERT INTO setup_token (id, token_hash) VALUES (true, $1)
ON CONFLICT (id) DO UPDATE SET token_hash = EXCLUDED.token_hash, created_at = now();

-- name: GetSetupTokenHash :one
SELECT token_hash FROM setup_token WHERE id;

-- name: DeleteSetupToken :exec
DELETE FROM setup_token;

-- name: CountTrackedKeywords :one
SELECT count(*) FROM keywords WHERE workspace_id = $1 AND kind = 'track';

-- name: CountEnabledMonitors :one
SELECT count(*) FROM monitors WHERE workspace_id = $1 AND enabled AND cardinality(source_ids) > 0;


-- name: HasEmbeddings :one
SELECT EXISTS (SELECT 1 FROM embeddings WHERE workspace_id = $1);

-- name: HasSuccessfulRunFor :one
SELECT EXISTS (
  SELECT 1 FROM model_runs
  WHERE workspace_id = $1 AND provider_id = $2 AND model = $3 AND error IS NULL
);
