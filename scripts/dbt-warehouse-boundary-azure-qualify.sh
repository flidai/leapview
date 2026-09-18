#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PUBLISHED="$ROOT/examples/dbt-warehouse-boundary/published"
readonly EXPECTED_MARTS=$'dim_customers.parquet\nfct_orders.parquet'

fail() {
  printf 'Azure warehouse-boundary qualification failed: %s\n' "$1" >&2
  exit 1
}

require_env() {
  local name
  for name in "$@"; do
    [[ -n "${!name:-}" ]] || fail "required environment variable $name is unavailable"
  done
}

require_commands() {
  local name
  for name in "$@"; do
    command -v "$name" >/dev/null 2>&1 || fail "required command $name is unavailable"
  done
}

require_safe_name() {
  local label="$1" value="$2"
  [[ "$value" =~ ^[A-Za-z0-9][A-Za-z0-9._/-]*$ ]] || fail "$label contains unsupported characters"
}

require_safe_scope() {
  local label="$1" value="$2"
  [[ "$value" =~ ^[A-Za-z0-9][A-Za-z0-9_.:-]*$ ]] || fail "$label contains unsupported characters"
}

assert_empty_prefix() {
  local account="$1" container="$2" prefix="$3" count
  count="$(az storage blob list \
    --auth-mode login \
    --account-name "$account" \
    --container-name "$container" \
    --prefix "$prefix/" \
    --query 'length(@)' \
    --output tsv \
    --only-show-errors)"
  [[ "$count" == "0" ]] || fail "immutable qualification prefix already exists"
}

remote_file_set() {
  local account="$1" container="$2" prefix="$3"
  az storage blob list \
    --auth-mode login \
    --account-name "$account" \
    --container-name "$container" \
    --prefix "$prefix/" \
    --query '[].name' \
    --output tsv \
    --only-show-errors |
    sed "s|^$prefix/||" |
    LC_ALL=C sort
}

verify_exact_file_set() {
  local account="$1" container="$2" prefix="$3" actual
  actual="$(remote_file_set "$account" "$container" "$prefix")"
  [[ "$actual" == "$EXPECTED_MARTS" ]] || return 1
}

verify_download_checksum() {
  local account="$1" container="$2" blob="$3" expected="$4" destination actual
  destination="$(mktemp)"
  trap 'rm -f "$destination"' RETURN
  az storage blob download \
    --auth-mode login \
    --account-name "$account" \
    --container-name "$container" \
    --name "$blob" \
    --file "$destination" \
    --overwrite true \
    --no-progress \
    --output none \
    --only-show-errors
  actual="$(sha256sum "$destination" | awk '{print $1}')"
  [[ "$actual" == "$expected" ]] || fail "downloaded publication checksum differs from producer evidence"
  rm -f "$destination"
  trap - RETURN
}

storage_request_status() {
  local method="$1" account="$2" container="$3" blob="$4" conditional="${5:-}" token auth_file response_headers status error_code
  require_safe_name "storage account" "$account"
  require_safe_name "container" "$container"
  require_safe_name "blob path" "$blob"
  token="$(az account get-access-token \
    --resource https://storage.azure.com/ \
    --query accessToken \
    --output tsv \
    --only-show-errors)"
  [[ -n "$token" ]] || fail "Azure Storage access token is unavailable"
  auth_file="$(mktemp)"
  response_headers="$(mktemp)"
  chmod 600 "$auth_file"
  trap 'rm -f "$auth_file" "$response_headers"' RETURN
  printf 'header = "Authorization: Bearer %s"\n' "$token" >"$auth_file"
  unset token
  local -a args=(
    --config "$auth_file"
    --silent
    --show-error
    --output /dev/null
    --dump-header "$response_headers"
    --write-out '%{http_code}'
    --request "$method"
    --header 'x-ms-version: 2023-11-03'
    --header "x-ms-date: $(LC_ALL=C date -u '+%a, %d %b %Y %H:%M:%S GMT')"
    --header "x-ms-client-request-id: leapview-${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-0}"
  )
  if [[ "$method" == "PUT" ]]; then
    args+=(
      --header 'x-ms-blob-type: BlockBlob'
      --header 'If-Match: "leapview-never-match"'
      --data-binary ''
    )
  elif [[ -n "$conditional" ]]; then
    args+=(--header "$conditional")
  fi
  status="$(curl "${args[@]}" "https://${account}.blob.core.windows.net/${container}/${blob}")"
  error_code="$(awk 'tolower($1) == "x-ms-error-code:" { code=$2; sub(/\r$/, "", code) } END { print code }' "$response_headers")"
  rm -f "$auth_file" "$response_headers"
  trap - RETURN
  printf '%s %s\n' "$status" "$error_code"
}

