#!/usr/bin/env bash
set -euo pipefail

readonly GITLEAKS_VERSION="v8.30.1"

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

# Scan tracked files plus non-ignored untracked files so squashed content cannot
# evade the gate. Materialize that explicit Git view instead of scanning ignored
# build output or dependency caches left behind by an earlier local task.
scan_root="$(mktemp -d)"
[[ "$scan_root" == /tmp/* || "$scan_root" == /var/tmp/* ]] || {
  printf 'refusing unexpected temporary scan root: %s\n' "$scan_root" >&2
  exit 1
}
trap 'rm -rf -- "$scan_root"' EXIT
git ls-files --cached --others --exclude-standard -z |
  tar --create --null --files-from=- --ignore-failed-read |
  tar --extract --directory "$scan_root"

# The small allowlist contains exact deterministic test placeholders, never
# file-wide or rule-wide exclusions.
go run "github.com/zricethezav/gitleaks/v8@${GITLEAKS_VERSION}" dir \
  --no-banner \
  --no-color \
  --redact=100 \
  --timeout=300 \
  "$scan_root"

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
