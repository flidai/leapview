# CI health collection recovery — 7 October 2026

This continuation belongs to [FAI-1067](https://linear.app/flid/issue/FAI-1067),
the only open issue in the CI/CD Quality & Speed project: 23 of 24 issues are Done.
All original four milestones are complete. The repository-wide follow-through
has completed workflow naming, release alias safety, report correctness,
deployment governance and clean-checkout generation. Whole-CI SLO acceptance
remains open.

The original eight PRs (#805, #807, #808, #814, #815, #816, #819, #821) and six
follow-through PRs (#823, #824, #825, #827, #829, #830) are merged. The former
`ganesh/ci-slo-followthrough` branch is the merged #829 delivery; it is not pending
implementation. This change reuses the clean current worktree, based on main
`7437799c92bfe962c9d083530ce05f180d23d92b`, with a focused continuation branch.
Open #883 and #885 belong to separate Nix and compiler work and are not part of
this change.

## Highest-priority prerequisite

The latest scheduled [health report](https://github.com/flidai/leapview/actions/runs/37328441724)
failed on 5 October at source `c2d09aafcb19c92a704e93038ab104fe1fbdef2f`.
Listing attempt-1 jobs for run 37282298281 returned `502 Bad Gateway` with
`{"message":"Server Error"}`. Collection aborted and uploaded no artifact.
The earlier issue assessment's assumption that scheduled collection was healthy
therefore needs correction before relying on the 12 October review checkpoint.

The collector now retries read-only GETs for HTTP 500/502/503/504, with at most
three requests and one- and two-second waits. It closes each response before
waiting and honors cancellation/deadlines. Exhausted errors still fail collection;
authentication, rate limits, missing evidence, transport errors and malformed
JSON retain their existing handling. This does not retry workflow jobs or alter
the evidence population, thresholds, gates or candidate identity checks.

Regression tests reproduce the original failure and prove recovery retains a
returned failed CI job. They also cover exhaustion, permanent/invalid responses,
response closure and cancellation/deadline interruption. Go's standard
`testing/synctest` exercises timed waits without adding real backoff time to CI.
Focused collector/adapter/platform tests and their race checks passed.

## Regenerated evidence

Read-only collection from 03:44:30 to 03:47:53 UTC on 7 October succeeded using
the modified collector, Go 1.27.1 and Linux amd64. The compact receipt is
[`measurements/health-recovery-2026-10-07.json`](measurements/health-recovery-2026-10-07.json).
The raw JSON SHA-256 is
`9cecdf19bbbb8ed66042d30d33f9a3490669158a349cab4d415b5cea463f816a`.
Raw reports, metadata, classified rows and collection receipts are retained at
`/home/codex/.cache/leapview-ci-tmp/fai-1067-continuation-2026-10-07/` on this VPS.
This local collection does not prove the scheduled workflow has run the fix.

The trailing seven-day population contains 570 runs: 282 successes, 61 failures,
156 cancellations and 71 skips. It retains 207 incomplete-evidence records and
131 unavailable durations. Full-PR/audit/manual p95 is 29m25s (229 duration
observations), merge p95 26m39s (107), queue p95 3m10s (422), and attempt-based
reruns 17/540 = 3.15%, excluding 30 deferred layers. Fourteen selection audits
have zero potential misses. The unchanged 12-minute/3% objectives are not met.

Filtering existing metadata at the #829/#830 merge cutoff,
`2026-10-04T06:39:47Z`, retains 242 observations: 142 successes, 16 failures,
66 cancellations and 18 skips. There are 68 incomplete records, 40 unavailable
durations and eight deferred layers. Actual full-PR duration p95 is 28m11s (109)
and merge p95 26m39s (48). Two audits have zero potential misses. These temporal
cohorts span 190 workflow-source revisions and varied application/workload inputs;
they are observational diagnostics, not a controlled experiment or attributed
improvement. Selective/nightly post-cutoff p95 remains unavailable below 20
observations. Attempt counts also omit new run IDs/requeues.

The latest [Nightly failure](https://github.com/flidai/leapview/actions/runs/37440255591)
is retained: independent dependency scans correctly rejected source-map-js and
KaTeX advisories. [#869](https://github.com/flidai/leapview/pull/869) subsequently
merged the source-map-js correction; KaTeX remediation is already in progress
under [FAI-1126](https://linear.app/flid/issue/FAI-1126) in the separate technical
debt project. This continuation neither duplicates that work nor upgrades
dependencies. The historical failure and required fresh security evidence remain.

## Remaining milestone

Latest main's [successful merge transition job](https://github.com/flidai/leapview/actions/runs/37458672929/job/112252451078)
remains the critical path: workflow 21m08s, transition job 20m55s, initial queue
5s, setup 1m58s, image build 10m14s and required fixture 7m41s. Within the build,
source generation took 231.7s, map extraction 96.1s and application compilation
129.1s. These spans are nested/overlapping and cannot be summed as independent
savings. One observation does not establish a percentile or performance change.

After this collector fix is reviewed and validated on GitHub, the next bounded
task is to time individual image generator commands and test the map-asset
dependency barrier identified in the 5 October assessment. Preserve fresh
generated inputs, asset digests, broad application generator dependencies and
exact image identity. Require the existing three matched-pair screen and ten
confirmation runs, including producer/transfer/runner costs, before adoption.
Compiler-mount persistence and recovery parallelism remain rejected;
predecessor trimpath remains deferred. Keep FAI-1067 open and project health
At Risk until its unchanged SLO acceptance is satisfied or explicitly resolved.
