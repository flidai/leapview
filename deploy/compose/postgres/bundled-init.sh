#!/bin/sh
set -eu

secret_dir="${LEAPVIEW_POSTGRES_SECRET_DIR:?the PostgreSQL service must set its persistent secret directory}"
if [ "${POSTGRES_USER:-}" != leapview_bootstrap ]; then
  printf 'bundled PostgreSQL requires the fixed bootstrap role\n' >&2
  exit 1
fi

read_secret() {
  path="$secret_dir/$1"
  if [ -L "$path" ] || [ ! -f "$path" ] || [ ! -s "$path" ]; then
    printf 'required bundled PostgreSQL secret is unavailable: %s\n' "$1" >&2
    exit 1
  fi
  value=$(cat "$path")
  case "$value" in
    *[!A-Za-z0-9_-]*|'')
      printf 'bundled PostgreSQL secret is malformed: %s\n' "$1" >&2
      exit 1
      ;;
  esac
  printf '%s' "$value"
}

bootstrap_host="${LEAPVIEW_POSTGRES_BOOTSTRAP_HOST:-}"
if [ -n "$bootstrap_host" ]; then
  if [ "$bootstrap_host" != postgres ]; then
    printf 'bundled PostgreSQL bootstrap host is invalid\n' >&2
    exit 1
  fi
  export PGHOST=postgres PGPORT=5432 PGSSLMODE=verify-full PGSSLROOTCERT="$secret_dir/ca.crt"
  export PGPASSWORD="$(read_secret bootstrap-password)"
fi

psql_db() {
  database="$1"
  shift
  psql --no-psqlrc --username "$POSTGRES_USER" --dbname "$database" --set ON_ERROR_STOP=1 "$@"
}

{
  printf "\\set control_runtime_password '%s'\n" "$(read_secret control-runtime-password)"
  printf "\\set control_migrator_password '%s'\n" "$(read_secret control-migrator-password)"
  printf "\\set control_maintenance_password '%s'\n" "$(read_secret control-maintenance-password)"
  printf "\\set ducklake_runtime_password '%s'\n" "$(read_secret ducklake-runtime-password)"
  printf "\\set ducklake_migrator_password '%s'\n" "$(read_secret ducklake-migrator-password)"
  printf "\\set ducklake_maintenance_password '%s'\n" "$(read_secret ducklake-maintenance-password)"
  cat <<'SQL'
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
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_migrator') THEN
        CREATE ROLE leapview_control_migrator LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        CREATE ROLE leapview_control_maintenance LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        CREATE ROLE leapview_control_readonly NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        CREATE ROLE leapview_control_backup NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_ducklake_runtime') THEN
        CREATE ROLE leapview_ducklake_runtime LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_ducklake_migrator') THEN
        CREATE ROLE leapview_ducklake_migrator LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_ducklake_maintenance') THEN
        CREATE ROLE leapview_ducklake_maintenance LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
    END IF;
END
$roles$;

ALTER ROLE leapview_control_owner NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
ALTER ROLE leapview_ducklake_owner NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
ALTER ROLE leapview_control_runtime LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT PASSWORD :'control_runtime_password';
ALTER ROLE leapview_control_migrator LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT PASSWORD :'control_migrator_password';
ALTER ROLE leapview_control_maintenance LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT PASSWORD :'control_maintenance_password';
ALTER ROLE leapview_control_readonly NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
ALTER ROLE leapview_control_backup NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
ALTER ROLE leapview_ducklake_runtime LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT PASSWORD :'ducklake_runtime_password';
ALTER ROLE leapview_ducklake_migrator LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT PASSWORD :'ducklake_migrator_password';
ALTER ROLE leapview_ducklake_maintenance LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT PASSWORD :'ducklake_maintenance_password';

GRANT leapview_control_owner TO leapview_control_migrator;
GRANT leapview_ducklake_owner TO leapview_ducklake_migrator;
REVOKE leapview_control_owner FROM leapview_control_runtime, leapview_control_maintenance;
REVOKE leapview_ducklake_owner FROM leapview_ducklake_runtime, leapview_ducklake_maintenance;
SQL
} | psql_db postgres

