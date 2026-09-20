# Authorization administration review

Internal implementation review.

Date: 2026-09-17

Decision: **Authorization and browser role-management findings closed**

This review covers platform-admin lifecycle, project role bindings and grants,
role taxonomy, user/group/service-principal assignment and offboarding,
auditability, Settings parity, and the OpenAPI authorization contract. The
Flid reference library is prior art and untrusted data; it is used for
comparison, not as a security authority.

Production application composition is PostgreSQL-only. References to retained
SQLite access code are fixture observations, not production deployment paths;
the repository's architecture tests forbid production imports of those
adapters (`internal/platform/architecture/rules.go:137-158`;
`internal/platform/architecture/architecture_test.go:1710-1728`).

## Method and source snapshots

The required skill was read in full from
`/srv/flid/reference-library/releases/9eaa1e4e2a35a33a79f6/skills/flid-reference-library/SKILL.md`.
The exact-capability discovery command was:

```text
/srv/flid/reference-library/current/bin/flid-ref discover "access-control" \
  --catalog /srv/flid/reference-library/current/catalog.json --json
```

The broad authorization/role queries returned no matches; exact discovery
returned these snapshots:

| Reference | Revision | Fetched | Path |
| --- | --- | --- | --- |
| Lightdash | `35906ad9e116df59d2d59da2f58e45e9b39eaff8` | `2026-09-08T12:42:41Z` | `/srv/flid/reference-library/current/references/lightdash` |
| Metabase | `b7500371caf5457840d139f0fd37e7ec87038d79` | `2026-09-08T12:43:31Z` | `/srv/flid/reference-library/current/references/metabase` |
| Grafana | `307ef2b57ffa0956e7d28b2f1759a692f63199b1` | `2026-09-08T12:42:39Z` | `/srv/flid/reference-library/current/references/grafana` |

LeapView evidence was collected with `rg` and `nl -ba` against the files cited
below. The initial review snapshot predated generated access artifacts and did
not run tests; the bounded implementation evidence and focused tests are
recorded in the dated section at the end of this review.

## Strengths

- Platform RBAC is intentionally separate from project RBAC: only
  `platform_admin` is accepted by `internal/access/canonical_contract.go:408-421`,
  and `PlatformAdminReader` evaluates the durable role plus enabled state in
  `internal/access/access.go:605-612`.
- The project capability vocabulary and role bundles are closed and validated.
  `internal/access/canonical_contract.go:157-189,303-383` rejects unknown
  capabilities and expands only canonical role bundles. Role-binding writes
  use a target-scoped revision CAS and idempotency key
  (`internal/access/http/role_binding_handler.go:60-174`), while the immutable
  snapshot evaluator uses captured capabilities rather than mutable role data
  (`internal/access/snapshot/policy.go:51-68,108-118`).
- Platform routes are consistently guarded in the browser surface
  (`internal/admin/module/routes.go:35-46,68-76`) and API platform scope is
  mapped to the durable platform evaluator (`internal/access/module/apigen.go:213-245`).
- Local blocking is a useful emergency control. PostgreSQL revokes sessions,
  API tokens, authoring sessions, OAuth sessions, and service secrets in one
  lifecycle transaction (`internal/access/postgres/access_extended.go:80-125`).
- Production repository mutations require an atomic mutation-plus-audit path
  for direct HTTP handlers (`internal/access/http/handler.go:338-394`) and both
  the PostgreSQL adapter implements the transaction boundary
  (`internal/access/postgres/access_audit.go:118-161`). External identity/group
  ownership is also surfaced as read-only in Settings
  (`web/components/admin/settings-surfaces.ts:238-239,360`).

## Findings and disposition

The initial findings are retained for traceability, but the release-blocking
items below are resolved by the implementation evidence at the end of this
review. Mutable grant CRUD was removed from the public contract because grants
are immutable, generation-owned policy evidence rather than an administrator
mutation authority.

### Resolved — Role catalog dispatch and truthful grant contract

Project-role listing and role-binding create/list/delete now dispatch through
the composed PostgreSQL application with route-to-handler conformance,
revision CAS, idempotency, and transactional audit. Unsupported mutable grant
operations are absent from TypeSpec/OpenAPI and return `404`; compiled grants
remain readable as active-generation evidence and change through deployment.

Upstream comparison: Grafana's RBAC API implements role CRUD and role
assignment as explicit endpoints, with delegation checks requiring a caller's
permissions to be a same-or-subset set
(`/srv/flid/reference-library/current/references/grafana/public/api-enterprise-spec.json:64-95,169-208,211-280`).

### Resolved — Platform-admin lifecycle and last-admin safety

