-- +goose Up
-- Progress of the current or latest monitor run, for the UI: a run is in
-- progress while run_started_at is later than last_run_at (set at the end).
ALTER TABLE monitors
  ADD COLUMN run_started_at    timestamptz,
  ADD COLUMN run_sources_done  int NOT NULL DEFAULT 0,
  ADD COLUMN run_sources_total int NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE monitors DROP COLUMN run_sources_total, DROP COLUMN run_sources_done, DROP COLUMN run_started_at;
