# Requalification after main f8c2edc45 integration — 2026-10-10

This separate qualification uses the pending merge of main `f8c2edc45f2609f14d270d26a5ef9d93ae977796` into PR HEAD `d81cd6f7ce60eb5b61221c8e932131e4fd92d6f1`, with merge base `07173b82ddb61fb605c848f02ff5357360139e9e`. Main's table hierarchy/cell-content declarations and this PR's twelve layout minimum annotations are both retained. It does not replace or rewrite the original comparison or `requalification-2026-10-10/`.

All **93 fixed prototype/fixture cases** retain their expected outcomes. All **90 preserved authored outputs** pass structural decoding, actual project compilation and exact intended behavior: A, B and C each pass 30/30. Canonical source hashes and outcomes match the original final grades. All **108 authored files**, **55 frozen corpus inputs**, and **130 pre-existing comparison files** remain byte-for-byte unchanged, including the corrected inventory metadata.

There are **zero new agent trials** and no query execution. This reruns the real compiler and deterministic intent comparisons for the existing submissions; it does not repeat inference or regrade process/scope transcripts. The original experiment's instruction-bounded filesystem and other limitations still apply.

## Build and checks

`identity.json` records the pending merge, schema/declaration and compiler-evidence fingerprints, Go 1.27.2 build information, script hash and exact helper identity. The final helper SHA-256 is `76d355e9c053c0d6c94d5e23d3eab87001e2af5b524525402302a31ae7f6a8d0`. It was built after complete generation, with `GOFLAGS='-tags=duckdb_arrow -buildvcs=false'`, `GOTOOLCHAIN=local`, `GOMAXPROCS=2`, `GOMEMLIMIT=768MiB`, and `TMPDIR=/var/tmp/leapview-dashboard-yaml-ci-temp`.

The canonical Dashboard generator was explicitly rerun inside a snapshot check and reproduced all selected artifacts unchanged. The refreshed evidence verifier passed all 11 exact-source compiler fixtures. Focused Go checks passed the document, compileradapter, application, compiler-evidence and compiler-trials packages, plus `TestDashboardGuideCompilesAgainstBundledSales` in the project compiler. `validation.json` preserves the exact check outputs, exit codes and log hashes; it is not a claim that the entire CI pipeline passed.

Initial checks exposed stale ignored generated outputs carried across the merge. The first combined Go command failed to compile application code against stale signal models; after regeneration its application rerun passed. Full generation also changed the compiler-source fingerprint without changing any of the 11 fixture outcomes. A first 93/90 diagnostic requalification had passed, but its pre-generation build is retained only under `/var/tmp/pr938-oct10-main-f8c2edc45`. The final evidence and helper were rebuilt after generation and all 93/90 checks rerun; the reports in this directory describe that final build.

## Reproduce

`requalify.ts.txt` retains the executed script without entering test discovery. The helper, frozen corpus snapshot, lowered canonical roots and complete logs remain at `/var/tmp/pr938-oct10-main-f8c2edc45-final`. The executed qualification command was:

```sh
bun /var/tmp/pr938-oct10-main-f8c2edc45-final/requalify.ts
```

For another qualification, copy the script to a new path, choose fresh `out` and `build` directories, and build `./internal/app/tools/dashboardcontracttrials` there with the recorded Go environment. Its exclusive output writes deliberately reject overwriting existing evidence. No author-inference command is involved.
