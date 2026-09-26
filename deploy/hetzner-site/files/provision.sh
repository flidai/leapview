#!/usr/bin/env bash
set -euo pipefail

if [[ -e /var/lib/leapview-site/kamal/ready.json ]]; then
  echo "Legacy site controller is retired after Kamal handover" >&2
  exit 64
fi

site_root="/opt/leapview-site"
cd "$site_root"
set -a
# shellcheck disable=SC1091
source ./deployment.env
set +a

immutable_reference='^[-A-Za-z0-9._/:]+@sha256:[0-9a-f]{64}$'
if [[ ! "$LEAPVIEW_SITE_IMAGE" =~ $immutable_reference ]]; then
  echo "LEAPVIEW_SITE_IMAGE must be pinned by sha256 digest" >&2
  exit 1
fi
if [[ ! "$CADDY_IMAGE" =~ $immutable_reference ]]; then
  echo "CADDY_IMAGE must be pinned by sha256 digest" >&2
  exit 1
fi

install -d -m 0755 \
  "$site_root" \
  /var/lib/leapview-site/caddy-data \
  /var/lib/leapview-site/caddy-config
systemctl enable --now docker

docker pull "$LEAPVIEW_SITE_IMAGE"
docker pull "$CADDY_IMAGE"
docker compose --env-file deployment.env -f compose.yaml config --quiet
docker compose --env-file deployment.env -f compose.yaml up --detach --remove-orphans

healthy=false
for _ in $(seq 1 60); do
  if docker compose --env-file deployment.env -f compose.yaml exec -T caddy \
    wget -qO- http://leapview-site:8081/healthz >/dev/null; then
    healthy=true
    break
  fi
  sleep 2
done
if [[ "$healthy" != true ]]; then
  docker compose --env-file deployment.env -f compose.yaml ps >&2
  docker compose --env-file deployment.env -f compose.yaml logs --no-color >&2
  exit 1
fi

deployed_image_tmp="$(mktemp "$site_root/deployed-image.next.XXXXXX")"
printf '%s\n' "$LEAPVIEW_SITE_IMAGE" >"$deployed_image_tmp"
chmod 0644 "$deployed_image_tmp"
mv "$deployed_image_tmp" "$site_root/deployed-image"
