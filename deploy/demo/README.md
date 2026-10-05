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

Merging a PR does not change the public demo. Operations in **Hosted demo
deployment** are manual and run on `main`, using one shared concurrency group:

- **prepare** captures a coordinated copy, reopens the predecessor, and tests
  the candidate and complete publication on that isolated copy. A successful
  result is required before a database/permission upgrade.
- **deploy** selects the supported image-only or database-upgrade path and replaces the application image on `app-leapview-demo-02` at
  `89.58.13.145`. Supply `image` as `ghcr.io/flidai/leapview@sha256:<digest>` and
  `qualification_run` as the successful **Build / Main image** run ID for that exact
  digest. The run must contain `production-image-qualification-<attempt>`; older
  runs without a receipt are not admissible. First qualify an image with the
  updated workflow. Mutable tags, PR candidates, foreign workflows, stale run
  attempts and mismatched digests fail before SSH. OCI admission independently
  verifies provenance, SBOM and vulnerability policy again.
- **publish** synchronizes and activates CFO/Olist project content using the
  source revision of the last successful runtime deployment. It does not
  replace the application container. The workflow installs only the two
  version-aware publication client scripts from its protected revision; the
  selected revision still supplies the Go CLI, generated code and content.
- **recover** finishes the original interrupted host operation.
- **reconcile** takes the original GitHub `deployment_id`, verifies the current
  host and authenticated public revision, and repairs deployment bookkeeping
  without applying migrations or changing the runtime.

The runtime transaction uses the existing Compose installation at `/opt/leapview`,
container `leapview-cfo-leapview-1`, state volume `leapview-cfo_leapview-state`, and
PostgreSQL container `demo02-postgres-cfo`. SSH must match the pinned demo-02 host
key. It does not mutate Hetzner firewalls, DNS or the old VPS. Existing agent
credential keys are preserved; a missing key is prepared and verified as part
of the candidate configuration before live apply.
It checks the actual database bindings and available backup space before stopping
writes. Control DB, DuckLake DB, globals, application state and configuration are
backed up together under `/etc/leapview-provider-cfo/compose-backup-*`.
Dump/tar readability is checked; this is not a full restore rehearsal.

Before recording a deployment, preflight compares the two exact source revisions:
SQL history, engines, role policy, and the versioned permission/publication
contract in `internal/platform/releasecontract/contract.json`. Unsupported or
missing contracts, rewritten migrations and engine changes fail before maintenance.
The one historical compatibility entry is the verified schema-32 predecessor;
it does not guess compatibility from a schema number. A same-schema permission
change still requires the upgrade path. The candidate-owned controller validates
the request again, and a live upgrade requires a passed detached rehearsal bound
to the same images, source identities, installation profile and access intent.
It captures a fresh recovery point; it never restores the older rehearsal copy
over subsequently acknowledged writes.

Maintenance verifies installed deployment files against the immutable predecessor
image before staging the candidate. The application healthcheck command may move
to the canonical `CMD /usr/local/bin/leapview healthcheck`; all other Compose
settings, proxy configuration, and deployment defaults must remain unchanged.
The candidate generation supplies the new command, and recovery reactivates the
original predecessor generation. A changed port, volume, service, or healthcheck
timing still requires a separately reviewed topology change.

