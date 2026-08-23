#!/usr/bin/env bash
set -euo pipefail

readonly GITLEAKS_VERSION="v8.30.1"

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

# Scan the complete current tree so untracked or squashed content cannot evade
# the gate. The small allowlist contains exact deterministic test placeholders,
# never file-wide or rule-wide exclusions.
go run "github.com/zricethezav/gitleaks/v8@${GITLEAKS_VERSION}" dir \
  --no-banner \
  --no-color \
  --redact=100 \
  --timeout=300 \
  .

# Scan every commit introduced by the candidate as a second boundary. Existing
# history is the explicit baseline; new history must be clean even when a
# secret was added and deleted before the final tree.
base_ref="${SECURITY_GITLEAKS_BASE_REF:-origin/main}"
git rev-parse --verify "$base_ref" >/dev/null
go run "github.com/zricethezav/gitleaks/v8@${GITLEAKS_VERSION}" git \
  --no-banner \
  --no-color \
  --redact=100 \
  --timeout=300 \
  --log-opts="${base_ref}..HEAD" \
  .
