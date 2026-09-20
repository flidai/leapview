# Administrator and deployment readiness review

Internal project review; not part of the public documentation catalog.

Date: 2026-09-18

Status: **Ready for administrator-managed production deployment**

## Executive decision

LeapView now has a strong deployment engine and substantially closed
administrator credential and discovery lifecycles. The delivery API supports
immutable planning, build and seal, publication, approval, activation evidence,
bounded discovery, and rollback. Credentials are generated with strong random
material and stored using keyed fingerprints and Argon2id verifiers. Platform
administration, project administration, and resource capabilities remain
deliberately separate.

The administrator lifecycle blockers are now implemented and manually
exercised. Ownership-bearing semantic attributes, dashboard roots, and agent
conversations can be inventoried and transferred or tombstoned through the
supported offboarding operation; an active agent run remains fail-closed until
the supported cancellation finishes. Access Settings explains platform,
direct, group-derived, owner, compiled, and denied authority. The offline
lockout drill restored a local platform administrator, revoked prior sessions,
rejected the old credential, enforced a password change, and retained audit
evidence without a database edit.

The release-blocking work is complete. The exact installed candidate passed
the Docker/Compose recovery qualification, including browser, governed-query,
audited-denial, upgrade, interruption, restart, and two-node failover drills.
The clean synthetic checkout also passed `task ci:full`. The scheduled P2
administrator hardening and UX work is now implemented as well; no item in the
FAI-932 through FAI-943 deployment-readiness plan remains open.

The target state is not “put every infrastructure secret in a browser.” OIDC,
SCIM, database, object-store, and deployment configuration can remain
deployment-managed. The release gate is that every product-owned identity,
authorization, credential, approval, deployment, and rollback state must be
discoverable and reversibly manageable through a supported administrator
workflow.

## Scope and method

This review covers:

- every route and action exposed by the personal and platform Settings panel;
- the generated public API contract and the corresponding runtime handlers;
- PostgreSQL production identity/credential behavior;
- role, grant, and deployment persistence and lifecycle operations;
- operator and recovery documentation; and
- implementation comparisons with upstream Lightdash, Metabase, Grafana, and
  Rill sources in Flid's local reference library.

Reference snapshots were fetched on 2026-09-08:

| Product | Revision |
| --- | --- |
| Lightdash | `35906ad9e116df59d2d59da2f58e45e9b39eaff8` |
| Metabase | `b7500371caf5457840d139f0fd37e7ec87038d79` |
| Grafana | `307ef2b57ffa0956e7d28b2f1759a692f63199b1` |
| Rill | `4f814a86196fac2ba9664b9237826582de2dad03` |

The reference library is prior art, not an authoritative security standard.
Comparisons are used to identify established administrator workflows and not to
copy another product's design indiscriminately.

## Implementation update

The implementation waves on 2026-09-16 and 2026-09-17 resolved PostgreSQL credential defaults
and maximum lifetimes, the service-secret selector, project-role listing and
audited/CAS-safe binding deletion, project-scoped authorization audit,
functional audit filters, and delivery read-model fidelity. Unsupported
mutable grant CRUD was removed from TypeSpec/OpenAPI rather than connected to
immutable compiled generation evidence.

The product now exposes platform-administrator list/grant/revoke with CAS,
idempotency, transactional audit, and last-admin protection; complete bounded
delivery collections; effective-access provenance; service-account
disable/enable, overlapping rotation, revoke-all, and last-used evidence; and
single-project Access and Delivery Settings surfaces. Manual E2E found and
fixed command execution, migration, composed-reader, and delivery-collection
authorization defects that focused tests missed. The overall readiness
decision is now **ready** because the final installed-candidate and clean
checkout qualification gates listed above passed on 2026-09-18.

The final hardening wave added redacted platform-administrator controls,
server-evaluated recent interactive authentication, optional durable
two-person approval, explicit administrator mutation feedback, and clearer
Access/Delivery evidence states. A final security review also closed
PostgreSQL session-evidence composition, Settings approval-policy propagation,
last-usable-administrator lifecycle races, and durable audit evidence for
authenticated denied platform-role mutations.

SQLite adapters still exist in the repository as test and offline-tooling
fixtures, but they are not an application composition option. The architecture
rules explicitly classify them as non-compositional fixtures and reject SQLite
imports from production sources (`internal/platform/architecture/rules.go:137-158`;
`internal/platform/architecture/architecture_test.go:1710-1728`). Fixture
behavior was useful for finding inconsistent domain assumptions, but it is not
part of the production deployment verdict.

## Current Settings inventory

