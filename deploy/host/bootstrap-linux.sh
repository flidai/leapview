#!/usr/bin/env bash
set -euo pipefail

readonly image_file=/run/leapview/image-reference
readonly config_file=/run/leapview/bootstrap.json

if [[ "$(id -u)" -ne 0 ]]; then
  printf 'LeapView host bootstrap must run as root\n' >&2
  exit 1
fi

# Keep the supported guest OS matrix explicit while allowing any VPS provider.
# The application lifecycle below remains identical across supported hosts.
# shellcheck disable=SC1091
source /etc/os-release
case "${ID:-}:${VERSION_ID:-}" in
  ubuntu:24.04) compose_package=docker-compose-v2 ;;
  debian:13) compose_package=docker-compose ;;
  *)
    printf 'LeapView host bootstrap requires Ubuntu 24.04 LTS or Debian 13\n' >&2
    exit 1
    ;;
esac
readonly compose_package
case "$(dpkg --print-architecture)" in
  amd64|arm64) ;;
  *)
    printf 'LeapView host bootstrap supports amd64 and arm64 hosts\n' >&2
    exit 1
    ;;
esac

IFS= read -r leapview_image <"$image_file"
if [[ ! "$leapview_image" =~ ^[A-Za-z0-9._:/-]+@sha256:[0-9a-f]{64}$ ]]; then
  printf 'LeapView image must be an immutable repository@sha256 digest\n' >&2
  exit 1
fi
if [[ ! -s "$config_file" ]]; then
  printf 'LeapView bootstrap configuration is missing\n' >&2
  exit 1
fi
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends \
  ca-certificates \
  docker.io \
  "$compose_package" \
  unattended-upgrades
systemctl enable --now docker
docker version >/dev/null
docker compose version >/dev/null

docker pull "$leapview_image"
payload_container="$(docker create "$leapview_image")"
payload_dir="$(mktemp -d /run/leapview-payload.XXXXXX)"
cleanup() {
  docker rm --force "$payload_container" >/dev/null 2>&1 || true
  rm -rf -- "$payload_dir"
}
trap cleanup EXIT

docker cp "$payload_container:/usr/local/share/leapview/deployment/." "$payload_dir"
test -x "$payload_dir/leapviewctl"
"$payload_dir/leapviewctl" host install \
  --config "$config_file" \
  --payload "$payload_dir" \
  --source-image "$leapview_image"