expect_storage_status() {
  local expected="$1" method="$2" account="$3" container="$4" blob="$5" label="$6" result status error_code
  result="$(storage_request_status "$method" "$account" "$container" "$blob")"
  read -r status error_code <<<"$result"
  [[ "$status" == "$expected" && "$error_code" == "AuthorizationPermissionMismatch" ]] ||
    fail "$label returned HTTP $status/$error_code instead of the required $expected/AuthorizationPermissionMismatch"
}

preflight() {
  require_env \
    DBT_PRODUCER_CLIENT_ID \
    DBT_QUALIFICATION_SOURCE_CLIENT_ID \
    DBT_QUALIFICATION_DUCKLAKE_CLIENT_ID \
    AZURE_STORAGE_ACCOUNT \
    AZURE_SOURCE_CONTAINER \
    AZURE_PUBLICATION_CONTAINER \
    DBT_QUALIFICATION_DUCKLAKE_STORAGE_ACCOUNT \
    DBT_QUALIFICATION_DUCKLAKE_CONTAINER \
    DBT_QUALIFICATION_PROJECT_UID \
    DBT_QUALIFICATION_ENVIRONMENT
  [[ "$DBT_PRODUCER_CLIENT_ID" != "$DBT_QUALIFICATION_SOURCE_CLIENT_ID" ]] || fail "producer and Source identities must differ"
  [[ "$DBT_PRODUCER_CLIENT_ID" != "$DBT_QUALIFICATION_DUCKLAKE_CLIENT_ID" ]] || fail "producer and DuckLake identities must differ"
  [[ "$DBT_QUALIFICATION_SOURCE_CLIENT_ID" != "$DBT_QUALIFICATION_DUCKLAKE_CLIENT_ID" ]] || fail "Source and DuckLake identities must differ"
  [[ "$AZURE_SOURCE_CONTAINER" != "$AZURE_PUBLICATION_CONTAINER" ]] || fail "bounded producer input and publication containers must differ"
  [[ "$AZURE_STORAGE_ACCOUNT/$AZURE_PUBLICATION_CONTAINER" != "$DBT_QUALIFICATION_DUCKLAKE_STORAGE_ACCOUNT/$DBT_QUALIFICATION_DUCKLAKE_CONTAINER" ]] || fail "producer publication and DuckLake storage scopes must differ"
  require_safe_name "producer storage account" "$AZURE_STORAGE_ACCOUNT"
  require_safe_name "producer source container" "$AZURE_SOURCE_CONTAINER"
  require_safe_name "producer publication container" "$AZURE_PUBLICATION_CONTAINER"
  require_safe_name "DuckLake storage account" "$DBT_QUALIFICATION_DUCKLAKE_STORAGE_ACCOUNT"
  require_safe_name "DuckLake container" "$DBT_QUALIFICATION_DUCKLAKE_CONTAINER"
  require_safe_scope "LeapView Project" "$DBT_QUALIFICATION_PROJECT_UID"
  require_safe_scope "LeapView environment" "$DBT_QUALIFICATION_ENVIRONMENT"
  echo "Validated three distinct Azure qualification identities and storage scopes"
}