Image payloads are extracted into a separate temporary directory before validation.
New releases contain the same six runtime files and permissions as the host
installer. Documentation and qualification helpers remain in the image, outside
the installed generation. Same-image retries accept either that runtime-only
layout or the legacy complete payload, provided its contents match the image
exactly. They never overwrite the active release or its local configuration;
modified files and unknown extras still require operator review.
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
An unresolved record blocks publication. The runner collects the durable host
journal, actual container digest, source revision, schema and installation
receipts, and independently authenticates the public revision. A committed
candidate becomes the runtime pin. Verified recovery retains the candidate's
failed attempt and creates an idempotent successful predecessor record. Unknown
state stays blocked; the resolver never falls back to an older successful record.
If the runner or GitHub write fails after commit, run **reconcile** with the
original deployment ID. This does not migrate or restore data. A later content
publication failure reports a deployed runtime with incomplete publication and
can be retried with **publish**. Runtime deployment and publication are separate
results; a content failure does not undo acknowledged runtime writes.
After CFO publication commits, the workflow reads the shared viewer credentials
over the same pinned SSH connection and checks all four pages again. It uses
the current verification policy and confirms the selected runtime revision;
this read-only check cannot invoke migration, rollout or recovery. A browser
failure leaves the runtime record intact and the release incomplete. The run
summary retains the verified runtime outcome and published generation separately.

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
credentials are exchanged for short-lived, project-scoped OAuth workload tokens
on every publication. The publisher is restricted to managed-data ingestion,
project authoring, and release publication. The release principal uses a separate credential for approval and delivery
inspection. The version-aware client selects scopes before OAuth from the
independently bound deployed source; it does not authenticate just to discover
which scopes authentication requires. The typed publisher requests
`connection.manage connection.read connection.use delivery.build delivery.plan delivery.publish delivery.read model.read semantic.consume source.read`
for source revisions below schema 46, and uses `connection.upload` in place of
`connection.manage` for schema 46 and later. The reviewer requests
`delivery.approve delivery.read`.
These publisher actions are only the short-lived token ceiling needed by
managed-data ingestion and the retained CFO graph. They do not create role or
grant authority. Target-owned typed policy must separately authorize each
exact resource/action pair, and the captured serving policy must authorize
runtime reads.

The `leapview-demo` environment variable `DEMO_PROJECT_ID` stores the target's
durable `ProjectUID`. Content publication must use that issuer-owned identity;
the source bundle does not provide or replace it.

Target capability discovery requires an authenticated credential but no
pre-existing project grant. This is essential on the first deployment,
because the project graph is not active until its exact candidate is
activated. The subsequent authoring, ingestion, publication, approval, and
activation calls remain protected by the canonical project grants.

### Fresh demo installation bootstrap

For a fresh demo-02 instance, complete the application bootstrap before retrying
the host installer. Install the PostgreSQL CA at the configured application
home path, `/var/lib/leapview/home/postgres-root.crt`, before configuration
validation or any TLS database connection. Preserve the existing credentials
and encryption keys. Run `admin initialize` through the canonical
`/opt/leapview` root (`LEAPVIEWCTL_ROOT=/opt/leapview`) and keep its one-time
credential response in the root-private bootstrap handoff. Then use the
qualified application image's `admin delivery pool qualify` to generate the
physical-pool artifacts, and admit them with
`admin delivery pool bootstrap --pool <pool.json> --evidence <evidence.json> --apply`.
Retain the generated pool identity in the canonical private application
configuration.

Start the app on loopback at `127.0.0.1:8081`, then claim the issuer-owned
project through the supported project-claim flow. A fresh target has no active
publication, so the initial host-install attempt can remain waiting for
readiness. That wait is expected until the first publication activates. Do not
repeat initialization or create a host marker or receipt by hand.

Before that first publication, the claimed human owner must nominate a separate
human `release_approver` through the authenticated **Admin → Access** command.
The pre-publication bootstrap accepts that human session and nominee; the
principal-detail route currently returns HTTP 500 before publication, so use
the Access surface instead. The reviewer completes OAuth device authorization
with the custom scope `delivery.approve delivery.read`. Publish the first
candidate to activate it and the pending access policy.

To move release approval to the permanent service principal, temporarily grant
the human reviewer `project_admin` and publish that policy change first. Then
use the supported Admin → Access command to delegate `release_approver` to the
service principal, then publish and activate that role binding. Confirm a
release approved by that principal before removing the human reviewer’s
temporary role or account through the supported admin commands. Stop the
loopback app with `leapviewctl stop` before retrying host installation:
credential acknowledgement takes the exclusive application-state lock. After
the installer completes, continue with the one-time handoff below.

