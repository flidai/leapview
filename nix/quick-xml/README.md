# Lance XML source backports

The locked Lance dependency graph selects quick-xml 0.37.5 and 0.38.4 for cloud
response parsing. `../quick-xml-backport-lock.json` binds the upstream fixes for
RUSTSEC-2026-0194 and RUSTSEC-2026-0195, the version-specific backports, the
original crate archives, and each original/patched source file by SHA-256.

The duplicate-attribute fix switches larger start tags to a hash pre-filter
while retaining exact duplicate-position reporting. The namespace fix rejects
more than 256 namespace declarations per element by default, before adding the
excess binding. The explicit resolver setting permits a caller to change that
limit. The 0.37 backport also exposes the resolver configuration API that this
older release lacked.

`../lance.nix` replaces only these two crates in the locked Cargo vendor
directory. The helper rejects unexpected source/checksum/patch bytes, applies
without fuzz, verifies the resulting files, and updates their Cargo file
checksums while preserving the original archive identity. The compiled Lance
descriptor binds both the Cargo lock and the backport receipt. These patches
are build inputs, not a vulnerability-policy exemption.

The FFI derivation retains its upstream library tests. Its `postCheck` runs
`../quick-xml-smoke.rs` offline against the same vendor directory: ordinary
deserialization, the exact default namespace boundary, an explicit lower limit,
and duplicate attributes recorded before/after the hash threshold on both
versions. Run `nix build .#lance-ffi` for the complete FFI build and checks.

These source checks alone do not qualify the final native engine or application
artifacts. Their architecture-specific builds, scans, provenance coverage and
protected admission remain required.
