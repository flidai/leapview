#!/usr/bin/env bash
# Match one scanner finding against the validated repository exception contract.
# This wrapper intentionally delegates validation and exact-tuple matching to
# the repository-owned Go policy tool; shell callers must not implement their
# own wildcard or expiry semantics.
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: security_exception_match.sh --scanner NAME --rule RULE --resource RESOURCE
  [--severity SEVERITY] [--class CLASS] [--root PATH]
EOF
  exit 64
}

root="."
scanner=""
rule=""
resource=""
severity=""
finding_class=""
while (($#)); do
  case "$1" in
    --root) root="${2:?missing value for --root}"; shift 2 ;;
    --scanner) scanner="${2:?missing value for --scanner}"; shift 2 ;;
    --rule) rule="${2:?missing value for --rule}"; shift 2 ;;
    --resource) resource="${2:?missing value for --resource}"; shift 2 ;;
    --severity) severity="${2:?missing value for --severity}"; shift 2 ;;
    --class) finding_class="${2:?missing value for --class}"; shift 2 ;;
    -h|--help) usage ;;
    *) printf 'unknown option: %s\n' "$1" >&2; usage ;;
  esac
done

[[ -n "$scanner" && -n "$rule" && -n "$resource" ]] || usage
[[ -d "$root" ]] || { printf 'repository root is missing: %s\n' "$root" >&2; exit 1; }
root="$(cd -- "$root" && pwd -P)"
cd "$root"
exec go run ./internal/app/tools/securitypolicy \
  --root "$root" \
  --match-scanner "$scanner" \
  --match-rule "$rule" \
  --match-resource "$resource" \
  --match-severity "$severity" \
  --match-class "$finding_class"
