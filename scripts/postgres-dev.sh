#!/usr/bin/env bash
set -euo pipefail

# Loopback-only PostgreSQL 18 provisioning for local development and tests.
# Production must inject externally managed database URLs instead.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="$ROOT/deploy/postgres/compose.yaml"
ENV_FILE="${LEAPVIEW_POSTGRES_DEV_ENV_FILE:-$ROOT/.tmp/postgres-dev.env}"

# A generated env file is the durable source for local credentials after the
# first provision. Load only password assignments, and only when the caller
# has not explicitly supplied a replacement, so repeated `task dev` runs keep
# using the passwords initialized in the persistent PostgreSQL volume.
if [[ -f "$ENV_FILE" ]]; then
  while IFS='=' read -r generated_name generated_value; do
    case "$generated_name" in
      LEAPVIEW_POSTGRES_CONTROL_RUNTIME_PASSWORD | \
        LEAPVIEW_POSTGRES_CONTROL_READONLY_PASSWORD | \
        LEAPVIEW_POSTGRES_DUCKLAKE_RUNTIME_PASSWORD | \
        LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_PASSWORD | \
        LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_PASSWORD | \
        LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_PASSWORD | \
        LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_PASSWORD | \
        LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_PASSWORD)
        if [[ -z "${!generated_name:-}" ]]; then
          export "$generated_name=$generated_value"
        fi
        ;;
    esac
  done < "$ENV_FILE"
fi

CONTROL_PASSWORD="${LEAPVIEW_POSTGRES_CONTROL_RUNTIME_PASSWORD:-leapview-local-control}"
CONTROL_READONLY_PASSWORD="${LEAPVIEW_POSTGRES_CONTROL_READONLY_PASSWORD:-leapview-local-control-readonly}"
DUCKLAKE_PASSWORD="${LEAPVIEW_POSTGRES_DUCKLAKE_RUNTIME_PASSWORD:-leapview-local-ducklake}"
CONTROL_MIGRATOR_PASSWORD="${LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_PASSWORD:-leapview-local-control-migrator}"
CONTROL_UPGRADE_COORDINATOR_PASSWORD="${LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_PASSWORD:-leapview-local-control-upgrade-coordinator}"
CONTROL_MAINTENANCE_PASSWORD="${LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_PASSWORD:-leapview-local-control-maintenance}"
DUCKLAKE_MIGRATOR_PASSWORD="${LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_PASSWORD:-leapview-local-ducklake-migrator}"
DUCKLAKE_MAINTENANCE_PASSWORD="${LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_PASSWORD:-leapview-local-ducklake-maintenance}"

# Keep worktrees isolated while retaining a stable project name for each path.
WORKTREE_DIGEST="$(printf '%s' "$ROOT" | cksum | awk '{print $1}')"
PROJECT_SUFFIX="${LEAPVIEW_POSTGRES_PROJECT_SUFFIX:-}"
PROJECT_NAME="${LEAPVIEW_POSTGRES_COMPOSE_PROJECT:-leapview-postgres-${WORKTREE_DIGEST}${PROJECT_SUFFIX}}"
PORT_BASE=55432
if [[ "${LEAPVIEW_POSTGRES_TEST_MODE:-}" == "1" ]]; then
  PORT_BASE=56432
fi
PORT="${LEAPVIEW_POSTGRES_DEV_PORT:-$((PORT_BASE + WORKTREE_DIGEST % 1000))}"

