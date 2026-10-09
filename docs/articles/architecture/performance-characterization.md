# Bounded performance characterization

## Compile-once CI screening

`nix develop --no-update-lock-file -c python3 scripts/performance_ci_study.py /absolute/new-evidence-directory` compares the maintained application shard discovery/test commands with a private compile-once binary experiment. It runs three predeclared cold pairs and three warm pairs, alternating arm order. Each pair and arm uses its own initially empty Go build cache; its warm arm reuses only its corresponding cold cache. Dependency modules stay warm. Package compilation and application shard execution are serial, with two Go runtime CPUs, `-p=1`, a 2 GiB Go memory target and the existing 4 GiB observed process-tree RSS stop. These bounds describe this experiment, rather than the hosted scheduler.

All four stable shard patterns and every executed test/subtest, outcome and skip reason must match exactly. The maintained PostgreSQL skip environment and MinIO dedicated-lane exclusion are explicit and identical in both arms. Their separate conformance lanes, non-application packages, generated checks and frontend suites remain required; this experiment cannot substitute for canonical CI. The raw per-step command, log, exit, cleanup and resource receipts are retained. The first failure or discrepancy stops the experiment. Three pairs per cache condition support screening only: a promising change requires a separately predeclared confirmation with at least ten pairs before independent review can authorize adoption. The screening decision retains the maintained implementation; it never selectively retries or extends a run after observing results.

## Olist route profiles

The cached Olist fixture supplies the dense overview, virtual table showcase and three governed tiled maps. `performance_browser_profile.py /absolute/new-evidence-directory` runs three fresh Chromium processes through those routes, retaining instrumented parse/render traces, CDP counters, HTML byte identity, deterministic offline gzip/Brotli sizes and actual resource encoded/decoded/transfer sizes. Offline compression does not imply production HTTP compression. Four table scroll positions must move the virtual window and settle loaded rows; maps must render and request successful non-world tiles. Navigating to an empty document must close every observed client SSE request. This proves client teardown, rather than server reader release.

Before running, `LEAPVIEW_BASE_URL` and `LEAPVIEW_BROWSER_PROFILE_SERVER_RECEIPT` select our dedicated native fixture and its private admission receipt. The receipt contains the frozen `source` commit/tree, `binary.path`/`sha256`, native `pid`/Linux `processStart`, exact `baseURL`, `datasetRoot` and `datasetFiles` path/SHA256 rows covering every CSV once. The driver rechecks executable/process/source/dataset bytes before and after profiling. Create the receipt only after the maintained generated-input, binary build, PostgreSQL pool bootstrap, managed-data publication and readiness checks succeed. Authentication can use the maintained local development session through `LEAPVIEW_QA_STORAGE_STATE`; credentials and raw traces stay private.

The generic maintained interaction runner also accepts `scripts/performance/olist.json` through `LEAPVIEW_PERF_SCENARIO`. It measures purchase-month, category and delivery selections and rapid category supersession against the same overview. The five-session study preserves its existing correctness and absolute guards. Route profiles are descriptive and make no p95 or optimization claim; interaction p95 requires all one hundred samples per scenario across the five fresh sessions.

These studies reuse production boundaries and keep correctness checks active.
They describe the current implementation. They do not nominate an optimizer,
accept a baseline, infer user latency from Go microbenchmarks or promise a
production capacity limit.

Run one measurement lane at a time on a frozen, clean source. Compilation and
CI must finish before measuring. An exclusive measurement slot is required;
the recorded host load and cgroup allocation are evidence, not proof that other
work cannot interfere. Keep source, toolchain, fixture and binary identities,
raw logs, failures and cleanup outcomes. A failure ends the declared run;
repeating until a desirable result appears is not qualification.

## Native reader capacity

```sh
nix develop --no-update-lock-file -c python3 scripts/performance_capacity.py \
  prepare /absolute/private/evidence/native-capacity
# After compilation and all competing work has ended:
nix develop --no-update-lock-file -c python3 scripts/performance_capacity.py \
  run /absolute/private/evidence/native-capacity
```

