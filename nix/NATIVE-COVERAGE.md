# Embedded native coverage

`native-component-lock.json` pins the source material needed to investigate the
native code embedded in the application image. It does not grant admission.
The application currently has unresolved compiled dependency edges and fresh
source-lock advisory matches; `nix_native_inventory.py verify` fails closed.
Site and static controller qualification remain independent profiles.

The recovered engine is DuckDB 1.5.4 at
`08e34c447bae34eaee3723cac61f2878b6bdf787`. All 22 static archives in each
`duckdb-go-bindings/lib/linux-{amd64,arm64}@v0.10504.0` module byte-match the
corresponding official release ZIP. Both official ZIP SHA256 identities and
all 44 individual archive hashes are retained in the policy. The 30 vendored
source trees are recorded separately: this is a source inventory, including
build/test material, rather than a claim that every tree is linked at runtime.
The ICU source lives under `extension/icu/third_party/icu`, outside those trees.

All 14 extension roots have exact official source revisions in the policy.
Both native architecture payload sets were recovered; each reconstructed supply
has exactly the existing reviewed Nix fixed-output hash. AMD64 also passed the
actual publisher's DuckDB signature/LOAD checks. Reconstructing ARM64 content
does not claim ARM64 execution or a new signature-validation run.

| Extension | Closest recovered transitive source evidence | Remaining compiled selection evidence |
| --- | --- | --- |
| avro | avro-c custom registry f13de79b; builtin baseline ce613c41; compression overlay manifests | actual registry/overlay selections and native closure |
| azure | explicit baseline 84bab45d, Azure SDK manifests, OpenSSL 3.6.0 override | selected SDK/CURL/TLS closure |
| delta | delta-kernel v0.21.0 exact commit 2cdf7333; Cargo.lock with 518 package records | selected FFI features, native TLS and compiled graph |
| ducklake | roaring manifest and pinned CI source | actual roaring selection and native closure |
| excel | expat/minizip-ng/zlib overlay manifests | selected overlay source versions and closure |
| httpfs | CURL/OpenSSL manifest and resolved official CI workflow identity | actual CURL/TLS compiled closure |
| iceberg | explicit baseline 84bab45d; avro/CURL/OpenSSL/roaring/AWS manifests and overrides | actual feature/overlay selections and AWS native closure |
| lance | exact root Cargo.lock with 574 records; locked AMD64 Cargo metadata | exact binary-to-source closure and advisory dispositions |
| mysql | MariaDB Connector/C 3.4.7 overlay; exact database-connector submodule | TLS/compression selection and native closure |
| postgres | libpq overlay; exact database-connector submodule | TLS/compression selection and native closure |
| quack | CURL/OpenSSL manifest and pinned CI source | actual CURL/TLS closure |
| spatial | explicit baseline 84bab45d; GDAL/GEOS/PROJ/SQLite overlay manifests | selected features and large native closure |
| sqlite | exact root includes bundled SQLite source | exact bundled SQLite version/patch inventory |
| vortex | pinned Vortex submodule 7b536257; Cargo.lock with 938 records; pinned vcpkg submodule | selected Rust/C++ features and compiled graph |

The official DuckDB build run 27606050438 retains the resolved reusable
workflow revision b777c70d, helping resolve mutable source references. Its
downloadable artifacts are absent and logs return HTTP 410. This metadata is
useful provenance context, but it does not supply a retained compiled dependency
receipt for every current payload. Spatial and Iceberg signed payload revisions
must be resolved independently of the older engine extension-config entries.

## Retained upstream receipt audit

`native-upstream-receipts.json` records the 2026-10-09 read-only audit of all 13
remaining signed extension source revisions, their exact candidate payload
digests, resolved reusable workflow revisions and retained response hashes.
All queried exact-source runs have zero downloadable artifacts and return HTTP
410 for logs. PostgreSQL has only an integration run at its exact source, so
that row is explicitly not a distribution receipt. GitHub attestation lookups
for both architecture payload digests return HTTP 404. These observations do
not disprove another upstream evidence service, but neither expired logs nor
workflow metadata establish the missing compiled dependency closure.

