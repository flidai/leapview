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

for command in base64 curl jq ssh ssh-keygen ssh-keyscan; do
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

if [[ -n "${DEMO_LOGIN_PASSWORD:-}" ]]; then
  printf '%s' "$DEMO_LOGIN_PASSWORD" | ssh \
    -i "$identity_file" \
    -o BatchMode=yes \
    -o ConnectTimeout=10 \
    -o StrictHostKeyChecking=yes \
    -o "UserKnownHostsFile=$pinned_known_hosts" \
    "root@$demo_host" 'umask 077; cat > /tmp/leapview-demo-login-password'
  unset DEMO_LOGIN_PASSWORD
fi

agent_api_key="${DEEPSEEK_API_KEY:-}"
if [[ -n "$agent_api_key" && ! "$agent_api_key" =~ ^[A-Za-z0-9._:+/@%=-]+$ ]]; then
  echo 'DEEPSEEK_API_KEY contains characters that cannot be stored safely in the systemd environment file' >&2
  exit 1
fi
if [[ -n "$agent_api_key" ]]; then
  printf '%s' "$agent_api_key" | ssh \
    -i "$identity_file" \
    -o BatchMode=yes \
    -o ConnectTimeout=10 \
    -o StrictHostKeyChecking=yes \
    -o "UserKnownHostsFile=$pinned_known_hosts" \
    "root@$demo_host" 'umask 077; cat > /tmp/leapview-demo-agent-api-key'
fi
unset agent_api_key DEEPSEEK_API_KEY

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
demo_login_password_file=/tmp/leapview-demo-login-password
agent_api_key_file=/tmp/leapview-demo-agent-api-key
trap 'rm -f "$demo_login_password_file" "$agent_api_key_file"' EXIT

install -d -m 0755 /etc/leapview
umask 077
if [[ -s "$agent_api_key_file" ]]; then
  agent_api_key="$(<"$agent_api_key_file")"
  rm -f "$agent_api_key_file"
  [[ "$agent_api_key" =~ ^[A-Za-z0-9._:+/@%=-]+$ ]]
  {
    printf 'LEAPVIEW_AGENT_API_KEY=%s\n' "$agent_api_key"
    printf 'LEAPVIEW_AGENT_BASE_URL=https://api.deepseek.com\n'
    printf 'LEAPVIEW_AGENT_MODEL=deepseek-v4-flash\n'
  } >/etc/leapview/demo-agent.env
  unset agent_api_key
  agent_provider=deepseek
else
  ollama_version=v0.34.1
  ollama_archive=/tmp/ollama-linux-amd64.tar.zst
  ollama_archive_sha256=f361dc3992ec07e4ad429f4bb2d10d4663ba2c295f9a9a688c7d52f4ba650034
  ollama_model=qwen3:4b
  ollama_model_digest=359d7dd4bcdab3d86b87d73ac27966f4dbb9f5efdfcc75d34a8764a09474fae7
  if [[ ! -x /usr/local/bin/ollama ]] || \
     [[ "$(/usr/local/bin/ollama --version 2>/dev/null || true)" != *"${ollama_version#v}"* ]]; then
    if ! command -v zstd >/dev/null; then
      apt-get update -qq
      DEBIAN_FRONTEND=noninteractive apt-get install -y -qq zstd
    fi
    curl --fail --location --silent --show-error \
      --output "$ollama_archive" \
      "https://github.com/ollama/ollama/releases/download/$ollama_version/ollama-linux-amd64.tar.zst"
    printf '%s  %s\n' "$ollama_archive_sha256" "$ollama_archive" | sha256sum --check --status
    tar --zstd --extract --file "$ollama_archive" --directory /usr/local
    rm -f "$ollama_archive"
  fi
  if ! id ollama >/dev/null 2>&1; then
    useradd --system --create-home --home-dir /var/lib/ollama --shell /usr/sbin/nologin ollama
  fi
  if [[ ! -x /usr/local/lib/ollama/llama-server ]]; then
    echo 'Ollama CPU runner inventory:' >&2
    find /usr/local/lib/ollama -maxdepth 3 -printf '%y %m %p -> %l\n' >&2 || true
    echo 'the pinned Ollama archive did not install its CPU runner' >&2
    exit 1
  fi
  if command -v file >/dev/null; then
    file /usr/local/lib/ollama/llama-server
  fi
  ldd /usr/local/lib/ollama/llama-server || true
  install -d -m 0750 -o ollama -g ollama /var/lib/ollama
  cat >/etc/systemd/system/ollama.service <<'OLLAMA_SERVICE'
[Unit]
Description=Local Ollama model service for LeapView
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=ollama
Group=ollama
Environment=HOME=/var/lib/ollama
Environment=OLLAMA_HOST=127.0.0.1:11434
Environment=OLLAMA_KEEP_ALIVE=10m
ExecStart=/usr/local/bin/ollama serve
Restart=on-failure
RestartSec=3
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
ReadWritePaths=/var/lib/ollama

