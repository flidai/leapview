#!/usr/bin/env bash
# Acquire the exact harness image once before parallel package workers start.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
postgres_image="$(sed -n 's/^const PostgreSQL18Image = "\([^"]*\)"$/\1/p' "$root/internal/platform/postgres/postgrestest/harness.go")"
if [[ ! "$postgres_image" =~ ^public\.ecr\.aws/docker/library/postgres:18-alpine@sha256:[a-f0-9]{64}$ ]]; then
  printf '%s\n' 'PostgreSQL fixture requires its canonical immutable image reference' >&2
  exit 1
fi
postgres_image_directory="$(mktemp -d)"
trap 'rm -rf "$postgres_image_directory"' EXIT

inspect_image() {
  docker image inspect "$postgres_image" --format '{{.Id}}' >"$postgres_image_directory/id"
  [[ "$(cat "$postgres_image_directory/id")" =~ ^sha256:[a-f0-9]{64}$ ]]
}
if inspect_image 2>/dev/null; then exit 0; fi

for attempt in 1 2 3; do
  if docker pull "$postgres_image" >"$postgres_image_directory/pull.log" 2>&1; then
    cat "$postgres_image_directory/pull.log"
    inspect_image
    exit 0
  else
    pull_status=$?
  fi
  cat "$postgres_image_directory/pull.log" >&2
  if ! grep -Eq 'toomanyrequests|Rate exceeded|429 Too Many Requests|i/o timeout|TLS handshake timeout|connection reset by peer|502 Bad Gateway|503 Service Unavailable|504 Gateway Timeout' "$postgres_image_directory/pull.log" ||
    [ "$attempt" -eq 3 ]; then
    exit "$pull_status"
  fi
  printf 'retrying pinned PostgreSQL image acquisition after registry failure %s/3\n' "$attempt" >&2
  sleep "$((attempt * 5))"
done
