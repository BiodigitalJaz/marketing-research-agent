-- +goose Up
ALTER TABLE drafts
  ADD COLUMN workspace_id uuid REFERENCES workspaces(id) ON DELETE CASCADE,
  ADD COLUMN batch_id     uuid,          -- one generation run; the detail view shows the latest batch per kind
  ADD COLUMN subject      text,          -- pitch subject line
  ADD COLUMN instruction  text,          -- the user's "regenerate with instruction" text, if any
  ADD COLUMN word_count   int NOT NULL DEFAULT 0,
  ADD COLUMN created_by   uuid REFERENCES users(id) ON DELETE SET NULL;
UPDATE drafts d SET workspace_id = o.workspace_id, batch_id = d.id
FROM opportunities o WHERE o.id = d.opportunity_id;
ALTER TABLE drafts ALTER COLUMN workspace_id SET NOT NULL, ALTER COLUMN batch_id SET NOT NULL;
DROP INDEX drafts_opp;
CREATE INDEX drafts_opp ON drafts (workspace_id, opportunity_id, created_at DESC);

-- +goose Down
DROP INDEX drafts_opp;
CREATE INDEX drafts_opp ON drafts (opportunity_id);
ALTER TABLE drafts DROP COLUMN created_by, DROP COLUMN word_count, DROP COLUMN instruction,
  DROP COLUMN subject, DROP COLUMN batch_id, DROP COLUMN workspace_id;
