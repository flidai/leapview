#!/usr/bin/env bash
# Acquire canonical fixture images before container startup or source builds.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
case "${1:-}" in
  postgres)
    fixture_images=("$(sed -n 's/^const PostgreSQL18Image = "\([^"]*\)"$/\1/p' "$root/internal/platform/postgres/postgrestest/harness.go")")
    ;;
  minio)
    mapfile -t fixture_images < <(awk 'toupper($1) == "FROM" && tolower($2) != "scratch" { print $2 }' "$root/internal/platform/testminio/Dockerfile")
    ;;
  registry)
    fixture_images=("$(sed -n 's/^const qualificationRegistryImage = "\([^"]*\)"$/\1/p' "$root/internal/app/cli/composectl/qualification_image.go")")
    ;;
  *) printf 'usage: %s postgres|minio|registry\n' "$0" >&2; exit 2 ;;
esac
if ((${#fixture_images[@]} == 0)); then
  printf '%s\n' 'fixture image inventory is empty' >&2
  exit 1
fi
fixture_image_directory="$(mktemp -d)"
trap 'rm -rf "$fixture_image_directory"' EXIT

inspect_image() {
  docker image inspect "$fixture_image" --format '{{.Id}}' >"$fixture_image_directory/id"
  [[ "$(cat "$fixture_image_directory/id")" =~ ^sha256:[a-f0-9]{64}$ ]]
}
prepare_image() {
  if inspect_image 2>/dev/null; then return 0; fi
  for attempt in 1 2 3; do
    if docker pull "$fixture_image" >"$fixture_image_directory/pull.log" 2>&1; then
      cat "$fixture_image_directory/pull.log"
      inspect_image
      return 0
    else
      pull_status=$?
    fi
    cat "$fixture_image_directory/pull.log" >&2
    if ! grep -Eq 'toomanyrequests|Rate exceeded|429 Too Many Requests|i/o timeout|TLS handshake timeout|connection reset by peer|502 Bad Gateway|503 Service Unavailable|504 Gateway Timeout' "$fixture_image_directory/pull.log" ||
      [ "$attempt" -eq 3 ]; then
      exit "$pull_status"
    fi
    printf 'retrying pinned fixture image acquisition after registry failure %s/3\n' "$attempt" >&2
    sleep "$((attempt * 5))"
  done
}
for fixture_image in "${fixture_images[@]}"; do
  if [[ ! "$fixture_image" =~ ^public\.ecr\.aws/docker/library/[a-z0-9]+:[a-zA-Z0-9._-]+@sha256:[a-f0-9]{64}$ ]]; then
    printf '%s\n' 'fixture requires its canonical immutable image reference' >&2
    exit 1
  fi
  prepare_image
done
