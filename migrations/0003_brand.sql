-- +goose Up
CREATE TABLE brand_profiles (
  workspace_id uuid PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
  profile      jsonb NOT NULL,   -- {services[], value_props[], differentiators[], tone:{voice, do[], dont[], examples[]}}
  updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE portfolio_items (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  kind         text NOT NULL CHECK (kind IN ('case_study','blog_post','work_sample','talk','other')),
  title        text NOT NULL,
  url          text,
  summary      text NOT NULL,
  tags         jsonb NOT NULL DEFAULT '{}',  -- {industries[], services[], problems[]}
  created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX portfolio_items_ws ON portfolio_items (workspace_id);
CREATE TABLE icps (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  name         text NOT NULL,
  definition   jsonb NOT NULL   -- {industries[], company_sizes[], roles[], pain_points[], buying_signals[]}
);
CREATE INDEX icps_ws ON icps (workspace_id);
CREATE TYPE lane AS ENUM ('inbound','media','thought_leadership');
CREATE TABLE keywords (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  term         text NOT NULL,
  kind         text NOT NULL CHECK (kind IN ('track','negative')),
  lane         lane,            -- NULL = all lanes
  UNIQUE (workspace_id, term, kind)
);
CREATE TABLE competitors (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  name         text NOT NULL,
  domains      text[] NOT NULL DEFAULT '{}',
  notes        text
);
CREATE INDEX competitors_ws ON competitors (workspace_id);

-- +goose Down
DROP TABLE competitors;
DROP TABLE keywords;
DROP TYPE lane;
DROP TABLE icps;
DROP TABLE portfolio_items;
DROP TABLE brand_profiles;
