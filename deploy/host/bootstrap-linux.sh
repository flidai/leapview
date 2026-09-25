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
extra_packages=()
case "${ID:-}:${VERSION_ID:-}" in
  ubuntu:24.04) compose_package=docker-compose-v2 ;;
  debian:13) compose_package=docker-compose; extra_packages=(docker-cli) ;;
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
readonly revision019_image='ghcr.io/flidai/leapview@sha256:4a4455ff0048704acf0df1a9308a39a09b4c786f801fe7f3a383ada089d21368'
readonly controller_image_file=/run/leapview/bootstrap-controller-image
install_config="$config_file"
if [[ "$leapview_image" == "$revision019_image" ]]; then
  extra_packages+=(python3)
  install_config=/run/leapview/bootstrap-revision019.json
fi
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends \
  ca-certificates \
  docker.io \
  "$compose_package" "${extra_packages[@]}" \
  unattended-upgrades
systemctl enable --now docker
docker version >/dev/null
docker compose version >/dev/null

if [[ "$leapview_image" == "$revision019_image" ]]; then
  /usr/local/libexec/leapview-revision019-config-compat prepare \
    --config "$config_file" --image "$leapview_image" --translated "$install_config" \
    --binding /opt/leapview/.host-target-binding.json \
    --marker /opt/leapview/.host-install.json
fi

docker pull "$leapview_image"
payload_container="$(docker create "$leapview_image")"
payload_dir="$(mktemp -d /run/leapview-payload.XXXXXX)"
controller_container=""
controller_dir=""
cleanup() {
  docker rm --force "$payload_container" >/dev/null 2>&1 || true
  if [[ -n "$controller_container" ]]; then
    docker rm --force "$controller_container" >/dev/null 2>&1 || true
  fi
  rm -rf -- "$payload_dir"
  if [[ -n "$controller_dir" ]]; then
    rm -rf -- "$controller_dir"
  fi
}
trap cleanup EXIT

docker cp "$payload_container:/usr/local/share/leapview/deployment/." "$payload_dir"
test -x "$payload_dir/leapviewctl"
installer="$payload_dir/leapviewctl"
install_args=()
if [[ "$leapview_image" == "$revision019_image" ]]; then
  if [[ ! -s "$controller_image_file" ]]; then
    printf 'Revision-019 bootstrap controller image is missing\n' >&2
    exit 1
  fi
  IFS= read -r controller_image <"$controller_image_file"
  if [[ ! "$controller_image" =~ ^[A-Za-z0-9._:/-]+@sha256:[0-9a-f]{64}$ ]] || [[ "$controller_image" == "$revision019_image" ]]; then
    printf 'Revision-019 bootstrap controller must be a distinct immutable image\n' >&2
    exit 1
  fi
  docker pull "$controller_image"
  controller_container="$(docker create "$controller_image")"
  controller_dir="$(mktemp -d /run/leapview-controller.XXXXXX)"
  docker cp "$controller_container:/usr/local/share/leapview/deployment/leapviewctl" "$controller_dir/leapviewctl"
  test -x "$controller_dir/leapviewctl"
  installer="$controller_dir/leapviewctl"
  install_args+=(--revision019-postgres-init /usr/local/libexec/leapview-postgres-init)
fi
"$installer" host install \
  --config "$install_config" \
  --payload "$payload_dir" \
  --source-image "$leapview_image" \
  "${install_args[@]}"
if [[ "$leapview_image" == "$revision019_image" ]]; then
  /usr/local/libexec/leapview-revision019-config-compat verify \
    --config "$config_file" --image "$leapview_image" --translated "$install_config" \
    --binding /opt/leapview/.host-target-binding.json \
    --marker /opt/leapview/.host-install.json
fi
