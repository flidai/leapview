-- Static PostgreSQL query leaves for the cache repository.

-- name: EnsureNamespace :one
SELECT cache.ensure_namespace(sqlc.arg(namespace_key), sqlc.arg(partition_kind), sqlc.arg(target_id), sqlc.arg(project_id), sqlc.arg(environment), sqlc.narg(candidate_id)::text) AS epoch;


-- name: GetNamespaceEpoch :one
SELECT epoch FROM cache.cache_namespace_epoch WHERE namespace_key = sqlc.arg(namespace_key);

-- name: GetNamespaceEpochForUpdate :one
SELECT epoch FROM cache.cache_namespace_epoch WHERE namespace_key = sqlc.arg(namespace_key) FOR UPDATE;

-- name: GetManifest :one
SELECT manifest_id, partition_kind, target_id, project_id, environment, candidate_id,
       partition_format_version, dependency_digest, policy_fingerprint,
       canonical_query_digest, key_format_version, origin_snapshot_seal_id, storage_security_domain,
       object_digest, object_key, byte_size, metadata, state, created_at,
       expires_at, retired_at, expired_at, retire_evidence, expire_evidence
FROM cache.cache_manifest
WHERE partition_kind = sqlc.arg(partition_kind)
  AND target_id = sqlc.arg(target_id)
  AND project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment)
  AND candidate_id IS NOT DISTINCT FROM sqlc.narg(candidate_id)::text
  AND partition_format_version = sqlc.arg(partition_format_version)
  AND dependency_digest = sqlc.arg(dependency_digest)
  AND policy_fingerprint = sqlc.arg(policy_fingerprint)
  AND canonical_query_digest = sqlc.arg(canonical_query_digest)
  AND key_format_version = sqlc.arg(key_format_version)
  AND state = 'admitted'
  AND (expires_at IS NULL OR expires_at > clock_timestamp());

-- name: ListManifestsByDependency :many
SELECT manifest_id, partition_kind, target_id, project_id, environment, candidate_id,
       partition_format_version, dependency_digest, policy_fingerprint,
       canonical_query_digest, key_format_version, origin_snapshot_seal_id, storage_security_domain,
       object_digest, object_key, byte_size, metadata, state, created_at,
       expires_at, retired_at, expired_at, retire_evidence, expire_evidence
FROM cache.cache_manifest
WHERE partition_kind = sqlc.arg(partition_kind)
  AND target_id = sqlc.arg(target_id)
  AND project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment)
  AND candidate_id IS NOT DISTINCT FROM sqlc.narg(candidate_id)::text
  AND partition_format_version = sqlc.arg(partition_format_version)
  AND dependency_digest = sqlc.arg(dependency_digest)
  AND state = 'admitted'
  AND (expires_at IS NULL OR expires_at > clock_timestamp())
ORDER BY created_at, manifest_id
LIMIT sqlc.arg(limit_count);

-- name: ObjectReachable :one
SELECT EXISTS (
    SELECT 1 FROM cache.cache_manifest AS m
    WHERE m.partition_kind = sqlc.arg(partition_kind)
      AND m.target_id = sqlc.arg(target_id)
      AND m.project_id = sqlc.arg(project_id)
      AND m.environment = sqlc.arg(environment)
      AND m.candidate_id IS NOT DISTINCT FROM sqlc.narg(candidate_id)::text
      AND m.storage_security_domain = sqlc.arg(storage_security_domain)
      AND m.object_key = sqlc.arg(object_key)
      AND (m.state IN ('admitted','retiring') OR EXISTS (
          SELECT 1 FROM cache.cache_retention_root AS rr
          WHERE rr.manifest_id = m.manifest_id AND rr.state IN ('live','retiring')
      ))
) AS reachable;

-- name: InvalidateNamespace :one
SELECT f.invalidation_id::uuid AS invalidation_id, f.event_id::bigint AS event_id,
       f.namespace_epoch::bigint AS namespace_epoch, f.retired_manifests::bigint AS retired_manifests,
       f.created_at::timestamptz AS created_at
FROM cache.invalidate_namespace(sqlc.arg(invalidation_id)::uuid, sqlc.arg(namespace_key), sqlc.arg(dependency_kind), sqlc.arg(dependency_id), sqlc.narg(dependency_digest)::text, sqlc.arg(expected_epoch), sqlc.arg(idempotency_key), sqlc.arg(reason), sqlc.narg(evidence)::jsonb) AS f(invalidation_id, event_id, namespace_epoch, retired_manifests, created_at);

