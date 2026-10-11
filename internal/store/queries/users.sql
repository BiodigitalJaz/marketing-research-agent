-- name: CreateUser :one
INSERT INTO users (email, name, password_hash) VALUES ($1, $2, $3)
RETURNING *;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1;

-- name: AddMembership :exec
INSERT INTO memberships (workspace_id, user_id, role) VALUES ($1, $2, $3)
ON CONFLICT (workspace_id, user_id) DO UPDATE SET role = EXCLUDED.role;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: SetUserPassword :exec
UPDATE users SET password_hash = $2 WHERE id = $1;

-- name: SetUserName :exec
UPDATE users SET name = $2 WHERE id = $1;

-- name: CountOtherMemberships :one
-- Workspaces other than this one the user belongs to.
SELECT count(*) FROM memberships WHERE user_id = $1 AND workspace_id <> $2;

-- name: ListMembers :many
SELECT u.id, u.email, u.name, u.created_at, m.role
FROM memberships m
JOIN users u ON u.id = m.user_id
WHERE m.workspace_id = $1
ORDER BY u.created_at;

-- name: GetMember :one
SELECT u.id, u.email, u.name, u.created_at, m.role
FROM memberships m
JOIN users u ON u.id = m.user_id
WHERE m.workspace_id = $1 AND m.user_id = $2;

-- name: LockWorkspaceAdmins :many
-- Locks the workspace's admin memberships so "keep at least one admin"
-- checks cannot race.
SELECT user_id FROM memberships
WHERE workspace_id = $1 AND role = 'admin'
FOR UPDATE;

-- name: SetMemberRole :execrows
UPDATE memberships SET role = $3 WHERE workspace_id = $1 AND user_id = $2;

-- name: DeleteMembership :execrows
DELETE FROM memberships WHERE workspace_id = $1 AND user_id = $2;
