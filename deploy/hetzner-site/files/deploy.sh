#!/usr/bin/env bash
set -euo pipefail

site_root="/opt/leapview-site"
retention_helper="$site_root/site_image_retention.py"
immutable_site_reference='^ghcr\.io/flidai/leapview-site@sha256:[0-9a-f]{64}$'
minimum_free_bytes="${LEAPVIEW_SITE_MIN_FREE_BYTES:-5368709120}"

if [[ $# -ne 1 ]]; then
  echo "usage: deploy.sh ghcr.io/flidai/leapview-site@sha256:<digest> | --recover" >&2
  exit 64
fi

recover_only=false
candidate_image="$1"
if [[ "$candidate_image" == "--recover" ]]; then
  recover_only=true
elif [[ ! "$candidate_image" =~ $immutable_site_reference ]]; then
  echo "site image must use the canonical GHCR repository and an immutable sha256 digest" >&2
  exit 64
fi

cd "$site_root"
if [[ "${LEAPVIEW_SITE_LOCK_FD:-}" == "9" && -e "/proc/$$/fd/9" ]]; then
  lock_target="$(readlink -f "/proc/$$/fd/9" || true)"
  if [[ "$lock_target" != "$site_root/site-mutation.lock" ]]; then
    echo "inherited descriptor 9 is not the site mutation lock" >&2
    exit 75
  fi
  if ! flock -n 9; then
    echo "inherited site mutation lock is not held" >&2
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

atomic_site_reference() {
  local path="$1"
  local reference="$2"
  local temporary
  temporary="$(mktemp "$path.next.XXXXXX")" || return 1
  if ! printf '%s\n' "$reference" >"$temporary"; then
    rm -f "$temporary"
    return 1
  fi
  if ! chmod 0644 "$temporary" || ! mv -f "$temporary" "$path"; then
    rm -f "$temporary"
    return 1
  fi
}

atomic_transaction() {
  local previous="$1"
  local candidate="$2"
  local phase="$3"
  local temporary
  temporary="$(mktemp "$site_root/deployment-in-progress.next.XXXXXX")" || return 1
  if ! printf 'version=1\nprevious_image=%s\ncandidate_image=%s\nphase=%s\n' \
    "$previous" "$candidate" "$phase" >"$temporary"; then
    rm -f "$temporary"
    return 1
  fi
  if ! chmod 0600 "$temporary" || ! mv -f "$temporary" "$site_root/deployment-in-progress"; then
    rm -f "$temporary"
    return 1
  fi
}

read_site_env() {
  local env_path="${1:-$site_root/deployment.env}"
  local lines
  lines="$(cat "$env_path")" || return 1
  local site_count=0 caddy_count=0 line
  LEAPVIEW_SITE_IMAGE=""
  CADDY_IMAGE=""
  while IFS= read -r line; do
    case "$line" in
      LEAPVIEW_SITE_IMAGE=*) LEAPVIEW_SITE_IMAGE="${line#*=}"; ((site_count += 1)) ;;
      CADDY_IMAGE=*) CADDY_IMAGE="${line#*=}"; ((caddy_count += 1)) ;;
      *) echo "deployment.env contains an unexpected setting" >&2; return 1 ;;
    esac
  done <<<"$lines"
  [[ "$site_count" -eq 1 && "$caddy_count" -eq 1 && "$LEAPVIEW_SITE_IMAGE" =~ $immutable_site_reference && "$CADDY_IMAGE" =~ ^[-A-Za-z0-9._/:]+@sha256:[0-9a-f]{64}$ ]] || return 1
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

clear_transaction_scratch() {
  local old="$1" candidate="$2" path
  local saved_site="$LEAPVIEW_SITE_IMAGE" saved_caddy="$CADDY_IMAGE"
  for path in "$site_root"/deployment.env.next.* "$site_root"/deployment.env.restore.*; do
    [[ -f "$path" ]] || continue
    if read_site_env "$path" && { [[ "$LEAPVIEW_SITE_IMAGE" == "$old" ]] || [[ "$LEAPVIEW_SITE_IMAGE" == "$candidate" ]]; }; then
      if ! rm -f "$path"; then
        LEAPVIEW_SITE_IMAGE="$saved_site"
        CADDY_IMAGE="$saved_caddy"
        echo "could not clear transaction scratch file $path" >&2
        return 1
      fi
    fi
  done
  LEAPVIEW_SITE_IMAGE="$saved_site"
  CADDY_IMAGE="$saved_caddy"
}

