# Automation and CI

Treat validation, plan creation, build, publication, approval, activation, and verification as separate gates. Build one candidate from an exact source-attestation digest and publish that unchanged candidate only from an approved branch or environment. Git is a useful source of change evidence, not LeapView's source of truth: correctness, rollback, and target activation depend on content digests, retained candidates, plans, and deployment evidence.

## Provide bounded credentials

Use `LEAPVIEW_WORKLOAD_CLIENT_ID`, `LEAPVIEW_WORKLOAD_CLIENT_SECRET`, and `LEAPVIEW_WORKLOAD_PROJECT` for production CI. Inject the service-principal secret from the CI secret manager and prevent pull requests from untrusted forks from reading it. The CLI exchanges it on demand for a short-lived target credential. The validation job does not need target credentials. `LEAPVIEW_API_TOKEN` remains a compatibility option for smaller teams.

The automation principal needs `RESOURCE_USE`, `RESOURCE_READ`, `RESOURCE_EDIT`, and `RESOURCE_PUBLISH` for its exact project. It must not receive human login, approval, activation, secret-provider administration, connection-secret, or source-data credentials. Target-owned connection resolution and row-level policy evaluation still run under the automation principal. Use a deliberately restricted automation role; do not impersonate an end user to make a candidate pass.

Keep the target and project identity in reviewable pipeline configuration:

```sh
export LEAPVIEW_TARGET=https://dash.example.com
export LEAPVIEW_WORKLOAD_PROJECT=analytics
```

## Validate without network access

Compile the complete project first and retain structured diagnostics as a job artifact:

```sh
leapview validate --project dashboards/leapview.yaml --json
```

Stop the pipeline on any non-zero exit status. Do not allow a later deployment job to replace or edit the project after validation.

## Create and build the immutable delivery plan

Create a durable plan from the exact source snapshot, then build only that
plan:

```sh
PLAN_JSON=$(leapview plan dashboards/leapview.yaml --target "$LEAPVIEW_TARGET" --format json)
PLAN_ID=$(printf '%s' "$PLAN_JSON" | jq -r .planId)
BUILD_JSON=$(leapview build "$PLAN_ID" --format json)
CANDIDATE_ID=$(printf '%s' "$BUILD_JSON" | jq -r .candidateId)
```

The target retains the portable bytes and source-attestation digest before
physical work. `build` resolves target policy, leases, and credentials, and
returns a candidate only after its catalog is sealed. `dev` remains an optional
private watch/preview loop; it is not a second CI deployment path. For a local
candidate preview, use `leapview dev --once --project dashboards/leapview.yaml`.

## Publish an immutable deployment request

Run publication from a protected job using the same project path and target used by `dev`:

```sh
leapview publish "$CANDIDATE_ID"
```

`publish` does not read, compile, or upload the project again. It submits the
exact sealed candidate and plan provenance returned by `build`. An environment
configured for immediate publication activates it; a protected environment
returns the immutable publication and approval request without activating it.

Approve the exact persisted plan with a different principal holding `PROJECT_ADMIN`, then request cutover with a principal holding `PROJECT_ADMIN`. Immediately before cutover, LeapView rechecks the release, plan digest, approval revision, expiry, reviewer credential and grant, and activator credential and grant. Revocation or expiry closes the workflow safely.

## Reconcile change lifecycle

Map source-control events onto the candidate lifecycle rather than inventing a CI deployment model:

| Source event | LeapView action |
| --- | --- |
| Open or reopen | Validate locally, then create a target-owned `plan` in a trusted job |
| Update or synchronize | Create a new plan for the new exact source snapshot |
| Retry or missed webhook | Repeat the same operation and idempotency key |
| Superseded commit | Synchronize the new revision; never publish the older checkpoint |
| Merge to a protected branch | Build the reviewed plan, then run `publish CANDIDATE_ID` |
| Close or abandon | Let the target expire the private candidate; reconcile its status before retrying |

There is no candidate-cancellation operation in the canonical Delivery API.

Candidates expire on the target, so a missed close event does not leave an active runtime indefinitely. Scheduled reconciliation should inspect the candidate or publication status and must never publish a superseded candidate.

## Preserve evidence and verify

The `plan`, `build`, and `publish` output records the exact source-attestation
digest, plan, artifact, target, candidate, principal, and result. Retain those
logs with the source-control run. The Delivery APIs expose the same immutable
evidence after the runner disappears.

After activation, verify readiness and exercise a representative project query or dashboard with a separate verifier identity. A transport retry must reuse the same durable plan or candidate ID and its plan/seal digests; never rebuild from a moving branch between attempts.

The maintained GitHub Actions reference is [`/.github/examples/leapview-authoring.yml`](https://github.com/flidai/leapview/blob/main/.github/examples/leapview-authoring.yml). It keeps fork validation credential-free, gates trusted candidate creation, uses protected publication, and relies on target expiry for abandoned candidates. Adapt only the source-control event syntax; keep the LeapView commands and target policy unchanged.

See [Targets and environments](/docs/cli/targets) for environment safeguards and the generated [`validate`](/docs/cli/validate), [`dev`](/docs/cli/dev), and [`publish`](/docs/cli/publish) references for all flags.