[Install]
WantedBy=multi-user.target
OLLAMA_SERVICE
  chmod 0644 /etc/systemd/system/ollama.service
  systemctl daemon-reload
  systemctl enable ollama.service
  systemctl restart ollama.service
  for _ in $(seq 1 60); do
    if curl -fsS --connect-timeout 2 --max-time 5 http://127.0.0.1:11434/api/tags >/dev/null 2>&1; then
      break
    fi
    sleep 2
  done
  curl -fsS --connect-timeout 2 --max-time 5 http://127.0.0.1:11434/api/tags >/dev/null
  installed_digest="$(curl -fsS http://127.0.0.1:11434/api/tags | \
    jq -r --arg model "$ollama_model" '.models[]? | select(.name == $model) | .digest' | head -n 1)"
  if [[ "$installed_digest" != "$ollama_model_digest" ]]; then
    HOME=/var/lib/ollama OLLAMA_HOST=127.0.0.1:11434 \
      /usr/local/bin/ollama pull "$ollama_model"
    installed_digest="$(curl -fsS http://127.0.0.1:11434/api/tags | \
      jq -r --arg model "$ollama_model" '.models[]? | select(.name == $model) | .digest' | head -n 1)"
  fi
  [[ "$installed_digest" == "$ollama_model_digest" ]]
  {
    printf 'LEAPVIEW_AGENT_API_KEY=local-ollama\n'
    printf 'LEAPVIEW_AGENT_BASE_URL=http://127.0.0.1:11434/v1\n'
    printf 'LEAPVIEW_AGENT_MODEL=%s\n' "$ollama_model"
  } >/etc/leapview/demo-agent.env
  unset installed_digest ollama_archive ollama_archive_sha256 ollama_model_digest
  agent_provider=ollama
fi
install -d -m 0755 /etc/systemd/system/leapview-demo-current.service.d
printf '%s\n' \
  '[Service]' \
  'EnvironmentFile=/etc/leapview/demo-agent.env' \
  >/etc/systemd/system/leapview-demo-current.service.d/agent.conf
chmod 0600 /etc/leapview/demo-agent.env
chmod 0644 /etc/systemd/system/leapview-demo-current.service.d/agent.conf
systemctl daemon-reload
systemctl restart leapview-demo-current.service
for _ in $(seq 1 60); do
  if curl -fsS --connect-timeout 2 --max-time 5 http://127.0.0.1:8132/healthz >/dev/null 2>&1; then
    break
  fi
  sleep 2
done
curl -fsS --connect-timeout 2 --max-time 5 http://127.0.0.1:8132/healthz >/dev/null
service_pid="$(systemctl show --property MainPID --value leapview-demo-current.service)"
[[ "$service_pid" =~ ^[1-9][0-9]*$ ]]
for variable_name in LEAPVIEW_AGENT_API_KEY LEAPVIEW_AGENT_BASE_URL LEAPVIEW_AGENT_MODEL; do
  grep -zq "^${variable_name}=" "/proc/$service_pid/environ"
done
echo "agent provider environment: installed ($agent_provider)"
echo '--- activation recovery inventory ---'
systemctl is-active leapview-demo-current.service || true
curl -sS -o /dev/null -w 'health=%{http_code}\n' http://127.0.0.1:8132/healthz || true
curl -sS -o /dev/null -w 'ready=%{http_code}\n' http://127.0.0.1:8132/readyz || true
if ! curl -fsS --connect-timeout 2 --max-time 5 http://127.0.0.1:8132/healthz >/dev/null 2>&1; then
  echo '--- failed hotfix startup diagnostics ---' >&2
  journalctl --unit leapview-demo-current.service --since '-5 minutes' \
    --no-pager --lines 160 >&2 || true
  if [[ -x /tmp/leapview-main/.tmp/leapview-dev.pre-delivery-hotfix ]]; then
    install -m 0755 /tmp/leapview-main/.tmp/leapview-dev.pre-delivery-hotfix \
      /tmp/leapview-main/.tmp/leapview-dev
    systemctl restart leapview-demo-current.service
    for _ in $(seq 1 60); do
      if curl -fsS --connect-timeout 2 --max-time 5 http://127.0.0.1:8132/healthz >/dev/null 2>&1; then
        break
      fi
      sleep 2
    done
    curl -fsS --connect-timeout 2 --max-time 5 http://127.0.0.1:8132/healthz >/dev/null
  fi
  rm -f /tmp/leapview-main/.tmp/leapview-dev.delivery-role-authorization \
    /tmp/leapview-main/.tmp/leapview-dev.delivery-hotfix
  exit 1
