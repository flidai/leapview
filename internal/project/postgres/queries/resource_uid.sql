-- Resource UID reads are always qualified by the claimed instance and
-- Project. There is intentionally no global UID lookup or standalone
-- allocator query.

-- name: GetResourceUIDByAuthoredID :one
SELECT resource_uid, instance_id, project_id, authored_resource_id, resource_kind,
       state, first_generation_id, latest_generation_id, current_generation_id,
       removed_in_generation_id, created_at, updated_at
FROM project.resource_uid_registry
WHERE instance_id = $1 AND project_id = $2 AND authored_resource_id = $3;

-- name: GetResourceUIDByUID :one
SELECT resource_uid, instance_id, project_id, authored_resource_id, resource_kind,
       state, first_generation_id, latest_generation_id, current_generation_id,
       removed_in_generation_id, created_at, updated_at
FROM project.resource_uid_registry
WHERE instance_id = $1 AND project_id = $2 AND resource_uid = $3;

-- name: ListGenerationResourceUIDs :many
SELECT resource_uid, instance_id, project_id, environment, target_id,
       generation_id, authored_resource_id, resource_kind,
       contract_status, contract_profile, contract_version, contract_digest, contract_bytes, bound_at
FROM project.resource_uid_generation
WHERE instance_id = $1 AND project_id = $2 AND generation_id = $3
ORDER BY authored_resource_id;

-- Admission retains the complete compiler-sealed inventory. It allocates no
-- UID; allocation is private to the activation transition trigger. The
-- definer validates the inventory against the already-admitted bundle before
-- writing, so runtime has no table INSERT capability.
-- name: AdmitResourceUIDInventory :one
SELECT project.admit_resource_uid_inventory(
    sqlc.arg(target_id)::text,
    sqlc.arg(project_id)::text,
    sqlc.arg(environment)::text,
    sqlc.arg(generation_id)::uuid,
    sqlc.arg(graph_digest)::text,
    sqlc.arg(bundle_digest)::text,
    sqlc.arg(graph_bytes)::bytea,
    sqlc.arg(inventory_json)::jsonb
) AS admitted;

-- name: GetResourceUIDInventory :one
SELECT generation_id, instance_id, project_id, environment, target_id,
       graph_digest, bundle_digest, inventory_json, created_at
FROM project.resource_uid_inventory
WHERE instance_id = $1 AND project_id = $2 AND generation_id = $3;

-- Restore is a separate audited authority operation; activation consumes its
-- pending row only for the exact generation and resource identity.
-- name: AuthorizeResourceUIDRestore :one
SELECT * FROM project.authorize_resource_uid_restore(
    sqlc.arg(instance_id)::text,
    sqlc.arg(target_id)::text,
    sqlc.arg(project_id)::text,
    sqlc.arg(environment)::text,
    sqlc.arg(generation_id)::uuid,
    sqlc.arg(resource_uid)::uuid,
    sqlc.arg(authored_resource_id)::text,
    sqlc.arg(resource_kind)::text,
    sqlc.arg(actor_id)::text,
    sqlc.arg(request_digest)::text
);
