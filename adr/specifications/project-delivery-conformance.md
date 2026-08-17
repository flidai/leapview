# Project delivery conformance specification

Status: draft

Last updated: 2026-08-17

Owners: LeapView maintainers

Governing decisions: [ADR-0007](../0007-adopt-plan-driven-project-delivery.md),
[ADR-0008](../0008-isolate-ducklake-candidate-physical-state.md), and
[ADR-0009](../0009-separate-control-and-physical-transactions.md)

## Purpose

This mutable specification defines the evidence required to implement the
plan-driven delivery decisions. The ADRs own architectural intent. This file
may evolve with schemas, APIs, test organization, and operational tooling as
long as it preserves those decisions.

`MUST`, `MUST NOT`, `SHOULD`, and `MAY` have their ordinary normative meaning.
Tests may be organized differently from the identifiers below, but maintained
evidence must cover every requirement applicable to an implemented surface.

## Lifecycle and identity

- **LC-01:** CLI, API, browser, agent, CI, local evaluation, and hosted paths
  expose the same `Source snapshot -> Plan -> Candidate -> Generation`
  transition model.
- **LC-02:** Routine human commands are `plan -> build -> publish`; local
  validation is never represented as a target deployment plan.
- **LC-03:** Every plan binds target, environment, project, operation kind,
  exact portable source digest, active base generation, and base target
  revision.
- **LC-04:** Every ready candidate binds exactly one plan, source snapshot,
  resolved execution record, immutable DuckLake catalog digest and object key,
  physical pool, runtime identity, and qualification record.
- **LC-05:** Every generation binds exactly one published candidate and remains
  a complete immutable project graph.
- **LC-06:** Reusing an idempotency identity with different canonical inputs is
  a conflict. Retrying identical input returns the same durable result.
- **LC-07:** Secret values are absent from source snapshots, plans, manifests,
  artifacts, checkpoints, events, structured output, and logs covered by
  non-secret evidence contracts.

## Target revision and planning

- **TP-01:** Each target/project/environment scope has one authoritative
  monotonic target revision.
- **TP-02:** Active generation and every plan-invalidating target mutation
  update the revision in the same SQLite transaction.
- **TP-03:** Sessions, query leases, audit appends, secret rotations with
  unchanged declared execution semantics, and other non-invalidating changes do
  not increment the revision.
- **TP-04:** A publication CAS compares both base generation and base target
  revision and increments revision on success.
- **TP-05:** Concurrent publication, binding, managed-data, capability, or
  policy changes cannot be lost, overwritten, silently rebased, or reverted by
  stale publication.
- **TP-06:** Detailed target evidence identifies which component caused a
  revision change without becoming a second CAS authority.
- **TP-07:** Plan output deterministically reports direct and indirect graph
  impact, compatibility and policy impact, physical work, qualification, reuse,
  and defensible estimates.
- **TP-08:** Semantic impact and qualification cover the relationship paths,
  metric dependencies, filters, grain, and multi-root behavior governed by
  ADR-0006.
- **TP-09:** Expired, cross-target, cross-project, and source-mismatched plans
  fail closed.

## Execution, provenance, and governance

- **EP-01:** Plan persistence and APIs distinguish execution identity,
  provenance, and governance even when one record contains all three.
- **EP-02:** The execution digest changes for every result-affecting compiler,
  executable contract, dependency, runtime, capability, non-secret variable,
  semantic binding, managed-data revision, or declared data-input change.
- **EP-03:** Repository, source revision, builder, and attestation metadata do
  not change execution equivalence when portable bytes and all execution inputs
  remain identical.
- **EP-04:** Governance-only changes may require replanning, new candidate
  qualification, approval, or publication rejection without forcing physical
  rebuilding when execution equivalence still holds.
- **EP-05:** Credential secret rotation does not change execution identity when
  endpoint, database, catalog, schema, role, privileges, and other declared
  binding semantics are unchanged.
- **EP-06:** A change to effective binding or result-affecting policy semantics
  changes execution identity even when the secret reference name is unchanged.
- **EP-07:** Trusted-provenance policy verifies supported attestations rather
  than trusting client-supplied repository and revision strings.

## Data-input modes

