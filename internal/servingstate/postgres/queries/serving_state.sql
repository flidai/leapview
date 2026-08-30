-- Named PostgreSQL queries for immutable serving-generation evidence.
-- Delivery remains the lifecycle/active-selection authority; these statements
-- only insert/read bundle projections and maintain reader leases.

-- name: GetBundle :one
SELECT b.generation_id::text, b.project_id, b.environment, b.artifact_id,
       b.artifact_digest, b.compiled_graph_digest, b.artifact_format,
       b.artifact_locator, b.storage_security_domain, b.artifact_content_type,
       b.artifact_metadata_digest, b.manifest_json::text, b.project_digest,
       b.access_policy_json::text, b.dashboard_publications_json::text,
       b.dashboard_appearances_json::text, b.size_bytes, b.created_by,
       b.created_at, s.ducklake_snapshot_id
FROM serving_state.bundle b
JOIN delivery.delivery_generation g ON g.generation_id = b.generation_id
JOIN delivery.delivery_snapshot_seal s ON s.seal_id = g.snapshot_seal_id
WHERE b.generation_id = $1::uuid;

-- name: GetActiveBundle :one
SELECT b.generation_id::text, b.project_id, b.environment, b.artifact_id,
       b.artifact_digest, b.compiled_graph_digest, b.artifact_format,
       b.artifact_locator, b.storage_security_domain, b.artifact_content_type,
       b.artifact_metadata_digest, b.manifest_json::text, b.project_digest,
       b.access_policy_json::text, b.dashboard_publications_json::text,
       b.dashboard_appearances_json::text, b.size_bytes, b.created_by,
       b.created_at, s.ducklake_snapshot_id
FROM delivery.delivery_active_pointer ap
JOIN delivery.delivery_target t ON t.target_id = ap.target_id
JOIN delivery.delivery_generation g ON g.generation_id = ap.generation_id
JOIN delivery.delivery_snapshot_seal s ON s.seal_id = g.snapshot_seal_id
JOIN serving_state.bundle b ON b.generation_id = g.generation_id
WHERE t.project_id = $1 AND t.environment = $2;

-- name: InsertBundle :execrows
INSERT INTO serving_state.bundle (
    generation_id, project_id, environment, artifact_id, artifact_digest,
    compiled_graph_digest, artifact_format, artifact_locator,
    storage_security_domain, artifact_content_type, artifact_metadata_digest,
    manifest_json,
    project_digest, access_policy_json, dashboard_publications_json,
    dashboard_appearances_json, size_bytes, created_by
) VALUES (sqlc.arg(generation_id)::uuid, sqlc.arg(project_id),
          sqlc.arg(environment), sqlc.arg(artifact_id),
          sqlc.arg(artifact_digest), sqlc.arg(compiled_graph_digest),
          sqlc.arg(artifact_format), sqlc.arg(artifact_locator),
          sqlc.arg(storage_security_domain), sqlc.arg(artifact_content_type),
          sqlc.arg(artifact_metadata_digest), sqlc.arg(manifest_json)::jsonb,
          sqlc.arg(project_digest), sqlc.arg(access_policy_json)::jsonb,
          sqlc.arg(dashboard_publications_json)::jsonb,
          sqlc.arg(dashboard_appearances_json)::jsonb,
          sqlc.arg(size_bytes), sqlc.arg(created_by))
ON CONFLICT (generation_id) DO NOTHING;

-- name: InsertAsset :execrows
INSERT INTO serving_state.asset (
    generation_id, snapshot_id, logical_asset_id, asset_type, asset_key,
    title, description, source_file, payload_schema, payload_json, content_hash
) VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11)
ON CONFLICT (generation_id, logical_asset_id) DO NOTHING;

-- name: InsertAssetEdge :execrows
INSERT INTO serving_state.asset_edge (
    generation_id, id, from_logical_asset_id, to_logical_asset_id, edge_type
) VALUES ($1::uuid, $2, $3, $4, $5)
ON CONFLICT (generation_id, id) DO NOTHING;

-- name: CreateReaderLease :execrows
INSERT INTO serving_state.reader_lease (
    lease_id, generation_id, ducklake_snapshot_id, owner_id, expires_at
) SELECT $1, $2::uuid, $3, $4,
         COALESCE($5::timestamptz, clock_timestamp() + interval '5 minutes')
WHERE EXISTS (
    SELECT 1 FROM delivery.delivery_generation g
    JOIN delivery.delivery_snapshot_seal s ON s.seal_id = g.snapshot_seal_id
    JOIN delivery.delivery_retention_root r
      ON r.generation_id = g.generation_id AND r.snapshot_seal_id = s.seal_id
    WHERE g.generation_id = $2::uuid AND s.ducklake_snapshot_id = $3
      AND r.root_kind = 'generation' AND r.state = 'live'
      AND (r.expires_at IS NULL OR r.expires_at > clock_timestamp())
)
  AND COALESCE($5::timestamptz, clock_timestamp() + interval '5 minutes') > clock_timestamp()
  AND COALESCE($5::timestamptz, clock_timestamp() + interval '5 minutes') <= clock_timestamp() + interval '24 hours';

-- name: ReleaseReaderLease :execrows
UPDATE serving_state.reader_lease
SET released_at = clock_timestamp()
WHERE lease_id = $1 AND released_at IS NULL;

