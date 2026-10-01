#!/bin/sh
set -eu

attempt=1
while :; do
	if GODEBUG=http2client=0 go mod download "$@"; then
		exit 0
	else
		status=$?
	fi

	if [ "$attempt" -ge 3 ]; then
		echo "MinIO module download failed after $attempt attempts" >&2
		exit "$status"
	fi

	case "$attempt" in
		1) delay=5 ;;
		2) delay=10 ;;
	esac
	echo "MinIO module download failed (attempt $attempt/3); retrying in ${delay}s" >&2
	sleep "$delay"
	attempt=$((attempt + 1))
done