- **DI-01:** Every planned data input is classified as `pinned`, `bounded`, or
  `observed` and plan output explains the mode.
- **DI-02:** A pinned input reads exactly its immutable dataset snapshot or
  managed-data revision; mismatch fails qualification.
- **DI-03:** A bounded input enforces the declared interval, as-of point, or
  upper watermark. Newer data outside the bound neither changes the result nor
  makes the plan stale.
- **DI-04:** An observed input records the exact build-time observation and is
  labeled as weaker reproducibility. It is rejected when target policy forbids
  observed inputs.
- **DI-05:** A planning-time observation or estimate is never represented as a
  pinned version.
- **DI-06:** Candidate execution evidence records the actual pinned version,
  enforced bound, or observed value used by every input.
- **DI-07:** Restatement records requested and effective intervals, input modes
  and versions, downstream scope, strategy, estimates, and idempotency evidence.

## Candidate qualification and stale-build policy

- **CQ-01:** One canonical successful build attempt seals at most one immutable
  candidate. A failed attempt never produces a ready candidate.
- **CQ-02:** Blocking validation or audit failure leaves active generation and
  the development session's last valid candidate unchanged.
- **CQ-03:** Non-blocking qualification and complete, bounded, or sampled data
  diffs are labeled explicitly.
- **CQ-04:** When target policy rejects stale builds, expensive physical work
  does not begin after the stale condition is observed.
- **CQ-05:** When target policy permits stale qualification, build proceeds only
  against a retained exact base and available declared inputs.
- **CQ-06:** A candidate built or becoming stale is permanently ineligible for
  publication under that plan. Staleness does not mutate its contents or create
  a candidate revision.
- **CQ-07:** Approval binds one exact candidate and plan digest and never carries
  forward to a replacement candidate or replan.
- **CQ-08:** Candidate preview applies live grants and data policies and cannot
  expose ungoverned physical storage.

## DuckLake catalog isolation and reuse

- **PI-01:** Each build uses one private writable DuckLake catalog created from
  an exact immutable sealed base or a recorded database-native consistent copy.
- **PI-02:** Two candidates built concurrently from the same base remain
  isolated when changing either the same or different logical tables.
- **PI-03:** A base and child that reuse physical data bind the same
  `PhysicalPool`; all catalogs sharing a pool are governed by one global
  retention authority and use one admitted DuckDB runtime, DuckLake extension,
  catalog format, storage implementation, and object-naming compatibility
  tuple. This shared-writer topology is a version-gated LeapView contract, not
  an upstream DuckLake guarantee.
- **PI-04:** A complete reuse-key match retains the exact base data and delete
  file references without creating replacement data, while every relevant
  execution mismatch rebuilds the affected state into new immutable objects.
- **PI-05:** Before seal, every inherited and intermediate snapshot is expired
  without physical cleanup; the sealed catalog contains exactly one retained
  current snapshot.
- **PI-06:** Preview, qualification, comparison, and serving attach the exact
  catalog digest read-only. Historical state is selected through another
  retained generation catalog, not internal catalog time travel.
- **PI-07:** Data inlining is disabled for every LeapView-managed
  materialization. Attach and process defaults are zero, every persisted global,
  schema, and table override is verified as effectively zero, existing inlined
  inserts and deletes are explicitly flushed table by table, and no live
  inlined data remains. Every current `data_file` and `delete_file` is then
  enumerable.
- **PI-08:** Candidate construction never byte-copies an open or mutating
  metadata database and never mutates a sealed artifact. Producing a fresh copy
  from a live or native database uses DuckLake's documented logical
  `COPY FROM DATABASE` path. Raw byte copying is limited to a closed immutable
  artifact under an admitted compatibility tuple and verifies its digest and
  read-only open.
- **PI-09:** Changed logic, upstream identity, pinned version, bounded interval,
  observed input, result-affecting policy or binding, materialization semantic,
  declared nondeterministic input, connector, adapter, executable contract, or
  runtime compatibility changes execution identity.
- **PI-10:** Provenance-only, approval-only, owner, and secret-rotation changes
  do not prevent reuse when execution identity is unchanged.
- **PI-11:** Undeclared nondeterminism disables reuse, and an observed source is
  reusable only with a stable connector-provided equivalence token accepted by
  target policy.