ensure_database() {
  database="$1"
  owner="$2"
  current_owner=$(psql_db postgres --tuples-only --no-align --command "SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname = '${database}'")
  if [ -z "$current_owner" ]; then
    psql_db postgres --command "CREATE DATABASE ${database} OWNER ${owner}"
  elif [ "$current_owner" != "$owner" ]; then
    printf 'bundled PostgreSQL database %s has an unexpected owner\n' "$database" >&2
    exit 1
  fi
}

ensure_database leapview_control leapview_control_owner
ensure_database leapview_ducklake leapview_ducklake_owner

psql_db postgres --command 'REVOKE ALL ON DATABASE postgres FROM PUBLIC; GRANT CONNECT ON DATABASE postgres TO leapview_bootstrap'
psql_db leapview_control <<'SQL'
REVOKE ALL ON DATABASE leapview_control FROM PUBLIC;
GRANT CONNECT ON DATABASE leapview_control TO leapview_control_runtime, leapview_control_migrator, leapview_control_maintenance;
REVOKE CONNECT ON DATABASE leapview_control FROM leapview_ducklake_runtime, leapview_ducklake_migrator, leapview_ducklake_maintenance;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE, CREATE ON SCHEMA public TO leapview_control_migrator;
SQL
psql_db leapview_ducklake <<'SQL'
REVOKE ALL ON DATABASE leapview_ducklake FROM PUBLIC;
GRANT CONNECT ON DATABASE leapview_ducklake TO leapview_ducklake_runtime, leapview_ducklake_migrator, leapview_ducklake_maintenance;
REVOKE CONNECT ON DATABASE leapview_ducklake FROM leapview_control_runtime, leapview_control_migrator, leapview_control_maintenance;
GRANT CREATE ON DATABASE leapview_ducklake TO leapview_ducklake_migrator;
REVOKE CREATE ON DATABASE leapview_ducklake FROM leapview_ducklake_runtime, leapview_ducklake_maintenance;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
CREATE SCHEMA IF NOT EXISTS ducklake AUTHORIZATION leapview_ducklake_owner;
REVOKE ALL ON SCHEMA ducklake FROM PUBLIC;
GRANT USAGE ON SCHEMA ducklake TO leapview_ducklake_runtime, leapview_ducklake_maintenance;
GRANT USAGE, CREATE ON SCHEMA ducklake TO leapview_ducklake_migrator;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA ducklake TO leapview_ducklake_runtime, leapview_ducklake_maintenance;
GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA ducklake TO leapview_ducklake_runtime, leapview_ducklake_maintenance;
ALTER DEFAULT PRIVILEGES FOR ROLE leapview_ducklake_owner IN SCHEMA ducklake
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO leapview_ducklake_runtime, leapview_ducklake_maintenance;
ALTER DEFAULT PRIVILEGES FOR ROLE leapview_ducklake_owner IN SCHEMA ducklake
    GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO leapview_ducklake_runtime, leapview_ducklake_maintenance;
REVOKE CREATE ON SCHEMA ducklake FROM leapview_ducklake_maintenance;
SQL

if [ -n "$bootstrap_host" ]; then
  verify_role() {
    role="$1"
    database="$2"
    secret="$3"
    password=$(read_secret "$secret")
    PGPASSWORD="$password" psql --no-psqlrc --host=postgres --port=5432 --username="$role" --dbname="$database" --tuples-only --no-align --command 'SELECT 1' >/dev/null
  }
  verify_role leapview_control_runtime leapview_control control-runtime-password
  verify_role leapview_control_migrator leapview_control control-migrator-password
  verify_role leapview_control_maintenance leapview_control control-maintenance-password
  verify_role leapview_ducklake_runtime leapview_ducklake ducklake-runtime-password
  verify_role leapview_ducklake_migrator leapview_ducklake ducklake-migrator-password
  verify_role leapview_ducklake_maintenance leapview_ducklake ducklake-maintenance-password
fi
