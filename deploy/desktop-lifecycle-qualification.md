# Exact Desktop upgrade and offline rollback qualification

The protected manual `nix-desktop-lifecycle.yml` workflow exercises two retained
Linux AMD64 Desktop Debian artifacts on a disposable Ubuntu 22.04 runner. Select
exact successful producer run IDs and attempts. Both must have original builder,
original qualification, and signed artifacts from `nix-desktop-candidate.yml`.
The candidate Debian version must be strictly greater than the predecessor;
different bytes with the same version are a reinstall, not upgrade evidence.

The collector authenticates GitHub run/artifact identities, verifies ZIP hashes,
rechecks retained bytes against authenticated metadata, and independently verifies
live provenance, exact SPDX and qualification attestations using the existing
Desktop verifier. It reads the selected source/signer checkouts as identity inputs;
it executes only the current protected qualification tools. Missing, expired,
failed, unsigned or substituted inputs stop before installation.

The runner downloads dependencies online, then runs all installations and app
processes inside a new loopback-only network namespace. It retains both archives
locally, forbids package downloads during execution, strips GitHub credentials
and ambient application configuration, and starts Electron as the ordinary
runner user with its normal sandbox. A pre-existing Desktop installation is a
hard refusal.

A private disposable profile contains one synthetic saved HTTPS instance at
`qualification.invalid`, with no credentials or remote connection. The real
installed trusted shell reads it and acknowledges a name change through its
normal form and durable profile store. The sequence is:

1. Install the exact predecessor and acknowledge a saved-name change.
2. Install the exact newer candidate, read that name, acknowledge another change,
   and kill the app process group with SIGKILL.
3. Restart the candidate and verify the acknowledged name survived.
4. Downgrade offline using the unchanged retained predecessor archive; verify the
   candidate-written name, acknowledge another change, restart and read it back.
5. Verify package removal and private-profile cleanup before publishing success.

Each installation compares the installed Electron payload tree with the exact
archive payload. The public receipt binds both archive hashes, producer identities,
qualification and attestation bindings, verifier revision and files, native host,
individual write/readback/termination checks and cleanup. Only this receipt is
uploaded. Private profiles, extracted packages and process diagnostics are not.
A failed or incomplete run does not issue a success receipt.

This proves only the reported Linux preview package and saved-profile lifecycle.
It does not prove authenticated remote sessions, server data recovery, OS code
signing, production promotion or profile observation. Existing single-artifact
qualification reports remain unchanged, including their pending lifecycle fields;
the new receipt is additional evidence for the exact pair, not a rewritten report.

As of 8 October 2026, historical successful producer `37420472821` predates the
signed Desktop path. Successful producer `37789985809` contains that path, but
both source package versions are `0.1.0`. Therefore these runs cannot establish
a signed lower-version predecessor/newer-version pair. No live upgrade pass is
claimed. Prepare genuine versioned, independently qualified inputs through the
normal release process; do not change a version, rebuild an archive, or relabel a
reinstall merely to pass this gate.
