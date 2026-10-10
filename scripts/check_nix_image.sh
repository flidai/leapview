#!/usr/bin/env bash
set -euo pipefail
umask 077

# Qualify already-built artifacts using this checkout's trusted fixtures. Building
# is the caller's responsibility; protected qualification never runs candidate recipes.
test "$#" = 2 || { echo 'usage: check_nix_image.sh IMAGE_ARCHIVE TRUSTED_APPLICATION' >&2; exit 64; }
archive="$(readlink -f "$1")"
application="$(readlink -f "$2")"
case "$(uname -m)" in
  x86_64) arch=amd64; interpreter=/lib64/ld-linux-x86-64.so.2 ;;
  aarch64) arch=arm64; interpreter=/lib/ld-linux-aarch64.so.1 ;;
  *) echo 'image qualification requires a native x86_64 or aarch64 Linux runner' >&2; exit 1 ;;
esac
cd "$(dirname "$0")/.."
reference="$(tar -xOf "$archive" manifest.json | jq -er \
  'if length == 1 and (.[0].RepoTags | length) == 1 then .[0].RepoTags[0] else error("expected one Nix candidate tag") end')"
# Docker normalizes imported config JSON, so its image ID can differ from the
# archive config digest. Restrict tag writes to the candidate namespace, then
# qualify the daemon's imported ID. Candidate tags cannot overwrite fixtures.
[[ "$reference" =~ ^leapview-nix:[0-9a-f]{12}$ ]]
docker load --input "$archive"
identity="$(docker image inspect "$reference" --format '{{.Os}}/{{.Architecture}} {{.Id}}')"
read -r image_platform image extra <<< "$identity"
[[ "$image_platform" == "linux/$arch" && -z "${extra:-}" ]] || {
  echo "loaded Nix image platform does not match native linux/$arch" >&2
  exit 1
}
[[ "$image" =~ ^sha256:[0-9a-f]{64}$ ]]
# Exercise the image's glibc rather than a host library. Compile with the pinned
# Nix compiler, then use the image's loader and runtime library search path.
mkdir -p .tmp/nix-image-qualification
probe="$PWD/.tmp/nix-image-qualification/strfmon_probe"
cc scripts/testdata/nix/strfmon_probe.c -o "$probe"
patchelf --no-sort --set-interpreter "$interpreter" --remove-rpath "$probe"
chmod 0555 "$probe"
docker run --platform "linux/$arch" --rm --network none --read-only --cap-drop ALL \
  --volume "$probe:/tmp/strfmon_probe:ro" --entrypoint /tmp/strfmon_probe "$image"
registry="leapview-nix-qualification-$$"
reference=""
registry_id=""
cleanup() {
  if [[ -n "$registry_id" ]]; then docker rm --force --volumes "$registry_id" >/dev/null 2>&1 || true; fi
  if [[ -n "$reference" ]]; then docker image rm "$reference" >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT
registry_id="$(docker run --detach --name "$registry" --publish 127.0.0.1::5000 \
  public.ecr.aws/docker/library/registry:2.8.3@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373)"
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
LEAPVIEWCTL_ROOT="$application/share/leapview/deploy/compose" \
  "$application/bin/leapviewctl" qualify image \
  --image "$digest" --require-immutable \
  --evidence-dir "$PWD/.tmp/nix-image-qualification/evidence"
