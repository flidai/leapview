# Embedded native coverage

`native-component-lock.json` pins the source material needed to investigate the
native code embedded in the application image. It does not grant admission.
The application currently has unresolved compiled dependency edges and fresh
source-lock advisory matches; `nix_native_inventory.py verify` fails closed.
Site and static controller qualification remain independent profiles.

## Source-built component receipts

The source-built DuckDB/Lance/SQLite recipes now retain architecture-specific
build evidence under `share/leapview/native-build` in their Nix outputs. Lance
records `compiler-artifact` messages from its actual successful Cargo build,
including target kinds, selected features, profiles, and package identities
checked against the **patched** `nix/lance-Cargo.lock`. The receipt also binds
the checked quick-xml vendor file hashes and reviewed backport inputs. This is
different evidence from the older upstream source inventory described below.
Compiler stdout is bounded while being captured.

DuckDB retains the effective CMake cache, compilation commands, static extension
selection, compiler target, and exact SQLite amalgamation/header hashes. Its
receipt hashes the actual installed static archives after Nix fixups. SQLite
now has one selected static/PIC archive shared with its engine wrapper and the
Spatial dependency work. The library receipt checks the actual compiler command,
unchanged FTS/RTREE definitions, all three amalgamation headers/source identities,
and a consumer linked against the final installed archive. Engine composition
rejects another inline `sqlite3.c` compilation and binds the exact same archive in
the engine and application link. Source-built whole-engine/runtime evidence is
still required; a component consumer alone does not qualify the application.

The application checks both component receipts against its selected archive
bytes and records the actual link-input list, compiler, build tags, and final
ELF hashes. Runtime dependency replacement retains its own input/output binding
and the two exact glibc/libstdc++ replacement pairs, followed by a portable
transformation receipt for the post-`patchelf` bytes. The image carries this
complete original → runtime rewrite → portable receipt chain at
`/usr/local/share/leapview/native-build`; its LeapView executable is
`/usr/local/bin/leapview` and its controller is `/usr/local/libexec/leapviewctl`.
Build paths are retained only in compiler/configuration evidence; source
identity and component link-input keys use recipe hashes and relative names.
Evidence files use bounded canonical `.b64` envelopes, preserving their exact
decoded bytes and hashes without literal Nix store references that would pull
build-only compilers and archives into the runtime closure. For inspection,
decode an individual envelope with `base64 --decode FILE.b64`.

After independently authenticating the candidate archive and source revision,
extract the receipt directory and both executables using the existing bounded
archive reader. Place the executables together in a directory for verification:

```sh
python3 scripts/nix_native_build_receipt.py verify-portable \
  --repo /path/to/authenticated/source --platform linux/amd64 \
  --revision SOURCE_SHA --destination /path/to/extracted/native-build \
  --binaries /path/to/extracted/bin
```

Use `verify` for the original application output and `verify-runtime` for the
runtime-rewritten `.#leapview` output before portable conversion. The manual
`native-application` Nix workflow retains the latter output's complete receipt
bundle, its build output metadata, and a consumer verification bound to the
actual application binary hashes, checkout revision, and native platform.
The existing native extension publisher still executes LOAD/signature checks;
its discovery record also binds the retained native-build verification hash.
The consumer compares recipe fingerprints to the supplied trusted checkout,
recomputes the compiled Cargo selection using its current patched lock, rejects
missing or substituted component/link/output evidence, and checks final ELF
architecture. Receipts are not signatures: a self-consistent bundle from an
unauthenticated builder does not establish trustworthy provenance.

This is **build composition evidence**, not an exhaustive list of the archive
members or native code incorporated into each final ELF. It does not establish
the closure of the 9 remaining signed extensions, engine vendored dependency
identities, build-script-produced C/C++ subgraphs, or a fresh complete native
vulnerability scan. Results deliberately contain neither `verifiedDigest` nor
an admission/complete flag. `nix_native_inventory.py verify` and whole-application
release admission remain closed. Focused synthetic receipt tests exercise the
consumer; actual receipts still require successful native builds on each
architecture and cannot be supplied by source inventories alone.

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
sets with Lance, SQLite and DuckLake replaced by their canonical descriptors and the manifest
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
reconstructed with all three replacements using the production Go descriptors and
manifest serialization, after verifying every retained original artifact
digest. The publisher and verifier use the canonical `sqlite_scanner` filename.
These desired-byte hashes are not publisher execution evidence; no
complete-coverage or application-admission claim is made by this source recipe,
reconstruction or component scan.


