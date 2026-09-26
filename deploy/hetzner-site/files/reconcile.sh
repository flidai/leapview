#!/usr/bin/env bash
set -euo pipefail

site_root="/opt/leapview-site"
retention_helper="$site_root/site_image_retention.py"
desired_tag="ghcr.io/flidai/leapview-site:production"
immutable_site_reference='^ghcr\.io/flidai/leapview-site@sha256:[0-9a-f]{64}$'
minimum_free_bytes="${LEAPVIEW_SITE_MIN_FREE_BYTES:-5368709120}"

cd "$site_root"
exec 9>"$site_root/site-mutation.lock"
if ! flock -n 9; then
  echo "site reconciliation is already running"
  exit 0
fi
export LEAPVIEW_SITE_LOCK_FD=9

run_retention() {
  local operation="$1"
  shift
  local -a command=(python3 "$retention_helper" "$operation" --site-root "$site_root" --lock-fd 9 --min-free-bytes "$minimum_free_bytes")
  if [[ -n "${LEAPVIEW_SITE_STORAGE_PATH:-}" ]]; then
    command+=(--storage-path "$LEAPVIEW_SITE_STORAGE_PATH")
  fi
  command+=("$@")
  if "${command[@]}"; then
    return 0
  else
    return "$?"
  fi
}

read_site_reference() {
  local path="$1" label="$2"
  local -a reference_lines=()
  if ! mapfile -t reference_lines <"$path"; then
    echo "cannot read $label" >&2
    return 65
  fi
  if [[ "${#reference_lines[@]}" -ne 1 || ! "${reference_lines[0]}" =~ $immutable_site_reference ]]; then
    echo "$label must contain exactly one canonical immutable site reference" >&2
    return 65
  fi
  REFERENCE_VALUE="${reference_lines[0]}"
}

# Resolve a transaction left by a process interruption before pruning or pulling.
if "$site_root/deploy.sh" --recover; then
  :
else
  recovery_status=$?
  echo "cannot reconcile an interrupted deployment (status $recovery_status)" >&2
  exit "$recovery_status"
fi

# This cleanup must precede every pull, including the mutable production tag.
if run_retention apply; then
  :
else
  cleanup_status=$?
  echo "site image retention/headroom check blocked the production pull (status $cleanup_status)" >&2
  exit "$cleanup_status"
fi

if docker pull "$desired_tag" >/dev/null; then
  :
else
  echo "could not fetch the production tag; the current deployment is unchanged and retryable" >&2
  exit 74
fi

if resolved_digests="$(docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$desired_tag")"; then
  :
else
  echo "could not inspect the pulled production image; retrying is safe" >&2
  exit 74
fi
desired_image=""
ambiguous=false
while IFS= read -r image_reference; do
  if [[ "$image_reference" =~ $immutable_site_reference ]]; then
    if [[ -z "$desired_image" ]]; then
      desired_image="$image_reference"
    elif [[ "$desired_image" != "$image_reference" ]]; then
      ambiguous=true
    fi
  fi
done <<<"$resolved_digests"
if [[ -z "$desired_image" || "$ambiguous" == true ]]; then
  echo "production tag did not resolve to exactly one canonical immutable site digest" >&2
  exit 74
fi

# A pulled candidate becomes an explicit root before retry classification or activation.
if run_retention apply --candidate-ref "$desired_image" --require-candidate; then
  :
else
  cleanup_status=$?
  echo "candidate is retained locally, but deployment is deferred by retention/headroom (status $cleanup_status)" >&2
  exit "$cleanup_status"
fi

active_image=""
if [[ -f "$site_root/deployed-image" ]]; then
  if ! read_site_reference "$site_root/deployed-image" "deployed-image"; then
    exit 65
  fi
  active_image="$REFERENCE_VALUE"
fi
if [[ "$active_image" == "$desired_image" ]]; then
  rm -f "$site_root/failed-desired-image"
  echo "desired image is already active; preserving previous-image"
  exit 0
fi

failed_image=""
if [[ -f "$site_root/failed-desired-image" ]]; then
  if ! read_site_reference "$site_root/failed-desired-image" "failed-desired-image"; then
    exit 65
  fi
  failed_image="$REFERENCE_VALUE"
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
if [[ "$status" -ne 76 ]]; then
  echo "deployment failure is retryable (status $status); not suppressing $desired_image" >&2
  exit "$status"
fi

failed_image_file="$(mktemp "$site_root/failed-desired-image.next.XXXXXX")" || exit 74
if ! printf '%s\n' "$desired_image" >"$failed_image_file" || ! chmod 0644 "$failed_image_file" || ! mv -f "$failed_image_file" "$site_root/failed-desired-image"; then
  rm -f "$failed_image_file"
  echo "candidate failed qualification but suppression state could not be recorded" >&2
  exit 74
fi
exit 76
