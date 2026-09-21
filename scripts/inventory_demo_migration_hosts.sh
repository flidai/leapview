#!/usr/bin/env bash
set -euo pipefail

old_host="${DEMO_HOST:?Set DEMO_HOST}"
new_host="${NEW_DEMO_HOST:?Set NEW_DEMO_HOST}"
firewall_id="${DEMO_FIREWALL_ID:?Set DEMO_FIREWALL_ID}"
hcloud_token="${HCLOUD_TOKEN:?Set HCLOUD_TOKEN}"
runner_ip="$(curl -4fsS --connect-timeout 5 --max-time 10 https://api.ipify.org)"
temporary_directory="$(mktemp -d)"
original_firewall_rules="$temporary_directory/original-firewall-rules.json"
firewall_changed=false

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
    set_firewall_rules "$restore_payload" || status=1
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
    description: "Temporary hosted-demo migration inventory access"
  }])}' "$original_firewall_rules" >"$open_payload"
  firewall_changed=true
  set_firewall_rules "$open_payload"
fi

identity_file="$temporary_directory/operator-identity"
printf '%s\n' "${DEMO_SSH_PRIVATE_KEY:?Set DEMO_SSH_PRIVATE_KEY}" >"$identity_file"
chmod 0600 "$identity_file"
unset DEMO_SSH_PRIVATE_KEY

known_hosts="$temporary_directory/known-hosts"
pin_host() {
  local host="$1"
  local expected="$2"
  local scan="$temporary_directory/scan"
  ssh-keyscan -T 10 "$host" >"$scan" 2>/dev/null
  while IFS= read -r key; do
    [[ -n "$key" && "$key" != \#* ]] || continue
    printf '%s\n' "$key" >"$temporary_directory/key"
    if [[ "$(ssh-keygen -lf "$temporary_directory/key" | awk '{print $2}')" == "$expected" ]]; then
      cat "$temporary_directory/key" >>"$known_hosts"
      return 0
    fi
  done <"$scan"
  echo "host key did not match the reviewed fingerprint for $host" >&2
  return 1
}
pin_host "$old_host" "$OLD_EXPECTED_FINGERPRINT"
pin_host "$new_host" "$NEW_EXPECTED_FINGERPRINT"

inventory='set -euo pipefail
echo "== identity =="
hostname
cat /etc/os-release | grep -E "^(NAME|VERSION|VERSION_ID|ID)="
uname -srmo
echo "architecture=$(uname -m) cpus=$(nproc)"
free -h
echo "== storage =="
lsblk -o NAME,TYPE,SIZE,FSTYPE,MOUNTPOINTS
df -hT
df -hi
echo "== platform =="
systemctl is-system-running || true
command -v docker || true
if command -v docker >/dev/null; then
  docker version --format "client={{.Client.Version}} server={{.Server.Version}}"
  docker info --format "driver={{.Driver}} root={{.DockerRootDir}} cpus={{.NCPU}} memory={{.MemTotal}}"
  docker ps --format "{{.Names}}|{{.Image}}|{{.Status}}|{{.Ports}}"
  docker images --digests --format "{{.Repository}}|{{.Tag}}|{{.Digest}}|{{.Size}}"
  docker system df
  docker volume ls --format "{{.Name}}|{{.Driver}}"
fi
echo "== services =="
systemctl list-units --type=service --all --no-pager | grep -Ei "leapview|docker|containerd|postgres|caddy|tailscale" || true
ss -lntp
echo "== leapview paths =="
du -sh /opt/leapview-demo /tmp/leapview-demo-host-state /tmp/leapview-main 2>/dev/null || true
find /opt/leapview-demo -maxdepth 2 -mindepth 1 -printf "%y %p\n" 2>/dev/null | sort || true
'

for host in "$old_host" "$new_host"; do
  echo "===== HOST $host ====="
  printf '%s\n' "$inventory" | ssh -i "$identity_file" -o BatchMode=yes -o ConnectTimeout=10 \
    -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=$known_hosts" \
    "root@$host" 'bash -se'
done
