-- name: UpsertWorkspace :exec
INSERT INTO workspaces (id, title, description, updated_at)
VALUES (?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(id) DO UPDATE SET
  title = excluded.title,
  description = excluded.description,
  updated_at = CURRENT_TIMESTAMP;
-- name: GetWorkspace :one
SELECT * FROM workspaces WHERE id = ?;

-- name: ListWorkspaces :many
SELECT * FROM workspaces ORDER BY created_at;

-- name: ListWorkspacesWithActiveMetadata :many
SELECT
  w.id,
  CASE WHEN a.title IS NOT NULL AND a.title <> '' THEN a.title ELSE w.title END AS title,
  CASE WHEN a.description IS NOT NULL THEN a.description ELSE w.description END AS description,
  COALESCE(active.serving_state_id, '') AS active_serving_state_id,
  w.created_at,
  w.updated_at
FROM workspaces w
LEFT JOIN workspace_active_serving_states active
  ON active.workspace_id = w.id AND active.environment = ?
LEFT JOIN assets a
  ON a.serving_state_id = active.serving_state_id
 AND a.asset_type = 'catalog'
 AND a.logical_asset_id = 'catalog:' || w.id
ORDER BY w.created_at;

-- name: GetWorkspaceWithActiveMetadata :one
SELECT
  w.id,
  CASE WHEN a.title IS NOT NULL AND a.title <> '' THEN a.title ELSE w.title END AS title,
  CASE WHEN a.description IS NOT NULL THEN a.description ELSE w.description END AS description,
  COALESCE(active.serving_state_id, '') AS active_serving_state_id,
  w.created_at,
  w.updated_at
FROM workspaces w
LEFT JOIN workspace_active_serving_states active
  ON active.workspace_id = w.id AND active.environment = ?
LEFT JOIN assets a
  ON a.serving_state_id = active.serving_state_id
 AND a.asset_type = 'catalog'
 AND a.logical_asset_id = 'catalog:' || w.id
WHERE w.id = ?;

-- name: GetWorkspaceAdministration :one
SELECT
  w.id,
  CAST(CASE WHEN catalog.title IS NOT NULL AND catalog.title <> '' THEN catalog.title ELSE w.title END AS TEXT) AS title,
  CAST(CASE WHEN catalog.description IS NOT NULL THEN catalog.description ELSE w.description END AS TEXT) AS description,
  COALESCE(active.serving_state_id, '') AS active_serving_state_id,
  w.created_at,
  w.updated_at,
  COALESCE(serving.status, '') AS active_serving_state_status,
  COALESCE(serving.activated_at, '') AS active_serving_state_since,
  COALESCE(serving.project_id, '') AS project_id,
  COALESCE(deployment.id, '') AS current_deployment_id,
  COALESCE(deployment.status, '') AS current_deployment_status,
  COALESCE(deployment.activated_at, '') AS current_deployment_since,
  COALESCE(link.release_id, '') AS current_release_id
FROM workspaces w
LEFT JOIN workspace_active_serving_states active
  ON active.workspace_id = w.id AND active.environment = sqlc.arg(environment)
LEFT JOIN serving_states serving ON serving.id = active.serving_state_id
LEFT JOIN assets catalog
  ON catalog.serving_state_id = active.serving_state_id
 AND catalog.asset_type = 'catalog'
 AND catalog.logical_asset_id = 'catalog:' || w.id
LEFT JOIN project_deployment_targets target
  ON target.serving_state_id = active.serving_state_id
 AND target.workspace_id = w.id
 AND target.status = 'active'
LEFT JOIN project_deployments deployment
  ON deployment.id = target.deployment_id
 AND deployment.environment = sqlc.arg(environment)
 AND deployment.status = 'active'
LEFT JOIN api_deployment_releases link ON link.deployment_id = deployment.id
WHERE w.id = sqlc.arg(id)
ORDER BY deployment.activated_at DESC, deployment.id DESC
LIMIT 1;

-- Workspace-owned catalog projection over the active serving generation.

-- name: GetActiveServingState :one
SELECT d.*
FROM serving_states d
JOIN workspace_active_serving_states active ON active.serving_state_id = d.id
WHERE active.workspace_id = ? AND active.environment = ?;

-- name: ListAssetsByServingState :many
SELECT * FROM assets WHERE serving_state_id = ? ORDER BY asset_type, asset_key;

-- name: ListAssetEdgesByServingState :many
SELECT * FROM asset_edges WHERE serving_state_id = ? ORDER BY edge_type, from_logical_asset_id, to_logical_asset_id;

-- name: ListAssetVersions :many
SELECT
  d.id AS serving_state_id,
  d.workspace_id,
  d.environment,
  d.status,
  d.digest,
  d.created_by,
  d.created_at,
  d.activated_at,
  a.snapshot_id,
  a.logical_asset_id,
  a.source_file,
  a.content_hash
FROM serving_states d
JOIN assets a ON a.serving_state_id = d.id
WHERE d.workspace_id = ?
  AND d.environment = ?
  AND a.logical_asset_id = ?
  AND d.source = 'publish'
  AND d.status IN ('active', 'draining', 'inactive', 'validated')
  AND NOT EXISTS (
    SELECT 1
    FROM serving_states newer
    JOIN assets newer_asset ON newer_asset.serving_state_id = newer.id
    WHERE newer.workspace_id = d.workspace_id
      AND newer.environment = d.environment
      AND newer.source = 'publish'
      AND newer.status IN ('active', 'draining', 'inactive', 'validated')
      AND newer_asset.logical_asset_id = a.logical_asset_id
      AND newer_asset.content_hash = a.content_hash
      AND (
        COALESCE(newer.activated_at, newer.created_at) > COALESCE(d.activated_at, d.created_at)
        OR (
          COALESCE(newer.activated_at, newer.created_at) = COALESCE(d.activated_at, d.created_at)
          AND newer.created_at > d.created_at
        )
        OR (
          COALESCE(newer.activated_at, newer.created_at) = COALESCE(d.activated_at, d.created_at)
          AND newer.created_at = d.created_at
          AND newer.id > d.id
        )
      )
  )
ORDER BY
  COALESCE(d.activated_at, d.created_at) DESC,
  d.created_at DESC,
  d.id DESC;