## Controlled DuckLake and CRoaring replacement

`nix/ducklake-source-lock.json` pins DuckLake source
`d318a545571d7d46eb751fa2aa5f6f4389285d3c` and CRoaring 5.2.2 source
`6ef0aaad28eb6298db5a7022695b290a8fe47e66`. The recipe retains the locked
Nixpkgs pkg-config patch, checks its exact hash and patched source identities,
builds a position-independent static archive, disables CPM network fetching,
and keeps the upstream test suite enabled. The closed `leapview_static_ducklake`
descriptor binds the engine, wrapper revision, architecture and complete native
dependency policy hash. DuckLake reports the full pinned wrapper revision at
runtime. Ordinary builds retain their signed extension loading behavior.

The CRoaring receipt records the actual compiler target, selected CMake options,
compilation commands and produced archive digest. DuckDB's receipt binds its
selected `roaring_DIR`, deletion-vector compilation unit and that archive's hash;
application composition requires the same CRoaring archive in the actual linker
inputs. Missing receipts, changed native options, changed source/patch identities
and substituted archives fail verification. Encoded raw evidence preserves
compiler paths without adding build-only Nix dependencies to the runtime closure.
This binds build composition; it does not prove which individual archive members
or headers reached the final ELF.

The static-engine regression forces Puffin deletion vectors, deletes rows,
reopens a fresh session, checks both current and historical snapshots, and applies
a second deletion. The SQL workload was exercised locally against the exact
pinned signed DuckLake payload as a bounded workload check. Qualification against
the new statically linked engine and two-architecture publisher remains required.
The desired supply NAR hashes use all three closed descriptors and production Go
serialization after checking every retained signed input; they are not publisher
execution evidence. Historical signed payload hashes and source inventories stay
separate from rebuilt identities.

Eleven other signed extension closures, engine vendored identity/member coverage,
and fresh complete native vulnerability disposition remain unresolved. The
whole-application native admission verifier continues to refuse admission.


## Controlled HTTPFS and Quack candidate

The native recipe statically compiles the pinned HTTPFS and Quack wrappers with
one explicit CURL/OpenSSL/nghttp2/zlib profile in `http-source-lock.json`.
HTTP/2 and gzip support remain enabled. Quack's client uses HTTPFS's HTTPUtil;
its manifest alone is not evidence of a separately linked CURL/TLS copy.
The static-link patch supplies CURL's selected transitive archives and the
Quack include patch selects this engine's httplib/autocomplete headers rather
than an absent upstream submodule. Neither patch changes wrapper runtime code.

Each library retains selected source file hashes after patches, actual compiler
commands and configuration, and the resulting archive hashes. The consumer
checks architecture, explicit TLS/HTTP2/zlib selection, static output selection,
missing receipts and substituted archives. The engine receipt binds its CMake
library choices to the same five archives used by the application link group.
Raw intermediate evidence is encoded before installation to prevent Nix split
output cycles or accidental compiler/source runtime dependencies. The complete
receipt chain still represents build composition, not exhaustive ELF members.

The image installs the pinned CA bundle under both the Nix/Go name and CURL's
conventional `ca-certificates.crt` name. The manual native application lane runs
the selected engine's default-HTTPS probe in a private real chroot: the previous
layout must reject the fixture CA, and the packaged layout must accept it without
`ca_cert_file`. The executable stays in the tools output and its hash and test-log
hash are bound to retained discovery evidence. Host certificates are never
modified; portable host certificate search paths remain unchanged.

