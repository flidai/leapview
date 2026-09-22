# CI health response — #667

## Observed problem (2026-09-22 UTC)

[Issue #667](https://github.com/flidai/leapview/issues/667) records trailing-week
p95 values of 21m57s for merge validation, 35m23s for nightly, 22m26s for full PRs,
and 24m25s for selective PRs. The limits remain 12 minutes and 6 minutes. These
are historical populations, not measurements of this change.

The [report artifact](https://github.com/flidai/leapview/actions/runs/35602667233)
contains 48 runs with incomplete evidence. Forty missing plan artifacts belong
to cancelled PR runs. Of 46 missing durations, 41 belong to cancelled PR runs,
three to failed PR runs, one to a successful merge run, and one to an unfinished
merge run. Cancellation explains much of the missing evidence but does not
establish success or justify silently excluding these runs.

The report now groups problems by run conclusion and includes up to three run
IDs per group. Every run remains in the JSON artifact, and all alerts, population
definitions, provenance checks, missing-duration rules and thresholds remain
unchanged. This also exposes successful runs with missing evidence for follow-up.

## Current critical path and cache evidence

[Successful PR run 35743736852](https://github.com/flidai/leapview/actions/runs/35743736852)
finished at 15:09:39 after being created at 14:55:39. Its application job ran
14:58:01–15:08:50. GitHub step timestamps show 77 seconds of toolchain setup,
183 seconds of preparation, 359 seconds of application validation, and 20 seconds
of cache cleanup/publication. The package lane completed earlier at 15:05:31.
These are one run's observations, not a new p95 or a controlled comparison.

The application log records an exact Go-cache miss for manifest/tool hash
`057295802563bd2e38686fbdcd481806938a408d91861547079dba68416c288d`.
Repository cache API inspection on September 22 found 724 archives occupying
107,128,808,947 bytes. There were no default-branch archives for the application,
package, or full-validation workloads, while application archives existed on
recent PR and merge-queue refs. Those refs are inaccessible to new candidates.
The absent main archives and large candidate archives are consistent with
cache eviction pressure; the API does not supply an eviction audit trail.

## Change and correctness

The shared setup action now lets successful default-branch workloads publish
Go caches at job completion. Other refs use the pinned restore-only action,
preventing each candidate from adding gigabytes of ref-local Go archives.
Nightly runs the common workload IDs on the default branch. PR-only readers
explicitly select compatible package, application or full-validation producers,
including the selective quality job that never runs in a manual full CI plan.
Reader mappings never change publication identity. This supplies scheduled
producers without adding another workflow or scheduling barrier. Unrelated Bun, browser, Terraform and native packaging caches retain
their existing behavior.

The exact key still includes dependency manifests, Taskfile and setup action.
A fallback retains workload, OS, architecture, image and installed Go version
while allowing reuse across manifest/tool-input changes. Every generation and
validation command still runs with its requested dependencies and tool versions.
No workspace, database, credentials or validation evidence is cached.

[GitHub's cache reference](https://docs.github.com/en/actions/reference/workflows-and-actions/dependency-caching)
documents immutable archives, prefix matching, default-branch visibility and
ref restrictions. [Go's cache contract](https://pkg.go.dev/cmd/go#hdr-Build_and_test_caching)
documents source/compiler/options-based invalidation. We retain the image and
compiler boundaries because Go does not detect arbitrary changes to external
C libraries. This is the existing hosted-image boundary, not a new guarantee
about image patch versions.

## Verification and completion criteria

Regression tests require mutually exclusive default-branch publication and
candidate restoration, compatible reader mappings with scheduled producers,
bounded fallback identity, pinned
actions and no cache-based validation bypass. Reporting tests cover cancellation
versus failure, unchanged alerts, deduplicated counts, bounded deterministic
examples and Markdown escaping.

After merge, run the existing nightly workflow on the default branch to seed
successful workloads. Inspect its cache save outcomes and the next independent
PR/merge group's restore outcomes, including archive size and setup/preparation
time. Compare cold and warm runs separately. Existing candidate archives need
not be deleted; they will age out under the normal cache policy.

Keep #667 open until hosted evidence establishes the latency limits and explains
remaining reporting gaps. This fix does not by itself prove the 6/12-minute
limits, eliminate external-service test execution, or resolve the separate
nightly dependency-security failures. The seven-day report will continue to
contain pre-change runs until they leave the window.

Local verification for this patch: the CI planner/gate, planner CLI, reporter and
complete architecture test packages passed after generated inputs were ready.
The new cache/report regressions failed against the previous implementation and
passed after the fix. Go vet, quality-budget/exception checks and Actionlint on
the PR, merge and nightly workflows passed (the existing `stacked` activity is
excluded from Actionlint's unsupported-event check). A live, read-only one-day
report completed over 99 runs and rendered the new diagnostics; its remaining
alerts were preserved. Full `task ci` was also started; its final outcome is
recorded in the PR rather than inferred from these focused checks.