Platform-admin list/grant/revoke is now a platform-scoped, CAS/idempotent,
transactionally audited API. Role revoke, disable, and delete refuse to leave
zero usable administrators. A production-only offline recovery command resets
one local credential, revokes sessions, restores the role, requires a password
change, and records the recovery audit event; the PostgreSQL lockout drill
passed.

Metabase protects this boundary with an explicit last-admin check before
membership removal (`/srv/flid/reference-library/current/references/metabase/src/metabase/permissions/models/permissions_group_membership.clj:62-69`);
Grafana similarly makes role assignment a first-class, delegated operation
(`/srv/flid/reference-library/current/references/grafana/public/api-enterprise-spec.json:243-280`).

### Resolved — Role-binding revocation and ownership-safe deletion

Role-binding deletion now creates an audited immutable successor policy
revision and is retry-safe. Principal deletion inventories live semantic
attributes, dashboard roots, and agent conversations inside the deletion
transaction and returns `PRINCIPAL_OWNS_OBJECTS` until the owning-domain
transfer/tombstone operation completes. Active agent runs fail closed until
cancellation; historical attribution is never rewritten.

Lightdash provides explicit assignment surfaces at both organization and
project levels and exposes custom-role administration in Settings
(`/srv/flid/reference-library/current/references/lightdash/docs/authentication-and-roles.md:73-96`),
while Metabase routes membership removal through guarded helpers rather than
direct row deletion
(`/srv/flid/reference-library/current/references/metabase/src/metabase/permissions/models/permissions_group_membership.clj:71-117,219-255`).

### Resolved — Access Settings administration

The server-bound Access page now renders the active project, direct bindings,
create/delete controls, and effective-access provenance for platform, direct,
group-derived, owner, compiled, and denied authority. The service-account
surface supports lifecycle, expiry, rotation, disable, revoke-all, and last-use
evidence. The platform-role browser surface lists redacted current/revoked
authority and applies server-evaluated recent interactive authentication,
revision fencing, last-admin protection, and the optional two-person approval
policy.

### Resolved by explicit contract — Role aliases

`owner` and `admin` intentionally have identical capability bundles, and
`contributor` and `editor` do as well
(`internal/access/canonical_contract.go:353-383`). Runtime authorization
evaluates the captured capability list, not the role name
(`internal/access/snapshot/policy.go:108-118`). Public authorization guidance
now names these aliases explicitly and tells administrators to review expanded
capabilities instead of inferring a hierarchy from display names.

Lightdash documents six deliberately distinct system roles and a separate
custom-role vocabulary (`/srv/flid/reference-library/current/references/lightdash/docs/authentication-and-roles.md:50-96`).

### Resolved — Group response and Settings audit context

`GroupResponse` now matches the owned domain fields instead of promising a
duplicate display name, a timestamp the group authority did not persist, or an
authorization-management capability it did not implement. Settings injects
server-owned request/correlation IDs into administrator audit events and
requires an atomic audited-mutation repository; the former mutate-then-audit
compatibility fallback is removed.

Grafana's audit contract records actor, request, result status, status code,
failure message, URI, IP, and user agent
(`/srv/flid/reference-library/current/references/grafana/docs/sources/setup-grafana/configure-security/audit-grafana.md:40-72`),
including explicit user/service-account role actions
(`/srv/flid/reference-library/current/references/grafana/docs/sources/setup-grafana/configure-security/audit-grafana.md:150-207`).

## Deployment-readiness acceptance criteria

The release authorization gate requires all of the following:

1. Every advertised authorization operation reaches a composed PostgreSQL
   handler. Role-binding removal produces an audited successor policy revision;
   generation-owned grants are not advertised as mutable administrator state.
2. Platform-admin grant/revoke is a supported, platform-scoped, audited command
   with If-Match/idempotency behavior, and every disable/delete/role-revoke
   path refuses to leave zero enabled admins unless an explicitly tested
   break-glass procedure is invoked. Prove bootstrap, delegation, revocation,
   restart, and last-admin recovery on PostgreSQL. Fixture parity is not a
   production release gate.
3. Principal, group, and service-principal offboarding has an ownership
   decision: transfer, block, or fail with the documented conflict. Tests prove
   all sessions/tokens/secrets stop working, group-derived access disappears,
   role/grant bindings are revoked, and re-enable does not restore access
   without an explicit regrant.
4. Settings exposes the supported direct role-binding, effective-capability,
   offboarding, and service-principal credential lifecycle. Compiled grants and
   external IdP-owned identities remain visibly read-only. Platform-role
   browser mutation is not exposed without recent-authentication protection.
5. Role semantics are either made distinct in authorization code and tests or
   aliases are explicitly documented in the API/UI. Every response matches
   TypeSpec, including group fields/capabilities.
