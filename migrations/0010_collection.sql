-- +goose Up
ALTER TABLE sources
  ADD COLUMN state      jsonb NOT NULL DEFAULT '{}',   -- connector cursor, e.g. {"last_max_id": 123}
  ADD COLUMN item_count int NOT NULL DEFAULT 0,        -- new items inserted, all time
  ADD COLUMN created_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE sources ADD CONSTRAINT sources_connector_check
  CHECK (connector IN ('rss', 'hn', 'search', 'journalist', 'reddit', 'research_fetch'));

ALTER TABLE monitors
  ADD COLUMN last_run_at    timestamptz,
  ADD COLUMN next_run_at    timestamptz,
  ADD COLUMN last_error     text,
  ADD COLUMN last_new_items int NOT NULL DEFAULT 0;
CREATE INDEX monitors_due ON monitors (next_run_at) WHERE enabled;

-- Queries sent to paid search backends, per workspace per month (budget).
CREATE TABLE search_usage (
  workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  backend      text NOT NULL,
  month        date NOT NULL,
  queries      int NOT NULL DEFAULT 0,
  PRIMARY KEY (workspace_id, backend, month)
);

CREATE INDEX raw_items_ws_fetched ON raw_items (workspace_id, fetched_at DESC);

-- +goose Down
DROP INDEX raw_items_ws_fetched;
DROP TABLE search_usage;
DROP INDEX monitors_due;
ALTER TABLE monitors DROP COLUMN last_new_items, DROP COLUMN last_error, DROP COLUMN next_run_at, DROP COLUMN last_run_at;
ALTER TABLE sources DROP CONSTRAINT sources_connector_check;
ALTER TABLE sources DROP COLUMN created_at, DROP COLUMN item_count, DROP COLUMN state;
