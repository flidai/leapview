#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/lib/hcloud_actions.sh
source "$repo_root/scripts/lib/hcloud_actions.sh"
demo_image="${DEMO_IMAGE:?Set DEMO_IMAGE to an immutable ghcr.io/flidai/leapview digest}"
demo_host="${DEMO_HOST:?Set DEMO_HOST to the demo server IP address}"
demo_target="${DEMO_TARGET:-https://demo.leapview.dev}"
source_revision="${DEMO_SOURCE_REVISION:?Set DEMO_SOURCE_REVISION to the deployed Git revision}"
publisher_client_id="${DEMO_PUBLISHER_CLIENT_ID:?Set DEMO_PUBLISHER_CLIENT_ID}"
publisher_client_secret="${DEMO_PUBLISHER_CLIENT_SECRET:?Set DEMO_PUBLISHER_CLIENT_SECRET}"
release_client_id="${DEMO_RELEASE_CLIENT_ID:?Set DEMO_RELEASE_CLIENT_ID}"
release_client_secret="${DEMO_RELEASE_CLIENT_SECRET:?Set DEMO_RELEASE_CLIENT_SECRET}"
firewall_id="${DEMO_FIREWALL_ID:?Set DEMO_FIREWALL_ID}"
hcloud_token="${HCLOUD_TOKEN:?Set HCLOUD_TOKEN}"
runner_ip="${DEMO_RUNNER_IP:?Set DEMO_RUNNER_IP to the deployment runner IPv4 address}"
project_path="$repo_root/dashboards/leapview.yaml"
data_link="$repo_root/.data/olist"
fingerprint_file="$repo_root/deploy/demo/ssh-host-key.sha256"
project_id="leapview-showcase"
candidate_key="hosted-demo"
temporary_directory="$(mktemp -d)"
firewall_changed=false
original_firewall_rules="$temporary_directory/original-firewall-rules.json"

immutable_image='^ghcr\.io/flidai/leapview@sha256:[0-9a-f]{64}$'
ipv4='^([0-9]{1,3}\.){3}[0-9]{1,3}$'
if [[ ! "$demo_image" =~ $immutable_image ]]; then
  echo "DEMO_IMAGE must use the canonical GHCR repository and an immutable sha256 digest" >&2
  exit 64
fi
if [[ ! "$demo_host" =~ $ipv4 || ! "$runner_ip" =~ $ipv4 ]]; then
  echo "DEMO_HOST and DEMO_RUNNER_IP must be IPv4 addresses" >&2
  exit 64
fi
if [[ ! "$source_revision" =~ ^[0-9a-f]{40}$ ]]; then
  echo "DEMO_SOURCE_REVISION must be a full Git commit identity" >&2
  exit 64
fi

for command in curl docker go jq ssh ssh-keygen ssh-keyscan; do
  if ! command -v "$command" >/dev/null; then
    echo "required command is unavailable: $command" >&2
    exit 69
  fi
done

hcloud_request() {
  local method="$1"
  local path="$2"
  local body="${3:-}"
  local arguments=(
    --fail --silent --show-error
    --request "$method"
    --header "Authorization: Bearer $hcloud_token"
    --header "Content-Type: application/json"
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
  echo "Hetzner firewall action $action_id did not complete" >&2
  return 1
}

set_firewall_rules() {
  local payload="$1"
  local response
  response="$(hcloud_request POST "/firewalls/$firewall_id/actions/set_rules" "$payload")"
  wait_hcloud_actions "$response"
}

cleanup() {
  local status=$?
  if [[ "$firewall_changed" == true && -s "$original_firewall_rules" ]]; then
    restore_payload="$temporary_directory/restore-firewall.json"
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
  next_payload="$temporary_directory/open-firewall.json"
  jq --arg cidr "$runner_cidr" '{rules: (. + [{
    direction: "in",
    protocol: "tcp",
    port: "22",
    source_ips: [$cidr],
    description: "Temporary GitHub-hosted demo deployment access"
  }])}' "$original_firewall_rules" >"$next_payload"
  firewall_changed=true
  set_firewall_rules "$next_payload"
fi

identity_file="$temporary_directory/operator-identity"
printf '%s\n' "${DEMO_SSH_PRIVATE_KEY:?Set DEMO_SSH_PRIVATE_KEY}" >"$identity_file"
chmod 0600 "$identity_file"
unset DEMO_SSH_PRIVATE_KEY