The compiled test lane rejects a manifest descriptor against the unmodified
prebuilt engine and exercises HTTPS trust rejection/acceptance, Parquet range
reads, and authenticated Quack queries across fresh clients. An upstream signed
payload probe checks those workloads only; it is not proof of the rebuilt
engine. Actual static engine execution and publisher proof remain required on
both architectures. The engine's selected httplib/mbedtls code, the nine remaining
signed extensions and the complete native vulnerability assessment still keep
application admission closed. These pinned library versions have not been given
a not-affected disposition by this build-composition work.

The preceding SQLite/DuckLake/Lance recipe at
`bedfad363b6655974f7acc73486c3e19517d4651` passed the native application and
publisher lane on AMD64 and ARM64 in GitHub run `38025258793`. Both authenticated
artifact ZIP hashes, consumer-verifiable retained receipts and reconstructed
extension supply hashes were independently checked. That proof belongs to the
preceding composition; it does not qualify this revised HTTPFS/Quack engine.

## Controlled PostgreSQL and MySQL candidate

`database-source-lock.json` selects the pinned PostgreSQL/MySQL wrappers and
their shared database-connector headers, libpq 18.6 and MariaDB Connector/C
3.4.11. Both clients use the existing selected OpenSSL/zlib profile. The client
archives are linked with their actual wrapper targets and the application link
group; they are never substituted for the historical signed payload identities.

The selected libpq build preserves password/SCRAM, verified TLS, and the wrapper's
OAuth bearer-token hook. Optional automatic OAuth acquisition through libcurl,
Kerberos, server-side LZ4/Zstd and translation dependencies are excluded from this
client profile. Its pinned Makefile patch allows only the `pthread_exit` symbol
from static OpenSSL in the upstream process-exit check; `exit`, `_exit` and
`_Exit` remain rejected, including versioned symbols. MariaDB retains native password, caching SHA2, SHA256, dialog,
cleartext and Ed25519 authentication as builtins. Its archive check resolves all
six plugins with a nonexistent dynamic-plugin directory. This prevents a static
build from silently dropping authentication or depending on absent plugin files.

Receipts retain selected source identities, compiler targets, effective options,
compilation commands, the generated builtin plugin table and all four produced
client archives. Composition binds those archives and the shared TLS dependencies
to the engine's selected CMake inputs and final application link group. Missing
receipts, substituted archives, changed TLS/plugin selections and wrong compiler
architectures fail verification. Encoded evidence does not retain compiler or
source store paths in the runtime closure.

The manual native application lane executes the existing PostgreSQL/MySQL source
read, write-denial and credential-recovery fixtures, plus the PostgreSQL DuckLake
lifecycle, using test binaries linked alongside the application. PostgreSQL uses
the existing verified-TLS fixture; MySQL also asserts a negotiated TLS cipher.
These Docker-backed fixtures run outside the Nix sandbox, and remain outside the
runtime image. Their exact executable and successful log hashes enter discovery
evidence. Required PostgreSQL setup and explicit PASS checks reject skipped or
empty test selections. The shared fixture verifies real static registration
before staging compiled descriptors; conventional builds retain signed fixtures.

The new engine's runtime and publisher qualification is still required on both
architectures. Desired supply hashes were reconstructed using the production Go
serializer after verifying every retained artifact's digest; that is not actual
publisher execution with the new engine. Seven other signed extension closures,
engine vendored identities and selected-header/archive-member coverage, and fresh
complete native security
assessment remain open. Neither source inventories nor these build-composition
receipts grant whole-application admission.

The selected Quack, PostgreSQL and MySQL wrapper patches expose their actual
engine-provided source revision through `Extension::Version()`. Quack previously
looked for an unrelated RPC macro; PostgreSQL/MySQL inherited the empty base
method. Source preparation compiles each real patched method with its selected engine
macro and invokes it from a separate loader translation unit without that macro.
Version definitions stay out of shared headers, so loader compilation cannot
inline an empty fallback. Empty or substituted revisions fail before the
expensive engine build. Full runtime identity assertions remain required.

## Controlled Excel candidate

`excel-source-lock.json` selects duckdb-excel revision
`f4c72b5ef04a03b3a78a95b5a2ee94ba93e3178d`, Expat 2.8.4 and minizip-ng 4.2.2,
reusing the selected HTTP profile's zlib 1.3.2 archive. The wrapper patch binds
those exact static libraries and exposes the source revision through DuckDB's
actual `Version()` method. The selected profile preserves upstream's default
features off: optional codecs, cryptography, iconv, libbsd and dependency fetching
are disabled. This maintained source selection does not describe the historical
signed Excel binary's dependency closure.

