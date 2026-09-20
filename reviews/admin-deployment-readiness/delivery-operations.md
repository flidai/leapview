# Delivery operations readiness review

Internal implementation review.

## Decision

The native mutation path has good transaction, identity, approval, and fencing
boundaries, and SQL-bounded discovery now covers plans, builds, candidates,
approvals, publications, and retained generations. Fresh-home recovery logic
can select and execute a rollback from server-owned state. The exact
installed-candidate recovery drill and governed two-process data-plane
failover proof now pass. The operator snapshot intentionally reports degraded
where detailed lease/root authorities cannot supply evidence; it does not
claim a healthy zero value. The original findings remain below as discovery
history, followed by current implementation evidence.

## Scope and method

Reviewed release and delivery TypeSpec contracts, native PostgreSQL plan/build/
publication/approval/read authorities, catalog and managed-data discovery, admin
routes/settings, and recovery documentation/CLI. Reference material was treated
as untrusted data. The requested Flid discovery query returned no deployment
catalog entry. A separate `lightdash` discovery returned the upstream snapshot
below; no other relevant deployment product was returned.

Reference snapshot: Lightdash, revision
`35906ad9e116df59d2d59da2f58e45e9b39eaff8`, fetched
`2026-09-08T12:42:41Z`, available, catalog path
`/srv/flid/reference-library/current/references/lightdash`.

## Strengths

