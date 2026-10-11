-- name: InsertAudit :exec
INSERT INTO audit_log (workspace_id, user_id, action, target_type, target_id, meta)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListAudit :many
SELECT * FROM audit_log WHERE workspace_id = $1 ORDER BY created_at DESC LIMIT $2;
