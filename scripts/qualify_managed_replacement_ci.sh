#!/usr/bin/env bash
set -euo pipefail

# Probe without provider arguments or credentials, so confinement failures are
# visible even though the real restore deliberately suppresses provider output.
bwrap=${LEAPVIEW_TEST_MANAGED_BWRAP:?locked managed-recovery shell required}
probe=$(type -P true)
profile_file=
cleanup() {
  if [[ -n "$profile_file" ]]; then
    sudo apparmor_parser -R "$profile_file"
    rm -f "$profile_file"
  fi
}
trap cleanup EXIT
check_confinement() {
  "$bwrap" --ro-bind / / --dev /dev --ro-bind /proc /proc --die-with-parent -- "$probe"
}
if ! check_confinement; then
  # Ubuntu's profile for /usr/bin/bwrap does not match the locked Nix binary.
  # Grant user namespaces only to this exact executable on the disposable runner.
  # Never disable the host-wide AppArmor restriction or skip the restore test.
  [[ ${GITHUB_ACTIONS:-} == true ]]
  [[ $(cat /proc/sys/kernel/apparmor_restrict_unprivileged_userns) == 1 ]]
  [[ "$bwrap" =~ ^/nix/store/[a-z0-9]{32}-bubblewrap-[a-zA-Z0-9.+_-]+/bin/bwrap$ ]]
  profile_file=$(mktemp "${RUNNER_TEMP:?}/managed-recovery-apparmor.XXXXXX")
  printf 'profile leapview-managed-recovery-ci "%s" flags=(unconfined) {\n  userns,\n}\n' "$bwrap" > "$profile_file"
  sudo apparmor_parser -r "$profile_file"
  check_confinement
fi
task test:qualification:managed-replacement
