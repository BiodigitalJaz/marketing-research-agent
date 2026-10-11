-- +goose Up
-- Internal knowledge: docs synced read-only from a Git repository, split
-- into citable sections, and the memory bank generated from them. Used by
-- research only; never by drafting.
CREATE TABLE knowledge_sources (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id    uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  name            text NOT NULL,
  kind            text NOT NULL DEFAULT 'github' CHECK (kind IN ('github')),
  repo            text NOT NULL,                  -- owner/name
  branch          text NOT NULL DEFAULT 'main',
  path_prefix     text NOT NULL DEFAULT 'docs/',
  exclude         text[] NOT NULL DEFAULT '{}',   -- glob patterns, relative to path_prefix
  token_secret_id uuid REFERENCES secrets(id) ON DELETE SET NULL,
  token_env       text,
  enabled         boolean NOT NULL DEFAULT true,
  last_commit     text,
  last_synced_at  timestamptz,
  last_error      text,
  status          text NOT NULL DEFAULT 'idle',   -- idle | syncing | building | error
  created_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (workspace_id, repo, branch, path_prefix)
);
CREATE TABLE knowledge_docs (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id  uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  source_id     uuid NOT NULL REFERENCES knowledge_sources(id) ON DELETE CASCADE,
  path          text NOT NULL,          -- repository path, e.g. docs/SECURITY.md
  blob_sha      text NOT NULL,
  commit_sha    text NOT NULL,          -- commit the content was read at (for links)
  title         text NOT NULL,
  content       text NOT NULL,
  digest        text,                   -- generated summary for the memory bank
  digest_sha    text,                   -- blob_sha the digest was made from
  informs       text[] NOT NULL DEFAULT '{}', -- memory bank files this doc informs
  updated_at    timestamptz NOT NULL DEFAULT now(),
  UNIQUE (source_id, path)
);
CREATE TABLE knowledge_chunks (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id  uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  doc_id        uuid NOT NULL REFERENCES knowledge_docs(id) ON DELETE CASCADE,
  ord           int NOT NULL,
  heading       text NOT NULL,          -- "SECURITY.md › Instance isolation"
  anchor        text NOT NULL,          -- GitHub heading anchor
  content       text NOT NULL,
  summary       text,                   -- generated, for the doc digest
  UNIQUE (doc_id, ord)
);
CREATE INDEX knowledge_chunks_ws ON knowledge_chunks (workspace_id);
CREATE TABLE memory_bank (
  workspace_id  uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  name          text NOT NULL,          -- projectbrief | productContext | ...
  content       text NOT NULL,
  sources       text[] NOT NULL DEFAULT '{}', -- "path@blob_sha" it was built from
  commit_sha    text,
  model_run_id  uuid REFERENCES model_runs(id) ON DELETE SET NULL,
  updated_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (workspace_id, name)
);

-- +goose Down
DROP TABLE memory_bank;
DROP TABLE knowledge_chunks;
DROP TABLE knowledge_docs;
DROP TABLE knowledge_sources;
