-- Seed helpers. Config seeds the DB once; the DB (Settings UI) wins afterwards.

-- name: InsertProviderIfMissing :exec
INSERT INTO model_providers (id, kind, base_url, is_local, name, api_key_env, options)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (id) DO NOTHING;

-- name: InsertTaskRouteIfMissing :exec
INSERT INTO task_routes (workspace_id, task, provider_id, model, fallback_provider_id, fallback_model)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (workspace_id, task) DO NOTHING;

-- name: InsertLaneConfigIfMissing :exec
INSERT INTO lane_configs (workspace_id, lane, weights, daily_quota, freshness_hours, min_score, draft_kind)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (workspace_id, lane) DO NOTHING;

-- name: ListLaneConfigs :many
SELECT * FROM lane_configs WHERE workspace_id = $1 ORDER BY lane;

-- name: ListTaskRoutes :many
SELECT * FROM task_routes WHERE workspace_id = $1 ORDER BY task;

-- name: SetProviderModelPriceIfEmpty :exec
UPDATE provider_models SET price = $3 WHERE provider_id = $1 AND model = $2 AND price = '{}'::jsonb;
