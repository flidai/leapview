#!/usr/bin/env bash
set -euo pipefail
umask 077

test "$#" = 2 || { echo 'usage: check_nix_registry_image.sh IMMUTABLE_IMAGE TRUSTED_APPLICATION' >&2; exit 64; }
image="$1"
[[ "$image" =~ ^ghcr.io/flidai/leapview@sha256:[0-9a-f]{64}$ ]]
application="$(readlink -f "$2")"
cd "$(dirname "$0")/.."

# Keep the final registry reference throughout qualification. Docker's internal
# config normalization must not substitute a daemon ID or a locally repushed tag.
docker pull --platform linux/amd64 "$image"
mkdir -p .tmp/nix-published-qualification/tmp
probe="$PWD/.tmp/nix-published-qualification/strfmon_probe"
cc scripts/testdata/nix/strfmon_probe.c -o "$probe"
patchelf --no-sort --set-interpreter /lib64/ld-linux-x86-64.so.2 --remove-rpath "$probe"
chmod 0555 "$probe"
docker run --rm --network none --read-only --cap-drop ALL \
  --volume "$probe:/tmp/strfmon_probe:ro" --entrypoint /tmp/strfmon_probe "$image"
TMPDIR="$PWD/.tmp/nix-published-qualification/tmp" \
LEAPVIEWCTL_ROOT="$application/share/leapview/deploy/compose" \
  "$application/bin/leapviewctl" qualify image \
  --image "$image" --require-immutable \
  --evidence-dir "$PWD/.tmp/nix-published-qualification/evidence"
