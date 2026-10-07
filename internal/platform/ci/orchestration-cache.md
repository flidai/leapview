# Orchestration toolchain archive

The planner and PR gate restore only the locked Linux x64 orchestration closure.
Validation jobs still use the complete development shell; application artifacts,
workspaces, runtime state, credentials and test evidence are excluded from this
archive. Cache success never replaces `nix develop` or environment export.

## Publication, imports and retention

`orchestration-cache.yml` publishes on default-branch changes to its complete key
inputs, or a manual `operation=produce` dispatch. Both require the default branch,
no alternate source, and checkout HEAD equal to the trusted event SHA before
execution and publication. A read-only lookup skips an already-published exact
identity. Push and manual producers share a concurrency group. The publisher has
`cache-mode: write-only`; other jobs and the PR planner/gate have `read` capability.

The key is `nix-orchestration-v1-Linux-X64-2.31.2-<input digest>`. Producer and setup
use the same digest of the flake, lock, every Nix recipe, compiler manifests,
exporter/importer, producer workflow, setup action and PR workflow. There are no
prefix restore keys. A changed source checkout can consume a matching trusted
archive but cannot publish one. This namespace replaces the measurement namespace;
old archives are not compatibility inputs.

The producer explicitly builds the orchestration shell output before inventory
and export. `nix develop` alone realizes an environment derivation and leaves that
output absent on a fresh store. This additional build is producer work and is
included in the measurements. Consumers use ordinary environment realization.

Import requires the exact evaluated shell root, profile, platform, Nix version,
input digest, unique toolchain store paths, archive size and SHA-256. Imported
requisites must match the manifest. Misses, cache-service errors and rejected
imports still execute complete locked realization. Normal toolchain failures
remain failures. Only the two archive/manifest files are cached; measurements and
retention receipts live outside the archive.

The archive envelope is limited to 300 MB, including manifest and 1 MiB transfer
allowance. Before a new publication, the trusted producer retires the oldest
identities in this exact default-branch namespace until current plus previous
identities fit three entries and 900 MB. It preserves the current identity and
never selects other refs or namespaces for deletion. `actions: write` is granted
only to this publisher for the narrowly scoped retention operation. Retired IDs,
bytes and inventory are recorded as artifacts. An evicted older identity becomes
an ordinary exact-key miss. GitHub's own cache eviction still applies; a manual
trusted producer dispatch can restore a missing identity without a source change.

## Hosted measurements and retained decision

The initial guarded harness merged as [#814](https://github.com/flidai/leapview/pull/814).
Its first trusted producer [37103488392](https://github.com/flidai/leapview/actions/runs/37103488392)
failed before publication on the missing shell-root condition; it is excluded
from timing statistics. [#819](https://github.com/flidai/leapview/pull/819) fixed that
condition and passed exact protected qualification.

The successful trusted producer [37109125037](https://github.com/flidai/leapview/actions/runs/37109125037)
and all consumers used source `c5aea6cc2768cffa504fc3000a3d235a098da1af`, Ubuntu 24.04
x64 and Nix 2.31.2. The closure had 124 paths / 657,007,184 NAR bytes; level-6 gzip
produced a 225,132,749-byte archive. GitHub reported 224,181,749 stored bytes.
The entire producer operation cost 203 runner seconds, including authority
resolution, realization/export, serialization, compression, cache upload,
artifact transfer and teardown.

Three screening pairs in [37109381741](https://github.com/flidai/leapview/actions/runs/37109381741)
reduced median affected setup from 152 to 65 seconds (57.24%). Full consumer cost,
including its share of the common resolver, fell from 160.17 to 74.17 seconds.
Allocating the entire producer over only three consumers gives 141.83 seconds,
11.45% below baseline.

Twelve later pairs in [37110083604](https://github.com/flidai/leapview/actions/runs/37110083604),
[37110085578](https://github.com/flidai/leapview/actions/runs/37110085578),
[37110087704](https://github.com/flidai/leapview/actions/runs/37110087704) and
[37110089626](https://github.com/flidai/leapview/actions/runs/37110089626) confirmed
178 → 66 seconds affected execution (62.92% improvement). Full consumer cost was
186.17 → 73.67 seconds; the same conservative entire-producer / three-consumer
allocation yields 141.33 seconds (24.08% improvement). All treatments were exact
cache hits with successful imports; source/input/platform/work and attempts
matched. The four dispatch refs contain the same commit and allow independent
batches without cancelling pending same-ref runs. They add no code or PRs.

`measurements/orchestration-cache-reuse.json` retains individual observations,
producer costs, allocations, source/attempt identities and separate initial queue
delays. There are 15 observations per mode, so no p95 conclusion. Cold, unavailable
and changed-input correctness samples are excluded from warm timing statistics.
These results clear the 10% improvement / at most 5% other-metric regression
threshold; retain the archive for orchestration only.

This measures affected setup, not whole-CI critical-path or billing savings.
Production lookup/retention add control work, and the expanded production key
starts cold. The adoption requires its own protected candidate checks, then a
trusted main producer and warm rollout verification. The complete Nix development
suite, distinct security scans, forced-fresh service tests, native proofs,
recovery/transition assertions and qualification before promotion stay required.

Manual `operation=measure` keeps three independent baseline/restore pairs.
`cache_service=unavailable` bypasses restore and still requires locked realization.
`source_revision=<exact SHA>` is a restore-only alternate-source check. Neither
measurement path publishes application/release artifacts or modifies live
infrastructure. The experiment is tracked in the
[Linear project](https://linear.app/flid/project/leapview-cicd-quality-and-speed-8a7e28e7113f/overview).
