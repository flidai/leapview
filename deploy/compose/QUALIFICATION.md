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
| Performance and resources | Against the exact installed digest, collect three restart-cold dashboard samples, five warm dashboard samples, eight filter interactions, six governed table-sort interactions, ten governed queries, three refresh runs, and an eight-reader concurrency wave. Enforce p95 latency, zero-error, CPU, RSS, temporary-disk, goroutine, and DuckDB-connection budgets from `qualification/performance-policy.json`. | Compare `performance-report.json` with the last accepted candidate. Investigate any material regression even when it remains under the absolute ceiling. |
| Interruption recovery | At API-observed boundaries, send `SIGKILL` to the exact candidate during a resumable managed upload, release finalization, deployment activation, refresh/materialization claim, and active query/SSE traffic. Require each durable operation to resume or end in an explicit recoverable state; require the prior revision/generation to remain visible until atomic activation; then repeat query/SSE reconnects and verify bounded goroutines, temporary files, and disk growth. Provider-native PostgreSQL/DuckLake recovery is outside this qualification. | While following the same managed upload, deployment activation, refresh, and query/SSE sequence, confirm the UI and event history name the attempted, interrupted, resumed, failed, and completed states without exposing credentials. |
| Multi-node process | Start two independent application containers against the same native PostgreSQL control and DuckLake authority, verify their durable instance identity and active pointer, kill the primary with `SIGKILL`, recover it, then roll both nodes one at a time while the peer remains ready. | Confirm the report records two nodes, abrupt loss, recovery, rolling restart, and durable convergence. This local topology does not replace a managed-provider HA/failover drill. |
| Operations | Verify readiness, authenticated metrics, bounded structured logs, candidate identity, and restart persistence using the original separately managed secret configuration. Production backup/PITR and DuckLake/object-store recovery follow the [PostgreSQL operations guide](/docs/guides/operate/postgresql-operations) and [Backup and restore guide](/docs/guides/operate/backup-restore). | Inspect the running dashboard and confirm the active serving state and managed data are unchanged. |

## Run from an extracted release

Install Docker Engine with the Compose plugin, `curl`, `jq`, `openssl`, and
`sha256sum`. From the extracted archive:

```sh
./leapviewctl qualify installed-candidate --multi-node-process
```

The controller uses only files in the archive plus public container registries. It
creates isolated Compose projects, stores credentials only in an owner-readable
temporary file, emits a bounded `qualification-evidence` directory, and removes
containers, volumes, and credentials on exit. Do not upload any other files
from the working directory.

From a source checkout, CI and local image qualification run the same authoring
journey against the already-built production image:

```sh
go build -o .tmp/leapviewctl-qualification ./cmd/leapviewctl
LEAPVIEWCTL_ROOT="$PWD/deploy/compose" \
  ./.tmp/leapviewctl-qualification qualify image --image leapview:ci
```

For local tag inputs, the controller pins the image through an isolated registry
before deployment. Already-immutable registry references pass unchanged through
bootstrap, authoring and performance qualification, without retagging or
republishing. The report records the selected deployment image. The controller writes
`qualification-evidence/authoring-ci/authoring-report.json`. Both trusted and
fork pull-request production-image jobs run this gate.

## Qualification read credentials

For an isolated managed lifecycle rehearsal, `qualify first-publication` accepts
`--preloaded-client-image` and `--preloaded-browser-image` together. Both must be
exact local `sha256:` image IDs, already loaded into the selected Docker daemon.
Build the helpers from `qualification/Dockerfile.authoring-client` (with the
exact release `LEAPVIEW_IMAGE`) and `qualification/Dockerfile.authoring-browser`
before network isolation, using `qualification/` as their build context. The
browser helper installs the checked-in npm lock into `/work/node_modules`.
The controller verifies both local identities and uses them without helper
pulls, builds, or runtime package installation. The caller owns their cleanup.
The fixed fresh-host profile and release-only first-publication checks still
apply.

