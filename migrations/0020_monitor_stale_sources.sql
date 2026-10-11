-- +goose Up
-- Deleting a source used to leave its id in monitors.source_ids, which then
-- failed validation on every save. Drop ids of sources that no longer exist.
UPDATE monitors m
SET source_ids = ARRAY(
  SELECT x FROM unnest(m.source_ids) AS x
  WHERE EXISTS (SELECT 1 FROM sources s WHERE s.id = x AND s.workspace_id = m.workspace_id))
WHERE EXISTS (
  SELECT 1 FROM unnest(m.source_ids) AS x
  WHERE NOT EXISTS (SELECT 1 FROM sources s WHERE s.id = x AND s.workspace_id = m.workspace_id));

-- +goose Down
SELECT 1;