| Surface | Available actions | Read-only information or limitations |
| --- | --- | --- |
| Profile | Upload/change/remove avatar; change display name; select theme; open archived-chat management; archive or delete all chats | Email is IdP-managed. Title and Username are editable inputs but have no persistence command. |
| Security and sessions | Change a local password; revoke individual browser/desktop sessions; revoke individual CLI/authoring sessions | IdP-managed passwords are read-only. There is no “revoke all my sessions” action in this surface. |
| Personal API tokens | Create a named, finite token with at least one explicit capability; reveal the new secret once; list and revoke the current user's tokens | No administrator view of another user's individual token material. Dynamic bearer credentials cannot act as platform administrators. |
| General | Change/reset instance display identity; upload/remove logo | Instance ID, canonical origin, and environment are read-only. |
| Principals | Create local user; rename local user; delete, block, or unblock; reset local password; revoke one or all sessions | External identities are correctly read-only. Project roles are displayed but cannot be assigned or removed. Individual PATs are not visible. |
| Groups | Create, rename, and delete local groups; add/remove members | Directory-managed groups are correctly read-only. Project roles and grants cannot be assigned here. |
| Service accounts | Create/delete and disable/enable a service principal; create, overlap-rotate, individually revoke, or revoke all secrets; inspect last-used evidence; select a finite 30/90/180/365-day lifetime | Project roles are assigned through the Access role-binding workflow rather than inline. Rename exists in the command service but is not exposed by the component. |
| Authentication | List current/revoked platform authority; grant/revoke with revision fencing, recent interactive authentication, and optional two-person approval | Authentication-provider configuration remains deployment-managed. Token, service, desktop, and authoring credentials cannot use the browser platform-role mutation path. |
| Agent | Change project agent system prompt when authorized; inspect tools and model status | Provider/model credentials remain deployment-managed. |
| Storage | Inspect backend summary, tables/views, active files, size and snapshot metadata | No destructive storage operation, which is appropriate for this surface. |
| Query history | Search/filter, paginate, inspect SQL, plan, timing, result and failure details | Operational inspection only. |
| Audit | Filter and paginate events by the server-bound project, actor, action, resource, and date range | The project is deliberately read-only because the server is bound to one Project. |
| System | None | Health, build, limits, storage, agent, and API status only. |
| Publications | Open/copy public URL and iframe; suspend, resume, or rotate a public URL; inspect history | This manages anonymous dashboard publication, not application deployment. |
| Access settings | Create and delete direct project role bindings with policy-revision fencing; inspect effective-access provenance for platform, direct, inherited, owner, compiled, and denied paths | Compiled fine-grained grants remain generation-owned and immutable in Settings. |
| Delivery | Inspect operator state, publication history, and retained generations; roll back an eligible retained generation | Plans, builds, candidates, approvals, publications, and generations are discoverable through bounded API collections; the UI intentionally summarizes publications/generations. |

Because a LeapView server is deliberately bound to one Project, the new Access
and Delivery routes intentionally use that server-owned project rather than a
browser project switcher.

## Current public API inventory

The generated OpenAPI document contains 200 operations. Coverage is broad:

- instance identity, product settings/status, logo, capabilities, and initial
  project claim;
- current-user profile, avatar, password, theme, browser/desktop sessions,
  authoring sessions, API tokens, and effective capabilities;
- principals, groups and memberships, service principals and secrets, semantic
  attributes and claim mappings;
- platform administrators, project roles, role bindings,
  effective-capability inspection, and batched authorization checks;
- project authoring, drafts, previews, commands, export, candidate source
  synchronization, and catalog access;
- managed connections, revisions, upload sessions, multipart uploads, and
  target connection binding plan/test/health/enable/disable/refresh;
- releases, release events, refresh runs and events, and query events;
- delivery plan/build/seal/candidate/publication/generation detail,
  bounded plan/build/candidate/approval/publication/generation collections, approval
  request/approve/deny/revoke, rollback, and operator snapshot;
- dashboards, filters, visuals, queries, appearance, and public publications;
- semantic catalog/query/explain operations; and
- agent configuration, conversations, runs, events, cancellation, and search.

The API is therefore not missing a deployment engine or its core discovery
surface. The lifecycle, recovery, and final qualification work identified by
this review is complete; the dated discovery tables below are retained as
defect history.

## Manual PostgreSQL E2E result — 2026-09-16

The running development server was bootstrapped from a fresh PostgreSQL 18
volume, synchronized with the sample project, deployed, and exercised through
both Chromium and the public API. Disposable credentials and bindings were
revoked or deleted after verification; secret values were never logged.

