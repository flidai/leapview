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

ssh_options=(
  -i "$identity_file"
  -o BatchMode=yes
  -o ConnectTimeout=10
  -o StrictHostKeyChecking=yes
  -o "UserKnownHostsFile=$pinned_known_hosts"
)

if [[ -n "${DEMO_RECOVERY_MANAGED_DATA_ARCHIVE:-}" ]]; then
  command -v scp >/dev/null || {
    echo "required command is unavailable: scp" >&2
    exit 69
  }
  recovery_archive="$DEMO_RECOVERY_MANAGED_DATA_ARCHIVE"
  recovery_revision="${DEMO_RECOVERY_MANAGED_DATA_REVISION:?Set DEMO_RECOVERY_MANAGED_DATA_REVISION}"
  [[ -f "$recovery_archive" ]] || {
    echo "managed-data recovery archive is unavailable" >&2
    exit 69
  }
  [[ "$recovery_revision" =~ ^sha256:[0-9a-f]{64}$ ]] || {
    echo "managed-data recovery revision is invalid" >&2
    exit 64
  }
  scp "${ssh_options[@]}" "$recovery_archive" "root@$demo_host:/tmp/leapview-managed-data-recovery.tar.gz"
  ssh "${ssh_options[@]}" "root@$demo_host" "DEMO_RECOVERY_MANAGED_DATA_REVISION='$recovery_revision' bash -se" <<'RECOVER_DATA'
set -euo pipefail
archive=/tmp/leapview-managed-data-recovery.tar.gz
revision="$DEMO_RECOVERY_MANAGED_DATA_REVISION"
root=/tmp/leapview-demo-host-state/managed-data/objects/revisions
staging="$(mktemp -d "$root/.recovery-XXXXXX")"
cleanup_recovery() {
  rm -rf "$staging" "$archive"
}
trap cleanup_recovery EXIT
tar -xzf "$archive" -C "$staging" --no-same-owner --no-same-permissions
python3 - "$staging" "$revision" <<'PY'
import hashlib
import json
import os
from pathlib import Path
import sys

root = Path(sys.argv[1])
revision = sys.argv[2]
manifest_path = root / "manifest.json"
data = root / "data"
manifest = json.loads(manifest_path.read_text())
files = manifest.get("files")
if not isinstance(files, list) or not files:
    raise SystemExit("managed-data recovery manifest is empty")
canonical = json.dumps(manifest, separators=(",", ":"), ensure_ascii=False).encode()
actual_revision = "sha256:" + hashlib.sha256(canonical).hexdigest()
if actual_revision != revision:
    raise SystemExit(f"managed-data recovery revision mismatch: {actual_revision}")
expected = set()
for item in files:
    logical = item.get("path", "")
    if not logical or Path(logical).is_absolute() or ".." in Path(logical).parts:
        raise SystemExit(f"invalid recovery path: {logical!r}")
    path = data / logical
    if not path.is_file() or path.is_symlink():
        raise SystemExit(f"recovery file is unavailable: {logical}")
    body = path.read_bytes()
    if len(body) != item.get("size") or hashlib.sha256(body).hexdigest() != item.get("sha256"):
        raise SystemExit(f"recovery file does not match manifest: {logical}")
    expected.add(logical)
observed = {
    str(path.relative_to(data))
    for path in data.rglob("*")
    if path.is_file()
}
if observed != expected:
    raise SystemExit("managed-data recovery archive contains unexpected files")
PY
destination="$root/$revision/data"
mkdir -p "$(dirname "$destination")"
rm -rf "$destination"
mv "$staging/data" "$destination"
chmod -R u=rwX,go=rX "$destination"
printf 'restored managed-data revision %s with %s files\n' "$revision" "$(find "$destination" -type f | wc -l)"
RECOVER_DATA
fi

if [[ "${DEMO_RECOVER_CREDENTIALS:-false}" == true ]]; then
  command -v scp >/dev/null || {
    echo "required command is unavailable: scp" >&2
    exit 69
  }
  recovery_binary="${DEMO_RECOVERY_BINARY:?Set DEMO_RECOVERY_BINARY}"
  [[ -x "$recovery_binary" ]] || {
    echo "demo recovery binary is unavailable" >&2
    exit 69
  }
  recovery_wrapper="$temporary_directory/recover-demo-credentials.sh"
  cat >"$recovery_wrapper" <<'RECOVER'
