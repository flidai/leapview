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
  # Include newly authored, not-yet-staged tests in local CI as well as files
  # already tracked in Git. Ignore files excluded by the repository's rules.
  git -C "$root" ls-files --cached --others --exclude-standard -- '*_test.go' |
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
    # Go runs each package in its own test process. The -exec wrapper owns one
    # disposable server for that process; the harness still creates a fresh
    # database for each test and the wrapper terminates the server on exit.
    exec_dir="$(mktemp -d)"
    trap 'rm -r -- "$exec_dir"' EXIT
    go build -o "$exec_dir/postgres-package-exec" ./internal/platform/postgres/postgrestest/cmd/packageexec
    # Bound this conformance lane at four package workers. Tests within each
    # package must stay serial because PostgreSQL roles are cluster-wide and
    # some conformance tests require exact production role names. The wrapper
    # enforces this again at the binary boundary. Include integration and
    # DuckDB build tags so
    # source-inventoried DuckLake PostgreSQL suites are actually compiled and
    # executed in this lane. MinIO has its own external lane. The application
    # package contains many container-backed tests; allow it more than Go's
    # default ten-minute package timeout on slower hosted runners.
    LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=1 \
      go test -exec "$exec_dir/postgres-package-exec" -tags 'integration duckdb_arrow' -p 4 -parallel 1 -count=1 -timeout=30m -v -skip '^TestMinIOParquetSourceRefreshContract$' "${packages[@]}"
    ;;
  *)
    printf 'usage: %s [list|run]\n' "$0" >&2
    exit 2
    ;;
esac
