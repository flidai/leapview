# Hosted product demo

`https://demo.leapview.dev` is the hosted LeapView demonstration environment.
The `leapview-demo` GitHub environment variable `DEMO_DATASET` selects its
project: `cfo` publishes the CFO Command Center, while `olist` (the default)
preserves the existing Olist showcase. CFO publication requires a qualified,
already-deployed runtime revision containing the CFO-aware publisher and
`deploy/demo/datasets.txt` declaring `cfo`. The workflow checks this declaration
in the pinned checkout before fetching credentials; older Olist-only pins are
rejected for CFO publication. Merging this workflow alone does not upgrade the
pinned runtime or its publisher. Other values are rejected before any
credential exchange or publication.

The CFO project lives in `dashboards/experiments/cfo-demo/`. Its four pages are
Executive Overview, P&L and Budget, Cash and Working Capital, and Profitability
Drivers. `bootstrapfinance` verifies the pinned Microsoft Financial Sample
workbook and prepares its managed CSV. Budget, forecast and cash extensions are
fictional demo scenarios; see the [CFO source README](../../dashboards/experiments/cfo-demo/README.md).

This directory contains only the publication contract, never secret values.

## Delivery

Merging a PR does not change the public demo. Both operations in **Hosted demo
deployment** are manual and run on `main`, using one shared concurrency group:

- **deploy** replaces the application image on `app-leapview-demo-02` at
  `89.58.13.145`. Supply `image` as `ghcr.io/flidai/leapview@sha256:<digest>` and
  `qualification_run` as the successful **Main artifacts** run ID for that exact
  digest. The run must contain `production-image-qualification-<attempt>`; older
  runs without a receipt are not admissible. First qualify an image with the
  updated workflow. Mutable tags, PR candidates, foreign workflows, stale run
  attempts and mismatched digests fail before SSH. OCI admission independently
  verifies provenance, SBOM and vulnerability policy again.
- **publish** synchronizes and activates CFO/Olist project content using the
  source revision of the last successful runtime deployment. It does not
  replace the application container.

The runtime transaction uses the existing Compose installation at `/opt/leapview`,
container `leapview-cfo-leapview-1`, state volume `leapview-cfo_leapview-state`, and
PostgreSQL container `demo02-postgres-cfo`. SSH must match the pinned demo-02 host
key. It does not mutate Hetzner firewalls, DNS, the old VPS, or agent credentials.
It checks the actual database bindings and available backup space before stopping
writes. Control DB, DuckLake DB, globals, application state and configuration are
backed up together under `/etc/leapview-provider-cfo/compose-backup-*`.
Dump/tar readability is checked; this is not a full restore rehearsal.

Only image updates with unchanged schema/engine dependencies and Compose payloads
are admitted. Before creating a runtime deployment record, the workflow inspects
both immutable source revisions and reports their schema revisions, pending SQL
migrations, and changed schema/engine paths in the run summary. Modified or
removed historical migrations, missing forward migrations, and downgrades are
rejected. This source comparison is diagnostic, not migration admission or
proof of recoverability. The runner repeats it immediately before rollout.

Schema changes remain blocked: this workflow never applies or reverses migrations.
The existing `host upgrade` command implements fenced staging, activation, and
restart phases; it requires an already admitted operation whose migrations have
completed. It is not a standalone database upgrade or provider restore command.
A schema upgrade needs an independently verified coordinated recovery frontier,
an explicit migration executor, and paired state recovery before the old image
can be restarted. Do not bypass the guard or run a destructive down migration.
Preflight rejection creates no runtime deployment record, leaving the existing
publication pin unchanged.
Image payloads are extracted into a separate temporary directory before validation.
Same-image retries reuse an existing release only when its contents match the image;
they never overwrite the active release or its local configuration.
A host lock protects against overlapping operators. After replacement, the remote
transaction waits up to five minutes for the runner's authenticated shared-viewer
check of all four CFO pages (27 visuals). Readiness failure, failed browser checks,
SSH EOF, or timeout restores the predecessor image and configuration. Existing
CFO data and credentials are preserved. The host receipt is
`/etc/leapview-provider-cfo/compose-deployment.json`.

