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
  if [[ ! "$CONTROL_PASSWORD" =~ ^[A-Za-z0-9._~-]+$ || ! "$CONTROL_READONLY_PASSWORD" =~ ^[A-Za-z0-9._~-]+$ || ! "$DUCKLAKE_PASSWORD" =~ ^[A-Za-z0-9._~-]+$ || ! "$CONTROL_MIGRATOR_PASSWORD" =~ ^[A-Za-z0-9._~-]+$ ]]; then
    echo "PostgreSQL development runtime passwords must contain only URL-safe characters" >&2
    return 1
  fi
  umask 077
  mkdir -p "$(dirname "$ENV_FILE")"
  {
    printf 'LEAPVIEW_POSTGRES_CONTROL_URL=postgres://leapview_control_runtime:%s@127.0.0.1:%s/leapview_control?sslmode=disable\n' "$CONTROL_PASSWORD" "$PORT"
    printf 'LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL=postgres://leapview_control_migrator:%s@127.0.0.1:%s/leapview_control?sslmode=disable\n' "$CONTROL_MIGRATOR_PASSWORD" "$PORT"
    printf 'LEAPVIEW_POSTGRES_CONTROL_READONLY_URL=postgres://leapview_control_readonly:%s@127.0.0.1:%s/leapview_control?sslmode=disable\n' "$CONTROL_READONLY_PASSWORD" "$PORT"
    printf 'LEAPVIEW_POSTGRES_DUCKLAKE_URL=postgres://leapview_ducklake_runtime:%s@127.0.0.1:%s/leapview_ducklake?sslmode=disable\n' "$DUCKLAKE_PASSWORD" "$PORT"
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

check_isolation() {
  # Verify both positive connectivity and the negative privilege boundary.
  [[ "$(runtime_psql leapview_control_runtime "$CONTROL_PASSWORD" leapview_control 'SELECT current_database()')" == "leapview_control" ]] || {
    echo "control runtime cannot connect to leapview_control" >&2
    return 1
  }
  [[ "$(runtime_psql leapview_control_readonly "$CONTROL_READONLY_PASSWORD" leapview_control 'SELECT current_database()')" == "leapview_control" ]] || {
    echo "control readonly cannot connect to leapview_control" >&2
    return 1
  }
  [[ "$(runtime_psql leapview_ducklake_runtime "$DUCKLAKE_PASSWORD" leapview_ducklake 'SELECT current_database()')" == "leapview_ducklake" ]] || {
    echo "DuckLake runtime cannot connect to leapview_ducklake" >&2
    return 1
  }
  if runtime_psql leapview_control_runtime "$CONTROL_PASSWORD" leapview_ducklake 'SELECT 1' >/dev/null 2>&1; then
    echo "control runtime unexpectedly connected to leapview_ducklake" >&2
    return 1
  fi
  if runtime_psql leapview_control_readonly "$CONTROL_READONLY_PASSWORD" leapview_ducklake 'SELECT 1' >/dev/null 2>&1; then
    echo "control readonly unexpectedly connected to leapview_ducklake" >&2
    return 1
  fi
  if runtime_psql leapview_ducklake_runtime "$DUCKLAKE_PASSWORD" leapview_control 'SELECT 1' >/dev/null 2>&1; then
    echo "DuckLake runtime unexpectedly connected to leapview_control" >&2
    return 1
  fi
  [[ "$(runtime_psql leapview_ducklake_runtime "$DUCKLAKE_PASSWORD" leapview_ducklake "SELECT has_schema_privilege(current_user, 'ducklake', 'CREATE')")" == "f" ]] || {
    echo "DuckLake runtime unexpectedly has metadata schema CREATE privilege" >&2
    return 1
  }
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
  if runtime_psql leapview_control_runtime "$CONTROL_PASSWORD" leapview_control 'SET ROLE leapview_control_readonly' >/dev/null 2>&1; then
    echo "control runtime unexpectedly can SET ROLE control readonly" >&2
    return 1
  fi
  if runtime_psql leapview_ducklake_runtime "$DUCKLAKE_PASSWORD" leapview_ducklake 'CREATE SCHEMA postgres_isolation_probe' >/dev/null 2>&1; then
    echo "DuckLake runtime unexpectedly created a schema" >&2
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
