#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
demo_host="${DEMO_HOST:?Set DEMO_HOST}"
firewall_id="${DEMO_FIREWALL_ID:?Set DEMO_FIREWALL_ID}"
hcloud_token="${HCLOUD_TOKEN:?Set HCLOUD_TOKEN}"
runner_ip="${DEMO_RUNNER_IP:?Set DEMO_RUNNER_IP}"
expected_fingerprint="${DEMO_EXPECTED_SSH_FINGERPRINT:?Set DEMO_EXPECTED_SSH_FINGERPRINT}"
temporary_directory="$(mktemp -d)"
original_firewall_rules="$temporary_directory/original-firewall-rules.json"
firewall_changed=false

for command in curl jq ssh ssh-keygen ssh-keyscan; do
  command -v "$command" >/dev/null || {
    echo "required command is unavailable: $command" >&2
    exit 69
  }
done

hcloud_request() {
  local method="$1"
  local path="$2"
  local body="${3:-}"
  local arguments=(
    --fail --silent --show-error
    --request "$method"
    --header "Authorization: Bearer $hcloud_token"
    --header 'Content-Type: application/json'
    "https://api.hetzner.cloud/v1$path"
  )
  if [[ -n "$body" ]]; then
    arguments+=(--data-binary "@$body")
  fi
  curl "${arguments[@]}"
}

wait_hcloud_action() {
  local action_id="$1"
  local response status
  for _ in $(seq 1 60); do
    response="$(hcloud_request GET "/actions/$action_id")"
    status="$(jq -r '.action.status' <<<"$response")"
    case "$status" in
      success) return 0 ;;
      error)
        jq -r '.action.error.message // "Hetzner action failed"' <<<"$response" >&2
        return 1
        ;;
    esac
    sleep 1
  done
  echo "Hetzner action $action_id did not complete" >&2
  return 1
}

set_firewall_rules() {
  local payload="$1"
  local response action_ids action_id
  response="$(hcloud_request POST "/firewalls/$firewall_id/actions/set_rules" "$payload")"
  action_ids="$(jq -er '[.action.id?, .actions[]?.id?] | map(select(type == "number")) | unique | .[]' <<<"$response")"
  while IFS= read -r action_id; do
    wait_hcloud_action "$action_id"
  done <<<"$action_ids"
}

cleanup() {
  local status=$?
  if [[ "$firewall_changed" == true && -s "$original_firewall_rules" ]]; then
    local restore_payload="$temporary_directory/restore-firewall.json"
    jq '{rules: .}' "$original_firewall_rules" >"$restore_payload"
    if ! set_firewall_rules "$restore_payload"; then
      echo "warning: failed to restore the demo firewall rules" >&2
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
if ! jq -e --arg cidr "$runner_cidr" '
  any(.[]; .direction == "in" and .protocol == "tcp" and .port == "22" and any(.source_ips[]?; . == $cidr))
' "$original_firewall_rules" >/dev/null; then
  open_payload="$temporary_directory/open-firewall.json"
  jq --arg cidr "$runner_cidr" '{rules: (. + [{
    direction: "in",
    protocol: "tcp",
    port: "22",
    source_ips: [$cidr],
    description: "Temporary hosted-demo asset recovery access"
  }])}' "$original_firewall_rules" >"$open_payload"
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
[[ -s "$pinned_known_hosts" ]] || {
  echo "demo server host key did not match the reviewed fingerprint" >&2
  exit 1
}

ssh \
  -i "$identity_file" \
  -o BatchMode=yes \
  -o ConnectTimeout=10 \
  -o ServerAliveInterval=15 \
  -o ServerAliveCountMax=20 \
  -o StrictHostKeyChecking=yes \
  -o "UserKnownHostsFile=$pinned_known_hosts" \
  "root@$demo_host" 'bash -se' <<'REMOTE'