The selected library builds passed their upstream checks and a real static
consumer that writes a deflated worksheet ZIP, reads it back, and parses its XML
with Expat. Receipts retain source/compiler/configuration evidence, compile
commands, archive digests and the consumer result. Composition verifies that the
engine uses those same Expat/minizip archives and the shared zlib bytes; missing
receipts, substituted libraries, optional feature drift and wrong compiler targets
fail verification.

The manual native application lane now requires an Excel fixture that checks the
exact static source revision, creates a real XLSX workbook, reads it through the
canonical governed source relation in two fresh sessions, and rejects a missing
sheet. Its executable and successful log hashes enter the retained evidence.
Actual revised-engine execution and native publisher qualification on AMD64 and
ARM64 remain pending. Reconstructed supply hashes use the production Go serializer
and verified retained bytes; they are not publisher execution or runtime proof.

Six other signed extension source closures remain: Avro, Azure, Delta, Iceberg,
Spatial and Vortex. Engine vendored identities, selected-header/archive-member
coverage and the fresh complete native security assessment also remain open.
This component milestone does not grant whole-application admission.

## Controlled Avro candidate

`avro-source-lock.json` retains DuckDB's custom Avro-C fork at
`8af400279c445a81b8552a7670d8c1ebd92ba34a` and wrapper
`f9d590297485f0318f480372c70bdd852826e258`. This fork supplies the logical-type,
field-ID and timestamp semantics used by the wrapper. It selects static Jansson
2.15.0, Snappy 1.2.2 and xz 5.8.3, with the existing HTTP profile's zlib archive.
A narrow CMake patch permits a static-only Avro-C build; wrapper discovery is
bound to the exact static archives. The two-translation-unit source probe checks
the existing out-of-line `Version()` method against `EXT_VERSION_AVRO`.

The pinned fork registers `test_avro_logical_types.c` without shipping that file.
A separate hash-pinned patch removes only this unavailable registration; the
required consumer covers those custom logical-schema APIs. The build explicitly
selects GNU C17: this fork uses pre-C23 unspecified callback parameter lists,
which GCC 15 otherwise interprets as zero-argument prototypes. Receipts require
the actual selected dialect; no compiler warning is suppressed.

The build requires Jansson, xz and all available Avro-C upstream tests, and a real static consumer
that writes and reopens null, deflate, Snappy and LZMA containers and checks the
custom logical-schema APIs. Snappy's packaged source omits the upstream test
submodules; its selected codec is exercised by this consumer. Receipts retain
selected source identities, effective compiler/configuration/compile evidence,
actual archive hashes and selected link inputs. Application composition checks
those bytes against the engine's CMake inputs and final application link group.
Encoded evidence avoids retaining build/source store paths in the runtime closure.

The manual native application lane requires the exact compiled Avro revision and
reads a container through the admitted Avro reader in two fresh sessions, checking
decimal, date and timestamp semantics. Avro is an Iceberg dependency, not a public
path-source format; canonical Iceberg workload qualification remains separate. The selected wrapper's
COPY writer has no compression option; codec coverage comes from the static
library consumer. Both-architecture revised-engine execution and publisher proof
remain required. Five other signed extension closures (Azure, Delta, Iceberg,
Spatial and Vortex), engine vendored/header/member evidence, and complete native
security assessment remain unresolved. This build composition does not authorize
whole-application admission or describe the historical signed Avro binary.

## Controlled Delta candidate

The Delta candidate replaces the signed wrapper with the exact
`duckdb/duckdb-delta` revision `45c40878601b54b4188b09e08732fe0d576ad222`
and builds `delta-kernel-rs` revision
`2cdf7333b8e10fb53c053677cd8b436752220188` (0.21.0) from its pinned Cargo lock.
The wrapper's networked Cargo/acceptance ExternalProject is replaced with the
selected offline FFI output. The production FFI profile retains upstream
`default-engine-rustls,arrow,test-ffi,delta-kernel-unity-catalog,tracing` and its
default features. The unrelated acceptance executable is not shipped or built.