expected_fingerprint="$(tr -d '[:space:]' <"$fingerprint_file")"
scanned_keys="$temporary_directory/scanned-host-keys"
pinned_known_hosts="$temporary_directory/known-hosts"
ssh-keyscan -T 10 "$demo_host" >"$scanned_keys" 2>"$temporary_directory/ssh-keyscan.log"
matched=false
while IFS= read -r scanned_key; do
  [[ -n "$scanned_key" && "$scanned_key" != \#* ]] || continue
  scanned_key_file="$temporary_directory/scanned-host-key"
  printf '%s\n' "$scanned_key" >"$scanned_key_file"
  fingerprint_output="$(ssh-keygen -lf "$scanned_key_file" 2>/dev/null || true)"
  actual_fingerprint="$(awk '{print $2}' <<<"$fingerprint_output")"
  if [[ "$actual_fingerprint" == "$expected_fingerprint" ]]; then
    printf '%s\n' "$scanned_key" >"$pinned_known_hosts"
    matched=true
    break
  fi
done <"$scanned_keys"
if [[ "$matched" != true ]]; then
  echo "demo server host key did not match the reviewed fingerprint" >&2
  exit 1
fi

ssh_options=(
  -i "$identity_file"
  -o BatchMode=yes
  -o ConnectTimeout=10
  -o StrictHostKeyChecking=yes
  -o "UserKnownHostsFile=$pinned_known_hosts"
)
ssh "${ssh_options[@]}" "root@$demo_host" "leapviewctl upgrade '$demo_image'"

exchange_workload_token() {
  local client_id="$1"
  local client_secret="$2"
  local scope="$3"
  local response token
  response="$(curl --fail --silent --show-error \
    --request POST \
    --header 'Content-Type: application/x-www-form-urlencoded' \
    --data-urlencode 'grant_type=client_credentials' \
    --data-urlencode "client_id=$client_id" \
    --data-urlencode "client_secret=$client_secret" \
    --data-urlencode "project_id=$project_id" \
    --data-urlencode "scope=$scope" \
    --data-urlencode 'lifetime_seconds=3600' \
    "$demo_target/oauth/token")"
  token="$(jq -er '.access_token | strings | select(length > 0)' <<<"$response")"
  printf '%s' "$token"
}

publisher_token="$(exchange_workload_token \
  "$publisher_client_id" \
  "$publisher_client_secret" \
  'AUTHOR_PROJECT PUBLISH_RELEASE INGEST_DATA')"
release_token="$(exchange_workload_token \
  "$release_client_id" \
  "$release_client_secret" \
  'VIEW_ITEM APPROVE_DEPLOYMENT ACTIVATE_DEPLOYMENT MANAGE_PUBLICATIONS')"
unset publisher_client_secret release_client_secret

docker pull "$demo_image"
container_id="$(docker create "$demo_image")"
docker cp "$container_id:/usr/local/bin/leapview" "$temporary_directory/leapview"
docker rm "$container_id" >/dev/null
chmod 0755 "$temporary_directory/leapview"
leapview="$temporary_directory/leapview"

cd "$repo_root"
go run ./internal/app/tools/configgen
go run ./internal/app/tools/bootstrapolist --shared-cache --out "$data_link"
data_path="$(cd -P "$data_link" && pwd)"
"$leapview" data sync \
  --project "$project_path" \
  --connection olist \
  --from "$data_path" \
  --target "$demo_target" \
  --token "$publisher_token"
"$leapview" dev --once --no-browser --format json \
  --project "$project_path" \
  --target "$demo_target" \
  --token "$publisher_token" \
  --candidate-key "$candidate_key" \
  --source-repository github.com/flidai/leapview \
  --source-ref refs/heads/main \
  --source-revision "$source_revision"
publication="$("$leapview" publish --format json \
  --project "$project_path" \
  --target "$demo_target" \
  --token "$publisher_token" \
  --candidate-key "$candidate_key")"
deployment_id="$(jq -r '.deploymentId' <<<"$publication")"
[[ "$deployment_id" == deployment_* ]] || {
  echo "demo publication did not return a deployment identity" >&2
  exit 1
}

deployment="$("$leapview" api call getDeployment \
  --target "$demo_target" \
  --token "$release_token" \
  --path "project=$project_id" \
  --path "deployment=$deployment_id")"
status="$(jq -r '.status' <<<"$deployment")"
if [[ "$status" != "active" ]]; then
  approval_id="$(jq -r '.approval.id // empty' <<<"$deployment")"
  approval_status="$(jq -r '.approval.status // empty' <<<"$deployment")"
  approval_revision="$(jq -r '.approval.revision // empty' <<<"$deployment")"
  if [[ "$approval_status" == "pending" ]]; then
    [[ -n "$approval_id" && "$approval_revision" =~ ^[0-9]+$ ]] || {
      echo "demo deployment has invalid approval evidence" >&2
      exit 1
    }
    "$leapview" api call approveDeployment \
      --target "$demo_target" \
      --token "$release_token" \
      --path "project=$project_id" \
      --path "deployment=$deployment_id" \
      --path "approval=$approval_id" \
      --body-json "{\"expectedRevision\":$approval_revision}" \
      --idempotency-key "demo-approve-$source_revision" >/dev/null
  elif [[ "$approval_status" != "approved" ]]; then
    echo "demo deployment approval is $approval_status" >&2
    exit 1
  fi
  "$leapview" api call activateDeployment \
    --target "$demo_target" \
    --token "$release_token" \
    --path "project=$project_id" \
    --path "deployment=$deployment_id" \
    --idempotency-key "demo-activate-$source_revision" >/dev/null
fi

for _ in $(seq 1 120); do
  deployment="$("$leapview" api call getDeployment \
    --target "$demo_target" \
    --token "$release_token" \
    --path "project=$project_id" \
    --path "deployment=$deployment_id")"
  status="$(jq -r '.status' <<<"$deployment")"
  case "$status" in
    active) break ;;
    failed|cancelled|superseded)
      echo "demo deployment ended in $status" >&2
      exit 1
      ;;
  esac
  sleep 2
done
if [[ "$status" != "active" ]]; then
  echo "demo deployment did not become active" >&2
  exit 1
fi

curl --fail --silent --show-error --max-time 15 "$demo_target/readyz" >/dev/null
printf 'deployed %s and the canonical project showcase to %s\n' "$demo_image" "$demo_target"
