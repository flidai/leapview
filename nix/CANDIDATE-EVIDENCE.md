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
task nix:qualify
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

### Final registry content

`scripts/nix_registry_content.py` reads the actual immutable registry image and
recomputes the content binding against freshly verified local candidate archives
and runtime evidence. Run it inside the locked runtime-security shell, which
supplies Skopeo. Authenticate separately using Skopeo's credential file; no token
is accepted on the command line and TLS verification remains enabled.

```sh
nix develop --no-update-lock-file .#runtime-security -c \
  python3 scripts/nix_registry_content.py \
  --image ghcr.io/flidai/leapview@sha256:FULL_MANIFEST_DIGEST \
  --kind application-image --platform linux/amd64 \
  --candidate result-image .tmp/nix-runtime-security/candidate-manifest.json .tmp/nix-runtime-security \
  --output .tmp/nix-runtime-security/registry-content-binding.json
```

Replace `--output` with `--verify` to refetch and compare a retained receipt.
Repeat `--candidate` and `--platform` for the entire declared platform matrix.
The application and site use their respective repositories; tags, tag-plus-digest
references and other repositories fail before a network request. Skopeo copies
every platform with `--all --preserve-digests --src-tls-verify=true`; the existing bounded OCI verifier
then checks the requested root digest and all config/layer bytes. Missing platforms,
normalization or substituted content fail. Network operations have a five-minute
timeout, tool diagnostics are suppressed, and temporary layouts are removed.
Candidate bytes, clean source and time-limited runtime evidence are checked again
after downloading, before a receipt is written.