- The native plan request is intentionally caller-intent only: target-owned
  policy, bindings, qualification, and the CAS fence are resolved server-side
  ([`api/typespec/deployments.tsp:511-519`](../../../api/typespec/deployments.tsp#L511)).
  Planning performs source-attestation and artifact inspection before one
  transaction persists operation, plan, event, audit, and workflow consequences
  ([`internal/app/deploymentpostgres/native_create_plan.go:206-209`](../../../internal/app/deploymentpostgres/native_create_plan.go#L206)).
- Plan and physical build are separate authorities, composed only at the
  module boundary; neither can silently activate the other
  ([`internal/app/deploymentpostgres/native_delivery.go:20-38`](../../../internal/app/deploymentpostgres/native_delivery.go#L20)).
  The build path is deliberately fail-closed when exact base-snapshot reuse is
  not admitted ([`internal/app/deploymentpostgres/native_build.go:385-391`](../../../internal/app/deploymentpostgres/native_build.go#L385)).
- Approval is durable and publication-scoped. Request validation locks and
  rechecks publication/generation/target/policy identities
  ([`internal/deployment/postgres/approval_authority.go:280-324`](../../../internal/deployment/postgres/approval_authority.go#L280));
  decisions enforce separation of duty, compare-and-swap revision, and enqueue
  activation in the same transaction ([`.../approval_authority.go:435-489`](../../../internal/deployment/postgres/approval_authority.go#L435)).
- Publication/rollback creation checks target scope, retained-generation roots,
  and operation replay before committing a pending CAS request
  ([`internal/deployment/module/native_coordinator.go:250-360`](../../../internal/deployment/module/native_coordinator.go#L250)).
  Activation is queued after the request transaction, not performed inline
  ([`.../native_coordinator.go:844-878`](../../../internal/deployment/module/native_coordinator.go#L844)).
- Managed-data contracts do provide authenticated connection discovery and
  separate, paginated immutable revision/session APIs
  ([`api/typespec/managed_data.tsp:55-82`](../../../api/typespec/managed_data.tsp#L55),
  [`.../managed_data.tsp:282-319`](../../../api/typespec/managed_data.tsp#L282)).
  Recovery guidance correctly keeps serving stopped, forbids hand-editing rows,
  and delegates PostgreSQL/catalog/object-store restore to native provider
  mechanisms ([`docs/articles/operate/delivery-recovery.md:1-56`](../../../docs/articles/operate/delivery-recovery.md#L1)).

## Severity-ranked findings

### High — operator snapshot reports healthy without operator evidence

The public contract promises `degraded`, degraded reasons, physical pools,
roots, query leases, and writer leases
([`api/typespec/deployments.tsp:497-509`](../../../api/typespec/deployments.tsp#L497)).
The actual adapter initializes every detail collection empty and never assigns
`Degraded`, so the zero value is always `false`
([`internal/deployment/module/native_delivery_read_api.go:233-236`](../../../internal/deployment/module/native_delivery_read_api.go#L233)).
The PostgreSQL authority intentionally returns only target identity and active
generation/publication pointers ([`internal/deployment/postgres/repository.go:98-109`](../../../internal/deployment/postgres/repository.go#L98),
[`.../repository.go:1050-1063`](../../../internal/deployment/postgres/repository.go#L1050)); the module port explicitly excludes retention/lease projections
([`internal/deployment/module/persistence.go:301-320`](../../../internal/deployment/module/persistence.go#L301)).

This directly contradicts the recovery runbook, which tells an operator that
the same endpoint reports admitted pools, roots, leases, GC cycles, delete
intents, and degraded reasons ([`docs/articles/operate/delivery-recovery.md:7-20`](../../../docs/articles/operate/delivery-recovery.md#L7)).
An indeterminate build, expired writer lease, leaked root, or retention problem
can therefore look healthy and cannot be diagnosed or safely fenced from the
admin surface. This is a release blocker for production recovery.

### High — native delivery cannot be enumerated or audited as a lifecycle

The delivery interface exposes only create/mutate routes and point reads by
plan, build, seal, candidate, generation, publication, or approval ID
([`api/typespec/deployments.tsp:898-1127`](../../../api/typespec/deployments.tsp#L898)).
There is no list/search route for those objects, no delivery event-history route,
and no approval-decision history route. The native reader correspondingly has
only singular `Plan`, `BuildAttempt`, `SnapshotSeal`, `Candidate`, `Generation`,
`Publication`, and snapshot methods ([`internal/deployment/module/persistence.go:301-320`](../../../internal/deployment/module/persistence.go#L301)).

Operators cannot discover stuck builds, expired plans, rejected/indeterminate
publications, retained generations, or pending approvals without already
knowing an ID. The legacy release API has list/events, but that is a separate
resource family and does not enumerate native delivery objects.

### High — read models erase failure, recovery, and managed-input evidence

The contract defines failed/preparing/sealing/abandoned states and requires
failure codes, lifecycle timestamps, resolved-input evidence, publication
reason, and generation activation/retirement/rollback windows
([`api/typespec/deployments.tsp:12-47`](../../../api/typespec/deployments.tsp#L12),
[`.../deployments.tsp:324-444`](../../../api/typespec/deployments.tsp#L324)).
The adapter does not preserve those fields:

- every unqualified seal becomes `uploaded`, and both `CreatedAt` and
  `VerifiedAt` are derived from `QualifiedAt`; an unqualified or failed seal
  cannot be distinguished ([`internal/deployment/module/native_delivery_read_api.go:60-65`](../../../internal/deployment/module/native_delivery_read_api.go#L60),
  [`.../native_delivery_read_api.go:134-143`](../../../internal/deployment/module/native_delivery_read_api.go#L134));
- candidate `ResolvedInputs` is always an empty slice and no resolved-input
  digest or failure code is populated ([`.../native_delivery_read_api.go:145-158`](../../../internal/deployment/module/native_delivery_read_api.go#L145));
- generation response only sets `CreatedAt`, omitting activation, retirement,
  and rollback horizon; publication response omits `reason`
  ([`.../native_delivery_read_api.go:210-230`](../../../internal/deployment/module/native_delivery_read_api.go#L210)).

Build/recovery code does retain rich classification internally, but it is not
available through the status API. Incident responders cannot tell a retryable
indeterminate operation from a deterministic rejection or verify that the
managed-data inputs used by a candidate match the plan.

### Medium — release and native delivery have two incompatible authoring paths

The legacy release create body lets the caller choose `environment` and
`generationId` ([`api/typespec/releases.tsp:97-105`](../../../api/typespec/releases.tsp#L97)); the handler constructs that serving identity directly
([`internal/release/module/api.go:243-255`](../../../internal/release/module/api.go#L243)).
The service validates digest/provenance consistency, but durable serving-state
validation is deferred until finalization ([`internal/release/service.go:119-138`](../../../internal/release/service.go#L119),
[`.../service.go:242-269`](../../../internal/release/service.go#L242)). Native delivery instead requires a target-owned plan and server-resolved evidence. Both
surfaces coexist, while the legacy artifact uploader is explicitly unavailable
when no legacy artifact store is configured ([`internal/release/service.go:154-162`](../../../internal/release/service.go#L154)).

This makes it possible to create drafts that cannot progress and leaves clients
uncertain which lifecycle is authoritative. Production should either retire the
legacy route or make it a compatibility adapter that proves a durable target,
plan, and artifact binding at create time.

### Medium — “paginated” release and connection lists fetch unbounded data first

`ListReleases` loads every release, then runs in-memory keyset paging
([`internal/release/module/api.go:308-328`](../../../internal/release/module/api.go#L308),
[`internal/release/postgres/repository.go:353-369`](../../../internal/release/postgres/repository.go#L353)); the SQL query has no limit ([`internal/release/postgres/queries/release.sql:40-48`](../../../internal/release/postgres/queries/release.sql#L40)). It also performs a per-row reload. Managed connection discovery has the same fetch-all, authorize/filter, then page pattern ([`internal/release/module/catalog_api.go:48-77`](../../../internal/release/module/catalog_api.go#L48),
[`internal/release/module/postgres_catalog.go:125-142`](../../../internal/release/module/postgres_catalog.go#L125)). Large projects can exhaust request memory/latency before the requested page is returned, undermining object discovery during an incident.

### Medium — admin Settings has no delivery target or incident surface

Authenticated admin routes expose publications, storage, audit, system, and
identity/authentication settings, but no delivery operator/history/recovery
route ([`internal/admin/module/routes.go:68-82`](../../../internal/admin/module/routes.go#L68)). Bootstrap signal loading for Settings covers only profile, product identity,
authentication, and system ([`internal/admin/http/handler.go:481-503`](../../../internal/admin/http/handler.go#L481)). The product Settings UI describes deployment-managed authentication and shows runtime, storage, build, and limits, not target connections, active generation, leases, managed-data revisions, or recovery evidence ([`web/components/admin/product-settings.ts:194-253`](../../../web/components/admin/product-settings.ts#L194)).

The separate APIs and CLI are useful for developers/operators, but the admin
surface does not provide a discoverable path from an unhealthy instance to the
authoritative delivery and recovery evidence.

## Upstream comparison (benchmark, not authority)

The Flid catalog had no deployment-specific upstream result. The discovered
Lightdash material nevertheless supplies useful operational comparisons:

- backend validation repeats auth, references, capabilities, and immutable
  invariants rather than trusting client types
  (`/srv/flid/reference-library/current/references/lightdash/docs/content-as-code.md:123-145`);
- upgrade verification requires a deployment run/SHA, fails closed when merge
  or ancestry evidence cannot be read, requires three consecutive matching
  readiness/version polls, and writes a bounded summary
  (`/srv/flid/reference-library/current/references/lightdash/examples/upgrade-automation/scripts/verify.sh:7-14,34-57,110-190`);
- secret rotation retains an old/new overlap window so rollback is an ordering
  swap, not an unrecoverable cutover
  (`/srv/flid/reference-library/current/references/lightdash/docs/lightdash-secret-rotation.md:97-105,167-175`).

LeapView matches the first benchmark on native command validation and exceeds
it on transactional CAS/approval evidence, but does not yet expose the
operator evidence or bounded rollout/incident summary needed to operate those
guarantees.

## Deployment-readiness acceptance criteria

1. `GET .../delivery/operator` computes `degraded` from authoritative attempt,
   lease, root, retention, and target state; returns populated, redacted pools,
   roots, query/writer leases, GC/delete state, and explicit reasons. Add tests
   for expired leases, leaked roots, indeterminate builds, and a healthy target.
2. Add authorized, SQL-bounded collection/history APIs for plans, builds, seals,
   candidates, generations, publications, approvals/decisions, and delivery
   events. Support status/target/time filters, stable cursors, and incident
   export without credentials, object keys, or raw observations.
3. Preserve failure classification/code, seal creation/verification timestamps,
   candidate resolved-input digest/views, publication reason, and generation
   activated/retired/rollback-until fields in read models. Prove each terminal
   and indeterminate state with integration tests.
4. Select one authoritative release lifecycle. Native delivery must remain
   target-owned; any legacy release compatibility route must resolve and verify
   durable target/plan/artifact identity before accepting a runnable draft.
5. Add an admin delivery/recovery surface linking target identity, active
   generation, managed-data revisions/bindings, object history, readiness, and
   evidence export, while preserving current platform-admin and redaction rules.
6. Push pagination, authorization filtering, and N+1 elimination into the
   database/catalog authorities; test projects with at least 100,000 releases,
   connections, and revisions under bounded memory.
7. Define an incident run that captures readiness, image/config revision,
   target/plan/build/publication IDs, operator snapshot, logs, and provider
   backup/PITR evidence; require an explicit writer fence and retained-root
   proof before rollback or recovery publication. A verification gate should
   require repeated matching readiness/version observations and record the
   outcome, modeled on the Lightdash benchmark above.

## Commands and checks run

- Read the complete Flid skill instructions at
  `/srv/flid/reference-library/releases/9eaa1e4e2a35a33a79f6/skills/flid-reference-library/SKILL.md`.
- Ran `flid-ref discover "deployment release rollback approval operator snapshot managed data recovery incident workflow" --catalog /srv/flid/reference-library/current/catalog.json --json` (no results).
- Ran `flid-ref discover "lightdash" --catalog /srv/flid/reference-library/current/catalog.json --json` (metadata recorded above), then inspected the cited Lightdash documents/scripts.
- Used `rg` and `nl -ba` over the LeapView TypeSpec, deployment/release/admin/recovery sources and focused tests to verify the line evidence above.
- `git diff --check -- reviews/admin-deployment-readiness/delivery-operations.md` passed.
- `go test ./internal/admin/cli` passed (cached). The combined focused command
  `go test ./internal/deployment/module ./internal/deployment/postgres ./internal/release/module ./internal/admin/cli`
  was blocked during package setup because this worktree lacks generated
  packages (`internal/deployment/api/gen`, `internal/release/api/gen`, and
  generated sqlc packages); the admin/cli package still passed. The bounded
  collection implementation and its focused validation are recorded below.

## FAI-936 bounded collection implementation evidence

The native PostgreSQL authority now exposes deterministic, target-scoped
collection pages for plans, build attempts, candidates, approval requests,
publications, and retained generations. Each SQL query applies `LIMIT` before
the repository rehydrates the bounded page, orders by immutable creation time
plus UUID, and uses a canonical `k1.` cursor. Cursor reuse is rejected when
the row is outside the requested project/target/environment scope. Historical
plans, failed/indeterminate attempts, rejected/retired candidates, and expired
approval requests remain enumerable as lifecycle evidence; only expired
generation retention roots are excluded from rollback candidates.

The TypeSpec/API surface adds `GET` collection routes under the instance-bound
delivery target for `/plans`, `/builds`, `/candidates`, and
`/approval-requests`, alongside the existing publication and retained-
generation routes. APIGen dispatch adapters forward the typed `limit` and
`pageToken` parameters, and the module composes those adapters without
expanding the existing narrow reader test doubles. Approval collection
responses redact credential fields and retain the latest decision status;
expired requests are reported as `expired`.

Focused evidence:

- `go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate --no-remote` completed successfully.
- SQLC `vet --no-remote` completed successfully.
- `go test ./internal/deployment/postgres/internal/db ./internal/deployment/http` passed.
- `go test ./internal/deployment/http -run '^TestAPIGenDispatcherMapsDeliveryCollectionParams$' -count=1` passed.
- `go test ./internal/deployment/module -run 'TestNativeDelivery(Read|Candidate|Operator)' -count=1` passed.
- `go test ./internal/app/api ./internal/app/api/aggregate` passed after APIGen regeneration.
- Manual application E2E initially exposed three integration defects beyond
  repository/module tests: collection operations fell through to the
  object-scoped authorizer (`403`), the application-owned native reader omitted
  four collection forwards (`503`), and the API protocol erased unrecognized
  `k1.` continuation cursors. Focused authorization, composition, and cursor
  protocol regressions now cover each seam.
- The rebuilt live instance returned `200` for plans, builds, candidates,
  approval requests, publications, and generations. A `k1.` publication cursor
  continued to the next page; malformed and cross-collection cursor reuse was
  rejected with `400` and `422` respectively.

## FAI-937 fresh-machine recovery qualification evidence

The composectl interruption-recovery qualification now includes a bounded
fresh-operator drill after the interrupted deployment has recovered. The drill
creates a new `fresh-operator-home` with mode `0700`, asserts that it contains
no checkpoint files, and uses only the authorized generated delivery client to
read server-owned PostgreSQL projections. It does not read access or
credential files, admin CLI recovery files, or local deployment checkpoints.
The orchestration is wired into the recovery phase at
[`qualification_recovery.go:564-589`](../../../internal/app/cli/composectl/qualification_recovery.go#L564),
and the empty-home, collection, and rollback assertions live in
[`qualification_recovery_discovery.go:85-223`](../../../internal/app/cli/composectl/qualification_recovery_discovery.go#L85).

With one request per bounded page (`limit=100`), the drill discovers the
operator snapshot plus plans, build attempts, candidates, approval requests,
publications, and retained generations. It proves the active generation is
present, identifies a distinct retired `rollback_safe` prior generation with a
live rollback window, and cross-checks the recovered candidate's plan, sealed
build, committed publication, and approved request identities before choosing
the rollback target.

The drill then submits the typed rollback command against that selected
generation, waits for the exact publication to commit, and waits for the
PostgreSQL-backed operator snapshot to report the selected generation active.
The redacted read projections and before/after operator snapshots are written
to `recovery-delivery-discovery.json`; the report assertion is
`freshOperatorRollback=true`. This is the smallest automated proof that an
operator arriving without checkpoint files can discover and select rollback
from durable product state.

Focused evidence:

- `go test ./internal/app/cli/composectl -count=1` passed, including
  `TestQualificationFreshOperatorStartsWithoutCheckpointFiles`,
  `TestSelectQualificationRollbackGenerationUsesRetainedPriorOnly`, and
  `TestQualifyFreshOperatorEvidenceLinksBuildApprovalAndPublication`, plus
  the generated-client HTTP drill
  `TestRunQualificationFreshOperatorDeliveryRecoveryDrillsCollectionsAndRollback`.
- `git diff --check -- internal/app/cli/composectl/qualification_recovery.go internal/app/cli/composectl/qualification_installed.go` passed.
- The exact Docker-backed `qualify installed-candidate` run passed on
  2026-09-18 for image digest
  `sha256:57368b49bc7a9bb2a6e1df42b2cc830faa7af1f4552a9cdca9e34dff55084bd5`
  in 505 seconds. All nine phases and all report assertions passed.
- The two-node phase mounts the shared physical-pool, extension-cache, and
  relevant object-store/managed-data subpaths while keeping private process
  state. It executes a real governed query through the secondary at startup,
  after abrupt primary loss, after secondary restart, and through the restored
  primary after rolling restart. The report records `dataPlaneQueries=true`
  alongside abrupt-loss, recovery, restart, and durable-convergence evidence.
- Clean synthetic snapshot
  `cc5c9692ac22861642a2ed362fe66be7df593504` passed the complete
  `task ci:full` contract from a detached worktree after the periodic
  expired-refresh rescue statement was moved onto the generated platform-jobs
  sqlc surface. The final exact-snapshot UI gate passed all 13 routes and 12
  visual baselines.
