-- FAI-637 durable control leaves. The repository owns canonicalization,
-- optimistic concurrency, control digest refresh, and audit coupling.

-- name: GetSemanticAttributeControlState :one
SELECT profile, control_revision, control_digest, updated_at::text AS updated_at
FROM access.semantic_attribute_control_state
WHERE singleton;

-- name: LockSemanticAttributeControlState :one
SELECT profile, control_revision, control_digest, updated_at::text AS updated_at
FROM access.semantic_attribute_control_state
WHERE singleton
FOR UPDATE;

-- name: UpdateSemanticAttributeControlState :one
UPDATE access.semantic_attribute_control_state
SET control_revision = sqlc.arg(control_revision)::bigint,
    control_digest = sqlc.arg(control_digest)::text,
    updated_at = clock_timestamp()
WHERE singleton
RETURNING profile, control_revision, control_digest, updated_at::text AS updated_at;

-- name: InsertSemanticAttributeAssignment :one
INSERT INTO access.semantic_attribute_assignment
    (assignment_id, definition_id, subject_kind, subject_id, definition_version,
     value_type, value_shape, canonical_values, value_digest)
VALUES
    (sqlc.arg(assignment_id)::uuid, sqlc.arg(definition_id)::uuid,
     sqlc.arg(subject_kind)::text, sqlc.arg(subject_id)::uuid,
     sqlc.arg(definition_version)::bigint, sqlc.arg(value_type)::text,
     sqlc.arg(value_shape)::text, sqlc.arg(canonical_values)::text[],
     sqlc.arg(value_digest)::text)
RETURNING assignment_id::text AS assignment_id, definition_id::text AS definition_id,
          subject_kind, subject_id::text AS subject_id, definition_version,
          value_type, value_shape, canonical_values, value_digest,
          assignment_version, COALESCE(tombstoned_at::text, '') AS tombstoned_at,
          created_at::text AS created_at, updated_at::text AS updated_at;

-- name: GetActiveSemanticAttributeAssignment :one
SELECT a.assignment_id::text AS assignment_id, a.definition_id::text AS definition_id,
       d.name AS definition_name, a.subject_kind, a.subject_id::text AS subject_id,
       a.definition_version, a.value_type, a.value_shape, a.canonical_values,
       a.value_digest, a.assignment_version,
       COALESCE(a.tombstoned_at::text, '') AS tombstoned_at,
       a.created_at::text AS created_at, a.updated_at::text AS updated_at
FROM access.semantic_attribute_assignment a
JOIN access.semantic_attribute_definition d ON d.definition_id = a.definition_id
WHERE a.definition_id = sqlc.arg(definition_id)::uuid
  AND a.subject_kind = sqlc.arg(subject_kind)::text
  AND a.subject_id = sqlc.arg(subject_id)::uuid
  AND a.tombstoned_at IS NULL;

-- name: GetSemanticAttributeAssignment :one
SELECT a.assignment_id::text AS assignment_id, a.definition_id::text AS definition_id,
       d.name AS definition_name, a.subject_kind, a.subject_id::text AS subject_id,
       a.definition_version, a.value_type, a.value_shape, a.canonical_values,
       a.value_digest, a.assignment_version,
       COALESCE(a.tombstoned_at::text, '') AS tombstoned_at,
       a.created_at::text AS created_at, a.updated_at::text AS updated_at
FROM access.semantic_attribute_assignment a
JOIN access.semantic_attribute_definition d ON d.definition_id = a.definition_id
WHERE a.assignment_id = sqlc.arg(assignment_id)::uuid;

-- name: ListSemanticAttributeAssignments :many
SELECT a.assignment_id::text AS assignment_id, a.definition_id::text AS definition_id,
       d.name AS definition_name, a.subject_kind, a.subject_id::text AS subject_id,
       a.definition_version, a.value_type, a.value_shape, a.canonical_values,
       a.value_digest, a.assignment_version,
       COALESCE(a.tombstoned_at::text, '') AS tombstoned_at,
       a.created_at::text AS created_at, a.updated_at::text AS updated_at
FROM access.semantic_attribute_assignment a
JOIN access.semantic_attribute_definition d ON d.definition_id = a.definition_id
WHERE (sqlc.arg(definition_id)::text = '' OR a.definition_id::text = sqlc.arg(definition_id)::text)
  AND (sqlc.arg(subject_kind)::text = '' OR a.subject_kind = sqlc.arg(subject_kind)::text)
  AND (sqlc.arg(subject_id)::text = '' OR a.subject_id::text = sqlc.arg(subject_id)::text)
  AND (sqlc.arg(include_tombstones)::boolean OR a.tombstoned_at IS NULL)
ORDER BY a.definition_id, a.subject_kind, a.subject_id, a.created_at, a.assignment_id;

-- name: UpdateSemanticAttributeAssignment :one
UPDATE access.semantic_attribute_assignment
SET canonical_values = sqlc.arg(canonical_values)::text[],
    value_digest = sqlc.arg(value_digest)::text,
    assignment_version = assignment_version + 1,
    updated_at = clock_timestamp()
WHERE assignment_id = sqlc.arg(assignment_id)::uuid
  AND assignment_version = sqlc.arg(expected_version)::bigint
  AND tombstoned_at IS NULL
RETURNING assignment_id::text AS assignment_id, definition_id::text AS definition_id,
          subject_kind, subject_id::text AS subject_id, definition_version,
          value_type, value_shape, canonical_values, value_digest,
          assignment_version, COALESCE(tombstoned_at::text, '') AS tombstoned_at,
          created_at::text AS created_at, updated_at::text AS updated_at;

