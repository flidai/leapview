#!/usr/bin/env bash
set -euo pipefail

# Compile once with the same tags as the package sweep, and enumerate that exact
# binary. Each shard owns a different disposable cluster; tests stay serial.
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
exec_dir="$1"
go test -c -tags 'integration duckdb_arrow' -o "$exec_dir/app.test" ./internal/app
go build -o "$exec_dir/testshard" ./internal/app/tools/testshard
(cd "$root/internal/app" && "$exec_dir/app.test" -test.list .) > "$exec_dir/app-tests"
patterns=()
for shard in 0 1 2 3; do
  patterns+=("$("$exec_dir/testshard" --list-file "$exec_dir/app-tests" --shard-index "$shard" --shard-count 4)")
done

pids=()
cleanup() {
  # Signal the wrappers, which terminate their children and PostgreSQL servers.
  for pid in "${pids[@]}"; do kill -TERM "$pid" 2>/dev/null || true; done
  for pid in "${pids[@]}"; do wait "$pid" 2>/dev/null || true; done
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
for shard in 0 1 2 3; do
  (
    cd "$root/internal/app"
    export LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=1
    # Testcontainers derives its session from the parent PID. Keep a distinct
    # shell parent alive per wrapper so a completed shard cannot reap a slower
    # sibling's database. Forward cancellation to the wrapper for cleanup.
    trap - EXIT
    "$exec_dir/postgres-package-exec" "$exec_dir/app.test" \
      -test.paniconexit0 -test.run="${patterns[$shard]}" -test.parallel=1 -test.count=1 \
      -test.timeout=30m -test.v -test.skip='^TestMinIOParquetSourceRefreshContract$' &
    runner=$!
    stop_runner() {
      trap - INT TERM
      kill -TERM "$runner" 2>/dev/null || true
      wait "$runner" 2>/dev/null || true
      exit 143
    }
    trap stop_runner INT TERM
    wait "$runner"
  ) > "$exec_dir/app-shard-$shard.log" 2>&1 &
  pids+=("$!")
done
status=0
for shard in 0 1 2 3; do
  if ! wait "${pids[$shard]}"; then status=1; fi
  unset 'pids[shard]'
  printf '\nPostgreSQL application shard %s\n' "$shard"
  cat "$exec_dir/app-shard-$shard.log"
done
pids=()
exit "$status"