### First-install runtime handoff

A fresh host installation has `.host-install.json` but no rollout receipt. After
the exact qualified image has been installed, the CFO project has been published,
and the shared-viewer CFO check passes, create the one-time installation handoff
with the operator adapter:

```sh
DEMO_HOST=89.58.13.145 \
DEMO_IMAGE='ghcr.io/flidai/leapview@sha256:<qualified-digest>' \
QUALIFICATION_RUN='<successful Build / Main image run ID>' \
python3 scripts/bootstrap/compose_installation.py
```

The operator must have `gh` access to the qualification run, the publisher
principal environment used by the normal publication validator, and the local
browser dependencies used by `scripts/demo_validate_browser.mjs`. The adapter
reads the qualification and transition receipts from that exact successful run,
authenticates the public build revision, then checks the shared viewer's four
CFO pages before it invokes the remote writer over the pinned demo-02 SSH key.
If qualification or either authenticated check fails, the writer is not invoked.
It reads the shared viewer login through the established root-protected handoff;
the writer receives no credentials.

The root-side writer independently checks the host-install marker, active
generation bytes against the immutable image payload, running container digest
and Compose identity, loopback readiness, live Goose schema, and instance ID
from both PostgreSQL and the application. It records the host target ID from
the marker and qualification metadata from the admitted artifact. It refuses
to run when a deployment receipt or host upgrade history already exists.
The resulting versioned, secret-free
`/etc/leapview-provider-cfo/compose-installation.json` is created atomically as
root with mode `0600`. Its UTC `validatedAt` is recorded only after these checks
finish. The file supplies first-install runtime reconciliation; after a real
replacement, `compose-deployment.json` remains authoritative.

### First runtime pin and upload compatibility

The restored schema-45 image predates `connection.upload`. Keep
`DEMO_RUNTIME_REVISION` pinned to the exact source revision already running;
do not point the fallback at a candidate that has not replaced it. After
writing first-install evidence, dispatch the normal `deploy` action with the
same installed digest and its qualification run. This same-image operation
records the installed runtime because its candidate and predecessor are
identical. The current adapter selects the schema-45-compatible
`connection.manage` scope, but the publisher has no exact manage grant and the
supported delegation path cannot issue connection administration. Its
publication therefore still fails with HTTP 403 on the old upload path. Treat
that workflow as an incomplete publication, even though its runtime record
remains successful and authoritative. A `publish` retry still selects the old
runtime and fails for the same missing grant.

Then complete the normal `prepare` and `upgrade` flow with the qualified
schema-46 image that includes the upload action. This must be a real runtime
replacement. The workflow's runtime job can commit while its separate,
mandatory publication job fails with HTTP 403 because the schema-46 publisher
does not yet have its exact `connection.upload` grant. Schema 45 cannot stage
that newly introduced action, so the grant must wait until schema 46 is active.
Treat the workflow as incomplete while retaining the successful runtime
record. Do not broaden publisher grants automatically.

With schema 46 active, use the supported `leapview admin access stage-grant`
operation to stage one exact `connection.upload` grant for the publisher on
`connection:finance_files`, using the current policy revision and the normal
operator approval flow. Then use the existing CFO data to plan, build, and
publish a candidate without running the upload/data-sync step. The permanent
release service principal approves this publication; activation makes the
staged grant effective. Retry the normal deployment/publication workflow only
after that activation, so its upload step can use the exact grant. Finally, use
a second qualified digest for the same schema-46 source in the normal image-only
`deploy` path; this verifies a distinct image replacement and its required
publication. The first-install evidence exception applies only to the
same-image runtime record and does not authorize a candidate whose image or
source differs from the installed predecessor.

## Database upgrades and interrupted-operation recovery

The demo workflow is a transport adapter for the shared **operator-authorized
single-host** `leapviewctl host upgrade` commands. Its installation profile is
`deploy/demo/host-upgrade.json`; it contains no secrets. The workflow validates
that profile against the host, installed image, Compose services, storage and
public origin. Other equivalent installations supply their own private profile
in the request and their own authenticated application validator.