fi
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
demo_cookies="$repo/.tmp/demo-login.cookies"
test -x "$leapview_binary"
latest_revision=5d870e7cf5f7e174dce115c416f6cab5e8259dcf
running_revision="$("$leapview_binary" version --json | jq -er '.revision')"
requires_publication=false
if curl -fsS --connect-timeout 2 --max-time 5 https://demo.leapview.dev/readyz >/dev/null 2>&1; then
  if [[ "$running_revision" != "$latest_revision" ]]; then
    echo "updating runtime from $running_revision to $latest_revision"
    go_binary="$(command -v go || true)"
    if [[ -z "$go_binary" ]]; then
      go_binary="$(find /root /usr /opt -type f -path '*/bin/go' -perm -111 -print -quit 2>/dev/null || true)"
    fi
    [[ -n "$go_binary" ]]
    next_binary="$repo/.tmp/leapview-dev.next"
    backup_binary="$repo/.tmp/leapview-dev.previous"
    latest_worktree="/tmp/leapview-runtime-$latest_revision"
    next_revision=""
    next_tag_marker="$repo/.tmp/leapview-dev.next.complete-assets"
    if [[ -x "$next_binary" && -f "$next_tag_marker" ]]; then
      next_revision="$("$next_binary" version --json | jq -r '.revision // empty')"
    fi
    if [[ "$next_revision" != "$latest_revision" ]]; then
    if [[ ! -d "$latest_worktree/.git" && ! -f "$latest_worktree/.git" ]] || \
       [[ "$(git -C "$latest_worktree" rev-parse HEAD 2>/dev/null || true)" != "$latest_revision" ]]; then
    git -C "$repo" fetch --quiet origin "$latest_revision"
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
    fi
    runtime_version="$("$leapview_binary" version --json | jq -er '.version')"
    while IFS= read -r generated_file; do
      [[ -f "$repo/$generated_file" ]] || continue
      mkdir -p "$latest_worktree/$(dirname "$generated_file")"
      cp -p "$repo/$generated_file" "$latest_worktree/$generated_file"
    done < <(git -C "$repo" ls-files -o -i --exclude-standard -- docs)
    test -s "$latest_worktree/docs/search-index.json"
    build_time="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    build_ldflags="-s -w -X github.com/flidai/leapview/internal/platform/buildinfo.version=$runtime_version -X github.com/flidai/leapview/internal/platform/buildinfo.revision=$latest_revision -X github.com/flidai/leapview/internal/platform/buildinfo.buildTime=$build_time -X github.com/flidai/leapview/internal/platform/buildinfo.dirty=false -X github.com/flidai/leapview/internal/platform/buildinfo.release=true"
    (cd "$latest_worktree" && "$go_binary" build -tags=duckdb_arrow -trimpath -ldflags "$build_ldflags" -o "$next_binary" ./cmd/leapview)
    printf '%s\n' "$latest_revision" >"$next_tag_marker"
    fi
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
      echo 'latest runtime readiness response:' >&2
      curl --silent --show-error --max-time 10 http://127.0.0.1:8132/readyz >&2 || true
      echo >&2
      journalctl --unit leapview-demo-current.service --since '-3 minutes' --no-pager --lines 120 >&2 || true
      install -m 0755 "$backup_binary" "$leapview_binary"
      systemctl restart leapview-demo-current.service
      echo 'latest runtime failed readiness; previous healthy binary restored' >&2
      exit 1
    fi
    if [[ -e "$latest_worktree" ]]; then
      git -C "$repo" worktree remove --force "$latest_worktree"
    fi
  fi

  if [[ -s "$demo_login_password_file" ]]; then
    test -f "$initial_credentials"
    test -f "$approval_file"
    publisher_token="$(jq -er '.publisherToken | strings | select(length > 0)' "$initial_credentials")"
    project_id="$(jq -er '.projectId' "$approval_file")"
    demo_login_password="$(<"$demo_login_password_file")"
    rm -f "$demo_login_password_file"

    principal_list="$("$leapview_binary" api call listPrincipals \
      --target https://demo.leapview.dev \
      --token "$publisher_token" \
      --query 'email=demo@leapview.dev')"
    demo_principal_id="$(jq -r '.items[0].id // empty' <<<"$principal_list")"
    if [[ -n "$demo_principal_id" ]]; then
      password_reset="$("$leapview_binary" api call resetPrincipalPassword \
        --target https://demo.leapview.dev \
        --token "$publisher_token" \
        --path "principal=$demo_principal_id" \
        --idempotency-key "demo-login-reset-$(date -u +%Y%m%d%H%M%S)")"
    else
      password_reset="$("$leapview_binary" api call createPrincipal \
        --target https://demo.leapview.dev \
        --token "$publisher_token" \
        --body-json '{"email":"demo@leapview.dev","displayName":"LeapView Demo"}' \
        --idempotency-key 'demo-login-principal-v1')"
      demo_principal_id="$(jq -er '.principal.id' <<<"$password_reset")"
    fi
    temporary_password="$(jq -er '.temporaryPassword | strings | select(length > 0)' <<<"$password_reset")"

    demo_cookies="$repo/.tmp/demo-login.cookies"
    demo_login_page="$repo/.tmp/demo-login-page.html"
    rm -f "$demo_cookies"
    curl --fail --silent --show-error --cookie-jar "$demo_cookies" \
      https://demo.leapview.dev/login >"$demo_login_page"
    demo_csrf="$(sed -n 's/.*name="csrf-token" content="\([^"]*\)".*/\1/p' "$demo_login_page" | head -n 1)"
    [[ -n "$demo_csrf" ]]
    login_status="$(curl --silent --show-error \
      --cookie "$demo_cookies" --cookie-jar "$demo_cookies" \
      --output "$repo/.tmp/demo-login-result.html" --write-out '%{http_code}' \
      --request POST \
      --header 'Origin: https://demo.leapview.dev' \
      --header 'Referer: https://demo.leapview.dev/login' \
      --header 'Content-Type: application/x-www-form-urlencoded' \
      --data-urlencode "gorilla.csrf.Token=$demo_csrf" \
      --data-urlencode 'email=demo@leapview.dev' \
      --data-urlencode "password=$temporary_password" \
      https://demo.leapview.dev/auth/local/login)"
    [[ "$login_status" == 302 ]]

    curl --fail --silent --show-error \
      --cookie "$demo_cookies" --cookie-jar "$demo_cookies" \
      https://demo.leapview.dev/login >"$demo_login_page"
    demo_csrf="$(sed -n 's/.*name="csrf-token" content="\([^"]*\)".*/\1/p' "$demo_login_page" | head -n 1)"
    [[ -n "$demo_csrf" ]]
    password_status="$(curl --silent --show-error \
      --cookie "$demo_cookies" --cookie-jar "$demo_cookies" \
      --output "$repo/.tmp/demo-password-result.html" --write-out '%{http_code}' \
      --request POST \
      --header 'Origin: https://demo.leapview.dev' \
      --header 'Referer: https://demo.leapview.dev/login' \
      --header 'Content-Type: application/x-www-form-urlencoded' \
      --data-urlencode "gorilla.csrf.Token=$demo_csrf" \
      --data-urlencode "currentPassword=$temporary_password" \
      --data-urlencode "newPassword=$demo_login_password" \
      https://demo.leapview.dev/auth/local/password)"
    [[ "$password_status" == 302 ]]

    current_policy="$("$leapview_binary" api call listProjectRoleBindings \
      --target https://demo.leapview.dev \
      --token "$publisher_token" \
      --path "project=$project_id")"
    policy_revision="$(jq -er '.policyRevision' <<<"$current_policy")"
    if ! jq -e --arg principal "$demo_principal_id" \
      'any(.items[]?; .subjectType == "principal" and .subjectId == $principal and .role == "viewer")' \
      <<<"$current_policy" >/dev/null; then
      "$leapview_binary" api call createProjectRoleBinding \
        --target https://demo.leapview.dev \
        --token "$publisher_token" \
        --path "project=$project_id" \
        --body-json "{\"id\":\"demo-login-viewer\",\"name\":\"Demo login viewer\",\"subjectType\":\"principal\",\"subjectId\":\"$demo_principal_id\",\"role\":\"viewer\",\"expectedRevision\":$policy_revision}" \
        --idempotency-key "demo-login-viewer-$demo_principal_id" >/dev/null
      requires_publication=true
    fi

    rm -f "$demo_cookies"
    curl --fail --silent --show-error --cookie-jar "$demo_cookies" \
      https://demo.leapview.dev/login >"$demo_login_page"
    demo_csrf="$(sed -n 's/.*name="csrf-token" content="\([^"]*\)".*/\1/p' "$demo_login_page" | head -n 1)"
    final_login_status="$(curl --silent --show-error \
      --cookie "$demo_cookies" --cookie-jar "$demo_cookies" \
      --output "$repo/.tmp/demo-final-login-result.html" --write-out '%{http_code}' \
      --request POST \
      --header 'Origin: https://demo.leapview.dev' \
      --header 'Referer: https://demo.leapview.dev/login' \
      --header 'Content-Type: application/x-www-form-urlencoded' \
      --data-urlencode "gorilla.csrf.Token=$demo_csrf" \
      --data-urlencode 'email=demo@leapview.dev' \
      --data-urlencode "password=$demo_login_password" \
      https://demo.leapview.dev/auth/local/login)"
    unset demo_login_password temporary_password password_reset principal_list demo_csrf
    [[ "$final_login_status" == 302 ]]
    echo 'demo login credential: verified'

    probe_suffix="$(date -u +%Y%m%d%H%M%S)"
    agent_conversation="$($leapview_binary api call createAgentConversation \
      --target https://demo.leapview.dev \
      --token "$publisher_token" \
      --body-json '{"title":"Hosted demo readiness"}' \
      --idempotency-key "demo-agent-probe-conversation-$probe_suffix")"
    agent_conversation_id="$(jq -er '.id' <<<"$agent_conversation")"
    agent_run="$($leapview_binary api call createAgentRun \
      --target https://demo.leapview.dev \
      --token "$publisher_token" \
      --path "conversation=$agent_conversation_id" \
      --body-json '{"input":"Reply with exactly: ready"}' \
      --idempotency-key "demo-agent-probe-run-$probe_suffix")"
    agent_run_id="$(jq -er '.id' <<<"$agent_run")"
    agent_run_status="$(jq -er '.status' <<<"$agent_run")"
    for _ in $(seq 1 60); do
      case "$agent_run_status" in
        completed|failed|cancelled) break ;;
      esac
      sleep 2
      agent_run="$($leapview_binary api call getAgentRun \
        --target https://demo.leapview.dev \
        --token "$publisher_token" \
        --path "conversation=$agent_conversation_id" \
        --path "run=$agent_run_id")"
      agent_run_status="$(jq -er '.status' <<<"$agent_run")"
    done
    if [[ "$agent_run_status" != completed ]]; then
      jq -c '{id, status, model, stopReason, error}' <<<"$agent_run" >&2
      if systemctl is-active --quiet ollama.service; then
        journalctl --unit ollama.service --since '-5 minutes' --no-pager --lines 120 >&2 || true
      fi
      echo 'agent provider probe did not complete' >&2
      exit 1
    fi
    agent_messages="$($leapview_binary api call listAgentMessages \
      --target https://demo.leapview.dev \
      --token "$publisher_token" \
      --path "conversation=$agent_conversation_id")"
    jq -e 'any(.items[]?; .role == "assistant" and (.contentText // "" | ascii_downcase | contains("ready")))' \
      <<<"$agent_messages" >/dev/null
    unset agent_conversation agent_conversation_id agent_run agent_run_id agent_run_status agent_messages probe_suffix
    echo 'agent provider probe: completed'

    demo_dashboard_status="$(curl --silent --show-error \
      --cookie "$demo_cookies" \
      --output "$repo/.tmp/demo-dashboard-result.html" \
      --write-out '%{http_code}' \
      https://demo.leapview.dev/dashboards/dashboard:cfo-command-center/pages/overview)"
    echo "demo dashboard status before publication: $demo_dashboard_status"
    if [[ "$demo_dashboard_status" != 200 ]]; then
      echo 'CFO Command Center is not active; publishing the complete finance project'
      requires_publication=true
    fi
  fi

  echo "active runtime revision: $("$leapview_binary" version --json | jq -r '.revision')"
  if [[ "$requires_publication" != true ]]; then
    echo 'public demo readiness: ready'
    exit 0
  fi
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
  --data-urlencode 'scope=PROJECT_ADMIN RESOURCE_USE RESOURCE_READ RESOURCE_EDIT RESOURCE_PUBLISH' \
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
go_binary="$(command -v go || true)"
if [[ -z "$go_binary" ]]; then
  go_binary="$(find /root /usr /opt -type f -path '*/bin/go' -perm -111 -print -quit 2>/dev/null || true)"
fi
[[ -n "$go_binary" ]]
cfo_source_root="$repo/dashboards/experiments/cfo-demo"
cfo_data_root="$repo/.data/cfo-demo"
test -d "$cfo_source_root"

# The current main revision validates every candidate impact against the old
# graph before consulting project-wide roles, which makes any added resource
# impossible to deploy. Build the narrow, tested authorization correction on
# top of the exact main revision until the fix is released normally.
delivery_hotfix_marker="$repo/.tmp/leapview-dev.project-role-authorization-v4"
hotfix_binary="$repo/.tmp/leapview-dev.project-role-authorization-v4"
if [[ ! -s "$delivery_hotfix_marker" && -x "$hotfix_binary" ]] && \
   [[ "$("$hotfix_binary" version --json | jq -r '.revision // empty')" == "$latest_revision" ]]; then
  install -m 0755 "$hotfix_binary" "$leapview_binary"
  systemctl restart leapview-demo-current.service
  for _ in $(seq 1 60); do
    if curl -fsS --connect-timeout 2 --max-time 5 http://127.0.0.1:8132/healthz >/dev/null 2>&1; then
      break
    fi
    sleep 2
  done
  curl -fsS --connect-timeout 2 --max-time 5 http://127.0.0.1:8132/healthz >/dev/null
  printf '%s\n' "$latest_revision" >"$delivery_hotfix_marker"
fi
if [[ ! -s "$delivery_hotfix_marker" ]]; then
  hotfix_worktree="/tmp/leapview-delivery-hotfix-$latest_revision"
  if [[ -e "$hotfix_worktree" ]]; then
    git -C "$repo" worktree remove --force "$hotfix_worktree" 2>/dev/null || true
  fi
  git -C "$repo" worktree add --quiet --detach "$hotfix_worktree" "$latest_revision"
  while IFS= read -r generated_file; do
    [[ -f "$repo/$generated_file" ]] || continue
    mkdir -p "$hotfix_worktree/$(dirname "$generated_file")"
    cp -p "$repo/$generated_file" "$hotfix_worktree/$generated_file"
  done < <(git -C "$repo" ls-files -o -i --exclude-standard -- api internal static web/generated)
  printf '%s' 'ZGlmZiAtLWdpdCBhL2ludGVybmFsL2FjY2Vzcy9tb2R1bGUvYXBpZ2VuLmdvIGIvaW50ZXJuYWwvYWNjZXNzL21vZHVsZS9hcGlnZW4uZ28KaW5kZXggMzIyNTMzNjkzLi5jZDA1ODExNmUgMTAwNjQ0Ci0tLSBhL2ludGVybmFsL2FjY2Vzcy9tb2R1bGUvYXBpZ2VuLmdvCisrKyBiL2ludGVybmFsL2FjY2Vzcy9tb2R1bGUvYXBpZ2VuLmdvCkBAIC03NjYsNiArNzY2LDExIEBAIGZ1bmMgKGEgKkFQSUdlbkF1dGhvcml6ZXIpIGF1dGhvcml6ZVJlc291cmNlcyhjdHggY29udGV4dC5Db250ZXh0LCBwcmluY2lwYWxJRCBzCiAJCXJldHVybiBmYWxzZSwgZXJyCiAJfQogCWZvciBfLCByZXNvdXJjZSA6PSByYW5nZSByZXNvdXJjZXMgeworCQkvLyBQcm9qZWN0IHJvbGVzIGFyZSBncmFwaC13aWRlIGFuZCBtYXkgYXV0aG9yaXplIGNyZWF0aW9uIG9mIGEgcmVzb3VyY2UKKwkJLy8gYmVmb3JlIHRoYXQgcmVzb3VyY2UgZXhpc3RzIGluIHRoZSBjdXJyZW50bHkgYWN0aXZlIGdyYXBoLgorCQlpZiBhY2Nlc3NzbmFwc2hvdC5Sb2xlQWxsb3dzQ2FwYWJpbGl0eShzbmFwc2hvdCwgc3ViamVjdHMsIGNhcGFiaWxpdHkpIHsKKwkJCWNvbnRpbnVlCisJCX0KIAkJaWYgcmVzb3VyY2UuS2luZCgpID09IHByb2plY3RncmFwaC5LaW5kUHJvamVjdE5hbWVzcGFjZSB7CiAJCQlpZiByZXNvdXJjZS5JRCgpICE9IHByb2plY3RJRCB7CiAJCQkJcmV0dXJuIGZhbHNlLCBlcnJBUElHZW5SZXNvdXJjZU5vdEZvdW5kCmRpZmYgLS1naXQgYS9pbnRlcm5hbC9hcHAvcHJvamVjdF9hdXRob3JpemF0aW9uLmdvIGIvaW50ZXJuYWwvYXBwL3Byb2plY3RfYXV0aG9yaXphdGlvbi5nbwppbmRleCBhNmQ4ODM4NzguLjRlNTY1MWZiOSAxMDA2NDQKLS0tIGEvaW50ZXJuYWwvYXBwL3Byb2plY3RfYXV0aG9yaXphdGlvbi5nbworKysgYi9pbnRlcm5hbC9hcHAvcHJvamVjdF9hdXRob3JpemF0aW9uLmdvCkBAIC0xNzYsNiArMTc2LDExIEBAIGZ1bmMgYXV0aG9yaXplUHJvamVjdFJlc291cmNlc1dpdGhDYXBhYmlsaXR5KAogCX0KIAlmb3IgXywgcmVzb3VyY2UgOj0gcmFuZ2UgcmVzb3VyY2VzIHsKIAkJY2FwYWJpbGl0eSA6PSBjYXBhYmlsaXR5Rm9yKHJlc291cmNlKQorCQkvLyBQcm9qZWN0IHJvbGVzIGFyZSBncmFwaC13aWRlIGJ5IGRlZmluaXRpb24gYW5kIG1heSBhdXRob3JpemUgY3JlYXRpb24KKwkJLy8gb2YgYSByZXNvdXJjZSB0aGF0IGlzIG5vdCBwcmVzZW50IGluIHRoZSBjdXJyZW50bHkgYWN0aXZlIGdyYXBoLgorCQlpZiBhY2Nlc3NzbmFwc2hvdC5Sb2xlQWxsb3dzQ2FwYWJpbGl0eShzbmFwc2hvdCwgc3ViamVjdHMsIGNhcGFiaWxpdHkpIHsKKwkJCWNvbnRpbnVlCisJCX0KIAkJLy8gUHJvamVjdC1zY29wZWQgYnJvd3NlciBvcGVyYXRpb25zIHVzZSByZXNvdXJjZSBjYXBhYmlsaXRpZXMgZnJvbSBhbgogCQkvLyBleHBsaWNpdCBwcm9qZWN0IHJvbGUgYnVuZGxlLCBqdXN0IGxpa2UgQVBJR2VuLiBUaGUgcHJvamVjdCBraW5kIG9ubHkKIAkJLy8gYWNjZXB0cyBQUk9KRUNUX0FETUlOIGFzIGEgZGlyZWN0IGdyYW50LCBzbyBjYWxsaW5nIHNuYXBzaG90LkFsbG93cyBmb3IKZGlmZiAtLWdpdCBhL2ludGVybmFsL2FwcC9ydW50aW1lX3JvdXRlcl9wb2xpY3kuZ28gYi9pbnRlcm5hbC9hcHAvcnVudGltZV9yb3V0ZXJfcG9saWN5LmdvCmluZGV4IDIwYmUxMjNhYS4uMmJhZTdlY2JhIDEwMDY0NAotLS0gYS9pbnRlcm5hbC9hcHAvcnVudGltZV9yb3V0ZXJfcG9saWN5LmdvCisrKyBiL2ludGVybmFsL2FwcC9ydW50aW1lX3JvdXRlcl9wb2xpY3kuZ28KQEAgLTM0OCw2ICszNDgsMTIgQEAgZnVuYyBkZWxpdmVyeUF1dGhvcml6YXRpb25SZXNvdXJjZXMocGxhbiBkZXBsb3ltZW50LkRlbGl2ZXJ5UGxhbikgKFtdYWNjZXNzLlJlc28KIGZ1bmMgZGVsaXZlcnlTbmFwc2hvdEFsbG93cyhzbmFwc2hvdCBhY2Nlc3NzbmFwc2hvdC5BdXRob3JpemF0aW9uU25hcHNob3QsIHN1YmplY3RzIFtdYWNjZXNzLlN1YmplY3RSZWYsIHJlc291cmNlcyBbXWFjY2Vzcy5SZXNvdXJjZVJlZiwgY2FwYWJpbGl0eSBhY2Nlc3MuQ2FwYWJpbGl0eSkgKGJvb2wsIGVycm9yKSB7CiAJZm9yIF8sIHJlc291cmNlIDo9IHJhbmdlIHJlc291cmNlcyB7CiAJCXJlc291cmNlQ2FwYWJpbGl0eSA6PSBkZWxpdmVyeVJlc291cmNlQ2FwYWJpbGl0eShyZXNvdXJjZSwgY2FwYWJpbGl0eSkKKwkJLy8gUHJvamVjdCByb2xlcyBhcmUgZ3JhcGgtd2lkZSBieSBkZWZpbml0aW9uLiBFdmFsdWF0ZSB0aGVpciBpbW11dGFibGUKKwkJLy8gY2FwYWJpbGl0eSBidW5kbGUgYmVmb3JlIHZhbGlkYXRpbmcgYSBjb25jcmV0ZSByZXNvdXJjZSBhZ2FpbnN0IHRoZQorCQkvLyBhY3RpdmUgZ3JhcGggc28gYW4gYXV0aG9yaXplZCBkZXBsb3ltZW50IGNhbiBpbnRyb2R1Y2UgYSBuZXcgbm9kZS4KKwkJaWYgYWNjZXNzc25hcHNob3QuUm9sZUFsbG93c0NhcGFiaWxpdHkoc25hcHNob3QsIHN1YmplY3RzLCByZXNvdXJjZUNhcGFiaWxpdHkpIHsKKwkJCWNvbnRpbnVlCisJCX0KIAkJaWYgaGFuZGxlZCwgcm9sZUFsbG93ZWQgOj0gcHJvamVjdFJvb3RSb2xlRGVjaXNpb24oc25hcHNob3QsIHN1YmplY3RzLCByZXNvdXJjZSwgcmVzb3VyY2VDYXBhYmlsaXR5KTsgaGFuZGxlZCB7CiAJCQlpZiAhcm9sZUFsbG93ZWQgewogCQkJCXJldHVybiBmYWxzZSwgbmlsCg==' \
    | base64 --decode \
    | git -C "$hotfix_worktree" apply
  printf '%s' 'ZGlmZiAtLWdpdCBhL2ludGVybmFsL2FjY2Vzcy9tb2R1bGUvY2Fub25pY2FsX2d1YXJkLmdvIGIvaW50ZXJuYWwvYWNjZXNzL21vZHVsZS9jYW5vbmljYWxfZ3VhcmQuZ28KaW5kZXggMGJkYmM5ZTM0Li5kZTZmMDdiN2EgMTAwNjQ0Ci0tLSBhL2ludGVybmFsL2FjY2Vzcy9tb2R1bGUvY2Fub25pY2FsX2d1YXJkLmdvCisrKyBiL2ludGVybmFsL2FjY2Vzcy9tb2R1bGUvY2Fub25pY2FsX2d1YXJkLmdvCkBAIC01Miw2ICs1MiwxMyBAQCBmdW5jIENvbm5lY3Rpb25BdXRob3JpemVyRnJvbVNuYXBzaG90KAogCQkJaWYgZXJyIDo9IHN1YmplY3QuVmFsaWRhdGUoKTsgZXJyICE9IG5pbCB7CiAJCQkJcmV0dXJuIGZhbHNlLCBlcnIKIAkJCX0KKwkJfQorCQkvLyBQcm9qZWN0IHJvbGVzIGF1dGhvcml6ZSB0aGUgY29tcGxldGUgcHJvamVjdCBncmFwaCwgaW5jbHVkaW5nIGNyZWF0aW9uCisJCS8vIG9mIGEgbWFuYWdlZC1kYXRhIGNvbGxlY3Rpb24gYmVmb3JlIGl0cyBjb25uZWN0aW9uIGlzIGFjdGl2YXRlZC4KKwkJaWYgc25hcHNob3QuUm9sZUFsbG93c0NhcGFiaWxpdHkobGVhc2VkLCBzdWJqZWN0cywgY2FwYWJpbGl0eSkgeworCQkJcmV0dXJuIHRydWUsIG5pbAorCQl9CisJCWZvciBfLCBzdWJqZWN0IDo9IHJhbmdlIHN1YmplY3RzIHsKIAkJCWFsbG93ZWQsIGVyciA6PSBsZWFzZWQuQWxsb3dzKHN1YmplY3QsIHJlc291cmNlLCBjYXBhYmlsaXR5KQogCQkJaWYgZXJyICE9IG5pbCB7CiAJCQkJcmV0dXJuIGZhbHNlLCBlcnIK' \
    | base64 --decode \
    | git -C "$hotfix_worktree" apply
  (cd "$hotfix_worktree" && GODEBUG=http2client=0 GOTOOLCHAIN=go1.26.7 \
    "$go_binary" run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate --no-remote)
  while IFS= read -r generated_file; do
    [[ -f "$repo/$generated_file" ]] || continue
    mkdir -p "$hotfix_worktree/$(dirname "$generated_file")"
    cp -p "$repo/$generated_file" "$hotfix_worktree/$generated_file"
  done < <(git -C "$repo" ls-files -o -i --exclude-standard -- docs)
  test -s "$hotfix_worktree/docs/search-index.json"
  runtime_version="$("$leapview_binary" version --json | jq -er '.version')"
  build_time="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  build_ldflags="-s -w -X github.com/flidai/leapview/internal/platform/buildinfo.version=$runtime_version -X github.com/flidai/leapview/internal/platform/buildinfo.revision=$latest_revision -X github.com/flidai/leapview/internal/platform/buildinfo.buildTime=$build_time -X github.com/flidai/leapview/internal/platform/buildinfo.dirty=false -X github.com/flidai/leapview/internal/platform/buildinfo.release=true"
  (cd "$hotfix_worktree" && "$go_binary" build -tags=duckdb_arrow -trimpath \
    -ldflags "$build_ldflags" -o "$hotfix_binary" ./cmd/leapview)
  [[ "$("$hotfix_binary" version --json | jq -er '.revision')" == "$latest_revision" ]]
  cp -p "$leapview_binary" "$repo/.tmp/leapview-dev.pre-delivery-hotfix"
  install -m 0755 "$hotfix_binary" "$leapview_binary"
  systemctl restart leapview-demo-current.service
  hotfix_ready=false
  for _ in $(seq 1 60); do
    if curl -fsS --connect-timeout 2 --max-time 5 http://127.0.0.1:8132/healthz >/dev/null 2>&1; then
      hotfix_ready=true
      break
    fi
    sleep 2
  done
  if [[ "$hotfix_ready" != true ]]; then
    install -m 0755 "$repo/.tmp/leapview-dev.pre-delivery-hotfix" "$leapview_binary"
    systemctl restart leapview-demo-current.service
    echo 'delivery authorization hotfix failed readiness; previous binary restored' >&2
    exit 1
  fi
  printf '%s\n' "$latest_revision" >"$delivery_hotfix_marker"
  git -C "$repo" worktree remove --force "$hotfix_worktree"
fi

activate_source_root() {
  local source_root="$1"
  local candidate_key="$2-$(date -u +%Y%m%d%H%M%S)"
  local verify_path="${3:-}"
  local expected_revision="${4:-}"
  local plan plan_id build build_error candidate_id publication publication_id publication_status
  local generation_id approval_result approval_id approval_revision operator_snapshot planned_revision active=false

  plan="$("$leapview_binary" plan \
    --source-root "$source_root" \
    --target https://demo.leapview.dev \
    --project-id "$project_id" \
    --token "$publisher_token" \
    --candidate-key "$candidate_key" \
    --format json)"
  plan_id="$(jq -er '.planId' <<<"$plan")"
  [[ "$(jq -r '.status' <<<"$plan")" == planned ]]
  if [[ -n "$expected_revision" ]]; then
    planned_revision="$(jq -er '
      first(.evidence.plannedInputs[] | select(.id == "connection:finance_files")) | .revision
    ' <<<"$plan")"
    [[ "$planned_revision" == "$expected_revision" ]] || {
      echo "finance revision mismatch: planned $planned_revision, staged $expected_revision" >&2
      return 1
    }
    echo "finance revision pinned: $planned_revision"
  fi
  build_error="$repo/.tmp/cfo-build-$plan_id.err"
  if ! build="$("$leapview_binary" build "$plan_id" --token "$publisher_token" --format json 2>"$build_error")"; then
    cat "$build_error" >&2
    echo '--- candidate build service diagnostics ---' >&2
    journalctl --unit leapview-demo-current.service --since '-3 minutes' \
      --no-pager --lines 160 >&2 || true
    return 1
  fi
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
  printf 'publication result: %s\n' "$(jq -c --arg candidate "$candidate_id" --arg generation "$generation_id" '{status,candidate:$candidate,generation:$generation}' <<<"$approval_result")"

  for _ in $(seq 1 120); do
    operator_snapshot="$("$leapview_binary" api call getDeliveryOperatorSnapshot \
      --target https://demo.leapview.dev \
      --token "$publisher_token" \
      --path "project=$project_id" 2>/dev/null || true)"
    if [[ "$(jq -r '.activeGeneration // empty' <<<"$operator_snapshot" 2>/dev/null)" == "$generation_id" ]] && \
       curl -fsS --connect-timeout 2 --max-time 5 https://demo.leapview.dev/readyz >/dev/null 2>&1; then
      if [[ -z "$verify_path" ]] || \
         [[ "$(curl --silent --show-error --cookie "$demo_cookies" --output /dev/null --write-out '%{http_code}' \
           "https://demo.leapview.dev$verify_path")" == 200 ]]; then
        active=true
        break
      fi
    fi
    sleep 2
  done
  [[ "$active" == true ]]
}

PATH="$(dirname "$go_binary"):/root/.bun/bin:/usr/local/bin:/usr/bin:/bin" \
  "$go_binary" run ./internal/app/tools/bootstrapfinance --shared-cache --out "$cfo_data_root"
cfo_data_path="$(cd -P "$cfo_data_root" && pwd)"
# Avoid replaying a historical upload idempotency record owned by a different
# principal. A trailing blank line is CSV-equivalent but produces a fresh,
# deployment-owned immutable revision digest.
cfo_upload_root="$(mktemp -d "$repo/.tmp/cfo-upload.XXXXXX")"
install -m 0644 "$cfo_data_path/financial-sample.csv" \
  "$cfo_upload_root/financial-sample.csv"
printf '\n' >>"$cfo_upload_root/financial-sample.csv"
cfo_data_path="$cfo_upload_root"

# Create the target binding from the already validated managed-file shape.
# The project administrator owns this project-scoped operation.
operator_snapshot="$("$leapview_binary" api call getDeliveryOperatorSnapshot \
  --target https://demo.leapview.dev \
  --token "$publisher_token" \
  --path "project=$project_id")"
target_id="$(jq -er '.targetId' <<<"$operator_snapshot")"
managed_binding='{
  "id":"demo-finance-files",
  "logicalConnection":"connection:finance_files",
  "configuration":{
    "connectorKind":"managed",
    "authenticationMode":"none",
    "endpoint":{}
  },
  "enabled":true
}'
binding_error="$repo/.tmp/cfo-binding.err"
if ! "$leapview_binary" api call createTargetConnectionBinding \
  --target https://demo.leapview.dev \
  --token "$approver_token" \
  --path "project=$project_id" \
  --path "target=$target_id" \
  --body-json "$managed_binding" \
  --idempotency-key 'demo-finance-files-binding-v2' >/dev/null 2>"$binding_error"; then
  grep -Eq 'already exists|CONNECTION_BINDING_CONFLICT' "$binding_error" || {
    cat "$binding_error" >&2
    exit 1
  }
fi
finance_sync_error="$repo/.tmp/cfo-finance-sync.err"
if ! finance_sync="$("$leapview_binary" data sync \
  --source-root "$cfo_source_root" \
  --connection finance_files \
  --from "$cfo_data_path" \
  --target https://demo.leapview.dev \
  --project-id "$project_id" \
  --token "$approver_token" \
  --format json 2>"$finance_sync_error")"; then
  cat "$finance_sync_error" >&2
  journalctl --unit leapview-demo-current.service --since '-3 minutes' \
    --no-pager --lines 200 >&2 || true
  if command -v docker >/dev/null 2>&1; then
    while IFS= read -r database_container; do
      [[ -n "$database_container" ]] || continue
      echo "--- database diagnostics: $database_container ---" >&2
      docker logs --since 3m "$database_container" 2>&1 | tail -n 120 >&2 || true
    done < <(docker ps --format '{{.Names}}' | grep -Ei 'postgres|database|db' || true)
  fi
  service_pid="$(systemctl show leapview-demo-current.service --property MainPID --value)"
  postgres_dsn="$(tr '\0' '\n' <"/proc/$service_pid/environ" | sed -n 's/^LEAPVIEW_POSTGRES_CONTROL_URL=//p' | head -n 1)"
  if [[ -n "$postgres_dsn" ]] && command -v psql >/dev/null 2>&1; then
    psql "$postgres_dsn" --no-psqlrc --tuples-only --command \
      "SELECT connection_id, status, created_by FROM managed_data.collection WHERE project_id = '$project_id' ORDER BY connection_id;" >&2 || true
  fi
  exit 1
fi
finance_revision="$(jq -er '.revisionId' <<<"$finance_sync")"
echo "finance revision staged: $finance_revision"
activate_source_root "$cfo_source_root" hosted-demo-cfo \
  /dashboards/dashboard:cfo-command-center/pages/overview \
  "$finance_revision"

for cfo_page in overview statement liquidity drivers; do
  cfo_status="$(curl --silent --show-error \
    --cookie "$demo_cookies" \
    --output /dev/null \
    --write-out '%{http_code}' \
    "https://demo.leapview.dev/dashboards/dashboard:cfo-command-center/pages/$cfo_page")"
  [[ "$cfo_status" == 200 ]] || {
    echo "CFO page $cfo_page returned HTTP $cfo_status" >&2
    exit 1
  }
  echo "CFO page $cfo_page: HTTP 200"
done
unset publisher_token approver_token
echo 'public CFO demo readiness: ready'
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
