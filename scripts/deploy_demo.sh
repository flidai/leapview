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

# Delivery is deliberately split into the target-owned plan, build, and
# publication commands. Each command persists an immutable checkpoint that
# the next command resolves, so the candidate cannot be redirected to another
# project or target by a later invocation.
plan="$("$leapview" plan "$project_path" \
  --target "$demo_target" \
  --token "$publisher_token" \
  --candidate-key "$candidate_key" \
  --format json)"
plan_id="$(jq -er '.planId | strings | select(length > 0)' <<<"$plan")"
[[ "$(jq -r '.projectId' <<<"$plan")" == "$project_id" ]] || {
  echo "demo delivery plan has an unexpected project identity" >&2
  exit 1
}
[[ "$(jq -r '.status' <<<"$plan")" == "planned" ]] || {
  echo "demo delivery plan did not remain planned" >&2
  exit 1
}

build="$("$leapview" build "$plan_id" \
  --token "$publisher_token" \
  --format json)"
[[ "$(jq -r '.status' <<<"$build")" == "sealed" ]] || {
  echo "demo delivery build did not seal" >&2
  exit 1
}
candidate_id="$(jq -er '.candidateId | strings | select(length > 0)' <<<"$build")"

candidate_status="$("$leapview" api call getDeliveryCandidateStatus \
  --target "$demo_target" \
  --token "$publisher_token" \
  --path "project=$project_id" \
  --path "candidate=$candidate_id")"
[[ "$(jq -r '.status' <<<"$candidate_status")" == "ready" ]] || {
  echo "demo delivery candidate is not ready" >&2
  exit 1
}

publication="$("$leapview" publish "$candidate_id" \
  --format json \
  --token "$publisher_token")"
publication_id="$(jq -er '.publicationId | strings | select(length > 0)' <<<"$publication")"
generation_id="$(jq -er '.generationId | strings | select(length > 0)' <<<"$publication")"
publication_status="$(jq -r '.status' <<<"$publication")"
[[ "$(jq -r '.candidateId' <<<"$publication")" == "$candidate_id" ]] || {
  echo "demo publication did not preserve the sealed candidate identity" >&2
  exit 1
}

# Protected targets return a pending publication. Request its approval as the
# publisher, then make the decision with the independent release principal.
if [[ "$publication_status" == "pending" ]]; then
  approval="$("$leapview" api call requestDeliveryPublicationApproval \
    --target "$demo_target" \
    --token "$publisher_token" \
    --path "project=$project_id" \
    --path "publication=$publication_id" \
    --idempotency-key "demo-request-approval-$source_revision")"
  approval_id="$(jq -er '.id | strings | select(length > 0)' <<<"$approval")"
  approval_revision="$(jq -er '.revision' <<<"$approval")"
  approval_status="$(jq -r '.status' <<<"$approval")"
  [[ "$approval_status" == "pending" && "$approval_revision" =~ ^[0-9]+$ ]] || {
    echo "demo publication approval request is invalid" >&2
    exit 1
  }
  approved="$("$leapview" api call approveDeliveryPublicationApproval \
    --target "$demo_target" \
    --token "$release_token" \
    --path "project=$project_id" \
    --path "publication=$publication_id" \
    --path "approval=$approval_id" \
    --body-json "{\"expectedRevision\":$approval_revision}" \
    --idempotency-key "demo-approve-$source_revision")"
  [[ "$(jq -r '.status' <<<"$approved")" == "approved" ]] || {
    echo "demo publication approval was not granted" >&2
    exit 1
  }
  approval_status="$("$leapview" api call getDeliveryPublicationApproval \
    --target "$demo_target" \
    --token "$release_token" \
    --path "project=$project_id" \
    --path "publication=$publication_id" \
    --path "approval=$approval_id")"
  [[ "$(jq -r '.status' <<<"$approval_status")" == "approved" ]] || {
    echo "demo publication approval status did not persist" >&2
    exit 1
  }
elif [[ "$publication_status" != "committed" ]]; then
  echo "demo publication has unexpected status $publication_status" >&2
  exit 1
fi

for _ in $(seq 1 120); do
  publication_status_json="$("$leapview" api call getDeliveryPublicationEvidence \
    --target "$demo_target" \
    --token "$release_token" \
    --path "project=$project_id" \
    --path "publication=$publication_id")"
  publication_status="$(jq -r '.status' <<<"$publication_status_json")"
  case "$publication_status" in
    committed) break ;;
    rejected|indeterminate)
      echo "demo publication ended in $publication_status" >&2
      exit 1
      ;;
  esac
  sleep 2
done
if [[ "$publication_status" != "committed" ]]; then
  echo "demo publication did not become committed" >&2
  exit 1
fi

generation_status="$("$leapview" api call getDeliveryGenerationStatus \
  --target "$demo_target" \
  --token "$release_token" \
  --path "project=$project_id" \
  --path "generation=$generation_id")"
[[ "$(jq -r '.status' <<<"$generation_status")" == "active" ]] || {
  echo "demo serving generation did not become active" >&2
  exit 1
}

"$leapview" api call getProject \
  --target "$demo_target" \
  --token "$release_token" \
  --path "project=$project_id" >/dev/null

jq -e --arg project "$project_id" --arg candidate "$candidate_id" --arg generation "$generation_id" '
  .projectId == $project and .candidateId == $candidate and .generationId == $generation
' <<<"$publication_status_json" >/dev/null
curl --fail --silent --show-error --max-time 15 "$demo_target/readyz" >/dev/null
printf 'published the canonical project showcase to %s\n' "$demo_target"
