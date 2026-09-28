#!/usr/bin/env bash
set -euo pipefail
umask 077

# Reuse the production qualifier with a disposable loopback-only registry.
# Nothing is published to GHCR or deployed to a customer environment.
cd "$(dirname "$0")/.."
nix build --no-update-lock-file .#leapview --out-link result-app
nix build --no-update-lock-file .#leapview-image --out-link result-image
archive="$(readlink -f result-image)"
image="$(tar -xOf "$archive" manifest.json | jq -er '.[0].RepoTags[0]')"
docker load --input "$archive"
registry="leapview-nix-qualification-$$"
reference=""
registry_id=""
cleanup() {
  if [[ -n "$registry_id" ]]; then docker rm --force --volumes "$registry_id" >/dev/null 2>&1 || true; fi
  if [[ -n "$reference" ]]; then docker image rm "$reference" >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT
registry_id="$(docker run --detach --name "$registry" --publish 127.0.0.1::5000 \
  registry:2.8.3@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373)"
address="$(docker port "$registry_id" 5000/tcp)"
candidate_reference="$address/leapview:nix"
for attempt in {1..30}; do
  if curl --fail --silent "http://$address/v2/" >/dev/null; then break; fi
  if [[ "$attempt" == 30 ]]; then echo 'qualification registry did not become ready' >&2; exit 1; fi
  sleep 1
done
docker tag "$image" "$candidate_reference"
reference="$candidate_reference"
docker push "$reference"
digest="$(docker image inspect "$reference" --format '{{json .RepoDigests}}' |
  jq -er --arg prefix "$address/leapview@" \
    'map(select(startswith($prefix))) | if length == 1 then .[0] else error("expected one qualification registry digest") end')"
mkdir -p .tmp/nix-image-qualification/tmp
TMPDIR="$PWD/.tmp/nix-image-qualification/tmp" \
LEAPVIEWCTL_ROOT="$(readlink -f result-app)/share/leapview/deploy/compose" \
  "$(readlink -f result-app)/bin/leapviewctl" qualify image \
  --image "$digest" --require-immutable \
  --evidence-dir "$PWD/.tmp/nix-image-qualification/evidence"
