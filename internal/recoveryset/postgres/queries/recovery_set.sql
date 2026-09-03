-- Static sqlc leaves for the durable recovery-set frontier.

-- name: InsertRecoverySet :one
INSERT INTO recovery.recovery_set (
 set_id, schema_version, expected_cluster_points, expected_object_roots,
 target_id, generation_id, publication_id, target_revision, snapshot_seal_id,
 physical_pool_id, tenant_domain, region, encryption_domain, object_namespace,
 catalog_database, catalog_id, catalog_uuid, catalog_version,
 ducklake_snapshot_id, relation_namespace, relation_manifest_digest,
 closure_digest, object_root, object_root_digest, artifact_root,
 artifact_root_digest, serving_artifact_id, serving_artifact_digest,
 compiled_graph_digest, compiled_config_digest, security_domain_fingerprint,
 request_digest, plan_digest, compatibility_digest, duckdb_version,
 runtime_version, ducklake_extension_version, ducklake_spec_version,
 catalog_schema_version, duckdb_runtime, ducklake_extension, catalog_format,
 storage_implementation, object_naming_contract, fence_epoch, audit_identity,
 status, created_by, created_at, frontier_digest
) VALUES (
 sqlc.arg(set_id)::uuid, sqlc.arg(schema_version)::integer, 2,
 sqlc.arg(expected_object_roots)::integer, sqlc.arg(target_id),
 sqlc.arg(generation_id)::uuid, sqlc.arg(publication_id)::uuid,
 sqlc.arg(target_revision)::bigint, sqlc.arg(snapshot_seal_id)::uuid,
 sqlc.arg(physical_pool_id), sqlc.arg(tenant_domain), sqlc.arg(region),
 sqlc.arg(encryption_domain), sqlc.arg(object_namespace), sqlc.arg(catalog_database),
 sqlc.arg(catalog_id), sqlc.arg(catalog_uuid), sqlc.arg(catalog_version)::bigint,
 sqlc.arg(ducklake_snapshot_id)::bigint, sqlc.arg(relation_namespace),
 sqlc.arg(relation_manifest_digest), sqlc.arg(closure_digest), sqlc.arg(object_root),
 sqlc.arg(object_root_digest), sqlc.arg(artifact_root), sqlc.arg(artifact_root_digest),
 sqlc.arg(serving_artifact_id), sqlc.arg(serving_artifact_digest),
 sqlc.arg(compiled_graph_digest), sqlc.arg(compiled_config_digest),
 sqlc.arg(security_domain_fingerprint), sqlc.arg(request_digest), sqlc.arg(plan_digest),
 sqlc.arg(compatibility_digest), sqlc.arg(duckdb_version), sqlc.arg(runtime_version),
 sqlc.arg(ducklake_extension_version), sqlc.arg(ducklake_spec_version),
 sqlc.arg(catalog_schema_version), sqlc.arg(duckdb_runtime), sqlc.arg(ducklake_extension),
 sqlc.arg(catalog_format), sqlc.arg(storage_implementation), sqlc.arg(object_naming_contract),
 sqlc.arg(fence_epoch)::bigint, sqlc.arg(audit_identity), sqlc.arg(status),
 sqlc.arg(created_by), sqlc.arg(created_at)::timestamptz, sqlc.arg(frontier_digest)
)
ON CONFLICT (set_id) DO NOTHING
RETURNING set_id::text AS set_id;

-- name: InsertRecoveryClusterPoint :exec
INSERT INTO recovery.recovery_cluster_point (set_id, database_role, cluster_identity, database_identity, recovery_identity)
VALUES (sqlc.arg(set_id)::uuid, sqlc.arg(database_role), sqlc.arg(cluster_identity), sqlc.arg(database_identity), sqlc.arg(recovery_identity));

-- name: InsertRecoveryObjectRoot :exec
INSERT INTO recovery.recovery_object_root (set_id, root_kind, root_uri, version_id, digest, provider_recovery_frontier)
VALUES (sqlc.arg(set_id)::uuid, sqlc.arg(root_kind), sqlc.arg(root_uri), sqlc.arg(version_id), sqlc.arg(digest), sqlc.arg(provider_recovery_frontier));

