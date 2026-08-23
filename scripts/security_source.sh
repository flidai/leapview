#!/usr/bin/env bash
set -euo pipefail

readonly TRIVY_IMAGE="aquasec/trivy:0.74.0@sha256:62b1e65e8869bc4b4c6aa4fa2b21595256c7c2f6018a9d9ad61caf87187c1969"

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

exception_contract=""
if [[ -f .security/coverage.yaml && -f internal/app/tools/securitypolicy/main.go ]]; then
  exception_contract="$(mktemp)"
  trivy_output="$(mktemp)"
  trivy_diagnostics="$(mktemp)"
  trap 'rm -f "$exception_contract" "$trivy_output" "$trivy_diagnostics"' EXIT
  go run ./internal/app/tools/securitypolicy --root "$repo_root" --exceptions-json >"$exception_contract" || {
    printf 'source security: validated exception contract is unavailable\n' >&2
    exit 1
  }
fi

match_exception() {
  local rule="$1" resource="$2" severity="${3:-}" finding_class="${4:-}"
  [[ -n "$exception_contract" ]] || return 1
  jq -e \
    --arg scanner trivy \
    --arg rule "$rule" \
    --arg resource "$resource" \
    --arg severity "$severity" \
    --arg class "$finding_class" \
    '((($severity | length) == 0) or (($severity | ascii_upcase) == "HIGH") or (($severity | ascii_upcase) == "CRITICAL") or (($scanner | ascii_downcase) == "provenance") or (($scanner | ascii_downcase) == "release-signing") or (($class | ascii_downcase) == "provenance") or (($class | ascii_downcase) == "release-signing")) | not) and
      any(.exceptions[]?; .scanner == $scanner and .rule == $rule and .resource == $resource)' \
    "$exception_contract" >/dev/null 2>&1
}

all_source_findings_waived() {
  local output="$1" findings rule resource severity finding_class
  [[ -n "$exception_contract" ]] || return 1
  # Trivy's JSON output has stable IDs and targets for vulnerabilities,
  # misconfigurations, and secrets. Missing identity is never waivable.
  findings="$(jq -r '
    [ .Results[]? as $result |
      ($result.Vulnerabilities[]? | [(.VulnerabilityID // ""), (.PkgName // ""), (.Severity // ""), ""]),
      ($result.Misconfigurations[]? | [(.ID // ""), ($result.Target // .ArtifactName // ""), (.Severity // ""), ""]),
      ($result.Secrets[]? | [(.RuleID // ""), ($result.Target // ""), (.Severity // ""), "secret"]) |
      @tsv ] | .[]
  ' "$output" 2>/dev/null)" || return 1
  [[ -n "$findings" ]] || return 1
  while IFS=$'\t' read -r rule resource severity finding_class; do
    [[ -n "$rule" && -n "$resource" ]] || return 1
    match_exception "$rule" "$resource" "$severity" "$finding_class" || return 1
  done <<<"$findings"
}

./scripts/security_secrets.sh

docker info >/dev/null
if [[ -z "$exception_contract" ]]; then
  docker run --rm \
    -v "$repo_root:/work:ro" \
    -w /work \
    "$TRIVY_IMAGE" \
    fs \
    --scanners secret,misconfig \
    --severity HIGH,CRITICAL \
    --exit-code 1 \
    --ignore-unfixed=false \
    --skip-dirs node_modules \
    --skip-dirs .data \
    --skip-dirs .tmp \
    .
  exit
fi

status=0
docker run --rm \
  -v "$repo_root:/work:ro" \
  -w /work \
  "$TRIVY_IMAGE" \
  fs \
  --scanners secret,misconfig \
  --severity HIGH,CRITICAL \
  --exit-code 1 \
  --ignore-unfixed=false \
  --skip-dirs node_modules \
  --skip-dirs .data \
  --skip-dirs .tmp \
  --format json \
  . >"$trivy_output" 2>"$trivy_diagnostics" || status=$?
if ((status == 0)); then
  exit 0
fi
if all_source_findings_waived "$trivy_output"; then
  printf 'trivy source scan: all findings match exact, active exceptions\n'
  exit 0
fi
cat "$trivy_diagnostics" "$trivy_output"
exit "$status"
