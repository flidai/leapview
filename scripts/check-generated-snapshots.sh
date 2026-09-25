#!/usr/bin/env bash
set -euo pipefail

if (( $# < 2 )); then
  printf 'usage: %s task-name snapshot-path...\n' "$0" >&2
  exit 2
fi

generated_task="$1"
shift
before_file="$(mktemp)"
after_file="$(mktemp)"
trap 'rm -f "$before_file" "$after_file"' EXIT

snapshot() {
  git ls-files --cached --others --exclude-standard -- "$@" | sort -u |
    while IFS= read -r snapshot_path; do
      if [ -f "$snapshot_path" ]; then
        sha256sum -- "$snapshot_path"
      fi
    done
}

snapshot "$@" >"$before_file"
task "$generated_task"
snapshot "$@" >"$after_file"

if ! cmp -s "$before_file" "$after_file"; then
  printf 'public contract snapshots changed during %s:\n' "$generated_task" >&2
  diff -u "$before_file" "$after_file" >&2 || true
  exit 1
fi
