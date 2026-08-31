#!/usr/bin/env bash
set -euo pipefail

# Loopback-only PostgreSQL 18 provisioning for local development and tests.
# Production must inject externally managed database URLs instead.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="$ROOT/deploy/postgres/compose.yaml"
CONTROL_PASSWORD="${LEAPVIEW_POSTGRES_CONTROL_RUNTIME_PASSWORD:-leapview-local-control}"
CONTROL_READONLY_PASSWORD="${LEAPVIEW_POSTGRES_CONTROL_READONLY_PASSWORD:-leapview-local-control-readonly}"
DUCKLAKE_PASSWORD="${LEAPVIEW_POSTGRES_DUCKLAKE_RUNTIME_PASSWORD:-leapview-local-ducklake}"
CONTROL_MIGRATOR_PASSWORD="${LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_PASSWORD:-leapview-local-control-migrator}"
CONTROL_UPGRADE_COORDINATOR_PASSWORD="${LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_PASSWORD:-leapview-local-control-upgrade-coordinator}"
CONTROL_MAINTENANCE_PASSWORD="${LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_PASSWORD:-leapview-local-control-maintenance}"
DUCKLAKE_MIGRATOR_PASSWORD="${LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_PASSWORD:-leapview-local-ducklake-migrator}"
DUCKLAKE_MAINTENANCE_PASSWORD="${LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_PASSWORD:-leapview-local-ducklake-maintenance}"
ENV_FILE="${LEAPVIEW_POSTGRES_DEV_ENV_FILE:-$ROOT/.tmp/postgres-dev.env}"

# Keep worktrees isolated while retaining a stable project name for each path.
WORKTREE_DIGEST="$(printf '%s' "$ROOT" | cksum | awk '{print $1}')"
PROJECT_SUFFIX="${LEAPVIEW_POSTGRES_PROJECT_SUFFIX:-}"
PROJECT_NAME="${LEAPVIEW_POSTGRES_COMPOSE_PROJECT:-leapview-postgres-${WORKTREE_DIGEST}${PROJECT_SUFFIX}}"
PORT_BASE=55432
if [[ "${LEAPVIEW_POSTGRES_TEST_MODE:-}" == "1" ]]; then
  PORT_BASE=56432
fi
PORT="${LEAPVIEW_POSTGRES_DEV_PORT:-$((PORT_BASE + WORKTREE_DIGEST % 1000))}"

if [[ ! "$PORT" =~ ^[0-9]+$ ]] || (( PORT < 1 || PORT > 65535 )); then
  echo "LEAPVIEW_POSTGRES_DEV_PORT must be between 1 and 65535" >&2
  exit 1
fi

compose() {
  docker compose --project-name "$PROJECT_NAME" --file "$COMPOSE_FILE" "$@"
}

