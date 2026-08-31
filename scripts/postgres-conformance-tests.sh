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
      # postgrestest.Start is the shared PostgreSQL harness. Keep the direct
      # tcpostgres form for legacy tests, but do not match generic
      # testcontainers usage (for example MinIO-only suites).
      if grep -Eq 'postgrestest\.Start\(t\)|tcpostgres\.Run\(' "$root/$file"; then
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
    # Keep package execution bounded like the ordinary package lane while
    # retaining one fail-closed inventory. MinIO has its own external lane.
    LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=1 \
      go test -p 2 -count=1 -v -skip '^TestMinIOParquetSourceRefreshContract$' "${packages[@]}"
    ;;
  *)
    printf 'usage: %s [list|run]\n' "$0" >&2
    exit 2
    ;;
esac
