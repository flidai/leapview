#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 4 ]]; then
  echo "usage: $0 BOOTSTRAP_RELEASE_RUN PREDECESSOR_RUN CANDIDATE_RUN NEW_EVIDENCE_DIRECTORY" >&2
  exit 2
fi
repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"
evidence_dir="$(realpath -m "$4")"
if [[ -e "$evidence_dir" ]]; then
  echo "evidence directory must be new" >&2
  exit 2
fi
tools_path="$(nix build path:./deploy/managed/nixos#kamal-transport-tools --no-link --no-update-lock-file --print-out-paths)"
docker_path="$(nix build path:./deploy/managed/nixos#nixosConfigurations.example-app.config.virtualisation.docker.package --no-link --no-update-lock-file --print-out-paths)"
controller_path="$(nix build .#leapviewctl-linux-amd64 --no-link --no-update-lock-file --print-out-paths)"
helper_root="$(mktemp -d /tmp/leapview-managed-application-tools.XXXXXXXX)"
trap 'rm -rf -- "$helper_root"' EXIT
nix develop --no-update-lock-file -c go build -o "$helper_root/ociadmission" ./internal/app/tools/ociadmission
export PATH="$tools_path/bin:$docker_path/bin:$PATH"
env BUNDLE_IGNORE_CONFIG=1 BUNDLE_FROZEN=1 BUNDLE_PATH="$helper_root/bundle" \
  BUNDLE_GEMFILE="$repo_root/deploy/managed/kamal/Gemfile" bundle install --jobs 2
# Keep authenticated artifact downloads in the online parent only. The runner
# starts its offline child with a fresh environment and hidden runtime roots.
export GH_CONFIG_DIR="${GH_CONFIG_DIR:-$HOME/.config/gh}"
privileged=()
if [[ $(id -u) -ne 0 ]]; then
  privileged=(sudo --preserve-env=PATH,GH_CONFIG_DIR,GH_TOKEN)
fi
work_root="$(mktemp -u /tmp/leapview-managed-application.XXXXXXXX)"
"${privileged[@]}" python3 deploy/managed/nixos/tests/managed_application.py \
  --tools "$tools_path" --docker-package "$docker_path" \
  --controller "$controller_path/bin/leapviewctl" --verifier "$helper_root/ociadmission" \
  --bundle-root "$helper_root/bundle" --work-root "$work_root" --evidence-dir "$evidence_dir" \
  --bootstrap-run "$1" --predecessor-run "$2" --candidate-run "$3"
