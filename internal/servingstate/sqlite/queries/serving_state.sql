-- Serving-generation lifecycle, artifacts, leases, and activation state.

-- name: CreateServingState :exec
INSERT INTO serving_states (id, project_id, environment, status, source, created_by)
VALUES (?, ?, ?, ?, ?, ?);

-- name: GetServingState :one
SELECT * FROM serving_states WHERE id = ?;

-- name: GetActiveServingState :one
SELECT d.*
FROM serving_states d
JOIN project_active_serving_states active ON active.generation_id = d.id
WHERE active.project_id = ? AND active.environment = ?;

-- name: ListActiveServingStateScopes :many
SELECT project_id, environment
FROM project_active_serving_states
ORDER BY project_id, environment;

-- name: ListReferencedDuckLakeSnapshots :many
SELECT DISTINCT ducklake_snapshot_id
FROM serving_states
WHERE ducklake_snapshot_id > 0
  AND environment = sqlc.arg(environment)
  AND status = 'active'
ORDER BY ducklake_snapshot_id;

-- name: ListActiveDuckLakeSnapshots :many
SELECT DISTINCT ducklake_snapshot_id
FROM serving_states
WHERE ducklake_snapshot_id > 0
  AND environment = sqlc.arg(environment)
  AND status = 'active'
ORDER BY ducklake_snapshot_id;

-- name: ListLeasedDuckLakeSnapshots :many
SELECT DISTINCT ducklake_snapshot_id
FROM query_snapshot_leases
WHERE ducklake_snapshot_id > 0
  AND EXISTS (
    SELECT 1 FROM serving_states state
    WHERE state.id = query_snapshot_leases.serving_state_id
      AND state.environment = sqlc.arg(environment)
  )
  AND released_at IS NULL
  AND expires_at > CURRENT_TIMESTAMP
ORDER BY ducklake_snapshot_id;

-- name: ListForeignEnvironmentDuckLakeSnapshots :many
SELECT DISTINCT ducklake_snapshot_id
FROM serving_states
WHERE ducklake_snapshot_id > 0
  AND environment <> sqlc.arg(environment)
  AND status <> 'deleted'
ORDER BY ducklake_snapshot_id;

-- name: ExpireInactiveServingStates :exec
UPDATE serving_states
SET status = 'expired', error = ''
WHERE status = 'inactive'
  AND environment = sqlc.arg(environment);

-- name: MarkOtherServingStatesDraining :exec
UPDATE serving_states
SET status = 'draining',
    superseded_at = CURRENT_TIMESTAMP,
    error = ''
WHERE project_id = ?
  AND environment = ?
  AND id <> ?
  AND status = 'active';

-- name: MarkDrainingServingStatesDeleteScheduled :exec
UPDATE serving_states
SET status = 'delete_scheduled', error = ''
WHERE status = 'draining'
  AND environment = sqlc.arg(environment);

-- name: ScheduleExpiredServingStateDeletion :exec
UPDATE serving_states
SET status = 'delete_scheduled', error = ''
WHERE status = 'expired'
  AND environment = sqlc.arg(environment);

-- name: MarkDeleteScheduledServingStatesDeleted :exec
UPDATE serving_states
SET status = 'deleted', error = ''
WHERE status = 'delete_scheduled'
  AND environment = sqlc.arg(environment);

-- name: UpdateServingStateValidated :exec
UPDATE serving_states
SET status = ?, project_digest = ?, access_policy_json = ?, dashboard_publications_json = ?, dashboard_appearances_json = ?, digest = ?, manifest_json = ?, error = ''
WHERE id = ?;

-- name: UpdateServingStateDuckLakeSnapshot :exec
UPDATE serving_states
SET ducklake_snapshot_id = ?
WHERE id = ?;

-- name: UpdateServingStateStatus :exec
UPDATE serving_states
SET status = ?, error = ?
WHERE id = ?;

-- name: MarkServingStateActive :exec
UPDATE serving_states
SET status = 'active', activated_at = CURRENT_TIMESTAMP, error = ''
WHERE id = ?;