Select `prepare` before a forward schema/permission transition, then `deploy`.
The deploy action selects the appropriate supported mode; the explicit `upgrade`
alias remains available. Select `recover` only for an interrupted operation. Supply the immutable image and its successful main-push Build / Main image
qualification run. The controller is extracted from that exact candidate. No
migration is run during serving startup or by the Python transport.

Admission requires both `qualification.json` (fresh-install/image checks) and
`transition.json` (the supported legacy-to-typed upgrade and subsequent
publication checks) from that same run attempt. The transition receipt binds
the exact published digest, source, validator revision and predecessor; a local
candidate test or another image's successful run is not deployment evidence.
For an authorized PR qualification run, GitHub signs the protected workflow
revision while the image contains the selected PR revision. The private request
binds both identities separately; normal main-push deployment retains their
equality. A PR qualification run is still not admitted for live demo deployment.
The frozen schema-32 fixture remains a regression boundary after the demo moves
forward. Each actual installation still needs its own passed `prepare` receipt.

Pre-merge tests use local image identities and test admission inputs inside
disposable containers; those inputs are never release-admission evidence. The
final-artifact lane verifies the registry image and its real OCI admission.
The fixture's subsequent-deployment check recreates the same candidate image
and publishes again. Deployment of a second, distinct qualified digest remains
an operational acceptance check after the initial upgrade.

Preflight compares immutable SQL history, relevant River/DuckDB dependencies,
product role policy and the actual packaged extension supply. Unrelated module
changes do not block deployment. Historical SQL changes, downgrade, changed
River/DuckLake/engine supply, external storage, unaccounted writers and unsupported
topology fail closed. The local provider supports PostgreSQL 18 at the existing
installation's exact digest, local managed data, and the canonical Compose
application/proxy payload. It does not upgrade the PostgreSQL engine.

This command uses protected Actions and pinned root SSH as operator authority.
It does not create release-owner records or change the existing authoritative
`host upgrade --phase` contract. Goose remains the sole schema version store and
migration engine. Pending versions are derived from the candidate, not hardcoded
as a particular release pair.

The preparation sequence is:

1. Check immutable qualification, installation and compatibility evidence.
2. Briefly fence writers and capture the complete stopped PostgreSQL/filesystem
   state. Persist a terminal live capture journal and reopen the predecessor.
3. Exercise the captured copy under a separate operation lock and journal.
   Clone failures and cancellation clean up only clone-owned resources. They
   cannot stop, restore, reconfigure or expose the live installation.
4. Check the predecessor, apply the exact candidate transition, authenticate the
   publication clients, run the real `deploy_demo.sh` from the exact candidate
   source, and validate the CFO pages. Browser, Python, curl and Go requests all
   use the same pinned clone-only proxy with no ambient proxy bypass or public
   fallback. The internal Docker network blocks external integrations.
5. Save a private passed receipt, including the candidate configuration digest.
   This is environment-specific upgrade evidence, distinct from fresh-install
   image qualification and from successful deployment.

