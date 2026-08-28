#!/usr/bin/env sh
set -eu

version=3.14.0
os=$(uname -s)
arch=$(uname -m)

case "$os" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *)
    echo "unsupported promtool operating system: $os" >&2
    exit 1
    ;;
esac

case "$arch" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *)
    echo "unsupported promtool architecture: $arch" >&2
    exit 1
    ;;
esac

platform="$os-$arch"
case "$platform" in
  linux-amd64) checksum=f665c6da19eb7ba399c915d30c7d9793c9b417bf8a749b504bc470678631478d ;;
  linux-arm64) checksum=077f3781ab7245dc04c9a3c9b78ba120fc8e41aa0dc97489b0af67247e50ba83 ;;
  darwin-amd64) checksum=a14307b9726e66cadb81be9a544732623af26dabeb7702c987aa9c3c062ada34 ;;
  darwin-arm64) checksum=a9623f7f4fe65b1b171b423c1a72bbf23dfdf41a171dcb33e7dd302af80dc01c ;;
esac

archive_name="prometheus-$version.$platform.tar.gz"
archive_root="prometheus-$version.$platform"
cache_root=${LEAPVIEW_PROMTOOL_CACHE_ROOT:-.tmp/tools}
tool_root="$cache_root/$archive_root"
promtool="$tool_root/promtool"

if [ ! -x "$promtool" ]; then
  if ! command -v curl >/dev/null 2>&1; then
    echo "curl is required to install promtool $version" >&2
    exit 1
  fi

  promtool_tmp=$(mktemp -d "${TMPDIR:-/tmp}/leapview-promtool.XXXXXX")
  cleanup() {
    rm -rf -- "$promtool_tmp"
  }
  trap cleanup EXIT HUP INT TERM

  archive="$promtool_tmp/$archive_name"
  curl --fail --location --silent --show-error \
    "https://github.com/prometheus/prometheus/releases/download/v$version/$archive_name" \
    --output "$archive"

  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$archive")
  elif command -v shasum >/dev/null 2>&1; then
    actual=$(shasum -a 256 "$archive")
  else
    echo "sha256sum or shasum is required to verify promtool" >&2
    exit 1
  fi
  actual=${actual%% *}
  if [ "$actual" != "$checksum" ]; then
    echo "promtool archive checksum mismatch: got $actual" >&2
    exit 1
  fi

  tar -xzf "$archive" -C "$promtool_tmp" "$archive_root/promtool"
  mkdir -p "$tool_root"
  mv "$promtool_tmp/$archive_root/promtool" "$promtool"
  chmod 0755 "$promtool"
fi

exec "$promtool" "$@"