The optional `--lifecycle-credential-file` exports a project-scoped token only
after committed first publication. Its JSON contains `projectID`, `environment`,
`targetURL`, `issuedAt`, `expiresAt`, `actions`, `uploadConnectionID`, and the private `token`. It lasts
two hours and grants exactly `connection.read`, `connection.use`,
`connection.upload`, `source.read`, `dashboard.read`, `semantic.read`, `semantic.query`, and
`semantic.consume`. Select a new absolute path in an existing mode-0700 parent,
outside the evidence directory; the controller writes it with mode 0400 without
overwriting a file and removes it if qualification fails. The public report
contains only the scope and expiry. Upload authority is restricted to
`connection:sample`; the optional lifecycle path stages that exact grant before
protected publication. This is a retained workload credential:
the lifecycle runner must delete it on completion and must never upload it.
No administrator password or browser session is exported.

The installed journey issues separate, short-lived project credentials for its
evidence and upload calls. `delivery.read` is used only for the candidate and
generation status reads before and after application upgrade. `connection.read`
is used for active-revision and upload-session list/status/event reads. A
recovery-only credential carries exactly `connection.read` and
`connection.upload` on `project:leapview-evaluation` and `connection:sample`; it
is used only by the recovery `leapview data sync` command, including its
upload-session mutations. Before publication, qualification stages and reads
back that exact owner grant. The approved authorization-policy digest includes
both it and the pipeline-run grant. No API endpoint policy or default role is
changed, and publisher/workload credentials are not reused for upload evidence.

Both native jobs in [manual release workflow run 37196382702](https://github.com/flidai/leapview/actions/runs/37196382702)
previously failed in application-upgrade qualification when the candidate-status
request used a token without `delivery.read` and received HTTP 403. The same
incorrect data credential would also fail recovery's active-revision request,
which requires `connection.read`. The focused regressions reproduce both denied
requests and verify the two dedicated bearer headers:

```sh
go test ./internal/app/cli/composectl -run 'TestQualification(DeliveryEvidence|ActiveRevision)' -count=1
```

## Performance policy

Every latency phase must include a finite, nonnegative, ordered summary
(`p50 <= p95 <= max`) with the exact sample count specified by the policy.
Missing phases or invalid summaries fail qualification before they can be
used in a baseline comparison. An absolute-budget run without a supplied
baseline leaves `assertions.comparisonTolerance` false; its success is not
evidence that a regression comparison ran.

Resource evidence uses `resources.schemaVersion: 1`. Unversioned aggregate-only
reports and unknown resource protocols fail; they are not converted into complete
evidence. The outer report and performance policy retain their existing version.
Resource evidence must retain all eight warm-process metric snapshots and a
before/after pair for every restart-cold load. CPU, resident memory, goroutines
and connection measurements must be present, finite and nonnegative; resident
memory and goroutines must be positive. Observed zero CPU and connections are
valid. CPU counters must not decrease within a process; each deliberately
restarted cold process has its own pair. Summaries must agree with these
measurements: peak RSS spans cold and warm loads, CPU is the sum of process
deltas, and steady-state goroutines and peak connections use the warm process.
The Go controller supplies disk growth independently. Missing or inconsistent
candidate or supplied baseline resource evidence fails qualification; this
validation does not establish independent acceptance of a baseline.

A supplied baseline must be a successfully finalized report with passing
environment, absolute-budget and error-free assertions and no recorded failures.
A successful absolute-only baseline need not have a passing comparison assertion;
requiring that would make the first comparison circular. These outcome checks do
not establish the baseline's independent acceptance or provenance.

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
accepted baseline. The report records the runner CPU, memory, architecture,
runtime, dataset size, raw samples, p50, p95, maxima, policy, and failures.

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

The protected Nix Compose fresh-guest workflow retains
`host-install-exit-code.txt` and `host-install-diagnostic.json` when the installer
returns, including failures. The diagnostic classifies known installer
boundaries (configuration, pool dry-run, initialization, pool apply, and private
startup) and causes using a fixed vocabulary. These are hints from the final
64 KiB of output, not proof of a root cause; unknown errors remain
`unclassified`. The collector never uploads raw installer output, which can
contain generated credentials, bootstrap responses, and private URLs. Guest
timeouts are reported before the SSH deadline, and the private temporary log
is removed on exit. Launcher lifecycle receipts separately record guest and
temporary-directory cleanup.

A scheduled or post-publication failure is a release/adoption incident. The
workflow creates or updates a GitHub issue assigned to the repository owner;
the release owner must post the affected digest, architecture, first failing
gate, and redacted evidence link to the active Linear release project before
closing the incident.