| Journey | Result | Evidence |
| --- | --- | --- |
| Personal API token create/default expiry/revoke | Pass | Browser and API creation produced a finite 90-day expiry; past and over-365-day requests returned `400`; revocation persisted. |
| Service-secret expiry policy | Partial | API omission produced 180 days and validation rejected past/over-maximum values, but the browser selector rendered 30 days selected rather than the declared 180-day recommended default. |
| Service-principal lifecycle response contract | Fail | Create, secret revoke, and principal delete returned `500 COMMAND_CONTRACT_NOT_EXECUTED` after committing. Follow-up reads proved the create/revoke/delete mutations occurred. |
| Project role catalog and binding lifecycle | Pass | Eight roles listed; a viewer binding created, listed, deleted, and replayed idempotently; final-admin deletion returned `409`. |
| Project-scoped authorization audit | Fail | Role-binding events appeared in global audit with `projectId: null` and were absent from the project audit endpoint/UI. |
| Audit action/time filtering | Pass | Project-bound, read-only project field plus action/from/to filters returned only the expected deployment event. |
| Delivery detail and operator reads | Partial | Known plan, build, candidate, generation, and publication IDs returned coherent PostgreSQL state; operator truthfully returned `degraded: true` with `detailed_evidence_unavailable`. No enumeration route exists. |
| Grant and deployment discovery | Fail/open | Grant GET returned `404`; deployment enumeration routes returned `404`; `/admin/access` and `/admin/delivery` do not exist. |

The manual verdict is therefore **not all planned problems are fixed**. The
credential response-contract failure and missing project audit scope are new
release-blocking defects discovered by the E2E pass; the previously documented
grant, platform-admin, delivery-enumeration, and Settings-surface gaps remain.

### Remediation verification — second pass

The original table above is retained as defect-discovery evidence. A second
manual pass against the same PostgreSQL 18 volume verified the subsequent
fixes, including an in-place schema upgrade rather than only a fresh database.

| Journey | Result | Evidence |
| --- | --- | --- |
| Service-principal lifecycle | Pass | Create returned `201`; secret revoke and principal delete returned `204`; final reads returned `404`. |
| Service-secret browser default | Pass | Chromium rendered 30/90/180/365-day choices with `180` selected. |
| Principal generated commands | Pass | Update, disable, enable, and delete execute through generated command contracts; disposable principal deletion returned `204`. |
| Project role-binding audit | Pass | Create/delete returned `201`/`204`, and both events appeared in the project audit endpoint with the bound project ID. |
| Platform-admin upgrade and lifecycle | Pass | Migration 020 applied to the existing volume; grant returned `200`, revoke returned `204`, stale revisions failed, and revoking the sole remaining usable admin returned `409`. |
| Delivery collections and Settings | Pass for implemented scope | The composed PostgreSQL reader populated publication and retained-generation sections; Chromium rendered active generation, publication history, retained generations, and a typed rollback handler. |
| Access Settings | Pass for implemented scope | Chromium rendered the active project, policy revision, direct bindings, create/delete controls, and an explicit compiled-grant limitation. |
| Public grant contract | Pass | Unsupported mutable `/grants` operations are absent from OpenAPI and return `404`; compiled grants remain generation-owned. |

These remediations close the concrete defects found by the first E2E pass, but
do not close the overall release gate. Severity findings below describe the
original review point; the current closure matrix in the implementation plan is
authoritative where they differ.

### Completion verification — 2026-09-17

A third manual pass used the rebuilt PostgreSQL-backed application after the
service-account, effective-access, ownership, and complete delivery-discovery
work. Disposable credentials were deleted and secret values were never
printed or retained.

| Journey | Result | Evidence |
| --- | --- | --- |
| Service-account containment | Pass | Live API create and one-time secret issuance returned `201`; OAuth exchange returned `200` and populated `lastUsedAt`; disable rejected the old secret; re-enable did not revive it; overlap rotation kept both selected secrets valid; revoke-all rejected both; clean deletion returned `204`. |
| Service-account Settings | Pass | Chromium rendered the disposable account plus Disable, Revoke all, secret inventory, Last used, and Rotate controls. |
| Effective-access provenance | Pass | The project-root query returned distinct authority reasons, and Chromium rendered the server-bound explanation surface. Browser contracts cover platform, direct, group-derived, owner, compiled, and denied paths. |
| Delivery discovery | Pass | Plans, builds, candidates, approvals, publications, and retained generations all returned `200` through the composed application. A `k1.` continuation returned the second publication; malformed and cross-collection cursors failed with `400` and `422`. |
| Access and Delivery Settings | Pass for implemented scope | Chromium rendered the active-project binding surface and the Delivery operator snapshot, publication history, and retained generations. |

