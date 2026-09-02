-- Static PostgreSQL query leaves for the physical-pool repository.

-- name: InsertPhysicalPool :execrows
INSERT INTO physical_pool.physical_pools (
    id, identity_digest, storage_location, storage_namespace,
    storage_implementation, object_naming_contract, region, tenant, encryption_domain,
    isolation_boundary, encryption_key_ref, credential_reference,
    retention_authority, orphan_grace_period_seconds,
    reader_grace_period_seconds, build_grace_period_seconds, retention_policy
) VALUES (
    sqlc.arg(id), sqlc.arg(id), sqlc.arg(storage_location), sqlc.arg(storage_namespace),
    sqlc.arg(storage_implementation), sqlc.arg(object_naming_contract), sqlc.arg(region),
    sqlc.arg(tenant), sqlc.arg(encryption_domain), sqlc.arg(isolation_boundary), sqlc.arg(encryption_key_ref),
    sqlc.arg(credential_reference), sqlc.arg(retention_authority),
    sqlc.arg(orphan_grace_period_seconds), sqlc.arg(reader_grace_period_seconds),
    sqlc.arg(build_grace_period_seconds), sqlc.arg(retention_policy)::jsonb
)
ON CONFLICT DO NOTHING;

-- name: GetPhysicalPool :one
SELECT id, identity_digest, storage_location, storage_namespace,
       storage_implementation, object_naming_contract, region, tenant, encryption_domain,
       isolation_boundary, encryption_key_ref, credential_reference,
       retention_authority, orphan_grace_period_seconds,
       reader_grace_period_seconds, build_grace_period_seconds,
       retention_policy
FROM physical_pool.physical_pools
WHERE id = sqlc.arg(id);

-- name: GetPhysicalPoolByNamespace :one
SELECT id
FROM physical_pool.physical_pools
WHERE storage_implementation = sqlc.arg(storage_implementation)
  AND storage_location = sqlc.arg(storage_location)
  AND storage_namespace = sqlc.arg(storage_namespace);

-- name: GetAdmissionByEvidence :one
SELECT compatibility_json, duckdb_runtime, ducklake_extension, catalog_format,
       storage_implementation, object_naming_contract, evidence_json,
       evidence_digest, compatibility_digest, conformance_version
FROM physical_pool.physical_pool_admissions
WHERE pool_id = sqlc.arg(pool_id)
  AND evidence_digest = sqlc.arg(evidence_digest)
LIMIT 1;

-- name: GetAdmissionByCompatibility :one
SELECT compatibility_json, duckdb_runtime, ducklake_extension, catalog_format,
       storage_implementation, object_naming_contract, evidence_json,
       evidence_digest, compatibility_digest, conformance_version
FROM physical_pool.physical_pool_admissions
WHERE pool_id = sqlc.arg(pool_id)
  AND compatibility_digest = sqlc.arg(compatibility_digest)
ORDER BY admitted_at DESC, evidence_digest DESC
LIMIT 1;

-- name: InsertAdmission :execrows
INSERT INTO physical_pool.physical_pool_admissions (
    pool_id, compatibility_json, duckdb_runtime, ducklake_extension,
    catalog_format, storage_implementation, object_naming_contract,
    evidence_json, evidence_digest, compatibility_digest, conformance_version
) VALUES (
    sqlc.arg(pool_id), sqlc.arg(compatibility_json)::jsonb,
    sqlc.arg(duckdb_runtime), sqlc.arg(ducklake_extension), sqlc.arg(catalog_format),
    sqlc.arg(storage_implementation), sqlc.arg(object_naming_contract),
    sqlc.arg(evidence_json)::jsonb, sqlc.arg(evidence_digest),
    sqlc.arg(compatibility_digest), sqlc.arg(conformance_version)
)
ON CONFLICT DO NOTHING;

-- name: GetCompatibilityJSONByDigest :one
SELECT compatibility_json
FROM physical_pool.physical_pool_admissions
WHERE pool_id = sqlc.arg(pool_id)
  AND compatibility_digest = sqlc.arg(compatibility_digest)
ORDER BY admitted_at DESC, evidence_digest DESC
LIMIT 1;

-- name: InsertOwnershipClaim :execrows
INSERT INTO physical_pool.namespace_ownership_claims (
    pool_id, compatibility_digest, evidence_digest, owner_id
) VALUES (sqlc.arg(pool_id), sqlc.arg(compatibility_digest),
          sqlc.arg(evidence_digest), sqlc.arg(owner_id))
ON CONFLICT (pool_id, evidence_digest) DO NOTHING;

-- name: GetOwnershipClaim :one
SELECT owner_id, compatibility_digest
FROM physical_pool.namespace_ownership_claims
WHERE pool_id = sqlc.arg(pool_id)
  AND evidence_digest = sqlc.arg(evidence_digest);

-- name: AcquireDeletionLease :one
INSERT INTO physical_pool.namespace_deletion_leases (
    singleton, owner_id, token, expires_at
) VALUES (
    true, sqlc.arg(owner_id), sqlc.arg(token)::uuid,
    clock_timestamp() + (sqlc.arg(ttl_seconds)::double precision * interval '1 second')
)
ON CONFLICT (singleton) DO UPDATE
SET owner_id = EXCLUDED.owner_id,
    token = EXCLUDED.token,
    expires_at = EXCLUDED.expires_at,
    acquired_at = clock_timestamp()
WHERE physical_pool.namespace_deletion_leases.expires_at <= clock_timestamp()
RETURNING token::text;

-- name: VerifyDeletionLease :one
SELECT owner_id = sqlc.arg(owner_id)
   AND token = sqlc.arg(token)::uuid
   AND expires_at > clock_timestamp() AS valid
FROM physical_pool.namespace_deletion_leases
WHERE singleton = true;

-- name: GetDeletionLeaseForUpdate :one
SELECT owner_id, token::text,
       expires_at > clock_timestamp() AS active
FROM physical_pool.namespace_deletion_leases
WHERE singleton = true
FOR UPDATE;

-- name: DeleteDeletionLease :one
DELETE FROM physical_pool.namespace_deletion_leases
WHERE singleton = true
  AND owner_id = sqlc.arg(owner_id)
  AND token = sqlc.arg(token)::uuid
  AND expires_at > clock_timestamp()
RETURNING true AS deleted;