The runner reads **only** the shared viewer login over pinned SSH from the existing
root-protected `jacob-cutover-secrets.json` handoff. It masks those values and keeps
them only in the browser subprocess environment. It does not import the Infisical
human-access folder or retrieve the administrator login. Keep that protected
handoff's viewer values synchronized when rotating the shared login.

A successful GitHub deployment record (`task=demo-compose-runtime`, environment
`leapview-demo-runtime`) is the runtime pin. The workflow token has only the
additional `deployments: write` permission; no variable-write PAT is required.
`DEMO_RUNTIME_REVISION` is a **bootstrap fallback only**, used before the first
runtime deployment record. After that, deployment records take precedence.
A failed/in-progress record blocks publication until an operator reconciles the
host and re-runs deployment successfully. If recording success fails after the
host commits, the image may already be live: inspect the host receipt and re-run
the same qualified image; do not change the pin manually to hide the failure.
The retired stage/prepare/systemd entrypoints exit without changing infrastructure.

The `leapview-demo` GitHub environment authenticates to Infisical through
GitHub OIDC. The Infisical `prod:/demo/deployment` path supplies the
service-principal secrets:

- `DEMO_PUBLISHER_CLIENT_SECRET`
- `DEMO_RELEASE_CLIENT_SECRET`

The GitHub environment stores their durable database identities as
`DEMO_PUBLISHER_PRINCIPAL_ID` and `DEMO_RELEASE_PRINCIPAL_ID`. Keeping these
issuer-owned UUIDs beside `DEMO_PROJECT_ID` prevents legacy client aliases from
being sent to the canonical PostgreSQL credential boundary.

The publisher and release identities are separate service principals. Their
credentials are exchanged for one-hour, project-scoped OAuth workload tokens
on every publication. The publisher is restricted to managed-data ingestion,
project authoring, and release publication. The release principal is restricted
to viewing, approving, and activating the demo project environment, plus
managing the public dashboard publications declared by the canonical showcase.

The `leapview-demo` environment variable `DEMO_PROJECT_ID` stores the target's
durable `ProjectUID`. Content publication must use that issuer-owned identity;
the source bundle does not provide or replace it.

Target capability discovery requires an authenticated credential but no
pre-existing project grant. This is essential on the first deployment,
because the project graph is not active until its exact candidate is
activated. The subsequent authoring, ingestion, publication, approval, and
activation calls remain protected by the canonical project grants.

## Database upgrades and interrupted-operation recovery

Use **Hosted demo deployment → `upgrade`** for the reviewed schema **28 → 30**
transition on `app-leapview-demo-02`. Supply the immutable candidate image and its
successful **main-push Main artifacts** qualification run. This is a maintenance
operation: public access closes while the backup, restore rehearsal, migration,
and private CFO checks run. Ordinary `deploy` remains image-only and refuses
schema or engine changes before creating a deployment record.

The upgrade is bounded to the existing native demo topology: pinned demo-02 SSH
identity, local PostgreSQL 18 at its exact digest, the two existing databases,
local managed data inside the application home, and the existing Compose network
and four volumes. It rejects external storage, unaccounted writable mounts,
unexpected network participants, pending background jobs, historical SQL edits,
changed DuckDB/DuckLake dependencies, and any other migration boundary. It never
changes DNS or provisions another server.

The Actions environment and pinned root SSH connection remain this provider's
operator authority. The private request binds the exact successful qualification
receipt, live OCI admission report, source comparison, reviewed migration hashes,
and workflow run/attempt. The host executes the controller extracted from that
same candidate image. This bounded provider adapter does **not** create generic
release-authority records or bypass the generic host transition runner's
binary-rollback restriction. Other provider topologies still need their own
qualified transition integration.

