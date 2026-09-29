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
inventory, failed matching controls, scanner errors, expired assessments, or any
unassessed HIGH/CRITICAL runtime finding, including findings without a fix.
`--coverage-only` is a diagnostic option that records unresolved findings without
enforcing the vulnerability gate. The Nix candidate workflow uses default
enforcement. `releaseReady` remains false even when this individual check passes.

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
- The image must contain exactly the glibc output built by this flake, which
  includes the upstream fix for CVE-2026-19499. A second or older glibc output
  fails inventory qualification even if Syft catalogs it.
- Raw Syft evidence is retained. A separate runtime SBOM adds upstream CPEs from
  `runtime-security-policy.json` without changing installed versions or store paths.
- Vulnerable synthetic controls exercise glibc, BusyBox, GCC (both outputs), xgcc
  and libidn2 against the same fresh, hash-validated Grype database as the image.
  The same VEX file is supplied to the control scan; filtering a control fails.
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
assessment resolves them. There are no package-wide ignores here.

## Finding review

The [glibc triage](GLIBC-TRIAGE.md) resolves the eleven matches into eight fixes
already present in the pinned backport bundle, two disputed/non-security
classifications, and one confirmed `strfmon` defect fixed by the candidate's
additional upstream patch. The image qualification exercises the corrected buffer
boundary. Raw version-based matches remain after a backport.

[`runtime-assessments.vex.json`](runtime-assessments.vex.json) records those
dispositions in standard OpenVEX, consumed by [Grype's VEX support](https://oss.anchore.com/docs/guides/vulnerability/filter-results/).
Nine statements describe fixes; two classify disputed records as `not_affected`
using Debian's published non-security assessment. Those two statements do not
claim a patch or prove that application paths are unreachable. They require
review alongside the source evidence in this PR.

Each statement names one CVE and the complete Nix PURL, including its output
hash. The wrapper verifies that identity against the image's store path before
applying it. The scanner policy sets review expiry to 2026-12-28 (a maximum
90-day interval); expiry,
missing identities, duplicate statements and package changes fail closed. When
updating Nixpkgs or the patch, recheck the derivation and replace the assessment
identity and dates only after reviewing the new package.

Evidence includes the unchanged raw scan, the VEX file and its SHA-256, and a
second assessed scan. Every raw match must occur exactly once in the assessed
scan's active or ignored findings, with identical package, severity and evidence.
An ignored match must have an exact CVE/PURL/store-path assessment. New CVEs remain
blocking; unexpected filtering is an error. This is candidate runtime assessment,
not an accepted-risk exception or authorization to publish a release.

## Qualification limits and production blockers

Known-CVE controls establish representative matcher behavior; they do not prove
complete detection of all vulnerabilities. libunistring has a reviewed GNU CPE
but no known-vulnerable control in this suite. The glibc control retains a Nix
revision suffix to test that version shape as well.

The corrected scan of the earlier candidate `000eac132fd6a2802d86d4ed4e14fad3f01dd8c7`
accounted for all 13 store paths (12 cataloged packages and the explicit map
asset payload), passed all six matching controls, and found 11 HIGH/CRITICAL
glibc matches that the default inventory missed. Some records
are disputed or have broad version constraints; others require checking the
pinned Nix glibc source and applied patches. The clean patched candidate at
revision `81d56a2a1099` passed the full production image qualifier and a fresh
coverage scan. Its image inventory contained only the patched glibc output,
all six matching controls passed, and Grype retained the same eleven raw matches.
Applying the VEX assessments to that same archive retained all eleven raw matches
and passed runtime enforcement. Nineteen medium and three low findings remain
active; this is not a zero-finding scan. The archive SHA-256 is
`6fe514991ecb875a4e391329c4387b8bd0ad694ff6d27743f374bd61803a8df4`.
The candidate is **not cleared for production release**.

Before switching production:

1. Review and adopt these candidate assessments in protected admission. Changes
   to this VEX file must receive the same review as the scanner policy.
2. Bind this check's evidence to the published OCI digest and require its default
   mode in protected admission. A coverage-only CI pass grants no release authority.
3. Preserve Go scanning and separately account for embedded native components
   (including DuckDB and extensions); Nix runtime scanning does not inspect those
   vendored components' source dependency inventories.
4. Complete the remaining architecture, identity, SPDX/provenance and installed
   artifact qualification gates in `README.md`.
