-- name: GetBrandProfile :one
SELECT * FROM brand_profiles WHERE workspace_id = $1;

-- name: UpsertBrandProfile :one
INSERT INTO brand_profiles (workspace_id, profile, updated_at)
VALUES ($1, $2, now())
ON CONFLICT (workspace_id) DO UPDATE SET profile = EXCLUDED.profile, updated_at = now()
RETURNING *;

-- name: InsertBrandProfileIfMissing :exec
INSERT INTO brand_profiles (workspace_id, profile) VALUES ($1, $2)
ON CONFLICT (workspace_id) DO NOTHING;

-- name: ListPortfolioItems :many
SELECT p.*,
  EXISTS (SELECT 1 FROM embeddings e
          WHERE e.workspace_id = p.workspace_id AND e.entity_type = 'portfolio_item'
            AND e.entity_id = p.id AND e.model = sqlc.arg(embed_model)::text) AS embedded
FROM portfolio_items p
WHERE p.workspace_id = $1
ORDER BY p.created_at DESC;

-- name: GetPortfolioItem :one
SELECT * FROM portfolio_items WHERE workspace_id = $1 AND id = $2;

-- name: CreatePortfolioItem :one
INSERT INTO portfolio_items (workspace_id, kind, title, url, summary, tags)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: UpdatePortfolioItem :one
UPDATE portfolio_items SET kind = $3, title = $4, url = $5, summary = $6, tags = $7
WHERE workspace_id = $1 AND id = $2
RETURNING *;

-- name: DeletePortfolioItem :execrows
DELETE FROM portfolio_items WHERE workspace_id = $1 AND id = $2;

-- name: CountPortfolioItems :one
SELECT count(*) FROM portfolio_items WHERE workspace_id = $1;

-- name: ListICPs :many
SELECT i.*,
  EXISTS (SELECT 1 FROM embeddings e
          WHERE e.workspace_id = i.workspace_id AND e.entity_type = 'icp'
            AND e.entity_id = i.id AND e.model = sqlc.arg(embed_model)::text) AS embedded
FROM icps i
WHERE i.workspace_id = $1
ORDER BY i.name;

-- name: GetICP :one
SELECT * FROM icps WHERE workspace_id = $1 AND id = $2;

-- name: CreateICP :one
INSERT INTO icps (workspace_id, name, definition) VALUES ($1, $2, $3)
RETURNING *;

-- name: UpdateICP :one
UPDATE icps SET name = $3, definition = $4
WHERE workspace_id = $1 AND id = $2
RETURNING *;

-- name: DeleteICP :execrows
DELETE FROM icps WHERE workspace_id = $1 AND id = $2;

-- name: CountICPs :one
SELECT count(*) FROM icps WHERE workspace_id = $1;

-- name: ListKeywords :many
SELECT * FROM keywords WHERE workspace_id = $1 ORDER BY kind, term;

-- name: CreateKeyword :one
INSERT INTO keywords (workspace_id, term, kind, lane) VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: UpdateKeyword :one
UPDATE keywords SET term = $3, kind = $4, lane = $5
WHERE workspace_id = $1 AND id = $2
RETURNING *;

-- name: DeleteKeyword :execrows
DELETE FROM keywords WHERE workspace_id = $1 AND id = $2;

-- name: InsertKeywordIfMissing :exec
INSERT INTO keywords (workspace_id, term, kind, lane) VALUES ($1, $2, $3, $4)
ON CONFLICT (workspace_id, term, kind) DO NOTHING;

-- name: ListCompetitors :many
SELECT * FROM competitors WHERE workspace_id = $1 ORDER BY name;

-- name: GetCompetitorByName :one
SELECT * FROM competitors WHERE workspace_id = $1 AND name = $2 LIMIT 1;

-- name: CreateCompetitor :one
INSERT INTO competitors (workspace_id, name, domains, notes) VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: UpdateCompetitor :one
UPDATE competitors SET name = $3, domains = $4, notes = $5
WHERE workspace_id = $1 AND id = $2
RETURNING *;

-- name: DeleteCompetitor :execrows
DELETE FROM competitors WHERE workspace_id = $1 AND id = $2;

-- Brand pack import (replace mode) clears these before inserting the pack's.

-- name: DeleteAllKeywords :exec
DELETE FROM keywords WHERE workspace_id = $1;

-- name: DeleteAllCompetitors :exec
DELETE FROM competitors WHERE workspace_id = $1;

-- name: DeleteAllICPs :exec
DELETE FROM icps WHERE workspace_id = $1;

-- name: DeleteAllPortfolioItems :exec
DELETE FROM portfolio_items WHERE workspace_id = $1;

-- name: DeleteOrphanBrandEmbeddings :exec
DELETE FROM embeddings e
WHERE e.workspace_id = $1 AND (
  (e.entity_type = 'icp' AND NOT EXISTS (SELECT 1 FROM icps i WHERE i.id = e.entity_id AND i.workspace_id = e.workspace_id))
  OR (e.entity_type = 'portfolio_item' AND NOT EXISTS (SELECT 1 FROM portfolio_items p WHERE p.id = e.entity_id AND p.workspace_id = e.workspace_id)));
