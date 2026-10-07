#!/usr/bin/env bash
set -euo pipefail

# Build disposable transport probes and fetch only the pinned proxy. The child
# creates and verifies fresh namespaces before starting any privileged service.
repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"
evidence_dir="${1:-$repo_root/.tmp/kamal-transport}"
if [[ $# -gt 1 ]]; then
  echo "usage: $0 [evidence-directory]" >&2
  exit 2
fi
evidence_dir="$(realpath -m "$evidence_dir")"
for visible_path in "$repo_root" "$evidence_dir"; do
  case "$visible_path/" in
    /root/*|/run/*|/var/lib/*)
      echo "checkout and evidence must remain outside fixture-hidden roots" >&2
      exit 2
      ;;
  esac
done
mkdir -p "$evidence_dir"
# Invalidate any prior success before builds or bundle installation can fail.
# Recreate this generated file as the invoking user, even if a prior privileged
# direct invocation produced a root-owned receipt in this user-owned directory.
rm -f -- "$evidence_dir/transport.json"
printf '%s\n' '{"scope":"synthetic-kamal-transport-only","fullManagedProfileQualified":false,"passed":false,"cleanupCompleted":false,"stage":"setup"}' > "$evidence_dir/transport.json"

build_output() {
  nix build "path:./deploy/managed/nixos#$1" --no-link --no-update-lock-file --print-out-paths
}
predecessor_image="$(build_output kamal-transport-predecessor)"
candidate_image="$(build_output kamal-transport-candidate)"
proxy_image="$(build_output kamal-transport-proxy)"
transport_tools="$(build_output kamal-transport-tools)"
docker_package="$(build_output nixosConfigurations.example-app.config.virtualisation.docker.package)"
export PATH="$transport_tools/bin:$PATH"

# Install as the invoking user before entering the network-isolated fixture.
# Frozen resolution uses the repository's exact Kamal lock, with no ambient
# Bundler configuration or changes to the working tree.
bundle_root="${XDG_CACHE_HOME:-$HOME/.cache}/leapview/kamal-transport-gems"
env BUNDLE_IGNORE_CONFIG=1 BUNDLE_FROZEN=1 BUNDLE_PATH="$bundle_root" \
  BUNDLE_GEMFILE="$repo_root/deploy/managed/kamal/Gemfile" bundle install --jobs 2

privileged=()
if [[ $(id -u) -ne 0 ]]; then
  privileged=(sudo)
fi
"${privileged[@]}" env PATH="$PATH" python3 \
  deploy/managed/nixos/tests/kamal_transport_test.py \
  --predecessor-image "$predecessor_image" \
  --candidate-image "$candidate_image" \
  --proxy-image "$proxy_image" \
  --docker-package "$docker_package" \
  --tools "$transport_tools" \
  --bundle-root "$bundle_root" \
  --evidence-dir "$evidence_dir"
