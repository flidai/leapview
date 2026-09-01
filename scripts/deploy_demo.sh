#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
demo_target="${DEMO_TARGET:-https://demo.leapview.dev}"
source_revision="${DEMO_SOURCE_REVISION:?Set DEMO_SOURCE_REVISION to the deployed Git revision}"
publisher_client_id="${DEMO_PUBLISHER_CLIENT_ID:?Set DEMO_PUBLISHER_CLIENT_ID}"
publisher_client_secret="${DEMO_PUBLISHER_CLIENT_SECRET:?Set DEMO_PUBLISHER_CLIENT_SECRET}"
release_client_id="${DEMO_RELEASE_CLIENT_ID:?Set DEMO_RELEASE_CLIENT_ID}"
release_client_secret="${DEMO_RELEASE_CLIENT_SECRET:?Set DEMO_RELEASE_CLIENT_SECRET}"
project_path="$repo_root/dashboards/leapview.yaml"
data_link="$repo_root/.data/olist"
project_id="project:leapview-showcase"
candidate_key="hosted-demo"
temporary_directory="$(mktemp -d)"

if [[ ! "$source_revision" =~ ^[0-9a-f]{40}$ ]]; then
  echo "DEMO_SOURCE_REVISION must be a full Git commit identity" >&2
  exit 64
fi

for command in curl go jq; do
  if ! command -v "$command" >/dev/null; then
    echo "required command is unavailable: $command" >&2
    exit 69
  fi
done

cleanup() {
  local status=$?
  rm -rf "$temporary_directory"
  exit "$status"
}
trap cleanup EXIT
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
  'RESOURCE_USE RESOURCE_READ RESOURCE_EDIT RESOURCE_PUBLISH')"
release_token="$(exchange_workload_token \
  "$release_client_id" \
  "$release_client_secret" \
  'PROJECT_ADMIN')"
unset publisher_client_secret release_client_secret

leapview="$temporary_directory/leapview"

cd "$repo_root"
go build -o "$leapview" ./cmd/leapview
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

"$leapview" api call getProject \
  --target "$demo_target" \
  --token "$release_token" \
  --path "project=$project_id" >/dev/null

jq -e --arg project "$project_id" '
  .projectId == $project and .evidence.projectId == $project
' <<<"$deployment" >/dev/null
curl --fail --silent --show-error --max-time 15 "$demo_target/readyz" >/dev/null
printf 'published the canonical project showcase to %s\n' "$demo_target"
