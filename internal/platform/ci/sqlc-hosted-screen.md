# Hosted SQLC full-image experiment

[FAI-1150](https://linear.app/flid/issue/FAI-1150/qualify-sqlc-executable-layer-reuse-at-the-hosted-full-image-boundary)
continues the [local screen](sqlc-layer-screen.md). The local 153s versus 71s
consumer median excluded most image work and increased exported storage 6.1%.
It warrants measurement, not production adoption.

`Qualification / SQLC image cache` is a manual experiment on trusted `main`.
It runs two cold producers and six consumers (three samples per mode), capped
at two concurrent samples. Each sample gets a fresh Ubuntu 24.04 amd64 runner
and pinned BuildKit container. The run SHA fixes the source, recipe inputs,
workflow, build revision and timestamp; later main commits cannot enter it.

The baseline retains the entire original Dockerfile and generator script. In a
disposable archive, the treatment inserts the pinned SQLC tool stage and replaces
only its `go run` invocation with that executable. All other generators, map
assets, web build, Go compilation, extension supply and runtime layers remain.
Production Docker/Nix/generator files are unchanged.

Producers export only to `sqlc-image-screen-RUN-ATTEMPT-MODE` GHA v2 scopes.
Consumers read only their matching producer scope and never write a cache.
The caller grants producers `write` (read and save) and caps consumers at read-only.
BuildKit's GHA exporter reads its mutable cache index even without `cache-from`,
so save-only tokens cannot publish a complete cache. Producers still have no
image cache import. A tiny scratch-image export checks the same backend before
the expensive build, using a separate run-scoped `-access-probe` cache. Its
overhead is outside the measured image-build clock and included in full job cost.
Neither mode imports production image caches or exports mutable compiler mounts.
A harmless source marker changes between producer and consumer so generation
must execute; a consumer cannot pass on a completely cached image. Sample pair
labels do not promise execution order or identical physical CPUs. Host and job
receipts must be inspected for comparability.

Every sample loads the complete runtime image, verifies its revision/clean flag,
and runs the existing required historical-transition fixture against its local
immutable image ID. No image is pushed, released or deployed. A separate cached
proof target exports every generated input copied from sourcegen into build/web.
The comparison requires identical paths and file hashes in all eight samples,
actual SQLC execution, stable tool identity and treatment-consumer tool-layer
hits. Build history is captured before diagnostic solves can replace it.

## Running after merge

The workflow must first exist on the default branch for
[GitHub manual dispatch](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/manually-run-a-workflow).
After its reviewed implementation is merged:

```sh
gh workflow run sqlc-image-screen.yml --ref main
```

This starts a bounded three-pair screen, not ten confirmations or adoption.
Do not rerun failed jobs as though they were original successful samples. A
fresh attempt creates fresh cache scopes; preserve both attempts and explain
any concrete fix or retry rationale. An unsuccessful producer blocks consumers;
the comparison still runs to record missing/failed evidence. No automatic retry
or relaxed qualification contract is introduced.

## First hosted attempt and cache-access correction

[Run 37621062598](https://github.com/flidai/leapview/actions/runs/37621062598),
attempt 1 at `5f98533d6d4dee5dac6bc4eaef066020fbb0ea68`, built both images
but failed both GHA cache exports with `permission_denied`. All six consumers
were skipped and the comparison failed. Neither image qualification nor generated
output comparison ran. The [compact failed receipt](measurements/sqlc-hosted-screen-failure.json)
retains the source, attempt, failure population and raw-log hashes. Its clocks
include failed exports and are not successful producer or speedup measurements.

The pinned BuildKit exporter always calls
[`SaveMutable` for its index](https://github.com/moby/buildkit/blob/dddd5621af04ea57823085c93a063383f71d3173/cache/remotecache/gha/gha.go#L316),
which begins with a
[`Load` lookup](https://github.com/moby/buildkit/blob/dddd5621af04ea57823085c93a063383f71d3173/vendor/github.com/tonistiigi/go-actions-cache/cache.go#L411).
GitHub's [`write-only` mode](https://docs.github.com/en/actions/reference/workflows-and-actions/dependency-caching#controlling-cache-access-with-cache-mode)
denies reads, even for export metadata. The first denied request is not identified
in the hosted logs, but the exporter/read incompatibility is established by the
pinned source. Producer `write` access repairs that incompatibility without adding
an image cache import. The new scratch export probe detects backend access failure
before full-image work; an unsuccessful probe blocks the measured build.

After the correction is reviewed and merged, dispatch a fresh screen and retain
this original failure. The correction has not yet demonstrated successful hosted
cache publication, consumer hits, image qualification or adoption. Probe caches
also count toward repository cache retention; no eviction or billing attribution
is inferred from local inventory bytes.

## Remaining whole-CI work

The two successful merge runs immediately before this screen are observations,
not matched performance samples or a new p95 cohort:

| Boundary | [Cumulative candidate `5f98533d6`](https://github.com/flidai/leapview/actions/runs/37618840756) | [Direct candidate `1b535b902`](https://github.com/flidai/leapview/actions/runs/37618768857) |
|---|---:|---:|
| Complete CI run | 18m24s | 19m41s |
| Historical-transition job | 16m39s | 19m27s |
| Image build | 471s | 579s |
| SQLC source generation | 59s | 86s |
| JSON-schema source generation | 63s | 93s |
| Historical fixture step, including preparation | 323s | 420s |

In the cumulative run, full merge validation finished two seconds after the
transition job. Accelerating SQLC alone therefore would not have advanced that
run's gate with other lanes held unchanged. Full validation also contains a long
runtime-test tail. Preserve both
paths and all required coverage when evaluating the remaining 12-minute target;
do not infer whole-CI completion from one generator optimization.

The next bounded generator candidate is invoking the existing schema exporter
through a small build tool rather than compiling the full application CLI.
It must preserve output and be measured together with the later application
build: reduced compiler warming can shift costs downstream. This is an
investigation candidate, not an adopted change. Retain the rejected cache-mount
and recovery-concurrency decisions. Bundle related generator evidence and
justified changes into a coherent follow-up instead of opening a PR per phase.

A [bounded local probe](measurements/schema-export-local-probe.json) at
`5f98533d6` completed one sequential pair using Go
1.27.1, separate cold compilation caches, equally seeded module downloads and
the existing `internal/project/cli.ExportSchema` function. All eight schema
files and both later application binaries matched byte-for-byte. Export time
was 121.50s versus 53.07s; subsequent `-tags=duckdb_arrow -trimpath` binary builds
were 136.25s versus 149.63s; combined time was 257.76s versus 202.71s. Concurrent
VPS CI work, one pair and the non-Docker environment prevent attribution or
adoption. The observed 9.8% downstream increase exceeds the unchanged 5%
other-cost screen; do not describe the 21.4% combined difference as an accepted
optimization. No schema adapter or production generator change is included.

## Evidence and interpretation

Each sample artifact retains preparation identities, experimental recipe,
BuildKit history and logs, generated manifest, image identity, historical fixture
receipt, host information, clocks and outcome. Source archives, generated proof
trees and cache contents are excluded from artifacts. Failed samples still
upload available diagnostics. Artifacts expire after 30 days; commit compact
reviewed measurements and hashes before expiry when making a decision.
History JSON retains only selected identity/timing fields, and native raw build
record uploads are disabled: cache-import attributes can contain credentials.
Artifact names include the attempt, so reruns cannot overwrite or mix receipts.

The comparison records full-image build elapsed (including GHA import/export and
Docker load), qualification elapsed and complete sample-job time from GitHub.
Producer and consumer costs remain separate. It also reports median consumer
plus one third of producer job time, explicitly assuming three consumers per
producer, and the entire-producer allocation for a one-consumer scenario.
Job time includes diagnostic and fixture setup costs and action cleanup/upload;
it is not monetary billing or queue time.

After its measured build, each producer exports a local `mode=max` inventory
from the same builder and arguments. This additional diagnostic has a separate
clock and is included in full job cost. Its compressed byte count is a storage
proxy: GHA deduplication, retention and billed storage are **not attributed** by
that count. The diagnostic export is removed after recording its size; no global
cache prune runs. The two run-specific GHA scopes remain subject to repository
retention/eviction. This first experiment does not implement a production cache
retention policy or promise that GitHub will retain a scope until consumption.
An evicted treatment cache fails its cache-hit proof.

`comparison.json` passes only when all eight samples and their required
qualifications pass and generated/source identities match. It always sets
`adoption_qualified: false`. Storage attribution, runner comparability, the
unchanged >=10% affected-boundary / <=5% other-cost limits and ten comparable
confirmations require explicit review. Whole-CI PR/merge p95 and rerun acceptance
remain separate under FAI-1067. A local preflight or successful harness test is
not a hosted measurement result.

Docker's [GHA cache documentation](https://docs.docker.com/build/cache/backends/gha/)
defines the scoped backend; [build history logs](https://docs.docker.com/reference/cli/docker/buildx/history/logs/)
provide the measured solve's execution and cache evidence.
