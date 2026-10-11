-- name: GetOpportunityForDraft :one
SELECT o.id, o.lane, o.opp_type, o.score, o.explanation, o.deadline, o.raw_item_id,
  r.url, r.title, r.body, r.community, r.author_handle, r.published_at,
  s.name AS source_name, s.connector, s.config AS source_config,
  lc.draft_kind, lc.min_score
FROM opportunities o
JOIN raw_items r ON r.id = o.raw_item_id AND r.workspace_id = o.workspace_id
JOIN sources s ON s.id = r.source_id AND s.workspace_id = r.workspace_id
JOIN lane_configs lc ON lc.workspace_id = o.workspace_id AND lc.lane = o.lane
WHERE o.workspace_id = $1 AND o.id = $2;

-- name: MatchPortfolioItems :many
-- Portfolio items closest to an item's stored embedding (same model only).
SELECT p.id, p.kind, p.title, p.url, p.summary,
  (1 - (pe.embedding <=> ie.embedding))::float8 AS similarity
FROM portfolio_items p
JOIN embeddings pe ON pe.workspace_id = p.workspace_id AND pe.entity_type = 'portfolio_item' AND pe.entity_id = p.id
JOIN embeddings ie ON ie.workspace_id = p.workspace_id AND ie.entity_type = 'raw_item'
  AND ie.entity_id = sqlc.arg(raw_item_id) AND ie.model = pe.model AND ie.dims = pe.dims
WHERE p.workspace_id = sqlc.arg(workspace_id)
ORDER BY pe.embedding <=> ie.embedding
LIMIT sqlc.arg(lim);

-- name: RecentPortfolioItems :many
-- Fallback when embeddings are unavailable.
SELECT id, kind, title, url, summary FROM portfolio_items
WHERE workspace_id = $1 ORDER BY created_at DESC LIMIT $2;

-- name: InsertDraft :one
INSERT INTO drafts (workspace_id, opportunity_id, batch_id, kind, variant, subject, body, portfolio_item_ids,
  include_link, rule_flags, word_count, instruction, model_run_id, created_by)
SELECT o.workspace_id, o.id, sqlc.arg(batch_id), sqlc.arg(kind), sqlc.arg(variant), sqlc.narg(subject), sqlc.arg(body),
  sqlc.arg(portfolio_item_ids)::uuid[], sqlc.arg(include_link), sqlc.arg(rule_flags), sqlc.arg(word_count),
  sqlc.narg(instruction), sqlc.narg(model_run_id), sqlc.narg(created_by)
FROM opportunities o WHERE o.workspace_id = sqlc.arg(workspace_id) AND o.id = sqlc.arg(opportunity_id)
RETURNING *;

-- name: ListLatestDrafts :many
-- The newest batch of each draft kind for an opportunity.
SELECT d.* FROM drafts d
WHERE d.workspace_id = $1 AND d.opportunity_id = $2
  AND d.batch_id IN (
    SELECT DISTINCT ON (kind) batch_id FROM drafts
    WHERE workspace_id = $1 AND opportunity_id = $2
    ORDER BY kind, created_at DESC)
ORDER BY d.kind, d.created_at, d.id;

-- name: DraftBelongsTo :one
SELECT EXISTS (SELECT 1 FROM drafts WHERE workspace_id = $1 AND opportunity_id = $2 AND id = $3);

-- name: FeaturedWithoutDrafts :many
-- Today's featured opportunities that have no drafts yet (eager drafting).
SELECT o.id FROM opportunities o
WHERE o.workspace_id = $1 AND o.featured_on = sqlc.arg(today)::date AND o.status = 'new'
  AND NOT EXISTS (SELECT 1 FROM drafts d WHERE d.workspace_id = o.workspace_id AND d.opportunity_id = o.id)
ORDER BY o.score DESC;

-- name: PortfolioItemsByIDs :many
SELECT id, kind, title, url FROM portfolio_items
WHERE workspace_id = $1 AND id = ANY(sqlc.arg(ids)::uuid[]);
