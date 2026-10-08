#!/bin/sh
set -eu

# Keep command output intact; timing is diagnostic and never replaces its status.
phase=$1
shift
started=$(date +%s) || started=
status=0
"$@" || status=$?
finished=$(date +%s) || finished=
elapsed=unavailable
case "$started:$finished" in
  *[!0-9:]*|:*|*:) ;;
  *)
    if [ "$finished" -ge "$started" ]; then
      elapsed=$((finished - started))
    fi
    ;;
esac
printf 'build_phase=%s elapsed_seconds=%s exit_code=%s\n' "$phase" "$elapsed" "$status" >&2 || :
exit "$status"
