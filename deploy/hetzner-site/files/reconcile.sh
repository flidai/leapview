#!/usr/bin/env bash
set -euo pipefail

if [[ -e /var/lib/leapview-site/kamal/ready.json ]]; then
  echo "Legacy site controller is retired after Kamal handover" >&2
  exit 64
fi

site_root="/opt/leapview-site"
desired_tag="ghcr.io/flidai/leapview-site:production"
immutable_site_reference='^ghcr\.io/flidai/leapview-site@sha256:[0-9a-f]{64}$'

cd "$site_root"
exec 8>"$site_root/reconcile.lock"
if ! flock -n 8; then
  echo "site reconciliation is already running"
  exit 0
fi

docker pull "$desired_tag" >/dev/null
desired_image=""
while IFS= read -r image_reference; do
  if [[ "$image_reference" =~ $immutable_site_reference ]]; then
    desired_image="$image_reference"
    break
  fi
done < <(
  docker image inspect \
    --format '{{range .RepoDigests}}{{println .}}{{end}}' \
    "$desired_tag"
)
if [[ ! "$desired_image" =~ $immutable_site_reference ]]; then
  echo "production tag did not resolve to a canonical immutable site image" >&2
  exit 1
fi

active_image=""
if [[ -f "$site_root/deployed-image" ]]; then
  active_image="$(tr -d '[:space:]' <"$site_root/deployed-image")"
fi
if [[ "$active_image" == "$desired_image" ]]; then
  rm -f "$site_root/failed-desired-image"
  exit 0
fi

failed_image=""
if [[ -f "$site_root/failed-desired-image" ]]; then
  failed_image="$(tr -d '[:space:]' <"$site_root/failed-desired-image")"
fi
if [[ "$failed_image" == "$desired_image" ]]; then
  echo "desired image previously failed qualification: $desired_image" >&2
  exit 0
fi

if "$site_root/deploy.sh" "$desired_image"; then
  rm -f "$site_root/failed-desired-image"
  exit 0
else
  status=$?
fi

if [[ "$status" -eq 75 ]]; then
  echo "deployment channel is busy; deferring $desired_image"
  exit 0
fi

failed_image_file="$(mktemp "$site_root/failed-desired-image.next.XXXXXX")"
printf '%s\n' "$desired_image" >"$failed_image_file"
chmod 0644 "$failed_image_file"
mv "$failed_image_file" "$site_root/failed-desired-image"
exit "$status"
