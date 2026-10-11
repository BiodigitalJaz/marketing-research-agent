-- ---- sources ----

-- name: ListSources :many
SELECT * FROM sources WHERE workspace_id = $1 ORDER BY name;

-- name: GetSource :one
SELECT * FROM sources WHERE workspace_id = $1 AND id = $2;

-- name: GetSourcesByIDs :many
SELECT * FROM sources WHERE workspace_id = $1 AND id = ANY(sqlc.arg(ids)::uuid[]) ORDER BY name;

-- name: CreateSource :one
INSERT INTO sources (workspace_id, connector, name, config, enabled)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: UpdateSource :one
UPDATE sources SET name = $3, config = $4, enabled = $5
WHERE workspace_id = $1 AND id = $2
RETURNING *;

-- name: DeleteSource :execrows
DELETE FROM sources WHERE workspace_id = $1 AND id = $2;

-- name: RecordSourceRun :exec
UPDATE sources SET health = $3, last_error = $4, last_run_at = now(), state = $5, item_count = item_count + $6
WHERE workspace_id = $1 AND id = $2;

-- name: GetSourceByName :one
SELECT * FROM sources WHERE workspace_id = $1 AND name = $2 LIMIT 1;

-- ---- monitors ----

-- name: ListMonitors :many
SELECT * FROM monitors WHERE workspace_id = $1 ORDER BY name;

-- name: GetMonitor :one
SELECT * FROM monitors WHERE workspace_id = $1 AND id = $2;

-- name: CreateMonitor :one
INSERT INTO monitors (workspace_id, name, source_ids, query, lanes, schedule_cron, enabled, next_run_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: UpdateMonitor :one
UPDATE monitors SET name = $3, source_ids = $4, query = $5, lanes = $6, schedule_cron = $7, enabled = $8, next_run_at = $9
WHERE workspace_id = $1 AND id = $2
RETURNING *;

-- name: DeleteMonitor :execrows
DELETE FROM monitors WHERE workspace_id = $1 AND id = $2;

-- name: RecordMonitorRun :exec
UPDATE monitors SET last_run_at = now(), last_error = $3, last_new_items = $4
WHERE workspace_id = $1 AND id = $2;

-- name: GetMonitorByName :one
SELECT * FROM monitors WHERE workspace_id = $1 AND name = $2 LIMIT 1;

-- name: ListDueMonitors :many
-- System scheduler query: scans all workspaces for due monitors. Each row
-- carries its workspace_id, which every downstream query filters by.
SELECT id, workspace_id, schedule_cron FROM monitors
WHERE enabled AND (next_run_at IS NULL OR next_run_at <= now())
ORDER BY next_run_at NULLS FIRST
LIMIT 200;

-- name: SetMonitorNextRun :exec
UPDATE monitors SET next_run_at = $3 WHERE workspace_id = $1 AND id = $2;

-- ---- raw items ----

-- name: InsertRawItem :one
-- Returns no row when the item already exists (deduplicated by source + external id).
INSERT INTO raw_items (workspace_id, source_id, monitor_id, external_id, url, author_handle, title, body, community,
  published_at, metrics, content_hash, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (source_id, external_id) DO NOTHING
RETURNING id;

-- name: ListRawItems :many
SELECT r.id, r.source_id, s.name AS source_name, s.connector, r.monitor_id, r.url, r.title, left(r.body, 400)::text AS snippet,
  r.community, r.author_handle, r.published_at, r.fetched_at, r.metrics
FROM raw_items r
JOIN sources s ON s.id = r.source_id AND s.workspace_id = r.workspace_id
WHERE r.workspace_id = $1
  AND (sqlc.narg(source_id)::uuid IS NULL OR r.source_id = sqlc.narg(source_id)::uuid)
  AND (sqlc.narg(monitor_id)::uuid IS NULL OR r.monitor_id = sqlc.narg(monitor_id)::uuid)
ORDER BY r.fetched_at DESC, r.id
LIMIT $2;

-- name: CountRawItems :one
SELECT count(*) FROM raw_items WHERE workspace_id = $1;

-- ---- search usage ----

-- name: IncrementSearchUsage :one
INSERT INTO search_usage (workspace_id, backend, month, queries) VALUES ($1, $2, date_trunc('month', now())::date, 1)
ON CONFLICT (workspace_id, backend, month) DO UPDATE SET queries = search_usage.queries + 1
RETURNING queries;

-- name: GetSearchUsage :one
SELECT coalesce(sum(queries), 0)::int FROM search_usage
WHERE workspace_id = $1 AND backend = $2 AND month = date_trunc('month', now())::date;

-- name: StartMonitorRun :exec
UPDATE monitors SET run_started_at = now(), run_sources_done = 0, run_sources_total = $3
WHERE workspace_id = $1 AND id = $2;

-- name: MonitorSourceDone :exec
UPDATE monitors SET run_sources_done = run_sources_done + 1
WHERE workspace_id = $1 AND id = $2;

-- name: MonitorRunItems :one
-- Items a monitor run collected (fetched from its sources during the run),
-- how many the pipeline has processed, and the opportunities they produced.
WITH run AS (
  SELECT r.id, r.processed_at FROM raw_items r
  WHERE r.workspace_id = sqlc.arg(workspace_id) AND r.source_id = ANY(sqlc.arg(source_ids)::uuid[])
    AND r.fetched_at >= sqlc.arg(since) AND r.fetched_at <= sqlc.arg(until)
)
SELECT
  (SELECT count(*) FROM run)::int AS collected,
  (SELECT count(*) FROM run WHERE processed_at IS NOT NULL)::int AS processed,
  (SELECT count(*) FROM opportunities o JOIN run ON run.id = o.raw_item_id WHERE o.workspace_id = sqlc.arg(workspace_id))::int AS opportunities;

-- name: RemoveSourceFromMonitors :exec
-- A deleted source leaves every monitor that listed it.
UPDATE monitors SET source_ids = array_remove(source_ids, sqlc.arg(source_id)::uuid)
WHERE workspace_id = sqlc.arg(workspace_id) AND sqlc.arg(source_id)::uuid = ANY(source_ids);
