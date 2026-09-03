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
| Operations | Verify readiness, authenticated metrics, bounded structured logs, candidate identity, and restart persistence using the original separately managed secret configuration. Production backup/PITR and DuckLake/object-store recovery follow the [PostgreSQL operations guide](/docs/guides/operate/postgresql-operations) and [Backup and restore guide](/docs/guides/operate/backup-restore). | Inspect the running dashboard and confirm the active serving state and managed data are unchanged. |
| Fresh-install migration | Require the candidate to reject a released v0.1.0 `libredash.db` marker before creating `leapview.db`; v0.1.0 is explicitly fresh-install-only. | Confirm the v0.1.0 export/reprovision runbook is clear before retiring its preserved state. |

## Run from an extracted release

Install Docker Engine with the Compose plugin, `curl`, `jq`, `openssl`, and
`sha256sum`. From the extracted archive:

```sh
./leapviewctl qualify installed-candidate
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

The controller pushes the local image through an isolated registry, deploys its
immutable digest with the production Compose bundle, and writes
`qualification-evidence/authoring-ci/authoring-report.json`. Both trusted and
fork pull-request production-image jobs run this gate.

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

The installed-candidate workflow runs this fresh-install journey independently
on every release architecture. The separate v0.1 preservation gate below
validates the historical artifact and the candidate's rejection of preserved
state before publication. The released v0.1.0 image
`ghcr.io/yacobolo/libredash@sha256:677caaf256cb3a0d61efd47b289debbd91984976a5a5c4b372196a5d79ce7153`
is fresh-install-only because it uses `libredash.db`, a different container and
configuration namespace, and an incompatible backup manifest. Preserve it with
the v0.1 release's own documented export procedure, then provision a fresh
LeapView instance and redeploy authored projects. Pass `--evidence-dir` to
redirect the bounded report and failure screenshot.

## v0.1 preservation release gate

The release workflow runs the v0.1 preservation gate in the pre-publication
qualification job after the candidate image has been assembled and admitted,
the candidate-bound transition policy has been generated, and the release
archive has passed its checksum verification. The historical v0.1 artifact has
only a `linux/amd64` runtime, so this gate runs on the `amd64` qualification
runner. The regular installed-candidate journey still runs independently on
every release architecture.

### Release-runner requirements

The runner must provide all of the following:

- a `linux/amd64` host capable of executing `linux/amd64` containers;
- Docker Engine with Buildx and permission to pull images, inspect OCI index
  and manifest bytes, and create and remove isolated containers, networks,
  volumes, and run directories;
- the extracted candidate's `leapviewctl`, `release-transition-policy.json`,
  and the exact `assembled-image-admission.json` produced by candidate image
  admission; and
- authenticated pull access to the policy-declared historical artifact in
  `ghcr.io/yacobolo/libredash`.

Configure GHCR credentials with `docker login ghcr.io` before qualification.
The resolver reads `config.json` from `DOCKER_CONFIG` when that variable is set,
or from `$HOME/.docker/config.json` otherwise. An auth entry, configured GHCR
credential helper, identity token, or credential store must be present. The
credential needs read access to the historical package only; do not grant
write, delete, or package-administration permission. In GitHub Actions the
qualification job has `packages: read`, and the token used by the login step
must also have been granted pull access to the cross-namespace historical
package. A syntactically configured credential that lacks that package access
still fails when the exact OCI object is resolved.

Do not copy registry credentials into the release archive, command arguments,
qualification directory, or uploaded evidence. The qualification evidence
contains normalized identities and checksums, not tokens, Docker configuration,
raw authenticated responses, or application credentials.

### Invocation and publication boundary

The release runner derives the policy checksum from the verified archive and
uses one evidence directory for the existing pre-publication artifact. The
equivalent controlled invocation is:

```sh
docker login ghcr.io
evidence_dir="$RUNNER_TEMP/installed-candidate-evidence"
policy="$PACKAGE_ROOT/release-transition-policy.json"
policy_sha256="$(sha256sum "$policy" | awk '{print $1}')"
predecessor_evidence="$evidence_dir/v0.1-reviewed-identity.json"

./leapviewctl qualify v0.1-artifact-review \
  --transition-policy "$policy" \
  --policy-sha256 "$policy_sha256" \
  --evidence "$predecessor_evidence"

