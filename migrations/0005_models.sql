-- +goose Up
CREATE TABLE model_providers (
  id            text PRIMARY KEY,         -- matches config provider id
  kind          text NOT NULL CHECK (kind IN ('anthropic','openai_compat')),
  base_url      text,
  is_local      boolean NOT NULL DEFAULT false,
  capabilities  jsonb NOT NULL DEFAULT '{}' -- {tools, json_schema, vision, max_context}
);
CREATE TABLE task_routes (
  workspace_id     uuid REFERENCES workspaces(id) ON DELETE CASCADE,
  task             text NOT NULL,         -- classify | score | draft | research | summarize
  provider_id      text NOT NULL REFERENCES model_providers(id),
  model            text NOT NULL,
  fallback_provider_id text REFERENCES model_providers(id),
  fallback_model   text,
  PRIMARY KEY (workspace_id, task)
);
CREATE TABLE prompts (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  task       text NOT NULL,
  version    int NOT NULL,
  body       text NOT NULL,
  active     boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (task, version)
);
CREATE TABLE model_runs (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id   uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  task           text NOT NULL,
  provider_id    text NOT NULL,
  model          text NOT NULL,
  prompt_id      uuid REFERENCES prompts(id),
  input_tokens   int,
  output_tokens  int,
  cost_usd       numeric(12,6),
  latency_ms     int,
  error          text,
  created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX model_runs_ws_created ON model_runs (workspace_id, created_at DESC);

-- +goose Down
DROP TABLE model_runs;
DROP TABLE prompts;
DROP TABLE task_routes;
DROP TABLE model_providers;
