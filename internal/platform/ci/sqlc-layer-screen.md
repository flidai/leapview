# SQLC executable-layer screen

FAI-1149 screens whether an ordinary, immutable BuildKit layer containing the
pinned SQLC executable merits a full image-build experiment. Production Docker,
Nix and shared generation commands are unchanged. This is not adoption of the
previously rejected persistent compiler-cache experiment.

Merged timing diagnostics in #897 identify SQLC as a material source-generation
cost. Two Docker observations recorded 86 and 65 seconds; a separate Nix build
recorded 143 seconds. These differ in source and execution conditions and are
not matched comparisons. JSON Schema generation also dominates, but it shares
the CLI dependency graph with subsequent documentation generation; moving that
command alone could shift compilation cost rather than remove it.

## Experiment boundary

The baseline executes the current versioned `go run` command. The treatment
builds SQLC v1.31.1 with Go 1.26.7 in a stable tool stage, then copies the binary
into the generation stage. Both retain the pinned production Go base image,
HTTP/1.1 workaround, checksum verification, `generate --no-remote`, module seed,
platform and complete source context. The executable is outside cache mounts;
compiler and module cache mounts are not exported as reusable content.

Each mode has its own cold producer and local `mode=max` cache export, followed
by three paired consumer measurements. Each consumer starts another fresh
Docker-container builder and imports
only its corresponding producer cache. A harmless source-context marker changes
between producer and consumer, invalidating generation while allowing the
treatment's tool layer to be reused. Pair order alternates. The harness removes
only builders it creates and never prunes shared Docker state.

Every generated Go filename and digest must match across modes and repetitions.
The receipt retains source and image identities, commands, logs, generated
manifests, phase timings, builder setup/teardown, total build time including
local cache transfer, and exported cache bytes. A failed build or mismatched
manifest invalidates the screen; it must not be omitted from the evidence.

## Running the screen

Run from the repository in the development shell, with Docker available:

```sh
python3 scripts/ci_sqlc_layer_screen.py \
  --source "$(git rev-parse HEAD)" \
  --output /tmp/leapview-sqlc-screen \
  --builder-image moby/buildkit:v0.33.0@sha256:6c2fa84a6b61ccd72899dde4239f8d5717f05f9a8ca6f3cad185fb1a95a94de3 \
  --pairs 3 --query-mutation
```

The output directory must be new and outside the checkout. The exact committed
source is archived; working-tree edits and ignored generated files are excluded.
The additional query-rename probe checks that changed SQL input changes outputs
equally in both modes. It is correctness evidence outside the timing cohort.

The harness itself is tested without Docker:

```sh
python3 -m unittest discover -s scripts/tests -p test_ci_sqlc_layer_screen.py
```

## Interpretation

This boundary includes SQLC generation, its prerequisite layers and measurement
export. It excludes the other generators, frontend build, application build,
runtime image qualification and hosted cache-service transfer. Shared VPS load
is an additional confounder. Local results can establish correctness and screen
feasibility, but cannot establish hosted runner savings or whole-CI SLO gains.

Report producer and consumer costs separately. Any amortization must state how
many consumers share a producer and include export/import and storage costs.
Do not compare a cold baseline with a warm same-source treatment whose complete
generation step was cached.

The existing adoption rule remains: three matched screening pairs showing at
least 10% improvement in the declared affected boundary and no more than 5%
regression in the other cost metric, followed by ten comparable confirmations.
A positive local screen requires a full, representative hosted image experiment
before production adoption. A negative or inconclusive screen is a valid result;
retain it without weakening correctness, timeouts or the 12-minute CI SLOs.

## Local result — 7 October 2026

The completed screen used source `1bc97690e851447cb97cfbc3764fafd69458378a`
(merged #897), the pinned builder above, and a shared Linux amd64 VPS. Canonical
CI ran concurrently for part of the experiment. The compact
[measurement receipt](measurements/sqlc-layer-local-screen.json) retains each
sample, identities and raw-log hashes. This is feasibility evidence only.

All 111 generated files in 29 directories matched across the two producers and
six consumers. Both changed-query probes produced identical output to each
other and different output from the normal cohort. All four treatment consumers
(three timing pairs and the probe) proved that the tool-install vertex was cached
while SQLC itself executed successfully.

| Observed cost | Baseline | Executable layer |
| --- | ---: | ---: |
| Cold producer build, including export | 266.6s | 266.3s |
| Cold producer, including builder lifecycle | 290.3s | 285.0s |
| Consumer SQLC phases, pairs 1–3 | 103 / 88 / 94s | 1 / 1 / 0s |
| Median consumer build, including local import/output | 153.2s | 71.0s |
| Median consumer, including builder lifecycle | 163.3s | 77.7s |
| Exported cache bytes | 966,424,237 | 1,025,603,085 |
| Median consumer plus one third of producer lifecycle | 260.1s | 172.7s |

SQLC phase timing has one-second resolution; zero means below that resolution.
The last row assumes exactly three consumers per producer; it is an illustrative
amortization, not another observation. Build time fell 53.7% at this local
consumer boundary, while exported storage grew 6.1% (59,178,848 bytes). This does
not satisfy a claim of no more than 5% regression across every cost dimension.
Hosted transfer, billing and full-image producer/consumer costs remain unknown.

The next experiment must measure representative hosted full-image pairs and
explicitly account for that storage increase before any adoption decision. The
three local pairs do not replace hosted screening or the ten confirmations.
Production build commands and SLO targets remain unchanged.

The first attempt stopped because the receipt collector rejected a repeated
BuildKit progress header for the same cached vertex. Its failed receipt is
retained separately and excluded from this completed cohort. A red/green
regression now accepts repeated observations of one vertex while still rejecting
distinct install vertices; the corrected harness started a fresh experiment.

All nine focused harness tests passed, including inside `task ci`. The canonical
run passed generation, Go/application, PostgreSQL conformance, quality and
frontend core, then failed the unchanged dashboard browser suite: its first
failure exceeded the existing five-second deadline while waiting for initial
table rows, before the `showHeader` assertion. The original case passed once in
isolation with the same locked tools and timeout (932ms); that is non-reproduction,
not a passing full run or an established cause. The failed full-run receipt is
retained on FAI-1128; no browser assertion, timeout or retry policy changed.