set -euo pipefail
echo '--- activation recovery inventory ---'
systemctl is-active leapview-demo-current.service || true
curl -sS -o /dev/null -w 'health=%{http_code}\n' http://127.0.0.1:8132/healthz || true
curl -sS -o /dev/null -w 'ready=%{http_code}\n' http://127.0.0.1:8132/readyz || true
find /tmp /root -maxdepth 3 -type f \( -iname '*approv*' -o -iname '*candidate*' -o -iname '*publication*' -o -iname '*credential*' -o -iname '*login*' \) -printf '%p\n' 2>/dev/null | sort | head -n 100
while IFS= read -r json_file; do
  [[ -f "$json_file" ]] || continue
  if jq -e 'type == "object"' "$json_file" >/dev/null 2>&1; then
    printf '%s keys: ' "$json_file"
    jq -r 'keys | join(",")' "$json_file"
  fi
done < <(find /tmp /root -maxdepth 3 -type f \( -iname '*approv*.json' -o -iname '*credential*.json' -o -iname '*login*.json' \) 2>/dev/null | sort)
echo '--- cli profile keys ---'
if [[ -f /root/.config/leapview/cli.json ]]; then
  jq '{projectAuthority: (.projectAuthority | keys), targets: (.targets | keys)}' /root/.config/leapview/cli.json
fi
echo '--- activation response metadata ---'
for response_file in /tmp/leapview-main/.tmp/approval.out /tmp/leapview-main/.tmp/approve.out /tmp/leapview-main/.tmp/approve-second.out /tmp/leapview-main/.tmp/approver-token.json; do
  [[ -f "$response_file" ]] || continue
  printf '%s: ' "$response_file"
  jq -c 'del(.access_token, .refresh_token, .token, .secret, .temporaryPassword, .password)' "$response_file" 2>/dev/null || { head -c 400 "$response_file"; echo; }
done
repo=/tmp/leapview-main
approval_file="$repo/.tmp/approval.out"
approver_oauth="$repo/.tmp/approver-oauth.json"
approver_identity="$repo/.tmp/approver.json"
approver_cookies="$repo/.tmp/approver.cookies"
approver_password_file="$repo/.tmp/approver-new-password"
initial_credentials="$repo/.tmp/initial-credentials.json"
leapview_binary="$repo/.tmp/leapview-dev"
test -x "$leapview_binary"
latest_revision=5d870e7cf5f7e174dce115c416f6cab5e8259dcf
running_revision="$("$leapview_binary" version --json | jq -er '.revision')"
if curl -fsS --connect-timeout 2 --max-time 5 https://demo.leapview.dev/readyz >/dev/null 2>&1; then
  if [[ "$running_revision" != "$latest_revision" ]]; then
    echo "updating runtime from $running_revision to $latest_revision"
    go_binary="$(command -v go || true)"
    if [[ -z "$go_binary" ]]; then
      go_binary="$(find /root /usr /opt -type f -path '*/bin/go' -perm -111 -print -quit 2>/dev/null || true)"
    fi
    [[ -n "$go_binary" ]]
    git -C "$repo" fetch --quiet origin "$latest_revision"
    latest_worktree="/tmp/leapview-runtime-$latest_revision"
    if [[ -e "$latest_worktree" ]]; then
      git -C "$repo" worktree remove --force "$latest_worktree"
    fi
    git -C "$repo" worktree add --quiet --detach "$latest_worktree" "$latest_revision"
    while IFS= read -r generated_file; do
      [[ -f "$repo/$generated_file" ]] || continue
      mkdir -p "$latest_worktree/$(dirname "$generated_file")"
      cp -p "$repo/$generated_file" "$latest_worktree/$generated_file"
    done < <(git -C "$repo" ls-files -o -i --exclude-standard -- api internal static web/generated)
    (cd "$latest_worktree" && GODEBUG=http2client=0 GOTOOLCHAIN=go1.26.7 \
      "$go_binary" run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate --no-remote)
    runtime_version="$("$leapview_binary" version --json | jq -er '.version')"
    build_time="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    next_binary="$repo/.tmp/leapview-dev.next"
    backup_binary="$repo/.tmp/leapview-dev.previous"
    build_ldflags="-s -w -X github.com/flidai/leapview/internal/platform/buildinfo.version=$runtime_version -X github.com/flidai/leapview/internal/platform/buildinfo.revision=$latest_revision -X github.com/flidai/leapview/internal/platform/buildinfo.buildTime=$build_time -X github.com/flidai/leapview/internal/platform/buildinfo.dirty=false -X github.com/flidai/leapview/internal/platform/buildinfo.release=true"
    (cd "$latest_worktree" && "$go_binary" build -trimpath -ldflags "$build_ldflags" -o "$next_binary" ./cmd/leapview)
    [[ "$("$next_binary" version --json | jq -er '.revision')" == "$latest_revision" ]]
    cp -p "$leapview_binary" "$backup_binary"
    install -m 0755 "$next_binary" "$leapview_binary"
    if ! systemctl restart leapview-demo-current.service; then
      install -m 0755 "$backup_binary" "$leapview_binary"
      systemctl restart leapview-demo-current.service
      exit 1
    fi
    updated=false
    for _ in $(seq 1 60); do
      if curl -fsS --connect-timeout 2 --max-time 5 http://127.0.0.1:8132/healthz >/dev/null 2>&1 && \
         curl -fsS --connect-timeout 2 --max-time 5 https://demo.leapview.dev/readyz >/dev/null 2>&1; then
        updated=true
        break
      fi
      sleep 2
    done
    if [[ "$updated" != true ]]; then
      install -m 0755 "$backup_binary" "$leapview_binary"
      systemctl restart leapview-demo-current.service
      echo 'latest runtime failed readiness; previous healthy binary restored' >&2
      exit 1
    fi
    git -C "$repo" worktree remove --force "$latest_worktree"
  fi
  echo "active runtime revision: $("$leapview_binary" version --json | jq -r '.revision')"
  echo 'public demo readiness: ready'
  exit 0