./leapviewctl qualify v0.1-preservation \
  --candidate-admission "$GITHUB_WORKSPACE/candidate/assembled-image-admission.json" \
  --transition-policy "$policy" \
  --policy-sha256 "$policy_sha256" \
  --predecessor-evidence "$predecessor_evidence" \
  --evidence-dir "$evidence_dir"
```

Do not replace either input with a locally built image, mutable tag, policy
from another archive, or reconstructed admission record. The artifact-review
command removes stale output before it starts and publishes evidence atomically
only after owner validation. The preservation command independently resolves
the exact historical artifact again and rejects a different artifact,
provenance record, or policy identity.

Any nonzero command result or missing final evidence file fails the `amd64`
qualification job. Release publication depends on the complete qualification
matrix, so the image and release assets cannot be published after this gate
fails. The evidence is produced and checked before the workflow can report
qualification success.

### Evidence chain

The existing GitHub Actions artifact
`prepublication-<release-tag>-amd64` retains the v0.1 evidence for 14 days:

| File | Meaning |
| --- | --- |
| `v0.1-reviewed-identity.json` | The Phase 1 owner-validated review of the authenticated, exact policy-declared v0.1 OCI index, `linux/amd64` manifest, config digest, source revision, and provenance. It is bound to the candidate policy SHA-256 and intentionally has no execution section. |
| `v0.1-preservation-qualification.json` | The same evidence contract extended with the observed container identity, authentic bootstrap/workload journey, before/after stopped-state inventory and checksums, clean restart and shutdown, isolated fresh-candidate inventory, policy denials, mutation-free checksums, and cleanup proof. This file exists only after the complete journey passes owner validation. |

The second document does not merely refer to the first by filename. Before
success, the controller requires their historical identity, OCI artifact,
provenance, policy version, and policy SHA-256 to match exactly. It also binds
the candidate identity to the assembled-image admission output. Preserve both
documents together when reviewing a release decision.

Success proves that the exact historical application executed, deterministic
application state survived a clean stop and restart, the admitted candidate
started with isolated clean state, and unsupported legacy-state adoption was
denied before mutation. It does not declare an in-place v0.1 migration path or
authorize restoring a v0.1 archive into LeapView.

### Diagnose failures

Open the GitHub Actions **Pre-publication qualification (amd64)** job, locate
the first failing v0.1 step, and download its
`prepublication-<release-tag>-amd64` artifact when present. The upload step runs
after failures so reviewed identity evidence and bounded logs may be available;
absence of `v0.1-preservation-qualification.json` means the complete journey
did not pass. Never infer success from the review document alone.

| Failure | Required response |
| --- | --- |
| Credentials unavailable or not configured | Configure an owner-readable Docker credential file through `docker login ghcr.io` or the approved credential helper. Verify the token can read the historical package. Do not make the package public, copy a token into evidence, or enable a local-image fallback. |
| Historical artifact unavailable | Confirm access to the exact immutable reference declared by policy. Escalate removal or registry unavailability to the release owner; do not substitute a tag, registry namespace, rebuilt image, or different digest. |
| OCI or image digest mismatch | Stop the release and retain the registry diagnostic. The index bytes, platform manifest, config, or pulled image no longer matches the reviewed immutable graph. Do not regenerate policy or evidence to accept the observation. |
| Candidate policy digest mismatch | Re-extract the checksummed candidate archive, recompute `sha256sum release-transition-policy.json`, and pass that exact value and file to both commands. Do not edit or reserialize the policy. |
| Reviewed predecessor evidence rejected | Regenerate `v0.1-reviewed-identity.json` with the same shipped controller, exact policy file, and policy checksum used by the preservation command. Do not hand-edit, reuse evidence from another candidate, or copy only its identity fields. |
| Candidate admission rejected | Use the original `assembled-image-admission.json` from the candidate artifact. A different image digest, source revision, workflow attestation, SBOM result, or vulnerability-policy result requires rebuilding and readmitting the candidate. |
| Preservation or fresh-candidate journey failed | Inspect the bounded step diagnostic to identify readiness, bootstrap, authentication, workload, inventory, restart, denial, or cleanup failure. Fix and readmit the candidate or restore the historical artifact service before rerunning; do not publish partial evidence. |

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
