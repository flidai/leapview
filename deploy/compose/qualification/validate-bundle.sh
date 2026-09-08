#!/usr/bin/env bash
# Validate the PostgreSQL-backed release bundle before it is archived or
# exercised by the installed-candidate journey. Keep this policy in one place
# so release and post-publication qualification cannot drift.
set -euo pipefail

fail() {
  printf 'qualification bundle validation failed: %s\n' "$*" >&2
  exit 1
}

if [[ $# -ne 1 ]]; then
  fail "usage: $0 BUNDLE_ROOT"
fi

bundle_root=$1
[[ -d "$bundle_root" ]] || fail "bundle root is not a directory: $bundle_root"
bundle_root=$(cd -- "$bundle_root" && pwd -P)
validator_path=$(realpath -- "$0")

require_regular_file() {
  local relative_path=$1
  local path="$bundle_root/$relative_path"
  [[ -e "$path" ]] || fail "missing required bundle path: $relative_path"
  [[ ! -L "$path" ]] || fail "required bundle path must not be a symlink: $relative_path"
  [[ -f "$path" ]] || fail "required bundle path is not a regular file: $relative_path"
}

require_mode() {
  local relative_path=$1
  local expected_mode=$2
  local path="$bundle_root/$relative_path"
  local actual_mode
  actual_mode=$(stat -c '%a' -- "$path") || fail "cannot inspect mode: $relative_path"
  [[ "$actual_mode" == "$expected_mode" ]] ||
    fail "$relative_path has mode $actual_mode; expected $expected_mode"
}

for required_path in \
  compose.yaml \
  compose.https.yaml \
  Caddyfile \
  leapview.env.example \
  qualification/postgres-init.sh \
  qualification/validate-bundle.sh; do
  require_regular_file "$required_path"
done
bundle_validator_path=$(realpath -- "$bundle_root/qualification/validate-bundle.sh")

require_mode qualification/postgres-init.sh 755
require_mode Caddyfile 644
require_mode qualification/validate-bundle.sh 755

[[ -s "$bundle_root/compose.https.yaml" ]] || fail "compose.https.yaml is empty"
grep -qF 'reverse_proxy leapview:8080' "$bundle_root/Caddyfile" ||
  fail 'Caddyfile does not proxy to leapview:8080'
grep -qFx 'LEAPVIEW_POSTGRES_REQUIRE_TLS=true' "$bundle_root/leapview.env.example" ||
  fail 'leapview.env.example must require PostgreSQL TLS'
grep -qF 'CREATE ROLE leapview_control_owner' "$bundle_root/qualification/postgres-init.sh" ||
  fail 'PostgreSQL init script is missing the control owner role'
grep -qF 'CREATE ROLE leapview_ducklake_owner' "$bundle_root/qualification/postgres-init.sh" ||
  fail 'PostgreSQL init script is missing the DuckLake owner role'
grep -qF 'CREATE DATABASE' "$bundle_root/qualification/postgres-init.sh" ||
  fail 'PostgreSQL init script is missing database creation'

# Scan only shipped bundle inputs. The validator itself necessarily mentions
# the forbidden terms in its policy and is excluded from this scan.
while IFS= read -r -d '' path; do
  [[ "$path" == "$validator_path" || "$path" == "$bundle_validator_path" ]] && continue
  if grep -qiF 'sqlite' -- "$path" ||
    grep -qE 'LEAPVIEW_(DB|DATABASE|DUCKDB)_' -- "$path"; then
    relative_path=${path#"$bundle_root/"}
    fail "forbidden SQLite or file-backed fallback content in $relative_path"
  fi
done < <(
  find "$bundle_root/compose.yaml" "$bundle_root/compose.https.yaml" \
    "$bundle_root/leapview.env.example" "$bundle_root/qualification" \
    -type f -print0
)

printf 'qualification bundle validation passed: %s\n' "$bundle_root"