write_runtime_env() {
  # Passwords are accepted in this local-only helper only when URL-safe. This
  # avoids emitting or constructing malformed URLs while never printing them.
  if [[ ! "$CONTROL_PASSWORD" =~ ^[A-Za-z0-9._~-]+$ || ! "$CONTROL_READONLY_PASSWORD" =~ ^[A-Za-z0-9._~-]+$ || ! "$DUCKLAKE_PASSWORD" =~ ^[A-Za-z0-9._~-]+$ || ! "$CONTROL_MIGRATOR_PASSWORD" =~ ^[A-Za-z0-9._~-]+$ || ! "$CONTROL_UPGRADE_COORDINATOR_PASSWORD" =~ ^[A-Za-z0-9._~-]+$ || ! "$CONTROL_MAINTENANCE_PASSWORD" =~ ^[A-Za-z0-9._~-]+$ || ! "$DUCKLAKE_MIGRATOR_PASSWORD" =~ ^[A-Za-z0-9._~-]+$ || ! "$DUCKLAKE_MAINTENANCE_PASSWORD" =~ ^[A-Za-z0-9._~-]+$ ]]; then
    echo "PostgreSQL development runtime passwords must contain only URL-safe characters" >&2
    return 1
  fi
  umask 077
  mkdir -p "$(dirname "$ENV_FILE")"
  {
    printf 'LEAPVIEW_POSTGRES_CONTROL_URL=postgres://leapview_control_runtime:%s@127.0.0.1:%s/leapview_control?sslmode=disable\n' "$CONTROL_PASSWORD" "$PORT"
    printf 'LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL=postgres://leapview_control_migrator:%s@127.0.0.1:%s/leapview_control?sslmode=disable\n' "$CONTROL_MIGRATOR_PASSWORD" "$PORT"
    printf 'LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_URL=postgres://leapview_control_upgrade_coordinator:%s@127.0.0.1:%s/leapview_control?sslmode=disable\n' "$CONTROL_UPGRADE_COORDINATOR_PASSWORD" "$PORT"
    printf 'LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_URL=postgres://leapview_control_maintenance:%s@127.0.0.1:%s/leapview_control?sslmode=disable\n' "$CONTROL_MAINTENANCE_PASSWORD" "$PORT"
    printf 'LEAPVIEW_POSTGRES_CONTROL_READONLY_URL=postgres://leapview_control_readonly:%s@127.0.0.1:%s/leapview_control?sslmode=disable\n' "$CONTROL_READONLY_PASSWORD" "$PORT"
    printf 'LEAPVIEW_POSTGRES_DUCKLAKE_URL=postgres://leapview_ducklake_runtime:%s@127.0.0.1:%s/leapview_ducklake?sslmode=disable\n' "$DUCKLAKE_PASSWORD" "$PORT"
    printf 'LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL=postgres://leapview_ducklake_migrator:%s@127.0.0.1:%s/leapview_ducklake?sslmode=disable\n' "$DUCKLAKE_MIGRATOR_PASSWORD" "$PORT"
    printf 'LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_URL=postgres://leapview_ducklake_maintenance:%s@127.0.0.1:%s/leapview_ducklake?sslmode=disable\n' "$DUCKLAKE_MAINTENANCE_PASSWORD" "$PORT"
    printf 'LEAPVIEW_POSTGRES_EXPECTED_MAJOR=18\n'
    printf 'LEAPVIEW_POSTGRES_CONTROL_RUNTIME_ROLE=leapview_control_runtime\n'
    printf 'LEAPVIEW_POSTGRES_DUCKLAKE_RUNTIME_ROLE=leapview_ducklake_runtime\n'
    printf 'LEAPVIEW_POSTGRES_REQUIRE_TLS=false\n'
  } >"$ENV_FILE"
  chmod 600 "$ENV_FILE"
}

runtime_psql() {
  local role="$1"
  local password="$2"
  local database="$3"
  local statement="$4"
  compose exec --no-tty --env "PGPASSWORD=$password" postgres \
    psql --host 127.0.0.1 --username "$role" --dbname "$database" \
    --set ON_ERROR_STOP=1 --tuples-only --no-align --command "$statement"
}

check_scalar() {
  local label="$1"
  local role="$2"
  local password="$3"
  local database="$4"
  local statement="$5"
  local expected="$6"
  local actual

  # Keep psql diagnostics out of the log so a failed probe cannot echo a
  # connection URL or password supplied by the caller.
  if ! actual="$(runtime_psql "$role" "$password" "$database" "$statement" 2>/dev/null)"; then
    echo "PostgreSQL isolation check failed: $label query" >&2
    return 1
  fi
  if [[ "$actual" != "$expected" ]]; then
    echo "PostgreSQL isolation check failed: $label" >&2
    return 1
  fi
}

check_role_attributes() {
  local role="$1"
  local expected="$2"
  check_scalar "role $role attributes" \
    leapview_control_runtime "$CONTROL_PASSWORD" leapview_control \
    "SELECT rolcanlogin::text || '|' || rolsuper::text || '|' || rolcreatedb::text || '|' || rolcreaterole::text || '|' || rolinherit::text FROM pg_roles WHERE rolname = '$role'" \
    "$expected"
}

