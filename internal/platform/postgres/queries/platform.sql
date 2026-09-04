-- name: Probe :one
SELECT current_setting('server_version_num') AS server_version_num,
       current_user::text AS runtime_role,
       current_setting('default_transaction_read_only') AS default_transaction_read_only,
       pg_is_in_recovery() AS in_recovery;

-- name: CurrentDatabase :one
SELECT current_database() AS database_name;

-- name: RequiredExtension :one
SELECT extension.extname::text AS extension_name,
       namespace.nspname::text AS schema_name
FROM pg_catalog.pg_extension AS extension
JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = extension.extnamespace
WHERE extension.extname = sqlc.arg(extension_name)::text;

-- name: HasSchemaPrivilege :one
SELECT pg_catalog.has_schema_privilege(
    current_user,
    sqlc.arg(schema_name)::text,
    sqlc.arg(privilege_name)::text
) AS allowed;

-- name: HasTablePrivilege :one
SELECT pg_catalog.has_table_privilege(
    current_user,
    sqlc.arg(table_name)::text,
    sqlc.arg(privilege_name)::text
) AS allowed;

-- name: HasFunctionPrivilege :one
SELECT pg_catalog.has_function_privilege(
    current_user,
    sqlc.arg(function_name)::text,
    sqlc.arg(privilege_name)::text
) AS allowed;

-- name: HasCurrentDatabasePrivilege :one
SELECT pg_catalog.has_database_privilege(
    current_user,
    current_database(),
    sqlc.arg(privilege_name)::text
) AS allowed;

-- name: RoleExists :one
SELECT CAST(to_regrole(sqlc.arg(role_name)::text) IS NOT NULL AS boolean) AS exists;
