#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -ne 4 ]]; then
  echo "usage: $0 <leapview-binary> <output-directory> <goos> <goarch>" >&2
  exit 2
fi

binary="$1"
output_dir="$2"
target_os="$3"
target_arch="$4"

for name in BUILD_VERSION BUILD_REVISION BUILD_TIME BUILD_RELEASE IMAGE_REFERENCE RELEASE_TAG; do
  if [[ -z "${!name:-}" ]]; then
    echo "$name is required" >&2
    exit 1
  fi
done

if [[ ! -x "$binary" ]]; then
  echo "authoring binary is missing or not executable: $binary" >&2
  exit 1
fi
if [[ ! "$BUILD_REVISION" =~ ^[0-9a-f]{40}$ ]]; then
  echo "BUILD_REVISION must be a lowercase 40-character Git revision" >&2
  exit 1
fi
if [[ ! "$IMAGE_REFERENCE" =~ ^[^[:space:]@]+@sha256:[0-9a-f]{64}$ ]]; then
  echo "IMAGE_REFERENCE must be an immutable sha256 image reference" >&2
  exit 1
fi
case "$BUILD_RELEASE" in
  true|false) ;;
  *) echo "BUILD_RELEASE must be true or false" >&2; exit 1 ;;
esac

case "$target_os/$target_arch" in
  linux/amd64|linux/arm64)
    support_profile="ubuntu-24.04-docker-engine"
    ;;
  darwin/amd64|darwin/arm64)
    support_profile="macos-15-docker-desktop"
    ;;
  *)
    echo "unsupported authoring CLI platform: $target_os/$target_arch; supported platforms are linux/amd64, linux/arm64, darwin/amd64, and darwin/arm64" >&2
    exit 1
    ;;
esac

actual_os="$(go env GOOS)"
actual_arch="$(go env GOARCH)"
if [[ "$actual_os/$actual_arch" != "$target_os/$target_arch" ]]; then
  echo "native authoring build required: runner is $actual_os/$actual_arch, requested $target_os/$target_arch" >&2
  exit 1
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
output_dir="$(mkdir -p "$output_dir" && cd "$output_dir" && pwd -P)"
package="leapview-cli-${RELEASE_TAG}-${target_os}-${target_arch}"
package_root="$output_dir/$package"
archive="$output_dir/$package.tar.gz"

if [[ -e "$package_root" || -e "$archive" || -e "$archive.sha256" ]]; then
  echo "refusing to overwrite existing authoring package: $package" >&2
  exit 1
fi

mkdir -p "$package_root/local-runtime"
cp "$binary" "$package_root/leapview"
chmod 0755 "$package_root/leapview"
cp "$repo_root/deploy/local/INSTALL.md" "$package_root/INSTALL.md"
cp "$repo_root/deploy/local/authoring-package.schema.json" "$package_root/authoring-package.schema.json"
cp "$repo_root/deploy/local/compose.yaml" "$package_root/local-runtime/compose.yaml"
cp "$repo_root/deploy/local/README.md" "$package_root/local-runtime/README.md"
cp "$repo_root/deploy/local/runtime-package.schema.json" "$package_root/local-runtime/runtime-package.schema.json"
cp "$repo_root/deploy/postgres/init.sh" "$package_root/local-runtime/postgres-init.sh"
chmod 0644 "$package_root/INSTALL.md" "$package_root/authoring-package.schema.json" \
  "$package_root/local-runtime/compose.yaml" "$package_root/local-runtime/README.md" \
  "$package_root/local-runtime/runtime-package.schema.json"
chmod 0755 "$package_root/local-runtime/postgres-init.sh"

postgres_image="$(awk '/^[[:space:]]+image: docker.io\/library\/postgres:/{print $2; exit}' "$repo_root/deploy/local/compose.yaml")"
if [[ -z "$postgres_image" ]]; then
  echo "local runtime does not declare an immutable PostgreSQL image" >&2
  exit 1
fi

PACKAGE_ROOT="$package_root" TARGET_OS="$target_os" TARGET_ARCH="$target_arch" \
SUPPORT_PROFILE="$support_profile" POSTGRES_IMAGE="$postgres_image" python3 - <<'PY'
import json
import os
from datetime import datetime

build_time = os.environ["BUILD_TIME"]
datetime.fromisoformat(build_time.replace("Z", "+00:00"))
root = os.environ["PACKAGE_ROOT"]
development = os.environ["BUILD_RELEASE"] != "true"
identity = {
    "version": os.environ["BUILD_VERSION"],
    "revision": os.environ["BUILD_REVISION"],
    "buildTime": build_time,
    "dirty": False,
    "development": development,
}
with open(os.path.join(root, "release-identity.json"), "w", encoding="utf-8") as target:
    json.dump(identity | {"image": os.environ["IMAGE_REFERENCE"]}, target, indent=2)
    target.write("\n")
with open(os.path.join(root, "image-reference.txt"), "w", encoding="utf-8") as target:
    target.write(os.environ["IMAGE_REFERENCE"] + "\n")
with open(os.path.join(root, "authoring-package.json"), "w", encoding="utf-8") as target:
    json.dump({
        "schemaVersion": 1,
        "product": "leapview",
        "identity": identity,
        "host": {
            "os": os.environ["TARGET_OS"],
            "architecture": os.environ["TARGET_ARCH"],
            "supportProfile": os.environ["SUPPORT_PROFILE"],
        },
        "applicationImage": os.environ["IMAGE_REFERENCE"],
    }, target, indent=2)
    target.write("\n")
with open(os.path.join(root, "local-runtime", "runtime-package.json"), "w", encoding="utf-8") as target:
    json.dump({
        "schemaVersion": 1,
        "persistentStateSchemaVersion": 1,
        "composeMinimumVersion": "2.17.0",
        "leapview": {
            "version": os.environ["BUILD_VERSION"],
            "revision": os.environ["BUILD_REVISION"],
            "image": os.environ["IMAGE_REFERENCE"],
        },
        "postgres": {
            "major": 18,
            "image": os.environ["POSTGRES_IMAGE"],
        },
    }, target, indent=2)
    target.write("\n")
PY

chmod 0644 "$package_root/release-identity.json" "$package_root/image-reference.txt" \
  "$package_root/authoring-package.json" "$package_root/local-runtime/runtime-package.json"

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$@"
  else
    shasum -a 256 "$@"
  fi
}

(
  cd "$package_root"
  : > SHA256SUMS
  while IFS= read -r path; do
    sha256_file "$path" >> SHA256SUMS
  done < <(find . -type f ! -name SHA256SUMS | LC_ALL=C sort)
	chmod 0644 SHA256SUMS
)

tar -C "$output_dir" -czf "$archive" "$package"
(
  cd "$output_dir"
  sha256_file "$package.tar.gz" > "$package.tar.gz.sha256"
)

printf '%s\n' "$archive"
