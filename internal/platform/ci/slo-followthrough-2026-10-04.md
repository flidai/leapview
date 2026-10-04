# CI latency and reliability follow-through

FAI-1067 follows the corrected reporting contract merged in #825. The read-only
seven-day collection at `2026-10-04T03:36:35Z`, using source
`36ef860e61c94cef65eb2d8ef3fa39d2b0bd3c25`, contains 522 runs. The compact baseline
and critical-path measurements are in `measurements/slo-followthrough-2026-10-04.json`.

| Cohort or indicator | Observations | Result | Existing objective |
| --- | ---: | --- | --- |
| Full PR | 208 | p95 29m25s | At most 12m |
| Merge | 84 | p95 25m04s | At most 12m |
| Selective / nightly | 13 each | p95 unavailable | At least 20 observations before reporting p95 |
| Queue | 357 | median 4s; p95 1m47s | Report separately from execution |
| Reruns | 18 | 3.6% | At most 3% |
| Selection audits | 14 | Zero potential misses | Cancelled evidence remains inconclusive |

There are 139 cancelled runs, 207 incomplete-evidence records, 159 unavailable
durations, and 18 deferred layers. Their evidence is retained. The changed
reporting population and collection window do not demonstrate a speedup.

The 66 failures comprise 35 PR-workflow runs, 24 merge runs, and seven nightly
runs. The receipt reconciles recent candidate-specific route/snapshot and
security-policy failures with later passing revisions, including the genuine
historical `braces` vulnerability finding. Current-main CI and security and the
later nightly pass. These later successes do not erase earlier failures or
establish that an unchanged retry fixed them; their source revisions are retained.

## Critical path

Four successful exact-candidate merge runs
[37134195995](https://github.com/flidai/leapview/actions/runs/37134195995),
[37134196876](https://github.com/flidai/leapview/actions/runs/37134196876),
[37134197372](https://github.com/flidai/leapview/actions/runs/37134197372), and
[37134198239](https://github.com/flidai/leapview/actions/runs/37134198239)
spent 21m54s–22m42s in the schema-transition job. Pinned toolchain setup took
4m18s–4m29s, image construction 9m34s–10m14s, and the required transition task
6m54s–7m10s. The browser-backed end-to-end test itself took 348–366 seconds.

Go and Bun validation caches were hits in all four logs. Go restoration took
26–30 seconds and Bun restoration 6–13 seconds. Nix environment realization took
214–221 seconds and fetched 366 store paths. Runner contention is visible in
some scheduling gaps, but the timestamps do not establish an account quota or
justify purchasing capacity.

## Verified fixes

The host compiler now uses the already-locked Nix Go 1.27.1 package, aligning
with the existing application-image compiler. Its upstream source includes the
[Go fuzztime cancellation fix](https://github.com/golang/go/issues/75804).
The upstream `TestScript/test_fuzz_fuzztime_timeout` regression passes, and the
product's exact bounded scheduler fuzz command passes without a crasher. No
fuzz assertion, duration, retry policy, or SLO threshold is weakened. The
separately pinned sqlc generator compiler remains governed by its existing
generator contract. The map-archive encoder's separately tested Go 1.26.8 pin
also remains in place to preserve its compressed archive digest. No Nix lock or
JavaScript dependency update is required.

Only `scripts/check_generated_snapshots.sh` and
`scripts/generated_snapshot_check.test.ts` are newly excluded from Docker
contexts. They are host validation inputs and remain in the checkout for the
complete PR and merge contracts. Neither is consumed by image generation.
They were the only changed paths between `cecbd974e` and `36ef860e`, yet broad
Docker `COPY` operations invalidated source generation and map extraction.

Those two serial vertices cost 341.6 seconds in the latest run. That is a
measured opportunity for matching layer reuse, not a measured whole-build
saving. The application compilation embeds each candidate's revision and build
time and must still execute when that identity changes. No saving from that
vertex is counted, and no new cache namespace, publisher, or compiler mount is
introduced.

A targeted scratch-image `COPY` probe with the actual ignore rules verifies
both helper mutations retain the same copied layer and hit cache. A mutation to
`internal/app/router.go` invalidates that layer. Under the original ignore rules,
each helper mutation invalidates the layer. All six assertions pass on Docker
29.1.3 / BuildKit 0.26.2. Layer digests and controls are retained in the measurement
receipt; this checks context behavior, not complete application build time.

The complete production Dockerfile also builds on Linux amd64. Its immutable
local image reports the exact clean `5be7e200d` source revision, matching OCI
revision and architecture. This precedes timing-only test and evidence-document
additions and is not a published or deployable qualification result. Required
hosted validation must still build and test the final candidate revision.

## Completion criteria

These fixes do not establish a passing whole-CI SLO. Retain FAI-1067 as open
until representative, comparable post-change cohorts meet the unchanged
12-minute p95 and 3% rerun objectives, or an explicit SLO outcome is accepted.
Record source/toolchain/platform/work identities, queue delay, producer cost,
cache transfer/export, storage, and complete consumer runner cost in any further
experiment. A small cache probe or a successful individual run cannot satisfy
that acceptance.

The current Go 1.27.1 validation shell's uncompressed NAR closure is 4,452,038,472
bytes across 347 paths. This is not its archive size or transfer cost. Four
timing-only logs now identify the predecessor, transition, initial browser/auth
and handoff, and replacement publication/browser phases of the existing test;
all execution, assertions and timeouts remain in place.

One complete receipt-free local diagnostic passes against the immutable image
above in 509.93 seconds: predecessor 283.285s, candidate transition 58.858s,
initial browser/auth and handoff 37.902s, replacement publication/browser
113.206s. The remainder includes setup, assertions and cleanup. Two local PR
contracts were also running, so this is a profiling sample, not a comparable
speedup measurement. The diagnostic writes no deployable transition receipt and
does not substitute for the required final-candidate test.

The next measured candidates are the complete validation-shell realization and
the transition fixture's internal phases. Any toolchain reuse must verify the
locked closure and account for archive production and transfer. Fixture profiling
must preserve browser, isolation, recovery, revision, and migration assertions.
Do not revive rejected compiler-mount persistence or recovery-concurrency changes
without new evidence.
