-- ---- research jobs ----

-- name: CreateResearchJob :one
INSERT INTO research_jobs (workspace_id, created_by, mode, template, question, use_web)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetResearchJob :one
SELECT * FROM research_jobs WHERE workspace_id = $1 AND id = $2;

-- name: ListResearchJobs :many
-- Active jobs, or only archived ones when archived is true.
SELECT * FROM research_jobs
WHERE workspace_id = $1 AND (archived_at IS NOT NULL) = sqlc.arg(archived)::bool
ORDER BY created_at DESC
LIMIT $2;

-- name: SetResearchArchived :execrows
UPDATE research_jobs SET archived_at = CASE WHEN sqlc.arg(archived)::bool THEN now() ELSE NULL END
WHERE workspace_id = $1 AND id = $2;

-- name: DeleteResearchJob :execrows
-- Steps and chat messages go with the job; reports keep their content
-- (their job_id becomes NULL). Running jobs must be cancelled first.
DELETE FROM research_jobs
WHERE workspace_id = $1 AND id = $2 AND status NOT IN ('queued', 'running');

-- name: ReportsForJobs :many
SELECT DISTINCT ON (job_id) job_id::uuid AS job_id, id FROM reports
WHERE workspace_id = $1 AND job_id = ANY(sqlc.arg(job_ids)::uuid[])
ORDER BY job_id, created_at DESC;

-- name: StartResearchJob :one
-- Claims a queued job for running; returns no row if it was cancelled.
UPDATE research_jobs SET status = 'running', started_at = coalesce(started_at, now()), error = NULL, finished_at = NULL
WHERE workspace_id = $1 AND id = $2 AND status = 'queued'
RETURNING *;

-- name: FinishResearchJob :exec
-- Leaves cancelled jobs cancelled.
UPDATE research_jobs SET status = $3, error = sqlc.narg(error), finished_at = now()
WHERE workspace_id = $1 AND id = $2 AND status = 'running';

-- name: CancelResearchJob :one
UPDATE research_jobs SET status = 'cancelled', finished_at = now(), error = coalesce(error, 'Cancelled by a user.')
WHERE workspace_id = $1 AND id = $2 AND status IN ('queued', 'running')
RETURNING *;

-- name: RequeueResearchJob :one
-- A chat follow-up: only a finished conversation can take another turn.
UPDATE research_jobs SET status = 'queued', error = NULL, finished_at = NULL
WHERE workspace_id = $1 AND id = $2 AND mode = 'chat' AND status IN ('succeeded', 'failed', 'cancelled')
RETURNING *;

-- name: SetResearchProgress :exec
UPDATE research_jobs SET progress = $3 WHERE workspace_id = $1 AND id = $2;

-- name: ResearchJobStatus :one
SELECT status FROM research_jobs WHERE workspace_id = $1 AND id = $2;

-- ---- steps (streamed over SSE) ----

-- name: InsertResearchStep :one
INSERT INTO research_steps (job_id, seq, kind, payload, model_run_id)
SELECT j.id, coalesce((SELECT max(s.seq) FROM research_steps s WHERE s.job_id = j.id), 0) + 1, sqlc.arg(kind), sqlc.arg(payload), sqlc.narg(model_run_id)
FROM research_jobs j WHERE j.workspace_id = sqlc.arg(workspace_id) AND j.id = sqlc.arg(job_id)
RETURNING *;

-- name: NotifyResearch :exec
SELECT pg_notify('research_events', sqlc.arg(payload)::text);

-- name: ListResearchSteps :many
SELECT s.* FROM research_steps s
JOIN research_jobs j ON j.id = s.job_id
WHERE j.workspace_id = $1 AND s.job_id = $2 AND s.seq > sqlc.arg(after_seq)::int
ORDER BY s.seq
LIMIT 1000;

-- ---- chat ----

-- name: InsertChatMessage :one
INSERT INTO chat_messages (job_id, role, content, sources, report_id)
SELECT j.id, sqlc.arg(role), sqlc.arg(content), sqlc.arg(sources), sqlc.narg(report_id)
FROM research_jobs j WHERE j.workspace_id = sqlc.arg(workspace_id) AND j.id = sqlc.arg(job_id)
RETURNING *;

