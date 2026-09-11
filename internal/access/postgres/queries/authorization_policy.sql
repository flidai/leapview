-- Target-owned authorization policy control state. The head row is mutable
-- only through the repository CAS path; revision children are immutable.

-- name: GetAuthorizationPolicyHead :one
SELECT target_id, project_id, environment, revision, digest
FROM access.authorization_policy
WHERE target_id = sqlc.arg(target_id)
  AND project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment);

-- name: LockAuthorizationPolicyHead :one
SELECT target_id, project_id, environment, revision, digest
FROM access.authorization_policy
WHERE target_id = sqlc.arg(target_id)
  AND project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment)
FOR UPDATE;

-- name: LockAuthorizationPolicyHeadForShare :one
SELECT target_id, project_id, environment, revision, digest
FROM access.authorization_policy
WHERE target_id = sqlc.arg(target_id)
  AND project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment)
FOR SHARE;

-- name: InsertAuthorizationPolicyHead :execresult
INSERT INTO access.authorization_policy(target_id, project_id, environment, revision, digest)
VALUES (sqlc.arg(target_id), sqlc.arg(project_id), sqlc.arg(environment), sqlc.arg(revision), sqlc.arg(digest))
ON CONFLICT (target_id, project_id, environment) DO NOTHING;

-- name: UpdateAuthorizationPolicyHead :execresult
UPDATE access.authorization_policy
SET revision = sqlc.arg(revision), digest = sqlc.arg(digest), updated_at = clock_timestamp()
WHERE target_id = sqlc.arg(target_id)
  AND project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment)
  AND revision = sqlc.arg(expected_revision);

-- name: InsertAuthorizationPolicyRevision :exec
INSERT INTO access.authorization_policy_revision(target_id, project_id, environment, revision, digest)
VALUES (sqlc.arg(target_id), sqlc.arg(project_id), sqlc.arg(environment), sqlc.arg(revision), sqlc.arg(digest));

-- name: InsertAuthorizationPolicyRevisionFromGeneration :exec
INSERT INTO access.authorization_policy_revision
    (target_id, project_id, environment, revision, digest, source_generation_id)
VALUES (sqlc.arg(target_id), sqlc.arg(project_id), sqlc.arg(environment), sqlc.arg(revision), sqlc.arg(digest), sqlc.arg(source_generation_id));

-- name: GetAuthorizationPolicyRevision :one
SELECT target_id, project_id, environment, revision, digest
FROM access.authorization_policy_revision
WHERE target_id = sqlc.arg(target_id)
  AND project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment)
  AND revision = sqlc.arg(revision);

-- name: GetAuthorizationPolicyRevisionByDigest :one
SELECT target_id, project_id, environment, revision, digest
FROM access.authorization_policy_revision
WHERE target_id = sqlc.arg(target_id)
  AND project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment)
  AND digest = sqlc.arg(digest)
ORDER BY revision DESC
LIMIT 1;

-- name: ListAuthorizationPolicyRoleBindings :many
SELECT id, name, subject_kind, subject_id, role, capabilities
FROM access.authorization_policy_role_binding
WHERE target_id = sqlc.arg(target_id)
  AND project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment)
  AND revision = sqlc.arg(revision)
ORDER BY id;

-- name: GetAuthorizationPolicyRoleBinding :one
SELECT id, name, subject_kind, subject_id, role, capabilities
FROM access.authorization_policy_role_binding
WHERE target_id = sqlc.arg(target_id)
  AND project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment)
  AND revision = sqlc.arg(revision)
  AND id = sqlc.arg(id);

-- name: InsertAuthorizationPolicyRoleBinding :exec
INSERT INTO access.authorization_policy_role_binding
    (target_id, project_id, environment, revision, id, subject_kind, subject_id, role, capabilities, name)
VALUES (sqlc.arg(target_id), sqlc.arg(project_id), sqlc.arg(environment), sqlc.arg(revision), sqlc.arg(id),
        sqlc.arg(subject_kind), sqlc.arg(subject_id), sqlc.arg(role), sqlc.arg(capabilities)::jsonb, sqlc.arg(name));

-- name: GetAuthorizationPolicyOperation :one
SELECT request_digest, revision, policy_digest, binding_id
FROM access.authorization_policy_operation
WHERE target_id = sqlc.arg(target_id)
  AND project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment)
  AND idempotency_key = sqlc.arg(idempotency_key);

-- name: InsertAuthorizationPolicyOperation :exec
INSERT INTO access.authorization_policy_operation
    (target_id, project_id, environment, idempotency_key, request_digest, revision, policy_digest, binding_id)
VALUES (sqlc.arg(target_id), sqlc.arg(project_id), sqlc.arg(environment), sqlc.arg(idempotency_key),
        sqlc.arg(request_digest), sqlc.arg(revision), sqlc.arg(policy_digest), sqlc.arg(binding_id));

-- name: AuthorizationPolicyPrincipalExists :one
SELECT EXISTS (
    SELECT 1 FROM access.principal
    WHERE id = sqlc.arg(subject_id)::uuid
      AND revoked_at IS NULL
);

-- name: AuthorizationPolicyGroupExists :one
SELECT EXISTS (
    SELECT 1 FROM access.access_group
    WHERE id = sqlc.arg(subject_id)::uuid
      AND revoked_at IS NULL
);
