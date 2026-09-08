#!/usr/bin/env bash
set -euo pipefail

readonly expected_cli_version="1.1.3"
readonly expected_spec_version="3.1.0"
readonly fixture_root="internal/project/contractodcs/testdata"
readonly schema="internal/project/contractodcs/schema/odcs-json-schema-v${expected_spec_version}.json"
readonly schema_sha256="2cb7dd6fe43344d2233e0406438622681dc3ebadcf8f0d606a15b40c8f6752c0"

command -v datacontract >/dev/null 2>&1 || {
  printf 'datacontract-cli %s is required for the CI-only ODCS oracle\n' "${expected_cli_version}" >&2
  exit 1
}

# No production environment, dotenv files, default credential config, or
# external definition lookups enter this local document-validation boundary.
oracle() {
  env -i PATH="${PATH}" PYTHON_DOTENV_DISABLED=1 datacontract --config-file /dev/null "$@"
}

version="$(oracle --version)"
[[ "${version}" == "${expected_cli_version}" ]] || {
  printf 'unexpected datacontract CLI version: %s\n' "${version}" >&2
  exit 1
}
printf '%s  %s\n' "${schema_sha256}" "${schema}" | sha256sum --check --status

validate() {
  # The CLI also imports legacy DCS documents. Reject non-ODCS dispatch here
  # so malformed ODCS headers cannot be normalized through that import path.
  jq -e '.kind == "DataContract" and (.apiVersion | type == "string" and startswith("v3"))' "$1" >/dev/null || {
    printf 'not an ODCS v3 document: %s\n' "$1" >&2
    return 1
  }
  oracle lint "$1" --json-schema "${schema}" --no-inline-references --all-errors
}

for fixture in "${fixture_root}/source.odcs.json" "${fixture_root}/model.odcs.json"; do
  validate "${fixture}"
done

for fixture in "${fixture_root}/invalid-unknown.odcs.json" "${fixture_root}/invalid-version.odcs.json" "${fixture_root}/invalid-format.odcs.json" "${fixture_root}/invalid-logical-type.odcs.json"; do
  if validate "${fixture}"; then
    printf 'independent ODCS oracle accepted invalid fixture %s\n' "${fixture}" >&2
    exit 1
  else
    status=$?
    [[ "${status}" == 1 ]] || { printf 'oracle failed unexpectedly with exit %s\n' "${status}" >&2; exit 1; }
  fi
done
