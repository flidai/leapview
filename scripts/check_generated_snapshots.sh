#!/usr/bin/env bash
set -euo pipefail

paths=()
while (($#)); do
  if [[ "$1" == "--" ]]; then
    shift
    break
  fi
  paths+=("$1")
  shift
done

if ((${#paths[@]} == 0)) || (($# == 0)); then
  printf 'usage: %s PATH... -- COMMAND [ARG...]\n' "$0" >&2
  exit 2
fi

snapshot_root="$(mktemp -d)"
trap 'rm -rf "$snapshot_root"' EXIT

snapshot_paths() {
  local snapshot_dir="$1"
  local path
  mkdir -p "$snapshot_dir"
  for path in "${paths[@]}"; do
    if [[ -e "$path" || -L "$path" ]]; then
      mkdir -p "$snapshot_dir/$(dirname "$path")"
      cp -a -- "$path" "$snapshot_dir/$path"
    fi
  done
}

snapshot_paths "$snapshot_root/before"
"$@"
snapshot_paths "$snapshot_root/after"

if diff -r --no-dereference "$snapshot_root/before" "$snapshot_root/after"; then
  exit 0
else
  diff_status=$?
  if ((diff_status == 1)); then
    printf 'generated snapshots changed during generation; update the source or commit the generated outputs above\n' >&2
    exit 1
  fi
  exit "$diff_status"
fi
