#!/usr/bin/env bash
set -euo pipefail

scope="${MIGRATION_INVENTORY_SCOPE:-both}"
runner_ip="$(curl -4fsS --connect-timeout 5 --max-time 10 https://api.ipify.org)"
temporary_directory="$(mktemp -d)"
original_firewall_rules="$temporary_directory/original-firewall-rules.json"
firewall_changed=false
old_host=
new_host=

case "$scope" in
  both)
    new_host="${NEW_DEMO_HOST:?Set NEW_DEMO_HOST}"
    old_host="${DEMO_HOST:?Set DEMO_HOST}"
    firewall_id="${DEMO_FIREWALL_ID:?Set DEMO_FIREWALL_ID}"
    hcloud_token="${HCLOUD_TOKEN:?Set HCLOUD_TOKEN}"
    ;;
  old)
    old_host="${DEMO_HOST:?Set DEMO_HOST}"
    firewall_id="${DEMO_FIREWALL_ID:?Set DEMO_FIREWALL_ID}"
    hcloud_token="${HCLOUD_TOKEN:?Set HCLOUD_TOKEN}"
    ;;
  new) new_host="${NEW_DEMO_HOST:?Set NEW_DEMO_HOST}" ;;
  *) echo "Unsupported migration inventory scope: $scope" >&2; exit 64 ;;
esac

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

if [[ "$scope" == both || "$scope" == old ]]; then
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
if [[ "$scope" == both ]]; then
  pin_host "$old_host" "$OLD_EXPECTED_FINGERPRINT"
  pin_host "$new_host" "$NEW_EXPECTED_FINGERPRINT"
elif [[ "$scope" == old ]]; then
  pin_host "$old_host" "$OLD_EXPECTED_FINGERPRINT"
else
  pin_host "$new_host" "$NEW_EXPECTED_FINGERPRINT"
fi

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
echo "== leapview runtime =="
systemctl show leapview-demo-current.service \
  --property=ActiveState,SubState,MainPID,NRestarts,FragmentPath,ExecMainStartTimestamp --no-pager || true
pid=$(systemctl show leapview-demo-current.service --property=MainPID --value 2>/dev/null || true)
if [[ "$pid" =~ ^[1-9][0-9]*$ && -e "/proc/$pid/exe" ]]; then
  executable=$(readlink -f "/proc/$pid/exe")
  echo "executable=$executable"
  "$executable" version --json || true
  echo "environment keys:"
  tr "\0" "\n" <"/proc/$pid/environ" | sed -n "s/=.*//p" | sort
fi
echo "== releases =="
if [[ -d /opt/leapview-demo/releases ]]; then
  for release in /opt/leapview-demo/releases/*; do
    [[ -d "$release" ]] || continue
    du -sh "$release"
    if [[ -x "$release/leapview" ]]; then
      "$release/leapview" version --json || true
      (cd "$release" && sha256sum --check leapview.sha256) 2>/dev/null || true
    fi
    if [[ -f "$release/immutable-image.txt" ]]; then
      printf "image="
      cat "$release/immutable-image.txt"
    fi
  done
fi
echo "== database state =="
while IFS= read -r database_container; do
  [[ -n "$database_container" ]] || continue
  echo "container=$database_container"
  docker inspect "$database_container" --format "mounts={{json .Mounts}}"
  postgres_user=$(docker exec "$database_container" printenv POSTGRES_USER)
  docker exec "$database_container" psql -v ON_ERROR_STOP=1 -U "$postgres_user" -d leapview_control -Atc \
    "SELECT datname || chr(124) || pg_database_size(oid) FROM pg_database ORDER BY datname"
  printf "schema_version|"
  docker exec "$database_container" psql -v ON_ERROR_STOP=1 -U "$postgres_user" -d leapview_control -Atc \
    "SELECT max(version_id) FROM public.goose_db_version WHERE is_applied"
  while IFS= read -r schema; do echo "schema|$schema"; done < <(
    docker exec "$database_container" psql -v ON_ERROR_STOP=1 -U "$postgres_user" -d leapview_control -Atc \
      "SELECT schema_name FROM information_schema.schemata ORDER BY schema_name"
  )
  while IFS= read -r role; do echo "role|$role"; done < <(
    docker exec "$database_container" psql -v ON_ERROR_STOP=1 -U "$postgres_user" -d leapview_control -Atc \
      "SELECT rolname FROM pg_roles ORDER BY rolname"
  )
done < <(docker ps --format "{{.Names}}" | grep -- "-demo-current-postgres-1$" || true)
echo "== rollback recovery sets =="
if [[ -d /opt/leapview-demo/rollbacks ]]; then
  find /opt/leapview-demo/rollbacks -mindepth 1 -maxdepth 2 -type f \
    -printf "%TY-%Tm-%TdT%TH:%TM:%TSZ|%s|%m|%p\n" | sort
  while IFS= read -r dump; do
    database_container=$(docker ps --format "{{.Names}}" | grep -- "-demo-current-postgres-1$" | head -1)
    docker exec -i "$database_container" pg_restore --list <"$dump" >/dev/null
    echo "verified_dump=$dump"
  done < <(find /opt/leapview-demo/rollbacks -type f -name "*.dump" | sort)
  while IFS= read -r archive; do
    tar -tf "$archive" >/dev/null
    echo "verified_archive=$archive"
  done < <(find /opt/leapview-demo/rollbacks -type f -name "*.tar" | sort)
fi
echo "== scheduled backup =="
systemctl show leapview-backup.service \
  --property=LoadState,ActiveState,SubState,Result,ExecMainStatus,ExecMainStartTimestamp,ExecMainExitTimestamp --no-pager || true
systemctl list-timers --all --no-pager | grep -Ei "leapview|backup" || true
find /opt/leapview-backups /var/backups/leapview /opt/leapview-demo/backups \
  -maxdepth 2 -type f -printf "%TY-%Tm-%TdT%TH:%TM:%TSZ|%s|%m|%p\n" 2>/dev/null | sort || true
'

case "$scope" in
  both) hosts=("$old_host" "$new_host") ;;
  old) hosts=("$old_host") ;;
  new) hosts=("$new_host") ;;
esac
for host in "${hosts[@]}"; do
  echo "===== HOST $host ====="
  ssh_options=(
    -i "$identity_file"
    -o BatchMode=yes
    -o ConnectTimeout=10
    -o StrictHostKeyChecking=yes
    -o "UserKnownHostsFile=$known_hosts"
  )
  users=(root)
  if [[ "$scope" != old && "$host" == "$new_host" ]]; then
    users=(root ganesh anand ubuntu)
  fi
  remote_user=
  for candidate in "${users[@]}"; do
    if ssh "${ssh_options[@]}" "$candidate@$host" true >/dev/null 2>&1; then
      remote_user="$candidate"
      break
    fi
  done
  if [[ -z "$remote_user" ]]; then
    echo "deployment SSH identity is not authorized for any reviewed operator account on $host" >&2
    exit 77
  fi
  remote_shell='bash -se'
  if [[ "$remote_user" != root ]]; then
    remote_shell='sudo -n bash -se'
  fi
  echo "access=$remote_user"
  printf '%s\n' "$inventory" | ssh "${ssh_options[@]}" "$remote_user@$host" "$remote_shell"
done
