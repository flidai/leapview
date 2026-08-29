#!/usr/bin/env bash
set -euo pipefail

# This script runs only during initialization of a fresh development/test
# volume. It deliberately emits no credentials or connection URLs.

control_runtime_password="${LEAPVIEW_POSTGRES_CONTROL_RUNTIME_PASSWORD:-leapview-local-control}"
control_readonly_password="${LEAPVIEW_POSTGRES_CONTROL_READONLY_PASSWORD:-leapview-local-control-readonly}"
ducklake_runtime_password="${LEAPVIEW_POSTGRES_DUCKLAKE_RUNTIME_PASSWORD:-leapview-local-ducklake}"
control_migrator_password="${LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_PASSWORD:-leapview-local-control-migrator}"
ducklake_migrator_password="${LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_PASSWORD:-leapview-local-ducklake-migrator}"

psql_admin=(psql --username "${POSTGRES_USER}" --dbname postgres --set ON_ERROR_STOP=1)

# Roles are intentionally split by capability and privilege level. Owner
# roles cannot log in; migration roles receive owner membership, while runtime
# roles receive only the grants needed by their own database.
"${psql_admin[@]}" \
  --set=control_runtime_password="${control_runtime_password}" \
  --set=control_readonly_password="${control_readonly_password}" \
  --set=ducklake_runtime_password="${ducklake_runtime_password}" \
  --set=control_migrator_password="${control_migrator_password}" \
  --set=ducklake_migrator_password="${ducklake_migrator_password}" <<'SQL'
DO $roles$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_owner') THEN
        CREATE ROLE leapview_control_owner NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_ducklake_owner') THEN
        CREATE ROLE leapview_ducklake_owner NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        CREATE ROLE leapview_control_runtime LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        CREATE ROLE leapview_control_readonly LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_ducklake_runtime') THEN
        CREATE ROLE leapview_ducklake_runtime LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_migrator') THEN
        CREATE ROLE leapview_control_migrator LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        CREATE ROLE leapview_control_backup NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_ducklake_migrator') THEN
        CREATE ROLE leapview_ducklake_migrator LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
    END IF;
END
$roles$;

ALTER ROLE leapview_control_runtime PASSWORD :'control_runtime_password';
ALTER ROLE leapview_control_readonly LOGIN PASSWORD :'control_readonly_password';
ALTER ROLE leapview_ducklake_runtime PASSWORD :'ducklake_runtime_password';
ALTER ROLE leapview_control_migrator PASSWORD :'control_migrator_password';
ALTER ROLE leapview_ducklake_migrator PASSWORD :'ducklake_migrator_password';

GRANT leapview_control_owner TO leapview_control_migrator;
GRANT leapview_ducklake_owner TO leapview_ducklake_migrator;

-- Keep the bootstrap database private as well; runtime roles are admitted
-- only to the capability database they serve.
REVOKE ALL ON DATABASE postgres FROM PUBLIC;
GRANT CONNECT ON DATABASE postgres TO leapview_bootstrap;
SQL

ensure_database() {
    local database="$1"
    local owner="$2"
    if [[ "$("${psql_admin[@]}" --tuples-only --no-align --command "SELECT 1 FROM pg_database WHERE datname = '${database}'")" != "1" ]]; then
        # Database identifiers are fixed by this script, never supplied by a
        # caller, so this command does not interpolate untrusted input.
        "${psql_admin[@]}" --command "CREATE DATABASE ${database} OWNER ${owner}"
    fi
}

ensure_database leapview_control leapview_control_owner
ensure_database leapview_ducklake leapview_ducklake_owner

# The control runtime can use only the control database; PUBLIC cannot connect
# to either database. The migration role is intentionally the only role with
# owner-level DDL through membership.
psql_db() {
    local database="$1"
    shift
    psql --username "${POSTGRES_USER}" --dbname "${database}" --set ON_ERROR_STOP=1 "$@"
}

psql_db leapview_control <<'SQL'
REVOKE ALL ON DATABASE leapview_control FROM PUBLIC;
GRANT CONNECT ON DATABASE leapview_control TO
    leapview_control_runtime,
    leapview_control_migrator,
    leapview_control_readonly,
    leapview_control_backup;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
SQL

psql_db leapview_ducklake <<'SQL'
REVOKE ALL ON DATABASE leapview_ducklake FROM PUBLIC;
GRANT CONNECT ON DATABASE leapview_ducklake TO leapview_ducklake_runtime, leapview_ducklake_migrator;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
CREATE SCHEMA IF NOT EXISTS ducklake AUTHORIZATION leapview_ducklake_owner;
REVOKE ALL ON SCHEMA ducklake FROM PUBLIC;
GRANT USAGE ON SCHEMA ducklake TO leapview_ducklake_runtime;
GRANT USAGE, CREATE ON SCHEMA ducklake TO leapview_ducklake_migrator;
SQL
