#!/usr/bin/env bash
set -euo pipefail

demo_host="${DEMO_HOST:?Set DEMO_HOST}"
firewall_id="${DEMO_FIREWALL_ID:?Set DEMO_FIREWALL_ID}"
hcloud_token="${HCLOUD_TOKEN:?Set HCLOUD_TOKEN}"
runner_ip="${DEMO_RUNNER_IP:?Set DEMO_RUNNER_IP}"
expected_fingerprint="${DEMO_EXPECTED_SSH_FINGERPRINT:?Set DEMO_EXPECTED_SSH_FINGERPRINT}"
temporary_directory="$(mktemp -d)"
original_firewall_rules="$temporary_directory/original-firewall-rules.json"
firewall_changed=false

for command in curl jq ssh ssh-keygen ssh-keyscan; do
  command -v "$command" >/dev/null || { echo "required command is unavailable: $command" >&2; exit 69; }
done

hcloud_request() {
  local method="$1" path="$2" body="${3:-}"
  local arguments=(--fail --silent --show-error --request "$method" --header "Authorization: Bearer $hcloud_token" --header 'Content-Type: application/json' "https://api.hetzner.cloud/v1$path")
  if [[ -n "$body" ]]; then arguments+=(--data-binary "@$body"); fi
  curl "${arguments[@]}"
}

wait_hcloud_action() {
  local action_id="$1" response status
  for _ in $(seq 1 60); do
    response="$(hcloud_request GET "/actions/$action_id")"
    status="$(jq -r '.action.status' <<<"$response")"
    case "$status" in
      success) return 0 ;;
      error) jq -r '.action.error.message // "Hetzner action failed"' <<<"$response" >&2; return 1 ;;
    esac
    sleep 1
  done
  echo "Hetzner action $action_id did not complete" >&2
  return 1
}

set_firewall_rules() {
  local payload="$1" response action_id
  response="$(hcloud_request POST "/firewalls/$firewall_id/actions/set_rules" "$payload")"
  while IFS= read -r action_id; do wait_hcloud_action "$action_id"; done < <(jq -er '[.action.id?, .actions[]?.id?] | map(select(type == "number")) | unique | .[]' <<<"$response")
}

cleanup() {
  local status=$?
  if [[ "$firewall_changed" == true && -s "$original_firewall_rules" ]]; then
    local restore_payload="$temporary_directory/restore-firewall.json"
    jq '{rules: .}' "$original_firewall_rules" >"$restore_payload"
    if ! set_firewall_rules "$restore_payload"; then
      echo 'warning: failed to restore the demo firewall rules' >&2
      status=1
    fi
  fi
  rm -rf "$temporary_directory"
  exit "$status"
}
trap cleanup EXIT

firewall="$(hcloud_request GET "/firewalls/$firewall_id")"
jq '.firewall.rules' <<<"$firewall" >"$original_firewall_rules"
runner_cidr="$runner_ip/32"
if ! jq -e --arg cidr "$runner_cidr" 'any(.[]; .direction == "in" and .protocol == "tcp" and .port == "22" and any(.source_ips[]?; . == $cidr))' "$original_firewall_rules" >/dev/null; then
  open_payload="$temporary_directory/open-firewall.json"
  jq --arg cidr "$runner_cidr" '{rules: (. + [{direction: "in", protocol: "tcp", port: "22", source_ips: [$cidr], description: "Temporary hosted-demo auth diagnosis"}])}' "$original_firewall_rules" >"$open_payload"
  firewall_changed=true
  set_firewall_rules "$open_payload"
fi

identity_file="$temporary_directory/operator-identity"
printf '%s\n' "${DEMO_SSH_PRIVATE_KEY:?Set DEMO_SSH_PRIVATE_KEY}" >"$identity_file"
chmod 0600 "$identity_file"
unset DEMO_SSH_PRIVATE_KEY

