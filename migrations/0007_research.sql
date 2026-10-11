-- +goose Up
CREATE TYPE job_status AS ENUM ('queued','running','succeeded','failed','cancelled');
CREATE TABLE research_jobs (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id  uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  created_by    uuid NOT NULL REFERENCES users(id),
  mode          text NOT NULL CHECK (mode IN ('chat','deep')),
  template      text,          -- competitor_teardown | market_scan | pain_points | topic_deep_dive
  question      text NOT NULL,
  status        job_status NOT NULL DEFAULT 'queued',
  progress      jsonb NOT NULL DEFAULT '{}',   -- {phase, steps_done, steps_planned}
  use_web       boolean NOT NULL DEFAULT true, -- false = corpus only
  created_at    timestamptz NOT NULL DEFAULT now(),
  finished_at   timestamptz
);
CREATE INDEX research_jobs_ws_created ON research_jobs (workspace_id, created_at DESC);
CREATE TABLE research_steps (
  id            bigserial PRIMARY KEY,
  job_id        uuid NOT NULL REFERENCES research_jobs(id) ON DELETE CASCADE,
  seq           int NOT NULL,
  kind          text NOT NULL,  -- plan | tool_call | tool_result | thought_summary | draft_section
  payload       jsonb NOT NULL,
  model_run_id  uuid REFERENCES model_runs(id),
  created_at    timestamptz NOT NULL DEFAULT now(),
  UNIQUE (job_id, seq)
);
CREATE TABLE chat_messages (
  id            bigserial PRIMARY KEY,
  job_id        uuid NOT NULL REFERENCES research_jobs(id) ON DELETE CASCADE,
  role          text NOT NULL CHECK (role IN ('user','assistant')),
  content       text NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX chat_messages_job ON chat_messages (job_id, id);
CREATE TABLE reports (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id  uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  job_id        uuid REFERENCES research_jobs(id) ON DELETE SET NULL,
  title         text NOT NULL,
  version       int NOT NULL DEFAULT 1,
  markdown      text NOT NULL,
  charts        jsonb NOT NULL DEFAULT '[]',   -- chart specs rendered by the frontend
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX reports_ws_created ON reports (workspace_id, created_at DESC);
CREATE TABLE citations (
  id            bigserial PRIMARY KEY,
  report_id     uuid NOT NULL REFERENCES reports(id) ON DELETE CASCADE,
  marker        text NOT NULL,     -- e.g. "[3]" as used in markdown
  raw_item_id   uuid REFERENCES raw_items(id) ON DELETE SET NULL,
  url           text NOT NULL,
  title         text,
  accessed_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX citations_report ON citations (report_id);
CREATE INDEX citations_raw_item ON citations (raw_item_id) WHERE raw_item_id IS NOT NULL;

-- +goose Down
DROP TABLE citations;
DROP TABLE reports;
DROP TABLE chat_messages;
DROP TABLE research_steps;
DROP TABLE research_jobs;
DROP TYPE job_status;
