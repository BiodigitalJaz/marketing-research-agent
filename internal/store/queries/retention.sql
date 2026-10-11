-- Retention: collected items past expires_at are deleted, except those a
-- person kept (an opportunity saved, snoozed, or acted on) or a report cites.
-- Deleting an item cascades to its other opportunities, drafts, and events.

-- name: DeleteExpiredRawItems :execrows
DELETE FROM raw_items
WHERE id IN (
  SELECT r.id FROM raw_items r
  WHERE r.workspace_id = $1 AND r.expires_at IS NOT NULL AND r.expires_at < now()
    AND NOT EXISTS (
      SELECT 1 FROM opportunities o
      WHERE o.raw_item_id = r.id AND o.workspace_id = r.workspace_id AND o.status IN ('saved', 'snoozed', 'acted'))
    AND NOT EXISTS (SELECT 1 FROM citations c WHERE c.raw_item_id = r.id)
  LIMIT sqlc.arg(lim)
);

-- name: DeleteOrphanItemEmbeddings :execrows
DELETE FROM embeddings e
WHERE e.workspace_id = $1 AND e.entity_type = 'raw_item'
  AND NOT EXISTS (SELECT 1 FROM raw_items r WHERE r.id = e.entity_id AND r.workspace_id = e.workspace_id);