-- name: GetRecoverySet :one
SELECT set_id::text AS set_id, schema_version, expected_cluster_points, expected_object_roots,
 target_id, generation_id::text AS generation_id, publication_id::text AS publication_id,
 target_revision, snapshot_seal_id::text AS snapshot_seal_id, physical_pool_id,
 tenant_domain, region, encryption_domain, object_namespace, catalog_database,
 catalog_id, catalog_uuid, catalog_version, ducklake_snapshot_id, relation_namespace,
 relation_manifest_digest, closure_digest, object_root, object_root_digest,
 artifact_root, artifact_root_digest, serving_artifact_id, serving_artifact_digest,
 compiled_graph_digest, compiled_config_digest, security_domain_fingerprint,
 request_digest, plan_digest, compatibility_digest, duckdb_version, runtime_version,
 ducklake_extension_version, ducklake_spec_version, catalog_schema_version,
 duckdb_runtime, ducklake_extension, catalog_format, storage_implementation,
 object_naming_contract, fence_epoch, audit_identity, status, created_by, created_at,
 frontier_digest, COALESCE(published_by, '') AS published_by,
 COALESCE(published_at, 'epoch'::timestamptz) AS published_at
FROM recovery.recovery_set WHERE set_id = sqlc.arg(set_id)::uuid;

-- name: ListRecoveryClusterPoints :many
SELECT database_role, cluster_identity, database_identity, recovery_identity
FROM recovery.recovery_cluster_point WHERE set_id = sqlc.arg(set_id)::uuid ORDER BY database_role;

-- name: ListRecoveryObjectRoots :many
SELECT root_kind, root_uri, version_id, digest, provider_recovery_frontier
FROM recovery.recovery_object_root WHERE set_id = sqlc.arg(set_id)::uuid ORDER BY root_kind, root_uri, version_id;

-- name: PublishRecoverySet :one
UPDATE recovery.recovery_set
SET status = 'published', published_by = sqlc.arg(published_by), published_at = COALESCE(published_at, clock_timestamp())
WHERE set_id = sqlc.arg(set_id)::uuid AND fence_epoch = sqlc.arg(fence_epoch)::bigint
  AND ((status = 'prepared') OR (status = 'published' AND published_by = sqlc.arg(published_by)))
RETURNING set_id::text AS set_id, status, published_by, published_at;

-- name: SupersedeRecoverySet :execrows
UPDATE recovery.recovery_set SET status = 'superseded'
WHERE set_id = sqlc.arg(set_id)::uuid AND status = 'published' AND fence_epoch = sqlc.arg(fence_epoch)::bigint;

-- name: InsertValidationAttempt :one
INSERT INTO recovery.validation_attempt (attempt_id, set_id, owner_id, fence_epoch, audit_identity, status, started_at)
VALUES (sqlc.arg(attempt_id)::uuid, sqlc.arg(set_id)::uuid, sqlc.arg(owner_id), sqlc.arg(fence_epoch)::bigint, sqlc.arg(audit_identity), 'running', sqlc.arg(started_at)::timestamptz)
ON CONFLICT (attempt_id) DO NOTHING RETURNING attempt_id::text AS attempt_id;

-- name: CompleteValidationAttempt :execrows
UPDATE recovery.validation_attempt SET status = sqlc.arg(status), result_digest = NULLIF(sqlc.arg(result_digest), ''), error = NULLIF(sqlc.arg(error), ''), completed_at = sqlc.arg(completed_at)::timestamptz
WHERE attempt_id = sqlc.arg(attempt_id)::uuid AND status = 'running' AND fence_epoch = sqlc.arg(fence_epoch)::bigint;

-- name: GetValidationAttempt :one
SELECT attempt_id::text AS attempt_id, set_id::text AS set_id, owner_id,
       fence_epoch, audit_identity, status, COALESCE(result_digest, '') AS result_digest,
       COALESCE(error, '') AS error, started_at,
       COALESCE(completed_at, 'epoch'::timestamptz) AS completed_at
FROM recovery.validation_attempt WHERE attempt_id = sqlc.arg(attempt_id)::uuid;

-- name: InsertValidationResult :one
INSERT INTO recovery.validation_result (attempt_id, result_digest, evidence, recorded_at)
SELECT validation.attempt_id, sqlc.arg(result_digest), sqlc.arg(evidence)::jsonb, sqlc.arg(recorded_at)::timestamptz
FROM recovery.validation_attempt AS validation
WHERE validation.attempt_id = sqlc.arg(attempt_id)::uuid AND validation.status = 'running'
ON CONFLICT (attempt_id) DO NOTHING
RETURNING attempt_id::text AS attempt_id;

-- name: GetValidationResult :one
SELECT attempt_id::text AS attempt_id, result_digest, evidence, recorded_at
FROM recovery.validation_result WHERE attempt_id = sqlc.arg(attempt_id)::uuid;