SQLite has a smaller reconstructible source boundary: its exact extension root
vendors `sqlite3.c` and `sqlite3.h`, and its vcpkg manifest has no dependencies.
The recovered C/header and two CMake files match the upstream Git blob hashes.
Both actual signed ELF payloads contain SQLite 3.38.1 and the same full SQLite
source ID. The recorded source-ID functions resolve directly to that literal;
this binds the observation to code in each payload rather than a guessed
version from an unrelated string. It remains component evidence, not full
application admission. A fresh diagnostic Grype scan reports seven HIGH version
matches; affected-code review or a patched source build is still required.
The [official SQLite release history](https://sqlite.org/changes.html) publishes
the source ID and amalgamation SHA3-256 for 3.53.4 as a concrete update candidate.

The cheapest admissible recovery paths and engineering estimates below are
planning ranges, not measured build results. They exclude native build and CI
waiting, and assume no further ABI or compiler repairs:

| Missing group | Concrete next proof or controlled build | Estimated implementation and focused proof |
| --- | --- | --- |
| SQLite | Review exact bundled source/binary identities and affected code; if affected, pin a current amalgamation in a source-built static extension and test read/write | 2–4 hours |
| DuckLake / roaring | Resolve the selected roaring source and build options from authenticated receipts, or source-build the pinned wrapper/library | 2–4 hours |
| HTTPFS / Quack / CURL / TLS | Recover exact triplet, CURL and TLS selections; otherwise build one shared pinned dependency profile and both wrappers | 3–6 hours |
| Avro / Excel / compression | Resolve custom registry and overlay selection; otherwise pin those sources and build both wrappers | 3–6 hours |
| PostgreSQL / MySQL / Azure / Iceberg | Resolve database connector, SDK, AWS and shared TLS selections; otherwise source-build those selected closures | 4–8 hours |
| Spatial / GDAL / GEOS / PROJ | Recover actual GDAL feature/dependency selections, or build a fully pinned native closure | 4–8 hours |
| Delta / Vortex Rust FFI | Recover selected features, native TLS and actual compiler receipts; otherwise build their pinned Rust/C++ closures | 4–8 hours |

Several groups share dependencies, so these ranges must not be added as
independent commitments. The current work does not establish a credible
complete two-architecture application admission within the remaining project
window. Site, desktop and controller profiles can advance independently.
The native verifier keeps the application profile closed while these edges
remain unresolved; source manifests and partial receipts cannot override it.

## Produce retained diagnostic evidence

Run the collector from the reviewed source checkout against an independently
authenticated image archive. It creates a new directory, downloads only exact
source files whose SHA256 and Git blob identities are pinned in policy, and
retains fresh OSV request/response bytes for the committed Cargo-lock union:

```sh
python3 scripts/nix_native_inventory.py collect /tmp/native-evidence \
  --artifact /path/to/authenticated-image.tar \
  --kind application-image --platform linux/amd64 \
  --source-revision SOURCE_SHA --output /tmp/native-evidence/collection.json
python3 scripts/nix_native_inventory.py verify /tmp/native-evidence \
  --artifact /path/to/authenticated-image.tar \
  --kind application-image --platform linux/amd64 \
  --source-revision SOURCE_SHA --output /tmp/native-evidence/verified.json
```

The first command reports `coverageComplete: false`; the second currently exits
nonzero with the reviewed unresolved edges. A producer-controlled boolean cannot
override those edges, and no `verifiedDigest` is emitted for incomplete coverage.
Raw evidence paths are relative and bounded to 128 MiB per file and 4096 files;
the collector rejects source mutation, symlink traversal, changed candidate
content, incomplete OSV responses and paginated diagnostic results.

## Current findings and remediation choices

The 2026-10-09 source-lock diagnostic queried 1194 distinct crates.io package
name/version pairs. It returned 34 matched package/version rows and 69 advisory
references, including aliases. These are source matches, including potentially
unused build/test material, and require selected-build and affected-code review.
Locked Lance metadata confirms a normal dependency path through reqwest and
Rustls to aws-lc-sys 0.38.0. Its HIGH advisories concern AWS-LC X.509 CN/CRL
validation, while Rustls ordinarily validates certificates through webpki; that
distinction needs exact source/code evidence before any not-affected disposition.
No broad exception or severity change is authorized by this inventory.

The current official DuckDB 1.5.6 Lance payload identifies source 291316938 and
requires the 1.5.6 C++ ABI. Its committed lock still contains aws-lc-sys 0.38.0,
Rustls 0.23.37, h2 0.4.13 and thrift 0.17.0. An engine/module bump alone therefore
does not remove those source matches. If affected-code review establishes a real
blocking finding, investigate a patched source build with static extension
registration first. Preserve official trust keys for other extensions; unsigned
loading is not a remediation. A separate signing authority requires a concrete
governed design and review before generating any key.

## Pinned source-built Lance candidate

`duckdb.nix` builds the same DuckDB 1.5.4 engine revision with the exact upstream
Lance C++ wrapper at `350060612087e1138ffa1bbb11a535013558241a` statically linked.
`lance.nix` supplies its independently locked Rust static library. The reviewed
`lance-Cargo.lock` changes aws-smithy-json 0.62.5 to 0.62.7, with the minimum
compatible aws-smithy-types 1.4.9 and runtime-api 1.12.3 plus their schema/macro
dependencies. ethnum 1.5.3 replaces 1.5.2 because the locked Rust compiler rejects
the older conversion-error representation. These are ordinary dependency fixes;
the TLS/HTTP remediation also updates rustls 0.23.45, rustls-webpki 0.103.14,
aws-lc-rs 1.18.0 / aws-lc-sys 0.44.0, h2 0.4.16 and quinn-proto 0.11.15.
Both selected quick-xml versions retain their original archive identities and
receive hash-verified source backports through the actual Cargo vendor input.
See [the backport receipt and tests](quick-xml/README.md). The patched vendor
derivation and its offline four-test parser smoke hook passed; this is source
and parser evidence, not a completed FFI or engine build.

Lance uses the hash-pinned official Rust 1.98.1 compiler/Cargo distributions
already packaged by the locked Nixpkgs input. The default Nix source-built
compiler combines Rust 1.98.1 with LLVM 21.1.8 and rejects Lance's ordinary
AVX512 dot-product intrinsic. The same-version official compiler bundles LLVM
22.1.8; the retained compiler regression passes with that pair. This changes
only Lance's build-time compiler adapter and neither disables SIMD nor relaxes
dependency or admission checks.

The application enables this implementation only with `leapview_static_lance`
and the external static-library binding mode. A closed registry binds the
engine revision, Lance revision, platform, exact Cargo-lock SHA256 and backport
receipt SHA256. Packaging
and runtime admission require the canonical descriptor and actual DuckDB
`STATICALLY_LINKED` / `(BUILT-IN)` state before loading the fixed name. Ordinary
builds cannot enable a builtin from manifest flags. The other extensions retain
their pinned official signed payloads and exact file loads; no signing key or
unsigned loading mode is introduced.

Both replacement supply NAR hashes were computed from the retained official
sets with Lance and SQLite replaced by their canonical descriptors and the manifest
updated. Production Go serialization independently byte-matches both resulting
manifests and descriptors. That reconstruction establishes the exact desired
bytes, not successful execution of a new engine or either architecture's build.
The upstream Rust tests, static registration and two-session Lance write/read
tests must pass on actual built outputs. Protected artifact qualification,
complete compiled dependency evidence and fresh advisory disposition remain
required; this candidate does not turn the diagnostic collector into admission.

## Source-built SQLite replacement

The separate SQLite recipe keeps the exact scanner wrapper revision
`494e9feed54c20b6bbfb665baf26864bc7e3b517` and replaces its vendored `sqlite3.c`
and `sqlite3.h` with the official SQLite 3.53.4 amalgamation. The archive SHA256,
published `sqlite3.c` SHA3-256, header/source SHA256 and official source ID are
checked before producing source. The current upstream scanner head still
vendors 3.38.1, so a scanner-root update alone does not replace that component.

`leapview_static_sqlite` selects only the closed `sqlite` descriptor. The engine
loads its canonical `sqlite_scanner` registration, and requires actual static
installation before loading it. The descriptor binds the replacement source
archive and SQLite identity; it does not reuse an official extension signature.
`native-component-lock.json` records these separate rebuilt inputs under
`sourceBuiltReplacements`; its original signed payload hashes remain historical
observations rather than provenance for the replacement.

The isolated Nix SQLite smoke build passed a normal transaction, FTS5 table
creation, close/reopen and readback of the persisted sum 30. Its direct runtime
version/source-ID calls match the pinned source. Syft did not discover this
statically embedded component automatically; the explicit component SBOM uses
the verified source-build/runtime receipt. A fresh Grype 0.119.0 scan against
the database built 2026-10-09T06:32:32Z returns zero matches for that component.
This scoped result covers the compiled SQLite smoke component, not the smoke
binary's system dependencies or the LeapView output's complete native closure.

The actual source-built DuckDB SQLite attach/write/reopen test and protected
two-architecture qualification remain pending. The current supply hashes were
reconstructed with both replacements using the production Go descriptors and
manifest serialization, after verifying every retained original artifact
digest. The publisher and verifier use the canonical `sqlite_scanner` filename.
These desired-byte hashes are not publisher execution evidence; no
complete-coverage or application-admission claim is made by this source recipe,
reconstruction or component scan.