Manual delivery E2E found three integration defects in sequence—collection
authorization fell through to an object resolver (`403`), the application
reader omitted four collection forwards (`503`), and the API protocol erased
the unrecognized native `k1.` continuation. Each now has focused regression
coverage and the final live pass succeeds.

Final local verification passed the focused access/application/PostgreSQL
tests, all five frontend CI shards, the Settings browser contract, all three
TypeScript typechecks, architecture rules, critical-package coverage,
engineering-quality budget, exception policy, generated API tests, and the
dead-export scan. The repository-wide `task ci` functional, PostgreSQL,
coverage, and quality lanes passed. The last check identified one otherwise
unused exported explanation helper; it was removed, and the focused package,
quality, diff, and dead-export checks pass. The complete local-password
lockout drill also passed. The exact installed-candidate recovery drill and
the complete detached-worktree `task ci:full` release gate subsequently
passed; no deployment-readiness release criterion remains open.

## Severity-ranked findings

### Resolved — Unsupported mutable grant API contract removed

Project-role listing is implemented in the generated adapter and module
dispatcher. The originally advertised grant CRUD was not implementable safely:
existing grant rows are immutable compiled-generation evidence, not mutable
administrator authority. Those unsupported operations have therefore been
removed from TypeSpec and OpenAPI, and route conformance proves `/grants` is
not exposed. Effective-access explanation is now available through both the
API and Access Settings.

Evidence:

- `api/typespec/access.tsp:925-933,973-1032`
- `internal/access/http/apigen.go:154-161`
- `internal/access/module/api.go:96-99`

Required outcome:

- implement and dispatch every advertised role/grant operation, or remove it
  from the public contract until it exists;
- add route-to-handler conformance tests generated from every OpenAPI
  operation ID;
- enforce project scope, CAS/idempotency, graph-resource validation, and
  transactional audit on each mutation; and
- qualify grant create/read/change/delete and effective-access removal on the
  PostgreSQL production composition.

### Resolved — PostgreSQL credential expiry policy

`expiresAt` remains optional in the personal-token and service-secret API
contracts. A domain-owned policy now resolves omission to 90 days for PATs and
180 days for service secrets, caps both at 365 days, and is enforced again at
the PostgreSQL boundary. Settings exposes 30/90/180/365-day choices for service
secrets and the live browser selects the declared 180-day recommended default.

Past and over-maximum expiries now fail through typed domain errors before
persistence, while PostgreSQL retains its database-clock constraints as
defense in depth.

Evidence:

- `api/typespec/current_user.tsp:11-16`
- `api/typespec/access.tsp:69-72`
- `web/components/admin/personal-settings.ts:466-469`
- `web/components/admin/settings-surfaces.ts:665`
- `internal/access/sqlite/credentials.go:169-171,449-452`
- `internal/access/postgres/access_core.go:1040-1090,1217-1263`
- `internal/access/postgres/queries/core_ops.sql:242-252,292-303`

Required outcome:

- define one domain-owned default and maximum lifetime policy;
- apply it at the request/domain boundary before PostgreSQL persistence is
  called;
- expose an expiry choice for service secrets;
- keep the database maximum as defense in depth; and
- add PostgreSQL integration tests for omitted, past, maximum, and over-maximum
  expiry, with domain-policy unit tests independent of persistence.

### Resolved — Service-principal command contracts

Create, secret revoke, and principal delete now execute through their generated
command contracts. The PostgreSQL E2E pass observed the documented `201`/`204`
responses and final `404` reads instead of a committed mutation hidden behind
`500 COMMAND_CONTRACT_NOT_EXECUTED`.

Required outcome:

- execute create, revoke, and delete through their generated command contracts;
- prove each successful mutation returns its documented `201` or `204` status;
- prove failed responses never hide a committed mutation; and
- retain non-replayable handling for one-time secret responses.

### Resolved — Project role-binding audit scope

Role-binding create/delete mutations are transactionally audited with the
canonical bound project ID. The PostgreSQL E2E pass found both events through
the project audit endpoint and Settings workflow while retaining global audit
visibility.

Required outcome:

- carry the canonical server-bound project ID into every project authorization
  audit event;
- test create/delete visibility through the project-scoped audit endpoint; and
- retain global audit visibility without duplicating events.

### Resolved — Project role bindings can be removed safely

The Access API now deletes an exact binding through a target-revision CAS and
produces an immutable successor policy revision. The operation is idempotent,
audited transactionally, and prevents removal of the last project-admin
binding. Fine-grained grants remain immutable, generation-owned evidence.

Direct binding removal and complete ownership-safe offboarding are closed.

Evidence:

- `api/typespec/access.tsp:942-961`
- `internal/access/postgres/authorization_policy.go:432-452`
- `api/typespec/access.tsp:976-1031`
- `web/components/admin/settings-surfaces.ts:262-265`

