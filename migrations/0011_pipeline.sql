-- +goose Up
ALTER TABLE raw_items
  ADD COLUMN processed_at  timestamptz,  -- pipeline finished (kept or dropped)
  ADD COLUMN filter_reason text,         -- why it produced no opportunity: negative_keyword | stale | too_short | duplicate | near_duplicate | not_relevant | below_min_score
  ADD COLUMN cluster_id    uuid;         -- near-duplicate group shared with its opportunities
CREATE INDEX raw_items_cluster ON raw_items (workspace_id, cluster_id) WHERE cluster_id IS NOT NULL;
CREATE INDEX raw_items_unprocessed ON raw_items (workspace_id, fetched_at) WHERE processed_at IS NULL;
CREATE INDEX opp_featured ON opportunities (workspace_id, featured_on) WHERE featured_on IS NOT NULL;

-- +goose Down
DROP INDEX opp_featured;
DROP INDEX raw_items_unprocessed;
DROP INDEX raw_items_cluster;
ALTER TABLE raw_items DROP COLUMN cluster_id, DROP COLUMN filter_reason, DROP COLUMN processed_at;