set_site_env_reference() {
  local reference="$1"
  local temporary
  temporary="$(mktemp "$site_root/deployment.env.next.XXXXXX")" || return 1
  if ! awk -v image="$reference" '
    BEGIN { found = 0 }
    /^LEAPVIEW_SITE_IMAGE=/ { print "LEAPVIEW_SITE_IMAGE=" image; found++; next }
    /^CADDY_IMAGE=/ { print; next }
    { exit 42 }
    END { if (found != 1) exit 42 }
  ' "$site_root/deployment.env" >"$temporary"; then
    rm -f "$temporary"
    echo "deployment.env is malformed" >&2
    return 1
  fi
  if ! chmod 0600 "$temporary" || ! mv -f "$temporary" "$site_root/deployment.env"; then
    rm -f "$temporary"
    return 1
  fi
}

record_history() {
  local old="$1" candidate="$2" result="$3" timestamp="$4"
  if ! printf '%s\t%s\t%s\t%s\n' "$timestamp" "$old" "$candidate" "$result" >>"$site_root/deployment-history.tsv"; then
    return 1
  fi
  chmod 0600 "$site_root/deployment-history.tsv"
}

finish_successful_transaction() {
  local old="$1" candidate="$2" timestamp="$3"
  if ! docker image inspect "$old" >/dev/null 2>&1; then
    echo "activated site but previous image is not locally resolvable; transaction remains protected" >&2
    return 1
  fi
  if ! atomic_site_reference "$site_root/previous-image" "$old"; then
    echo "activated site but could not record previous-image; transaction remains protected" >&2
    return 1
  fi
  if [[ -e "$site_root/retention-first-install" ]] && ! rm -f "$site_root/retention-first-install"; then
    echo "activated site but could not remove first-install marker; transaction remains protected" >&2
    return 1
  fi
  if ! record_history "$old" "$candidate" activated "$timestamp"; then
    echo "activated site but could not record deployment history; transaction remains protected" >&2
    return 1
  fi
  if ! clear_transaction_scratch "$old" "$candidate"; then
    echo "activated site but could not clear transaction scratch files; transaction remains protected" >&2
    return 1
  fi
  if ! rm -f "$site_root/deployment-in-progress"; then
    echo "activated site but could not clear transaction record" >&2
    return 1
  fi
  return 0
}