The prospective protocol fixes two Go scheduler CPUs, an ascending concurrent
reader ladder of 1, 10, 20 and 100, and three fresh processes per family and
step. Each process executes one complete concurrent batch. Its 120-second Go
timeout and 150-second outer timeout bound the experiment, not a product SLO.
The driver stops on the first error, timeout, observed cgroup OOM kill, missing
or altered benchmark row, source drift or cleanup failure. An observed OOM may
come from unrelated cgroup work; it still invalidates the controlled study.
Unavailable cgroup accounting is recorded as unknown. The runner separately
enforces a prospective four-GiB process-tree RSS stop on the observed 32-GiB
host, sampled every 100 milliseconds. This bounds the experiment, not a product
memory budget; brief peaks can fall between samples. Source admission binds the
maintained Go receipt fingerprint of selected Go/native/embed inputs, including
ignored generated files and effective settings, before and after compilation
and measurement. Unexpected untracked Go code is rejected. External system
libraries/headers and unrecorded runtime environment remain explicit identity
limitations rather than being inferred from HEAD.

| Family | Fixed fixture and measured boundary | Correctness guards |
| --- | --- | --- |
| Compiler | Existing authored 20-resource, 19-edge graph; simultaneous `Compile` calls after warm preflight | Every canonical bundle and digest match the independently checked fixture |
| Authorization | 100 resources, grants, bindings and policies; simultaneous public allowed and denied `AllowsTyped` decisions | All decisions match and published graph validation remains immutable |
| Pagestream | One shared stream versus independent streams; real loopback HTTP clients through `Broker` and `SignalStream` | Every client receives the ordered patch; HTTP handlers and subscriptions drain |
| Managed revision | Deterministic 100,000-row CSV in real content-addressed filesystem storage; cold materialization versus warm verification of one shared immutable revision | Production digest/size verification, lease release and revision deletion |
| Query | Existing five governed warm-cache workloads, including wide charts and JSON/Arrow table windows | Exact warm hits, zero physical queries, row types and nulls through the existing fixture checks |

Go reports batch `ns/op`, `B/op`, `allocs/op` and explicit work per batch.
Whole-process CPU and Linux peak RSS include fixture setup, measured work and
cleanup. They are separate from benchmark-timed allocations and latency. Cold
and warm are workload conditions, not competing production implementations.
The managed study does not measure PostgreSQL activation, remote upload,
dashboard settlement or complete refresh/recovery. Existing lifecycle,
retention, failure and cache-cutover qualification remains necessary.
The existing query benchmark also prints in-process request percentiles. The
one-batch driver does not qualify or interpret them as end-to-end p95; it retains
the complete log and reports this study as batch characterization only.

## Maintained browser interaction study

Prepare and admit a fixed running MovieLens dashboard and its matching server
log, then run:

```sh
LEAPVIEW_BASE_URL=http://127.0.0.1:PORT \
LEAPVIEW_PERF_LOG=/absolute/private/server.log \
nix develop --no-update-lock-file -c python3 scripts/performance_browser_study.py \
  /absolute/private/evidence/browser-interactions
```

The driver opens five fresh browser processes with twenty measured operations
per declared scenario in each, retaining all 100 samples per scenario. The
maintained harness warms each interaction separately and verifies typed target
updates, table windows, rapid-filter supersession and browser/network health.
Its existing absolute latency/query guardrails are enabled; custom ceiling
overrides are rejected. Missing raw samples, invalid timings, incomplete server
observations or correctness/guardrail failures stop the run. The server remains
warm across sessions. Source identity alone does not admit that server's image
or dataset: retain its independent build and fixture admission alongside these
reports. Dense dashboard, scrolling, map and teardown workloads require their
own applicable maintained scenarios rather than extrapolation from MovieLens.
The browser launcher owns and subreaps its descendants, including browser
processes in separate groups. Normal exit, timeout and RSS termination all
require bounded process-tree cleanup before admitting a session.

## Experiment decisions

The earlier compiler experiment adopted only its confirmed warm-path change;
its graph-size ladder was not a concurrent reader-capacity study. The earlier
authorization experiment failed its unchanged-control timing guard and ended
inconclusive, with no candidate adoption. These finite decisions remain valid;
new measurements must not overwrite their source identities or extend their
samples selectively.

For any new optimization, predeclare its primary metric and guardrails, use
three alternating paired screens, and confirm a promising change with at least
ten fresh pairs. Adoption requires statistically supported ten-percent primary
gain, no material five-percent relevant guardrail regression, and preserved
absolute budgets and correctness. Rejecting or remaining inconclusive is an
acceptable decision. These descriptive capacity and browser runs alone do not
satisfy that adoption rule.
