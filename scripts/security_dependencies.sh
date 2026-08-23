#!/usr/bin/env bash
set -euo pipefail

readonly GOVULNCHECK_VERSION="v1.6.0"
readonly AUDIT_LEVEL="critical"

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

exception_contract=""
if [[ -f .security/coverage.yaml && -f internal/app/tools/securitypolicy/main.go ]]; then
  exception_contract="$(mktemp)"
  trap 'rm -f "$exception_contract"' EXIT
  go run ./internal/app/tools/securitypolicy --root "$repo_root" --exceptions-json >"$exception_contract" || {
    printf 'dependency security: validated exception contract is unavailable\n' >&2
    exit 1
  }
fi

match_exception() {
  local scanner="$1" rule="$2" resource="$3" severity="${4:-}" finding_class="${5:-}"
  [[ -n "$exception_contract" ]] || return 1
  jq -e \
    --arg scanner "$scanner" \
    --arg rule "$rule" \
    --arg resource "$resource" \
    --arg severity "$severity" \
    --arg class "$finding_class" \
    '((($severity | length) == 0) or (($severity | ascii_upcase) == "HIGH") or (($severity | ascii_upcase) == "CRITICAL") or (($scanner | ascii_downcase) == "provenance") or (($scanner | ascii_downcase) == "release-signing") or (($class | ascii_downcase) == "provenance") or (($class | ascii_downcase) == "release-signing")) | not) and
      any(.exceptions[]?; .scanner == $scanner and .rule == $rule and .resource == $resource)' \
    "$exception_contract" >/dev/null 2>&1
}

