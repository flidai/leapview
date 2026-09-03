-- Typed semantic-access registry leaves. Mutation orchestration, canonical
-- registry digest calculation, and audit coupling remain in the Go repository
-- so every write uses one caller-owned transaction.

-- name: LockSemanticAttributeRegistry :one
SELECT profile, registry_revision, registry_digest, updated_at::text AS updated_at
FROM access.semantic_attribute_registry
WHERE singleton
FOR UPDATE;

-- name: GetSemanticAttributeRegistry :one
SELECT profile, registry_revision, registry_digest, updated_at::text AS updated_at
FROM access.semantic_attribute_registry
WHERE singleton;

-- name: UpdateSemanticAttributeRegistry :one
UPDATE access.semantic_attribute_registry
SET registry_revision = sqlc.arg(registry_revision)::bigint,
    registry_digest = sqlc.arg(registry_digest)::text,
    updated_at = clock_timestamp()
WHERE singleton
RETURNING profile, registry_revision, registry_digest, updated_at::text AS updated_at;

-- name: InsertSemanticAttributeDefinition :one
INSERT INTO access.semantic_attribute_definition
    (definition_id, name, value_type, value_shape, profile, owner_kind,
     owner_id, display_name, description, documentation_url)
VALUES
    (sqlc.arg(definition_id)::uuid, sqlc.arg(name)::text,
     sqlc.arg(value_type)::text, sqlc.arg(value_shape)::text,
     sqlc.arg(profile)::text, sqlc.arg(owner_kind)::text,
     NULLIF(sqlc.arg(owner_id)::text, '')::uuid, sqlc.arg(display_name)::text,
     sqlc.arg(description)::text, sqlc.arg(documentation_url)::text)
RETURNING definition_id::text AS definition_id, name, value_type, value_shape,
          profile, definition_version, owner_kind, COALESCE(owner_id::text, '')::text AS owner_id, display_name,
          description, documentation_url, enabled,
          COALESCE(disabled_at::text, '')::text AS disabled_at,
          created_at::text AS created_at, updated_at::text AS updated_at;

-- name: SemanticAttributePrincipalOwnerExists :one
SELECT EXISTS (
    SELECT 1 FROM access.principal
    WHERE id = sqlc.arg(owner_id)::uuid AND revoked_at IS NULL
);

-- name: SemanticAttributeGroupOwnerExists :one
SELECT EXISTS (
    SELECT 1 FROM access.access_group
    WHERE id = sqlc.arg(owner_id)::uuid AND revoked_at IS NULL
);

-- name: GetSemanticAttributeDefinition :one
SELECT definition_id::text AS definition_id, name, value_type, value_shape,
       profile, definition_version, owner_kind, COALESCE(owner_id::text, '')::text AS owner_id, display_name,
       description, documentation_url, enabled,
       COALESCE(disabled_at::text, '')::text AS disabled_at,
       created_at::text AS created_at, updated_at::text AS updated_at
FROM access.semantic_attribute_definition
WHERE name = sqlc.arg(name)::text;

-- name: GetSemanticAttributeDefinitionByID :one
SELECT definition_id::text AS definition_id, name, value_type, value_shape,
       profile, definition_version, owner_kind, COALESCE(owner_id::text, '')::text AS owner_id, display_name,
       description, documentation_url, enabled,
       COALESCE(disabled_at::text, '')::text AS disabled_at,
       created_at::text AS created_at, updated_at::text AS updated_at
FROM access.semantic_attribute_definition
WHERE definition_id = sqlc.arg(definition_id)::uuid;

-- name: ListSemanticAttributeDefinitions :many
SELECT definition_id::text AS definition_id, name, value_type, value_shape,
       profile, definition_version, owner_kind, COALESCE(owner_id::text, '')::text AS owner_id, display_name,
       description, documentation_url, enabled,
       COALESCE(disabled_at::text, '')::text AS disabled_at,
       created_at::text AS created_at, updated_at::text AS updated_at
FROM access.semantic_attribute_definition
ORDER BY name;

-- name: SearchSemanticAttributeDefinitions :many
SELECT definition_id::text AS definition_id, name, value_type, value_shape,
       profile, definition_version, owner_kind, COALESCE(owner_id::text, '')::text AS owner_id, display_name,
       description, documentation_url, enabled,
       COALESCE(disabled_at::text, '')::text AS disabled_at,
       created_at::text AS created_at, updated_at::text AS updated_at
FROM access.semantic_attribute_definition
WHERE sqlc.arg(search_query)::text = ''
   OR strpos(lower(name), lower(sqlc.arg(search_query)::text)) > 0
   OR strpos(lower(display_name), lower(sqlc.arg(search_query)::text)) > 0
   OR strpos(lower(description), lower(sqlc.arg(search_query)::text)) > 0
ORDER BY name
LIMIT sqlc.arg(page_size)::int;

-- name: UpdateSemanticAttributeDefinitionMetadata :one
UPDATE access.semantic_attribute_definition
SET owner_kind = sqlc.arg(owner_kind)::text,
    owner_id = NULLIF(sqlc.arg(owner_id)::text, '')::uuid,
    display_name = sqlc.arg(display_name)::text,
    description = sqlc.arg(description)::text,
    documentation_url = sqlc.arg(documentation_url)::text,
    definition_version = definition_version + 1,
    updated_at = clock_timestamp()
WHERE name = sqlc.arg(name)::text
  AND (sqlc.arg(expected_version)::bigint <= 0 OR definition_version = sqlc.arg(expected_version)::bigint)
  AND (owner_kind, owner_id, display_name, description, documentation_url)
      IS DISTINCT FROM
      (sqlc.arg(owner_kind)::text, NULLIF(sqlc.arg(owner_id)::text, '')::uuid,
       sqlc.arg(display_name)::text, sqlc.arg(description)::text,
       sqlc.arg(documentation_url)::text)
RETURNING definition_id::text AS definition_id, name, value_type, value_shape,
          profile, definition_version, owner_kind, COALESCE(owner_id::text, '')::text AS owner_id, display_name,
          description, documentation_url, enabled,
          COALESCE(disabled_at::text, '')::text AS disabled_at,
          created_at::text AS created_at, updated_at::text AS updated_at;

-- name: SetSemanticAttributeDefinitionEnabled :one
UPDATE access.semantic_attribute_definition
SET enabled = sqlc.arg(enabled)::boolean,
    disabled_at = CASE WHEN sqlc.arg(enabled)::boolean THEN NULL ELSE clock_timestamp() END,
    definition_version = definition_version + 1,
    updated_at = clock_timestamp()
WHERE name = sqlc.arg(name)::text
  AND (sqlc.arg(expected_version)::bigint <= 0 OR definition_version = sqlc.arg(expected_version)::bigint)
  AND enabled <> sqlc.arg(enabled)::boolean
RETURNING definition_id::text AS definition_id, name, value_type, value_shape,
          profile, definition_version, owner_kind, COALESCE(owner_id::text, '')::text AS owner_id, display_name,
          description, documentation_url, enabled,
          COALESCE(disabled_at::text, '')::text AS disabled_at,
          created_at::text AS created_at, updated_at::text AS updated_at;
