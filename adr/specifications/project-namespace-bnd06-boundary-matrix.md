# ADR-0018 BND-06 boundary matrix

Status: maintained qualification evidence

Repository baseline: `92ff6cdb51d51d3e03eeb21adcd1cdac047c9b8f`

Normative requirement: [BND-06](project-namespace-conformance.md)

BND-06 requires Project identity at every durable or runtime boundary where
omission could permit collision, retargeting, disclosure, or incorrect garbage
collection. This matrix records the authoritative source, the enforcing
persistence or constraint, and a maintained test for every named boundary.

| Boundary | Authoritative identity source | Persistence or constraint | Maintained proof |
| --- | --- | --- | --- |
| Plan | The issuer-owned `ProjectClaim` resolves the exact `(target, Project, environment)` tuple before planning. | `delivery_target` owns immutable `project_id` and `environment`; `delivery_plan.target_id` references that target and plan identity is immutable. | `TestCreatePlanRejectsRichDocumentOutsideTargetScope` in [`internal/deployment/postgres/plan_document_test.go`](../../internal/deployment/postgres/plan_document_test.go) and `TestNativeCandidateSourcePlanRequiresProjectClaimAuthority` in [`internal/deployment/module/candidate_source_plan_test.go`](../../internal/deployment/module/candidate_source_plan_test.go). |
| Candidate | The admitted plan's target is the candidate scope; a request cannot independently relabel it. | `delivery_candidate` has composite `(plan_id, target_id)` and immutable candidate-identity constraints. | `TestPostgresFreshTargetPlanAllocationAndCandidateConcurrency` in [`internal/deployment/postgres/revision_allocation_test.go`](../../internal/deployment/postgres/revision_allocation_test.go). |
| Generation | The exact candidate, plan, target, and snapshot seal determine generation scope. | `delivery_generation` has composite foreign keys to candidate/plan/seal identity, an immutable generation row, and monotonically allocated target-local generation revision. | `TestPostgresGenerationRevisionAllocationReplayRollbackAndConcurrency` in [`internal/deployment/postgres/revision_allocation_test.go`](../../internal/deployment/postgres/revision_allocation_test.go) and `TestPostgresDeliveryAuthorityLifecycleAndReplay` in [`internal/deployment/postgres/repository_authority_lifecycle_test.go`](../../internal/deployment/postgres/repository_authority_lifecycle_test.go). |
| Serving | The admitted delivery generation and its target supply Project and environment; the serving bundle cannot author them. | `serving_state.bundle` persists `(generation_id, project_id, environment)` and its admission trigger cross-checks canonical delivery target/generation identity. Reader leases reference the admitted generation and exact DuckLake snapshot. | `TestAdmitGenerationBundleAndActiveRead` and `TestReaderLeaseBindsSnapshotAndDBClock` in [`internal/servingstate/postgres/repository_test.go`](../../internal/servingstate/postgres/repository_test.go). |
| Query | The active Project runtime supplies Project identity; the request must match it, and execution is pinned to the serving generation/candidate namespace. | Runtime admission rejects empty or mismatched Project scope. Durable query audit rows require `project_id`, and Project-qualified repository lookups prevent an unqualified read. | `TestProjectRuntimeBindsEveryQueryToItsProject` and `TestProjectRuntimeRejectsEmptyOrMismatchedProjectQueries` in [`internal/analytics/duckdb/project_identity_test.go`](../../internal/analytics/duckdb/project_identity_test.go), plus `TestRepositoryExactReplayConflictAndBounds` in [`internal/analytics/queryaudit/postgres/repository_test.go`](../../internal/analytics/queryaudit/postgres/repository_test.go). |
| Cache | Canonical runtime partitions derive Project, target, environment, candidate, generation, and policy identities from admitted runtime state. | Cache constructors reject missing scope; typed partition and generation execution keys prevent cross-Project/candidate collision, while policy revisions guard authorization reuse. | `TestProjectBoundaryCacheKeysDoNotCollide` and `TestProjectBoundaryCacheRejectsMissingScope` in [`internal/analytics/cache/project_boundary_test.go`](../../internal/analytics/cache/project_boundary_test.go), and `TestBundleFlightsAreIsolatedByGenerationExecutionScope` in [`internal/analytics/materialize/query_cache_runtime_test.go`](../../internal/analytics/materialize/query_cache_runtime_test.go). |
| Lineage | The admitted generation publishes its canonical Project and graph digest; callers cannot substitute another Project. | Lineage graphs, revisions, nodes, edges, and generation bindings use Project-qualified composite primary/foreign keys. Traversal queries require the same `project_id`. | `TestRevisionPublicationReplacementRollbackAndProjectIsolation` and `TestRuntimePublicationRoleBoundaryAndCompositeProjectFK` in [`internal/lineage/postgres/repository_test.go`](../../internal/lineage/postgres/repository_test.go). |
| Audit | Project-scoped producers use the authoritative server Project; only genuine platform events may omit it. | Access audit persists nullable `project_id` with Project-filtered reads; query audit requires non-null Project identity. Audit mutation and record commits are atomic at their owning boundary. | `TestListAuditEventsBindsProjectAndPreservesProjectIdentity` and `TestListAuditEventsRejectsForeignProjectBeforeRepositoryRead` in [`internal/access/http/audit_test.go`](../../internal/access/http/audit_test.go), plus `TestAccessRemainingPostgreSQL18AuditFiltersCursorAndBootstrapEvidence` in [`internal/access/postgres/access_remaining_test.go`](../../internal/access/postgres/access_remaining_test.go). |
| Managed data | Collection and admitted generation state supply `(Project, environment, revision)`; a retention caller cannot relabel the revision. | Managed-data collections and retention roots persist Project-qualified identity. Root state is capability-controlled, and the PostgreSQL reachability epoch fences GC against concurrent lifecycle changes. | `TestBND06CrossProjectManagedBlobGCProtectsSharedContent` in [`internal/manageddata/postgres/bnd06_gc_qualification_test.go`](../../internal/manageddata/postgres/bnd06_gc_qualification_test.go). |
| Physical retention roots | The delivery root's target resolves Project/environment transitively; its immutable seal supplies `(physical pool, catalog, snapshot)`. Physical snapshot identity is intentionally global to a shared catalog. | `delivery_retention_root` has exact target/candidate/generation/seal composite constraints. DuckLake retirement and claim queries consider **all** live/retiring delivery roots and reader leases for the physical tuple, without a Project filter, so one Project's root protects shared physical data. | `TestBND06CrossProjectPhysicalRetentionProtectsLiveSnapshot` in [`internal/analytics/ducklake/postgres/bnd06_retention_qualification_test.go`](../../internal/analytics/ducklake/postgres/bnd06_retention_qualification_test.go). |

## Cross-Project cleanup qualification

The physical-retention qualification creates two issuer Projects with targets
in the same physical pool and catalog. Project A retains a live generation
root; Project B's candidate root becomes due. The production delivery
maintenance function retires and expires only B's root. PostgreSQL then rejects
retirement of A's snapshot, accepts retirement of B's snapshot, and the real
DuckLake retention coordinator claims only B. A remains `live` and unclaimed.
The native catalog session is substituted because it is the external deletion
effect; delivery draining, root enforcement, snapshot eligibility, claiming,
and completion all use the installed PostgreSQL schemas and production paths.

The managed-data qualification stores actual shared content-addressed bytes in
the filesystem store and records distinct Project A and Project B revisions
that reference them. After B's root expires, the production PostgreSQL
reachability source still returns the digest through A's live root. The
production blob collector deletes an unprotected control blob but preserves
the shared bytes, proving both that GC ran and that another Project's expired
root cannot release content still reachable from A.

No production behavior is introduced by this qualification.
