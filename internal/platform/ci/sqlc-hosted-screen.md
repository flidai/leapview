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
The caller caps producer tokens at write-only and consumers at read-only.
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
