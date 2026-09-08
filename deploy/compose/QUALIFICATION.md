# Installed-candidate qualification

This runbook qualifies the exact immutable image and Compose archive offered to
testers. It is deliberately independent of a source checkout or development
server. The bundled `leapviewctl qualify installed-candidate` command is the
executable form of the same journey.

| Gate | Automated step | Human check |
| --- | --- | --- |
| Anonymous distribution | Log out of GHCR, pull `image-reference.txt`, download the release archive without credentials, and verify both checksums and runtime identity. | Open the documented release links in a private browser session and confirm neither the image nor archive requires an account. |
| Initialization | Run real `./leapviewctl init`, `start`, `status`, and `first-login`; reject a second credential read. | Confirm the first-login warning is unavoidable and the output is understandable without repository context. |
| Enterprise authoring | In an unprivileged client container, run `leapview login`, approve its device challenge in a real browser, persist the credential in a native Secret Service keyring, stage the bundled source revision, run `leapview dev`, and verify governed candidate data. Publish that exact candidate to the protected target, approve it as a distinct human with a separately issued scoped credential, activate it, and prove the active candidate ID, revision, target, publisher, artifact digest, and release provenance match the previewed candidate without a rebuild. | Repeat login, private preview, publish, approval, and activation with the intended production author and reviewer identities. Confirm the candidate remains private before publish and that the approval history names distinct principals. |
| Five-minute sample | Stage the bundled synthetic data, deploy the bundled evaluation project, sign in, change the password, open **Five-minute Sales Evaluation**, select State `SP`, and verify KPI and governed table results. | Starting from the installation guide, time the same journey from the first pull through the filtered dashboard. Record the total without recording credentials. |
| Governed access | Execute a governed semantic query, verify an unauthenticated query is denied, then use the deliberately restricted bootstrap publisher token against the project grants endpoint and require the denial to appear as `authorization.denied` in the project audit stream. | Inspect the access/query audit surfaces for the successful and denied attempts and retain only IDs/timestamps. |
| Performance and resources | Against the exact installed digest, collect three restart-cold dashboard samples, five warm dashboard samples, eight filter interactions, six governed table-sort interactions, ten governed queries, three refresh runs, and an eight-reader concurrency wave. Enforce p95 latency, zero-error, CPU, RSS, temporary-disk, goroutine, and DuckDB-connection budgets from `qualification/performance-policy.json`. The first accepted run must explicitly use baseline-bootstrap mode; comparison mode must name a compatible accepted report. | Compare `performance-report.json` with the last accepted candidate. Investigate any material regression even when it remains under the absolute ceiling. A comparison is rejected when fixture, policy, toolchain, sample protocol, or runner identity differs; hardware is never treated as universally comparable. |
| Interruption recovery | At API-observed boundaries, send `SIGKILL` to the exact candidate during a resumable managed upload, release finalization, deployment activation, refresh/materialization claim, and active query/SSE traffic. Require each durable operation to resume or end in an explicit recoverable state; require the prior revision/generation to remain visible until atomic activation; then repeat query/SSE reconnects and verify bounded goroutines, temporary files, and disk growth. Provider-native PostgreSQL/DuckLake recovery is outside this qualification. | While following the same managed upload, deployment activation, refresh, and query/SSE sequence, confirm the UI and event history name the attempted, interrupted, resumed, failed, and completed states without exposing credentials. |
| Multi-node process | Start two independent application containers against the same native PostgreSQL control and DuckLake authority, verify their durable instance identity and active pointer, kill the primary with `SIGKILL`, recover it, then roll both nodes one at a time while the peer remains ready. | Confirm the report records two nodes, abrupt loss, recovery, rolling restart, and durable convergence. This local topology does not replace a managed-provider HA/failover drill. |
| Operations | Verify readiness, authenticated metrics, bounded structured logs, candidate identity, and restart persistence using the original separately managed secret configuration. Production backup/PITR and DuckLake/object-store recovery follow the [PostgreSQL operations guide](/docs/guides/operate/postgresql-operations) and [Backup and restore guide](/docs/guides/operate/backup-restore). | Inspect the running dashboard and confirm the active serving state and managed data are unchanged. |

## Run from an extracted release

Install Docker Engine with the Compose plugin, `curl`, `jq`, `openssl`, and
`sha256sum`. From the extracted archive:

```sh
QUALIFICATION_PERFORMANCE_MODE=bootstrap QUALIFICATION_PERFORMANCE_BASELINE= \
  ./leapviewctl qualify installed-candidate --multi-node-process
```

The controller uses only files in the archive plus public container registries. It
creates isolated Compose projects, stores credentials only in an owner-readable
temporary file, emits a bounded `qualification-evidence` directory, and removes
containers, volumes, and credentials on exit. Do not upload any other files
from the working directory.

This explicit bootstrap invocation checks absolute budgets only; it does not
provide relative qualification. For a compatible reviewed baseline, select
`compare` and set `QUALIFICATION_PERFORMANCE_BASELINE` as described below.

From a clean source checkout, build an attributable local development image and
run the same authoring journey with absolute performance checks. Do not label a
dirty source tree clean merely to pass qualification:

```sh
test -z "$(git status --porcelain)"
qualification_revision=$(git rev-parse HEAD)
qualification_build_time=$(git show -s --format=%cI HEAD)
docker build --build-arg BUILD_REVISION="$qualification_revision" \
  --build-arg BUILD_TIME="$qualification_build_time" --build-arg BUILD_DIRTY=false \
  --tag leapview:ci .
go build -tags=duckdb_arrow -o .tmp/leapviewctl-qualification ./cmd/leapviewctl
LEAPVIEWCTL_ROOT="$PWD/deploy/compose" QUALIFICATION_PERFORMANCE_MODE=bootstrap \
  QUALIFICATION_PERFORMANCE_BASELINE= \
  ./.tmp/leapviewctl-qualification qualify image --image leapview:ci
```

