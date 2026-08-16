# Automation and CI

Treat validation, candidate creation, publication, approval, activation, and verification as separate gates. Build one candidate from an immutable source revision and publish that unchanged candidate only from an approved branch or environment. Git is a useful source of change evidence, not LeapView's source of truth: correctness, rollback, and target activation depend on content digests, retained candidates, releases, and deployment evidence.

## Provide bounded credentials

Use `LEAPVIEW_WORKLOAD_CLIENT_ID`, `LEAPVIEW_WORKLOAD_CLIENT_SECRET`, and `LEAPVIEW_WORKLOAD_PROJECT` for production CI. Inject the service-principal secret from the CI secret manager and prevent pull requests from untrusted forks from reading it. The CLI exchanges it on demand for a short-lived target credential. The validation job does not need target credentials. `LEAPVIEW_API_TOKEN` remains a compatibility option for smaller teams.

The automation principal needs `AUTHOR_PROJECT`, `PUBLISH_RELEASE`, and `REQUEST_DEPLOYMENT` for its exact project. It must not receive human login, approval, activation, secret-provider administration, connection-secret, or source-data credentials. Target-owned connection resolution and row-level policy evaluation still run under the automation principal. Use a deliberately restricted automation role; do not impersonate an end user to make a candidate pass.

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

## Create the immutable target candidate

Synchronize the project to the exact target that will receive the deployment:

```sh
leapview dev --once --no-browser \
  --project dashboards/leapview.yaml \
  --target "$LEAPVIEW_TARGET" \
  --candidate-key "$CHANGE_KEY" \
  --source-revision "$SOURCE_REVISION" \
  --source-repository "$SOURCE_REPOSITORY" \
  --source-ref "$SOURCE_REF" \
  --source-change "$CHANGE_KEY"
```

`CHANGE_KEY` is a stable, vendor-neutral identity such as `github:pull/42`, `gitlab:merge-request/42`, or `branch:main`. It isolates concurrent candidates owned by the same service principal. A newer source revision with identical bytes advances the candidate provenance without changing its content digest. Repeating the same key, revision, and content is idempotent.

The target compiles the uploaded source snapshot, resolves target-owned connection evidence and managed-data pins, prepares an owner-isolated preview runtime, and retains immutable provenance for that exact candidate. `dev` stores only the non-secret candidate handoff locally; credentials remain in the workload-identity flow. Run `dev` again after any source change and complete review against the candidate preview before publication.

## Publish an immutable deployment request

Run publication from a protected job using the same project path and target used by `dev`:

```sh
leapview publish \
  --project dashboards/leapview.yaml \
  --target "$LEAPVIEW_TARGET" \
  --candidate-key "$CHANGE_KEY"
```

`publish` does not read, compile, or upload the project again. It submits the exact retained candidate revision and provenance produced by `dev`. An environment configured for immediate publication waits for activation to finish. A protected environment returns the immutable deployment and approval request without activating it.

Approve the exact persisted plan with a different principal holding `APPROVE_DEPLOYMENT`, then request cutover with a principal holding `ACTIVATE_DEPLOYMENT`. Immediately before cutover, LeapView rechecks the release, plan digest, approval revision, expiry, reviewer credential and grant, and activator credential and grant. Revocation or expiry closes the workflow safely.

## Reconcile change lifecycle

Map source-control events onto the candidate lifecycle rather than inventing a CI deployment model:

| Source event | LeapView action |
| --- | --- |
| Open or reopen | Validate locally, then `dev --once` in a trusted job |
| Update or synchronize | Repeat `dev --once` with the same candidate key and the new exact revision |
| Retry or missed webhook | Repeat the same operation and idempotency key |
| Superseded commit | Synchronize the new revision; never publish the older checkpoint |
| Merge to a protected branch | Create or refresh that branch's candidate, then run `publish` |
| Close or abandon | Cancel the active candidate by its stable key |

Cancellation uses the same generated Deployment API:

```sh
leapview api call cancelProjectCandidateByKey \
  --target "$LEAPVIEW_TARGET" \
  --path project="$LEAPVIEW_WORKLOAD_PROJECT" \
  --path candidateKey="$CHANGE_KEY" \
  --idempotency-key "candidate-close:$CHANGE_KEY"
```

Candidates also expire on the target, so a missed close event does not leave an active runtime indefinitely. Scheduled reconciliation may safely repeat close calls and ignore a not-found result for an already expired or cancelled key.

## Preserve evidence and verify

The `dev` and `publish` output records the exact source revision when present, artifact, target, candidate revision, principal, and result. Retain those logs with the source-control run. The Release and Deployment APIs expose the same source revision and digest-bound evidence after the runner disappears.

After activation, verify readiness and exercise a representative project query or dashboard with a separate verifier identity. A transport retry must reuse the same stable candidate key and immutable revision; never rebuild from a moving branch between attempts.

The maintained GitHub Actions reference is [`/.github/examples/leapview-authoring.yml`](https://github.com/flidai/leapview/blob/main/.github/examples/leapview-authoring.yml). It keeps fork validation credential-free, gates trusted candidate creation, uses protected publication, and reconciles closed pull requests. Adapt only the source-control event syntax; keep the LeapView commands and target policy unchanged.

See [Targets and environments](/docs/cli/targets) for environment safeguards and the generated [`validate`](/docs/cli/validate), [`dev`](/docs/cli/dev), and [`publish`](/docs/cli/publish) references for all flags.