-- name: ListChatMessages :many
SELECT m.* FROM chat_messages m
JOIN research_jobs j ON j.id = m.job_id
WHERE j.workspace_id = $1 AND m.job_id = $2
ORDER BY m.id;

-- ---- reports ----

-- name: InsertReport :one
INSERT INTO reports (workspace_id, job_id, title, markdown, charts, template)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: InsertCitation :exec
INSERT INTO citations (report_id, marker, raw_item_id, url, title)
SELECT r.id, sqlc.arg(marker), sqlc.narg(raw_item_id), sqlc.arg(url), sqlc.narg(title)
FROM reports r WHERE r.workspace_id = sqlc.arg(workspace_id) AND r.id = sqlc.arg(report_id);

-- name: ListReports :many
SELECT r.id, r.job_id, r.title, r.version, r.template, r.created_at,
  (SELECT count(*) FROM citations c WHERE c.report_id = r.id)::int AS citation_count
FROM reports r WHERE r.workspace_id = $1
ORDER BY r.created_at DESC
LIMIT $2;

-- name: GetReport :one
SELECT * FROM reports WHERE workspace_id = $1 AND id = $2;

-- name: ListCitations :many
SELECT c.* FROM citations c
JOIN reports r ON r.id = c.report_id
WHERE r.workspace_id = $1 AND c.report_id = $2
ORDER BY c.id;

-- ---- corpus tools ----

-- name: SearchCorpusText :many
-- Keyword search over collected items: full-text match or trigram word
-- similarity, ranked by the better of the two.
SELECT r.id, r.url, r.title, left(r.body, 500)::text AS snippet, r.community, s.name AS source_name, r.published_at, r.fetched_at,
  greatest(ts_rank(to_tsvector('english', coalesce(r.title, '') || ' ' || r.body), websearch_to_tsquery('english', sqlc.arg(query)::text)),
           word_similarity(sqlc.arg(query)::text, coalesce(r.title, '') || ' ' || left(r.body, 4000)))::float8 AS rank
FROM raw_items r
JOIN sources s ON s.id = r.source_id AND s.workspace_id = r.workspace_id
WHERE r.workspace_id = sqlc.arg(workspace_id)
  AND (to_tsvector('english', coalesce(r.title, '') || ' ' || r.body) @@ websearch_to_tsquery('english', sqlc.arg(query)::text)
       OR word_similarity(sqlc.arg(query)::text, coalesce(r.title, '') || ' ' || left(r.body, 4000)) > 0.4)
  AND (sqlc.narg(since)::timestamptz IS NULL OR coalesce(r.published_at, r.fetched_at) >= sqlc.narg(since)::timestamptz)
  AND (sqlc.narg(community)::text IS NULL OR coalesce(r.community, s.name) ILIKE '%' || sqlc.narg(community)::text || '%')
  AND (sqlc.narg(lane)::lane IS NULL OR EXISTS (SELECT 1 FROM opportunities o WHERE o.raw_item_id = r.id AND o.workspace_id = r.workspace_id AND o.lane = sqlc.narg(lane)::lane))
ORDER BY rank DESC, r.fetched_at DESC
LIMIT sqlc.arg(lim);

-- name: SearchCorpusVector :many
-- Semantic search over items embedded with the workspace's current model.
SELECT r.id, r.url, r.title, left(r.body, 500)::text AS snippet, r.community, s.name AS source_name, r.published_at, r.fetched_at,
  (1 - (e.embedding <=> sqlc.arg(vec)::vector))::float8 AS rank
FROM embeddings e
JOIN raw_items r ON r.id = e.entity_id AND r.workspace_id = e.workspace_id
JOIN sources s ON s.id = r.source_id AND s.workspace_id = r.workspace_id
WHERE e.workspace_id = sqlc.arg(workspace_id) AND e.entity_type = 'raw_item' AND e.model = sqlc.arg(model) AND e.dims = sqlc.arg(dims)
  AND (sqlc.narg(since)::timestamptz IS NULL OR coalesce(r.published_at, r.fetched_at) >= sqlc.narg(since)::timestamptz)
  AND (sqlc.narg(community)::text IS NULL OR coalesce(r.community, s.name) ILIKE '%' || sqlc.narg(community)::text || '%')
  AND (sqlc.narg(lane)::lane IS NULL OR EXISTS (SELECT 1 FROM opportunities o WHERE o.raw_item_id = r.id AND o.workspace_id = r.workspace_id AND o.lane = sqlc.narg(lane)::lane))