#!/usr/bin/env bash
set -euo pipefail
pid="$(systemctl show leapview-demo-current.service --property=MainPID --value)"
process_env() {
  python3 -c 'import sys
pid,key=sys.argv[1:]
items=dict(item.split(b"=",1) for item in open("/proc/"+pid+"/environ","rb").read().split(b"\0") if b"=" in item)
sys.stdout.write(items.get(key.encode(),b"").decode())' "$pid" "$1"
}
export LEAPVIEW_POSTGRES_CONTROL_URL="$(process_env LEAPVIEW_POSTGRES_CONTROL_URL)"
export LEAPVIEW_TOKEN_HASH_KEY="$(process_env LEAPVIEW_TOKEN_HASH_KEY)"
export LEAPVIEW_CSRF_KEY="$(process_env LEAPVIEW_CSRF_KEY)"
chmod 0700 /tmp/recover-demo-credentials
/tmp/recover-demo-credentials
rm -f /tmp/recover-demo-credentials /tmp/recover-demo-credentials.sh
RECOVER
  scp "${ssh_options[@]}" "$recovery_binary" "root@$demo_host:/tmp/recover-demo-credentials"
  scp "${ssh_options[@]}" "$recovery_wrapper" "root@$demo_host:/tmp/recover-demo-credentials.sh"
fi

ssh "${ssh_options[@]}" "root@$demo_host" 'bash -se' <<'REMOTE'
set -euo pipefail
printf 'Runtime service state: '
systemctl is-active leapview-demo-current.service || true
systemctl show leapview-demo-current.service --property=FragmentPath,WorkingDirectory,MainPID
printf 'Installed binary identity:\n'
if [[ -x /tmp/leapview-main/.tmp/leapview-dev ]]; then
  /tmp/leapview-main/.tmp/leapview-dev version --json
fi
printf 'Container inventory:\n'
if command -v docker >/dev/null; then
  docker ps --format '{{.Names}} {{.Image}} {{.Status}}'
fi
printf 'Available build tools:\n'
for cmd in go bun task docker; do command -v "$cmd" || true; done
printf 'Disk and memory:\n'
df -h / /tmp
free -h
printf 'Readiness:\n'
curl -fsS --max-time 15 https://demo.leapview.dev/readyz
printf '\n'
printf 'Selected runtime configuration:\n'
python3 - <<'PYREMOTE'
import os, subprocess
pid=subprocess.check_output(['systemctl','show','leapview-demo-current.service','--property=MainPID','--value']).decode().strip()
env=dict(item.split(b'=',1) for item in open('/proc/'+pid+'/environ','rb').read().split(b'\0') if b'=' in item)
for key in sorted(env):
    name=key.decode()
    if name.startswith('LEAPVIEW_'):
        safe=any(word in name for word in ['_DIR','_PATH','_HOME','_ADDR','_PORT']) and not any(word in name for word in ['PASSWORD','SECRET','TOKEN','KEY','URL'])
        print(name+'='+ (env[key].decode() if safe else '<configured>'))
print('process argument names: '+ ' '.join(x.decode() for x in open('/proc/'+pid+'/cmdline','rb').read().split(b'\0') if x.startswith(b'--')))
PYREMOTE
printf 'PostgreSQL current schema versions:\n'
for container in $(docker ps --format '{{.Names}}' | grep -- '-postgres-1$'); do
  printf '%s: ' "$container"
  docker exec "$container" sh -c 'psql -U "$POSTGRES_USER" -d leapview_control -Atc "SELECT max(version_id) FROM public.goose_db_version WHERE is_applied"' || true
  printf 'Delivery targets and active generations:\n'
  docker exec -i "$container" sh -c 'psql -U "$POSTGRES_USER" -d leapview_control -At' <<'SQL' || true
SELECT t.target_id,t.project_id,t.environment,COALESCE(p.generation_id::text,'')
FROM delivery.delivery_target t
LEFT JOIN delivery.delivery_active_pointer p ON p.target_id=t.target_id
ORDER BY t.target_id;
SQL
  printf 'Canonical project identities:\n'
  docker exec "$container" sh -c 'psql -U "$POSTGRES_USER" -d leapview_control -Atc "SELECT project_id FROM project.project_identity ORDER BY project_id"' || true
  printf 'Service principal credential inventory:\n'
  docker exec -i "$container" sh -c 'psql -U "$POSTGRES_USER" -d leapview_control -At' <<'SQL' || true