-- name: RecordDependencyRevision :one
SELECT f.revision::bigint AS revision, f.revision_digest::text AS revision_digest,
       f.updated_at::timestamptz AS updated_at
FROM cache.record_dependency_revision(sqlc.arg(namespace_key), sqlc.arg(dependency_kind), sqlc.arg(dependency_id), sqlc.arg(revision_digest), sqlc.arg(expected_revision), sqlc.arg(invalidation_id)::uuid, sqlc.arg(idempotency_key), sqlc.arg(reason), sqlc.narg(evidence)::jsonb) AS f(revision, revision_digest, updated_at, changed, old_digest, invalidation_id, event_id, namespace_epoch, retired_manifests, created_at);

-- name: GetDependencyRevisionUpdatedAt :one
SELECT updated_at FROM cache.cache_dependency_revision
WHERE namespace_key = sqlc.arg(namespace_key)
  AND dependency_kind = sqlc.arg(dependency_kind)
  AND dependency_id = sqlc.arg(dependency_id);

-- name: ReconcileInvalidations :many
SELECT i.invalidation_id, i.event_id, i.namespace_key,
       e.partition_kind, e.target_id, e.project_id, e.environment, e.candidate_id,
       i.dependency_kind, i.dependency_id, i.dependency_digest,
       i.namespace_epoch, i.retired_manifests, i.reason, i.evidence, i.created_at
FROM cache.cache_invalidation AS i
JOIN cache.cache_namespace_epoch AS e ON e.namespace_key = i.namespace_key
WHERE i.event_id > sqlc.arg(after_event_id)
ORDER BY i.event_id
LIMIT sqlc.arg(limit_count);

-- name: PruneCoordination :one
SELECT f.invalidations::bigint AS invalidations, f.expired_leases::bigint AS expired_leases
FROM cache.prune_coordination(sqlc.arg(before)::timestamptz, sqlc.arg(limit_count)) AS f(invalidations, expired_leases);

-- name: CountManifestStates :one
SELECT count(*) FILTER (WHERE state = 'admitted') AS admitted_manifests,
       count(*) FILTER (WHERE state = 'retiring') AS retiring_manifests,
       count(*) FILTER (WHERE state = 'expired') AS expired_manifests
FROM cache.cache_manifest;

-- name: CountActiveFills :one
SELECT count(*) AS active_fills FROM cache.cache_fill_lease WHERE expires_at > clock_timestamp();

-- name: CountInvalidationEventsAndMaxEpoch :one
SELECT count(*) AS invalidation_events, coalesce(max(epoch), 0)::bigint AS max_epoch
FROM cache.cache_invalidation AS i
JOIN cache.cache_namespace_epoch AS e ON e.namespace_key = i.namespace_key;

-- name: AcquireFill :one
SELECT f.lease_id::uuid AS lease_id, f.cache_key::text AS cache_key,
       f.namespace_epoch::bigint AS namespace_epoch, f.owner_id::text AS owner_id,
       f.fencing_epoch::bigint AS fencing_epoch, f.expires_at::timestamptz AS expires_at,
       f.acquired_at::timestamptz AS acquired_at
FROM cache.acquire_fill(sqlc.arg(lease_id)::uuid, sqlc.arg(cache_key), sqlc.arg(namespace_key), sqlc.arg(namespace_epoch), sqlc.arg(owner_id), sqlc.arg(lease_microseconds)::bigint * interval '1 microsecond') AS f(lease_id, cache_key, namespace_epoch, owner_id, fencing_epoch, expires_at, acquired_at);

-- name: RenewFill :one
SELECT cache.renew_fill(sqlc.arg(lease_id)::uuid, sqlc.arg(cache_key), sqlc.arg(owner_id), sqlc.arg(fencing_epoch), sqlc.arg(lease_microseconds)::bigint * interval '1 microsecond') AS renewed;

-- name: ReleaseFill :one
SELECT cache.release_fill(sqlc.arg(lease_id)::uuid, sqlc.arg(cache_key), sqlc.arg(owner_id), sqlc.arg(fencing_epoch)) AS released;

-- name: GetFillLeaseForUpdate :one
SELECT lease_id, fencing_epoch, namespace_key, namespace_epoch, manifest_id,
       (expires_at > clock_timestamp()) AS active
FROM cache.cache_fill_lease
WHERE cache_key = sqlc.arg(cache_key)
  AND namespace_key = sqlc.arg(namespace_key)
  AND owner_id = sqlc.arg(owner_id)
  AND fencing_epoch = sqlc.arg(fencing_epoch)
  AND lease_id = sqlc.arg(lease_id)::uuid
