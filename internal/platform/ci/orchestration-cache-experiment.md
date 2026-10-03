# Orchestration cache measurement

The initial rollout is merged: [#805](https://github.com/flidai/leapview/pull/805),
[#807](https://github.com/flidai/leapview/pull/807), and
[#808](https://github.com/flidai/leapview/pull/808). The last two exact candidates,
`e248e85379f6df3df047c3af3187e3890338a94d` and
`6e6751c2123b12e80179a814e439a46f56877b7f`, passed protected full CI, Security gate,
all four native proofs, and mandatory recovery/transition qualification.
The [Linear project](https://linear.app/flid/project/leapview-cicd-quality-and-speed-8a7e28e7113f/overview)
retains the independent experiment decisions and integrated closeout.

`orchestration-cache-experiment.yml` is an opt-in measurement workflow for
FAI-1050. Normal CI setup and the complete Nix development suite remain unchanged.
It neither publishes releases/images nor runs application/deployment code.

## Producer and consumers

Dispatch `operation=produce` on the default branch with no alternate source.
The resolver requires checkout HEAD to equal the trusted event SHA before the
producer can execute; the producer checks that identity again before publication.
It realizes only the orchestration shell, then serializes its toolchain closure.
The producer explicitly builds the shell output before inventory/export:
`nix develop` alone realizes an environment derivation and can leave that output
absent on a fresh store. This build is included in producer realization costs.
Application sources, workspaces, runtime state, credentials and test evidence
are excluded from the archive. Metrics live outside the cached directory.

Dispatch `operation=measure` after the producer succeeds. Three pairs run on
independent Ubuntu 24.04 x64 runners with identical source/toolchain inputs:
ordinary locked realization versus exact archive restore/import followed by the
same complete environment export. Candidates and alternate-source SHA dispatches
restore only. There are no prefix restore keys. Changes to flake/Nix files,
compiler manifests, exporter, measurement script or workflow create a new key.
Normal realization remains required after cache misses or rejected imports.

The workflow grants `cache-mode: read`; only the verified producer overrides it
with `write-only`. GitHub enforces these capabilities in scoped cache tokens,
including alternate-source dispatches. See the
[cache access syntax](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#cache-mode).

Keys include platform, profile, Nix version and the complete input digest. The
archive has a 300 MB cap. The experimental namespace admits at most three input
identities and 900 MB; publication stops when that budget is exhausted. Existing
production caches are never deleted by this workflow. GitHub's cache lifecycle
still applies; accessing an archive is not a producer refresh policy. This opt-in
experiment does not define automatic production warming or validation caching.

Use `cache_service=unavailable` to bypass GitHub restore while still requiring
the full locked environment. A changed-input alternate-source dispatch exercises
an exact-key miss without publishing a replacement. These runs verify correctness
under absence; they do not count as successful warm-cache performance samples.

## Measurements and decision

Producer artifacts record closure/NAR/archive bytes and realization,
serialization and compression times. Consumer artifacts record evaluation,
import and complete realization/export times, source/input/run/attempt identity,
cache hits and rejected imports. Retrieve the run-attempt jobs API as well:
include Nix installation, cache restore/upload and setup/teardown costs.
Report initial workflow queue delay separately; job start offsets include
dependency and allocation waits and are not pure queue measurements.

Reject incomplete pairs, import errors, cold treatment misses, changed selected
work, or mismatched inputs/platforms. Compare median affected execution time and
total runner usage across at least three valid pairs. Charge the entire producer
job and its transfer costs to the measured consumers (three consumers in the
initial screening), and record storage separately. Adoption needs at least 10%
improvement in one metric and at most 5% regression in the other. Confirm a
retained change over at least ten later comparable runs. No p95 conclusion below
twenty observations. Failure to meet the threshold retains current setup.

A shared warm-store local check serialized 124 paths / 657,007,400 NAR bytes into
a 225,132,778-byte gzip archive at level 6, verified its identity, re-imported it,
and completed environment export. A changed input was rejected while normal
locked realization succeeded. This verifies the mechanism locally; it excludes
hosted transfer, cold import and producer amortization and establishes no speedup.
Hosted paired measurements and an adoption/rejection decision remain pending.

The first trusted hosted producer,
[37103488392](https://github.com/flidai/leapview/actions/runs/37103488392), failed
before publication because the shell output had not been realized. It is excluded
from performance samples. The producer now explicitly realizes that root; the
fresh-store regression test requires realization before inventory while a cold
consumer still executes only the normal environment contract. The earlier local
check used a warm store and therefore did not establish this producer condition.
