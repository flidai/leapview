# CI critical-path follow-up for #667

## Observed bottlenecks

The successful [merge run 35821278421](https://github.com/flidai/leapview/actions/runs/35821278421)
spent 891 seconds in the application lane. Its validation step took 542 seconds:
about 78 seconds for ordinary application shards, 8 seconds for MinIO, and
456 seconds for PostgreSQL conformance. The unsharded PostgreSQL application
package accounted for 406.444 seconds of that last phase.

The [nightly run 35701145868](https://github.com/flidai/leapview/actions/runs/35701145868)
spent 1091 seconds in full validation, including 731 seconds in its serial extras
step. The merge workflow already overlaps static and runtime extras and runs UI
route QA on the frontend site runner. These are observations from different
commits and runners, not a controlled before/after benchmark.

## Scheduling changes

Nightly uses the existing merge layout: UI route QA runs on the site shard after
preparation, frontend validation, and generated-artifact checks. The site retains
the 120-minute budget and visual failure artifacts. Backend extras run through
`ci:full:extras:hosted`, including desktop tests and both static/runtime branches.
The strict nightly gate still requires every lane, including security and
JavaScript dependency evidence refresh. A workflow regression compares the
nightly and merge lane definitions so their coverage cannot silently diverge.

PostgreSQL conformance retains its complete source-derived package inventory.
For `internal/app`, it compiles one binary with `integration duckdb_arrow`, lists
that exact binary, and assigns every runnable test, example, and fuzz seed to one
of four balanced shards. Each shard gets its own disposable PostgreSQL server;
tests within a shard remain serial, with fresh databases and roles per test.
MinIO retains its existing separate lane.

Each wrapper also has a distinct, persistent shell parent. Testcontainers derives
its cleanup session from its parent's PID and creation time. Sharing that parent
between wrappers allowed a completed worker's cleanup to remove a slower
worker's database. The shell forwards termination to its wrapper and waits for
cleanup. Completed worker PIDs are removed from the cancellation set.

The remaining PostgreSQL packages run afterward with the existing four-worker
limit. The two waves never overlap. Compilation, test listing, an empty shard,
any shard failure, or a package-sweep failure fails the lane. Shard logs are
retained until all workers have been joined and are printed even on failure.

## Nightly clearance blockers

Branch-dispatched nightly scans need `origin/main` for their history baseline.
The security job now fetches full history instead of only the dispatched branch.
A regression requires that checkout contract.

Nightly dependency clearance also rejected four unwaived `fast-uri` findings.
The root override moves from 3.1.5 to the patched 3.1.6 release, with only that
resolved package changed in the lockfile and fresh vulnerability evidence. The
[upstream advisory](https://github.com/advisories/GHSA-5jgf-p345-68v8) identifies
3.1.6 as patched. The root live audit now returns no findings. No exception or
severity threshold changes are involved.

## Verification and rollout

Executable fixtures cover the exact tagged compilation, complete/disjoint test
selection (including examples and fuzz seeds), distinct cleanup-session parents,
serial execution, cancellation cleanup, the existing MinIO exclusion, error propagation, and routing
the application package exactly once. The parent-isolation regression rejected
the shared-parent implementation before the fix. Workflow tests preserve the
nightly lane inventory, strict gate, UI artifacts, and complete hosted extras.

The complete local PostgreSQL conformance runner passed in 5m26.974s after the
cleanup isolation fix, including all four application shards and the remaining
package inventory. This is a local observation, not a hosted p95 claim.

A new hosted measurement is still required before claiming the 6/12-minute p95
targets are met. Cache warmup, preparation, runner contention, and UI route QA
remain part of elapsed time. Keep #667 open until independent hosted runs and the
rolling health report establish those limits; thresholds and evidence populations
are not relaxed by this change.