The controller pushes the local image through an isolated registry, deploys its
immutable digest with the production Compose bundle, and writes
`qualification-evidence/authoring-ci/authoring-report.json`. Both trusted and
fork pull-request production-image jobs run this gate. Full relative image
qualification instead uses `task image:qualify:performance IMAGE=ghcr.io/flidai/leapview@sha256:...`
against the reviewed reference; bootstrap success is not a substitute.

## Performance policy

The installed-candidate gate assumes a dedicated Docker runtime with at least
2 logical CPUs and 4 GiB memory. Its bundled Olist workload contains 24
synthetic orders. The absolute rc.1 ceilings are:

| Measurement | Budget |
| --- | ---: |
| Restart-cold dashboard readiness p95 | 15 s |
| Warm dashboard readiness p95 | 5 s |
| Filter-to-settle p95 | 5 s |
| Governed table-sort interaction p95 | 2 s |
| Governed query p95 | 1 s |
| Refresh/materialization p95 | 15 s |
| Eight-reader governed-query p95 | 5 s |
| Controlled-request error rate | 0 |
| Peak resident memory | 1.5 GiB |
| Measured workload CPU | 120 CPU-seconds |
| Temporary state growth | 64 MiB |
| Steady-state goroutine growth | 25 |
| Peak open DuckDB connections | 16 |

These are release gates, not claims that every host will produce identical
timings. Future candidates must still satisfy the absolute ceilings and also
fail comparison when a p95 is at least 50 ms slower and more than 25% above the
accepted baseline. The report records the runner CPU model, kernel, host and effective application limits, memory, architecture, runtime, dataset size, raw samples, p50, p95, maxima, policy, immutable image and runtime commit, fixture and policy digests, installed toolchain, harness digest (the exact browser and performance scripts), sample protocol, runner identity, baseline identity, and failures. Process resources are sampled at the eight workload checkpoints reported by `metricSamples`; RSS and open-connection maxima are observed checkpoints, not a continuous-process trace.
The report retains the raw eight warm metric snapshots and two snapshots for
each cold restart. Each phase must retain one process identity and a
non-decreasing CPU counter; Go recomputes CPU, RSS, goroutine, and connection
summaries from those snapshots before applying budgets.

Reliability `operations` is the fixed count of measured workload actions and must
match the policy; `requests` may be higher because refresh-status polling is also
recorded. Polling cannot hide an error: the policy's `errorRateMax` is zero, so
any controlled error or failure rejects the report.

The login and dashboard setup is preparation for the measured workload. Each warm
dashboard navigation is measured; no warm sample is silently discarded. Percentiles
use the nearest-rank rule implemented by the harness (`ceil((rank / 100) * n)`) and values
are rounded to milliseconds, so with the small release sample counts p95 is a
conservative observed sample rather than a variance estimate.

Set `QUALIFICATION_PERFORMANCE_MODE=bootstrap` for an explicit non-comparison
run that establishes an accepted baseline. Set
`QUALIFICATION_PERFORMANCE_MODE=compare` together with
`QUALIFICATION_PERFORMANCE_BASELINE=/path/to/accepted-performance-report.json`
for regression comparison. Comparison requires the same fixture and policy
digests, sample protocol, browser/Node/package/harness toolchain, and exact runner
identity (architecture, runtime, CPU model, kernel, logical CPUs, host memory,
effective application CPU/memory limits, and dataset); a report
from different hardware fails closed rather than being normalized into a false
universal comparison. Any other or unset `QUALIFICATION_PERFORMANCE_MODE` is
rejected. Latency and relevant resource metrics use the existing 1.25 relative
regression limit; absolute budgets remain mandatory in both modes.

The repository-owned MovieLens scale path remains the supplementary high-row
workload. From a source checkout, run `task dev:movielens`, then
`LEAPVIEW_PERF_ENFORCE_THRESHOLDS=true task qa:movielens-performance`; retain
`.tmp/movielens-performance.json` beside the installed Olist report for the
release decision. It measures warm interaction p50/p95, query counts,
supersession, table delivery, and browser/network correctness against the same
dashboard runtime, while the installed Olist gate owns shipped-artifact and
process-resource budgets.

The installed-candidate workflow runs this fresh-install journey, including
the multi-node process drill, independently on every release architecture.
Pass `--evidence-dir` to redirect the bounded report and failure screenshot.
The `multiNode` object in `qualification-report.json` records the process-drill
result without credentials or connection URLs.

## Evidence and timing

Retain only `qualification-report.json`, `authoring-report.json`, `performance-report.json`,
`recovery-report.json`, `recovery-events.json`, `runtime-identity.json`,
bounded redacted Compose logs, and the failure screenshot when present. Never retain
`initial-credentials.json`, `leapview.env`, browser storage state, cookies, or
API tokens. The five-minute budget applies to the sample evaluator journey;
the destructive interruption matrix is recorded separately because it
deliberately repeats restarts and recovery-boundary checks.

## Incident ownership

A scheduled or post-publication failure is a release/adoption incident. The
workflow creates or updates a GitHub issue assigned to the repository owner;
the release owner must post the affected digest, architecture, first failing
gate, and redacted evidence link to the active Linear release project before
closing the incident.