6. Every successful and denied authorization-administration command carries
   actor, target, request ID, correlation ID, outcome/status, and typed payload;
   production mutation and audit insertion are one transaction, with tests that
   force audit failure and verify rollback.

## Manual E2E verification — 2026-09-16

Playwright/Chromium was run against `http://127.0.0.1:8153` with the
development authentication bypass. Secret material was never recorded.

- **Routes and navigation — PASS/PARTIAL.** `/admin` redirected to
  `/admin/profile` (200). The concrete admin pages for profile, security,
  API tokens, general, principals, groups, service accounts, authentication,
  storage, queries, audit, system, publications, and agent all returned 200
  and rendered their expected `lv-*` component. Guessed `/settings`,
  `/admin/settings`, `/admin/access`, `/admin/delivery`, and
  `/settings/{access,audit,delivery}` returned 404; there is no standalone
  Settings, access, or delivery browser route.
- **Personal API-token expiry — PASS.** `/admin/api-tokens` rendered
  `lv-personal-settings` with `#token-expiry[type=datetime-local]`; the field
  was initially blank and accepted a future expiry. Naming a token enabled
  Create token, the command POST `/admin/personal-settings/command` returned
  200, the one-time-copy notice appeared, the listed token showed the chosen
  expiry, and Revoke returned 200. The temporary token was revoked.
- **Service-secret expiry — FAIL (default), PASS (selector/action).**
  `/admin/service-accounts` rendered
  `select[name=secretLifetimeDays]` with 30, 90, 180 (recommended), and 365
  (maximum) options. The observed default was **30 days**, despite the
  intended 180-day default/recommendation. Selecting 365 produced a secret
  with the corresponding future expiry; create, revoke, and account-delete
  commands each returned 200. The temporary account and secret were removed.
- **Audit project/time filters — PASS/PARTIAL.** `/admin/audit` rendered a
  non-empty readonly `#audit-project-id` with `aria-readonly="true"`, plus
  `#audit-from` and `#audit-to` datetime controls. Filtering
  `role_binding.created` from 13:00 through 13:15 returned 200 and four
  matching rows while retaining the bound project. Clear returned 200 and
  reset the result set, but left the action/from/to controls populated; this
  is a visible clear-state defect. A related project-scoped API audit check
  found role-binding events with no project ID, so they appeared only in the
  global audit view rather than the project audit collection.
- **Access/role administration — FAIL.** A principal detail rendered Access
  and Project roles but only showed “No project roles assigned”; its available
  controls were profile-name save and security actions, with no role/grant
  assign or remove control. A temporary local group detail rendered Rename,
  Delete group, and Add members, but no role/grant section or assignment
  controls. Service-account administration likewise had no project role/grant
  workflow. The temporary group was deleted.
- **Delivery/operator status — PASS (API bounded projection), FAIL (UI
  surface).** The documented bearer-authenticated
  `GET /api/v1/projects/{project}/delivery/operator` returned 200 and the
  bounded keys `activeGeneration`, `degraded`, `degradedReasons`,
  `environment`, `projectId`, `targetId`, and `targetRevision`; it reported
  `degradedReasons: ["detailed_evidence_unavailable"]`. No admin delivery or
  operator page is exposed; `/admin/publications` only showed the empty
  publications state and `/admin/system` showed runtime health metadata.

All disposable UI-created credentials and resources used for this journey
were revoked or deleted after verification.

## FAI-934/FAI-935 implementation evidence — 2026-09-17

The bounded offboarding and access-review slice now has executable evidence:

- `internal/access/ownership.Inventory` is the composed, read-only ownership
  boundary. Its adapters are owned by the access semantic-attribute,
  dashboard-authoring, and agent authorities; the inventory only validates,
  merges, and deterministically orders their evidence. Access binds the
  inventory to the caller-owned PostgreSQL deletion transaction before
  revoking a principal or service principal, so the guard does not reopen a
  second pool read. A non-empty report returns an object-level 409 with no
  credential or SQL details. Semantic-attribute definitions, active dashboard
  roots, and non-tombstoned active agent conversations support transaction-
  bound transfer; semantic attributes intentionally have no tombstone because
  they remain live registry definitions. Agent conversations with an active
  run are left untouched and remain a transient fail-closed conflict until the
  run settles. Archived dashboard roots, archived conversations, deleted
  transcripts, publication event actors, delivery worker/lease IDs, release
  `created_by`, and connection/managed-data provenance are historical or
  infrastructure identities and are not treated as live principal ownership.
  The same guard protects the final usable platform administrator.
