#!/usr/bin/env bash
set -euo pipefail

if (($# != 4)); then
  printf 'expected exactly four security lane results, received %d\n' "$#" >&2
  exit 64
fi

for result in "$@"; do
  if [[ "$result" != success ]]; then
    printf 'Security validation result: %s\n' "$result" >&2
    exit 1
  fi
done