`delta-source-lock.json` binds the wrapper/header transform, kernel source,
compiler and feature profile. `delta-quick-xml-backport-lock.json` retains the
same two reviewed upstream XML fixes used for Lance, adapted to the selected
0.39.2 source. The original crate checksum and exact original/patched file hashes
are preserved. Lance's two-version backport policy remains separate.
The Rust derivation tests its upstream FFI library and the actual selected
patched XML parser's ordinary reads, namespace boundary and duplicate attributes.

The component receipt retains actual Cargo compiler-artifact features and source
checksums, generated C/C++ header bytes, reviewed source/patch identities and the
produced static archive hash. The engine receipt binds both this archive and the
actual generated header directory selected by CMake; application composition
cross-checks those inputs. Compiler records establish the selected build graph,
not exhaustive membership of the final executable or complete native-code
vulnerability coverage (including Rust crates' bundled C/assembly).

The tools-only runtime fixture reuses the existing canonical Delta log/Parquet
fixture, adds a replacement transaction, and checks latest/explicit snapshots and
fresh admitted sessions. The manual native lane requires a concrete non-skipped
pass and retains both test-binary and log hashes. Wrapper version identity uses
an out-of-line method verified across separate extension/loader translation
units. Actual rebuilt engine, two-architecture runtime and publisher evidence
remain qualification requirements. Iceberg, Spatial and Vortex still have
unresolved signed source closures after the Azure milestone below; engine vendored source
coverage and complete scanning remain separate blockers. Admission stays denied.

## Controlled Azure candidate

`azure-source-lock.json` selects duckdb-azure revision
`563589b2f24290a4dcdd4247eaedf2b544f9dbcd`, Azure Core 1.16.3,
Identity 1.13.3, Storage Common 12.12.0, Blobs 12.16.0, Data Lake 12.14.0,
and libxml2 2.15.4. SDK archives are static/PIC and bind the existing selected
HTTP profile's Curl, OpenSSL, nghttp2 and zlib archives. Dynamic XML modules,
ICU, XML zlib, Python and HTTP support are disabled; the XML parser remains
available. The static XML build must run its upstream checks and retain their
success marker; skipped checks fail receipt verification. Identity and Storage Common retain their consumed source-fetch option in the
CMake cache through hash-pinned patches; missing or enabled fetch settings fail
verification. The SDK retains its upstream tests-off default. A required native
consumer exercises signed Blob/Data Lake XML responses and client-secret token
JSON with an in-memory transport and owned synthetic credentials.

The wrapper's out-of-line `Version()` and generated loader are checked in two
translation units. CMake receives explicit SDK/XML and HTTP archive lists;
application receipts compare these bytes with each component and the final link
group. Selected source/configuration/PIC compilation, actual archive hashes and
consumer ELF dependencies are retained; unexpected selected shared libraries,
source or dependency rebinding fail verification. This is selected build
composition evidence, not exhaustive binary/header/archive-member closure.

The manual native application lane requires the exact static Azure revision and
runs the existing canonical source fixture: a scoped SharedKey signs the request,
a CSV row is read, a missing object fails, and a subsequent valid read succeeds.
The executable and passing log hashes are retained. Execution on the revised
source-built engine and both-architecture native publisher qualification remain
pending. Two other signed extension closures remain (Iceberg and Spatial), alongside engine vendored identities and the complete native
security assessment. No whole-application admission is granted by this milestone.

## Controlled Vortex candidate

The Vortex candidate selects wrapper `275ac230e1d9afd08926b6989ec2467f92fae6e3`
and Rust workspace `7b536257e2653c3bd293e12e51ccbcb445534e60`. The exact
Cargo lock and Git crate hashes are retained; only the actual `vortex-duckdb`
static FFI target is compiled for the application. No root feature is added.
The source-only engine input is the same pinned DuckDB revision used by the
final engine. The explicit upstream source branch generates C-to-Rust and
Rust-to-C bindings and compiles 17 C++ bridge sources before returning; a narrow
assertion prevents fallback to downloaded engines. The wrapper uses the selected
archive and generated header directory without Corrosion/network/package installs.

The selected object-store graph includes quick-xml 0.39.4, which still lacks the
two retained namespace/duplicate-attribute protections. Its independent checksum
policy applies the same source-compatible backport used for Delta. The actual
unpatched namespace test fails at 257 declarations, while the patched parser
passes ordinary reads, default/configured bounds and duplicate-attribute checks.
Lance and Delta keep their own exact-version policies.

The receipt retains the actual Cargo compiler artifacts, C++ bridge commands,
compiler identities, selected engine/source/patch hashes, generated ABI bytes and
static output hash. Final engine evidence rehashes the selected engine headers
and checks its CMake wrapper/library/include selection; application composition
cross-checks the archive and ABI hashes. All retained build paths are encoded to
avoid retaining the compiler closure in portable/runtime images. These are build
composition records, not an exhaustive archive-member or binary closure proof.

The selected Rust derivation runs upstream pure file tests without creating an
engine dependency cycle. The tools-only native workload writes Vortex, then uses
the canonical admitted path reader in two fresh sessions to check filtered rows,
nulls and decimals. The manual lane requires a non-skipped pass and binds the test
binary and log hashes. Actual selected Rust compilation, rebuilt-engine runtime
and two-architecture publisher qualification must pass before this candidate is
claimed proven. Remaining signed components, engine vendored identities and fresh
complete native security assessment still block whole-application admission.

## Controlled Iceberg candidate

The Iceberg candidate selects wrapper `757264559e745be697e9306e144e8889eb1dc024`
and 14 locked AWS library sources. The SDK builds only core, SSO and STS; static
CRT dependencies retain their upstream checks. The SDK and final application
share the existing selected CURL, OpenSSL, zlib and nghttp2 archives. Iceberg also
uses the selected CRoaring archive and the separately admitted Avro extension.
No VCPKG fetch or alternate shared AWS/HTTP dependency is allowed by the recipe.

Each library retains selected source hashes, compiler identity, CMake feature
selection and actual compile commands. An aggregate consumer exercises SigV4
payload/session-token signing, HTTP-client creation, SSO JSON and STS XML, then
checks dynamic dependencies. Receipts bind all 16 actual AWS archives, shared HTTP
bytes, final engine selections and application link inputs. Encoded evidence
avoids retaining compiler/store paths in the runtime closure. These records prove
build composition, not exhaustive archive-member or final binary closure.

The manual native lane runs the canonical Iceberg fixture against its exact
linked test binary: latest and explicit snapshots agree, a missing snapshot is
rejected, valid reads recover, and fresh sessions repeat the checks. It preserves
the required fixture working directory and retains binary/log hashes. The signed
control and selected-library prototype pass independently; the final selected
aggregate, rebuilt engine and both-architecture publisher still require actual
qualification. Spatial remains a signed source-replacement gap, alongside engine
vendored identities and the complete native security assessment. Whole-application
admission remains closed.

## Spatial source-build prerequisites in progress

The exact Spatial wrapper remains `28db190f7184bcf61eb01d291e0cba79849bddb6`.
The independent GEOS producer selects the wrapper overlay's exact 3.14.1 source
commit `7db6f62d50221a7ccac91c329a9a4fe61d172a56`, static/PIC output, mandatory
nonempty upstream CTest execution and a native geometry/containment/buffer consumer.
This is a selected dependency prerequisite, not a completed Spatial engine closure.
GDAL, PROJ, their formats/embedded data and all transitive source selections remain
pending, as do revised engine, both-architecture runtime and publisher checks.

The prerequisite SQLite refactor preserves the already selected official 3.53.4
amalgamation and scanner feature definitions. It also authenticates `sqlite3ext.h`
from the same retained ZIP for PROJ's in-memory VFS. Sharing this one archive avoids
mixing the Spatial overlay's older SQLite with the current scanner's global SQLite
symbols. Transaction/reopen, FTS and RTREE component checks do not substitute for
revised-engine SQLite/Spatial functionality and full selected archive binding.
