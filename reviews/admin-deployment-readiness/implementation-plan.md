# Administrator-managed deployment readiness plan

Internal execution plan; not part of the public documentation catalog.

Date: 2026-09-18; updated 2026-09-20

Status: **Integrated; exact-candidate release qualification pending**

Source review: [Administrator and deployment readiness review](../admin-deployment-readiness.md)

Linear project: [LeapView administrator-managed deployment readiness](https://linear.app/flid/project/leapview-administrator-managed-deployment-readiness-a2512f4b2475)

## Current integration gate — 2026-09-20

The prior closure below records the earlier standalone PR candidate. PR #659
has now been integrated with `origin/main` while preserving main's credential
expiry defaults, Settings refinements, and partition-fenced refresh recovery.
The PR migrations follow main's published revisions 020–022 at 023–027, and
the production control plane and relevant tests use PostgreSQL rather than
retired SQLite adapters. Focused PostgreSQL tests cover the ownership race,
OIDC freshness, credential-aware authorization, recovery replay, and migration
replay/upgrade. A final security pass found an additional REST platform-role
PAT bypass; its recent-interactive-auth guard now fails closed for PATs and
missing freshness wiring, with generated REST regression tests. FAI-934,
FAI-939, FAI-935, FAI-941, FAI-954,
and FAI-955 need final verification/status updates; FAI-943 remains open until
the integrated exact candidate passes `task ci:full`, generated checks,
browser/API journeys, and installed-candidate Docker/Compose qualification.
FAI-997 covers explicit, request-attenuated PAT issuance and atomic rotation;
FAI-998 covers audited, all-class administrator incident revocation. Both are
implemented and await the same exact-candidate gate. Session-only bulk
revocation is now set-based, so its separate Settings action covers more than
a single inventory page.

## Linear delivery breakdown

The umbrella workstreams remain FAI-927 through FAI-931. Execution is split
into the following child issues so estimates, dependencies, and acceptance
criteria can be scheduled independently:

| Issue | Deliverable | Classification | Estimate | Parent |
| --- | --- | --- | --- | --- |
| [FAI-932](https://linear.app/flid/issue/FAI-932/close-service-account-authorization-and-credential-containment) | Service-account authorization, suspension, rotation, and revoke-all | Release blocker | 6–10 days | FAI-927 |
| [FAI-933](https://linear.app/flid/issue/FAI-933/expose-service-secret-last-used-evidence) | Service-secret last-used evidence | Release blocker | 2–4 days | FAI-927 |
| [FAI-934](https://linear.app/flid/issue/FAI-934/implement-ownership-safe-principal-offboarding) | Ownership-safe principal offboarding | Release blocker | 6–10 days | FAI-928 |
| [FAI-935](https://linear.app/flid/issue/FAI-935/explain-effective-and-inherited-access) | Effective and inherited access explanation | Release blocker | 4–7 days | FAI-928 |
| [FAI-936](https://linear.app/flid/issue/FAI-936/add-plan-build-candidate-and-approval-discovery-apis) | Plan, build, candidate, and approval discovery | Release blocker | 5–8 days | FAI-930 |
| [FAI-937](https://linear.app/flid/issue/FAI-937/qualify-fresh-machine-deployment-recovery) | Fresh-machine deployment recovery | Release blocker | 2–4 days plus discovered defects | FAI-930 |
| [FAI-938](https://linear.app/flid/issue/FAI-938/add-platform-administrator-settings-controls) | Platform-administrator Settings controls | Optional if secure API/runbook is complete | 2–3 days | FAI-931 |
| [FAI-939](https://linear.app/flid/issue/FAI-939/require-recent-authentication-for-platform-role-changes) | Recent authentication for platform-role changes | Required before browser mutation | 3–5 days | FAI-929 |
| [FAI-940](https://linear.app/flid/issue/FAI-940/design-optional-second-admin-approval-for-platform-role-changes) | Optional second-administrator approval | Recommended hardening | 5–8 days | FAI-929 |
| [FAI-941](https://linear.app/flid/issue/FAI-941/implement-and-drill-platform-administrator-lockout-recovery) | Platform-administrator lockout recovery | Release blocker | 4–7 days | FAI-929 |
| [FAI-942](https://linear.app/flid/issue/FAI-942/refine-access-and-delivery-administrator-ux) | Access and Delivery UX refinement | Nice to have | 3–5 days | FAI-931 |
| [FAI-943](https://linear.app/flid/issue/FAI-943/run-final-task-cifull-deployment-readiness-qualification) | Final `task ci:full` qualification | Release blocker | 1 day if green; 2–5 days defect reserve | FAI-929 |
| [FAI-997](https://linear.app/flid/issue/FAI-997/close-personal-api-token-rotation-and-issuance-gaps) | PAT issuance attenuation, explicit scope, atomic rotation, and observable touch failures | Release blocker | 2–4 days | FAI-927 |
| [FAI-998](https://linear.app/flid/issue/FAI-998/add-all-class-administrator-incident-revocation) | All-class, transactional administrator incident revocation | Release blocker | 2–4 days | FAI-927 |

The planned execution order was FAI-932/933, FAI-934/935, and FAI-936 in
parallel; then FAI-939/941 and FAI-937; finally FAI-943. The optional FAI-938,
FAI-940, and FAI-942 hardening was subsequently completed rather than carried
as post-release work.

## Current closure — 2026-09-18

| Issue | Status | Remaining boundary |
| --- | --- | --- |
| FAI-932 | Complete | Service-principal authorization uses the existing Access role-binding workflow; lifecycle, rotation, disable/enable, revoke-all, PostgreSQL restart behavior, and browser/API E2E pass. |
| FAI-933 | Complete | Metadata-only last-used evidence persists, is coalesced, survives restart, and was observed after a live OAuth exchange. |
| FAI-934 | Complete | Transaction-bound inventory and deletion guards cover semantic attributes, dashboard roots, and agent conversations. Transfer/tombstone retries are idempotent; active agent runs fail closed until cancellation. Live PostgreSQL/API E2E passed. |
| FAI-935 | Complete | API provenance and the server-bound Access Settings explanation surface distinguish platform, direct, group-derived, owner, compiled, and denied authority with bounded loading/error states. |
| FAI-936 | Complete after E2E remediation | All six collections are SQL-bounded and cursor-scoped. Manual E2E exposed and then verified the collection authorization binding. |
| FAI-937 | Complete | Exact installed-candidate qualification passed all nine phases in 505 seconds, including fresh-home discovery/rollback, interruption recovery, restart persistence, and two-node data-plane failover. |
| FAI-938 | Complete | Settings lists current/revoked platform authority without credentials and drives the CAS/idempotent grant/revoke authority. |
| FAI-939 | Complete | Browser mutations require a server-verified interactive session authenticated within 15 minutes; token and authoring credentials fail closed. |
| FAI-940 | Complete | Optional deployment-controlled two-person approval is durable, restart-safe, expiring, cancelable, separation-of-duty enforced, CAS-fenced, and transactionally audited. |
| FAI-941 | Complete | The production-only recovery command can replace an existing local credential, revoke sessions, restore one platform administrator, require password change, and audit the transaction. The manual PostgreSQL lockout drill passed; external IdP recovery remains provider-owned. |
| FAI-942 | Complete | Access and Delivery now expose understandable authority/state labels, technical IDs on demand, explicit degraded/stale/unauthorized states, destructive scope, keyboard-safe controls, and durable outcome feedback. |
| FAI-943 | Complete | A detached clean synthetic checkout passed `task ci:full`, including deterministic generation, PostgreSQL 18 conformance, SQL audit, browser, desktop, route, race/static, and deployment validation lanes. |

## Objective

Make the PostgreSQL production composition safely operable by administrators
without database edits, undocumented IDs, or access that cannot be revoked.
Every public API operation must dispatch, every privileged identity must have a
closed credential and authorization lifecycle, and every deployment must be
discoverable and recoverable from server-owned state.

The current mainline has retired SQLite adapters. PostgreSQL is the only
deployment authority and integration-test target; the earlier SQLite findings
remain historical evidence for why the credential lifecycle was hardened.

## Delivery principles

- PostgreSQL is the only deployment authority and integration-test target.
- TypeSpec, generated OpenAPI, runtime dispatch, and Settings must describe the
  same supported lifecycle.
- Mutations remain project/target scoped, idempotent where retryable, guarded by
  compare-and-swap where state can race, and transactionally audited.
- Secret material is returned once; inventories expose metadata and use
  evidence only.
- A disabled or revoked identity never regains authority without an explicit
  administrator action.
- Deployment recovery must work from a fresh operator machine without local
  checkpoint files.

## Workstreams

### WS1 — Credential lifecycle correctness

Scope:

1. Define shared finite defaults and maximums for personal API tokens and
   service-principal secrets.
2. Apply defaults before persistence so omitted `expiresAt` succeeds against
   PostgreSQL and every response contains the resolved expiry.
3. Add a Settings expiry choice for service-principal secrets.
4. Add service-secret last-used evidence and observable best-effort touch
   failures.
5. Add overlapping rotation plus explicit individual revocation.
6. Add an audited “revoke all credentials” administrator operation.
7. Make explicit capability allowlists the normal PAT flow; isolate and warn on
   dynamic authority.

Acceptance:

- PostgreSQL tests cover omitted, past, maximum, and over-maximum expiry.
- Old material fails after revocation/rotation and after disable/re-enable.
- Credential inventories expose owner, class, scope mode, creation, expiry,
  last use, and revocation without returning secret material.
- An incident drill leaves no browser, desktop, authoring, OAuth, PAT, or
  service-secret credential usable.

### WS2 — Authorization API and offboarding closure

Scope:

1. Implement and dispatch advertised project-role listing and grant
   list/create/get/update/delete operations.
2. Add OpenAPI-operation-to-runtime-handler conformance coverage.
3. Add role-binding removal with target revision CAS, idempotency, and audit.
4. Add platform-admin list/grant/revoke with last-admin protection.
5. Add ownership inspection and transfer/tombstone handling before principal or
   service-principal deletion.
6. Make direct, group-derived, owner, and platform authority distinguishable in
   API responses.
7. Resolve or explicitly document the non-monotonic role taxonomy.

Acceptance:

- Every advertised operation reaches a composed PostgreSQL handler.
- Mixed direct/group authorization can be completely removed and does not
  reappear after principal re-enable.
- No disable, delete, or role-revoke path can leave zero usable platform
  administrators.
- Owned objects produce the documented conflict until transferred or
  intentionally tombstoned.

### WS3 — Delivery discovery and recovery evidence

Scope:

1. Add bounded, stable pagination for plans, build attempts, candidates,
   publications, approvals, and retained generations.
2. Preserve seal failure/state/timestamps, candidate resolved-input evidence,
   publication reason, and generation activation/retirement/rollback windows in
   public read models.
3. Either populate the complete operator snapshot from owning authorities or
   narrow its public contract and runbook to evidence it can truthfully supply.
4. Declare native target-owned delivery authoritative and retire or adapt the
   legacy release lifecycle.
5. Push release and connection pagination into their owning stores.

Acceptance:

- A fresh administrator can find the active and prior generations, identify
  pending approvals, diagnose a failed build, and select a rollback target.
- Every terminal and indeterminate lifecycle state has an integration fixture.
- Operator health never reports a healthy zero value when evidence is absent.
- Recovery and rollback runbooks pass against the production composition.

### WS4 — Administrator Settings closure

Scope:

1. Add a single-project Access surface for role bindings, grants, effective
   access explanation, semantic attributes, and offboarding.
2. Add a Delivery surface for history, approvals, generation evidence, and
   rollback.
3. Add the complete service-account lifecycle: create, rename, authorize,
   expire, rotate, disable, and revoke.
4. Remove or implement false affordances: profile title/username, audit project
   and date filters, service-account rename, and the dead projects component.
5. Keep IdP-, storage-, and infrastructure-managed settings explicitly
   read-only and link them to operator runbooks.

Acceptance:

- Every visible control has a browser test proving its durable effect.
- Settings and API expose the same administrator lifecycle, with inherited
  access clearly separated from directly revocable access.

### WS5 — Qualification and release gate

Scope:

1. Add route/contract/browser/PostgreSQL journey coverage for each workstream.
2. Update security and recovery documentation only after supported behavior is
   composed.
3. Run focused suites during development, `task ci` for each integrated wave,
   and `task ci:full` before changing the readiness decision.

Release gate:

- WS1–WS4 acceptance criteria pass on PostgreSQL.
- Generated artifacts are current.
- No public operation is undispatched and no Settings control is inert.
- Fresh-machine deployment recovery and administrator lockout drills pass.

## Dependency order

1. WS1 expiry correctness and WS2 advertised API dispatch are immediate
   correctness blockers and can proceed independently.
2. WS3 read-model fidelity precedes delivery lists and the Settings Delivery
   surface.
3. WS2 mutation closure precedes the Settings Access surface.
4. WS1 service-secret metadata/rotation precedes the Settings service-account
   workflow.
5. WS5 gates each merge wave and closes the project.

## Active implementation wave

This first wave deliberately avoids overlapping file ownership:

| Track | Deliverable | Primary areas |
| --- | --- | --- |
| A | Resolve omitted PAT/service-secret expiry for PostgreSQL, including focused contract tests | `internal/access`, access HTTP tests |
| B | Implement advertised project-role and grant runtime dispatch with conformance tests | `internal/access/http`, `internal/access/module` |
| C | Preserve native delivery lifecycle/recovery fields in supported read responses and tests | `internal/deployment/module` |

The coordinating agent owns integration, generation decisions, documentation
status, and cross-track validation. Subsequent waves begin only after this wave
is reviewed and its dependencies are stable.

## Progress log

- 2026-09-18: Closed the release gate. Exact installed-candidate image digest
  `sha256:57368b49bc7a9bb2a6e1df42b2cc830faa7af1f4552a9cdca9e34dff55084bd5`
  passed the 505-second Docker/Compose qualification with all assertions true.
  Multi-node qualification shares the physical pool, extension cache, and
  object-store subpaths while retaining private process homes, and executes
  governed data-plane queries through the secondary across abrupt loss and
  rolling restart. Final detached clean snapshot
  `2dc0aeb3dd6d634391362de583146e8367c992f9` passed the complete
  `task ci:full` contract. Its final browser rerun covered all 13 routes,
  accessibility, filter/map interactions, and all 12 visual baselines.
- 2026-09-18: The final quality-budget correction was structural rather than a
  policy increase: snapshot-seal and rollback-window reads moved out of the
  oversized deployment repository, while expired River-job recovery and its
  integration tests moved into focused refresh files. Production and test
  budgets, exception/trend checks, critical coverage, dead-export, static/race,
  deployment, MinIO, plan-GC, and PostgreSQL multi-node gates all pass.
- 2026-09-18: Personal token issuance now requires explicit capabilities;
  dynamic bearer credentials cannot inherit platform administration. TypeSpec
  declares platform-admin authorization explicitly, and a real revision
  21-to-23 PostgreSQL migration/replay test covers native delivery evidence.

- 2026-09-16: Readiness review completed and SQLite fixture scope corrected.
- 2026-09-16: Plan created; first implementation wave started.
- 2026-09-16: Credential expiry policy completed for PATs and service secrets;
  PostgreSQL now receives resolved finite expiries, and Settings exposes
  30/90/180/365-day service-secret choices.
- 2026-09-16: Project-role catalog dispatch completed. Review determined that
  PostgreSQL grant rows are immutable generation evidence, not a safe mutable
  administrator authority; unsupported grant CRUD was subsequently removed
  from the public contract rather than given false-success handlers.
- 2026-09-16: Delivery read models began preserving retained failure/lifecycle
  evidence. Seal creation time, resolved inputs, and publication reason were
  identified as schema-owner gaps and were closed in the final implementation
  wave.
- 2026-09-16: Operator snapshot contract and recovery documentation narrowed to
  native-owned target/active-pointer evidence; the response is degraded while
  detailed evidence authorities are unavailable.
- 2026-09-16: Audit Settings filters now bind to the active server project and
  apply actor/action/resource/from/to filters durably.
- 2026-09-16: Role-binding deletion implemented as an audited, idempotent,
  CAS-protected immutable successor policy revision; final project-admin safety
  is part of the integration check.
- 2026-09-17: Access Settings now renders active-generation effective-access
  provenance for the authenticated administrator. Direct, group-derived,
  owner, platform, compiled, and denied reasons are explicit in the typed
  signal; provider failures remain bounded in the surface. Go and Chromium
  browser contracts plus all three frontend typechecks pass.
- 2026-09-16: Integrated validation passed focused access, administrator,
  deployment, browser, PostgreSQL 18 conformance, route-inventory, SQL
  generation/audit, APIGen, engineering-quality-budget, and all five frontend
  CI shards. The repository-wide `task ci` reached and passed every functional
  Go/PostgreSQL lane before its initial quality-budget failure; cohesive read
  and credential-issuance helpers were then extracted from oversized files,
  after which focused tests and the quality budget passed. `task
  generated:check` regenerates cleanly but, as designed for a clean checkout,
  reports the intentionally modified uncommitted `docs/api/openapi.yaml`
  snapshot.
- 2026-09-16: Manual E2E ran against a fresh PostgreSQL 18 development
  deployment through Chromium and the public API. PAT expiry, role
  list/create/delete/replay, final-admin protection, audit action/time filters,
  and delivery detail reads passed. The release gate remains closed: service
  principal create, secret revoke, and principal delete commit but return
  `500 COMMAND_CONTRACT_NOT_EXECUTED`; role-binding audit rows have no project
  ID; the service-secret browser selector renders 30 rather than the declared
  180-day default; grant/deployment enumeration and Access/Delivery Settings
  surfaces remain absent.
- 2026-09-16: Remediation wave completed. Service-principal and principal
  lifecycle mutations now complete their generated command contracts;
  role-binding audit carries project scope; the service-secret selector
  defaults to 180 days; unsupported mutable grant CRUD was removed from the
  public contract; and platform-admin list/grant/revoke now has transactional
  audit, CAS/idempotency, last-admin safety, and an in-place PostgreSQL
  migration.
- 2026-09-16: Added bounded delivery publication and retained-generation APIs,
  a composed delivery reader, and Access/Delivery Settings pages. The second
  PostgreSQL/Chromium pass verified the supported journeys and caught the
  migration and reader-adapter gaps before handoff. The release gate remains
  open for service-account closure, ownership-safe offboarding, the remaining
  delivery collections, recovery drills, and full CI.
- 2026-09-16: Mirrored the workstreams into Linear as FAI-927 through FAI-931
  under the linked deployment-readiness project.
- 2026-09-16: Final remediation verification passed the focused
  access/application/PostgreSQL tests, Settings browser contract, TypeScript
  typechecks, architecture rules, critical coverage, quality budget,
  exception policy, and dead-export scan. The bounded `task ci` functional and
  PostgreSQL lanes are green. The aggregate clean-checkout generated-snapshot
  assertion is not claimed against the intentionally regenerated, uncommitted
  OpenAPI file, and `task ci:full` plus the fresh-machine recovery drill remain
  required before changing the project readiness decision.
- 2026-09-17: Service-account containment and last-used evidence were completed
  and qualified on PostgreSQL 18. Live API E2E proved create, one-time secret
  issuance, OAuth exchange, last-used persistence, disable/re-enable permanent
  revocation, overlap rotation, revoke-all, and deletion; Playwright verified
  the corresponding Settings controls.
- 2026-09-17: Added transactional ownership inventory/deletion guards and
  effective-access provenance. This initial pass identified the dashboard,
  agent, and administrator-surface work subsequently closed by FAI-934 and
  FAI-935 below.
- 2026-09-17: Added SQL-bounded plan, build, candidate, approval, publication,
  and generation collections with scope-bound cursors. Manual E2E exposed a
  403 authorization integration defect on collection routes; the project-root
  authorization path and regression coverage were corrected before closure.
- 2026-09-17: Added fresh-home recovery discovery/rollback automation and an
  offline, production-only, CAS/idempotent, transactionally audited platform
  role recovery command. The local-password lockout drill subsequently closed
  FAI-941; the installed-candidate drill remained the FAI-937 release gate.
- 2026-09-17: `task ci` passed generation, database verification, application
  packages, TypeSpec, the pinned PostgreSQL 18 conformance inventory, coverage,
  and the quality budget. Its last check identified one unused exported
  explanation helper; the helper was removed and the focused package, quality,
  diff, and dead-export checks pass. `task ci:full` is still open.
- 2026-09-17: Closed FAI-934 with transaction-bound transfer for
  semantic-attribute definitions, dashboard roots, and agent conversations,
  plus agent tombstoning and fail-closed active-run handling. Live PostgreSQL
  E2E proved conflict, transfer, retry, tombstone, deletion, and durable
  attribution behavior.
- 2026-09-17: Closed FAI-941 with a manual production-mode PostgreSQL lockout
  drill. Offline recovery restored the role and local credential, revoked the
  prior session, rejected the old password, required a password change, and
  restored browser administrator access with audited evidence.