The sequence is:

1. Preflight source compatibility and candidate-owned request validation before
   creating a runtime deployment record; authenticate the current CFO viewer.
2. Acquire the host upgrade lock; validate the running predecessor and storage
   topology. Retain private original configuration and immutable image identities.
3. Change Caddy to loopback-only bindings, disable automatic writer restarts,
   stop the application, require drained jobs, and stop PostgreSQL and Caddy.
4. Capture the entire stopped PostgreSQL cluster (both databases and roles),
   application home/managed files, and Caddy state. Hash and retain a private
   manifest. Restore all volumes into independent directories on an internal
   Docker network. Start the exact predecessor and validate all four CFO pages
   (27 visuals) through pinned SSH, retaining the real HTTPS origin and certificate
   verification. Listing a backup is never treated as restore proof.
5. Persist migration intent outside restored volumes. Run only the candidate's
   embedded migrations 029 and 030 with the control migrator credential under the
   canonical PostgreSQL migration fence, revalidate the request/recovery frontier,
   retain the existing River schema, and reconcile product role policy.
6. Preserve the agent encryption key, or provision it once if absent. Start the
   candidate behind the loopback gate. Require exact image/source/schema/readiness
   and another authenticated check of all four CFO pages.
7. Persist the commit boundary **before** restoring public bindings. Verify the
   public source and CFO pages, then mark the runtime deployment successful.
   Publication follows this successful record; it never advances on a failed check.

The private journal is `/etc/leapview-provider-cfo/upgrade-operation.json`.
Snapshots, original configuration, request evidence, and restricted operator logs
live under `upgrade-operations/<request-digest>/` beside it. Keep this directory
outside every restored volume. Snapshots and retained failed state are deliberately
not pruned automatically. The provisioned agent key is retained privately in the
operation directory as well as `leapview.env`; preserve it in operator backups.
No credentials or state archives are uploaded to Actions artifacts.

Before commit, migration, readiness, browser failure, SSH EOF, or handled
cancellation invokes paired recovery with a fresh bounded context. Recovery first
stops the named migration process and all writers, verifies the entire recovery
set, restores the cluster and file volumes together, restores configuration and
the predecessor, and checks its schema/readiness behind the gate. It persists
reopening intent before exposing the predecessor. It never runs down SQL.
A failed recovery leaves the journal blocking normal deployments and publication.

For power loss, a killed controller, or incomplete finalization, dispatch
**`recover` with the same candidate digest and qualification run**. It loads the
persisted request; a new workflow attempt cannot replace an unfinished operation.
Before commit it restores the predecessor. The Actions deployment is intentionally
reported unsuccessful in that case: the candidate was not deployed. After commit
it only completes candidate exposure/verification and the runtime record; it will
**never restore old data after the candidate could have accepted public writes**.
After successful predecessor recovery, start a new `upgrade` run when the failure
has been corrected. Ordinary deployments share the same lock and reject every
nonterminal journal.

If registry or Actions access is unavailable, the root operator can use the already
retained candidate controller and request on demo-02:

```sh
/etc/leapview-provider-cfo/upgrade-controllers/<candidate-sha256>/leapviewctl \
  demo-upgrade recover \
  --request /etc/leapview-provider-cfo/upgrade-operations/<request-digest>/request.json
```

Inspect the durable phase and private operator log first. This offline command
recovers/finalizes the host only; reconcile the GitHub runtime deployment through
`recover` once Actions access returns. Do not edit the journal, Goose ledger, or
runtime pin to conceal an incomplete operation.

CI exercises real PostgreSQL 28 → 30 migration, partial migration failure,
restricted-role policy reconciliation, physical paired recovery, process restart,
lock exclusion, and browser-approval failure paths using disposable resources.
The **actual deployed predecessor/data and selected candidate** receive the two
private browser gates during the explicitly dispatched maintenance operation;
CI fixtures are not represented as a backup of the live demo. The candidate image
also requires the normal provenance, OCI admission and enterprise qualification.
Future schema transitions are intentionally rejected until their policy and
recovery tests are reviewed; image-only updates continue through `deploy`.

