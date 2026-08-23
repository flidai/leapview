#!/usr/bin/env bash
set -euo pipefail

readonly TRIVY_IMAGE="aquasec/trivy:0.74.0@sha256:62b1e65e8869bc4b4c6aa4fa2b21595256c7c2f6018a9d9ad61caf87187c1969"

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

./scripts/security_secrets.sh

docker info >/dev/null
docker run --rm \
  -v "$repo_root:/work:ro" \
  -w /work \
  "$TRIVY_IMAGE" \
  fs \
  --scanners secret,misconfig \
  --severity HIGH,CRITICAL \
  --exit-code 1 \
  --ignore-unfixed=false \
  --skip-dirs node_modules \
  --skip-dirs .data \
  --skip-dirs .tmp \
  .
