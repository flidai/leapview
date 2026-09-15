#!/usr/bin/env bash
set -euo pipefail

# Keep the executable entrypoint small so the qualification logic can be run
# on Linux and macOS with only the Python standard library.
script_dir="$(cd "$(dirname "$0")" && pwd -P)"
exec python3 "$script_dir/qualify.py" "$@"
