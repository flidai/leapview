-- Static sqlc leaves for the additive RecoverySet v3 evidence graph.

-- name: GetSuccessorEvidenceDigests :one
SELECT anchor_digest, profile_digest, capture_core_digest, capture_core_required,
       receipt_digest, authority_digest
FROM recovery.successor_manifest_binding
WHERE manifest_digest = sqlc.arg(manifest_digest);

-- name: GetSuccessorEvidence :one
SELECT l.payload_family,
       l.backend,
       l.storage_profile_id,
       l.storage_profile_revision,
       l.account_identity,
       l.endpoint,
       l.region,
       l.bucket,
       l.namespace,
       l.object_key,
       l.version_id,
       l.payload_version::integer AS payload_version,
       l.payload_digest,
       l.raw_sha256,
       l.byte_length,
       e.canonical_bytes
FROM recovery.successor_evidence_locator_v2 AS l
JOIN recovery.successor_evidence_v2 AS e
  USING (payload_family, payload_version, payload_digest)
WHERE l.payload_family = sqlc.arg(payload_family)
  AND l.payload_version::integer = sqlc.arg(payload_version)
  AND l.payload_digest = sqlc.arg(payload_digest);

-- name: InsertSuccessorEvidence :exec
INSERT INTO recovery.successor_evidence_v2
    (payload_family, payload_version, payload_digest, canonical_bytes)
VALUES (sqlc.arg(payload_family), sqlc.arg(payload_version)::smallint,
        sqlc.arg(payload_digest), sqlc.arg(canonical_bytes))
ON CONFLICT (payload_family, payload_version, payload_digest) DO NOTHING;

-- name: InsertSuccessorEvidenceLocator :exec
INSERT INTO recovery.successor_evidence_locator_v2
    (payload_family, payload_version, payload_digest, backend,
     storage_profile_id, storage_profile_revision, account_identity, endpoint,
     region, bucket, namespace, object_key, version_id, byte_length, raw_sha256)
VALUES (sqlc.arg(payload_family), sqlc.arg(payload_version)::smallint,
        sqlc.arg(payload_digest), sqlc.arg(backend),
        sqlc.arg(storage_profile_id), sqlc.arg(storage_profile_revision),
        sqlc.arg(account_identity), sqlc.arg(endpoint), sqlc.arg(region),
        sqlc.arg(bucket), sqlc.arg(namespace), sqlc.arg(object_key),
        sqlc.arg(version_id), sqlc.arg(byte_length), sqlc.arg(raw_sha256))
ON CONFLICT (payload_family, payload_version, payload_digest) DO NOTHING;

-- name: InsertSuccessorBinding :exec
INSERT INTO recovery.successor_manifest_binding
    (manifest_digest, set_id, anchor_digest, profile_digest, capture_core_digest, capture_core_required, receipt_digest,
     authority_digest, canonical_set, set_locator, verification_metadata)
VALUES (sqlc.arg(manifest_digest), sqlc.arg(set_id)::text::uuid,
        sqlc.arg(anchor_digest), sqlc.arg(profile_digest),
        sqlc.arg(capture_core_digest), true, sqlc.arg(receipt_digest), sqlc.arg(authority_digest),
        sqlc.arg(canonical_set), sqlc.arg(set_locator),
        sqlc.arg(verification_metadata))
ON CONFLICT (manifest_digest) DO NOTHING;

-- name: GetSuccessorBinding :one
SELECT manifest_digest,
       set_id::text AS set_id,
       anchor_digest,
       profile_digest,
       capture_core_digest,
       capture_core_required,
       receipt_digest,
       authority_digest,
       canonical_set,
       set_locator,
       verification_metadata
FROM recovery.successor_manifest_binding
WHERE manifest_digest = sqlc.arg(manifest_digest);

-- name: InsertRecoverySet3 :exec
INSERT INTO recovery.recovery_set_v3
    (set_id, schema_version, manifest_digest, anchor_digest, profile_digest,
     receipt_digest, receipt_core_digest, capture_core_digest, capture_core_required,
     authority_digest, frontier_projection,
     frontier_digest, canonical_bytes, created_by)
VALUES (sqlc.arg(set_id)::text::uuid, 3, sqlc.arg(manifest_digest),
        sqlc.arg(anchor_digest), sqlc.arg(profile_digest),
        sqlc.arg(receipt_digest), sqlc.arg(receipt_core_digest),
        sqlc.arg(capture_core_digest), true,
        sqlc.arg(authority_digest), sqlc.arg(frontier_projection),
        sqlc.arg(frontier_digest), sqlc.arg(canonical_bytes),
        sqlc.arg(created_by))
ON CONFLICT (set_id) DO NOTHING;

-- name: InsertRecoverySet3Root :exec
INSERT INTO recovery.recovery_set_v3_root
    (set_id, root_kind, root_uri, version_id, root_digest,
     provider_recovery_frontier, canonical_bytes)
VALUES (sqlc.arg(set_id)::text::uuid, sqlc.arg(root_kind), sqlc.arg(root_uri),
        sqlc.arg(version_id), sqlc.arg(root_digest),
        sqlc.arg(provider_recovery_frontier), sqlc.arg(canonical_bytes))
ON CONFLICT (set_id, root_kind) DO NOTHING;

-- name: GetRecoverySet3 :one
SELECT set_id::text AS set_id,
       schema_version::integer AS schema_version,
       manifest_family,
       manifest_version::integer AS manifest_version,
       manifest_digest,
       anchor_family,
       anchor_version::integer AS anchor_version,
       anchor_digest,
       profile_family,
       profile_version::integer AS profile_version,
       profile_digest,
       receipt_family,
       receipt_version::integer AS receipt_version,
       receipt_digest,
       receipt_core_digest,
       capture_core_digest,
       capture_core_required,
       authority_family,
       authority_version::integer AS authority_version,
       authority_digest,
       frontier_projection,
       frontier_digest,
       canonical_bytes,
       status,
       created_by
FROM recovery.recovery_set_v3
WHERE set_id::text = sqlc.arg(set_id_text)::varchar;

-- name: GetRecoverySet3Roots :many
SELECT set_id::text AS set_id,
       root_kind,
       root_uri,
       version_id,
       root_digest,
       provider_recovery_frontier,
       canonical_bytes
FROM recovery.recovery_set_v3_root
WHERE set_id::text = sqlc.arg(set_id_text)::varchar
ORDER BY root_kind;

-- name: LockSuccessorAssignment :one
SELECT g.incarnation_id::text AS incarnation_id,
       g.revision::bigint AS revision,
       g.policy_digest::text AS policy_digest,
       g.worker_fence::bigint AS worker_fence
FROM recovery.lock_successor_assignment() AS g(incarnation_id, revision, policy_digest, worker_fence);