scanned_keys="$temporary_directory/scanned-host-keys"
pinned_known_hosts="$temporary_directory/known-hosts"
ssh-keyscan -T 10 "$demo_host" >"$scanned_keys" 2>"$temporary_directory/ssh-keyscan.log"
while IFS= read -r scanned_key; do
  [[ -n "$scanned_key" && "$scanned_key" != \#* ]] || continue
  scanned_key_file="$temporary_directory/scanned-host-key"
  printf '%s\n' "$scanned_key" >"$scanned_key_file"
  actual_fingerprint="$(ssh-keygen -lf "$scanned_key_file" 2>/dev/null | awk '{print $2}')"
  if [[ "$actual_fingerprint" == "$expected_fingerprint" ]]; then
    printf '%s\n' "$scanned_key" >"$pinned_known_hosts"
    break
  fi
done <"$scanned_keys"
[[ -s "$pinned_known_hosts" ]] || { echo 'demo server host key did not match the reviewed fingerprint' >&2; exit 1; }

ssh -i "$identity_file" -o BatchMode=yes -o ConnectTimeout=10 \
  -o ServerAliveInterval=15 -o ServerAliveCountMax=20 \
  -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=$pinned_known_hosts" \
  "root@$demo_host" bash -se <<'REMOTE'
set -euo pipefail
container="$(docker ps --format '{{.Names}}' | awk '/-demo-current-postgres-1$/ { print }')"
[[ -n "$container" && "$(wc -l <<<"$container")" -eq 1 ]] || { echo 'expected one hosted-demo PostgreSQL container' >&2; exit 1; }

available_kb="$(df --output=avail / | tail -1 | tr -d ' ')"
if (( available_kb < 7340032 )); then
  echo 'Reclaiming regenerable developer caches before the next verified rollout'
  for cache in /home/ganesh/.cache/go-build /home/ganesh/.cache/leapview/dev-assets /home/ganesh/.cache/leapview/ci-duckdb-extensions /home/ganesh/.cache/apigen/typespec; do
    if [[ -d "$cache" && ! -L "$cache" ]]; then
      du -sh "$cache"
      rm -rf -- "$cache"
    fi
  done
  df -h /
fi

available_kb="$(df --output=avail / | tail -1 | tr -d ' ')"
if (( available_kb < 1048576 )); then
  echo 'Hosted demo disk is critically full; reclaiming regenerable caches and unused Docker data'
  if [[ -d /root/.cache/go-build && ! -L /root/.cache/go-build ]]; then
    du -sh /root/.cache/go-build || true
    rm -rf /root/.cache/go-build/*
  fi
  if [[ -d /root/go/pkg/mod && ! -L /root/go/pkg/mod ]]; then
    du -sh /root/go/pkg/mod || true
    rm -rf /root/go/pkg/mod/*
  fi
  docker builder prune --force
  docker image prune --force
  df -h /
  available_kb="$(df --output=avail / | tail -1 | tr -d ' ')"
  if (( available_kb < 1048576 )); then
    echo 'Inspecting and reclaiming package caches only:'
    du -h --max-depth=2 /root/.bun /root/.cache 2>/dev/null | sort -h | tail -30 || true
    for cache in /root/.bun/install/cache /root/.cache/pip /root/.cache/uv /root/.cache/bun /root/.npm/_cacache /root/.cache/leapview/dev-assets /root/.cache/leapview/ci-duckdb-extensions /root/.cache/apigen/typespec; do
      if [[ -d "$cache" && ! -L "$cache" ]]; then
        du -sh "$cache"
        rm -rf -- "$cache"
      fi
    done
    for cache in /tmp/leapview-main/node_modules /tmp/leapview-main/.demo-image-context /tmp/leapview-chat-ui-*/node_modules; do
      if [[ -d "$cache" && ! -L "$cache" ]]; then
        du -sh "$cache"
        rm -rf -- "$cache"
      fi
    done
    df -h /
    available_kb="$(df --output=avail / | tail -1 | tr -d ' ')"
  fi
  if (( available_kb < 262144 )); then
    echo 'Largest top-level paths after safe cache cleanup:'
    du -x -h --max-depth=1 /opt /var /root 2>/dev/null | sort -h | tail -30
    echo 'Root filesystem accounting:'
    du -x -h --max-depth=1 / 2>/dev/null | sort -h | tail -22
    echo 'Temporary directories and legacy installation:'
    du -x -h --max-depth=1 /tmp /opt/leapview 2>/dev/null | sort -h | tail -35
    echo 'Open deleted files:'
    lsof +L1 2>/dev/null | awk 'NR == 1 || $7 > 104857600 {print $1, $2, $7, $9}' | head -20 || true
    echo 'Docker disk accounting:'
    docker system df || true
    echo 'Journal disk accounting:'
    journalctl --disk-usage || true
    exit 1
  fi
fi

for _ in $(seq 1 60); do
  if [[ "$(docker inspect --format '{{.State.Running}} {{.State.Restarting}}' "$container")" == 'true false' ]]; then
    break
  fi
  sleep 1
done
if [[ "$(docker inspect --format '{{.State.Running}} {{.State.Restarting}}' "$container")" != 'true false' ]]; then
  docker inspect --format 'database state: status={{.State.Status}} restarting={{.State.Restarting}} oomKilled={{.State.OOMKilled}} exitCode={{.State.ExitCode}} error={{json .State.Error}} restartCount={{.RestartCount}}' "$container"
  df -h / /opt
  docker logs --tail 80 "$container" 2>&1
  exit 1
fi

query=$(cat <<'SQL'
WITH shared AS (
  SELECT p.id, p.status, p.disabled_at, p.blocked_at, p.revoked_at,
         c.principal_id AS credential_id, c.must_change, c.revoked_at AS credential_revoked_at
  FROM access.principal p
  LEFT JOIN access.local_credential c ON c.principal_id = p.id
  WHERE lower(p.email) = 'demo@leapview.dev'
), latest_denied AS (
  SELECT lower(metadata->>'email') AS email, occurred_at
  FROM audit.audit_event
  WHERE action = 'sign_in' AND outcome = 'denied' AND metadata->>'provider' = 'local'
  ORDER BY occurred_at DESC
  LIMIT 1
)
SELECT json_build_object(
  'sharedPrincipalExists', EXISTS (SELECT 1 FROM shared),
  'sharedPrincipalActive', EXISTS (SELECT 1 FROM shared WHERE status = 'active' AND disabled_at IS NULL AND blocked_at IS NULL AND revoked_at IS NULL),
  'sharedCredentialActive', EXISTS (SELECT 1 FROM shared WHERE credential_id IS NOT NULL AND credential_revoked_at IS NULL),
  'sharedMustChangePassword', COALESCE((SELECT must_change FROM shared LIMIT 1), false),
  'latestDeniedExists', EXISTS (SELECT 1 FROM latest_denied),
  'latestDeniedMatchesSharedEmail', COALESCE((SELECT email = 'demo@leapview.dev' FROM latest_denied), false),
  'latestDeniedAgeSeconds', COALESCE((SELECT floor(extract(epoch FROM (clock_timestamp() - occurred_at)))::bigint FROM latest_denied), -1),
  'deniedLocalSignInsLastHour', (SELECT count(*) FROM audit.audit_event WHERE action = 'sign_in' AND outcome = 'denied' AND metadata->>'provider' = 'local' AND occurred_at >= clock_timestamp() - interval '1 hour'),
  'successfulLocalSignInsLastDay', (SELECT count(*) FROM audit.audit_event WHERE action = 'sign_in' AND outcome = 'success' AND metadata->>'provider' = 'local' AND occurred_at >= clock_timestamp() - interval '1 day')
)::text;
SQL
)
docker exec "$container" sh -c 'exec psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d leapview_control -Atc "$1"' query "$query"

if ! curl --fail --silent --show-error --max-time 5 http://127.0.0.1:8132/readyz >/dev/null 2>&1; then
  echo 'Restarting the unhealthy application service after database recovery'
  systemctl restart leapview-demo-current.service
  for _ in $(seq 1 30); do
    if curl --fail --silent --show-error --max-time 5 http://127.0.0.1:8132/readyz >/dev/null 2>&1; then
      echo 'Application ready after database recovery'
      df -h /
      exit 0
    fi
    sleep 2
  done
  echo 'Application did not become ready after database recovery' >&2
  systemctl status leapview-demo-current.service --no-pager -l | tail -35 >&2 || true
  exit 1
fi
echo 'Application ready after database recovery'
echo 'Hosted-demo temporary/build disk footprint:'
du -x -h --max-depth=2 /tmp/leapview-main /tmp/leapview-chat-ui-* /tmp/leapview-demo-runtime /home 2>/dev/null | sort -h | tail -40 || true
echo 'Remaining development cache and temporary build footprint:'
du -x -h --max-depth=2 /home/ganesh/.cache /tmp/leapview-main/.tmp 2>/dev/null | sort -h | tail -35 || true
echo 'Temporary deployment scratch footprint:'
du -x -h --max-depth=1 /tmp/leapview-main/.tmp 2>/dev/null | sort -h | tail -20 || true
REMOTE