- **PI-12:** Candidate schemas, globally versioned relation names, and an
  authoritative SQLite physical-output ownership graph are absent; the sealed
  DuckLake catalog is the physical manifest.
- **PI-13:** Catalog artifacts, physical pool objects, and storage credentials
  remain inaccessible outside target-controlled authorization.
- **PI-14:** Every DuckDB runtime or DuckLake extension upgrade reruns
  concurrent same-table and different-table writes, new-object disjointness and
  collision checks, abort cleanup isolation, remote sealed reads, cross-catalog
  orphan classification, and global mark completeness before the new tuple may
  join a pool.

## Build, seal, and reconciliation

- **PR-01:** A durable SQLite build attempt binds canonical plan and execution
  inputs, exact base catalog, physical pool, and writer lease before physical
  work begins.
- **PR-02:** Build-local DuckLake transactions and intermediate snapshots are
  private and disposable. A pre-seal crash produces no candidate and may retry
  computation from the exact base without transaction-receipt reconciliation.
- **PR-03:** Snapshot normalization supplies an exact version set, preserves the
  latest snapshot, performs no physical deletion, and verifies the single-state
  postcondition. It also verifies effective inlining options at every persisted
  scope and that legacy inlined inserts and deletes have been flushed.
- **PR-04:** Qualification and seal preparation verify contracts, tests, audits,
  admitted runtime compatibility, absence of live inlined data, and the complete
  current data/delete-file closure.
- **PR-05:** The metadata database is detached and safely closed without using a
  DuckLake catalog-level checkpoint or another path that can invoke physical
  cleanup.
- **PR-06:** Before artifact upload, SQLite durably records the catalog digest,
  size, physical pool, content-addressed create-only key, canonical inputs, and
  verification evidence under a unique sealing identity.
- **PR-07:** Artifact creation is conditional and immutable. A lost upload
  acknowledgement is reconciled by reading the exact recorded key and verifying
  bytes, digest, size, and required metadata.
- **PR-08:** An existing object with mismatching bytes or evidence is corruption
  and is never overwritten or accepted for the seal.
- **PR-09:** SQLite marks a candidate ready only after remote read-only catalog
  attachment verifies the exact artifact and physical closure.
- **PR-10:** Reusing a sealing identity with different canonical inputs, digest,
  pool, or key is a conflict; retrying the same sealed identity converges on the
  same candidate.
- **PR-11:** A pre-seal attempt whose local artifact is lost may fail and be
  recomputed under a new attempt. Its unreferenced objects are eventually GC'd.
- **PR-12:** Refresh and restatement create a new private catalog and retain run,
  effective-input, strategy, seal, and qualification evidence through every
  crash boundary.

## Exact publication

- **PU-01:** Publication accepts only the exact ready,
  publication-eligible candidate and plan approved for the target.
- **PU-02:** Publication performs no source capture, source-ref resolution,
  compilation, materialization, qualification rerun, or candidate mutation.
- **PU-03:** Active generation and target revision compare-and-swap atomically;
  stale publication affects neither active state nor newer evidence.
- **PU-04:** Successful publication establishes the generation's exact catalog
  root in the same SQLite transaction as the active pointer.
- **PU-05:** A timeout or crash around activation leaves a durable indeterminate
  publication that resolves to the committed result or a proven non-commit
  before retry.
- **PU-06:** Publication reconciliation never activates a newly built candidate
  or cleans up the intended candidate while outcome is unknown.
- **PU-07:** Publication mutates no DuckLake table, snapshot, catalog artifact,
  or physical-pool object.

## Retention and garbage collection

- **RG-01:** Candidate TTL, quota, retirement, pull-request cleanup, orphan
  cleanup, and retention exceptions preserve every rooted catalog artifact and
  every object reachable from it.
- **RG-02:** SQLite is authoritative for the catalog root set; each verified
  DuckLake catalog is authoritative for its current data/delete-file membership.
- **RG-03:** Root creation, query-lease acquisition, and catalog retirement
  serialize through one durable fence: a winning root or lease prevents
  retirement, while winning retirement rejects new roots and leases.
- **RG-04:** Roots cover sealing or indeterminate seal records, ready
  candidates, prepared and active generations, pending or indeterminate
  publications, rollback windows, retention exceptions, and active candidate
  or generation query leases. Unsealed work is protected by writer fencing.