## Human access

Human credentials are isolated from the deployment identity in the Infisical
`prod:/demo/access` path. GitHub Actions must never import this path. It contains
the private administrator recovery login and the deliberately shared product
demo login:

- `DEMO_ADMIN_EMAIL`
- `DEMO_ADMIN_PASSWORD`
- `DEMO_VIEWER_EMAIL`
- `DEMO_VIEWER_PASSWORD`

The shared principal is `demo@leapview.dev`. For the CFO project, grant it `RESOURCE_READ` on
`dashboard:cfo-command-center` and `RESOURCE_USE` on `semantic-model:finance`.
Do not carry Olist grants into a CFO-only project graph. Stage the grants before
publishing the candidate; the same activation requirement below applies.

For the Olist project, the target policy grants it `RESOURCE_READ` on each canonical dashboard ID
and `RESOURCE_USE` on the three backing semantic models. Use canonical IDs
(`dashboard:executive-sales`, `dashboard:fulfillment-operations`,
`dashboard:visual-showcase`, and `semantic-model:sales`,
`semantic-model:operations`, `semantic-model:visuals`), not dashboard URL names.
Dashboard read access opens the page; semantic-model use authorizes its queries. Project namespaces accept only `PROJECT_ADMIN` as a direct
grant, and dashboards do not support `RESOURCE_USE`; neither is needed for
this shared dashboard reader. Do not bind it to the built-in `viewer` role: that role
also grants access to shared conversation history. The shared login must
never receive administration, authoring, preview, refresh, deployment,
or connection privileges. Personal API tokens are allowed, but remain limited
to this principal's existing resource access; token capabilities cannot grant
additional authority. Agent tools remain governed by the principal's exact
resource grants.
Conversation and personal-settings pages are authenticated user surfaces, so
absence of a project role must not be treated as a blanket denial of those pages.

### Agent provider

LeapView platform admins select, test, and save the chatbot provider and model
in **Admin → Agent**. Ordinary chatbot users cannot change these settings.
Legacy provider environment values remain in use until an admin saves the first
configuration; subsequent image deployments do not override the saved selection.

The operator provisions `LEAPVIEW_AGENT_CREDENTIAL_KEY` once in the private
`/opt/leapview/leapview.env`. It must be 64 hexadecimal characters representing
32 random bytes. Preserve it across releases and back it up separately from the
database: saved provider credentials are encrypted with this key. The Compose
rollout preserves the environment file byte-for-byte and does not rotate keys.

Introducing admin configuration adds a database migration. Use the explicit `upgrade` action above for the reviewed 28 → 30 boundary;
the phased `host upgrade` command alone does not provide the native recovery
orchestration. After upgrading, an admin tests
and saves the provider configuration before verifying a chatbot conversation.

Treat the shared credential as public. To rotate it, reset the local password,
revoke every existing session for the principal, complete the forced password
change, and update `DEMO_VIEWER_PASSWORD` in Infisical. A password reset alone
does not revoke an already-issued browser session. On a replacement instance,
create the local shared principal before the first project deployment; the
administrator stages its least-privilege grants through
`POST /api/v1/projects/{project}/grants`, supplying a stable `id`, the exact
`resourceKind`/`resourceId`, `subjectType`/`subjectId`, `capability`, an
`expectedRevision`, and an `Idempotency-Key` header. Read the current policy
revision from the role-bindings or grants list response. Each mutation produces
a new target-policy revision; publish and activate a candidate containing that
revision before expecting serving access to change. The portable source bundle
does not contain these target-owned grants.

After runtime deployment, dispatch `publish` when the PR changes project content.
It uses the deployed revision, not an unrelated newer main revision.