SELECT p.id,p.display_name,p.status,COUNT(s.id),COUNT(s.id) FILTER (WHERE s.revoked_at IS NULL AND s.expires_at > clock_timestamp())
FROM access.principal p
LEFT JOIN access.service_principal_secret s ON s.service_principal_id=p.id
WHERE p.principal_type='service'
GROUP BY p.id,p.display_name,p.status
ORDER BY p.display_name;
SQL
  printf 'Service principal authorization inventory:\n'
  docker exec -i "$container" sh -c 'psql -U "$POSTGRES_USER" -d leapview_control -At' <<'SQL' || true
SELECT subject_id,'role:'||role,capabilities::text
FROM access.authorization_role_binding
WHERE subject_kind='principal' AND revoked_at IS NULL
  AND subject_id IN (SELECT id::text FROM access.principal WHERE principal_type='service')
UNION ALL
SELECT subject_id,'grant:'||capability,resource_id
FROM access.authorization_grant
WHERE subject_kind='principal' AND revoked_at IS NULL
  AND subject_id IN (SELECT id::text FROM access.principal WHERE principal_type='service')
ORDER BY 1,2,3;
SQL
  printf 'Target authorization policy inventory:\n'
  docker exec -i "$container" sh -c 'psql -U "$POSTGRES_USER" -d leapview_control -At' <<'SQL' || true
SELECT p.target_id,p.project_id,p.environment,p.revision,
       COUNT(b.id),COALESCE(string_agg(b.subject_id||':'||b.role,',' ORDER BY b.subject_id,b.role),'')
FROM access.authorization_policy p
LEFT JOIN access.authorization_policy_role_binding b
  ON b.target_id=p.target_id AND b.project_id=p.project_id
 AND b.environment=p.environment AND b.revision=p.revision
GROUP BY p.target_id,p.project_id,p.environment,p.revision
ORDER BY p.target_id,p.project_id,p.environment;
SQL
  printf 'Active generation authorization inventory:\n'
  docker exec -i "$container" sh -c 'psql -U "$POSTGRES_USER" -d leapview_control -At' <<'SQL' || true
SELECT r.project_id,r.environment,r.generation_id,COUNT(*),
       COALESCE(string_agg(r.subject_id||':'||r.role,',' ORDER BY r.subject_id,r.role),'')
FROM access.authorization_role_binding r
WHERE r.revoked_at IS NULL
GROUP BY r.project_id,r.environment,r.generation_id
ORDER BY r.project_id,r.environment,r.generation_id;
SQL
done
printf 'Operator environment file paths:\n'
find /tmp/leapview-main/.tmp /etc/leapview /opt/leapview -maxdepth 2 -type f -name '*env*' -print 2>/dev/null || true
printf 'Runtime argument inventory:\n'
python3 - <<'PYARGS'
import subprocess
pid=subprocess.check_output(['systemctl','show','leapview-demo-current.service','--property=MainPID','--value']).decode().strip()
args=[s.decode() for s in open('/proc/'+pid+'/cmdline','rb').read().split(b'\0') if s]
print('Expected serve --production arguments:', args[1:]==['serve','--production'])
PYARGS
printf 'Redacted recent runtime errors:\n'
python3 - <<'PYLOG'
import json, subprocess, re
from pathlib import Path
pid=subprocess.check_output(['systemctl','show','leapview-demo-current.service','--property=MainPID','--value']).decode().strip()
env=dict(item.split(b'=',1) for item in open('/proc/'+pid+'/environ','rb').read().split(b'\0') if b'=' in item)
secrets=[value.decode() for key,value in env.items() if any(word in key for word in (b'KEY',b'PASSWORD',b'TOKEN',b'URL',b'SECRET')) and len(value)>7]
logs=subprocess.check_output(['journalctl','-u','leapview-demo-current.service','--since','-10 minutes','--no-pager','-o','json']).decode()
messages=[]
for line in logs.splitlines():
    item=json.loads(line)
    message=item.get('MESSAGE','')
    if item.get('_COMM') != 'systemd':
        for secret in sorted(secrets,key=len,reverse=True): message=message.replace(secret,'<redacted>')
        message=re.sub(r'postgres(?:ql)?://[^\s]+','<redacted database URL>',message)
        messages.append(message)