check_isolation() {
  # Fail fast if a caller points this helper at a PostgreSQL major other than
  # the pinned development/test baseline.
  check_scalar "PostgreSQL major version" \
    leapview_control_runtime "$CONTROL_PASSWORD" leapview_control \
    "SELECT current_setting('server_version_num')::integer / 10000" \
    "18" || return 1

  # Verify exact database owners before checking connectivity and privilege
  # boundaries. Names are fixed constants and no secrets are selected.
  check_scalar "control database owner" \
    leapview_control_runtime "$CONTROL_PASSWORD" leapview_control \
    "SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname = 'leapview_control'" \
    "leapview_control_owner" || return 1
  check_scalar "DuckLake database owner" \
    leapview_ducklake_runtime "$DUCKLAKE_PASSWORD" leapview_ducklake \
    "SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname = 'leapview_ducklake'" \
    "leapview_ducklake_owner" || return 1

  # Verify both positive connectivity and the exact PostgreSQL identity each
  # credential resolves to.
  [[ "$(runtime_psql leapview_control_runtime "$CONTROL_PASSWORD" leapview_control 'SELECT current_database()')" == "leapview_control" ]] || {
    echo "control runtime cannot connect to leapview_control" >&2
    return 1
  }
  check_scalar "control runtime identity" leapview_control_runtime "$CONTROL_PASSWORD" leapview_control 'SELECT current_user' leapview_control_runtime || return 1
  [[ "$(runtime_psql leapview_control_readonly "$CONTROL_READONLY_PASSWORD" leapview_control 'SELECT current_database()')" == "leapview_control" ]] || {
    echo "control readonly cannot connect to leapview_control" >&2
    return 1
  }
  check_scalar "control readonly identity" leapview_control_readonly "$CONTROL_READONLY_PASSWORD" leapview_control 'SELECT current_user' leapview_control_readonly || return 1
  [[ "$(runtime_psql leapview_control_upgrade_coordinator "$CONTROL_UPGRADE_COORDINATOR_PASSWORD" leapview_control 'SELECT current_database()')" == "leapview_control" ]] || {
    echo "control upgrade coordinator cannot connect to leapview_control" >&2
    return 1
  }
  check_scalar "control upgrade coordinator identity" leapview_control_upgrade_coordinator "$CONTROL_UPGRADE_COORDINATOR_PASSWORD" leapview_control 'SELECT current_user' leapview_control_upgrade_coordinator || return 1
  [[ "$(runtime_psql leapview_control_maintenance "$CONTROL_MAINTENANCE_PASSWORD" leapview_control 'SELECT current_database()')" == "leapview_control" ]] || {
    echo "control maintenance role cannot connect to leapview_control" >&2
    return 1
  }
  check_scalar "control maintenance identity" leapview_control_maintenance "$CONTROL_MAINTENANCE_PASSWORD" leapview_control 'SELECT current_user' leapview_control_maintenance || return 1
  [[ "$(runtime_psql leapview_ducklake_runtime "$DUCKLAKE_PASSWORD" leapview_ducklake 'SELECT current_database()')" == "leapview_ducklake" ]] || {
    echo "DuckLake runtime cannot connect to leapview_ducklake" >&2
    return 1
  }
  check_scalar "DuckLake runtime identity" leapview_ducklake_runtime "$DUCKLAKE_PASSWORD" leapview_ducklake 'SELECT current_user' leapview_ducklake_runtime || return 1
  [[ "$(runtime_psql leapview_ducklake_migrator "$DUCKLAKE_MIGRATOR_PASSWORD" leapview_ducklake 'SELECT current_database()')" == "leapview_ducklake" ]] || {
    echo "DuckLake migrator cannot connect to leapview_ducklake" >&2
    return 1
  }
  check_scalar "DuckLake migrator identity" leapview_ducklake_migrator "$DUCKLAKE_MIGRATOR_PASSWORD" leapview_ducklake 'SELECT current_user' leapview_ducklake_migrator || return 1
  [[ "$(runtime_psql leapview_ducklake_maintenance "$DUCKLAKE_MAINTENANCE_PASSWORD" leapview_ducklake 'SELECT current_database()')" == "leapview_ducklake" ]] || {
    echo "DuckLake maintenance cannot connect to leapview_ducklake" >&2
    return 1
  }
  check_scalar "DuckLake maintenance identity" leapview_ducklake_maintenance "$DUCKLAKE_MAINTENANCE_PASSWORD" leapview_ducklake 'SELECT current_user' leapview_ducklake_maintenance || return 1
  check_scalar "control migrator identity" leapview_control_migrator "$CONTROL_MIGRATOR_PASSWORD" leapview_control 'SELECT current_user' leapview_control_migrator || return 1

  # Ensure every critical role has the exact login and capability flags from
  # deploy/postgres/init.sh. The query deliberately excludes rolpassword.
  check_role_attributes leapview_control_owner 'false|false|false|false|false' || return 1
  check_role_attributes leapview_ducklake_owner 'false|false|false|false|false' || return 1
  check_role_attributes leapview_control_runtime 'true|false|false|false|false' || return 1
  check_role_attributes leapview_control_readonly 'true|false|false|false|false' || return 1
  check_role_attributes leapview_ducklake_runtime 'true|false|false|false|false' || return 1
  check_role_attributes leapview_control_migrator 'true|false|false|false|false' || return 1
  check_role_attributes leapview_control_upgrade_coordinator 'true|false|false|false|false' || return 1
  check_role_attributes leapview_control_maintenance 'true|false|false|false|false' || return 1
  check_role_attributes leapview_control_backup 'false|false|false|false|false' || return 1
  check_role_attributes leapview_ducklake_migrator 'true|false|false|false|false' || return 1
  check_role_attributes leapview_ducklake_maintenance 'true|false|false|false|false' || return 1

  # Owner-capable migration roles are the only logins allowed to assume the
  # corresponding owner role.
  if ! runtime_psql leapview_control_migrator "$CONTROL_MIGRATOR_PASSWORD" leapview_control 'SET ROLE leapview_control_owner' >/dev/null 2>&1; then
    echo "control migrator cannot SET ROLE control owner" >&2
    return 1
  fi
  if ! runtime_psql leapview_ducklake_migrator "$DUCKLAKE_MIGRATOR_PASSWORD" leapview_ducklake 'SET ROLE leapview_ducklake_owner' >/dev/null 2>&1; then
    echo "DuckLake migrator cannot SET ROLE DuckLake owner" >&2
    return 1
  fi

  if runtime_psql leapview_control_runtime "$CONTROL_PASSWORD" leapview_ducklake 'SELECT 1' >/dev/null 2>&1; then
    echo "control runtime unexpectedly connected to leapview_ducklake" >&2
    return 1
  fi
  if runtime_psql leapview_control_readonly "$CONTROL_READONLY_PASSWORD" leapview_ducklake 'SELECT 1' >/dev/null 2>&1; then
    echo "control readonly unexpectedly connected to leapview_ducklake" >&2
    return 1
  fi
  if runtime_psql leapview_control_migrator "$CONTROL_MIGRATOR_PASSWORD" leapview_ducklake 'SELECT 1' >/dev/null 2>&1; then
    echo "control migrator unexpectedly connected to leapview_ducklake" >&2
    return 1
  fi
  if runtime_psql leapview_ducklake_runtime "$DUCKLAKE_PASSWORD" leapview_control 'SELECT 1' >/dev/null 2>&1; then
    echo "DuckLake runtime unexpectedly connected to leapview_control" >&2
    return 1
  fi
  if runtime_psql leapview_control_upgrade_coordinator "$CONTROL_UPGRADE_COORDINATOR_PASSWORD" leapview_ducklake 'SELECT 1' >/dev/null 2>&1; then
    echo "control upgrade coordinator unexpectedly connected to leapview_ducklake" >&2
    return 1
  fi
  if runtime_psql leapview_control_maintenance "$CONTROL_MAINTENANCE_PASSWORD" leapview_ducklake 'SELECT 1' >/dev/null 2>&1; then
    echo "control maintenance role unexpectedly connected to leapview_ducklake" >&2
    return 1
  fi
  if runtime_psql leapview_ducklake_migrator "$DUCKLAKE_MIGRATOR_PASSWORD" leapview_control 'SELECT 1' >/dev/null 2>&1; then
    echo "DuckLake migrator unexpectedly connected to leapview_control" >&2
    return 1
  fi
  if runtime_psql leapview_ducklake_maintenance "$DUCKLAKE_MAINTENANCE_PASSWORD" leapview_control 'SELECT 1' >/dev/null 2>&1; then
    echo "DuckLake maintenance unexpectedly connected to leapview_control" >&2
    return 1
  fi
  [[ "$(runtime_psql leapview_ducklake_runtime "$DUCKLAKE_PASSWORD" leapview_ducklake "SELECT has_schema_privilege(current_user, 'ducklake', 'CREATE')")" == "f" ]] || {
    echo "DuckLake runtime unexpectedly has metadata schema CREATE privilege" >&2
    return 1
  }
  [[ "$(runtime_psql leapview_ducklake_runtime "$DUCKLAKE_PASSWORD" leapview_ducklake "SELECT has_database_privilege(current_user, current_database(), 'CREATE')")" == "f" ]] || {
    echo "DuckLake runtime unexpectedly has database CREATE privilege" >&2
    return 1
  }
  if ! runtime_psql leapview_ducklake_migrator "$DUCKLAKE_MIGRATOR_PASSWORD" leapview_ducklake 'CREATE SCHEMA leapview_catalog_isolation_probe; DROP SCHEMA leapview_catalog_isolation_probe' >/dev/null 2>&1; then
    echo "DuckLake migrator cannot create and drop an explicit metadata schema" >&2
    return 1
  fi
  if runtime_psql leapview_ducklake_runtime "$DUCKLAKE_PASSWORD" leapview_ducklake 'SET ROLE leapview_ducklake_owner' >/dev/null 2>&1; then
    echo "DuckLake runtime unexpectedly can SET ROLE ducklake owner" >&2
    return 1
  fi
  if runtime_psql leapview_ducklake_runtime "$DUCKLAKE_PASSWORD" leapview_ducklake 'SET ROLE leapview_ducklake_migrator' >/dev/null 2>&1; then
    echo "DuckLake runtime unexpectedly can SET ROLE ducklake migrator" >&2
    return 1
  fi
  if runtime_psql leapview_control_runtime "$CONTROL_PASSWORD" leapview_control 'SET ROLE leapview_control_owner' >/dev/null 2>&1; then
    echo "control runtime unexpectedly can SET ROLE control owner" >&2
    return 1
  fi
  if runtime_psql leapview_control_runtime "$CONTROL_PASSWORD" leapview_control 'SET ROLE leapview_control_migrator' >/dev/null 2>&1; then
    echo "control runtime unexpectedly can SET ROLE control migrator" >&2
    return 1
  fi
  if runtime_psql leapview_control_upgrade_coordinator "$CONTROL_UPGRADE_COORDINATOR_PASSWORD" leapview_control 'SET ROLE leapview_control_owner' >/dev/null 2>&1; then
    echo "control upgrade coordinator unexpectedly can SET ROLE control owner" >&2
    return 1
  fi
  if runtime_psql leapview_control_runtime "$CONTROL_PASSWORD" leapview_control 'SET ROLE leapview_control_readonly' >/dev/null 2>&1; then
    echo "control runtime unexpectedly can SET ROLE control readonly" >&2
    return 1
  fi
  if runtime_psql leapview_ducklake_runtime "$DUCKLAKE_PASSWORD" leapview_ducklake 'CREATE SCHEMA postgres_isolation_probe' >/dev/null 2>&1; then
    echo "DuckLake runtime unexpectedly created a schema" >&2
    return 1
  fi
  if runtime_psql leapview_ducklake_maintenance "$DUCKLAKE_MAINTENANCE_PASSWORD" leapview_ducklake 'CREATE SCHEMA postgres_maintenance_isolation_probe' >/dev/null 2>&1; then
    echo "DuckLake maintenance unexpectedly created a schema" >&2
    return 1
  fi
  if runtime_psql leapview_control_runtime "$CONTROL_PASSWORD" leapview_control 'CREATE SCHEMA postgres_isolation_probe' >/dev/null 2>&1; then
    echo "control runtime unexpectedly created a schema" >&2
    return 1
  fi
  if runtime_psql leapview_control_readonly "$CONTROL_READONLY_PASSWORD" leapview_control 'CREATE SCHEMA postgres_isolation_probe' >/dev/null 2>&1; then
    echo "control readonly unexpectedly created a schema" >&2
    return 1
  fi
  echo "PostgreSQL development role isolation check passed"
}

usage() {
  echo "Usage: $0 up|down|status|env|check"
}

case "${1:-}" in
  up)
    compose up --detach --wait
    write_runtime_env
    ;;
  down)
    compose down
    ;;
  status)
    compose ps
    ;;
  env)
    if [[ ! -f "$ENV_FILE" ]]; then
      write_runtime_env
    fi
    echo "$ENV_FILE"
    ;;
  check)
    check_isolation
    ;;
  *)
    usage >&2
    exit 2
    ;;
esac
