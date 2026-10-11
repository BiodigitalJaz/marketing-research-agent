-- +goose Up
-- UI-entered credentials, AES-256-GCM encrypted (internal/secrets).
-- workspace_id NULL = instance-level (LLM providers, search), which are
-- shared by all workspaces until multi-tenant work in V3.
CREATE TABLE secrets (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id  uuid REFERENCES workspaces(id) ON DELETE CASCADE,
  name          text NOT NULL,
  ciphertext    bytea NOT NULL,              -- AAD = id || workspace_id
  nonce         bytea NOT NULL,
  key_id        text NOT NULL,               -- master key fingerprint (rotation)
  last4         text NOT NULL,
  created_by    uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX secrets_scope_name ON secrets (COALESCE(workspace_id, '00000000-0000-0000-0000-000000000000'::uuid), name);

ALTER TABLE model_providers
  ADD COLUMN name              text,
  ADD COLUMN api_key_secret_id uuid REFERENCES secrets(id) ON DELETE SET NULL, -- wins over api_key_env
  ADD COLUMN api_key_env       text,
  ADD COLUMN headers           jsonb NOT NULL DEFAULT '{}',  -- static, non-secret
  ADD COLUMN options           jsonb NOT NULL DEFAULT '{}',  -- {reasoning_style, max_tokens_param, max_retries}
  ADD COLUMN enabled           boolean NOT NULL DEFAULT true,
  ADD COLUMN updated_at        timestamptz NOT NULL DEFAULT now();

CREATE TABLE provider_models (
  provider_id    text NOT NULL REFERENCES model_providers(id) ON DELETE CASCADE,
  model          text NOT NULL,
  display_name   text,
  capabilities   jsonb NOT NULL DEFAULT '{}',  -- llm.Capabilities
  default_params jsonb NOT NULL DEFAULT '{}',  -- router.Params
  price          jsonb NOT NULL DEFAULT '{}',  -- {input_per_mtok, output_per_mtok}
  enabled        boolean NOT NULL DEFAULT true,
  PRIMARY KEY (provider_id, model)
);

ALTER TABLE task_routes
  ADD COLUMN params         jsonb NOT NULL DEFAULT '{}',   -- overrides model default_params
  ADD COLUMN tools          text[] NOT NULL DEFAULT '{}',  -- built-in tools enabled for the task
  ADD COLUMN mcp_server_ids uuid[] NOT NULL DEFAULT '{}',
  ADD COLUMN updated_at     timestamptz NOT NULL DEFAULT now();

ALTER TABLE prompts
  ADD COLUMN workspace_id uuid REFERENCES workspaces(id) ON DELETE CASCADE, -- NULL = shipped default
  ADD COLUMN created_by   uuid REFERENCES users(id) ON DELETE SET NULL,
  ADD COLUMN notes        text;
ALTER TABLE prompts DROP CONSTRAINT prompts_task_version_key;
CREATE UNIQUE INDEX prompts_ws_task_version ON prompts (COALESCE(workspace_id, '00000000-0000-0000-0000-000000000000'::uuid), task, version);

CREATE TABLE mcp_servers (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id   uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  name           text NOT NULL,
  transport      text NOT NULL CHECK (transport IN ('streamable_http','stdio')),
  url            text,
  command        text,
  args           text[] NOT NULL DEFAULT '{}',
  header_secrets jsonb NOT NULL DEFAULT '{}',  -- {"Authorization": {"secret_id": "..."} | {"env": "NAME"}}
  allowed_tools  text[] NOT NULL DEFAULT '{}', -- deny by default; read-only-annotated tools only
  enabled        boolean NOT NULL DEFAULT true,
  created_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (workspace_id, name)
);

CREATE TABLE workspace_settings (
  workspace_id uuid PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
  local_only   boolean NOT NULL DEFAULT false,
  embeddings   jsonb NOT NULL DEFAULT '{}',  -- {provider_id, model, dimensions}
  search       jsonb NOT NULL DEFAULT '{}',  -- {api_key_secret_id, api_key_env, country, search_lang, safesearch, monthly_budget}
  updated_at   timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE model_runs
  ADD COLUMN thinking_tokens int,
  ADD COLUMN fallback        boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE model_runs DROP COLUMN fallback, DROP COLUMN thinking_tokens;
DROP TABLE workspace_settings;
DROP TABLE mcp_servers;
DROP INDEX prompts_ws_task_version;
ALTER TABLE prompts DROP COLUMN notes, DROP COLUMN created_by, DROP COLUMN workspace_id;
ALTER TABLE prompts ADD CONSTRAINT prompts_task_version_key UNIQUE (task, version);
ALTER TABLE task_routes DROP COLUMN updated_at, DROP COLUMN mcp_server_ids, DROP COLUMN tools, DROP COLUMN params;
DROP TABLE provider_models;
ALTER TABLE model_providers DROP COLUMN updated_at, DROP COLUMN enabled, DROP COLUMN options, DROP COLUMN headers,
  DROP COLUMN api_key_env, DROP COLUMN api_key_secret_id, DROP COLUMN name;
DROP TABLE secrets;
