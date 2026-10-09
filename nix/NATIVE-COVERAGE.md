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
