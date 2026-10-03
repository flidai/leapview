# Release amd64 cache reads

Release amd64 builds can read the existing `production-amd64` GitHub layer cache
from the default branch after their existing `release-amd64` scope. ARM retains
its current release scope. The release writer, same-tag rerun behavior, pinned
compiler/platform context, revision stamping, SBOM/provenance, image admission,
attestation and qualification-before-promotion remain in place. This change adds
no publisher, warming workload or cache namespace.

The compatible producer is the existing main production-image job. The controlled
screen used main `c8f728eed2df1399d596d9184626c704c6aa5b22`, whose image and
historical transition passed [run 37094287650](https://github.com/flidai/leapview/actions/runs/37094287650).
Application inputs, Dockerfile, Go module files, Bun lock, pinned Go image and
amd64 platform were identical across all six candidate builds; workflow changes
are excluded from the Docker context. Every local candidate passed exact runtime
revision/version/dirty and OCI architecture/revision checks.

[Screening run 37096704297](https://github.com/flidai/leapview/actions/runs/37096704297)
used three independent baseline/main-read pairs. Median build execution fell from
566 to 259 seconds (54.24%). Median complete consumer runner time fell from 616 to
294 seconds. Conservatively allocating the entire 919-second existing producer
build across three treatment consumers gives 600.33 seconds per consumer, 2.54%
below baseline. Producer transfer/export and consumer import/load/teardown costs
are included in the job timings. Queue delay is reported separately. This clears
the initial 10% execution improvement / at most 5% runner regression threshold
under that explicit allocation; it is not a claim about release cadence or billing.

The baseline found no reusable layers; treatment logs show 77–78 cached steps.
Input, image and runtime receipts, job/step timings and producer allocation are
recorded in `measurements/release-layer-screening.json`. These are controlled local
candidate builds on hosted runners. They do not establish published-release
admission or identical image digests across independent builders.

Twelve later comparable baseline/treatment consumers per mode passed across
[runs 37098047199](https://github.com/flidai/leapview/actions/runs/37098047199),
[37098777888](https://github.com/flidai/leapview/actions/runs/37098777888),
[37099481025](https://github.com/flidai/leapview/actions/runs/37099481025) and
[37100477556](https://github.com/flidai/leapview/actions/runs/37100477556).
All 30 input and runtime receipts match; Docker client/server remained 28.0.4 and
BuildKit v0.33.1. Later median build execution was 560.5 → 270 seconds (51.83%
improvement); complete consumer runner time was 611 → 303.5 seconds. Applying
the same entire-producer / three-consumer allocation gives 609.83 seconds, 0.19%
below baseline. The final batch alone had a 5.07% allocated runner regression;
the 12-observation confirmation median meets the limit with little margin.
Per-batch and individual observations are in `measurements/release-layer-confirmations.json`.
The cost allocation does not establish release cadence or billing savings.

Protected qualification of the adoption commit is still pending. There are 15
observations per mode in total and no p95 conclusion. Required admission/publication assertions remain
independent of cache performance, and normal builds remain required on misses.
Persistent compiler mounts are evaluated separately with their full seed,
extraction/injection, compression, transfer and storage costs.
