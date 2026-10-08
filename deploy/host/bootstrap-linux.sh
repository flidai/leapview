#!/usr/bin/env bash
set -euo pipefail

readonly image_file=/run/leapview/image-reference
readonly config_file=/run/leapview/bootstrap.json
readonly operator_config_file=/run/leapview/operator-bootstrap.json
readonly install_marker=/opt/leapview/.host-install.json

if [[ "$#" -ne 1 || ( "$1" != "prepare-host" && "$1" != "install" ) ]]; then
  printf 'Usage: leapview-bootstrap prepare-host|install\n' >&2
  exit 2
fi
readonly mode="$1"

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
if [[ "$leapview_image" == "$revision019_image" ]]; then
  extra_packages+=(python3)
fi

if [[ "$mode" == "prepare-host" ]]; then
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
  exit 0
fi

if [[ "$leapview_image" == "$revision019_image" && ! -s "$install_marker" ]]; then
  printf 'A first install requires an image with the operator-bootstrap lifecycle; revision-019 is supported only for an existing host\n' >&2
  exit 1
fi
if [[ ! -s "$install_marker" && ! -s "$operator_config_file" ]]; then
  printf 'Private operator PostgreSQL and physical-pool bootstrap input is missing: %s\n' "$operator_config_file" >&2
  exit 1
fi
if ! command -v docker >/dev/null || ! systemctl is-active --quiet docker; then
  printf 'Host prerequisites are missing; run leapview-bootstrap prepare-host first\n' >&2
  exit 1
fi
docker compose version >/dev/null

install_config="$config_file"
if [[ "$leapview_image" == "$revision019_image" ]]; then
  install_config=/run/leapview/bootstrap-revision019.json
fi

if [[ "$leapview_image" == "$revision019_image" ]]; then
  /usr/local/libexec/leapview-revision019-config-compat prepare \
    --config "$config_file" --image "$leapview_image" --translated "$install_config" \
    --binding /opt/leapview/.host-target-binding.json \
    --marker /opt/leapview/.host-install.json
fi

docker pull "$leapview_image"
payload_container=
payload_dir=
cleanup() {
  if [[ -n "$payload_container" ]]; then
    docker rm --force "$payload_container" >/dev/null 2>&1 || true
  fi
  if [[ -n "$payload_dir" ]]; then
    rm -rf -- "$payload_dir"
  fi
}
trap cleanup EXIT
# /run may be noexec. Stage beside the installed /opt/leapview controller;
# mktemp keeps the extracted payload private until the installer validates it.
mkdir -p /opt
payload_dir="$(mktemp -d /opt/leapview-payload.XXXXXX)"
payload_container="$(docker create "$leapview_image")"

docker cp "$payload_container:/usr/local/share/leapview/deployment/." "$payload_dir"
if [[ ! -x "$payload_dir/leapviewctl" ]]; then
  printf 'LeapView deployment controller is not executable on the payload filesystem\n' >&2
  exit 1
fi
install_arguments=(
  host install
  --config "$install_config"
  --payload "$payload_dir"
  --source-image "$leapview_image"
)
if [[ "$leapview_image" != "$revision019_image" ]]; then
  install_arguments+=(--operator-config "$operator_config_file")
fi
"$payload_dir/leapviewctl" "${install_arguments[@]}"
if [[ "$leapview_image" == "$revision019_image" ]]; then
  /usr/local/libexec/leapview-revision019-config-compat verify \
    --config "$config_file" --image "$leapview_image" --translated "$install_config" \
    --binding /opt/leapview/.host-target-binding.json \
    --marker /opt/leapview/.host-install.json
fi
