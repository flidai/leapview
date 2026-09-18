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

ssh -i "$identity_file" -o BatchMode=yes -o ConnectTimeout=10 \
  -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=$pinned_known_hosts" \
  "root@$demo_host" 'bash -se' <<'REMOTE'
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
logs=subprocess.check_output(['journalctl','-u','leapview-demo-current.service','--since','2026-09-18 19:48:00 UTC','--until','2026-09-18 19:50:15 UTC','--no-pager','-o','json']).decode()
messages=[]
for line in logs.splitlines():
    item=json.loads(line)
    message=item.get('MESSAGE','')
    if any(word in message.lower() for word in ('error','failed','fatal','invalid','missing','mismatch','panic')):
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
print('\n'.join(dict.fromkeys(messages))[-18000:])
PYLOG
printf 'Repository identity:\n'
if [[ -d /tmp/leapview-main/.git ]]; then
  git -C /tmp/leapview-main rev-parse HEAD
fi
REMOTE