unit_path=subprocess.check_output(['systemctl','show','leapview-demo-current.service','--property=FragmentPath','--value']).decode().strip()
for line in Path(unit_path).read_text().splitlines():
    if line.startswith(('StandardOutput=','StandardError=')):
        destination=line.split('=',1)[1]
        if destination.startswith(('append:/','file:/','truncate:/')):
            log_path=destination.split(':',1)[1]
            print('Runtime log path: '+log_path)
            if Path(log_path).is_file():
                for message in Path(log_path).read_text(errors='replace')[-40000:].splitlines():
                    if any(word in message.lower() for word in ('error','failed','fatal','invalid','missing','mismatch','panic','requires','refus')):
                        for secret in sorted(secrets,key=len,reverse=True): message=message.replace(secret,'<redacted>')
                        message=re.sub(r'postgres(?:ql)?://[^\s]+','<redacted database URL>',message)
                        messages.append(message)
print('\n'.join(dict.fromkeys(messages))[:18000])
PYLOG
printf 'Repository identity:\n'
if [[ -d /tmp/leapview-main/.git ]]; then
  git -C /tmp/leapview-main rev-parse HEAD
fi
REMOTE

if [[ "${DEMO_RECOVER_CREDENTIALS:-false}" == true ]]; then
  recovery_payload="$(jq -cn \
    --arg publisher_id "${DEMO_PUBLISHER_CLIENT_ID:?Set DEMO_PUBLISHER_CLIENT_ID}" \
    --arg publisher_secret "${DEMO_PUBLISHER_CLIENT_SECRET:?Set DEMO_PUBLISHER_CLIENT_SECRET}" \
    --arg release_id "${DEMO_RELEASE_CLIENT_ID:?Set DEMO_RELEASE_CLIENT_ID}" \
    --arg release_secret "${DEMO_RELEASE_CLIENT_SECRET:?Set DEMO_RELEASE_CLIENT_SECRET}" \
    --arg platform_admin_mode "${DEMO_PLATFORM_ADMIN_MODE:-unchanged}" \
    --arg recovery_publisher_token_id "${DEMO_RECOVERY_PUBLISHER_TOKEN_ID:-}" \
    --arg recovery_publisher_principal_id "${DEMO_RECOVERY_PUBLISHER_PRINCIPAL_ID:-}" \
    --arg recovery_publisher_token "${DEMO_RECOVERY_PUBLISHER_TOKEN:-}" \
    --arg recovery_release_token_id "${DEMO_RECOVERY_RELEASE_TOKEN_ID:-}" \
    --arg recovery_release_principal_id "${DEMO_RECOVERY_RELEASE_PRINCIPAL_ID:-}" \
    --arg recovery_release_token "${DEMO_RECOVERY_RELEASE_TOKEN:-}" \
    '{platformAdminMode:$platform_admin_mode,credentials:[
      {clientId:$publisher_id,clientSecret:$publisher_secret,name:"publisher"},
      {clientId:$release_id,clientSecret:$release_secret,name:"release"}
    ],tokens:[
      {id:$recovery_publisher_token_id,clientId:$recovery_publisher_principal_id,clientSecret:$recovery_publisher_token,name:"publisher"},
      {id:$recovery_release_token_id,clientId:$recovery_release_principal_id,clientSecret:$recovery_release_token,name:"release"}
    ]}')"
  unset DEMO_PUBLISHER_CLIENT_SECRET DEMO_RELEASE_CLIENT_SECRET DEMO_RECOVERY_PUBLISHER_TOKEN DEMO_RECOVERY_RELEASE_TOKEN
  printf '%s' "$recovery_payload" | ssh "${ssh_options[@]}" "root@$demo_host" 'bash /tmp/recover-demo-credentials.sh'
  if [[ "${DEMO_PLATFORM_ADMIN_MODE:-unchanged}" == grant-generation || "${DEMO_PLATFORM_ADMIN_MODE:-unchanged}" == revoke-generation ]]; then
    ssh "${ssh_options[@]}" "root@$demo_host" 'systemctl restart leapview-demo-current.service'
    for _ in $(seq 1 60); do
      if curl --fail --silent --show-error --max-time 10 https://demo.leapview.dev/readyz >/dev/null; then
        echo "demo runtime reloaded the active authorization snapshot"
        break
      fi
      sleep 2
    done
    curl --fail --silent --show-error --max-time 10 https://demo.leapview.dev/readyz >/dev/null
  fi
fi