# Compose reads the host-port interpolation from the environment. Export the
# resolved, worktree-scoped value so the URL file and the published service
# always address the same endpoint (including test-mode overrides).
export LEAPVIEW_POSTGRES_DEV_PORT="$PORT"

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
  # Preserve an admitted pool identity only when this exact database still
  # contains the corresponding immutable admission row.  The helper may be
  # pointed at a newly-created volume (for example after `compose down
  # --volumes`); retaining an old ID in that case would make the next server
  # start fail its admission check instead of re-bootstraping the fresh pool.
  local preserved_pool_id=""
  local preserved_compatibility_digest=""
  local previous_port=""
  if [[ -f "$ENV_FILE" ]]; then
    previous_port="$(awk -F= '$1 == "LEAPVIEW_POSTGRES_DEV_PORT" { print $2; exit }' "$ENV_FILE" 2>/dev/null || true)"
    local previous_pool_id previous_compatibility_digest
    previous_pool_id="$(awk -F= '$1 == "LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID" { print $2; exit }' "$ENV_FILE" 2>/dev/null || true)"
    previous_compatibility_digest="$(awk -F= '$1 == "LEAPVIEW_DELIVERY_PHYSICAL_POOL_COMPATIBILITY_DIGEST" { print $2; exit }' "$ENV_FILE" 2>/dev/null || true)"
    if [[ "$previous_port" == "$PORT" && "$previous_pool_id" =~ ^sha256:[0-9a-f]{64}$ && "$previous_compatibility_digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
      local admission_match
      admission_match="$(runtime_psql leapview_control_runtime "$CONTROL_PASSWORD" leapview_control \
        "SELECT p.id || '|' || a.compatibility_digest FROM physical_pool.physical_pools p JOIN physical_pool.physical_pool_admissions a ON a.pool_id = p.id WHERE p.id = '$previous_pool_id' AND a.compatibility_digest = '$previous_compatibility_digest' LIMIT 1" 2>/dev/null || true)"
      if [[ "$admission_match" == "$previous_pool_id|$previous_compatibility_digest" ]]; then
        preserved_pool_id="$previous_pool_id"
        preserved_compatibility_digest="$previous_compatibility_digest"
      fi
    fi
  fi
  umask 077
  mkdir -p "$(dirname "$ENV_FILE")"
  {
    printf 'LEAPVIEW_POSTGRES_CONTROL_RUNTIME_PASSWORD=%s\n' "$CONTROL_PASSWORD"
    printf 'LEAPVIEW_POSTGRES_CONTROL_READONLY_PASSWORD=%s\n' "$CONTROL_READONLY_PASSWORD"
    printf 'LEAPVIEW_POSTGRES_DUCKLAKE_RUNTIME_PASSWORD=%s\n' "$DUCKLAKE_PASSWORD"
    printf 'LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_PASSWORD=%s\n' "$CONTROL_MIGRATOR_PASSWORD"
    printf 'LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_PASSWORD=%s\n' "$CONTROL_UPGRADE_COORDINATOR_PASSWORD"
    printf 'LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_PASSWORD=%s\n' "$CONTROL_MAINTENANCE_PASSWORD"
    printf 'LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_PASSWORD=%s\n' "$DUCKLAKE_MIGRATOR_PASSWORD"
    printf 'LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_PASSWORD=%s\n' "$DUCKLAKE_MAINTENANCE_PASSWORD"
    printf 'LEAPVIEW_POSTGRES_CONTROL_URL=postgres://leapview_control_runtime:%s@127.0.0.1:%s/leapview_control?sslmode=disable\n' "$CONTROL_PASSWORD" "$PORT"
    printf 'LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL=postgres://leapview_control_migrator:%s@127.0.0.1:%s/leapview_control?sslmode=disable\n' "$CONTROL_MIGRATOR_PASSWORD" "$PORT"
    printf 'LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_URL=postgres://leapview_control_upgrade_coordinator:%s@127.0.0.1:%s/leapview_control?sslmode=disable\n' "$CONTROL_UPGRADE_COORDINATOR_PASSWORD" "$PORT"
    printf 'LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_URL=postgres://leapview_control_maintenance:%s@127.0.0.1:%s/leapview_control?sslmode=disable\n' "$CONTROL_MAINTENANCE_PASSWORD" "$PORT"
    printf 'LEAPVIEW_POSTGRES_CONTROL_READONLY_URL=postgres://leapview_control_readonly:%s@127.0.0.1:%s/leapview_control?sslmode=disable\n' "$CONTROL_READONLY_PASSWORD" "$PORT"
    printf 'LEAPVIEW_POSTGRES_DUCKLAKE_URL=postgres://leapview_ducklake_runtime:%s@127.0.0.1:%s/leapview_ducklake?sslmode=disable\n' "$DUCKLAKE_PASSWORD" "$PORT"
    printf 'LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL=postgres://leapview_ducklake_migrator:%s@127.0.0.1:%s/leapview_ducklake?sslmode=disable\n' "$DUCKLAKE_MIGRATOR_PASSWORD" "$PORT"
    printf 'LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_URL=postgres://leapview_ducklake_maintenance:%s@127.0.0.1:%s/leapview_ducklake?sslmode=disable\n' "$DUCKLAKE_MAINTENANCE_PASSWORD" "$PORT"
    printf 'LEAPVIEW_POSTGRES_EXPECTED_MAJOR=18\n'
    printf 'LEAPVIEW_POSTGRES_DEV_PORT=%s\n' "$PORT"
    printf 'LEAPVIEW_POSTGRES_CONTROL_RUNTIME_ROLE=leapview_control_runtime\n'
    printf 'LEAPVIEW_POSTGRES_DUCKLAKE_RUNTIME_ROLE=leapview_ducklake_runtime\n'
    printf 'LEAPVIEW_POSTGRES_REQUIRE_TLS=false\n'
    printf 'LEAPVIEW_ENVIRONMENT=dev\n'
    printf 'LEAPVIEW_PRODUCTION=false\n'
    printf 'LEAPVIEW_DEV_AUTH_BYPASS=true\n'
    printf 'LEAPVIEW_DEV_API_TOKEN=dev\n'
    # Development still needs a stable process-local fingerprint key for
    # PostgreSQL access/audit identities; this value is intentionally not a
    # production credential and is scoped to the generated worktree file.
    printf 'LEAPVIEW_CSRF_KEY=leapview-development-csrf-key-000000000000\n'
    if [[ -n "$preserved_pool_id" ]]; then
      printf 'LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID=%s\n' "$preserved_pool_id"
      printf 'LEAPVIEW_DELIVERY_PHYSICAL_POOL_COMPATIBILITY_DIGEST=%s\n' "$preserved_compatibility_digest"
    fi
  } >"$ENV_FILE"
  chmod 600 "$ENV_FILE"
}

runtime_psql() {
  local role="$1"
  local password="$2"
  local database="$3"
  local statement="$4"
  compose exec -T --env "PGPASSWORD=$password" postgres \
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

  # psql does not echo PGPASSWORD, but defensively redact the supplied value
  # before returning a diagnostic. Keeping the server error visible is
  # important when a fresh CI topology fails before the privilege checks.
  if ! actual="$(runtime_psql "$role" "$password" "$database" "$statement" 2>&1)"; then
    actual="${actual//"$password"/[redacted]}"
    echo "PostgreSQL isolation check failed: $label query" >&2
    printf '%s\n' "$actual" >&2
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
    # The PostgreSQL image only runs init.sh for a fresh data directory. A
    # caller can therefore override one of the role passwords while a named
    # volume still contains the old credential. Verify every role before
    # persisting URLs; otherwise the generated env file would advertise a
    # password that cannot authenticate to the initialized volume.
    check_isolation
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
