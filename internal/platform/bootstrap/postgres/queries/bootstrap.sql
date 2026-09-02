-- Static query leaves for the platform bootstrap authority.

-- name: GetSetting :one
SELECT value FROM platform.setting WHERE key = sqlc.arg(key);

-- name: UpsertSetting :exec
INSERT INTO platform.setting(key, value)
VALUES (sqlc.arg(key), sqlc.arg(value))
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value,
                                updated_at = clock_timestamp();

-- name: InsertSettingIfMissing :execrows
INSERT INTO platform.setting(key, value)
VALUES (sqlc.arg(key), sqlc.arg(value))
ON CONFLICT (key) DO NOTHING;

-- name: GetInstanceIdentity :one
SELECT instance_id, created_at
FROM platform.instance_identity
WHERE singleton_id = 1;

-- name: InsertInstanceIdentity :execrows
INSERT INTO platform.instance_identity(singleton_id, instance_id)
VALUES (1, sqlc.arg(instance_id))
ON CONFLICT (singleton_id) DO NOTHING;

-- name: GetInstanceEnvironment :one
SELECT environment, bound_at
FROM platform.instance_environment
WHERE singleton_id = 1;

-- name: InsertInstanceEnvironment :execrows
INSERT INTO platform.instance_environment(singleton_id, environment)
VALUES (1, sqlc.arg(environment))
ON CONFLICT (singleton_id) DO NOTHING;

-- name: GetProjectClaim :one
SELECT project_id, environment, claimed_by, claimed_at
FROM platform.instance_project_claim
WHERE singleton_id = 1;

-- name: InsertProjectClaim :execrows
INSERT INTO platform.instance_project_claim(singleton_id, project_id, environment, claimed_by, claimed_at)
VALUES (1, sqlc.arg(project_id), sqlc.arg(environment), sqlc.arg(claimed_by), sqlc.arg(claimed_at))
ON CONFLICT (singleton_id) DO NOTHING;