-- name: ExtendReaderLease :execrows
WITH live_retention AS (
    SELECT 1
    FROM serving_state.reader_lease l
    JOIN delivery.delivery_generation g ON g.generation_id = l.generation_id
    JOIN delivery.delivery_snapshot_seal s ON s.seal_id = g.snapshot_seal_id
    JOIN delivery.delivery_retention_root r
      ON r.generation_id = g.generation_id AND r.snapshot_seal_id = s.seal_id
    WHERE l.lease_id = $1 AND l.released_at IS NULL
      AND l.expires_at > clock_timestamp()
      AND s.ducklake_snapshot_id = l.ducklake_snapshot_id
      AND r.root_kind = 'generation' AND r.state = 'live'
      AND (r.expires_at IS NULL OR r.expires_at > clock_timestamp())
)
UPDATE serving_state.reader_lease l
SET expires_at = $2
FROM delivery.delivery_generation g
JOIN delivery.delivery_snapshot_seal s ON s.seal_id = g.snapshot_seal_id
JOIN delivery.delivery_retention_root r
  ON r.generation_id = g.generation_id AND r.snapshot_seal_id = s.seal_id
WHERE l.lease_id = $1 AND l.generation_id = g.generation_id
  AND l.ducklake_snapshot_id = s.ducklake_snapshot_id
  AND r.root_kind = 'generation' AND r.state = 'live'
  AND (r.expires_at IS NULL OR r.expires_at > clock_timestamp())
  AND EXISTS (SELECT 1 FROM live_retention)
  AND l.released_at IS NULL
  AND l.expires_at > clock_timestamp()
  AND $2 > l.expires_at
  AND $2 <= clock_timestamp() + interval '24 hours';

-- name: ActiveFlag :one
SELECT EXISTS (
    SELECT 1 FROM delivery.delivery_active_pointer p
    JOIN delivery.delivery_generation g ON g.generation_id = p.generation_id
    WHERE p.generation_id = $1::uuid
);

-- name: ListActiveScopes :many
SELECT t.project_id, t.environment
FROM delivery.delivery_active_pointer ap
JOIN delivery.delivery_target t ON t.target_id = ap.target_id
ORDER BY t.project_id, t.environment;

-- name: GuardReaderSnapshotRetention :one
SELECT serving_state.guard_reader_snapshot_retention($1::uuid, $2);

-- name: ReaderLease :one
SELECT generation_id::text, ducklake_snapshot_id
FROM serving_state.reader_lease
WHERE lease_id = $1 AND released_at IS NULL AND expires_at > clock_timestamp();

-- name: ReleaseExpiredLeases :one
SELECT serving_state.release_expired_query_snapshot_leases(
    sqlc.arg(environment), sqlc.arg(batch_limit)
);

-- name: LeasedSnapshots :many
SELECT DISTINCT l.ducklake_snapshot_id
FROM serving_state.reader_lease l
JOIN delivery.delivery_generation g ON g.generation_id = l.generation_id
JOIN delivery.delivery_target t ON t.target_id = g.target_id
WHERE t.environment = $1 AND l.released_at IS NULL
  AND l.expires_at > clock_timestamp()
ORDER BY l.ducklake_snapshot_id;

-- name: ReferencedSnapshots :many
SELECT DISTINCT s.ducklake_snapshot_id
FROM delivery.delivery_generation g
JOIN delivery.delivery_snapshot_seal s ON s.seal_id = g.snapshot_seal_id
JOIN delivery.delivery_active_pointer ap ON ap.generation_id = g.generation_id
JOIN delivery.delivery_target t ON t.target_id = g.target_id
WHERE t.environment = $1 AND s.ducklake_snapshot_id > 0
ORDER BY s.ducklake_snapshot_id;

-- name: ForeignSnapshots :many
SELECT DISTINCT s.ducklake_snapshot_id
FROM delivery.delivery_generation g
JOIN delivery.delivery_snapshot_seal s ON s.seal_id = g.snapshot_seal_id
JOIN delivery.delivery_target t ON t.target_id = g.target_id
WHERE t.environment <> $1 AND s.ducklake_snapshot_id > 0
ORDER BY s.ducklake_snapshot_id;

-- name: ListAssets :many
SELECT snapshot_id, logical_asset_id, asset_type, asset_key,
       parent_logical_asset_id, title, description, source_file,
       payload_schema, payload_json::text, content_hash
FROM serving_state.asset
WHERE generation_id = $1::uuid
ORDER BY asset_type, asset_key;

-- name: ListAssetEdges :many
SELECT id, from_logical_asset_id, to_logical_asset_id, edge_type
FROM serving_state.asset_edge
WHERE generation_id = $1::uuid
ORDER BY edge_type, from_logical_asset_id, to_logical_asset_id;

-- name: AssetVersions :many
SELECT g.generation_id::text, t.project_id, t.environment,
       CASE WHEN ap.generation_id IS NULL THEN 'validated' ELSE 'active' END,
       g.serving_artifact_digest, p.actor_id, g.created_at,
       a.snapshot_id, a.source_file, a.payload_json::text, a.content_hash,
       COALESCE(p.committed_at, g.created_at)
FROM serving_state.asset a
JOIN delivery.delivery_generation g ON g.generation_id = a.generation_id
JOIN delivery.delivery_target t ON t.target_id = g.target_id
JOIN delivery.delivery_publication p
  ON p.generation_id = g.generation_id AND p.state = 'committed'
LEFT JOIN delivery.delivery_active_pointer ap ON ap.generation_id = g.generation_id
WHERE t.project_id = $1 AND t.environment = $2
  AND a.logical_asset_id = $3
ORDER BY g.created_at DESC, g.generation_id DESC;

-- name: GenerationEvidence :one
SELECT t.project_id, t.environment,
       g.serving_artifact_digest, g.compiled_graph_digest
FROM delivery.delivery_generation g
JOIN delivery.delivery_target t ON t.target_id = g.target_id
WHERE g.generation_id = $1::uuid;
