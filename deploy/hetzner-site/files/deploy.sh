#!/usr/bin/env bash
set -euo pipefail

if [[ -e /var/lib/leapview-site/kamal/ready.json ]]; then
  echo "Legacy site controller is retired after Kamal handover" >&2
  exit 64
fi

if [[ $# -ne 1 ]]; then
  echo "usage: deploy.sh ghcr.io/flidai/leapview-site@sha256:<digest>" >&2
  exit 64
fi

candidate_image="$1"
immutable_site_reference='^ghcr\.io/flidai/leapview-site@sha256:[0-9a-f]{64}$'
if [[ ! "$candidate_image" =~ $immutable_site_reference ]]; then
  echo "site image must use the canonical GHCR repository and an immutable sha256 digest" >&2
  exit 64
fi

site_root="/opt/leapview-site"
cd "$site_root"

exec 9>"$site_root/deploy.lock"
if ! flock -n 9; then
  echo "another site deployment is already running" >&2
  exit 75
fi

set -a
# shellcheck disable=SC1091
source "$site_root/deployment.env"
set +a
previous_image="${LEAPVIEW_SITE_IMAGE:?deployment.env is missing LEAPVIEW_SITE_IMAGE}"
if [[ ! "$previous_image" =~ $immutable_site_reference ]]; then
  echo "the active site image is not a canonical immutable reference" >&2
  exit 1
fi

# Pull before mutating the active release so registry failures are harmless.
docker pull "$candidate_image"

timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
rollback_env="$(mktemp "$site_root/deployment.env.rollback.$timestamp.XXXXXX")"
next_env="$(mktemp "$site_root/deployment.env.next.XXXXXX")"
# Invoked by the EXIT trap.
# shellcheck disable=SC2329
cleanup() {
  rm -f "$next_env"
}
trap cleanup EXIT

cp "$site_root/deployment.env" "$rollback_env"
chmod 0600 "$rollback_env"
if ! awk -v image="$candidate_image" '
  BEGIN { found = 0 }
  /^LEAPVIEW_SITE_IMAGE=/ {
    print "LEAPVIEW_SITE_IMAGE=" image
    found = 1
    next
  }
  { print }
  END {
    if (!found) {
      exit 42
    }
  }
' "$site_root/deployment.env" >"$next_env"; then
  echo "deployment.env is missing LEAPVIEW_SITE_IMAGE" >&2
  exit 1
fi
chmod 0600 "$next_env"
mv "$next_env" "$site_root/deployment.env"

record_history() {
  local result="$1"
  printf '%s\t%s\t%s\t%s\n' \
    "$timestamp" "$previous_image" "$candidate_image" "$result" \
    >>"$site_root/deployment-history.tsv"
  chmod 0600 "$site_root/deployment-history.tsv"
}

if "$site_root/provision.sh"; then
  previous_tmp="$(mktemp "$site_root/previous-image.next.XXXXXX")"
  printf '%s\n' "$previous_image" >"$previous_tmp"
  chmod 0644 "$previous_tmp"
  mv "$previous_tmp" "$site_root/previous-image"
  record_history "activated"
  printf 'activated %s\n' "$candidate_image"
  exit 0
else
  candidate_status=$?
fi

echo "candidate failed qualification; restoring $previous_image" >&2
restore_env="$(mktemp "$site_root/deployment.env.restore.XXXXXX")"
cp "$rollback_env" "$restore_env"
chmod 0600 "$restore_env"
mv "$restore_env" "$site_root/deployment.env"

if "$site_root/provision.sh"; then
  record_history "failed-rolled-back"
  echo "restored $previous_image" >&2
else
  record_history "rollback-failed"
  echo "candidate and rollback qualification both failed" >&2
fi
exit "$candidate_status"
