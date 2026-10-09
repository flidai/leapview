# Desktop 0.1.1 preview release

This preview changes the Linux release path to publish the exact independently
qualified Nix-built Debian package. The release job authenticates the original
candidate, qualification and signing artifacts, verifies their live GitHub
attestations, and copies the retained bytes rather than rebuilding them.
macOS and Windows retain their existing native package and security checks.

The application and policy version is `0.1.1`; the planned public prerelease is
`desktop-v0.1.1-alpha.1`. The public manifest remains withdrawn until the real
release is published. Electron, Chromium and Node versions are unchanged.
The GitHub attestations establish producer provenance; they do not establish
OS code signing. This remains an explicitly confirmed unsigned preview, with
production updater access disabled.

After these changes merge, dispatch the existing protected
`nix-desktop-candidate.yml` on main for the reviewed source. Retain the exact
successful run and attempt. Dispatch `nix-desktop-lifecycle.yml` using the
retained lower-version `0.1.0` producer and the new `0.1.1` producer. That job
must prove a version upgrade, saved-profile persistence, crash and restart,
and offline rollback to the original package with cleanup complete.

Only then dispatch `desktop-preview-release.yml` on main with the exact source,
tag, Nix producer run/attempt, completed lifecycle run/attempt and explicit
unsigned-preview confirmation. Its existing `desktop-preview` environment
approval, immutable unused tag, draft asset refetch, byte comparison and
publication attestation gates remain required. The release notes retain the
original source, Nix producer, lifecycle producer and publication verifier
identities separately.

The lifecycle receipt covers the saved-profile Linux preview. Authenticated
remote sessions, server data recovery, OS code signing, profile observation and
production promotion remain separate acceptance gates. A successful preview
must not mark those gates complete.