- **RG-05:** Every catalog sharing a physical pool is visible to the same global
  collector; independently governed catalogs never share a deletable namespace.
- **RG-06:** GC verifies each rooted artifact and marks every non-null
  `data_file` and `delete_file` for every current base table, plus the rooted
  catalog artifacts and declared pool metadata. Current-state enumeration is
  complete only for an admitted compatibility tuple after one-snapshot
  normalization and verification that no live inlined data remains.
- **RG-07:** The destructive phase excludes or epochs physical writers and
  protects in-flight namespaces and objects newer than the configured build,
  orphan, and reader grace periods.
- **RG-08:** Immediately before deletion, GC revalidates its pool epoch, root
  revision, query leases, writer fence, and candidate/publication state; a
  relevant change aborts or restarts deletion.
- **RG-09:** Every delete cycle has an exact bounded SQLite intent and reconciles
  ambiguous storage responses by verifying the intended keys and postcondition.
- **RG-10:** `ducklake_cleanup_old_files`,
  `ducklake_delete_orphaned_files`, explicit or externally scheduled cleanup,
  persisted maintenance defaults when invoked, and checkpoint-triggered cleanup
  are unreachable for catalogs in a shared pool.
- **RG-11:** Explicit pre-seal snapshot expiration may mutate only the private
  metadata catalog; all physical deletion remains global.
- **RG-12:** A multi-process deployment uses durable writer and GC leases,
  epochs, or equivalent control-store fencing rather than relying on a
  process-local mutex.
- **RG-13:** Crashed pre-seal builds may leave orphan objects, but those objects
  cannot become queryable and become collectible only after writer fencing and
  the grace period.

## Rollback, development, and promotion

- **DP-01:** Every generation declares and enforces `rollback_safe`,
  `serving_safe`, or `non_reversible` with a truthful retention window and
  external-effect description.
- **DP-02:** Rollback directly selects a retained qualified generation and
  appends a new audit event; it does not rebuild or imply reversal of undeclared
  external effects.
- **DP-03:** Development watch mode can update only private candidate and
  session-pointer state.
- **DP-04:** `plan` selects the target. Build and publish inherit it and reject a
  different destination assertion.
- **DP-05:** Destination promotion replans the same portable source bytes and
  creates a target-specific candidate rather than copying the source target's
  plan or candidate.
- **DP-06:** Successful development qualification is retained as provenance but
  cannot replace destination qualification.
- **DP-07:** Convenience commands emit durable plan and candidate identities and
  cannot bypass inspection, qualification, approval, concurrency, or evidence.

## Audit and operational evidence

- **AO-01:** Plan creation, build start and completion, qualification,
  publication request, approval or rejection, activation, rollback,
  restatement, retirement, and cleanup append immutable actor-, object-,
  digest-, version-, timestamp-, and result-bound events.
- **AO-02:** Runtime lineage and quality events are OpenLineage-compatible where
  lossless, while LeapView evidence remains authoritative.
- **AO-03:** Routine UX exposes plan impact, candidate qualification,
  publication eligibility, and required decisions without requiring users to
  restate target or provenance internals.
- **AO-04:** Inspection and audit surfaces expose immutable source, execution,
  provenance, governance, target revision, plan, candidate, catalog seal,
  publication, and generation evidence.
- **AO-05:** Operational repair tools verify SQLite state against DuckLake
  catalog digests, immutable object bytes, and actual file closure before
  changing protected state.

## End-to-end suites

Maintained end-to-end suites must exercise the same transitions for:

- a local evaluation target;
- an automated private-development or pull-request target; and
- a protected target requiring approval and trusted provenance.

Policy differences must be configuration rather than alternate lifecycle code
paths. Suites must include concurrent plan and publication changes, same-base
candidate builds, every build-and-seal crash boundary, lost upload
acknowledgements, root-versus-GC and lease-versus-retirement races, long queries
through publication and GC, rollback within and outside retention, cross-target
requalification, and pinned, bounded, and observed data inputs.

Generated CLI and API contracts, public workflow documentation, maintained CI
examples, and operational runbooks must agree with the implemented lifecycle.