For legacy permissions, the operator supplies private
`/etc/leapview-provider-cfo/access-transition-intent.json` before preparation.
It names the exact target/project/environment, previous policy and serving
identities, explicit typed assignments, and distinct publisher/reviewer
principals. The transition preserves historical rows and requires a new captured
serving policy. It never infers project-wide authority from legacy capabilities.
An intent entry may list several actions for one resource; the plan expands it
into separate grants with deterministic IDs, one exact permission per stored
grant. Single-action entries retain their supplied grant ID. Duplicate principal
roles or exact permissions are rejected before policy capture.
The candidate's read-only `leapview admin transition-access-inventory --project`
command reads the existing policy revision/digest and active generation from
schema 32 before migration. Use those returned identities in the private intent;
do not guess them or derive replacement permissions from broad legacy roles.
For the retained CFO source graph, the publisher's typed policy also needs
`source.read` on `source:finance.financials`, `model.read` on each of
`model:cash_forecast`, `model:cash_scenarios`, `model:cash_weeks`,
`model:finance_countries`, `model:finance_dates`, `model:finance_discount_bands`,
`model:finance_products`, `model:finance_segments`,
`model:financial_performance`, `model:pnl_lines`, `model:pnl_statement`,
`model:variance_driver_dimension`, and `model:variance_drivers`,
`semantic.consume` on `semantic-model:finance`, and `connection.read`,
`connection.use`, and `connection.upload` on `connection:finance_files`.
Managed-data synchronization needs read access to recover and poll its upload
session as well as the exact upload grant to stage files. `connection.manage`
is a separate, non-delegable administration action and does not substitute for
upload authority. These resource grants are
independent of the publisher's project-scoped `release_operator` role. The
schema-32 inventory command does not emit graph dependencies; this list comes
from the unchanged retained CFO graph. This supported transition path covers
the unchanged retained graph and routine runtime/frontend image updates that
reuse it. A changed graph must be separately reviewed for its dependency pairs
and authoring permissions, with matching publisher client-scope changes; do not
assume this transition intent can supply create authority or that an exact
resource grant substitutes for project-scoped create permission. The unchanged-
source rehearsal does not validate a changed-graph publication.
The reviewer authenticates with its actual workload credential; naming a reviewer
in intent is not an approval. Credentials never enter the immutable request or
public evidence. Credential ID/secret changes, if needed, must be synchronized
in GitHub/Infisical before publication can be reported complete.

The subsequent live maintenance sequence is:

1. Validate qualification/admission and source compatibility before recording a
   runtime deployment. Validate the current viewer, host inventory and capacity.
2. Acquire the shared installation lifecycle lock and persist maintenance intent.
   Ordinary start/install/update commands reject an unfinished journal, even
   after reboot. Close public bindings, disable restarts and stop every writer.
3. Copy the whole stopped PostgreSQL cluster, application/managed files and proxy
   state. Retain original configuration outside all restored volumes.
4. Restore an independent writable copy on an internal Docker network. Validate
   the predecessor, then run the candidate's actual migration/configuration and
   validate the candidate on that copy. All four CFO pages/27 visuals must pass.
   The copy has no production-network membership or external integration access.
5. Persist live migration intent. Run embedded Goose migrations with the control
   migrator under the canonical fence and reconcile product permissions. Keep
   River unchanged. Preserve the agent credential encryption key, generating a
   missing key during preparation and reusing that exact key for live apply.
   An existing key is never rotated by the upgrade.
6. Start and validate the live candidate through loopback bindings. Persist commit
   before reopening public traffic. Public image/source and CFO checks must pass
   before the workflow advances the runtime record.

When an explicit access transition is needed, the candidate authenticates the
publisher and independent reviewer, publishes the retained source with the typed
policy, and records normal approval evidence. The host waits for that exact
publication and serving-policy digest to become active before viewer validation.

