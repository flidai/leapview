-- Canonical project-generation authorization persistence. This query source
-- intentionally contains no project, legacy privilege, or securable-object
-- fields; global identity queries remain in access.sql until their owning
-- identity slices are migrated.

-- name: InsertAuthorizationRoleBinding :exec
INSERT INTO authorization_role_bindings
 (id, project_id, environment, generation_id, subject_kind, subject_id, role, capabilities_json, name)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: InsertAuthorizationGrant :exec
INSERT INTO authorization_grants
 (id, project_id, environment, generation_id, subject_kind, subject_id, resource_id, resource_kind, capability, name)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: InsertAuthorizationDataPolicy :exec
INSERT INTO authorization_data_policies
 (id, project_id, environment, generation_id, resource_id, resource_kind, subject_kind, subject_id, policy_type, expression_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetAuthorizationSnapshotDigest :one
SELECT digest
FROM authorization_snapshots
WHERE project_id = ? AND environment = ? AND generation_id = ?;
