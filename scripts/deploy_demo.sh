#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
demo_target="${DEMO_TARGET:-https://demo.leapview.dev}"
demo_dataset="${DEMO_DATASET:-olist}"
case "$demo_dataset" in
  olist)
    source_root="$repo_root/dashboards"
    data_link="$repo_root/.data/olist"
    data_connection="olist"
    bootstrap_tool="./internal/app/tools/bootstrapolist"
    ;;
  cfo)
    source_root="$repo_root/dashboards/experiments/cfo-demo"
    data_link="$repo_root/.data/cfo-demo"
    data_connection="finance_files"
    bootstrap_tool="./internal/app/tools/bootstrapfinance"
    ;;
  *)
    echo "DEMO_DATASET must be olist or cfo" >&2
    exit 64
    ;;
esac
source_revision="${DEMO_SOURCE_REVISION:?Set DEMO_SOURCE_REVISION to the deployed Git revision}"
publisher_client_id="${DEMO_PUBLISHER_CLIENT_ID:?Set DEMO_PUBLISHER_CLIENT_ID}"
publisher_client_secret="${DEMO_PUBLISHER_CLIENT_SECRET:?Set DEMO_PUBLISHER_CLIENT_SECRET}"
release_client_id="${DEMO_RELEASE_CLIENT_ID:?Set DEMO_RELEASE_CLIENT_ID}"
release_client_secret="${DEMO_RELEASE_CLIENT_SECRET:?Set DEMO_RELEASE_CLIENT_SECRET}"
project_id="${DEMO_PROJECT_ID:?Set DEMO_PROJECT_ID to the durable target ProjectUID}"
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
command -v python3 >/dev/null || {
  echo "required command is unavailable: python3" >&2
  exit 69
}

client_contract="$repo_root/scripts/demo_client_contract.py"
permission_profile="${DEMO_PERMISSION_PROFILE:?Set DEMO_PERMISSION_PROFILE from trusted release or predecessor metadata}"
publisher_scope="$(python3 "$client_contract" --profile "$permission_profile" --role publisher --source-revision "$source_revision")"
release_scope="$(python3 "$client_contract" --profile "$permission_profile" --role release)"
python3 "$client_contract" --validate-environment

