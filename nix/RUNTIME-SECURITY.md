# Nix runtime vulnerability qualification

## Decision

Keep production release publication on the existing path. Trivy's successful Go
scan is insufficient to qualify Nix runtime libraries. Use the locked Syft and
Grype tools to qualify Nix inventory and upstream NVD matching separately from
vulnerability clearance. This is a candidate qualification, not a replacement for
OCI admission, provenance, the published SPDX SBOM, or application dependency scans.

```sh
nix build --no-update-lock-file .#leapview-image --out-link result-image
nix develop --no-update-lock-file .#runtime-security -c \
  python3 scripts/check_nix_runtime_security.py result-image \
  --evidence-dir .tmp/runtime-security-review
```

Use a new evidence directory on every run. The default command fails on missing
inventory, failed matching controls, scanner errors, or any HIGH/CRITICAL runtime
finding, including findings without a fix. `--coverage-only` checks detection and
records unresolved findings without requiring vulnerability clearance. The Nix
candidate workflow uses that mode explicitly; it must never authorize production
promotion. `releaseReady` remains false even when this individual check passes.

## What is checked

- Syft 1.52.0 and Grype 0.119.0 come from the existing `flake.lock`; runtime checks
  reject unexpected versions. Their versions and this policy must change together.
- Every store path in the Docker archive is checked against the Syft inventory.
  The sole unversioned payload allowance is LeapView's map asset derivation.
  Newly introduced Nix packages require classification in the reviewed policy.
- glibc, both GCC runtime outputs, xgcc's libgcc, BusyBox, libidn2 and libunistring
  remain in the runtime SBOM. Certificate, timezone, MIME and protocol data have
  explicit classifications. The LeapView application keeps its existing admission
  requirements; classification is not a vulnerability waiver.
- Raw Syft evidence is retained. A separate runtime SBOM adds upstream CPEs from
  `runtime-security-policy.json` without changing installed versions or store paths.
- Vulnerable synthetic controls exercise glibc, BusyBox, GCC (both outputs), xgcc
  and libidn2 against the same fresh, hash-validated Grype database as the image.
  These are matching controls, not vulnerable binaries and not image findings.
- Evidence binds the archive SHA-256, scanner image identity, runtime inventory,
  database identity, control results and all runtime findings. Missing or stale
  databases cannot turn into a successful empty report.

## Why upstream CPE mappings are necessary

The default Syft Nix cataloger discovers the packages, but generated CPEs such as
`glibc:glibc` and `gcc:gcc` miss NVD's `gnu:glibc` and `gnu:gcc` records. Nix's
`xgcc` runtime is also GCC. The first real control scan detected BusyBox's known
vulnerability but missed the GNU controls. Adding upstream identities restored
matching. Merely having a nonempty SBOM or zero HIGH findings is not sufficient.

The mappings use [NVD's glibc record](https://nvd.nist.gov/vuln/detail/CVE-2023-4911),
[GCC record](https://nvd.nist.gov/vuln/detail/CVE-2018-12886),
[BusyBox record](https://nvd.nist.gov/vuln/detail/CVE-2022-48174), and
[libidn2 record](https://nvd.nist.gov/vuln/detail/CVE-2019-18224).
The GCC control verifies product matching only: that historical compiler issue
is not evidence that the current amd64 runtime is affected.

[Anchore documents Nix inventory and NVD/CPE matching](https://oss.anchore.com/docs/capabilities/nix/).
Its Nix cataloger supplies inventory, not a Nix advisory feed aware of backported
patches. Retain findings until a package update or a reviewed, artifact-specific
assessment resolves them. There are no automatic ignores or exemptions here.

## Finding review

The [glibc triage](GLIBC-TRIAGE.md) resolves the eleven matches into eight fixes
already present in the pinned backport bundle, two disputed/non-security
classifications, and one confirmed `strfmon` defect requiring an update.
No suppressions have been activated; enforcement remains blocked.

## Qualification limits and production blockers

Known-CVE controls establish representative matcher behavior; they do not prove
complete detection of all vulnerabilities. libunistring has a reviewed GNU CPE
but no known-vulnerable control in this suite. The glibc control retains a Nix
revision suffix to test that version shape as well.

The corrected scan of candidate `000eac132fd6a2802d86d4ed4e14fad3f01dd8c7`
accounted for all 13 store paths (12 cataloged packages and the explicit map
asset payload), passed all six matching controls, and found 11 HIGH/CRITICAL
glibc matches that the default inventory missed. Some records
are disputed or have broad version constraints; others require checking the
pinned Nix glibc source and applied patches. Their dispositions are documented in the triage above; they remain unsuppressed
in scanner enforcement. The candidate is **not cleared for
production release**.

Before switching production:

1. Triage the retained HIGH/CRITICAL matches against the exact Nix derivation and
   its patches; update dependencies or integrate reviewed, bounded assessments
   with the release exception policy. Do not broadly ignore glibc or unfixed CVEs.
2. Bind this check's evidence to the published OCI digest and require its default
   mode in protected admission. A coverage-only CI pass grants no release authority.
3. Preserve Go scanning and separately account for embedded native components
   (including DuckDB and extensions); Nix runtime scanning does not inspect those
   vendored components' source dependency inventories.
4. Complete the remaining architecture, identity, SPDX/provenance and installed
   artifact qualification gates in `README.md`.
