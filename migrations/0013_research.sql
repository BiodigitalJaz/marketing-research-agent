-- +goose Up
ALTER TABLE research_jobs
  ADD COLUMN error      text,          -- why a job failed or stopped early
  ADD COLUMN started_at timestamptz;
ALTER TABLE chat_messages
  ADD COLUMN sources jsonb NOT NULL DEFAULT '[]',  -- citations for an assistant answer: [{marker, raw_item_id, url, title}]
  ADD COLUMN report_id uuid REFERENCES reports(id) ON DELETE SET NULL;
ALTER TABLE reports ADD COLUMN template text;
CREATE INDEX research_steps_job ON research_steps (job_id, seq);

-- +goose Down
DROP INDEX research_steps_job;
ALTER TABLE reports DROP COLUMN template;
ALTER TABLE chat_messages DROP COLUMN report_id, DROP COLUMN sources;
ALTER TABLE research_jobs DROP COLUMN started_at, DROP COLUMN error;
