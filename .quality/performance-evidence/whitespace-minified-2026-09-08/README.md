# Whitespace-minified bundle qualification

This is measured bundle-size evidence, not a runtime or production speedup
claim. No byte ceilings, baseline records, relative ratchets, dependencies, or
shipped-entry coverage were changed to obtain a passing result. The earlier
merged-main increase proposal remains unapplied and is not needed for this
candidate to pass the existing policy.

## Candidate and method

- Clean implementation commit: `e5e46c35f84247937d41fd518be345face440a88`.
- Three sequential `PATH=/home/codex/.bun/bin:$PATH bun run build` commands
  cleaned emitted outputs through the production build script. Dependencies
  were reused; these are not three independent installations.
- Reports were byte-identical, SHA-256
  `cfc804bb99a448e5818c40fc8b79ff0e64a1e9016482df1820893af847559b84`.
- [Complete report](normal.json) retains commit, source digest, lockfile and
  toolchain identities. Bun 1.3.14, Node compatibility v24.3.0, Linux x64;
  local shared Debian 13 host, kernel 6.12.105+deb13-amd64, 16 QEMU logical CPUs.
- Source digest: `96782042d7a9bde2a69ac4fc3e8c3b3ed08bd7c7871f1f721c04b18b49d6c6aa`.
- `task quality:frontend-bundle:check` passed all 20 logical entries and the
  deduplicated aggregate: **12,838,433 raw / 2,906,055 gzip bytes**.
- Existing aggregate ceilings remain **15,934,947 raw / 3,183,940 gzip bytes**.
  Individual entry ceilings and zero-percent ratchets also remain unchanged.

## What changed and what remains

Production Bun output uses whitespace minification, with identifier renaming
and syntax minification explicitly disabled. Tests execute emitted fixture
code, check retained identifiers and license notices, and independently
recompute bytes and exact shipped-JavaScript coverage. The options module is
included in source identity and the independent-review rule.

This archive does not apply a new baseline. Any future recalibration still
requires explicit evidence-backed review. Historical unminified reports remain
available, but differing dependency-resolution/build-directory experiments are
not used here to claim a precise paired improvement. Final-candidate canonical
CI, browser/runtime qualification, and independent GitHub approval remain
separate requirements; this report alone does not establish merge readiness.
