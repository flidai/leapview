# Bounded performance characterization

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