recover_pending_transaction() {
  local transaction="$site_root/deployment-in-progress"
  [[ -e "$transaction" ]] || return 0
  local version="" old="" candidate="" phase="" key value line
  local version_count=0 old_count=0 candidate_count=0 phase_count=0
  while IFS= read -r line; do
    key="${line%%=*}"
    value="${line#*=}"
    case "$key" in
      version) version="$value"; ((version_count += 1)) ;;
      previous_image) old="$value"; ((old_count += 1)) ;;
      candidate_image) candidate="$value"; ((candidate_count += 1)) ;;
      phase) phase="$value"; ((phase_count += 1)) ;;
      *) echo "deployment-in-progress is malformed" >&2; return 65 ;;
    esac
  done <"$transaction"
  if [[ "$version_count" -ne 1 || "$old_count" -ne 1 || "$candidate_count" -ne 1 || "$phase_count" -ne 1 || "$version" != 1 || ! "$old" =~ $immutable_site_reference || ! "$candidate" =~ $immutable_site_reference || "$old" == "$candidate" || ( "$phase" != prepared && "$phase" != activating && "$phase" != rollback-started && "$phase" != rollback-verified && "$phase" != rollback-failed && "$phase" != activated ) ]]; then
    echo "deployment-in-progress has invalid identities" >&2
    return 65
  fi
  read_site_env || { echo "cannot recover malformed deployment.env" >&2; return 65; }
  local deployed=""
  if [[ -f "$site_root/deployed-image" ]]; then
    if ! read_site_reference "$site_root/deployed-image" "deployed-image"; then
      return 65
    fi
    deployed="$REFERENCE_VALUE"
  fi
  if [[ ! "$deployed" =~ $immutable_site_reference || ( "$LEAPVIEW_SITE_IMAGE" != "$old" && "$LEAPVIEW_SITE_IMAGE" != "$candidate" ) || ( "$deployed" != "$old" && "$deployed" != "$candidate" ) ]]; then
    echo "deployment state contradicts deployment-in-progress; preserving recovery images" >&2
    return 65
  fi
  case "$phase" in
    prepared)
      if [[ "$deployed" != "$old" ]]; then
        echo "prepared deployment has an incompatible deployed-image" >&2
        return 65
      fi
      ;;
    activating)
      if [[ "$LEAPVIEW_SITE_IMAGE" != "$candidate" ]]; then
        echo "activating deployment has an incompatible deployment.env" >&2
        return 65
      fi
      ;;
    rollback-failed|rollback-verified)
      if [[ "$LEAPVIEW_SITE_IMAGE" != "$old" ]]; then
        echo "rollback phase has an incompatible deployment.env" >&2
        return 65
      fi
      if [[ "$phase" == rollback-verified && "$deployed" != "$old" ]]; then
        echo "verified rollback has an incompatible deployed-image" >&2
        return 65
      fi
      ;;
    activated)
      if [[ "$LEAPVIEW_SITE_IMAGE" != "$candidate" || "$deployed" != "$candidate" ]]; then
        echo "activated deployment has incompatible image evidence" >&2
        return 65
      fi
      ;;
  esac

  local timestamp
  timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
  if [[ "$LEAPVIEW_SITE_IMAGE" == "$candidate" && "$deployed" == "$candidate" ]]; then
    if ! finish_successful_transaction "$old" "$candidate" "$timestamp"; then
      return 74
    fi
    echo "completed interrupted activation of $candidate"
    return 0
  fi
  if [[ "$phase" == "prepared" && "$LEAPVIEW_SITE_IMAGE" == "$old" && "$deployed" == "$old" ]]; then
    clear_transaction_scratch "$old" "$candidate" || return 74
    rm -f "$transaction" || return 74
    echo "cleared deployment that stopped before activation"
    return 0
  fi

  if [[ "$LEAPVIEW_SITE_IMAGE" != "$old" ]]; then
    set_site_env_reference "$old" || return 74
  fi
  if ! atomic_transaction "$old" "$candidate" rollback-started; then
    echo "could not persist rollback recovery phase" >&2
    return 74
  fi
  if "$site_root/provision.sh"; then
    if ! clear_transaction_scratch "$old" "$candidate"; then
      echo "rollback is verified but transaction scratch files could not be cleared" >&2
      return 74
    fi
    if ! rm -f "$transaction"; then
      echo "rollback is verified but transaction record could not be removed" >&2
      return 74
    fi
    echo "restored and verified $old after interrupted deployment"
    return 0
  else
    atomic_transaction "$old" "$candidate" rollback-failed || true
    echo "could not verify recovery of $old; candidate and rollback remain protected" >&2
    return 77
  fi
}

if recover_pending_transaction; then
  :
else
  status=$?
  exit "$status"
fi

if [[ "$recover_only" == true ]]; then
  exit 0
fi

read_site_env || { echo "deployment.env is malformed" >&2; exit 65; }
previous_image="$LEAPVIEW_SITE_IMAGE"
deployed_image=""
if [[ -f "$site_root/deployed-image" ]]; then
  if ! read_site_reference "$site_root/deployed-image" "deployed-image"; then
    exit 65
  fi
  deployed_image="$REFERENCE_VALUE"
fi

# Direct operator deployment and pull reconciliation share pre-pull cleanup.
if run_retention apply --candidate-ref "$candidate_image"; then
  :
else
  retention_status=$?
  exit "$retention_status"
fi

if [[ "$candidate_image" == "$previous_image" && "$candidate_image" == "$deployed_image" ]]; then
  echo "site image is already active; previous-image remains $previous_image"
  exit 0
fi

if docker pull "$candidate_image"; then
  :
else
  echo "could not fetch site candidate; active deployment is unchanged and the digest remains retryable" >&2
  exit 74
fi

if run_retention apply --candidate-ref "$candidate_image" --require-candidate; then
  :
