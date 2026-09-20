#!/usr/bin/env bash
set -euo pipefail

demo_target="${DEMO_TARGET:-https://demo.leapview.dev}"
source_revision="${DEMO_SOURCE_REVISION:?Set DEMO_SOURCE_REVISION}"
project_id="${DEMO_PROJECT_ID:?Set DEMO_PROJECT_ID}"
publisher_client_id="${DEMO_RELEASE_CLIENT_ID:?Set DEMO_RELEASE_CLIENT_ID}"
publisher_client_secret="${DEMO_RELEASE_CLIENT_SECRET:?Set DEMO_RELEASE_CLIENT_SECRET}"

[[ "$source_revision" =~ ^[0-9a-f]{40}$ ]] || { echo 'DEMO_SOURCE_REVISION must be a full Git commit identity' >&2; exit 64; }
[[ "$demo_target" =~ ^https://[A-Za-z0-9.-]+$ ]] || { echo 'DEMO_TARGET must be an HTTPS origin' >&2; exit 64; }
for command in curl jq; do
  command -v "$command" >/dev/null || { echo "required command is unavailable: $command" >&2; exit 69; }
done

response="$(curl --fail --silent --show-error --request POST \
  --header 'Content-Type: application/x-www-form-urlencoded' \
  --data-urlencode 'grant_type=client_credentials' \
  --data-urlencode "client_id=$publisher_client_id" \
  --data-urlencode "client_secret=$publisher_client_secret" \
  --data-urlencode "project_id=$project_id" \
  --data-urlencode 'scope=PROJECT_ADMIN' \
  --data-urlencode 'lifetime_seconds=900' \
  "$demo_target/oauth/token")"
release_token="$(jq -er '.access_token | strings | select(length > 0)' <<<"$response")"
unset publisher_client_secret

api_get() {
  curl --fail --silent --show-error --max-time 30 \
    --header "Authorization: Bearer $release_token" "$demo_target$1"
}
capabilities="$(api_get /api/v1/capabilities)"
jq -e --arg revision "$source_revision" '
  .apiVersion == "v1" and
  .deliveryMode == "native_postgres" and
  .buildRevision == $revision and
  .buildDirty == false and
  .buildDevelopment == true
' <<<"$capabilities" >/dev/null || { echo 'Runtime identity check failed' >&2; exit 1; }
runtime_revision="$(jq -er '.buildRevision' <<<"$capabilities")"

printf 'Exact deployed runtime identity passed: %s\n' "$runtime_revision"
project="$(api_get "/api/v1/projects/$project_id")"
jq -e --arg project "$project_id" '.id == $project' <<<"$project" >/dev/null || {
  echo 'Project identity check failed' >&2; exit 1;
}
# The active serving generation is authoritative. Legacy release/deployment
# projection fields on getProject are optional for native delivery projects.
catalog="$(api_get "/api/v1/projects/$project_id/authoring/catalog")"
jq -e --arg project "$project_id" '
  any(.items[]; .projectId == $project and .id == "dashboard:cfo-command-center" and
    .semanticModel == "semantic-model:finance" and .source == "project" and .status == "published" and
    .servingIdentity.projectId == $project and ((.servingIdentity.generationId // "") | length > 0))
' <<<"$catalog" >/dev/null || {
  jq -c '{count,items:[.items[]|{id,source,status,semanticModel,servingIdentity}]}' <<<"$catalog" >&2
  echo 'Active CFO catalog check failed' >&2; exit 1;
}

curl --fail --silent --show-error --max-time 15 "$demo_target/readyz" >/dev/null
login_code="$(curl --silent --show-error --max-time 15 --output /dev/null --write-out '%{http_code}' "$demo_target/login")"
[[ "$login_code" == 200 ]]

printf 'Hosted CFO verification passed: runtime=%s project=%s active serving generation and CFO Command Center with Finance model present; readyz/login healthy\n' "$runtime_revision" "$project_id"
