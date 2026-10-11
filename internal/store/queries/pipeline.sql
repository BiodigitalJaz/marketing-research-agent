-- ---- pipeline ----

-- name: GetRawItemForProcessing :one
SELECT r.id, r.workspace_id, r.source_id, r.monitor_id, r.url, r.title, r.body, r.community, r.published_at, r.fetched_at,
  r.metrics, r.content_hash, r.processed_at, s.connector, s.name AS source_name,
  coalesce(m.lanes, '{inbound,media,thought_leadership}'::lane[])::lane[] AS lanes
FROM raw_items r
JOIN sources s ON s.id = r.source_id AND s.workspace_id = r.workspace_id
LEFT JOIN monitors m ON m.id = r.monitor_id AND m.workspace_id = r.workspace_id
WHERE r.workspace_id = $1 AND r.id = $2;

-- name: MarkRawItemProcessed :exec
UPDATE raw_items SET processed_at = now(), filter_reason = $3, cluster_id = coalesce($4, cluster_id)
WHERE workspace_id = $1 AND id = $2;

-- name: FindClusterByContentHash :one
-- An earlier item with identical content that already has a cluster.
SELECT cluster_id::uuid FROM raw_items
WHERE workspace_id = $1 AND content_hash = $2 AND id <> $3 AND cluster_id IS NOT NULL
ORDER BY fetched_at
LIMIT 1;

-- name: NearestRawItem :one
-- Most similar recent item embedding (same model and dimensions).
SELECT r.cluster_id::uuid AS cluster_id, (1 - (e.embedding <=> sqlc.arg(vec)::vector))::float8 AS similarity
FROM embeddings e
JOIN raw_items r ON r.id = e.entity_id AND r.workspace_id = e.workspace_id
WHERE e.workspace_id = sqlc.arg(workspace_id) AND e.entity_type = 'raw_item' AND e.model = sqlc.arg(model) AND e.dims = sqlc.arg(dims)
  AND e.entity_id <> sqlc.arg(exclude_id) AND r.cluster_id IS NOT NULL AND r.fetched_at > now() - interval '14 days'
ORDER BY e.embedding <=> sqlc.arg(vec)::vector
LIMIT 1;

-- name: MaxBrandSimilarity :one
-- Best match between an item embedding and the portfolio/ICP embeddings.
SELECT coalesce(max(1 - (e.embedding <=> sqlc.arg(vec)::vector)), -1)::float8
FROM embeddings e
WHERE e.workspace_id = sqlc.arg(workspace_id) AND e.entity_type IN ('portfolio_item', 'icp')
  AND e.model = sqlc.arg(model) AND e.dims = sqlc.arg(dims);

-- name: CommunityScoreP90 :one
-- 90th percentile engagement score in a community over 30 days (reach).
SELECT coalesce(percentile_cont(0.9) WITHIN GROUP (ORDER BY (metrics->>'score')::float8), 0)::float8
FROM raw_items
WHERE workspace_id = $1 AND community = $2 AND metrics ? 'score' AND jsonb_typeof(metrics->'score') = 'number'
  AND fetched_at > now() - interval '30 days';

-- name: InsertOpportunity :one
INSERT INTO opportunities (workspace_id, raw_item_id, lane, opp_type, score, score_breakdown, explanation, deadline, cluster_id, model_run_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (raw_item_id, lane) DO NOTHING
RETURNING id;

-- name: ListUnprocessedRawItems :many
SELECT id FROM raw_items WHERE workspace_id = $1 AND processed_at IS NULL ORDER BY fetched_at LIMIT $2;

-- name: ResetRawItemsForReprocess :execrows
-- Items that produced no opportunity are re-run (e.g. after models change).
UPDATE raw_items r SET processed_at = NULL, filter_reason = NULL
WHERE r.workspace_id = $1 AND r.processed_at IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM opportunities o WHERE o.raw_item_id = r.id AND o.workspace_id = r.workspace_id)
  AND r.fetched_at > now() - interval '14 days'
  AND r.filter_reason IS DISTINCT FROM 'research';

-- name: UpdateLaneConfig :one
UPDATE lane_configs SET weights = $3, daily_quota = $4, freshness_hours = $5, min_score = $6, draft_kind = $7
WHERE workspace_id = $1 AND lane = $2
RETURNING *;

-- ---- inbox ----

-- name: ListOpportunities :many
SELECT o.id, o.lane, o.opp_type, o.score, o.score_breakdown, o.explanation, o.deadline, o.status, o.snoozed_until,
  o.featured_on, o.cluster_id, o.created_at, r.id AS raw_item_id, r.url, r.title, left(r.body, 280)::text AS snippet,
  r.community, r.published_at, r.fetched_at, s.name AS source_name
