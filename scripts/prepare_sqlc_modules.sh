#!/usr/bin/env bash
# Acquire SQLC's complete pinned module graph before source generation. Keep
# retries at this network boundary; generation and checksum failures fail closed.
set -euo pipefail

sqlc_module_directory="$(mktemp -d)"
trap 'rm -rf "$sqlc_module_directory"' EXIT
cd "$sqlc_module_directory"

# Match db:generate's compiler and proxy transport without changing the caller's
# module files, checksum policy, or application/test process environment.
export GOTOOLCHAIN=go1.26.9 GODEBUG=http2client=0
go mod init leapview-ci-sqlc
go mod edit -go=1.26.9 -require=github.com/sqlc-dev/sqlc@v1.31.1

for attempt in 1 2 3; do
  if go mod download all >download.log 2>&1; then
    cat download.log
    exit 0
  else
    download_status=$?
  fi
  cat download.log >&2
  if grep -Eq 'checksum mismatch|SECURITY ERROR' download.log ||
    ! grep -Eq 'i/o timeout|TLS handshake timeout|connection reset by peer|temporary failure in name resolution|Temporary failure in name resolution|unexpected EOF|502 Bad Gateway|503 Service Unavailable|504 Gateway Timeout' download.log ||
    [ "$attempt" -eq 3 ]; then
    exit "$download_status"
  fi
  printf 'retrying pinned SQLC module acquisition after transport failure %s/3\n' "$attempt" >&2
  sleep "$((attempt * 5))"
done
