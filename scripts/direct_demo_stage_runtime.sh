#!/usr/bin/env bash
set -euo pipefail

demo_host="${DEMO_HOST:?Set DEMO_HOST}"
firewall_id="${DEMO_FIREWALL_ID:?Set DEMO_FIREWALL_ID}"
hcloud_token="${HCLOUD_TOKEN:?Set HCLOUD_TOKEN}"
runner_ip="${DEMO_RUNNER_IP:?Set DEMO_RUNNER_IP}"
expected_fingerprint="${DEMO_EXPECTED_SSH_FINGERPRINT:?Set DEMO_EXPECTED_SSH_FINGERPRINT}"
revision="${DIRECT_DEMO_REVISION:?Set DIRECT_DEMO_REVISION}"
image="${DIRECT_DEMO_IMAGE:?Set DIRECT_DEMO_IMAGE}"
temporary_directory="$(mktemp -d)"
original_firewall_rules="$temporary_directory/original-firewall-rules.json"
firewall_changed=false

[[ "$revision" =~ ^[0-9a-f]{40}$ ]] || { echo 'DIRECT_DEMO_REVISION must be a full Git commit identity' >&2; exit 64; }
[[ "$image" =~ ^ghcr\.io/flidai/leapview@sha256:[0-9a-f]{64}$ ]] || { echo 'DIRECT_DEMO_IMAGE must be an immutable LeapView OCI reference' >&2; exit 64; }
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
  jq --arg cidr "$runner_cidr" '{rules: (. + [{direction: "in", protocol: "tcp", port: "22", source_ips: [$cidr], description: "Temporary hosted-demo direct rollout access"}])}' "$original_firewall_rules" >"$open_payload"
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
  "root@$demo_host" "DIRECT_DEMO_REVISION=$revision DIRECT_DEMO_IMAGE=$image bash -se" <<'REMOTE'
set -euo pipefail
revision="${DIRECT_DEMO_REVISION:?}"
image="${DIRECT_DEMO_IMAGE:?}"
release="/opt/leapview-demo/releases/$revision"
service='leapview-demo-current.service'

if ! systemctl is-active --quiet "$service"; then
  echo 'demo service is not active; refusing to stage while the live runtime is unhealthy' >&2
  exit 1
fi
active_pid="$(systemctl show "$service" --property=MainPID --value)"
[[ "$active_pid" =~ ^[0-9]+$ && "$active_pid" != 0 ]]
curl --fail --silent --show-error http://127.0.0.1:8132/readyz >/dev/null

if [[ -x "$release/leapview" && -f "$release/immutable-image.txt" && -f "$release/leapview.sha256" ]]; then
  [[ "$(<"$release/immutable-image.txt")" == "$image" ]]
  identity="$("$release/leapview" version --json)"
  jq -e --arg revision "$revision" '.revision == $revision and .dirty == false' <<<"$identity" >/dev/null
  (cd "$release" && sha256sum --check leapview.sha256)
  printf 'Exact direct-demo image already staged at %s; active service unchanged\n' "$release"
  exit 0
fi

available_kb="$(df --output=avail /opt | tail -1 | tr -d ' ')"
if (( available_kb <= 7000000 )); then
  df -h /opt
  docker system df
  echo 'At least 7 GB free space required to stage image' >&2
  exit 1
fi

docker pull "$image"
docker image inspect "$image" --format '{{json .RepoDigests}}' | jq -e --arg image "$image" 'index($image) != null' >/dev/null
[[ "$(docker image inspect "$image" --format '{{index .Config.Labels "org.opencontainers.image.revision"}}')" == "$revision" ]]
install -d -m 0755 "$release"
container="$(docker create "$image")"
cleanup_container() { docker rm "$container" >/dev/null 2>&1 || true; }
trap cleanup_container EXIT
docker cp "$container:/app/." "$release/"
docker cp "$container:/usr/local/bin/leapview" "$release/leapview"
chmod 0755 "$release/leapview"
identity="$("$release/leapview" version --json)"
jq -e --arg revision "$revision" '.revision == $revision and .dirty == false' <<<"$identity" >/dev/null
printf '%s\n' "$image" >"$release/immutable-image.txt"
sha256sum "$release/leapview" >"$release/leapview.sha256"
docker rm "$container" >/dev/null
trap - EXIT
printf 'Staged exact direct-demo image at %s; active service unchanged\n' "$release"
REMOTE
