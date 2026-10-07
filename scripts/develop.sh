#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

if [[ "$(uname -s)" == Linux && "$(uname -m)" == x86_64 ]]; then
  if ! command -v nix >/dev/null 2>&1; then
    echo "Install Nix with nix-command and flakes enabled; see nix/README.md." >&2
    exit 1
  fi
  if [[ $# == 0 ]]; then
    exec nix develop --no-update-lock-file
  fi
  exec nix develop --no-update-lock-file -c "$@"
fi

# Preserve the supported conventional toolchain on other platforms until their
# development shells have passed the same qualification contract.
if [[ $# == 0 ]]; then
  exec "${SHELL:-bash}"
fi
exec "$@"
