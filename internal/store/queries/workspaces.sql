-- Workspaces are the tenant root, so these queries filter by the workspace
-- id itself (or by membership) rather than a workspace_id column.

-- name: CreateWorkspace :one
INSERT INTO workspaces (name) VALUES ($1)
RETURNING *;

-- name: GetWorkspace :one
SELECT * FROM workspaces WHERE id = $1;

-- name: ListWorkspacesForUser :many
SELECT w.id, w.name, w.created_at, m.role
FROM workspaces w
JOIN memberships m ON m.workspace_id = w.id
WHERE m.user_id = $1
ORDER BY w.created_at;

-- name: GetMembership :one
SELECT * FROM memberships
WHERE workspace_id = $1 AND user_id = $2;

-- name: GetWorkspaceByName :one
SELECT * FROM workspaces WHERE name = $1 ORDER BY created_at LIMIT 1;

-- name: RenameWorkspace :one
UPDATE workspaces SET name = $2 WHERE id = $1
RETURNING *;
