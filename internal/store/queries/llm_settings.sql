-- ---- secrets ----
-- Instance-level secrets have workspace_id NULL; workspace secrets carry
-- the workspace. Lookups pass the scope explicitly (NULL for instance).

-- name: CreateSecret :one
INSERT INTO secrets (id, workspace_id, name, ciphertext, nonce, key_id, last4, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, workspace_id, name, key_id, last4, created_at, updated_at;

-- name: UpdateSecretValue :execrows
UPDATE secrets SET ciphertext = $2, nonce = $3, key_id = $4, last4 = $5, updated_at = now()
WHERE id = $1 AND workspace_id IS NOT DISTINCT FROM sqlc.narg(workspace_id)::uuid;

-- name: RenameSecret :execrows
UPDATE secrets SET name = $2, updated_at = now()
WHERE id = $1 AND workspace_id IS NOT DISTINCT FROM sqlc.narg(workspace_id)::uuid;

-- name: GetSecret :one
SELECT * FROM secrets WHERE id = $1 AND workspace_id IS NOT DISTINCT FROM sqlc.narg(workspace_id)::uuid;

-- name: GetSecretAnyScope :one
-- Used to resolve references; callers have already authorized the scope.
SELECT * FROM secrets WHERE id = $1 AND (workspace_id IS NULL OR workspace_id = sqlc.arg(workspace_id)::uuid);

-- name: ListSecrets :many
SELECT s.id, s.workspace_id, s.name, s.key_id, s.last4, s.created_at, s.updated_at,
  (SELECT coalesce(array_agg(p.id ORDER BY p.id), '{}')::text[] FROM model_providers p WHERE p.api_key_secret_id = s.id) AS used_by_providers
FROM secrets s
WHERE s.workspace_id IS NULL OR s.workspace_id = $1
ORDER BY s.name;

-- name: DeleteSecret :execrows
DELETE FROM secrets WHERE id = $1 AND workspace_id IS NOT DISTINCT FROM sqlc.narg(workspace_id)::uuid;

-- name: ListSecretsNotOnKey :many
SELECT * FROM secrets WHERE key_id <> $1;

-- name: ReencryptSecret :exec
UPDATE secrets SET ciphertext = $2, nonce = $3, key_id = $4 WHERE id = $1;

-- ---- providers ----

-- name: ListProviders :many
SELECT * FROM model_providers ORDER BY id;

-- name: GetProvider :one
SELECT * FROM model_providers WHERE id = $1;

-- name: CreateProvider :one
INSERT INTO model_providers (id, kind, base_url, is_local, name, api_key_secret_id, api_key_env, headers, options, enabled)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: UpdateProvider :one
UPDATE model_providers SET kind = $2, base_url = $3, is_local = $4, name = $5, api_key_secret_id = $6,
  api_key_env = $7, headers = $8, options = $9, enabled = $10, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteProvider :execrows
DELETE FROM model_providers WHERE id = $1;

-- ---- models ----

-- name: ListProviderModels :many
SELECT * FROM provider_models ORDER BY provider_id, model;

-- name: GetProviderModel :one
SELECT * FROM provider_models WHERE provider_id = $1 AND model = $2;

-- name: UpsertProviderModel :one
INSERT INTO provider_models (provider_id, model, display_name, capabilities, default_params, price, enabled)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (provider_id, model) DO UPDATE SET display_name = EXCLUDED.display_name, capabilities = EXCLUDED.capabilities,
  default_params = EXCLUDED.default_params, price = EXCLUDED.price, enabled = EXCLUDED.enabled
RETURNING *;

-- name: InsertProviderModelIfMissing :exec
INSERT INTO provider_models (provider_id, model, capabilities) VALUES ($1, $2, $3)
ON CONFLICT (provider_id, model) DO NOTHING;

-- name: DeleteProviderModel :execrows
DELETE FROM provider_models WHERE provider_id = $1 AND model = $2;

-- ---- routes ----

-- name: GetTaskRoute :one
SELECT * FROM task_routes WHERE workspace_id = $1 AND task = $2;

-- name: UpsertTaskRoute :one
INSERT INTO task_routes (workspace_id, task, provider_id, model, fallback_provider_id, fallback_model, params, tools, mcp_server_ids, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
ON CONFLICT (workspace_id, task) DO UPDATE SET provider_id = EXCLUDED.provider_id, model = EXCLUDED.model,
  fallback_provider_id = EXCLUDED.fallback_provider_id, fallback_model = EXCLUDED.fallback_model,
  params = EXCLUDED.params, tools = EXCLUDED.tools, mcp_server_ids = EXCLUDED.mcp_server_ids, updated_at = now()
RETURNING *;

-- ---- prompts ----

-- name: ListPrompts :many
SELECT * FROM prompts
WHERE (workspace_id = $1 OR workspace_id IS NULL) AND task = $2
ORDER BY version DESC;

-- name: GetPrompt :one
SELECT * FROM prompts WHERE id = $1 AND (workspace_id = $2 OR workspace_id IS NULL);

-- name: GetActivePrompt :one
-- A workspace's active version wins over the shipped default.
SELECT * FROM prompts
WHERE task = $2 AND active AND (workspace_id = $1 OR workspace_id IS NULL)
ORDER BY (workspace_id IS NULL), version DESC
LIMIT 1;

-- name: CreatePromptVersion :one
INSERT INTO prompts (workspace_id, task, version, body, notes, created_by, active)
VALUES ($1, $2,
  (SELECT coalesce(max(version), 0) + 1 FROM prompts p WHERE p.task = $2 AND (p.workspace_id = $1 OR p.workspace_id IS NULL)),
  $3, $4, $5, false)
RETURNING *;

-- name: DeactivateWorkspacePrompts :exec
UPDATE prompts SET active = false WHERE workspace_id = $1 AND task = $2;

-- name: ActivateWorkspacePrompt :execrows
UPDATE prompts SET active = true WHERE id = $1 AND workspace_id = $2;

-- name: InsertDefaultPromptIfMissing :exec
INSERT INTO prompts (workspace_id, task, version, body, active)
SELECT NULL, $1, $2, $3, true
WHERE NOT EXISTS (SELECT 1 FROM prompts WHERE workspace_id IS NULL AND task = $1 AND version = $2);

-- ---- MCP servers ----

-- name: ListMCPServers :many
SELECT * FROM mcp_servers WHERE workspace_id = $1 ORDER BY name;

-- name: GetMCPServer :one
SELECT * FROM mcp_servers WHERE workspace_id = $1 AND id = $2;

-- name: GetMCPServersByIDs :many
SELECT * FROM mcp_servers WHERE workspace_id = $1 AND id = ANY(sqlc.arg(ids)::uuid[]) AND enabled;

-- name: CreateMCPServer :one
INSERT INTO mcp_servers (workspace_id, name, transport, url, command, args, header_secrets, allowed_tools, enabled)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: UpdateMCPServer :one
UPDATE mcp_servers SET name = $3, transport = $4, url = $5, command = $6, args = $7, header_secrets = $8,
  allowed_tools = $9, enabled = $10
WHERE workspace_id = $1 AND id = $2
RETURNING *;

-- name: DeleteMCPServer :execrows
DELETE FROM mcp_servers WHERE workspace_id = $1 AND id = $2;

-- ---- workspace settings ----

-- name: GetWorkspaceSettings :one
SELECT * FROM workspace_settings WHERE workspace_id = $1;

-- name: UpsertWorkspaceSettings :one
INSERT INTO workspace_settings (workspace_id, local_only, embeddings, search, updated_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (workspace_id) DO UPDATE SET local_only = EXCLUDED.local_only, embeddings = EXCLUDED.embeddings,
  search = EXCLUDED.search, updated_at = now()
RETURNING *;

-- name: InsertWorkspaceSettingsIfMissing :exec
INSERT INTO workspace_settings (workspace_id, local_only, embeddings, search) VALUES ($1, $2, $3, $4)
ON CONFLICT (workspace_id) DO NOTHING;

-- ---- model runs ----

-- name: InsertModelRun :one
INSERT INTO model_runs (workspace_id, task, provider_id, model, prompt_id, input_tokens, output_tokens, thinking_tokens,
  cost_usd, latency_ms, error, fallback)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING id;

-- name: ListRecentModelRuns :many
SELECT * FROM model_runs WHERE workspace_id = $1 ORDER BY created_at DESC LIMIT $2;