publish() {
  require_commands az curl sha256sum awk sed sort
  require_env \
    AZURE_STORAGE_ACCOUNT \
    AZURE_PUBLICATION_CONTAINER \
    DBT_QUALIFICATION_DUCKLAKE_STORAGE_ACCOUNT \
    DBT_QUALIFICATION_DUCKLAKE_CONTAINER \
    GITHUB_REPOSITORY GITHUB_RUN_ID GITHUB_RUN_ATTEMPT GITHUB_SHA GITHUB_OUTPUT
  [[ -s "$PUBLISHED/dim_customers.parquet" && -s "$PUBLISHED/fct_orders.parquet" ]] || fail "verified dbt Parquet output is unavailable"
  require_safe_name "GitHub repository" "$GITHUB_REPOSITORY"
  require_safe_name "GitHub run ID" "$GITHUB_RUN_ID"
  require_safe_name "GitHub run attempt" "$GITHUB_RUN_ATTEMPT"
  require_safe_name "Git revision" "$GITHUB_SHA"

  local account="$AZURE_STORAGE_ACCOUNT" container="$AZURE_PUBLICATION_CONTAINER"
  local prefix partial_prefix dim_sha orders_sha mart
  prefix="dbt-warehouse-boundary/qualification/${GITHUB_REPOSITORY}/${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}-${GITHUB_SHA}"
  partial_prefix="${prefix}-partial"
  dim_sha="$(sha256sum "$PUBLISHED/dim_customers.parquet" | awk '{print $1}')"
  orders_sha="$(sha256sum "$PUBLISHED/fct_orders.parquet" | awk '{print $1}')"
  assert_empty_prefix "$account" "$container" "$prefix"
  assert_empty_prefix "$account" "$container" "$partial_prefix"

  az storage blob upload \
    --auth-mode login --account-name "$account" --container-name "$container" \
    --name "$partial_prefix/dim_customers.parquet" \
    --file "$PUBLISHED/dim_customers.parquet" \
    --overwrite false --no-progress --output none --only-show-errors
  if verify_exact_file_set "$account" "$container" "$partial_prefix"; then
    fail "partial publication unexpectedly satisfied the exact mart set"
  fi

  while IFS= read -r mart; do
    az storage blob upload \
      --auth-mode login --account-name "$account" --container-name "$container" \
      --name "$prefix/$mart" \
      --file "$PUBLISHED/$mart" \
      --overwrite false --no-progress --output none --only-show-errors
  done <<<"$EXPECTED_MARTS"
  verify_exact_file_set "$account" "$container" "$prefix" || fail "complete publication does not contain the exact mart set"

  if az storage blob upload \
    --auth-mode login --account-name "$account" --container-name "$container" \
    --name "$prefix/dim_customers.parquet" \
    --file "$PUBLISHED/dim_customers.parquet" \
    --overwrite false --no-progress --output none --only-show-errors >/dev/null 2>&1; then
    fail "immutable publication accepted an overwrite"
  fi
  verify_download_checksum "$account" "$container" "$prefix/dim_customers.parquet" "$dim_sha"
  verify_download_checksum "$account" "$container" "$prefix/fct_orders.parquet" "$orders_sha"

  expect_storage_status 403 GET \
    "$DBT_QUALIFICATION_DUCKLAKE_STORAGE_ACCOUNT" "$DBT_QUALIFICATION_DUCKLAKE_CONTAINER" \
    "qualification/producer-deny-${GITHUB_RUN_ID}" \
    "producer cross-scope DuckLake read"
  expect_storage_status 403 PUT \
    "$DBT_QUALIFICATION_DUCKLAKE_STORAGE_ACCOUNT" "$DBT_QUALIFICATION_DUCKLAKE_CONTAINER" \
    "qualification/producer-deny-${GITHUB_RUN_ID}-write" \
    "producer cross-scope DuckLake write"

  {
    printf 'publication_prefix=%s\n' "$prefix"
    printf 'partial_prefix=%s\n' "$partial_prefix"
    printf 'dim_customers_sha256=%s\n' "$dim_sha"
    printf 'fct_orders_sha256=%s\n' "$orders_sha"
  } >>"$GITHUB_OUTPUT"
  echo "Qualified immutable producer publication and producer-publication-checksum evidence"
}

source_read() {
  require_commands az curl sha256sum awk sed sort
  require_env \
    AZURE_STORAGE_ACCOUNT AZURE_SOURCE_CONTAINER AZURE_PUBLICATION_CONTAINER \
    DBT_QUALIFICATION_DUCKLAKE_STORAGE_ACCOUNT DBT_QUALIFICATION_DUCKLAKE_CONTAINER \
    PUBLICATION_PREFIX EXPECTED_DIM_CUSTOMERS_SHA256 EXPECTED_FCT_ORDERS_SHA256 GITHUB_RUN_ID
  verify_exact_file_set "$AZURE_STORAGE_ACCOUNT" "$AZURE_PUBLICATION_CONTAINER" "$PUBLICATION_PREFIX" || fail "Source identity cannot observe the exact publication"
  verify_download_checksum "$AZURE_STORAGE_ACCOUNT" "$AZURE_PUBLICATION_CONTAINER" "$PUBLICATION_PREFIX/dim_customers.parquet" "$EXPECTED_DIM_CUSTOMERS_SHA256"
  verify_download_checksum "$AZURE_STORAGE_ACCOUNT" "$AZURE_PUBLICATION_CONTAINER" "$PUBLICATION_PREFIX/fct_orders.parquet" "$EXPECTED_FCT_ORDERS_SHA256"

  expect_storage_status 403 PUT "$AZURE_STORAGE_ACCOUNT" "$AZURE_PUBLICATION_CONTAINER" "$PUBLICATION_PREFIX/dim_customers.parquet" "Source overwrite probe"
  expect_storage_status 403 PUT "$AZURE_STORAGE_ACCOUNT" "$AZURE_PUBLICATION_CONTAINER" "$PUBLICATION_PREFIX/source-create-probe" "Source create probe"
  expect_storage_status 403 DELETE "$AZURE_STORAGE_ACCOUNT" "$AZURE_PUBLICATION_CONTAINER" "$PUBLICATION_PREFIX/source-delete-probe" "Source delete probe"
  expect_storage_status 403 GET "$AZURE_STORAGE_ACCOUNT" "$AZURE_SOURCE_CONTAINER" "dbt-warehouse-boundary/raw_orders.csv" "Source cross-scope producer-input read"
  expect_storage_status 403 GET "$DBT_QUALIFICATION_DUCKLAKE_STORAGE_ACCOUNT" "$DBT_QUALIFICATION_DUCKLAKE_CONTAINER" "qualification/source-deny-${GITHUB_RUN_ID}" "Source cross-scope DuckLake read"
  expect_storage_status 403 PUT "$DBT_QUALIFICATION_DUCKLAKE_STORAGE_ACCOUNT" "$DBT_QUALIFICATION_DUCKLAKE_CONTAINER" "qualification/source-deny-${GITHUB_RUN_ID}-write" "Source cross-scope DuckLake write"
  echo "Qualified checksum-preserving Source reads and fail-closed create, overwrite, and delete denial"
}