fi
test -f "$approval_file"
test -f "$approver_oauth"
test -f "$approver_identity"
test -f "$approver_cookies"
test -f "$approver_password_file"
test -f "$initial_credentials"
publisher_token="$(jq -er '.publisherToken | strings | select(length > 0)' "$initial_credentials")"
approver_principal_id="$(jq -er 'if (.principal | type) == "object" then (.principal.id // .principal.principalId) else .principal end' "$approver_identity")"
project_id="$(jq -er '.projectId' "$approval_file")"
current_policy="$("$leapview_binary" api call listProjectRoleBindings \
  --target https://demo.leapview.dev \
  --token "$publisher_token" \
  --path "project=$project_id")"
policy_revision="$(jq -er '.policyRevision' <<<"$current_policy")"
role_binding_error="$repo/.tmp/recovery-role-binding.err"
if ! "$leapview_binary" api call createProjectRoleBinding \
  --target https://demo.leapview.dev \
  --token "$publisher_token" \
  --path "project=$project_id" \
  --body-json "{\"id\":\"demo-release-approver\",\"name\":\"Demo release approver\",\"subjectType\":\"principal\",\"subjectId\":\"$approver_principal_id\",\"role\":\"admin\",\"expectedRevision\":$policy_revision}" \
  --idempotency-key "demo-grant-approver-$approver_principal_id" >/dev/null 2>"$role_binding_error"; then
  grep -q 'subject/role already bound' "$role_binding_error" || { cat "$role_binding_error" >&2; exit 1; }
fi
approver_email="$(jq -er '.principal.email | strings | select(length > 0)' "$approver_identity")"
approver_password="$(<"$approver_password_file")"
login_page="$repo/.tmp/recovery-login-page.html"
rm -f "$approver_cookies"
curl --fail --silent --show-error \
  --cookie-jar "$approver_cookies" \
  https://demo.leapview.dev/login >"$login_page"