-- name: InsertActiveServingState :execrows
INSERT INTO project_active_serving_states (project_id, environment, generation_id, updated_at)
VALUES (?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(project_id, environment) DO NOTHING;

-- name: CompareAndSwapActiveServingState :execrows
UPDATE project_active_serving_states
SET generation_id = sqlc.arg(generation_id), updated_at = CURRENT_TIMESTAMP
WHERE project_id = sqlc.arg(project_id) AND environment = sqlc.arg(environment)
  AND generation_id = sqlc.arg(expected_generation_id);

-- name: MarkOtherServingStatesInactive :exec
UPDATE serving_states
SET status = 'inactive'
WHERE project_id = ? AND environment = ? AND id <> ? AND status = 'active';

-- name: InsertServingStateArtifact :exec
INSERT INTO serving_state_artifacts (id, serving_state_id, digest, format, path, manifest_json, size_bytes)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(serving_state_id) DO UPDATE SET
  digest = excluded.digest,
  format = excluded.format,
  path = excluded.path,
  manifest_json = excluded.manifest_json,
  size_bytes = excluded.size_bytes;

-- name: GetArtifactByServingState :one
SELECT * FROM serving_state_artifacts WHERE serving_state_id = ?;

-- name: CreateQuerySnapshotLease :exec
INSERT INTO query_snapshot_leases (id, serving_state_id, ducklake_snapshot_id, owner_id, expires_at)
VALUES (?, ?, ?, ?, ?);

-- name: ReleaseQuerySnapshotLease :exec
UPDATE query_snapshot_leases
SET released_at = COALESCE(released_at, CURRENT_TIMESTAMP)
WHERE id = ?;

-- name: ExtendQuerySnapshotLease :execrows
UPDATE query_snapshot_leases
SET expires_at = ?
WHERE id = ?
  AND released_at IS NULL;

-- name: ReleaseExpiredQuerySnapshotLeases :exec
UPDATE query_snapshot_leases
SET released_at = CURRENT_TIMESTAMP
WHERE released_at IS NULL
  AND EXISTS (
    SELECT 1 FROM serving_states state
    WHERE state.id = query_snapshot_leases.serving_state_id
      AND state.environment = sqlc.arg(environment)
  )
  AND expires_at <= CURRENT_TIMESTAMP;

-- name: ClearAssetsForServingState :exec
DELETE FROM assets WHERE serving_state_id = ?;

-- name: ClearAssetEdgesForServingState :exec
DELETE FROM asset_edges WHERE serving_state_id = ?;

-- name: InsertAsset :exec
INSERT INTO assets (snapshot_id, logical_asset_id, serving_state_id, asset_type, asset_key, parent_logical_asset_id, title, description, source_file, payload_schema, payload_json, content_hash)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: InsertAssetEdge :exec
INSERT INTO asset_edges (id, serving_state_id, from_logical_asset_id, to_logical_asset_id, edge_type)
VALUES (?, ?, ?, ?, ?);

-- name: ListAssetsByServingState :many
SELECT * FROM assets WHERE serving_state_id = ? ORDER BY asset_type, asset_key;

-- name: ListAssetEdgesByServingState :many
SELECT * FROM asset_edges WHERE serving_state_id = ? ORDER BY edge_type, from_logical_asset_id, to_logical_asset_id;

-- name: ListAssetVersions :many
SELECT
  d.id AS serving_state_id,
  d.project_id,
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
WHERE d.project_id = ?
  AND d.environment = ?
  AND a.logical_asset_id = ?
  AND (
    d.source = 'publish'
    OR EXISTS (
      SELECT 1
      FROM delivery_publications publication
      WHERE publication.project_id = d.project_id
        AND publication.environment = d.environment
        AND publication.generation_id = d.id
        AND publication.status = 'committed'
    )
  )
  AND d.status IN ('active', 'draining', 'inactive', 'validated')
  AND NOT EXISTS (
    SELECT 1
    FROM serving_states newer
    JOIN assets newer_asset ON newer_asset.serving_state_id = newer.id
    WHERE newer.project_id = d.project_id
      AND newer.environment = d.environment
      AND (
        newer.source = 'publish'
        OR EXISTS (
          SELECT 1
          FROM delivery_publications newer_publication
          WHERE newer_publication.project_id = newer.project_id
            AND newer_publication.environment = newer.environment
            AND newer_publication.generation_id = newer.id
            AND newer_publication.status = 'committed'
        )
      )
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