Required outcome:

- add a target-revision-CAS delete operation with idempotency and transactional
  audit;
- prevent removal of the last effective project owner/administrator when that
  would make the target unmanageable;
- surface direct and group-derived assignment removal in Settings; and
- qualify create, change, delete, stale-revision, retry, disabled-subject, and
  last-admin behavior.

### Resolved — Platform-administrator lifecycle and lockout recovery

The public API now lists, grants, and revokes `platform_admin` using PostgreSQL
CAS/idempotency, transactional audit, and last-usable-admin protection. An
in-place migration adds the operation identity needed by existing databases.
The production-only offline command restores one existing local administrator,
can reset its local credential, revokes existing sessions, requires password
change, and records a transactional audit event. The manual PostgreSQL lockout
drill passed. Settings now shows current and revoked authority without exposing
credentials. Browser grant/revoke requires a server-verified interactive
session authenticated within 15 minutes; bearer, service, and authoring
credentials fail closed. Deployments can require the durable two-person
approval workflow, whose request/approve/cancel/expire/execute transitions are
restart-safe, CAS-fenced, separation-of-duty protected, and transactionally
audited.

Evidence:

- `internal/access/postgres/schema.sql:536-543`
- `internal/access/postgres/access_core.go:339-375`
- `internal/access/postgres/instance_initialization.go:63-85`
- `internal/admin/module/routes.go:25-83`

Required outcome:

- add list/grant/revoke platform-admin operations;
- require recent strong authentication and preferably a second administrator
  for changes in production;
- prohibit removal/disablement of the last usable administrator;
- revoke request credentials or re-evaluate them immediately after role
  changes; and
- document and test a supported lockout-recovery procedure.

### Resolved — Principal deletion enforces ownership-safe offboarding

Principal and service-principal deletion now performs a transaction-bound
ownership inventory and returns `PRINCIPAL_OWNS_OBJECTS` while live resources
remain. The ownership API transfers semantic-attribute definitions and
dashboard roots, and transfers or tombstones agent conversations. Retries are
idempotent, historical attribution remains durable, and an active agent run
fails closed until the supported cancellation completes.

Evidence:

- `api/typespec/access.tsp:651-666,769-788`
- `internal/access/http/principal_admin_handler.go:117-155`
- `internal/access/http/service_principal_handler.go:131-149`

Implemented outcome:

- Defined which product objects have transferable ownership versus durable
  historical attribution;
- deletion returns the documented conflict while live ownership remains;
- transfer and explicit tombstone workflows are domain-owned and retry-safe;
  and
- local users and service principals are covered with owned and
  historical objects.

### Resolved — Delivery recovery is server-discoverable

Delivery now provides SQL-bounded, scope-bound cursor collections for plans,
builds, candidates, approvals, publications, and retained generations in
addition to point reads. The fresh-operator qualification starts with an empty
home, discovers the active and rollback-safe prior generation from server-owned
PostgreSQL state, validates its linked plan/build/candidate/approval/publication
evidence, submits rollback, and verifies the selected generation becomes active.

The operator snapshot now truthfully returns only target identity and the active
generation and reports itself degraded with
`detailed_evidence_unavailable`. The recovery guide no longer claims empty
physical pools, roots, leases, GC cycles, or delete intents are authoritative.
Detailed pool/root/lease evidence that is not owned by this projection remains
explicitly degraded instead of appearing healthy.

Evidence:

- `api/typespec/deployments.tsp:901-1127`
- `internal/project/cli/candidate_checkpoint.go`
- `internal/deployment/postgres/repository.go:98-111`
- `internal/deployment/module/native_delivery_read_api.go:233-236`
- `docs/articles/operate/delivery-recovery.md:7-20`

Implemented outcome:

- Bounded, paginated lists cover plans, build attempts, candidates,
  publications, approvals, and retained generations;
- immutable evidence is sufficient to select and validate a rollback target;
- the operator projection and runbook expose only authority-owned evidence;
- Settings provides approval and deployment history; and
- recovery qualification uses a fresh administrator home with no local
  checkpoints.

### Resolved — Delivery status preserves incident and rollback evidence

The native response adapter now preserves build failure classification, seal
creation/verification time, compact canonical resolved inputs and their digest,
publication reason, and generation activation/retirement/rollback windows.
Snapshot seals and candidates persist the resolved-input record atomically;
status reads validate it against the immutable plan and qualification evidence
and fail closed on mismatches. Migration 022 backfills historical seal creation
time from the only previously retained timestamp and leaves legacy empty-input
records explicitly empty.

Evidence:

- `api/typespec/deployments.tsp:12-47,324-444`
- `internal/deployment/module/native_delivery_read_api.go:60-65,134-158`
- `internal/deployment/module/native_delivery_read_api.go:210-230`

Implemented outcome:

- Every supported lifecycle state and failure classification is preserved by the
  read adapter;
- resolved-input evidence excludes credentials and object keys;
- publication/generation recovery timestamps and reasons are populated; and
- integration fixtures cover terminal and indeterminate states.

### Resolved — Personal tokens use explicit attenuated authority

Settings and API issuance now require at least one explicit capability, and the
issued allowlist is intersected with current effective access on every use.
Legacy/dynamic bearer credentials are explicitly barred from platform-admin
authority, so later promotion cannot turn a PAT into a platform-administration
credential.

Evidence:

- `web/components/admin/personal-settings.ts:457-477,523,643-650`
- `internal/access/canonical_contract.go:217-272`
- `internal/access/module/module.go:220-246`

Implemented outcome:

- default to an explicit allowlist and require at least one capability for an
  executable token;
- make dynamic authority a separate advanced choice with an explicit warning;
- show “dynamic” versus the exact allowlist in every credential inventory;
- prevent a dynamic PAT from becoming a platform-administration credential, or
  require separate issuance for that class; and
- test promotion, demotion, role deletion, token expiry, and token revocation.

### Resolved for release — Service-account lifecycle is complete

Settings and the public API now cover creation, finite secret expiry,
disable/enable, overlapping rotation, individual and revoke-all revocation,
last-used evidence, and deletion. Project authorization is assigned through
the audited Access role-binding workflow, preserving the separation between
identity, credential, and authority. A single guided wizard and inline rename
remain UX refinements rather than lifecycle gaps.

The separation of identity and credential persistence is correct. The missing
piece is an administrator workflow that composes those existing primitives
without weakening their audit boundaries.

Required outcome:

- create the principal, explicit project role/grants, expiry, and first secret
  through one guided workflow with a review step;
- preserve independent transactions/audit records where required, but report a
  clear partial-failure state and safe remediation;
- support enable/disable, rename, overlapping rotation, last-used evidence, and
  revocation; and
- offer least-privilege deployment presets without hiding the exact resulting
  capabilities.

### P2 — Legacy release and native delivery expose incompatible lifecycles

The release API accepts caller-selected environment and generation identity and
can retain drafts that later fail serving-state validation. Native delivery
instead begins with a target-owned plan and server-resolved evidence. Both
public lifecycles remain present, and the legacy artifact path may be
unavailable depending on composition.

Operators and automation cannot infer from the API contract which path is
authoritative for a production deployment.

Evidence:

- `api/typespec/releases.tsp:97-105`
- `internal/release/module/api.go:243-255`
- `internal/release/service.go:119-162,242-269`
- `api/typespec/deployments.tsp:511-519`

Required outcome:

- declare native target-owned delivery authoritative;
- retire the legacy lifecycle or make it a compatibility adapter that resolves
  and verifies target, plan, artifact, and serving identity before accepting a
  runnable draft; and
- publish migration and deprecation behavior for existing clients.

### P2 — Some paginated operational lists fetch all rows before paging

Release and managed-connection list handlers load complete collections, apply
authorization/filtering in memory, and only then page the result. The release
query itself has no SQL limit and performs additional per-row loading. This can
make an incident-time list request unbounded despite its paginated contract.

Evidence:

- `internal/release/module/api.go:308-328`
- `internal/release/postgres/repository.go:353-369`
- `internal/release/postgres/queries/release.sql:40-48`
- `internal/release/module/catalog_api.go:48-77`

Required outcome:

- push stable keyset pagination and safely expressible authorization filters
  into the owning database/catalog authority;
- remove per-row reloads; and
- test large collections under an explicit memory and latency budget.

### Resolved — Settings exposes the supported authorization model

The server-bound Access Settings route now supports direct project role binding
creation/removal with policy-revision fencing and explains platform, direct,
group-derived, owner, compiled, and denied authority. Compiled fine-grained
grants remain immutable generation evidence and are identified as such rather
than advertised as mutable administrator records.

Evidence:

- `internal/admin/module/routes.go:25-83`
- `web/components/admin/settings-surfaces.ts:262-266`
- `docs/articles/security/authorization.md:13-29,74-87`
- `internal/app/api/ideal_surface_contract_test.go:60-76`

Required outcome:

- add a project access surface for role bindings, grants, effective access, and
  semantic attribute assignments; or explicitly declare these API-only and
  correct every UI reference;
- show inherited/group-derived versus direct access and the exact revocation
  control;
- align policy documentation with the semantic-model access implementation; and
- add an end-to-end access-review/offboarding journey.

