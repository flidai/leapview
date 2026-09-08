# Frontend bundle reproducibility verification — 2026-09-08


Scope: additional current-controls reproducibility verification only. No budget/baseline regeneration, ceiling change, PR/push, or runtime-performance claim. The synthetic regression below is a controlled checker exercise, not a product change.

## Revisions and image

- Base revision: `e704f88068696c9fe1136a51c22b088663976f31` (`e704f8806`).
- Candidate revision: `57b0ae9979d3eb18db5e8f7a921710ae1cd12311` (`57b0ae997`).
- First Docker web build command (target/argument/tag are evidenced by the retained BuildKit log; the wrapper shell line is not itself present in that log):
  `docker build --target web --build-arg BUILD_REVISION=57b0ae9979d3eb18db5e8f7a921710ae1cd12311 --tag leapview-performance-controls-web:57b0ae997 .`
- First build/checker root log: `.tmp/performance-docker-web.log`; it records `bun run build`, `bun scripts/frontend_bundle_budget.ts`, and pass at 20 logical entries / 15934947 raw / 3183940 gzip.
- Pinned image: `leapview-performance-controls-web:57b0ae997`; image ID `sha256:dbd7ded4c97a5f62bad6034100a6656e632f86ff7a1c85f43902be87ec83dd61`.

Image/runtime identity in all reports: Bun 1.3.14, Node `v24.3.0`, Linux x64, package manager `bun@1.3.14`, lockfile SHA-256 `ef842b88f7e7ab22495d84c69ca4fe76acb1e9248144195a1fe5a472b87638fa`, source-input digest `da238bde2074cf4d1b9bf58423509a4ac072b58923cb517c53c4d89c3ca0d51c`, commit source `build-arg`.

## Two fresh-container repeats

Each repeat started from the same pinned web image in a new writable container, removed the prior evidence file, ran the exact clean-output command `BUILD_REVISION=57b0ae9979d3eb18db5e8f7a921710ae1cd12311 bun run build`, then ran `bun scripts/frontend_bundle_budget.ts`. The build script cleans generated bundle paths; no host build was run. Containers were stopped and removed after capture.

Command template (with the listed name substituted):
```sh
docker create --name <name> leapview-performance-controls-web:57b0ae997 /bin/sh -lc 'set -eu
rm -f .tmp/frontend-bundle-evidence.json
BUILD_REVISION=57b0ae9979d3eb18db5e8f7a921710ae1cd12311 bun run build
bun scripts/frontend_bundle_budget.ts
echo __REPORT_BEGIN__
cat .tmp/frontend-bundle-evidence.json
echo __REPORT_END__'
docker start -a <container-id>
docker rm <container-id>
```

| repeat | container identity | result | full raw report | report SHA-256 |
| --- | --- | --- | --- | --- |
| 1 | name `leapview-bundle-repro-1b-20260908`; ID `227f0da590301502f862d55175447d6d7aafe995e4a6aa16f067423744101fce`; exit 0 | checker passed | `.tmp/frontend-bundle-evidence.repro-20260908-run1.json` (20612 bytes) | `d2e47a8eaa48366da64c8ce779d329a4803bbaebf811937130619300159e3622` |
| 2 | name `leapview-bundle-repro-2c-20260908`; ID `4ba95dffb16a897131a23afcf9181e4d24ba88d958ab92eaf32c4369184d9697`; exit 0 | checker passed | `.tmp/frontend-bundle-evidence.repro-20260908-run2.json` (20612 bytes) | `d2e47a8eaa48366da64c8ce779d329a4803bbaebf811937130619300159e3622` |

## Three-report comparison

The first Docker report `normal.json` is retained alongside both repeats. All three full report files are byte-identical (each SHA-256 `d2e47a8eaa48366da64c8ce779d329a4803bbaebf811937130619300159e3622`). All three have identical source digest/toolchain identity, all 20 per-entry raw/gzip measurements, and aggregate totals: **15934947 raw / 3183940 gzip**. The two repeats therefore reproduce the first current-controls Docker output exactly under the same reused dependency/toolchain image; this does not establish independent dependency-environment diversity.

## Controlled synthetic regression

A third disposable container `leapview-bundle-regression-control-20260908` (ID `56cfc25a9be9f4478d7539c7fee27d61c1645ec174afb4ba42ebb7443b8bef2c`) used the same pinned image and injected a deterministic 300000-character xorshift32-generated pseudorandom string into `web/components/app/app-shell.ts` only inside the container. Original source SHA-256: `e58ae3606d9af43b91a699d94ecd314c58360348f338f9ca7c25932fccd57290`; injected source SHA-256: `9ebb423a4c19814fa5516b92750d90981e6ae5cdf2193f5f9a660abe78e9905f`.

The same build succeeded, then `bun scripts/frontend_bundle_budget.ts` exited 1 with actionable violations (app-shell 440707 raw / 281948 gzip over 140645 / 34392; aggregate 16235009 raw / 3431496 gzip over 15934947 / 3183940). Full generated synthetic report: `synthetic-regression.json` (SHA-256 `5619013ced8ffd997611b958b055d00c5e50a77501d2dd4e8f92d549f711eea3`). This proves the checker catches controlled bundle growth; it is not product evidence or a runtime-performance claim.

Equivalent shell reproduction of the captured synthetic probe (build the image from an isolated checkout of the candidate revision above; the probe changes only its disposable container):

```sh
docker run --rm -i --name leapview-bundle-regression-reproduction leapview-performance-controls-web:57b0ae997 /bin/sh -s <<'PROBE'
set -eu
echo __ORIGINAL_SOURCE_BEGIN__
sha256sum web/components/app/app-shell.ts
echo __ORIGINAL_SOURCE_END__
bun -e 'const p="web/components/app/app-shell.ts"; const t=await Bun.file(p).text(); const chars="ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!#$%&()*+,-./:;<=>?@[]^_{|}~"; let seed=0x13579bdf; let s=""; for(let i=0;i<300000;i++){seed^=seed<<13; seed^=seed>>>17; seed^=seed<<5; seed>>>=0; s+=chars[seed%chars.length]} await Bun.write(p,t+"\nglobalThis[\"__leapviewSyntheticBundleBudgetRegression\"] = \""+s+"\";\n")'
echo __INJECTED_SOURCE_BEGIN__
sha256sum web/components/app/app-shell.ts
echo __INJECTED_SOURCE_END__
BUILD_REVISION=57b0ae9979d3eb18db5e8f7a921710ae1cd12311 bun run build
if bun scripts/frontend_bundle_budget.ts > /tmp/frontend-budget-check.log 2>&1; then check_exit=0; else check_exit=$?; fi
cat /tmp/frontend-budget-check.log
echo __REPORT_BEGIN__
cat .tmp/frontend-bundle-evidence.json
echo __REPORT_END__
echo __CHECK_EXIT__=${check_exit}
exit "${check_exit}"
PROBE
```

The Bun payload uses seed `0x13579bdf`, xorshift32 steps `seed ^= seed << 13; seed ^= seed >>> 17; seed ^= seed << 5; seed >>>= 0`, and the literal alphabet `ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!#$%&()*+,-./:;<=>?@[]^_{|}~`. It appends `globalThis["__leapviewSyntheticBundleBudgetRegression"] = "<generated string>";` to the fixture source, ensuring the 300000-character payload survives bundling and minification.

The repeated reports are byte-identical, so this archive retains one canonical `normal.json` rather than three copies. Repeat filenames above identify the original local captures. The measured commit predates the later `tsconfig.json` identity coverage fix; these are attributable historical validation reports, not evidence for a later commit or a replacement calibration.
