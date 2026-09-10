# Go application validation critical path

Profile updated: 2026-09-10. This report compares two successful hosted
`merge_group` observations. It does not change workflows, concurrency, caches,
PostgreSQL settings, or validation coverage.

Experiment: **Parallelize disjoint Go application validation branches**

Result: **Rejected**.

Reason: Concurrent execution caused runner contention and increased Go
application validation, PostgreSQL conformance, and overall merge duration.

## Result — rejected experiment

PR [#558](https://github.com/flidai/leapview/pull/558) did not reduce the Go
application critical path. The parallel candidate took **18m55s**, 56 seconds
longer than the **17m59s** sequential observation. Overall merge validation
increased from **18m10s** to **19m52s**.

The branches did run concurrently. The ordinary shards and external branch
started within one millisecond of each other, and all required validation
completed successfully. The lost saving came from slower work on the shared
runner:

- The ordinary shard envelope increased from **2m33s** to **5m02s**.
- The MinIO command envelope increased from **10s** to **2m07s**, although the
  test itself still reported about six seconds. The additional wall time is
  consistent with dependency, compilation, and cache/filesystem work while the
  ordinary shards were doing the same.
- PostgreSQL conformance increased from **9m19s** to **10m47s**.
- The external MinIO then PostgreSQL branch therefore increased from **9m29s**
  to **12m54s**. That 3m25s increase was greater than the 2m33s of sequential
  ordinary-shard work that the experiment attempted to hide.

The best-supported classification is **runner limited**, meaning the overlapping
workloads contended for resources on one hosted runner. Concurrent Go compilation
and test execution are the likely mechanism. The exact CPU versus filesystem
contribution is unknown because GitHub's job log contains no CPU, load,
disk-throughput, memory-pressure, or throttling telemetry.

## Hosted evidence

| Observation | Workflow | SHA | Go application | Overall merge | Result |
|---|---|---|---:|---:|---|
| Sequential | [Run 34325304846](https://github.com/flidai/leapview/actions/runs/34325304846) | `4b9ee86fbd331f9f216a8ee2118e612d04422b4f` | **17m59s** | **18m10s** | Success |
| Parallel | [Run 34460087405](https://github.com/flidai/leapview/actions/runs/34460087405) | `be27673acde5c43242427e6a8adc50a91ad2ee5a` | **18m55s** | **19m52s** | Success |

The parallel SHA is PR #558's merge commit. Its application lane invokes
`task --parallel test:go:app:shards test:go:external`. The hosted log records
four ordinary shard completions, the MinIO contract, and 54 unique
source-inventoried PostgreSQL packages. The job, full validation, and CI gate
all completed successfully, so this is a coverage-complete observation.

Both observations missed their exact Go validation cache keys and hit the Bun
download cache. Both used Ubuntu 24.04 hosted runners with approximately 16 GB
of reported memory, runner version 2.337.0, and Docker Server 28.0.4. The runner
region and image changed between observations. Together with intervening code
changes and the single observation in each mode, this prevents treating the
comparison as a controlled A/B test.

## Timing comparison

Durations come from GitHub step timestamps and Task markers in the application
job logs. Nested branch rows are included in application validation and must not
be added to the job total.

| Phase | Sequential | Parallel | Change |
|---|---:|---:|---:|
| Runner wait | 3s | 4s | +1s |
| Checkout and job bootstrap | 8s | 6s | -2s |
| Cached toolchain setup | 1m14s | 1m12s | -2s |
| `ci:prepare` | 3m59s | 4m14s | +15s |
| Go application validation | **12m02s** | **12m54s** | **+52s** |
| Ordinary application shards | 2m33s | 5m02s | +2m29s |
| MinIO command envelope | 10s | 2m07s | +1m57s |
| PostgreSQL conformance | 9m19s | 10m47s | +1m28s |
| Cache save and cleanup | 36s | 29s | -7s |
| **Go application job** | **17m59s** | **18m55s** | **+56s** |

The parallel branch timing was:

```text
09:26:02  ordinary shards begin ---------------------- 09:31:04
          external begins
09:26:02    MinIO build and test -------- 09:28:09
09:28:09    PostgreSQL conformance ------------------------------ 09:38:56
                                                               completion
```

For comparison, the sequential observation ran ordinary shards from 07:48:48
to 07:51:21, MinIO to 07:51:32, and PostgreSQL conformance to 08:00:50.

## Contention analysis

The overlap created more runnable work than the prior lane on one hosted
runner. During the first two minutes, up to three ordinary shard processes and
the MinIO `go test` compiled and tested the same package concurrently. Once
MinIO completed, PostgreSQL conformance used `go test -p 4` while ordinary
shards continued for nearly three minutes. These processes shared the runner's
CPU, filesystem, Go module cache, Go build cache, memory, and Docker daemon.

The slowdown begins before PostgreSQL container work. The four ordinary test
durations changed from 23.7–42.4 seconds to 59.2–114.9 seconds, and the MinIO
test still reported 6.1 seconds while its command took 2m07s wall time. This is
direct evidence of Go build and test contention during overlap.

PostgreSQL also slowed. Excluding the four overlapping ordinary-shard records,
its 54 reported package durations summed to about **2,282 seconds**, compared
with **2,006 seconds** before, an increase of roughly 14%. The parallel log
records 497 container terminations versus 478 in the sequential log. That 4%
workload increase and the runner-image change make this an observational
comparison rather than a controlled A/B test, but neither accounts for the
earlier ordinary-shard and MinIO slowdown.

There is no direct evidence that Docker itself was the limiting resource. The
parallel run contains no out-of-memory, no-space, timeout, connection-refused,
or Docker-daemon errors; container lifecycle completed without a logged failure,
and every package passed. Docker and database work competed for the same runner
after 09:28:09, but the logs do not expose daemon CPU, disk, or memory
utilization.

| Classification | Finding |
|---|---|
| Runner limited | **Primary classification, medium confidence.** Independent workloads slowed when they shared one runner; this does not establish a runner-fleet or queueing regression. |
| CPU limited | Likely mechanism, but not directly measured. Concurrent compilation and testing are the strongest signals. |
| I/O limited | Possible secondary mechanism through shared build caches and container storage; no throughput data is available. |
| Docker limited | Not supported as the primary cause. There were no daemon or container failures, and slowdown started before PostgreSQL. |
| Unknown | The precise CPU-versus-I/O share remains unknown because hosted telemetry was not captured. |

## Decision

The experiment is **Rejected**. Runner contention increased the Go application
duration by **56s**, PostgreSQL conformance by **1m28s**, and overall merge
validation by **1m42s**. It preserved coverage but increased each measured
critical path, so further same-runner parallelization is not recommended. The
pre-PR #558 serial ordering is restored in `Taskfile.yml`.