else
  retention_status=$?
  echo "candidate was fetched but activation is deferred until retention/headroom checks pass" >&2
  exit "$retention_status"
fi
if ! docker image inspect "$candidate_image" >/dev/null; then
  echo "pulled candidate is not locally resolvable; refusing activation" >&2
  exit 69
fi

timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
rollback_env="$(mktemp "$site_root/deployment.env.rollback.$timestamp.XXXXXX")" || exit 74
next_env="$(mktemp "$site_root/deployment.env.next.XXXXXX")" || exit 74
if ! cp "$site_root/deployment.env" "$rollback_env" || ! chmod 0600 "$rollback_env"; then
  rm -f "$rollback_env" "$next_env"
  exit 74
fi
if ! awk -v image="$candidate_image" '
  BEGIN { found = 0 }
  /^LEAPVIEW_SITE_IMAGE=/ { print "LEAPVIEW_SITE_IMAGE=" image; found++; next }
  /^CADDY_IMAGE=/ { print; next }
  { exit 42 }
  END { if (found != 1) exit 42 }
' "$site_root/deployment.env" >"$next_env"; then
  rm -f "$next_env"
  echo "deployment.env is malformed" >&2
  exit 65
fi
if ! chmod 0600 "$next_env"; then
  rm -f "$next_env"
  exit 74
fi
if ! atomic_transaction "$previous_image" "$candidate_image" prepared; then
  rm -f "$next_env"
  echo "could not persist deployment recovery record; active deployment is unchanged" >&2
  exit 74
fi
if ! mv -f "$next_env" "$site_root/deployment.env"; then
  echo "could not select candidate; recovery record retained" >&2
  exit 74
fi
if ! atomic_transaction "$previous_image" "$candidate_image" activating; then
  echo "could not persist activation phase; recovery record retained" >&2
  exit 74
fi

if "$site_root/provision.sh"; then
  if ! finish_successful_transaction "$previous_image" "$candidate_image" "$timestamp"; then
    exit 74
  fi
  echo "activated $candidate_image"
  if run_retention apply --candidate-ref "$candidate_image" --require-candidate; then
    :
  else
    cleanup_status=$?
    echo "site is healthy, but post-activation image cleanup failed (status $cleanup_status); retryable maintenance warning" >&2
  fi
  exit 0
else
  candidate_status=$?
fi

echo "candidate failed qualification; restoring $previous_image" >&2
if ! atomic_transaction "$previous_image" "$candidate_image" rollback-started; then
  echo "candidate failed and rollback state could not be recorded; recovery images remain protected" >&2
  exit 77
fi
restore_env="$(mktemp "$site_root/deployment.env.restore.XXXXXX")" || exit 77
if ! cp "$rollback_env" "$restore_env" || ! chmod 0600 "$restore_env" || ! mv -f "$restore_env" "$site_root/deployment.env"; then
  rm -f "$restore_env"
  atomic_transaction "$previous_image" "$candidate_image" rollback-failed || true
  echo "candidate failed; deployment.env restoration failed" >&2
  exit 77
fi
if "$site_root/provision.sh"; then
  if [[ "$candidate_status" -eq 76 ]]; then
    failure_result="failed-rolled-back"
  else
    failure_result="retryable-rolled-back"
  fi
  if ! record_history "$previous_image" "$candidate_image" "$failure_result" "$timestamp"; then
    atomic_transaction "$previous_image" "$candidate_image" rollback-verified || true
    echo "rollback is healthy but history recording failed; recovery record retained" >&2
    exit 77
  fi
  if ! clear_transaction_scratch "$previous_image" "$candidate_image"; then
    echo "rollback is healthy but transaction scratch files could not be cleared" >&2
    exit 77
  fi
  if ! rm -f "$site_root/deployment-in-progress"; then
    echo "rollback is healthy but transaction record could not be removed" >&2
    exit 77
  fi
  echo "restored $previous_image after candidate failure" >&2
  if [[ "$candidate_status" -eq 76 ]]; then
    exit 76
  fi
  echo "candidate activation failed for a retryable runtime reason (status $candidate_status)" >&2
  exit 74
else
  atomic_transaction "$previous_image" "$candidate_image" rollback-failed || true
  echo "candidate and rollback qualification both failed; retaining both images for recovery" >&2
  exit 77
fi