# Synthetic qualification uses the same publication commands but cannot
# substitute source/data on the public deployment path.
fixture_source="${DEMO_FIXTURE_SOURCE_ROOT:-}"
fixture_data="${DEMO_FIXTURE_DATA_PATH:-}"
if [[ -n "$fixture_source" || -n "$fixture_data" ]]; then
  if [[ "${DEMO_CLONE_ONLY:-}" != "1" || "$fixture_source" != /* || "$fixture_data" != /* || ! -d "$fixture_source" || ! -d "$fixture_data" ]]; then
    echo "synthetic publication fixtures require both absolute directories and clone-only transport" >&2
    exit 64
  fi
  source_root="$fixture_source"
fi

demo_curl() {
  if [[ "${DEMO_CLONE_ONLY:-}" == "1" ]]; then
    local clone_proxy="${DEMO_CLONE_PROXY:-}"
    if [[ ! "$clone_proxy" =~ ^http://127\.0\.0\.1:[0-9]+$ ]]; then
      echo "clone-only publication requires a loopback DEMO_CLONE_PROXY" >&2
      return 64
    fi
    # Pin curl explicitly. Its redirect support stays disabled and both
    # protocol lists exclude alternate origins if the command changes later.
    command curl --proxy "$clone_proxy" --noproxy '' --proto '=https' \
      --proto-redir '=https' --max-redirs 0 "$@"
  else
    command curl "$@"
  fi
}

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
  response="$(demo_curl --fail --silent --show-error \
    --request POST \
    --header 'Content-Type: application/x-www-form-urlencoded' \
    --data-urlencode 'grant_type=client_credentials' \
    --data-urlencode "client_id=$client_id" \
    --data-urlencode "client_secret=$client_secret" \
    --data-urlencode "project_id=$project_id" \
    --data-urlencode "scope=$scope" \
    --data-urlencode 'lifetime_seconds=1800' \
    "$demo_target/oauth/token")"
  token="$(jq -er '.access_token | strings | select(length > 0)' <<<"$response")"
  printf '%s' "$token"
}

publisher_token="$(exchange_workload_token \
  "$publisher_client_id" \
  "$publisher_client_secret" \
  "$publisher_scope")"
release_token="$(exchange_workload_token \
  "$release_client_id" \
  "$release_client_secret" \
  "$release_scope")"
unset publisher_client_secret release_client_secret

leapview="$temporary_directory/leapview"

cd "$repo_root"
go build -o "$leapview" ./cmd/leapview

# This workflow publishes content only; runtime rollout is owned by the demo
# platform. Bind every publication to the already-running immutable runtime's
# authenticated compatibility identity before sending project data.
runtime_capabilities="$("$leapview" api call getCapabilities \
  --target "$demo_target" \
  --token "$publisher_token")"
runtime_revision="$(jq -er '.buildRevision | strings | select(test("^[0-9a-f]{40}$"))' <<<"$runtime_capabilities")"
jq -e --arg source_revision "$source_revision" '
  .apiVersion == "v1" and
  .deliveryMode == "native_postgres" and
  .buildRevision == $source_revision and
  .buildDirty == false and
  .buildDevelopment == true
' <<<"$runtime_capabilities" >/dev/null || {
  jq -c '{apiVersion,deliveryMode,buildRevision,buildDirty,buildDevelopment}' <<<"$runtime_capabilities" >&2
  echo "demo runtime does not satisfy the content-publication compatibility contract" >&2
  exit 1
}
go run ./internal/app/tools/configgen
if [[ -n "$fixture_data" ]]; then
  data_path="$(cd -P "$fixture_data" && pwd)"
else
  go run "$bootstrap_tool" --shared-cache --out "$data_link"
  data_path="$(cd -P "$data_link" && pwd)"
fi
"$leapview" data sync \
  --source-root "$source_root" \
  --connection "$data_connection" \
  --from "$data_path" \
  --target "$demo_target" \
  --project-id "$project_id" \
  --token "$publisher_token"

# Delivery is deliberately split into the target-owned plan, build, and
# publication commands. Each command persists an immutable checkpoint that
# the next command resolves, so the candidate cannot be redirected to another
# project or target by a later invocation.
plan="$("$leapview" plan --source-root "$source_root" \
  --target "$demo_target" \
  --project-id "$project_id" \
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
publication_target_id="$(jq -er '.targetId | strings | select(length > 0)' <<<"$publication_status_json")"
publication_environment="$(jq -er '.environment | strings | select(length > 0)' <<<"$publication_status_json")"
jq -e --arg publication "$publication_id" --arg project "$project_id" \
  --arg candidate "$candidate_id" --arg generation "$generation_id" \
  --arg target_id "$publication_target_id" --arg environment "$publication_environment" '
  .id == $publication and .projectId == $project and
  .candidateId == $candidate and .generationId == $generation and
  .targetId == $target_id and .environment == $environment
' <<<"$publication_status_json" >/dev/null || {
  echo "committed publication evidence did not preserve the expected runtime identities" >&2
  exit 1
}

generation_status="$(DEMO_GENERATION_TOKEN="$release_token" \
  python3 "$client_contract" --wait-generation \
    --target "$demo_target" \
    --project "$project_id" \
    --generation "$generation_id" \
    --candidate "$candidate_id" \
    --target-id "$publication_target_id" \
    --environment "$publication_environment" \
    --timeout 90 \
    --poll-interval 2)"
jq -e --arg project "$project_id" --arg generation "$generation_id" --arg candidate "$candidate_id" \
  --arg target_id "$publication_target_id" --arg environment "$publication_environment" '
  .status == "active" and .projectId == $project and
  .id == $generation and .candidateId == $candidate and
  .targetId == $target_id and .environment == $environment
' <<<"$generation_status" >/dev/null || {
  echo "demo serving generation did not become active with the expected identities" >&2
  exit 1
}

jq -e --arg project "$project_id" --arg candidate "$candidate_id" --arg generation "$generation_id" '
  .projectId == $project and .candidateId == $candidate and .generationId == $generation
' <<<"$publication_status_json" >/dev/null
demo_curl --fail --silent --show-error --max-time 15 "$demo_target/readyz" >/dev/null
mapfile -t browser_entry < <(demo_curl --silent --show-error --max-time 15 \
  --output /dev/null \
  --write-out '%{http_code}\n%{redirect_url}\n' \
  "$demo_target/")
if [[ "${browser_entry[0]:-}" != "302" || "${browser_entry[1]:-}" != "$demo_target/login" ]]; then
  echo "demo browser entry did not redirect unauthenticated visitors to /login" >&2
  exit 1
fi
login_page="$(demo_curl --fail --silent --show-error --max-time 15 "$demo_target/login")"
if [[ "$login_page" != *"<title>LeapView Login</title>"* ]]; then
  echo "demo login page did not render the branded sign-in surface" >&2
  exit 1
fi
printf 'published source %s to compatible runtime %s at %s\n' "$source_revision" "$runtime_revision" "$demo_target"
if [[ -n "${DEMO_PUBLICATION_RECEIPT:-}" ]]; then
  if [[ "$DEMO_PUBLICATION_RECEIPT" != /* || -e "$DEMO_PUBLICATION_RECEIPT" || -L "$DEMO_PUBLICATION_RECEIPT" ]]; then
    echo "DEMO_PUBLICATION_RECEIPT must be an absolute new file path" >&2
    exit 64
  fi
  receipt_directory="$(dirname "$DEMO_PUBLICATION_RECEIPT")"
  [[ -d "$receipt_directory" ]] || {
    echo "DEMO_PUBLICATION_RECEIPT parent directory must exist" >&2
    exit 64
  }
  umask 077
  jq -n --arg project "$project_id" --arg candidate "$candidate_id" \
    --arg publication "$publication_id" --arg generation "$generation_id" \
    --arg status "$publication_status" --arg source "$source_revision" \
    --arg runtime "$runtime_revision" --arg profile "$permission_profile" \
    --arg target "$demo_target" \
    '{projectId:$project,candidateId:$candidate,publicationId:$publication,generationId:$generation,status:$status,sourceRevision:$source,runtimeRevision:$runtime,permissionProfile:$profile,target:$target}' \
    > "$DEMO_PUBLICATION_RECEIPT"
fi
if [[ -n "${GITHUB_STEP_SUMMARY:-}" && "${DEMO_CLONE_ONLY:-}" != "1" ]]; then
  printf '\n### Content publication\n\n- Source: `%s`\n- Publication: `%s`\n- Active generation: `%s`\n' \
    "$source_revision" "$publication_id" "$generation_id" >> "$GITHUB_STEP_SUMMARY"
fi