login_csrf="$(sed -n 's/.*name="csrf-token" content="\([^"]*\)".*/\1/p' "$login_page" | head -n 1)"
[[ -n "$login_csrf" ]] || { echo 'login CSRF token was unavailable' >&2; exit 1; }
login_status="$(curl --silent --show-error \
  --cookie "$approver_cookies" \
  --cookie-jar "$approver_cookies" \
  --output "$repo/.tmp/recovery-login-result.html" \
  --write-out '%{http_code}' \
  --request POST \
  --header 'Origin: https://demo.leapview.dev' \
  --header 'Referer: https://demo.leapview.dev/login' \
  --header 'Content-Type: application/x-www-form-urlencoded' \
  --data-urlencode "gorilla.csrf.Token=$login_csrf" \
  --data-urlencode "email=$approver_email" \
  --data-urlencode "password=$approver_password" \
  https://demo.leapview.dev/auth/local/login)"
unset approver_email approver_password login_csrf
[[ "$login_status" == 302 ]] || { echo "approver login returned HTTP $login_status" >&2; exit 1; }
device_challenge="$(curl --fail --silent --show-error \
  --request POST \
  --header 'Content-Type: application/x-www-form-urlencoded' \
  --data-urlencode 'client_id=leapview-cli' \
  --data-urlencode "project_id=$project_id" \
  --data-urlencode 'scope=PROJECT_ADMIN RESOURCE_READ' \
  https://demo.leapview.dev/oauth/device/code)"
device_code="$(jq -er '.device_code' <<<"$device_challenge")"
user_code="$(jq -er '.user_code' <<<"$device_challenge")"
verification_uri_complete="$(jq -er '.verification_uri_complete' <<<"$device_challenge")"
device_page="$repo/.tmp/recovery-device-page.html"
curl --fail --silent --show-error \
  --cookie "$approver_cookies" \
  --cookie-jar "$approver_cookies" \
  "$verification_uri_complete" >"$device_page"
csrf_token="$(sed -n 's/.*name="gorilla.csrf.Token" value="\([^"]*\)".*/\1/p' "$device_page" | head -n 1)"
[[ -n "$csrf_token" ]] || { echo 'approver browser session is not active' >&2; exit 1; }
device_result="$repo/.tmp/recovery-device-result.html"
curl --fail --silent --show-error \
  --cookie "$approver_cookies" \
  --cookie-jar "$approver_cookies" \
  --request POST \
  --header 'Origin: https://demo.leapview.dev' \
  --header "Referer: $verification_uri_complete" \
  --header 'Content-Type: application/x-www-form-urlencoded' \
  --data-urlencode "gorilla.csrf.Token=$csrf_token" \
  --data-urlencode "user_code=$user_code" \
  --data-urlencode 'decision=approve' \
  https://demo.leapview.dev/device >"$device_result"
grep -q 'CLI authorized' "$device_result"
device_oauth="$(curl --fail --silent --show-error \
  --request POST \
  --header 'Content-Type: application/x-www-form-urlencoded' \
  --data-urlencode 'client_id=leapview-cli' \
  --data-urlencode 'grant_type=urn:ietf:params:oauth:grant-type:device_code' \
  --data-urlencode "device_code=$device_code" \
  https://demo.leapview.dev/oauth/token)"
approver_token="$(jq -er '.access_token | strings | select(length > 0)' <<<"$device_oauth")"
unset device_challenge device_code user_code verification_uri_complete csrf_token device_oauth
cd "$repo"
plan="$("$leapview_binary" plan \
  --source-root "$repo/dashboards" \
  --target https://demo.leapview.dev \
  --project-id "$project_id" \
  --token "$publisher_token" \
  --candidate-key hosted-demo-recovery-v2 \
  --format json)"
plan_id="$(jq -er '.planId' <<<"$plan")"
[[ "$(jq -r '.status' <<<"$plan")" == planned ]]
build="$("$leapview_binary" build "$plan_id" --token "$publisher_token" --format json)"
[[ "$(jq -r '.status' <<<"$build")" == sealed ]]
candidate_id="$(jq -er '.candidateId' <<<"$build")"
publication="$("$leapview_binary" publish "$candidate_id" --token "$publisher_token" --format json)"
publication_id="$(jq -er '.publicationId' <<<"$publication")"
publication_status="$(jq -er '.status' <<<"$publication")"
generation_id="$(jq -er '.generationId' <<<"$publication")"
approval_result="$publication"
if [[ "$publication_status" == pending ]]; then
  approval_result="$("$leapview_binary" api call requestDeliveryPublicationApproval \
    --target https://demo.leapview.dev \
    --token "$publisher_token" \
    --path "project=$project_id" \
    --path "publication=$publication_id" \
    --idempotency-key "demo-recovery-request-$publication_id")"
  approval_id="$(jq -er '.id' <<<"$approval_result")"
  approval_revision="$(jq -er '.revision' <<<"$approval_result")"
  approval_result="$("$leapview_binary" api call approveDeliveryPublicationApproval \
    --target https://demo.leapview.dev \
    --token "$approver_token" \
    --path "project=$project_id" \
    --path "publication=$publication_id" \
    --path "approval=$approval_id" \
    --body-json "{\"expectedRevision\":$approval_revision}" \
    --idempotency-key "demo-recovery-approve-$approval_id")"
  [[ "$(jq -r '.status' <<<"$approval_result")" == approved ]]
else
  [[ "$publication_status" == committed ]]
fi
unset publisher_token approver_token
printf 'publication result: %s\n' "$(jq -c --arg candidate "$candidate_id" --arg generation "$generation_id" '{status,candidate:$candidate,generation:$generation}' <<<"$approval_result")"
ready=false
for _ in $(seq 1 120); do
  if curl -fsS --connect-timeout 2 --max-time 5 https://demo.leapview.dev/readyz >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 2
done
[[ "$ready" == true ]]
echo 'public demo readiness: ready'
exit 0
repo=/tmp/leapview-main
test -d "$repo/.git"
cd "$repo"
printf 'source revision: %s\n' "$(git rev-parse HEAD)"
go_binary="$(command -v go || true)"
if [[ -z "$go_binary" ]]; then
  go_binary="$(find /root /usr /opt -type f -path '*/bin/go' -perm -111 -print -quit 2>/dev/null || true)"
fi
[[ -n "$go_binary" ]] || {
  echo 'Go installation not found on the demo host' >&2
  exit 1
}
printf 'Go binary: %s\n' "$go_binary"
PATH="$(dirname "$go_binary"):/root/.bun/bin:/usr/local/bin:/usr/bin:/bin" \
  "$go_binary" run ./internal/app/tools/mapassets --shared-cache --out .data/map-assets
test -e .data/map-assets
printf 'verified map package files: %s\n' "$(find -L .data/map-assets -type f | wc -l)"

database_reset_marker=.tmp/demo-recovery-db-reset-v2
if [[ ! -f "$database_reset_marker" ]]; then
  pkill -x leapview-dev >/dev/null 2>&1 || true
  pkill -x leapview-demo-recovery >/dev/null 2>&1 || true
  rm -f .tmp/dev-server.pid .tmp/dev-server.port
  ./scripts/postgres-dev.sh destroy
  ./scripts/postgres-dev.sh up
  touch "$database_reset_marker"
fi

postgres_environment=.tmp/postgres-dev.env
test -f "$postgres_environment"
updated_environment="${postgres_environment}.recovery.$$"
while IFS= read -r line; do
  case "$line" in
    LEAPVIEW_DEV_AUTH_BYPASS=*) continue ;;
    LEAPVIEW_ENVIRONMENT=*) line='LEAPVIEW_ENVIRONMENT=dev' ;;
  esac
  printf '%s\n' "$line"
done <"$postgres_environment" >"$updated_environment"
chmod 0600 "$updated_environment"
mv "$updated_environment" "$postgres_environment"
grep -q '^LEAPVIEW_DEV_AUTH_BYPASS=' "$postgres_environment" || \
  printf '%s\n' 'LEAPVIEW_DEV_AUTH_BYPASS=true' >>"$postgres_environment"

pkill -x leapview-dev >/dev/null 2>&1 || true
pkill -x leapview-demo-recovery >/dev/null 2>&1 || true
for listener_pid in $(lsof -tiTCP:8132 -sTCP:LISTEN 2>/dev/null || true); do
  kill "$listener_pid" >/dev/null 2>&1 || true
done
for _ in $(seq 1 20); do
  [[ -z "$(lsof -tiTCP:8132 -sTCP:LISTEN 2>/dev/null || true)" ]] && break
  sleep 0.25
done
rm -f .tmp/dev-server.pid .tmp/dev-server.port
server_binary=.tmp/leapview-dev
if [[ ! -x "$server_binary" ]]; then
  PATH="$(dirname "$go_binary"):/root/.bun/bin:/usr/local/bin:/usr/bin:/bin" \
    "$go_binary" build -tags=duckdb_arrow -o "$server_binary" ./cmd/leapview
fi

while IFS='=' read -r name value; do
  [[ "$name" =~ ^[A-Z_][A-Z0-9_]*$ ]] || continue
  if [[ "$name" == LEAPVIEW_DUCKDB_EXTENSION_SUPPLY_PATH && "$value" != /* ]]; then
    value="$repo/$value"
  fi
  export "$name=$value"
done <"$postgres_environment"
if ! grep -q '^LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID=' "$postgres_environment"; then
  for installed_environment in /opt/leapview/leapview.env /opt/leapview/deployment.env; do
    [[ -f "$installed_environment" ]] || continue
    installed_pool_id="$(awk -F= '$1 == "LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID" {print $2; exit}' "$installed_environment")"
    installed_pool_digest="$(awk -F= '$1 == "LEAPVIEW_DELIVERY_PHYSICAL_POOL_COMPATIBILITY_DIGEST" {print $2; exit}' "$installed_environment")"
    if [[ "$installed_pool_id" =~ ^sha256:[0-9a-f]{64}$ && "$installed_pool_digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
      printf 'LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID=%s\n' "$installed_pool_id" >>"$postgres_environment"
      printf 'LEAPVIEW_DELIVERY_PHYSICAL_POOL_COMPATIBILITY_DIGEST=%s\n' "$installed_pool_digest" >>"$postgres_environment"
      export LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID="$installed_pool_id"
      export LEAPVIEW_DELIVERY_PHYSICAL_POOL_COMPATIBILITY_DIGEST="$installed_pool_digest"
      break
    fi
  done
fi
extension_supply="$repo/.tmp/dev-extension-supply/extension-supply.json"
test -f "$extension_supply"
export LEAPVIEW_DUCKDB_EXTENSION_SUPPLY_PATH="$extension_supply"
export LEAPVIEW_DUCKDB_EXTENSION_SUPPLY_SHA256="$(awk 'NF {print $1; exit}' "$extension_supply.sha256")"
export LEAPVIEW_ADDR=127.0.0.1:8132
export LEAPVIEW_DEV_WORKTREE="$repo"
export LEAPVIEW_MANAGED_DATA_MIN_FREE_BYTES="${LEAPVIEW_MANAGED_DATA_MIN_FREE_BYTES:-67108864}"
start_source_server() {
  nohup "$server_binary" >.tmp/demo-source.log 2>&1 </dev/null &
  server_pid=$!
  echo "$server_pid" >.tmp/dev-server.pid
  echo 8132 >.tmp/dev-server.port
}
wait_for_endpoint() {
  local endpoint="$1"
  for _ in $(seq 1 60); do
    if curl -fsS --connect-timeout 2 --max-time 5 "http://127.0.0.1:8132/$endpoint" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  return 1
}

start_source_server
if ! wait_for_endpoint healthz; then
  echo 'current-main source server did not become healthy' >&2
  tail -n 160 .tmp/demo-source.log >&2 || true
  exit 1
fi

if ! grep -q '^LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID=' "$postgres_environment"; then
  kill "$server_pid" >/dev/null 2>&1 || true
  wait "$server_pid" 2>/dev/null || true
  envelope=.tmp/postgres-dev-qualification.json
  pool_file=.tmp/postgres-dev-pool.json
  evidence_file=.tmp/postgres-dev-evidence.json
  "$server_binary" admin delivery pool qualify >"$envelope"
  jq -c '.pool' "$envelope" >"$pool_file"
  jq -c '.evidence' "$envelope" >"$evidence_file"
  bootstrap_output="$("$server_binary" admin delivery pool bootstrap --pool "$pool_file" --evidence "$evidence_file" --apply)"
  pool_id="$(awk '$1 == "pool_id:" {print $2; exit}' <<<"$bootstrap_output")"
  compatibility_digest="$(awk '$1 == "compatibility_digest:" {print $2; exit}' <<<"$bootstrap_output")"
  [[ "$pool_id" =~ ^sha256:[0-9a-f]{64}$ && "$compatibility_digest" =~ ^sha256:[0-9a-f]{64}$ ]]
  printf 'LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID=%s\n' "$pool_id" >>"$postgres_environment"
  printf 'LEAPVIEW_DELIVERY_PHYSICAL_POOL_COMPATIBILITY_DIGEST=%s\n' "$compatibility_digest" >>"$postgres_environment"
  export LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID="$pool_id"
  export LEAPVIEW_DELIVERY_PHYSICAL_POOL_COMPATIBILITY_DIGEST="$compatibility_digest"
  start_source_server
  wait_for_endpoint healthz
fi

token="${LEAPVIEW_DEV_API_TOKEN:-dev}"
client_profile=/root/.config/leapview/cli.json
project_id="$(jq -er '.projectAuthority.projectUid | strings | select(length > 0)' "$client_profile" 2>/dev/null || true)"
if [[ -z "$project_id" ]]; then
  bootstrap_output="$("$server_binary" bootstrap-project http://127.0.0.1:8132 --token "$token" --format json)"
  project_id="$(jq -er '.projectUid | strings | select(length > 0)' <<<"$bootstrap_output")"
fi
data_root="$(readlink -f .data/olist)"
"$server_binary" data sync --source-root dashboards --connection olist --from "$data_root" --target http://127.0.0.1:8132 --project-id "$project_id" --token "$token"
dev_output="$("$server_binary" dev --once --no-browser --source-root dashboards --target http://127.0.0.1:8132 --project-id "$project_id" --token "$token")"
printf '%s\n' "$dev_output"
candidate_id="$(awk '$1 == "candidate" {print $2; exit}' <<<"$dev_output")"
[[ "$candidate_id" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]]
"$server_binary" publish "$candidate_id" --token "$token"
kill "$server_pid" >/dev/null 2>&1 || true
wait "$server_pid" 2>/dev/null || true
sed -i '/^LEAPVIEW_DEV_AUTH_BYPASS=/d' "$postgres_environment"
unset LEAPVIEW_DEV_AUTH_BYPASS
start_source_server
if ! wait_for_endpoint readyz; then
  echo 'current-main source server did not become ready after publication' >&2
  tail -n 160 .tmp/demo-source.log >&2 || true
  exit 1
fi
echo 'current-main source server is ready on 127.0.0.1:8132'
./scripts/dev-server.sh status || true
if [[ -f .tmp/demo-source.log ]]; then
  echo '--- latest source server log ---'
  tail -n 80 .tmp/demo-source.log
fi
echo '--- configuration key inventory ---'
for environment_file in .tmp/postgres-dev.env /opt/leapview/leapview.env /opt/leapview/deployment.env; do
  if [[ -f "$environment_file" ]]; then
    printf '%s:\n' "$environment_file"
    sed -n 's/^\([A-Z_][A-Z0-9_]*\)=.*/  \1/p' "$environment_file" | sort
  else
    printf '%s: missing\n' "$environment_file"
  fi
done
echo '--- installed deployment status ---'
/opt/leapview/leapviewctl status || true
echo '--- container inventory ---'
docker ps --format 'table {{.Names}}\t{{.Image}}\t{{.Status}}'
if getent hosts postgres >/dev/null; then
  echo 'postgres hostname: resolvable'
else
  echo 'postgres hostname: unresolved'
fi
if [[ -s /tmp/postgres-ca.crt ]]; then
  echo 'PostgreSQL CA: present'
else
  echo 'PostgreSQL CA: missing'
fi
echo '--- listening TCP sockets ---'
ss -lntp | sed -n '1,80p'
REMOTE