The prepare capture and the subsequent live upgrade require bounded downtime.
The longer detached publication rehearsal runs with the predecessor serving.
The live operation repeats its protected recovery/candidate gates against a fresh
recovery point; the detached receipt does not waive them.
A fresh-install image qualification does not replace the restored-data rehearsal.
The [PostgreSQL cold-copy requirements](https://www.postgresql.org/docs/18/backup-file.html)
require stopping the cluster and copying it as a whole. The isolated Docker
network uses a loopback TCP relay and pinned SSH for normal HTTPS validation.
Each validation acknowledgment binds the operation digest and named checkpoint.

For demo-02 the durable journal is `/opt/leapview/upgrade-operation.json` and the
shared lifecycle lock is `/opt/leapview/.leapviewctl.lock`. Private backups,
request evidence, original configuration, generated key and logs remain under
`/etc/leapview-provider-cfo/upgrade-operations/<request-digest>/`. Keep these paths
outside restored volumes. They are not automatically pruned.

On precommit failure the controller stops all writers and restores the previous
application, database/files and configuration together, then verifies readiness
before reopening. A rehearsal failure does not migrate live data. A failed
recovery leaves traffic closed. After commit, retries finalize the candidate and
never restore old data over new public writes. Never run down migrations or
manually start the application through an unfinished maintenance window.

For an interrupted Actions run, choose `recover` with the same candidate and
qualification run. The transport loads the original persisted request. An
operator on the host can also use the retained candidate controller:

```sh
sudo /etc/leapview-provider-cfo/upgrade-controllers/<candidate-digest>/leapviewctl \
  host upgrade status --request /etc/leapview-provider-cfo/upgrade-operations/<request-digest>/request.json
sudo /etc/leapview-provider-cfo/upgrade-controllers/<candidate-digest>/leapviewctl \
  host upgrade recover --request /etc/leapview-provider-cfo/upgrade-operations/<request-digest>/request.json
```

`plan` validates a private admitted request and image engine compatibility without
starting maintenance. `capture` closes and durably releases the live capture transaction.
`verify-copy` runs only the detached clone. `apply` requires the passed preparation
and performs the full live operation with a fresh recovery point. `status` reports the
persisted state. `recover` restores precommit state or finalizes a committed
candidate. The `migrate`/`rehearse`/`migrate-copy` subcommands are guarded internal container
entrypoints, not a standalone bypass for operators.

CI exercises migration preservation, partial failure, a synthetic future revision,
physical recovery, two installation profiles, private validation failure and
ordinary-start exclusion. These disposable fixtures are not a live demo backup.
After merge, qualify the final main image and explicitly schedule the first live
upgrade. No host, database, runtime pin or DNS change is performed by opening or
merging this PR.

## Human access

Human credentials are isolated from the deployment identity in the Infisical
`prod:/demo/access` path. GitHub Actions must never import this path. It contains
the private administrator recovery login and the deliberately shared product
demo login:

- `DEMO_ADMIN_EMAIL`
- `DEMO_ADMIN_PASSWORD`
- `DEMO_VIEWER_EMAIL`
- `DEMO_VIEWER_PASSWORD`

The shared principal is `demo@leapview.dev`. For the CFO project, grant it typed `dashboard.read` on
`dashboard:cfo-command-center` and `semantic.consume` on `semantic-model:finance`.
Do not carry Olist grants into a CFO-only project graph. Stage the grants before
publishing the candidate; the same activation requirement below applies.

For the Olist project, the target policy grants it typed `dashboard.read` on each canonical dashboard ID
and `semantic.consume` on the three backing semantic models. Use canonical IDs
(`dashboard:executive-sales`, `dashboard:fulfillment-operations`,
`dashboard:visual-showcase`, and `semantic-model:sales`,
`semantic-model:operations`, `semantic-model:visuals`), not dashboard URL names.
Dashboard read access opens the page; semantic-model use authorizes its queries. Do not bind it to the built-in `viewer` role: that role
also grants access to shared conversation history. The shared login must
never receive administration, authoring, preview, refresh, deployment,
or connection privileges. Personal API tokens are allowed, but remain limited
to this principal's existing resource access; token scopes cannot grant
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

Introducing admin configuration adds a database migration. Use the explicit `upgrade` action above for supported control-schema changes;
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
`resourceKind`/`resourceId`, `subjectType`/`subjectId`, typed permission actions, an
`expectedRevision`, and an `Idempotency-Key` header. Read the current policy
revision from the role-bindings or grants list response. Each mutation produces
a new target-policy revision; publish and activate a candidate containing that
revision before expecting serving access to change. The portable source bundle
does not contain these target-owned grants.

After a successful runtime deploy, the workflow publishes from that verified
deployed revision. Dispatch `publish` to retry a content-only failure. It uses
the deployed revision, not an unrelated newer main revision.
