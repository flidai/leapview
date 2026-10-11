#!/usr/bin/env bash
set -euo pipefail
umask 077

# This disposable installed component uses the actual publication fixture and
# production controller. It never deploys a protected image/customer host.
: "${LEAPVIEW_TEST_MANAGED_RESTIC:?locked managed-recovery shell required}"
: "${LEAPVIEW_TEST_MANAGED_POSTGRES_BIN:?locked managed-recovery shell required}"
# The VM driver puts AF_UNIX sockets below runtime/vm-state-<machine>.
# GitHub's nested Nix TMPDIR can exceed the kernel's socket path limit.
fixture_root=$(mktemp -d /tmp/lv-coordinator.XXXXXX)
cleanup() {
  outcome=$?
  if [[ $outcome == 0 ]]; then
    rm -rf -- "$fixture_root"
  else
    printf 'Installed component failed; private fixture retained at %s (do not upload).\n' "$fixture_root" >&2
  fi
}
trap cleanup EXIT

# Build public artifacts before capturing the time-bounded source frontier.
# One Nix job/two build cores; guest CPU counts remain one each.
fixture_tools=$(nix build .#managed-recovery-test-tools --no-link --print-out-paths --no-update-lock-file --max-jobs 1 --cores 2 -L)
controller=$(nix build .#leapviewctl-linux-amd64 --no-link --print-out-paths --no-update-lock-file --max-jobs 1 --cores 2 -L)
driver=$(nix build path:./deploy/managed/nixos#managed-coordinator-test.driver --no-link --print-out-paths --no-update-lock-file --max-jobs 1 --cores 2 -L)
nix-store --query --requisites "$fixture_tools" "$controller" > "$fixture_root/public-tool-paths"
mapfile -t tool_paths < "$fixture_root/public-tool-paths"
nix-store --export "${tool_paths[@]}" > "$fixture_root/public-tools.nar"
export LEAPVIEW_TEST_MANAGED_EXPORT_DIR="$fixture_root/export"
export LEAPVIEW_TEST_MANAGED_FIXTURE_TOOLS="$fixture_tools"
export LEAPVIEW_TEST_MANAGED_CONTROLLER="$controller"
export LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=true
cd "$fixture_tools/share/leapview/internal/app"
"$fixture_tools/bin/managed-recovery-fixture" -test.run '^TestManagedRecoveryInstalledPublicationExport$' -test.timeout 10m -test.v > "$fixture_root/export.log" 2>&1
mkdir -m 0700 "$fixture_root/results" "$fixture_root/runtime"
# Driver cleanup stops all owned guests even on failure. Provider stderr stays
# in mode0600 files below this private result directory; never a public artifact.
XDG_RUNTIME_DIR="$fixture_root/runtime" "$driver/bin/nixos-test-driver" -o "$fixture_root/results"
