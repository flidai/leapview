#!/usr/bin/env bash
set -euo pipefail

# Repository convenience entrypoint; the contract and implementation live
# beside the released local runtime payload.
script_dir="$(cd "$(dirname "$0")" && pwd -P)"
exec "$script_dir/../deploy/local/qualification/qualify.sh" "$@"