FOR UPDATE;

-- name: AdmitManifest :one
SELECT cache.admit_manifest(sqlc.arg(manifest_id)::uuid, sqlc.arg(lease_id)::uuid, sqlc.arg(cache_key), sqlc.arg(owner_id), sqlc.arg(fencing_epoch), sqlc.arg(namespace_key), sqlc.arg(namespace_epoch), sqlc.arg(partition_kind), sqlc.arg(target_id), sqlc.arg(project_id), sqlc.arg(environment), sqlc.narg(candidate_id)::text, sqlc.arg(partition_format_version), sqlc.arg(dependency_digest), sqlc.arg(policy_fingerprint), sqlc.arg(canonical_query_digest), sqlc.arg(key_format_version), sqlc.arg(storage_security_domain), sqlc.arg(object_digest), sqlc.arg(object_key), sqlc.arg(byte_size), sqlc.arg(metadata)::jsonb, sqlc.arg(origin_snapshot_seal_id)::uuid, sqlc.narg(expires_at)::timestamptz) AS manifest_id;

-- name: GetManifestByID :one
SELECT manifest_id, partition_kind, target_id, project_id, environment, candidate_id,
       partition_format_version, dependency_digest, policy_fingerprint,
       canonical_query_digest, key_format_version, origin_snapshot_seal_id, storage_security_domain,
       object_digest, object_key, byte_size, metadata, state, created_at,
       expires_at, retired_at, expired_at, retire_evidence, expire_evidence
FROM cache.cache_manifest WHERE manifest_id = sqlc.arg(manifest_id)::uuid;

-- name: ManifestIsAdmittedLive :one
SELECT state = 'admitted' AND (expires_at IS NULL OR expires_at > clock_timestamp()) AS admitted_live
FROM cache.cache_manifest WHERE manifest_id = sqlc.arg(manifest_id)::uuid;

-- name: GetAdmittedManifestForUpdate :one
SELECT state = 'admitted' AND (expires_at IS NULL OR expires_at > clock_timestamp()) AS admitted
FROM cache.cache_manifest WHERE manifest_id = sqlc.arg(manifest_id)::uuid FOR UPDATE;

-- name: GetRetentionRootForUpdate :one
SELECT manifest_id, state, reason FROM cache.cache_retention_root WHERE root_id = sqlc.arg(root_id)::uuid FOR UPDATE;

-- name: AddRetentionRoot :one
SELECT cache.add_retention_root(sqlc.arg(root_id)::uuid, sqlc.arg(manifest_id)::uuid, sqlc.arg(reason)) AS inserted;

-- name: GetRetentionRootRetireForUpdate :one
SELECT state, retire_evidence FROM cache.cache_retention_root WHERE root_id = sqlc.arg(root_id)::uuid FOR UPDATE;

-- name: RetireRetentionRoot :one
SELECT cache.retire_retention_root(sqlc.arg(root_id)::uuid, sqlc.arg(evidence)::jsonb) AS updated;

-- name: GetRetentionRootExpireForUpdate :one
SELECT state, expire_evidence FROM cache.cache_retention_root WHERE root_id = sqlc.arg(root_id)::uuid FOR UPDATE;

-- name: ExpireRetentionRoot :one
SELECT cache.expire_retention_root(sqlc.arg(root_id)::uuid, sqlc.arg(evidence)::jsonb) AS updated;

-- name: GetManifestExpireForUpdate :one
SELECT state, expire_evidence FROM cache.cache_manifest WHERE manifest_id = sqlc.arg(manifest_id)::uuid FOR UPDATE;

-- name: ManifestHasLiveRoots :one
SELECT EXISTS (SELECT 1 FROM cache.cache_retention_root WHERE manifest_id = sqlc.arg(manifest_id)::uuid AND state IN ('live','retiring')) AS roots_live;

-- name: ExpireManifest :one
SELECT cache.expire_manifest(sqlc.arg(manifest_id)::uuid, sqlc.arg(evidence)::jsonb) AS updated;

-- name: GetManifestRetireForUpdate :one
SELECT state, retire_evidence FROM cache.cache_manifest WHERE manifest_id = sqlc.arg(manifest_id)::uuid FOR UPDATE;

-- name: RetireManifest :one
SELECT cache.retire_manifest(sqlc.arg(manifest_id)::uuid, sqlc.arg(evidence)::jsonb) AS updated;
