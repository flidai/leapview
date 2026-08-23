#!/usr/bin/env bash
set -euo pipefail

readonly GOVULNCHECK_VERSION="v1.6.0"
readonly AUDIT_LEVEL="critical"

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

scan_go_module() {
  local module_file="$1"
  local module_dir
  module_dir="$(dirname "$module_file")"
  printf 'govulncheck %s\n' "$module_dir"
  (
    cd "$module_dir"
    go run "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}" ./...
  )
}

scan_bun_lock() {
  local lock_file="$1"
  local package_dir
  package_dir="$(dirname "$lock_file")"
  printf 'bun audit %s\n' "$package_dir"
  (cd "$package_dir" && bun audit --audit-level "$AUDIT_LEVEL")
}

scan_npm_lock() {
  local lock_file="$1"
  local package_dir
  package_dir="$(dirname "$lock_file")"
  printf 'npm audit %s\n' "$package_dir"
  (
    cd "$package_dir"
    npm audit --package-lock-only --audit-level="$AUDIT_LEVEL" --ignore-scripts
  )
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