- `internal/access/snapshot.AuthorizationSnapshot` remains the effective
  access authority. Its explanation projection retains direct grants, group
  inheritance, owner-role evidence, and platform-admin evidence, and returns a
  fail-closed denied reason when no authority matches. Runtime wiring resolves
  current identity subjects/groups and derives explanations from the active
  immutable snapshot only.
- The generated access surface is wired through both the typed APIGen
  dispatcher and the compatibility module dispatcher: effective-capability
  listing supports resource filters, and batch authorization checks preserve
  allowed/denied evidence. HTTP tests cover direct/inherited evidence and
  denied explanations; domain tests cover owner evidence; PostgreSQL tests
  cover user/service-principal conflict, ownership transfer, and successful
  deletion after transfer.

Focused validation on 2026-09-17:

```text
go test ./internal/access ./internal/access/ownership ./internal/access/snapshot ./internal/access/http ./internal/access/module
go test ./internal/access/http -run 'Test(ListEffectiveCapabilities|CheckAuthorizationBatch)' -count=1
go test ./internal/access/postgres -run 'Test(SemanticAttributeOwnership|OwnershipAuthority)' -count=1
go test ./internal/access/ownership -count=1
go run ./internal/app/tools/sqlcaudit --root .
```

Remaining ownership decision: agent conversations with an active run remain a
transient, non-resolvable state and must be retried after the run settles;
mutating a live transcript during execution is not safe. Semantic attributes
support transfer only and deliberately do not support tombstone because their
definitions remain live registry state. Disabling a principal remains a
reversible emergency block, while destructive offboarding permanently revokes
the principal; owner-role capability semantics still need a product decision
beyond the explicit explanation flag. Effective-access review is available in
Access Settings; complete browser controls for role/grant assignment remain an
optional follow-up to the supported API and CLI workflows.

## FAI-941 lockout-recovery evidence — 2026-09-17

Status: **local-credential lockout recovery implemented and manually
qualified**. External IdP account/configuration recovery remains provider-owned.

The production-only command is `leapview admin access
recover-platform-admin`. Its CLI adapter requires an existing principal UUID,
expected email, stable operation UUID, explicit offline acknowledgement, and
an explicit expected revision for apply (`internal/admin/cli/command.go`). The
PostgreSQL adapter rejects non-production targets, requires the production TLS
control runtime, verifies the control baseline, canonicalizes UUIDs, confirms
the returned principal ID/email/kind and enabled state, and emits bounded JSON
evidence without connection URLs, passwords, tokens, or credential hashes
(`internal/app/adminpostgres/access_recovery.go`).

Preview is read-only and returns the current revision. Apply submits that exact
revision to `GrantPlatformAdmin`; concurrent delegation changes fail with a
stale-revision error. The operation UUID becomes the durable idempotency key,
and idempotent replay is allowed to reach the PostgreSQL writer before any
preview-time state comparison. The writer takes the platform-authority
advisory lock, performs CAS and operation replay checks, and the grant,
operation record, and `platform_admin.recovered` audit event share the one
`RunAuditedMutation` transaction (`internal/access/postgres/platform_admin.go`,
`internal/access/postgres/access_audit.go`).

Focused evidence:

```text
go test ./internal/admin/cli ./internal/app/adminpostgres
go test ./internal/access/postgres -run 'TestPlatformAdministrator' -count=1
```

Both suites pass. The app tests cover read-only preview, production gating,
identity mismatch and unexpected-principal rejection, stale preview revision,
canonical identity handling, expected-revision CAS dispatch, stable
idempotency key construction, atomic-audit callback wiring, and redacted
evidence. PostgreSQL tests cover delegation when administrators are absent,
replay, conflicting keys, stale revisions, disabled-principal rejection,
last-admin protection, and commit/rollback of delegation with audit.

For an existing enabled local principal, apply can replace the local password
from an owner-only file in the same transaction as role restoration, audit,
and session revocation. It does not create a principal, issue a token/session,
or alter an external identity provider. If no eligible local principal exists,
the command fails closed and the deployment's approved IdP recovery process is
required. See the [platform administrator lockout recovery
runbook](/docs/security/platform-admin-recovery).

The manual production-mode PostgreSQL drill removed the only platform binding
while retaining an authenticated non-admin session, then ran preview/apply
with both acknowledgements. Apply restored one binding and revoked all prior
sessions. The old session redirected to `session_expired`; the old password
was rejected; the replacement credential could sign in only to the mandatory
password-change flow; and the final credential restored `200` access to
administrator pages. Audit evidence recorded `platform_admin.recovered` with
`recoveryMode=offline_operator` and `localPasswordReset=true`, followed by
`password.changed`. Sanitized evidence is retained under
`.tmp/fai941-lockout-recovery/`.
