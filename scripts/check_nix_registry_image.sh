#!/usr/bin/env bash
set -euo pipefail
umask 077

test "$#" = 2 || { echo 'usage: check_nix_registry_image.sh IMMUTABLE_IMAGE TRUSTED_APPLICATION' >&2; exit 64; }
image="$1"
[[ "$image" =~ ^ghcr.io/flidai/leapview@sha256:[0-9a-f]{64}$ ]]
application="$(readlink -f "$2")"
case "$(uname -m)" in
  x86_64) arch=amd64; interpreter=/lib64/ld-linux-x86-64.so.2 ;;
  aarch64) arch=arm64; interpreter=/lib/ld-linux-aarch64.so.1 ;;
  *) echo 'image qualification requires a native x86_64 or aarch64 Linux runner' >&2; exit 1 ;;
esac
cd "$(dirname "$0")/.."

# Keep the final registry reference throughout qualification. Docker's internal
# config normalization must not substitute a daemon ID or a locally repushed tag.
docker pull --platform "linux/$arch" "$image"
image_platform="$(docker image inspect "$image" --format '{{.Os}}/{{.Architecture}}')"
[[ "$image_platform" == "linux/$arch" ]] || {
  echo "pulled image platform does not match native linux/$arch" >&2
  exit 1
}
mkdir -p .tmp/nix-published-qualification/tmp
probe="$PWD/.tmp/nix-published-qualification/strfmon_probe"
cc scripts/testdata/nix/strfmon_probe.c -o "$probe"
patchelf --no-sort --set-interpreter "$interpreter" --remove-rpath "$probe"
chmod 0555 "$probe"
docker run --platform "linux/$arch" --rm --network none --read-only --cap-drop ALL \
  --volume "$probe:/tmp/strfmon_probe:ro" --entrypoint /tmp/strfmon_probe "$image"
TMPDIR="$PWD/.tmp/nix-published-qualification/tmp" \
LEAPVIEWCTL_ROOT="$application/share/leapview/deploy/compose" \
  "$application/bin/leapviewctl" qualify image \
  --image "$image" --require-immutable \
  --evidence-dir "$PWD/.tmp/nix-published-qualification/evidence"
