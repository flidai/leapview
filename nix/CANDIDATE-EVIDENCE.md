# Nix candidate evidence

`scripts/nix_candidate_manifest.py` collects a common, versioned identity record
for Nix application images, site images, CLI archives, application archives and
Linux desktop archives. The Nix qualification workflow currently integrates the
application-image adapter on AMD64. Other outputs and ARM64 still need their own
builders and qualification; accepting an identity in this format does not add a
supported release platform.

## Collect and verify

Build from a clean Git checkout. For an application image, first run image
qualification and the enforced runtime scan:

```sh
nix develop --no-update-lock-file -c bash scripts/check_nix_image.sh
nix develop --no-update-lock-file .#runtime-security -c \
  python3 scripts/check_nix_runtime_security.py result-image \
  --evidence-dir .tmp/nix-runtime-security
python3 scripts/nix_candidate_manifest.py result-image --kind application-image \
  --runtime-evidence .tmp/nix-runtime-security \
  --output .tmp/nix-runtime-security/candidate-manifest.json
python3 scripts/nix_candidate_manifest.py result-image --kind application-image \
  --runtime-evidence .tmp/nix-runtime-security \
  --verify .tmp/nix-runtime-security/candidate-manifest.json
```

Collection refuses to overwrite an existing manifest. Verification recomputes
the entire record from the artifact, checkout and report bytes, including the
current assessment expiry. Retain the archive alongside the manifest and runtime
reports if it must be verified later. Ordinary CI retains bounded reports for
14 days, so it is qualification evidence rather than durable release storage.

The identity contains:

- The SHA-256 of the exact archive bytes, including compression.
- The exact source commit and hashes of tracked Nix recipes, policy and
  assessments, `flake.lock`, Go module inputs and JavaScript package/lock inputs.
- The output kind, version and Linux platform.
- For Docker archives, the content-verified config digest and ordered,
  content-verified uncompressed layer diff IDs. The source and version come from
  image labels, and `dev.leapview.build.kind` must match the selected output.
  Dirty builds, duplicate members and inconsistent layers fail.
- Optional runtime evidence: the complete summary hash, all report/config hashes,
  pinned scanner versions, current policy and exact assessment bytes. Coverage
  controls must pass and unresolved runtime findings must be enforced. A
  coverage-only, incomplete, changed, expired or different-artifact scan fails.
  Database validity and build time must also satisfy the scanner's existing
  120-hour freshness policy when collecting or verifying the record.
- A domain-separated digest of the canonical record, and the full set of
  required release gates. `releaseAdmission` always remains `false`.

Syft exports `sbom.spdx.json` (SPDX 2.3) from the same native inventory retained in
`sbom.syft.json`. The enriched matching inventory, synthetic controls, raw and
assessed Grype findings, OpenVEX assessments and scanner configs are retained
separately. The collector binds these reports; it does not rerun the scanners or
independently validate their finding semantics.

For archive outputs, an output builder must provide `--archive-identity` with
exactly `platform`, `version` and `sourceRevision`. These are builder declarations;
the common collector hashes the archive without interpreting its contents.
Output-specific embedded identity and installation checks remain required. Image
runtime reports cannot be attached to an archive output.

## Release authority and follow-up adapters

### OCI content binding

The read-only image qualification lane exports the existing Docker archive to
an OCI layout while preserving the exact config and uncompressed layer bytes,
without rebuilding. Flake-pinned Skopeo copies that layout with
`--preserve-digests`; directly converting a Docker archive through Skopeo
normalizes the config and changes its digest, which this verifier rejects.
The lane then runs
`scripts/nix_oci_content.py` to bind the layout's selected root manifest or image
index digest and every declared platform manifest to freshly verified candidate
archive and runtime report bytes:

```sh
python3 scripts/nix_oci_content.py export --layout .tmp/nix-exported-oci \
  --platform linux/amd64 --kind application-image \
  --candidate result-image .tmp/nix-runtime-security/candidate-manifest.json .tmp/nix-runtime-security
nix develop --no-update-lock-file .#runtime-security -c \
  skopeo copy --preserve-digests oci:.tmp/nix-exported-oci:candidate oci:.tmp/nix-candidate-oci:candidate
digest=$(python3 -c 'import json; print(json.load(open(".tmp/nix-exported-oci/index.json"))["manifests"][0]["digest"])')
python3 scripts/nix_oci_content.py bind --layout .tmp/nix-candidate-oci \
  --manifest-digest "$digest" --platform linux/amd64 --kind application-image \
  --candidate result-image .tmp/nix-runtime-security/candidate-manifest.json .tmp/nix-runtime-security \
  --output .tmp/nix-runtime-security/oci-content-binding.json
```

Replace `--output` with `--verify` to recompute and compare a retained binding.
For a platform matrix, repeat `--candidate ARCHIVE MANIFEST RUNTIME_EVIDENCE` and
`--platform` for every platform. The candidate and OCI index platform sets must
match exactly, with one source/input identity, output kind and version. No
platform is inferred or omitted. An application record cannot qualify a site
image. Actual ARM64 and site qualification remain pending.

Descriptor SHA-256 values and sizes are verified against local blob bytes. The
image config must retain its original digest, and every ordered layer must
decompress to its original diff ID. Compression may change without changing
image content; changed configs, missing or reordered layers and substituted
platforms fail. The bounded adapter accepts OCI image manifests and flat image
indexes, with plain or gzip layers. Nested indexes, platform variants, external
descriptors and symlinked content are rejected. JSON documents are limited to
2 MiB, layers to 8 GiB, and total inspected bytes (including decompression) to
16 GiB. These are qualification limits, not a change to release support.

The workflow retains `oci-content-binding.json` for 14 days alongside the
candidate and runtime reports. It neither publishes nor reads from a registry.
The caller selects the root digest; this unsigned record proves content matching
when verified against retained artifacts, not registry authenticity or trusted
builder provenance. Publication and promotion must independently verify the
actual immutable registry digest and platform manifests. `releaseAdmission`
remains `false`, and all existing release gates remain required.

### Remaining release authority

The manifest is unsigned. Its hashes detect accidental substitution when checked
against trusted source and retained evidence, but they do not authenticate a
builder or authorize publication. The image config digest is not the registry
manifest digest. Load/push can change representation; protected qualification
must bind the final immutable published digest and every supported platform.

The existing OCI admission action and protected `Main artifacts` / release
workflows remain the release authority. Conventional published builders remain
selected. The runtime SPDX export is not a trusted SPDX attestation discoverable
by that action, and Nix runtime evidence covers neither Go vulnerabilities nor
embedded DuckDB and extension libraries.

D04's remaining admission adapters must bind trusted provenance, discoverable
SPDX evidence, the existing Trivy/Go/embedded-native checks, supported-host
compatibility and installation/upgrade/rollback/recovery results to the exact
candidate, with canonical release identity across the supported platform matrix.
They must then promote the admitted immutable artifact without
rebuilding. Each output's adoption is gated separately in D05–D08. A successful
application image scan grants no clearance to the site, CLI or desktop.

Protected candidate qualification continues to require one exact open PR head
with a direct base of `main`. Stacked children must land their prerequisites,
retarget and revalidate before using that protected path. This collector supplies
reusable identity and evidence binding while those release gates remain pending.