This adapter only reads the registry. Its unsigned receipt always retains
`releaseAdmission: false`; content matching does not establish trusted provenance,
an authenticated SPDX statement or the remaining security and installation gates.
The pinned [Skopeo copy contract](https://github.com/podman-container-tools/skopeo/blob/main/docs/skopeo-copy.1.md)
defines digest preservation and full-index copying. No published builder or
protected candidate authorization is changed.

### Trusted provenance and runtime SPDX

`scripts/nix_signed_evidence.py` composes the read-only registry-content adapter
with live GitHub attestation verification. It verifies SLSA v1 provenance for the
root manifest/index and SPDX 2.3 for **each platform manifest**, then compares each
verified SPDX predicate with that platform's exact bound runtime document. The
SPDX JSON representation may differ, but the canonical document, namespace,
packages and all other fields must be identical. The receipt retains both the
original report hash and the canonical predicate/statement hashes.

```sh
nix develop --no-update-lock-file .#runtime-security -c \
  python3 scripts/nix_signed_evidence.py \
  --image ghcr.io/flidai/leapview@sha256:FULL_MANIFEST_DIGEST \
  --kind application-image --platform linux/amd64 \
  --candidate result-image .tmp/nix-runtime-security/candidate-manifest.json .tmp/nix-runtime-security \
  --expected-workflow flidai/leapview/.github/workflows/artifacts.yml \
  --signer-revision FULL_PROTECTED_WORKFLOW_EVENT_SHA \
  --output .tmp/nix-runtime-security/signed-evidence-binding.json
```

Repeat candidate/platform arguments for the whole matrix. Authenticate GitHub CLI
and the registry separately before verification. The locked runtime-security shell
supplies GitHub CLI as well as Skopeo. Approved signers are the existing protected
application artifact/release workflows and the site image workflow, selected by
output kind. Verification requires the exact signing revision, `refs/heads/main`,
GitHub-hosted runners and registry-discoverable bundles. It accepts no offline
bundle, pre-verified JSON file, alternative signer or hermetic success flag.

For protected `workflow_dispatch`, `--signer-revision` is the workflow event SHA
(`github.sha` on main), **not** the checked-out candidate head. The candidate source
is independently bound by the clean checkout, archive identity, locked inputs and
registry content. Preserve the existing exact-open-PR-head/direct-main-base gate;
this command does not implement or replace that authorization. The GitHub CLI
[verification contract](https://cli.github.com/manual/gh_attestation_verify) defines
the signer/source constraints and verified statement output used here.

Replace `--output` with `--verify` to perform fresh registry/signature checks and
compare a retained receipt. A changed selected statement requires fresh evidence.
Missing signatures, changed inventories, wrong subjects/predicates, tool failures,
oversized verifier JSON and expired/changed candidate inputs all fail. Tool calls
have bounded timeouts and suppress credential-bearing diagnostics. Verification
outputs are not accepted as an independent trust authority: the receipt is unsigned
and must be regenerated under the protected admission caller.

Protected **Nix** builders still need to publish immutable candidates, sign their
provenance and attest each platform's already-bound `sbom.spdx.json` without
rebuilding or replacing its inventory. Existing Buildx SPDX cannot stand in for
the Nix runtime document. No Nix producer or release adoption is enabled by this
adapter. Both its receipt and the registry-content receipt retain
`releaseAdmission: false`; Go/embedded-native coverage, full platform and host
qualification, installation/recovery and exact-artifact promotion remain required.

### Protected candidate producer

`.github/workflows/nix-candidate.yml` is a manual, main-only producer for the
AMD64 application candidate. It must first be reviewed and land on main; do not
dispatch its feature-branch copy. `source_revision` must be the exact head of one
open PR directly based on main. A stacked child is ineligible until its
prerequisites land and it is directly based on main. The producer repeats that
authorization before publication.

Candidate recipes run only in a read-only build job. A separate read-only job
downloads that build's immutable artifact ID, generates inventory and assessment
evidence with the protected workflow revision's scanners/policy, and qualifies
the already-built archive with protected PostgreSQL/browser fixtures and CLI.
`check_nix_image.sh ARCHIVE TRUSTED_APPLICATION` performs qualification without
building; `task nix:qualify` retains Task-owned build ordering for local use.

After publication and live signature verification, `qualify-published` downloads
the exact qualification and publisher-binding artifact IDs. Its commands,
verifier and qualification CLI come from the same protected workflow revision.
It has only read access to repository contents and packages, and no signing or
publication authority. Before execution, it reverifies the archive, Go/runtime
reports, preserved registry content, provenance and exact SPDX.

`check_nix_registry_image.sh` pulls only an immutable application reference and
keeps that exact reference in the loader probe and enterprise image qualification.
It does not retag or repush the image, or substitute Docker's normalized daemon
ID. The Go qualifier also preserves an already-immutable input through Compose
bootstrap, authoring and performance; only local tag inputs need registry pinning.
The image report records the selected deployment reference. A controller
regression checks the generated Compose environment and rejects registry startup,
retagging or republishing for a signed digest. The existing pre-publication archive
qualification also remains enforced.

`nix_candidate_publication.py bind-qualified` requires a successful schema-1
image report for that same registry digest, all four current bounded phases and
fresh, ordered phase timestamps. The qualifier preserves fractional-second UTC
timestamps so millisecond phase durations can be checked for ordering without
rounding a phase start backward.
It repeats live signed-content verification, matches the publisher's retained
signature receipt, and rejects changed report bytes. Its receipt binds the
source, AMD64 candidate digest, signed-evidence
digest, protected qualifier revision and exact qualification-report hash. The
report and receipt are retained together for fourteen days, including the report
when qualification fails; a failed run cannot produce a successful binding.

This is AMD64 candidate image evidence and still sets `releaseAdmission: false`.
The Nix authoring fixture's Ubuntu baseline does not replace the advertised
Debian host matrix. Full platform/host, installation/upgrade/rollback/recovery,
embedded native coverage, canonical release identity and exact production
promotion remain separate gates. Live positive qualification of this protected
path must run after the workflow and verifier changes land on main.

The privileged publisher imports only protected verifier code and locked tools.
Its separate source checkout supplies identity, never executable scripts or
policy. Proposed runtime-policy or glibc-baseline changes must land on main
before publication against that new protected policy. It downloads the exact
qualification artifact ID, rechecks archive/source/runtime bytes and freshness,
exports preserved OCI content, and pushes only a unique
`nix-candidate-RUN_ID-ATTEMPT` tag. It refetches and verifies the immutable digest
before signing SLSA provenance and the exact platform runtime SPDX. SPDX must fit
the attestation action's 16 MiB predicate limit. The single-platform root and
platform manifest are the same digest here; this is not ARM64 qualification.

`scripts/nix_candidate_publication.py` supplies the protected `record`, `prepare`,
`publish` and `verify-signed` operations. All require an explicit clean candidate
source checkout and exact authorized revision. The imported verifier's root stays
at the protected checkout for policy and assessments. Signing is followed by live
registry-discoverable verification under the main workflow event SHA. Receipts
retain `releaseAdmission: false`; no production tag, deployment or promotion is
performed. Archive/scan transfer artifacts expire after seven days, bindings
and the protected image qualification report after fourteen days. These are
candidate evidence, not durable release storage.

No live producer success is claimed by adding the workflow. Protected dispatch
and positive Nix SPDX/signature evidence remain required after it is eligible on
main. Go/embedded-native coverage, complete platform/host and installation/recovery
admission, canonical release identity and exact-artifact promotion remain separate
D04 and output-specific gates before adopting a replacement builder.

### Exact Go binary verifier

`securitydependencies` supplies a separate binary operation alongside the existing
source-aware repository scan. It accepts only an actual Linux AMD64 or ARM64 Go
ELF binary whose main package, ELF architecture and Go build settings match the
explicit expectation. Metadata and SHA-256 come from the same bytes; the pinned
`govulncheck v1.6.0` scans a private snapshot without executing that binary.

Run with trusted verifier code and toolchain, using a new report directory:

```sh
go run ./internal/app/tools/securitydependencies -root . \
  -binary /path/to/leapviewctl \
  -binary-package github.com/flidai/leapview/cmd/leapviewctl \
  -binary-platform linux/amd64 -binary-evidence /path/to/new-go-evidence
```

Successful scans retain `govulncheck.json` in full and `summary.json`, binding
the exact binary hash, complete Go build metadata, scanner/database identity and
raw report hash. The SBOM must match the binary's Go version, main module and
complete dependency/replacement versions. Missing or malformed messages, source
mode, scanner diagnostics/failures, coverage warnings and vulnerable symbols
fail closed. Report capture is bounded to 32 MiB; binaries to 256 MiB. Failed
scans can leave diagnostic reports, but never a success summary. Existing report
directories cannot be reused by a scan.

Use the same arguments with `-verify-binary-evidence` for offline verification.
This rechecks binary and report bytes, metadata and findings without installing
or running a scanner or candidate. Scan receipts expire at 120 hours and future
scan/database timestamps are rejected. Database last-modified time is retained
as database identity; it is not itself a scan timestamp or evidence that the
database must publish a change every five days.

This is **Go binary evidence only**, and its receipt is unsigned. Stripped binaries
receive conservative module-level analysis from govulncheck; a symbol finding
still blocks, even if the source scan does not reach it. A clean source scan does
not waive a binary finding. No clean application or controller candidate is
claimed by adding this tool. The adapter does not establish archive containment,
embedded DuckDB/native-extension coverage, provenance or release admission.

### Protected archive Go evidence

The protected producer requires Go evidence for all three declared application
entrypoints: `/usr/local/bin/leapview`, `/usr/local/libexec/leapviewctl`, and
`/usr/local/share/leapview/deployment/leapviewctl`. The two controller copies must
be byte-identical. `scripts/nix_archive_go_evidence.py` reads the nested layer
archives without extracting arbitrary paths or executing candidate binaries.
Regular overlays use final manifest layer order; links, non-directory ancestors,
whiteouts affecting these paths, missing/non-executable files and oversized inputs
are rejected. Image startup and healthcheck must use the absolute
`/usr/local/bin/leapview` path, so a permissions failure cannot make PATH lookup
select an unscanned binary. The standard Compose healthcheck also uses that
absolute path. PATH must still start with `/usr/local/bin` for container CLI
operations that use command names. Configuration redirection is rejected.
Layer metadata rejects global PAX headers, PAX size/sparse overrides, sparse files,
mixed PAX/GNU headers and repeated headers of the same type, which Python and
Docker's Go layer reader can interpret differently.
Independent PAX headers and GNU long names/links remain supported; each extended
header is bounded to 1 MiB. Absolute container-root paths in
dockerTools store layers are normalized for lookup only, never used as host
extraction paths. Extracted files use fixed private names without execute
permissions. Each parsed layer must match
its verified diff ID, and the whole archive hash is checked before and after.

Qualification builds the Go verifier from the protected checkout with its locked
Go compiler, scans these exact bytes in the credentialless job, and retains every
raw report and receipt under `runtime/go/`. Failed scans retain diagnostics but
cannot produce a candidate manifest. `record`, `prepare`, `publish` and
`verify-signed` all require `--binary-verifier /absolute/protected/verifier` and
offline-reverify the complete report set against freshly extracted archive bytes.
The publisher builds only the protected verifier: it neither executes candidate
Go binaries nor runs a scanner or rebuilds the candidate archive.

The manifest's `go-binaries` evidence binds the archive, each shipped path and main
package, binary/report/receipt hashes, complete Go build metadata, scanner/database
identity and scan time. Verification uses private report snapshots and rejects
changed input reports, symlinks, missing/extra report directories, diagnostics,
substitution, malformed/source-mode reports and stale scans. It never treats a
receipt's asserted success as independent authority. The reports must originate
from the protected qualification job and travel with its immutable artifact ID.

The standalone manifest collector accepts `--go-evidence runtime/go` together with
`--binary-verifier`; OCI, registry and signed-evidence adapters accept that verifier
flag and reverify the corresponding `runtime/go` reports. Omitting it cannot verify
a manifest that already binds Go evidence. Generic unsigned candidates may still
record incomplete evidence; the protected publisher always requires the Go set.

This completes the archive/report composition implementation only. Live positive
protected producer qualification must run after the workflow/helper changes land
on main, against an eligible exact open PR head. Existing stripped candidates can
fail conservative module-level binary analysis; those findings remain blocking,
with no exception based on a source scan. The Nix application recipe retains Go
function symbols (`-w`, without `-s`, and `dontStrip`) for precise binary coverage.
Locally rebuilt application and controller binaries with those flags passed the
protected scanner; those probes do not qualify a new Nix archive or authorize
publication. Embedded DuckDB/extensions, full platform/host and
installation/recovery qualification, production adoption and
exact promotion remain separate gates. `releaseAdmission` remains false.

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

D04's remaining protected producer and admission composition must supply trusted
provenance and discoverable SPDX for these adapters and bind the existing
Trivy/Go/embedded-native checks, supported-host
compatibility and installation/upgrade/rollback/recovery results to the exact
candidate, with canonical release identity across the supported platform matrix.
They must then promote the admitted immutable artifact without
rebuilding. Each output's adoption is gated separately in D05–D08. A successful
application image scan grants no clearance to the site, CLI or desktop.

Protected candidate qualification continues to require one exact open PR head
with a direct base of `main`. Stacked children must land their prerequisites,
retarget and revalidate before using that protected path. This collector supplies
reusable identity and evidence binding while those release gates remain pending.

### Standalone static controller archive reports

The same Go evidence adapter supports `--kind cli-archive`, with the required
`--archive-identity` source/platform/version record. Its archive contract is exactly
one regular executable named `leapviewctl` at the root. Extraction writes only a
fixed private filename, rejects extra or duplicate members, links, set-ID files,
size overrides, sparse files and ambiguous extended headers, and rechecks the full
archive hash after reading. No controller bytes are executed by the scanner.

`nix_candidate_manifest.py --kind cli-archive --archive-identity IDENTITY
--go-evidence REPORT_DIRECTORY --binary-verifier ABSOLUTE_VERIFIER` binds and
reverifies the expected controller package and platform, raw reports, build
metadata, scanner identity and freshness against the exact archive. Both AMD64
and ARM64 reports are retained alongside the unchanged native Debian 12 probes.
The read-only PR scan uses reviewed-with-the-PR helpers and has no signing or
publication authority. Protected qualification must generate fresh Go reports with
main-owned tools; offline verification alone cannot establish the authenticity of
PR-generated scanner results. Protected archive signing/admission, SPDX, full
installation and recovery acceptance remain prerequisites; `releaseAdmission`
stays false.
