-- Native PostgreSQL dashboard appearance query leaves.
-- name: Get :one
SELECT COALESCE(icon, '') AS icon, COALESCE(color, '') AS color, revision, updated_by, updated_at
FROM dashboard.appearance_override
WHERE project_id = sqlc.arg(project_id) AND dashboard_id = sqlc.arg(dashboard_id);

-- name: ListProject :many
SELECT dashboard_id, COALESCE(icon, '') AS icon, COALESCE(color, '') AS color, revision, updated_by, updated_at
FROM dashboard.appearance_override
WHERE project_id = sqlc.arg(project_id)
ORDER BY dashboard_id;

-- name: Upsert :exec
INSERT INTO dashboard.appearance_override(project_id, dashboard_id, icon, color, updated_by)
VALUES (sqlc.arg(project_id), sqlc.arg(dashboard_id), NULLIF(sqlc.arg(icon), ''), NULLIF(sqlc.arg(color), ''), sqlc.arg(updated_by))
ON CONFLICT(project_id, dashboard_id) DO UPDATE SET
  icon = CASE WHEN sqlc.arg(icon_present)::boolean THEN EXCLUDED.icon ELSE dashboard.appearance_override.icon END,
  color = CASE WHEN sqlc.arg(color_present)::boolean THEN EXCLUDED.color ELSE dashboard.appearance_override.color END,
  revision = dashboard.appearance_override.revision + 1,
  updated_by = EXCLUDED.updated_by,
  updated_at = clock_timestamp();

-- name: UpsertCAS :execrows
INSERT INTO dashboard.appearance_override(project_id, dashboard_id, icon, color, updated_by)
VALUES (sqlc.arg(project_id), sqlc.arg(dashboard_id), NULLIF(sqlc.arg(icon), ''), NULLIF(sqlc.arg(color), ''), sqlc.arg(updated_by))
ON CONFLICT(project_id, dashboard_id) DO UPDATE SET
  icon = CASE WHEN sqlc.arg(icon_present)::boolean THEN EXCLUDED.icon ELSE dashboard.appearance_override.icon END,
  color = CASE WHEN sqlc.arg(color_present)::boolean THEN EXCLUDED.color ELSE dashboard.appearance_override.color END,
  revision = dashboard.appearance_override.revision + 1,
  updated_by = EXCLUDED.updated_by,
  updated_at = clock_timestamp()
WHERE dashboard.appearance_override.revision = sqlc.arg(expected_revision);

-- name: Ping :one
SELECT 1 AS ping;