all_dependency_findings_waived() {
  local scanner="$1" output="$2" findings rule resource severity
  [[ -n "$exception_contract" ]] || return 1
  case "$scanner" in
    govulncheck)
      findings="$(jq -s -r '
        [ .[] | select(.finding? != null) | .finding |
          [(.osv // "" | tostring), (.trace[0].module // "" | tostring), ((.severity // "") | tostring)] | @tsv ] | .[]
      ' "$output" 2>/dev/null)" || return 1
      [[ -n "$findings" ]] || return 1
      while IFS=$'\t' read -r rule resource severity; do
        # govulncheck does not consistently emit severity.  An unknown
        # severity cannot be proven below the non-waivable HIGH/CRITICAL
        # boundary, so the finding remains blocking.
        [[ -n "$rule" && -n "$resource" && -n "$severity" ]] || return 1
        match_exception "$scanner" "$rule" "$resource" "$severity" || return 1
      done <<<"$findings"
      ;;
    bun-audit)
      # Bun 1.3 emits a top-level package map.  Prefer a GHSA identifier from
      # the advisory URL, falling back to the stable numeric advisory id.
      findings="$(jq -r '
        [ to_entries[] as $entry | ($entry.value // [])[] |
          select(type == "object") |
          select((.severity // "" | ascii_downcase) == "critical") |
          [((.url // "" | capture("(?<id>GHSA-[A-Za-z0-9-]+)")?.id) // (.id // "") | tostring), $entry.key, ((.severity // "") | tostring)] |
          select(.[0] != "" and .[1] != "") | @tsv ] | .[]
      ' "$output" 2>/dev/null)" || return 1
      [[ -n "$findings" ]] || return 1
      while IFS=$'\t' read -r rule resource severity; do
        [[ -n "$rule" && -n "$resource" && -n "$severity" ]] || return 1
        match_exception "$scanner" "$rule" "$resource" "$severity" || return 1
      done <<<"$findings"
      ;;
    npm-audit)
      # npm audit v2 exposes package keys and advisory IDs in
      # `vulnerabilities[].via`. String-only `via` entries are unsupported:
      # without a stable rule identity we fail closed.
      jq -e '
        (.vulnerabilities // {}) as $v |
        (($v | to_entries | map(select((.value.via // [] | map(select(type == "object" and ((.source // .id // "") | tostring | length > 0))) | length == 0))) | length) == 0)
      ' "$output" >/dev/null 2>&1 || return 1
      findings="$(jq -r '
        [ (.vulnerabilities // {}) | to_entries[] as $entry |
          ($entry.value.via // [])[]? | select(type == "object") |
          select((.source // .id // "") | tostring | length > 0) |
          [((.source // .id) | tostring), $entry.key, ((.severity // $entry.value.severity // "") | tostring)] | @tsv ] | .[]
      ' "$output" 2>/dev/null)" || return 1
      [[ -n "$findings" ]] || return 1
      while IFS=$'\t' read -r rule resource severity; do
        [[ -n "$rule" && -n "$resource" && -n "$severity" ]] || return 1
        match_exception "$scanner" "$rule" "$resource" "$severity" || return 1
      done <<<"$findings"
      ;;
    *) return 1 ;;
  esac
}

bun_blocking_finding_count() {
  local output="$1"
  jq -er '
    [ to_entries[] | (.value // [])[] |
      select(type == "object" and ((.severity // "" | ascii_downcase) == "critical"))
    ] | length
  ' "$output"
}

scan_go_module() {
  local module_file="$1"
  local module_dir
  module_dir="$(dirname "$module_file")"
  printf 'govulncheck %s\n' "$module_dir"
  if [[ -z "$exception_contract" ]]; then
    (
      cd "$module_dir"
      go run "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}" ./...
    )
    return
  fi
  local output diagnostics status
  output="$(mktemp)"
  diagnostics="$(mktemp)"
  if (cd "$module_dir" && go run "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}" -json ./...) >"$output" 2>"$diagnostics"; then
    rm -f "$output" "$diagnostics"
    return
  else
    status=$?
  fi
  if all_dependency_findings_waived govulncheck "$output"; then
    printf 'govulncheck %s: all findings match exact, active exceptions\n' "$module_dir"
    rm -f "$output" "$diagnostics"
    return
  fi
  cat "$diagnostics" "$output"
  rm -f "$output" "$diagnostics"
  return "$status"
}

scan_bun_lock() {
  local lock_file="$1"
  local package_dir
  package_dir="$(dirname "$lock_file")"
  printf 'bun audit %s\n' "$package_dir"
  if [[ -z "$exception_contract" ]]; then
    (cd "$package_dir" && bun audit --audit-level "$AUDIT_LEVEL")
    return
  fi
  local output diagnostics status=0 blocking_count
  output="$(mktemp)"
  diagnostics="$(mktemp)"
  (cd "$package_dir" && bun audit --audit-level "$AUDIT_LEVEL" --json) >"$output" 2>"$diagnostics" || status=$?
  if ! blocking_count="$(bun_blocking_finding_count "$output")"; then
    cat "$diagnostics" "$output"
    rm -f "$output" "$diagnostics"
    printf 'bun audit %s: scanner output is not valid JSON\n' "$package_dir" >&2
    return 1
  fi
  if ((blocking_count == 0)); then
    printf 'bun audit %s: no Critical findings\n' "$package_dir"
    rm -f "$output" "$diagnostics"
    return
  fi
  ((status != 0)) || status=1
  if all_dependency_findings_waived bun-audit "$output"; then
    printf 'bun audit %s: all findings match exact, active exceptions\n' "$package_dir"
    rm -f "$output" "$diagnostics"
    return
  fi
  cat "$diagnostics" "$output"
  rm -f "$output" "$diagnostics"
  return "$status"
}

scan_npm_lock() {
  local lock_file="$1"
  local package_dir
  package_dir="$(dirname "$lock_file")"
  printf 'npm audit %s\n' "$package_dir"
  if [[ -z "$exception_contract" ]]; then
    (
      cd "$package_dir"
      npm audit --package-lock-only --audit-level="$AUDIT_LEVEL" --ignore-scripts
    )
    return
  fi
  local output diagnostics status
  output="$(mktemp)"
  diagnostics="$(mktemp)"
  if (
    cd "$package_dir"
    npm audit --package-lock-only --audit-level="$AUDIT_LEVEL" --ignore-scripts --json
  ) >"$output" 2>"$diagnostics"; then
    rm -f "$output" "$diagnostics"
    return
  else
    status=$?
  fi
  if all_dependency_findings_waived npm-audit "$output"; then
    printf 'npm audit %s: all findings match exact, active exceptions\n' "$package_dir"
    rm -f "$output" "$diagnostics"
    return
  fi
  cat "$diagnostics" "$output"
  rm -f "$output" "$diagnostics"
  return "$status"
}

while IFS= read -r module_file; do
  scan_go_module "$module_file"
done < <(find . -type f -name go.mod \
  -not -path '*/.data/*' \
  -not -path '*/.tmp/*' \
  -not -path '*/node_modules/*' \
  -not -path '*/testdata/*' \
  -print | sort)

while IFS= read -r lock_file; do
  scan_bun_lock "$lock_file"
done < <(find . -type f -name bun.lock \
  -not -path '*/.data/*' \
  -not -path '*/.tmp/*' \
  -not -path '*/node_modules/*' \
  -not -path '*/testdata/*' \
  -print | sort)

while IFS= read -r lock_file; do
  scan_npm_lock "$lock_file"
done < <(find . -type f -name package-lock.json \
  -not -path '*/.data/*' \
  -not -path '*/.tmp/*' \
  -not -path '*/node_modules/*' \
  -not -path '*/testdata/*' \
  -print | sort)