FROM opportunities o
JOIN raw_items r ON r.id = o.raw_item_id AND r.workspace_id = o.workspace_id
JOIN sources s ON s.id = r.source_id AND s.workspace_id = r.workspace_id
WHERE o.workspace_id = sqlc.arg(workspace_id)
  AND (sqlc.narg(lane)::lane IS NULL OR o.lane = sqlc.narg(lane)::lane)
  AND (sqlc.narg(status)::opp_status IS NULL OR o.status = sqlc.narg(status)::opp_status)
  AND (sqlc.narg(opp_type)::text IS NULL OR o.opp_type = sqlc.narg(opp_type)::text)
  AND o.score >= sqlc.arg(min_score)::numeric
  AND (NOT sqlc.arg(featured_only)::boolean OR o.featured_on = sqlc.arg(today)::date)
  AND (sqlc.narg(cursor_score)::numeric IS NULL OR (o.score, o.id) < (sqlc.narg(cursor_score)::numeric, sqlc.narg(cursor_id)::uuid))
ORDER BY (o.featured_on = sqlc.arg(today)::date) DESC NULLS LAST, o.score DESC, o.id DESC
LIMIT sqlc.arg(lim);

-- name: CountOpportunitiesByLane :many
SELECT lane, count(*) FILTER (WHERE status = 'new')::int AS new_count,
  count(*) FILTER (WHERE status = 'new' AND featured_on = sqlc.arg(today)::date)::int AS featured_count
FROM opportunities WHERE workspace_id = sqlc.arg(workspace_id) GROUP BY lane;

-- name: GetOpportunity :one
SELECT o.*, r.url, r.title, r.body, r.community, r.author_handle, r.published_at, r.fetched_at, r.metrics,
  s.name AS source_name, s.connector
FROM opportunities o
JOIN raw_items r ON r.id = o.raw_item_id AND r.workspace_id = o.workspace_id
JOIN sources s ON s.id = r.source_id AND s.workspace_id = r.workspace_id
WHERE o.workspace_id = $1 AND o.id = $2;

-- name: SetOpportunityStatus :one
UPDATE opportunities SET status = $3, snoozed_until = $4
WHERE workspace_id = $1 AND id = $2
RETURNING *;

-- name: InsertOpportunityEvent :exec
INSERT INTO opportunity_events (opportunity_id, user_id, event, reason, outcome, meta)
SELECT o.id, $3, $4, $5, $6, $7 FROM opportunities o WHERE o.workspace_id = $1 AND o.id = $2;

-- name: ListOpportunityEvents :many
SELECT e.* FROM opportunity_events e
JOIN opportunities o ON o.id = e.opportunity_id
WHERE o.workspace_id = $1 AND o.id = $2
ORDER BY e.created_at;

-- name: FeatureTopOpportunities :execrows
-- Feature up to `quota` (minus those already featured today) of the best
-- new, fresh, canonical (first in cluster) opportunities in a lane.
WITH already AS (
  SELECT count(*) AS n FROM opportunities a
  WHERE a.workspace_id = sqlc.arg(workspace_id) AND a.lane = sqlc.arg(lane) AND a.featured_on = sqlc.arg(today)::date
), candidates AS (
  SELECT o.id FROM opportunities o
  JOIN raw_items r ON r.id = o.raw_item_id AND r.workspace_id = o.workspace_id
  WHERE o.workspace_id = sqlc.arg(workspace_id) AND o.lane = sqlc.arg(lane) AND o.status = 'new' AND o.featured_on IS NULL
    AND coalesce(r.published_at, r.fetched_at) > now() - make_interval(hours => sqlc.arg(freshness_hours)::int)
    AND NOT EXISTS (
      SELECT 1 FROM opportunities o2
      WHERE o2.workspace_id = o.workspace_id AND o2.lane = o.lane AND o2.cluster_id = o.cluster_id
        AND o2.id <> o.id AND (o2.created_at, o2.id) < (o.created_at, o.id))
  ORDER BY o.score DESC, o.id
  LIMIT greatest(sqlc.arg(quota)::int - (SELECT n FROM already), 0)
)
UPDATE opportunities u SET featured_on = sqlc.arg(today)::date
WHERE u.id IN (SELECT c.id FROM candidates c);

-- name: ExpireOpportunities :execrows
-- New opportunities past their deadline or older than their lane's freshness window.
UPDATE opportunities o SET status = 'expired'
FROM raw_items r, lane_configs lc
WHERE o.workspace_id = $1 AND o.status = 'new'
  AND r.id = o.raw_item_id AND r.workspace_id = o.workspace_id
  AND lc.workspace_id = o.workspace_id AND lc.lane = o.lane
  AND ((o.deadline IS NOT NULL AND o.deadline < now())
    OR coalesce(r.published_at, r.fetched_at) < now() - make_interval(hours => lc.freshness_hours));

-- name: UnsnoozeOpportunities :execrows
UPDATE opportunities SET status = 'new', snoozed_until = NULL
WHERE workspace_id = $1 AND status = 'snoozed' AND snoozed_until <= now();

-- name: ListWorkspaceIDs :many
-- System maintenance jobs iterate workspaces; each query below filters by one.
SELECT id FROM workspaces ORDER BY created_at;