### Resolved — Published authorization metadata matches platform requirements

TypeSpec and generated contracts now use a first-class `platform_admin` mode
for platform operations. Contract tests reject platform-scoped operations that
declare only authenticated access, while runtime handlers remain fail-closed.

Evidence:

- `api/typespec/access.tsp:737-824,834-923`
- `internal/access/http/handler.go:109-156`
- `internal/access/http/service_principal_handler.go:17-241`

Required outcome:

- add a first-class platform-administrator authorization mode to the contract;
- generate consistent OpenAPI authorization metadata; and
- add a contract test that compares declared platform scope with handler-level
  requirements.

### P3 — Several Settings controls or filters are false affordances

- Profile Title and Username accept edits but have no save command.
- The service-account update command exists, but the component has no rename
  control.
- `projects-admin` has a component and fixture entry but is not a valid section
  and has no producer.

These should be implemented or removed before general availability so operators
can trust that a visible control has a durable effect.

## Permission and token design assessment

The foundations are solid:

- platform administration is distinct from project roles and resource grants;
- project roles expand into stable canonical capability bundles;
- resource capabilities are validated against graph resource kinds;
- explicit token allowlists are checked against current effective access and
  intersected again at request time;
- authoring credentials are short-lived and project/capability scoped;
- random bearer material is prefixed by credential class;
- keyed HMAC-SHA256 fingerprints support indexed lookup without storing the raw
  secret;
- Argon2id verifiers defend the stored credential material;
- cleartext secrets are returned once with `Cache-Control: no-store` and
  `Pragma: no-cache`;
- PostgreSQL principal disablement transactionally revokes sessions, PATs, and
  service secrets; and
- security mutations are designed around transactional audit records.

The setup is operationally solid: creation, least-privilege assignment,
inventory, use evidence, finite expiry, overlapping rotation, individual and
bulk revocation, principal disablement, and complete access removal are all
available through supported Settings/API/CLI workflows. The separation across
credential classes is deliberate and the installed-candidate drill verifies
the production PostgreSQL composition.

The project-role taxonomy is explicit rather than monotonic. `owner` and
`admin` are capability aliases, as are `contributor` and `editor`; `member`
includes `RESOURCE_MANAGE` while editor/contributor do not. Public
authorization guidance tells administrators to review the expanded capability
set and not infer authority from a display-name hierarchy.

## Baseline comparison

| Capability | LeapView | Lightdash | Metabase | Grafana | Rill |
| --- | --- | --- | --- | --- | --- |
| Machine identity and permission are assigned together | Separate audited identity, Access binding, and secret workflows | Yes; a service account must select one system/custom role shape | Yes; an API key is created in a permissions group | Yes; service account creation includes org role and optional granular roles | Project/org membership and roles have explicit add/set/remove operations |
| Multiple simultaneously valid secrets | Yes | No; the current rotate endpoint replaces the account token | Regeneration replaces the key | Yes | Token types vary; deployment credentials are separately scoped |
| Explicit expiry | Finite defaults and maximums enforced by API, Settings, and PostgreSQL | Supported; non-expiring is allowed but discouraged | API-key model is group-oriented; rotation is explicit | Token expiry is supported in the service-account UI/API | TTL-scoped tokens are used for deployment/runtime access |
| Last-used evidence | PAT and service-secret evidence | Yes for service accounts | Key inventory exposes management metadata | Token inventory is first-class | Token/audit behavior depends on token class |
| Change/revoke project role | CAS-safe create/change/delete with audit and final-admin protection | Project roles can be managed and removed | Change the key's permissions group | Update org/granular roles; disable service account | Explicit member-role set and member removal |
| Deployment discovery | Bounded native lifecycle collections, operator pointer, evidence, and rollback | Deploy sessions/logs and project UI workflows | Not a dashboards-as-code deployment baseline | Provisioning-oriented, not equivalent | Projects expose production/branch deployment state and management actions |

Concrete upstream evidence is recorded in the supporting reviews linked below.
The recurring baseline is not a particular role model; it is lifecycle closure.
Products let an administrator see which authority a machine identity has, change
that authority, rotate its credential, disable it, and recover deployment state
without reconstructing IDs from a single client machine.

The Lightdash snapshot itself demonstrates why implementation evidence was
preferred over narrative docs: its service-account guide says rotation is not
available, while the current controller implements `PATCH
/{tokenUuid}/rotate`. The comparison above follows the implementation.

LeapView is ahead of this baseline in several areas: immutable delivery
evidence, explicit approval separation-of-duty, target CAS fences, multiple
service-principal secrets for overlap rotation, capability attenuation, and
transactional audit intent. Closing the administrator lifecycle gaps preserves
those advantages.