ducklake_write() {
  require_commands az curl sha256sum awk
  require_env \
    AZURE_STORAGE_ACCOUNT AZURE_PUBLICATION_CONTAINER DBT_QUALIFICATION_DUCKLAKE_STORAGE_ACCOUNT DBT_QUALIFICATION_DUCKLAKE_CONTAINER \
    DBT_QUALIFICATION_PROJECT_UID DBT_QUALIFICATION_ENVIRONMENT PUBLICATION_PREFIX \
    GITHUB_REPOSITORY GITHUB_RUN_ID GITHUB_RUN_ATTEMPT GITHUB_SHA
  local probe source downloaded expected actual
  probe="qualification/${DBT_QUALIFICATION_PROJECT_UID}/${DBT_QUALIFICATION_ENVIRONMENT}/${GITHUB_REPOSITORY}/${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}-${GITHUB_SHA}/write-probe"
  source="$(mktemp)"
  downloaded="$(mktemp)"
  trap 'rm -f "$source" "$downloaded"' EXIT
  printf 'bounded DuckLake qualification %s %s\n' "$GITHUB_RUN_ID" "$GITHUB_SHA" >"$source"
  expected="$(sha256sum "$source" | awk '{print $1}')"
  az storage blob upload \
    --auth-mode login --account-name "$DBT_QUALIFICATION_DUCKLAKE_STORAGE_ACCOUNT" --container-name "$DBT_QUALIFICATION_DUCKLAKE_CONTAINER" \
    --name "$probe" --file "$source" --overwrite false --no-progress --output none --only-show-errors
  az storage blob download \
    --auth-mode login --account-name "$DBT_QUALIFICATION_DUCKLAKE_STORAGE_ACCOUNT" --container-name "$DBT_QUALIFICATION_DUCKLAKE_CONTAINER" \
    --name "$probe" --file "$downloaded" --overwrite true --no-progress --output none --only-show-errors
  actual="$(sha256sum "$downloaded" | awk '{print $1}')"
  [[ "$actual" == "$expected" ]] || fail "DuckLake write-side round trip changed the qualified bytes"
  az storage blob delete \
    --auth-mode login --account-name "$DBT_QUALIFICATION_DUCKLAKE_STORAGE_ACCOUNT" --container-name "$DBT_QUALIFICATION_DUCKLAKE_CONTAINER" \
    --name "$probe" --output none --only-show-errors

  expect_storage_status 403 GET "$AZURE_STORAGE_ACCOUNT" "$AZURE_PUBLICATION_CONTAINER" "$PUBLICATION_PREFIX/dim_customers.parquet" "DuckLake cross-scope publication read"
  expect_storage_status 403 PUT "$AZURE_STORAGE_ACCOUNT" "$AZURE_PUBLICATION_CONTAINER" "$PUBLICATION_PREFIX/ducklake-create-probe" "DuckLake cross-scope publication write"
  echo "Qualified DuckLake-owned write scope and producer-publication denial"
}

case "${1:-}" in
  preflight) preflight ;;
  publish) publish ;;
  source-read) source_read ;;
  ducklake-write) ducklake_write ;;
  *) echo "Usage: $0 preflight|publish|source-read|ducklake-write" >&2; exit 2 ;;
esac
