# Verify a desktop release

The official [download page](/download) publishes only artifacts declared by the checked-in desktop release manifest. Desktop downloads are currently withdrawn because the advertised GitHub preview release is unavailable. Use the checks below when a verified release becomes available. Pull-request artifacts are never linked; signed stable artifacts replace the preview only after the production release gate.

## Match the release record

Before installation, confirm that the page shows:

- the expected operating system, architecture, version, and minimum supported version;
- an immutable artifact URL and exact file name;
- the SHA-256 digest and checksum document;
- the signing state and, for stable releases, the platform code-signature identity;
- an SPDX 2.3 SBOM;
- build provenance tied to the exact source commit and workflow;
- release notes and current preview or stable channel state.

The downloadable manifest, artifact, checksum, signature, SBOM, and provenance must agree on version, product identity, architecture, and digest.

## Verify the checksum

Compare the output with the SHA-256 value on the download page.

macOS:

```sh
shasum -a 256 LeapView-*.dmg
```

Windows PowerShell:

```powershell
Get-FileHash .\LeapView-Setup-*.exe -Algorithm SHA256
```

Ubuntu:

```sh
sha256sum leapview_*.deb
```

Do not install when the digest differs, the architecture is wrong, or the file came from an instance-controlled URL.

## Verify the signing state

The early preview is intentionally unsigned. On macOS and Windows, first verify the immutable GitHub release URL and SHA-256 checksum, then follow the preview installation guidance. Do not interpret an unsigned preview as a production signature.

For a stable release, verify the code signature as follows.

On macOS, Gatekeeper must accept the installed application and report the expected LeapView Developer ID:

```sh
spctl --assess --type execute --verbose /Applications/LeapView.app
codesign --display --verbose=4 /Applications/LeapView.app
```

On Windows, inspect the Authenticode status and signer:

```powershell
Get-AuthenticodeSignature .\LeapView-Setup-*.exe | Format-List
```

The status must be `Valid` and the signer must match the publisher shown on the download page.

On Ubuntu, install through the documented APT source. APT verifies the repository's signed metadata before accepting the DEB. Do not add an unlisted key or use an unauthenticated package source.

## Verify SBOM and provenance

Download the linked SBOM to inspect the embedded Electron, Chromium, Node, and application dependency graph. Use the provenance link and its documented verifier to confirm that the artifact digest was produced by the protected LeapView workflow from the recorded source commit.

Release evidence contains no customer URLs, credentials, diagnostics, or dashboard data.

## Withdrawn releases

If a release is withdrawn, the download page removes its installer actions while retaining enough metadata for incident response and verification of already-installed copies. The stable pointer moves only to a newer qualified version; it never redirects users to an older release and the application does not downgrade automatically.

Contact support when a local signature or digest no longer matches the immutable release record.
