-- +goose Up
-- Archived research jobs are hidden from the research list (still readable).
ALTER TABLE research_jobs ADD COLUMN archived_at timestamptz;

-- +goose Down
ALTER TABLE research_jobs DROP COLUMN archived_at;
