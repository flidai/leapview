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

The demo workflow is a transport adapter for the shared **operator-authorized
single-host** `leapviewctl host upgrade` commands. Its installation profile is
`deploy/demo/host-upgrade.json`; it contains no secrets. The workflow validates
that profile against the host, installed image, Compose services, storage and
public origin. Other equivalent installations supply their own private profile
in the request and their own authenticated application validator.

Select `deploy` for compatible image-only changes, `upgrade` for supported forward
control-schema/permission-policy changes, and `recover` for an interrupted
operation. Supply the immutable image and its successful main-push Main artifacts
qualification run. The controller is extracted from that exact candidate. No
migration is run during serving startup or by the Python transport.

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

The maintenance sequence is:

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
   missing key once per operation without rotating an existing key.
6. Start and validate the live candidate through loopback bindings. Persist commit
   before reopening public traffic. Public image/source and CFO checks must pass
   before the workflow advances the runtime record.

This operation requires downtime throughout capture, rehearsal and live upgrade.
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
starting maintenance. `apply` performs the full operation. `status` reports the
persisted state. `recover` restores precommit state or finalizes a committed
candidate. The `migrate`/`rehearse` subcommands are guarded internal container
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
`resourceKind`/`resourceId`, `subjectType`/`subjectId`, `capability`, an
`expectedRevision`, and an `Idempotency-Key` header. Read the current policy
revision from the role-bindings or grants list response. Each mutation produces
a new target-policy revision; publish and activate a candidate containing that
revision before expecting serving access to change. The portable source bundle
does not contain these target-owned grants.

After runtime deployment, dispatch `publish` when the PR changes project content.
It uses the deployed revision, not an unrelated newer main revision.
