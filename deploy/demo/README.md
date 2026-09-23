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

After `Main artifacts` builds and qualifies the `main` revision,
`.github/workflows/demo-deploy.yml`:

1. downloads the selected pinned public dataset and synchronizes it as
   managed data;
2. authenticates to `/api/v1/capabilities` and admits the running runtime only
   when it reports API v1, native PostgreSQL delivery, a clean production
   build, and a canonical immutable build revision;
3. publishes the selected project source root through the normal candidate,
   approval, and activation APIs; and
4. verifies publication activation and public readiness.

Normal automatic runs are deliberately content-only. The `compose-deploy`
manual action is a bounded operator rollout for the current replacement VPS.
It accepts only the exact reviewed predecessor and exact digest-pinned target,
requires the target's successful `Main artifacts` qualification and OCI
provenance, stages the matching deployment payload under `/opt/leapview`, and
checks public readiness and the running build identity. Before its first
durable change it snapshots the active generation, deployment configuration,
and host marker while retaining the private application environment only in
the rollout process. It never leaves a second provider-key copy on disk. Any failed write, restart,
identity check, or readiness check restores that complete predecessor state.
This pinned transition contains no database migration, Compose contract, or
host-lifecycle change. A different predecessor or target must use the full
[release qualification](../../.github/workflows/release.yml) and
[installed-candidate qualification](../../.github/workflows/installed-candidate.yml)
path instead of reusing this action.
The publication records both the selected source revision and the authenticated
running build revision in its job output; they must match, and the runtime
must satisfy the compatibility contract above. The bounded rollout temporarily
admits only its GitHub runner through the provider firewall, pins the reviewed
host fingerprint, and removes the runner rule afterward. No SSH private key is
tracked in the repository.

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

For an operator-qualified replacement runtime, set `DEMO_RUNTIME_REVISION` in
the `leapview-demo` GitHub environment to the exact source revision reported by
that immutable image. Set it together with the new project and principal IDs
only after cutover. Then dispatch the `publish` action to validate the new
credentials and publish matching source. While this override is set, unrelated
main artifact builds do not republish the pinned source automatically. An unset
override preserves the legacy tracked revision for the existing demo. The
legacy `stage`, `prepare`, and `deploy` actions target the old installation;
do not use them for the new Compose-managed host. The replacement host uses
only the exact, revision-pinned `compose-deploy` action described above.

The `leapview-demo` environment variable `DEMO_PROJECT_ID` stores the target's
durable `ProjectUID`. Content publication must use that issuer-owned identity;
the source bundle does not provide or replace it.

Target capability discovery requires an authenticated credential but no
pre-existing project grant. This is essential on the first deployment,
because the project graph is not active until its exact candidate is
activated. The subsequent authoring, ingestion, publication, approval, and
activation calls remain protected by the canonical project grants.

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

The hosted-demo rollouts receive `DEEPSEEK_API_KEY` from the protected
Infisical `prod:/demo/deployment` path and pass it directly to the selected
runtime rollout script. The rollout step must not override that injected value
with an unset GitHub environment secret. The legacy
`scripts/rollout_demo_runtime.sh` path writes the mapped provider variables to
its private mode-0600 `runtime.env`; the replacement-host
`scripts/deploy_compose_demo_runtime.sh` path writes them to the private
mode-0600 `leapview.env`. Both set `LEAPVIEW_AGENT_API_KEY`,
`LEAPVIEW_AGENT_BASE_URL`, and `LEAPVIEW_AGENT_MODEL`. The provider key is never committed.
An operator-triggered rollout refreshes the key from the deployment
secret. Provider configuration enables the runtime while access policy
continues to control which resources its tools can reach.

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

Manual recovery is available from the workflow dispatch control. It republishes
the selected `main` revision through the identical project-content path.