ORDER BY e.embedding <=> sqlc.arg(vec)::vector
LIMIT sqlc.arg(lim);

-- name: GetResearchItem :one
SELECT r.id, r.url, r.title, r.body, r.community, s.name AS source_name, r.published_at, r.fetched_at
FROM raw_items r
JOIN sources s ON s.id = r.source_id AND s.workspace_id = r.workspace_id
WHERE r.workspace_id = $1 AND r.id = $2;

-- name: OpportunityStats :many
-- Opportunity counts grouped by one dimension (chart data for reports).
SELECT (CASE sqlc.arg(group_by)::text
    WHEN 'lane' THEN o.lane::text
    WHEN 'opp_type' THEN o.opp_type
    WHEN 'status' THEN o.status::text
    WHEN 'community' THEN coalesce(r.community, s.name)
    WHEN 'source' THEN s.name
    WHEN 'day' THEN to_char(date_trunc('day', o.created_at), 'YYYY-MM-DD')
    ELSE to_char(date_trunc('week', o.created_at), 'YYYY-MM-DD')
  END)::text AS key,
  count(*)::int AS count,
  round(avg(o.score), 3)::float8 AS avg_score
FROM opportunities o
JOIN raw_items r ON r.id = o.raw_item_id AND r.workspace_id = o.workspace_id
JOIN sources s ON s.id = r.source_id AND s.workspace_id = r.workspace_id
WHERE o.workspace_id = sqlc.arg(workspace_id) AND o.created_at >= sqlc.arg(since)::timestamptz
  AND (sqlc.narg(lane)::lane IS NULL OR o.lane = sqlc.narg(lane)::lane)
GROUP BY 1
ORDER BY (CASE WHEN sqlc.arg(group_by)::text IN ('day', 'week') THEN 1 ELSE 0 END), count(*) DESC, 1
LIMIT 60;

-- name: ItemStats :many
-- Collected item counts grouped by one dimension.
SELECT (CASE sqlc.arg(group_by)::text
    WHEN 'community' THEN coalesce(r.community, s.name)
    WHEN 'source' THEN s.name
    WHEN 'day' THEN to_char(date_trunc('day', coalesce(r.published_at, r.fetched_at)), 'YYYY-MM-DD')
    ELSE to_char(date_trunc('week', coalesce(r.published_at, r.fetched_at)), 'YYYY-MM-DD')
  END)::text AS key,
  count(*)::int AS count
FROM raw_items r
JOIN sources s ON s.id = r.source_id AND s.workspace_id = r.workspace_id
WHERE r.workspace_id = sqlc.arg(workspace_id) AND coalesce(r.published_at, r.fetched_at) >= sqlc.arg(since)::timestamptz
  AND s.connector <> 'research_fetch'
GROUP BY 1
ORDER BY (CASE WHEN sqlc.arg(group_by)::text IN ('day', 'week') THEN 1 ELSE 0 END), count(*) DESC, 1
LIMIT 60;

-- name: ResearchSourceID :one
-- The workspace's hidden source for pages fetched during research.
WITH existing AS (
  SELECT src.id FROM sources src WHERE src.workspace_id = sqlc.arg(workspace_id)::uuid AND src.connector = 'research_fetch' ORDER BY src.id LIMIT 1
), created AS (
  INSERT INTO sources (workspace_id, connector, name, config, enabled)
  SELECT sqlc.arg(workspace_id)::uuid, 'research_fetch', 'Research fetches', '{}'::jsonb, false
  WHERE NOT EXISTS (SELECT 1 FROM existing)
  RETURNING sources.id
)
SELECT existing.id FROM existing UNION ALL SELECT created.id FROM created;

-- name: UpsertResearchItem :one
-- Pages fetched during research are stored so they can be cited; they are
-- marked processed so the opportunity pipeline ignores them.
INSERT INTO raw_items (workspace_id, source_id, external_id, url, title, body, community, published_at, content_hash, expires_at,
  processed_at, filter_reason)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now(), 'research')
ON CONFLICT (source_id, external_id) DO UPDATE
  SET title = EXCLUDED.title, body = EXCLUDED.body, fetched_at = now(), content_hash = EXCLUDED.content_hash
RETURNING id;
