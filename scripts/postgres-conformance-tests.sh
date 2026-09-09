#!/usr/bin/env bash
set -euo pipefail

# Keep PostgreSQL container tests out of the ordinary package sweep.  The
# inventory is derived from the source rather than a hand-maintained package
# list so adding a test that starts the shared harness cannot silently fall
# into the unit-test lane.
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
module="$(awk '$1 == "module" { print $2; exit }' "$root/go.mod")"
if [[ -z "$module" ]]; then
  printf '%s\n' 'could not determine Go module path for PostgreSQL inventory' >&2
  exit 1
fi

inventory() {
  git -C "$root" ls-files '*_test.go' |
    while IFS= read -r file; do
      # A concurrent worktree may retain a tracked deletion in its index;
      # only authored files present on disk can contribute a test package.
      [[ -f "$root/$file" ]] || continue
      # postgrestest.Start/StartTLS are the shared PostgreSQL harnesses. Keep
      # the direct tcpostgres form for legacy tests, but do not match generic
      # testcontainers usage (for example MinIO-only suites).
      if grep -Eq 'postgrestest\.Start(TLS)?\(t\)|tcpostgres\.Run\(' "$root/$file"; then
        dirname "$file"
      fi
    done |
    sort -u |
    while IFS= read -r dir; do
      printf '%s/%s\n' "$module" "$dir"
    done
}

case "${1:-list}" in
  list)
    inventory
    ;;
  run)
    mapfile -t packages < <(inventory)
    if ((${#packages[@]} == 0)); then
      printf '%s\n' 'PostgreSQL conformance inventory is empty' >&2
      exit 1
    fi

    # Keep the app package in its own bounded phase. Its native tests are
    # discovered and run with the same build tags, while the other inventory
    # packages retain the package-level four-worker bound. The phases are
    # intentionally sequential so no more than four conformance test
    # processes are active at once.
    app_package="$module/internal/app"
    non_app_packages=()
    app_present=0
    for package in "${packages[@]}"; do
      if [[ "$package" == "$app_package" ]]; then
        app_present=1
      else
        non_app_packages+=("$package")
      fi
    done

    # Include integration and DuckDB build tags so source-inventoried DuckLake
    # PostgreSQL suites are actually compiled and executed in this lane.
    # MinIO has its own external lane.
    if ((${#non_app_packages[@]} > 0)); then
      LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=1 \
        go test -tags 'integration duckdb_arrow' -p 4 -count=1 -v -skip '^TestMinIOParquetSourceRefreshContract$' "${non_app_packages[@]}"
    fi

    if ((app_present)); then
      app_shard_count=4
      app_patterns=()
      # Discover every pattern before starting any worker. A failed or empty
      # discovery result must fail closed without leaving partial execution.
      for ((shard_index = 0; shard_index < app_shard_count; shard_index++)); do
        pattern="$(go run ./internal/app/tools/testshard \
          --package "$app_package" \
          --shard-index "$shard_index" \
          --shard-count "$app_shard_count" \
          --tags 'integration duckdb_arrow')"
        if [[ -z "$pattern" ]]; then
          printf 'empty PostgreSQL app test shard pattern for shard %d\n' "$shard_index" >&2
          exit 1
        fi
        app_patterns+=("$pattern")
      done

      app_pids=()
      for ((shard_index = 0; shard_index < app_shard_count; shard_index++)); do
        pattern="${app_patterns[$shard_index]}"
        (
          printf 'PostgreSQL app conformance shard %d/%d\n' "$((shard_index + 1))" "$app_shard_count"
          LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=1 \
            go test -tags 'integration duckdb_arrow' -run "$pattern" -count=1 -v \
              -skip '^TestMinIOParquetSourceRefreshContract$' "$app_package"
        ) &
        app_pids+=("$!")
      done

      app_status=0
      # Always reap every started worker. Preserve the first non-zero status so
      # a failed shard cannot be hidden by a later successful shard.
      for pid in "${app_pids[@]}"; do
        if wait "$pid"; then
          :
        else
          status=$?
          if ((app_status == 0)); then
            app_status=$status
          fi
        fi
      done
      if ((app_status != 0)); then
        exit "$app_status"
      fi
    fi
    ;;
  *)
    printf 'usage: %s [list|run]\n' "$0" >&2
    exit 2
    ;;
esac
