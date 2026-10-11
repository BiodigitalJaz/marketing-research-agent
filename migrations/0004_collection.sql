-- +goose Up
CREATE TABLE sources (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id   uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  connector      text NOT NULL,          -- 'reddit' | 'hn' | 'rss' | 'journalist' | 'research_fetch'
  name           text NOT NULL,
  config         jsonb NOT NULL DEFAULT '{}', -- e.g. {subreddits[]} or {feed_url}
  enabled        boolean NOT NULL DEFAULT true,
  health         text NOT NULL DEFAULT 'unknown', -- ok | degraded | failing | unknown
  last_error     text,
  last_run_at    timestamptz
);
CREATE INDEX sources_ws ON sources (workspace_id);
CREATE TABLE monitors (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id   uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  name           text NOT NULL,
  source_ids     uuid[] NOT NULL,
  query          text NOT NULL DEFAULT '',  -- keyword expression, connector-translated
  lanes          lane[] NOT NULL DEFAULT '{inbound,media,thought_leadership}',
  schedule_cron  text NOT NULL DEFAULT '*/30 * * * *',
  enabled        boolean NOT NULL DEFAULT true,
  created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX monitors_ws ON monitors (workspace_id);
CREATE TABLE raw_items (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id   uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  source_id      uuid NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  monitor_id     uuid REFERENCES monitors(id) ON DELETE SET NULL,
  external_id    text NOT NULL,
  url            text NOT NULL,
  author_handle  text,
  title          text,
  body           text NOT NULL,
  community      text,                    -- subreddit, feed name, publication
  published_at   timestamptz,
  fetched_at     timestamptz NOT NULL DEFAULT now(),
  metrics        jsonb NOT NULL DEFAULT '{}',  -- {score, comments, followers...}
  content_hash   bytea NOT NULL,          -- sha256 of normalized title+body
  expires_at     timestamptz,             -- retention
  UNIQUE (source_id, external_id)
);
CREATE INDEX raw_items_ws_published ON raw_items (workspace_id, published_at DESC);
CREATE INDEX raw_items_hash ON raw_items (workspace_id, content_hash);
CREATE INDEX raw_items_trgm ON raw_items USING gin (body gin_trgm_ops);
CREATE INDEX raw_items_expires ON raw_items (expires_at) WHERE expires_at IS NOT NULL;

-- +goose Down
DROP TABLE raw_items;
DROP TABLE monitors;
DROP TABLE sources;
