#!/usr/bin/env bash
set -euo pipefail

site_root="/opt/leapview-site"
cd "$site_root"

immutable_reference='^[-A-Za-z0-9._/:]+@sha256:[0-9a-f]{64}$'
immutable_site_reference='^ghcr\.io/flidai/leapview-site@sha256:[0-9a-f]{64}$'
if [[ "${LEAPVIEW_SITE_LOCK_FD:-}" == "9" && -e "/proc/$$/fd/9" ]]; then
  lock_target="$(readlink -f "/proc/$$/fd/9" || true)"
  if [[ "$lock_target" != "$site_root/site-mutation.lock" ]] || ! flock -n 9; then
    echo "inherited site mutation lock is invalid or not held" >&2
    exit 75
  fi
else
  exec 9>"$site_root/site-mutation.lock"
  if ! flock -n 9; then
    echo "another site mutation is already running" >&2
    exit 75
  fi
  export LEAPVIEW_SITE_LOCK_FD=9
fi
set -a
# shellcheck disable=SC1091
source ./deployment.env
set +a

if [[ ! "$LEAPVIEW_SITE_IMAGE" =~ $immutable_site_reference ]]; then
  echo "LEAPVIEW_SITE_IMAGE must be pinned by canonical sha256 digest" >&2
  exit 64
fi
if [[ ! "$CADDY_IMAGE" =~ $immutable_reference ]]; then
  echo "CADDY_IMAGE must be pinned by sha256 digest" >&2
  exit 64
fi

# A genuinely fresh host records its initial verified image as an explicit
# first-install state. Missing rollback state on an established host is never
# silently converted into this exception.
first_install="$site_root/retention-first-install"
if [[ ! -e "$site_root/deployed-image" && ! -e "$site_root/previous-image" && ! -e "$site_root/deployment-history.tsv" && ! -e "$site_root/deployment-in-progress" && ! -e "$first_install" ]]; then
  marker_tmp="$(mktemp "$site_root/retention-first-install.next.XXXXXX")" || exit 74
  if ! printf '%s\n' "$LEAPVIEW_SITE_IMAGE" >"$marker_tmp" || ! chmod 0644 "$marker_tmp" || ! mv -f "$marker_tmp" "$first_install"; then
    rm -f "$marker_tmp"
    echo "could not record explicit first-install state" >&2
    exit 74
  fi
elif [[ -e "$first_install" && ! -e "$site_root/deployment-in-progress" ]]; then
  mapfile -t first_references < "$first_install"
  if [[ "${#first_references[@]}" -ne 1 || ! "${first_references[0]:-}" =~ $immutable_site_reference ]]; then
    echo "first-install marker contains a malformed identity" >&2
    exit 65
  fi
  if [[ "${first_references[0]}" != "$LEAPVIEW_SITE_IMAGE" ]]; then
    echo "first-install marker contradicts deployment.env" >&2
    exit 65
  fi
fi

install -d -m 0755 \
  "$site_root" \
  /var/lib/leapview-site/caddy-data \
  /var/lib/leapview-site/caddy-config
systemctl enable --now docker || exit 74

ensure_image() {
  local reference="$1"
  if docker image inspect "$reference" >/dev/null 2>&1; then
    return 0
  fi
  if docker pull "$reference"; then
    return 0
  fi
  echo "image pull failed for $reference; deployment remains retryable" >&2
  return 74
}
if ensure_image "$LEAPVIEW_SITE_IMAGE"; then :; else exit "$?"; fi
if ensure_image "$CADDY_IMAGE"; then :; else exit "$?"; fi
if ! docker compose --env-file deployment.env -f compose.yaml config --quiet; then
  echo "Compose configuration failed" >&2
  exit 74
fi
if ! docker compose --env-file deployment.env -f compose.yaml up --detach --remove-orphans; then
  echo "Compose could not start the selected release; deployment remains retryable" >&2
  exit 74
fi

healthy=false
probe_output=""
for _ in $(seq 1 60); do
  if probe_output="$(docker compose --env-file deployment.env -f compose.yaml exec -T caddy \
    wget -S -O /dev/null http://leapview-site:8081/healthz 2>&1)"; then
    healthy=true
    break
  fi
  sleep 2
done
if [[ "$healthy" != true ]]; then
  docker compose --env-file deployment.env -f compose.yaml ps >&2 || true
  docker compose --env-file deployment.env -f compose.yaml logs --no-color >&2 || true
  container_ids="$(docker compose --env-file deployment.env -f compose.yaml ps -q leapview-site 2>/dev/null)" || exit 74
  if [[ -z "$container_ids" || "$container_ids" == *$'\n'* ]]; then
    echo "site container is absent or ambiguous after startup failure" >&2
    exit 74
  fi
  if running="$(docker inspect --format '{{.State.Running}}' "$container_ids" 2>/dev/null)"; then
    :
  else
    echo "could not inspect site container after startup failure" >&2
    exit 74
  fi
  # A failed exec/DNS/connection probe does not establish an application failure.
  # Only an HTTP error response from the site endpoint can suppress this digest.
  if [[ "$running" == true ]] && printf '%s\n' "$probe_output" | LC_ALL=C grep -Eq '^[[:space:]]*HTTP/[0-9.]+[[:space:]]+[45][0-9]{2}([[:space:]]|$)'; then
    echo "site health endpoint returned an HTTP error during qualification" >&2
    exit 76
  fi
  echo "site health could not be confirmed; treating as a retryable runtime or network failure" >&2
  exit 74
fi

deployed_image_tmp="$(mktemp "$site_root/deployed-image.next.XXXXXX")" || exit 74
if ! printf '%s\n' "$LEAPVIEW_SITE_IMAGE" >"$deployed_image_tmp" || ! chmod 0644 "$deployed_image_tmp" || ! mv -f "$deployed_image_tmp" "$site_root/deployed-image"; then
  rm -f "$deployed_image_tmp"
  echo "site is healthy but deployed-image evidence could not be committed" >&2
  exit 74
fi
