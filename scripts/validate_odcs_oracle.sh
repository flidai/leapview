#!/usr/bin/env bash
set -euo pipefail

readonly expected_cli_version="0.9.1"
readonly expected_spec_version="3.1.0"
readonly fixture_root="internal/project/contractodcs/testdata"

command -v odcs >/dev/null 2>&1 || {
  printf '%s\n' "odcs ${expected_cli_version} is required for the CI-only ODCS oracle" >&2
  exit 1
}

version_json="$(odcs version --json)"
grep -Eq '"crateVersion"[[:space:]]*:[[:space:]]*"0\.9\.1"' <<<"${version_json}" || {
  printf 'unexpected odcs CLI version: %s\n' "${version_json}" >&2
  exit 1
}
grep -Eq '"upstreamSpecVersion"[[:space:]]*:[[:space:]]*"3\.1\.0"' <<<"${version_json}" || {
  printf 'unexpected ODCS upstream version: %s\n' "${version_json}" >&2
  exit 1
}

for fixture in "${fixture_root}/source.odcs.json" "${fixture_root}/model.odcs.json"; do
  odcs validate "${fixture}"
done

if odcs validate "${fixture_root}/invalid-unknown.odcs.json"; then
  printf '%s\n' 'independent ODCS oracle accepted the invalid unknown-field fixture' >&2
  exit 1
fi
