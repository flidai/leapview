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

git rev-parse --is-inside-work-tree >/dev/null 2>&1 || {
  printf 'generated snapshot checks must run inside a Git worktree\n' >&2
  exit 2
}

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

ignored_paths() {
  local path
  local -a existing_paths=()
  for path in "${paths[@]}"; do
    if [[ -e "$path" || -L "$path" ]]; then
      existing_paths+=("$path")
    fi
  done
  if ((${#existing_paths[@]})); then
    git ls-files --others --ignored --exclude-standard -z -- "${existing_paths[@]}"
  fi
}

compare_snapshots() {
  diff -r --no-dereference "$1" "$2"
}

snapshot_paths "$snapshot_root/before"
"$@"
snapshot_paths "$snapshot_root/after"
new_ignored=()
while IFS= read -r -d '' path; do
  if [[ ! -e "$snapshot_root/before/$path" && ! -L "$snapshot_root/before/$path" ]]; then
    new_ignored+=("$path")
  fi
done < <(ignored_paths)

# A cold worktree may acquire ignored build outputs during generation. Ignore
# only those new files for the first comparison; changes to any preexisting
# output and all tracked or visible snapshots must still fail the check.
after_for_comparison="$snapshot_root/after"
if ((${#new_ignored[@]})); then
  after_for_comparison="$snapshot_root/after-without-new-ignored"
  mkdir -p "$after_for_comparison"
  cp -a -- "$snapshot_root/after/." "$after_for_comparison/"
  for path in "${new_ignored[@]}"; do
    rm -f -- "$after_for_comparison/$path"
  done
  while IFS= read -r -d '' path; do
    directory="$path"
    while [[ "$directory" != "$after_for_comparison" ]]; do
      relative_path="${directory#"$after_for_comparison/"}"
      if [[ -e "$snapshot_root/before/$relative_path" || -L "$snapshot_root/before/$relative_path" ]]; then
        break
      fi
      rmdir -- "$directory" 2>/dev/null || break
      directory="$(dirname "$directory")"
    done
  done < <(find "$after_for_comparison" -mindepth 1 -depth -type d -empty -print0)
fi

if compare_snapshots "$snapshot_root/before" "$after_for_comparison"; then
  :
else
  diff_status=$?
  if ((diff_status == 1)); then
    printf 'generated snapshots changed during generation; update the source or commit the generated outputs above\n' >&2
    exit 1
  fi
  exit "$diff_status"
fi

if ((${#new_ignored[@]})); then
  "$@"
  snapshot_paths "$snapshot_root/repeat"
  if compare_snapshots "$snapshot_root/after" "$snapshot_root/repeat"; then
    :
  else
    diff_status=$?
    if ((diff_status == 1)); then
      printf 'generated outputs are nondeterministic after cold initialization\n' >&2
      exit 1
    fi
    exit "$diff_status"
  fi
fi

exit 0