-- name: TombstoneSemanticAttributeAssignment :one
UPDATE access.semantic_attribute_assignment
SET tombstoned_at = clock_timestamp(), assignment_version = assignment_version + 1,
    updated_at = clock_timestamp()
WHERE assignment_id = sqlc.arg(assignment_id)::uuid
  AND assignment_version = sqlc.arg(expected_version)::bigint
  AND tombstoned_at IS NULL
RETURNING assignment_id::text AS assignment_id, definition_id::text AS definition_id,
          subject_kind, subject_id::text AS subject_id, definition_version,
          value_type, value_shape, canonical_values, value_digest,
          assignment_version, COALESCE(tombstoned_at::text, '') AS tombstoned_at,
          created_at::text AS created_at, updated_at::text AS updated_at;

-- name: InsertTrustedClaimMapping :one
INSERT INTO access.semantic_attribute_claim_mapping
    (mapping_id, source_kind, provider, issuer, audience, claim, definition_id, definition_version, value_type, value_shape)
VALUES
    (sqlc.arg(mapping_id)::uuid, sqlc.arg(source_kind)::text, sqlc.arg(provider)::text, sqlc.arg(issuer)::text, sqlc.arg(audience)::text, sqlc.arg(claim)::text,
     sqlc.arg(definition_id)::uuid, sqlc.arg(definition_version)::bigint,
     sqlc.arg(value_type)::text, sqlc.arg(value_shape)::text)
RETURNING mapping_id::text AS mapping_id, source_kind, provider, issuer, audience, claim,
          definition_id::text AS definition_id, definition_version, value_type,
          value_shape, mapping_version, COALESCE(tombstoned_at::text, '') AS tombstoned_at,
          created_at::text AS created_at, updated_at::text AS updated_at;

-- name: GetActiveTrustedClaimMapping :one
SELECT m.mapping_id::text AS mapping_id, m.source_kind, m.provider, m.issuer, m.audience, m.claim,
       m.definition_id::text AS definition_id, d.name AS definition_name,
       m.definition_version, m.value_type, m.value_shape, m.mapping_version,
       COALESCE(m.tombstoned_at::text, '') AS tombstoned_at,
       m.created_at::text AS created_at, m.updated_at::text AS updated_at
FROM access.semantic_attribute_claim_mapping m
JOIN access.semantic_attribute_definition d ON d.definition_id = m.definition_id
WHERE m.source_kind = sqlc.arg(source_kind)::text AND m.provider = sqlc.arg(provider)::text
  AND m.issuer = sqlc.arg(issuer)::text AND m.audience = sqlc.arg(audience)::text
  AND m.claim = sqlc.arg(claim)::text AND m.definition_id = sqlc.arg(definition_id)::uuid AND m.tombstoned_at IS NULL;

-- name: GetTrustedClaimMapping :one
SELECT m.mapping_id::text AS mapping_id, m.source_kind, m.provider, m.issuer, m.audience, m.claim,
       m.definition_id::text AS definition_id, d.name AS definition_name,
       m.definition_version, m.value_type, m.value_shape, m.mapping_version,
       COALESCE(m.tombstoned_at::text, '') AS tombstoned_at,
       m.created_at::text AS created_at, m.updated_at::text AS updated_at
FROM access.semantic_attribute_claim_mapping m
JOIN access.semantic_attribute_definition d ON d.definition_id = m.definition_id
WHERE m.mapping_id = sqlc.arg(mapping_id)::uuid;

-- name: ListTrustedClaimMappings :many
SELECT m.mapping_id::text AS mapping_id, m.source_kind, m.provider, m.issuer, m.audience, m.claim,
       m.definition_id::text AS definition_id, d.name AS definition_name,
       m.definition_version, m.value_type, m.value_shape, m.mapping_version,
       COALESCE(m.tombstoned_at::text, '') AS tombstoned_at,
       m.created_at::text AS created_at, m.updated_at::text AS updated_at
FROM access.semantic_attribute_claim_mapping m
JOIN access.semantic_attribute_definition d ON d.definition_id = m.definition_id
WHERE (sqlc.arg(source_kind)::text = '' OR m.source_kind = sqlc.arg(source_kind)::text)
  AND (sqlc.arg(provider)::text = '' OR m.provider = sqlc.arg(provider)::text)
  AND (sqlc.arg(issuer)::text = '' OR m.issuer = sqlc.arg(issuer)::text)
  AND (sqlc.arg(audience)::text = '' OR m.audience = sqlc.arg(audience)::text)
  AND (sqlc.arg(claim)::text = '' OR m.claim = sqlc.arg(claim)::text)
  AND (sqlc.arg(include_tombstones)::boolean OR m.tombstoned_at IS NULL)
ORDER BY m.source_kind, m.provider, m.issuer, m.audience, m.claim, m.definition_id, m.created_at, m.mapping_id;

-- name: TombstoneTrustedClaimMapping :one
UPDATE access.semantic_attribute_claim_mapping
SET tombstoned_at = clock_timestamp(), mapping_version = mapping_version + 1,
    updated_at = clock_timestamp()
WHERE mapping_id = sqlc.arg(mapping_id)::uuid
  AND mapping_version = sqlc.arg(expected_version)::bigint
  AND tombstoned_at IS NULL
RETURNING mapping_id::text AS mapping_id, source_kind, provider, issuer, audience, claim,
          definition_id::text AS definition_id, mapping_version,
          definition_version, value_type, value_shape,
          COALESCE(tombstoned_at::text, '') AS tombstoned_at,
          created_at::text AS created_at, updated_at::text AS updated_at;

-- name: ListPrincipalSemanticAttributeGroups :many
SELECT group_id::text AS group_id
FROM access.principal_group
WHERE principal_id = sqlc.arg(principal_id)::uuid AND revoked_at IS NULL
ORDER BY group_id;