## Deployment-ready acceptance plan

### Gate A — Credential lifecycle safety

- PostgreSQL passes the credential-expiry contract exposed by the API and UI.
- In PostgreSQL, disabling and re-enabling a principal cannot revive any issued
  session, PAT, service secret, authoring credential, or OAuth credential.
- Every credential inventory shows owner, class, exact scope mode, created,
  expiry, last-used, and revoked state.
- Service-secret rotation can overlap old and new credentials and revoke either
  individually.
- An administrator can revoke one compromised user PAT without disabling the
  principal.
- An administrator can execute and verify an audited “revoke all credentials”
  incident-response action across every credential class.
- Dynamic PATs are not the default and cannot silently become platform-admin
  credentials.

### Gate B — Reversible authorization

- Platform admins can be listed, granted, and revoked with last-admin safety.
- Every advertised role and grant operation has a runtime handler and passes a
  generated route-to-handler conformance test.
- Project role bindings can be created, changed, and deleted with CAS,
  idempotency, and transactional audit.
- Direct, group-derived, inherited, owner, and platform authority are
  distinguishable in Settings and API responses.
- Principal deletion blocks on live ownership and offers a supported transfer
  or explicit tombstone workflow.
- A test starts with a user/service principal holding mixed direct and group
  access and proves that offboarding removes every effective capability.

### Gate C — Recoverable deployment operations

- A fresh operator can enumerate the active target, prior publications,
  retained generations, build/candidate evidence, and pending approvals.
- Status reads preserve lifecycle state, failure classification, resolved-input
  evidence, publication reasons, and generation recovery timestamps.
- The operator snapshot reports only evidence it actually owns and populates.
- Rollback target selection can be completed without local CLI state.
- Native target-owned delivery is the documented authoritative lifecycle; the
  legacy release path is removed or behaves as a verified compatibility layer.
- Approval and rollback actions are available in a governed Settings workflow
  or an explicitly supported administrator CLI/API workflow with equivalent
  discoverability.
- Recovery, rollback, and publication runbooks are exercised against the
  PostgreSQL production composition.

### Gate D — Contract and UI trustworthiness

- OpenAPI authorization metadata matches runtime platform/project checks.
- Every OpenAPI operation ID is exercised against the composed router, so an
  advertised operation cannot exist without dispatch.
- Every visible Settings input/filter has a tested durable effect.
- Security documentation names only currently supported routes and concepts.
- The administrator journey is covered in route, contract, browser, and
  PostgreSQL integration tests.

## Validation performed

### Final release qualification — 2026-09-18

The exact installed candidate
`sha256:57368b49bc7a9bb2a6e1df42b2cc830faa7af1f4552a9cdca9e34dff55084bd5`
passed in 505 seconds. All nine phases succeeded: preflight, target bootstrap,
enterprise authoring, application upgrade, performance, governance,
interruption recovery, restart persistence, and multi-node process. The report
records all release assertions true, including native-PostgreSQL-only,
fresh-operator rollback, upgrade persistence, and two-node data-plane queries
before and after abrupt loss and rolling restarts.

The final source snapshot, captured as clean synthetic commit
`2dc0aeb3dd6d634391362de583146e8367c992f9`, passed the complete
`task ci:full` contract from a detached worktree. The final exact-candidate
browser run covered all 13 routes, accessibility scans, filter/map
interactions, and all 12 reviewed visual baselines. The quality budget was met
by extracting cohesive deployment and refresh responsibilities, without
raising the budget.
Migration qualification includes a real native-delivery schema revision 21 to
current revision 24 upgrade and replay, rather than fresh-schema coverage only.
SQLite remains a test/offline adapter and is not selectable by production
composition; its secret-revival regression coverage does not imply that the
production authority migrated back from PostgreSQL.

The focused pure access suites passed:

```text
go test ./internal/access ./internal/access/auditcontract \
  ./internal/access/oidc ./internal/access/policy \
  ./internal/access/scimprov ./internal/access/snapshot \
  ./internal/access/trustedclaims
```

The administrator CLI package also passed:

```text
go test ./internal/admin/cli
```

Generated Go, signal, OpenAPI, and sqlc artifacts are present and verified by
the integrated generation/database checks. The earlier package-setup failure
was an intermediate checkout condition and is retained only in the dated
discovery history above.

## Supporting implementation reviews

- [Implementation plan](admin-deployment-readiness/implementation-plan.md)
- [Identity and token lifecycle](admin-deployment-readiness/identity-tokens.md)
- [Authorization and administrator lifecycle](admin-deployment-readiness/authorization-admin.md)
- [Delivery and operational recovery](admin-deployment-readiness/delivery-operations.md)
