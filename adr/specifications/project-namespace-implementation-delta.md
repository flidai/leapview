# ADR-0018 implementation delta

Status: baseline recorded

Baseline date: 2026-09-04

Governing decision:
[ADR-0018](../0018-retain-project-as-the-durable-deployment-namespace.md)

Normative requirements:
[Project namespace conformance](project-namespace-conformance.md)

Tracking issue: [FAI-665](https://linear.app/flid/issue/FAI-665)

## Purpose

This is the pre-implementation disposition map required by FAI-665. It records
what the repository already implements, what must be retained, and which one
surviving Linear issue owns each gap. It is not final conformance evidence:
FAI-679 must replace pending work-item links with maintained executable evidence
after the implementation issues merge.

The dispositions mean:

- **Conforming**: the current implementation and maintained evidence satisfy the
  requirement.
- **Partial**: relevant machinery exists, but a required behavior or direct
  conformance test is absent.
- **Missing**: no implementation of the required contract was found.
- **Conflicting**: current behavior accepts or requires something the profile
  forbids.

## Existing machinery to retain

| Boundary | Existing implementation and evidence |
| --- | --- |
| Singleton target claim | [`deployment.ProjectClaim`](../../internal/deployment/project_claim.go), the singleton [migration](../../internal/platform/migrations/071_instance_project_claim.sql), and the SQLite and PostgreSQL repository tests in [`project_claim_test.go`](../../internal/deployment/sqlite/project_claim_test.go) and [`repository_test.go`](../../internal/platform/bootstrap/postgres/repository_test.go). |
| Atomic claim and candidate start | [`CandidateService.Start`](../../internal/deployment/candidate_service.go) and `TestCandidateClaimConflictRollsBackCandidate` in [`project_claim_test.go`](../../internal/deployment/sqlite/project_claim_test.go). |
| Target-bound delivery lifecycle | [`DeliveryPlan`](../../internal/deployment/plan_delivery_plan.go) and [`DeliveryLifecycle`](../../internal/deployment/lifecycle.go) already carry target, Project, environment, source digest, active base generation, target revision, provenance, governance, and resolved-input evidence. |
| Serving identity | [`ServingIdentity` and `CandidateScope`](../../internal/project/graph/graph.go) are the canonical Project/environment/generation scope. |
| Activation, leases, and drain | The runtime host tests in [`lifecycle_test.go`](../../internal/runtimehost/lifecycle_test.go) cover Project/environment mismatch, stale activation, prior-generation preservation, authorization installation, leases, and drain. |
| Durable generation state | The serving-state repository tests in [`repository_test.go`](../../internal/servingstate/sqlite/repository_test.go) cover immutable validated state, compare-and-swap activation, environment-scoped pointers, retention, leases, and foreign-Project rejection. |
| Project-qualified runtime paths | Existing focused evidence includes authorization snapshots in [`policy_test.go`](../../internal/access/snapshot/policy_test.go), result identity and caches in [`project_runtime_test.go`](../../internal/analytics/module/project_runtime_test.go), managed-data identity in [`repository_test.go`](../../internal/manageddata/sqlite/repository_test.go), and release generation identity in [`generation_test.go`](../../internal/release/generation_test.go). |
| Closed graph resolution | Graph construction rejects missing endpoints and cycles in [`graph_test.go`](../../internal/project/graph/graph_test.go); compiler tests cover candidate-local Model resolution, duplicate authored IDs, and deterministic bytes in [`project_flat_test.go`](../../internal/project/compiler/project_flat_test.go). |

These are extension points, not invitations to introduce a second compiler,
binding service, lifecycle, Project context, or registry framework.

## Requirement disposition

### Instance claim and lifecycle

| Requirement | Disposition | Current evidence or conflict | Gap owner |
| --- | --- | --- | --- |
| PRJ-01 | Conforming | The database schema permits only `singleton_id = 1`; repository restart, conflicting tuple, and concurrent A/B tests are in [`internal/deployment/sqlite/project_claim_test.go`](../../internal/deployment/sqlite/project_claim_test.go). Runtime binding rejects a second Project in [`internal/runtimehost/project_claim_test.go`](../../internal/runtimehost/project_claim_test.go). | — |
| PRJ-02 | Conflicting | `CandidateService.Start` currently claims the target from `StartCandidateRequest.ProjectID`, which originates in the authored Project-root flow. No deployment-authority mint-once state exists. | [FAI-667](https://linear.app/flid/issue/FAI-667) |
| PRJ-03 | Missing | The claim service and transaction exist, but there is no narrow pre-Project, instance-administrator-authorized bootstrap API. | [FAI-667](https://linear.app/flid/issue/FAI-667) |
| PRJ-04 | Partial | Exact tuple idempotency, conflict, concurrency, and atomic candidate rollback already pass in [`internal/deployment/sqlite/project_claim_test.go`](../../internal/deployment/sqlite/project_claim_test.go). Issuer identity and durable success/conflict audit evidence are absent. | [FAI-667](https://linear.app/flid/issue/FAI-667) |
| PRJ-05 | Conforming | Both SQLite and PostgreSQL claim repositories preserve the first tuple and reject a replacement; the PostgreSQL test also rejects deletion in [`internal/platform/bootstrap/postgres/repository_test.go`](../../internal/platform/bootstrap/postgres/repository_test.go). No retarget API exists. | — |
| PRJ-06 | Partial | Project/environment/generation state is instance-local and environment-qualified, but no authority-owned UID flow proves that several targets receive the same exact Project UID or retain independent target state. | [FAI-667](https://linear.app/flid/issue/FAI-667), [FAI-669](https://linear.app/flid/issue/FAI-669) |
| PRJ-07 | Conforming | Active pointers are Project/environment qualified and compare-and-swap protected; see `TestRepositoryTracksActiveGenerationsPerEnvironment` and `TestRepositoryRejectsForeignProjectSecondActiveGeneration` in [`internal/servingstate/sqlite/repository_test.go`](../../internal/servingstate/sqlite/repository_test.go). | — |
| PRJ-08 | Missing | Local setup has no durable deployment-authority Project UID state and still defaults to `dashboards/leapview.yaml`. | [FAI-667](https://linear.app/flid/issue/FAI-667) |
| PRJ-09 | Conforming | Repository and API inspection found no multi-Project create/list/rename/archive/select control plane or Project picker. The runtime deliberately binds one Project. | — |

### Authoring, compilation, and binding

| Requirement | Disposition | Current evidence or conflict | Gap owner |
| --- | --- | --- | --- |
| BND-01 | Conflicting | [`graph.KindProject`](../../internal/project/graph/graph.go) is a valid graph kind and the compiler accepts a `kind: Project` root. | [FAI-666](https://linear.app/flid/issue/FAI-666) |
| BND-02 | Conflicting | Compiler and CLI entry points call `LoadProject` with a required `leapview.yaml`; Project includes drive resource discovery. | [FAI-666](https://linear.app/flid/issue/FAI-666) |
| BND-03 | Partial | Canonical graph and bundle bytes are deterministic and target-free, as shown by `TestProjectGraphCanonicalBytesStableAcrossTraversalOrder` and `TestPackProjectPreservesAuthoredSourcesDeterministically`. They still derive and serialize Project identity from authored source. | [FAI-666](https://linear.app/flid/issue/FAI-666) |
| BND-04 | Partial | [`DeliveryPlan`](../../internal/deployment/plan_delivery_plan.go) already binds all named target and lifecycle fields, and stale work is rejected before physical work by [`internal/deployment/lifecycle_test.go`](../../internal/deployment/lifecycle_test.go). It has not yet been connected to an unbound BundleDigest and authoritative claim/locator resolution. | [FAI-669](https://linear.app/flid/issue/FAI-669) |
| BND-05 | Conflicting | `ProjectGraph` requires exactly one `KindProject` root even though ordinary resource edges are already closed and validated. | [FAI-666](https://linear.app/flid/issue/FAI-666) |
| BND-06 | Partial | Project-qualified serving, release, managed-data, authorization, audit, cache, lease, and retention types exist. A complete boundary audit and collision/rejection corpus has not been performed. | [FAI-671](https://linear.app/flid/issue/FAI-671) |
| BND-07 | Partial | Plans, approvals, target revisions, credentials, and active pointers are target-owned today, but there is no maintained same-bundle multi-environment promotion test proving that none are copied. | [FAI-669](https://linear.app/flid/issue/FAI-669) |

### Resource identity

| Requirement | Disposition | Current evidence or conflict | Gap owner |
| --- | --- | --- | --- |
| RID-01 | Conforming | `TestFlatProjectRejectsDuplicateStableIDsAcrossKinds` in [`internal/project/compiler/project_flat_test.go`](../../internal/project/compiler/project_flat_test.go) proves authored-ID uniqueness across kinds before graph construction. | — |
| RID-02 | Missing | No instance-local ResourceUID registry or activation binding for `(ProjectUID, metadata.id, kind)` exists. | [FAI-670](https://linear.app/flid/issue/FAI-670) |
| RID-03 | Missing | Authored IDs are Project-qualified in many runtime paths, but no registry allocates independent ResourceUIDs for unrelated instances/Projects. | [FAI-670](https://linear.app/flid/issue/FAI-670) |
| RID-04 | Missing | Generation and provenance evidence is qualified, but there is no ResourceUID whose instance-local portability rules can be enforced or tested. | [FAI-670](https://linear.app/flid/issue/FAI-670) |
| RID-05 | Missing | Compatible ResourceUID reuse and kind-change rejection are not implemented. | [FAI-670](https://linear.app/flid/issue/FAI-670) |
| RID-06 | Missing | Generation rollback and Project/environment isolation exist, but resource tombstone, restore, and rollback registry semantics do not. | [FAI-670](https://linear.app/flid/issue/FAI-670) |
| RID-07 | Partial | Graph IDs are explicit and provenance/path is descriptive only, but Project identity still comes from authored Project YAML and there is no registry-key test corpus. | [FAI-667](https://linear.app/flid/issue/FAI-667), [FAI-670](https://linear.app/flid/issue/FAI-670) |

### Public surfaces and authorization

| Requirement | Disposition | Current evidence or conflict | Gap owner |
| --- | --- | --- | --- |
| API-01 | Conforming | `ProjectID` is explicit in serving identity, deployment plans/candidates/publication evidence, authorization snapshots, release provenance, audit events, catalog projections, and generated deployment contracts. | — |
| API-02 | Conforming | Browser routes are Project-free and use the bound runtime; `TestBoundProjectUsesActiveProjectResolver` in [`internal/project/http/browser_test.go`](../../internal/project/http/browser_test.go) and `TestDevelopCatalogUsesStableDashboardLinksWithoutProjectPicker` in [`internal/project/ui/develop_test.go`](../../internal/project/ui/develop_test.go) provide focused evidence. | — |
| API-03 | Partial | Delivery target resolution compares requested target, Project, and environment in [`internal/deployment/lifecycle.go`](../../internal/deployment/lifecycle.go), but the current candidate path can establish a claim from authored identity and there is no bootstrap-only exception or locator-to-claim test. | [FAI-667](https://linear.app/flid/issue/FAI-667), [FAI-669](https://linear.app/flid/issue/FAI-669) |
| API-04 | Conforming | Authorization snapshots bind an exact `ServingIdentity` and fail closed on malformed identity/capability in [`internal/access/snapshot/policy_test.go`](../../internal/access/snapshot/policy_test.go); runtime activation also fails closed when authorization installation fails. | — |
| API-05 | Partial | Candidate APIs conceal foreign ownership and bound browser/search paths exist, but all list, autocomplete, audit, lineage, and error projections still need one systematic shared-boundary audit. | [FAI-671](https://linear.app/flid/issue/FAI-671) |
| API-06 | Partial | Result-cache execution scopes and managed-data collection identities are qualified, with evidence in [`internal/analytics/module/project_runtime_test.go`](../../internal/analytics/module/project_runtime_test.go) and [`internal/manageddata/sqlite/repository_test.go`](../../internal/manageddata/sqlite/repository_test.go). A complete cache/idempotency collision audit is absent. | [FAI-671](https://linear.app/flid/issue/FAI-671) |
| API-07 | Conforming | Runtime host binding rejects a second Project or environment and serving-state persistence rejects a foreign second active Project; see [`internal/runtimehost/lifecycle_test.go`](../../internal/runtimehost/lifecycle_test.go) and [`internal/servingstate/sqlite/repository_test.go`](../../internal/servingstate/sqlite/repository_test.go). | — |

### Environment and generation behavior

| Requirement | Disposition | Current evidence or conflict | Gap owner |
| --- | --- | --- | --- |
| ENV-01 | Partial | Environment is explicit target state and not branch-derived, but Project identity is still authored and no same-UID multi-environment authority flow is maintained. | [FAI-667](https://linear.app/flid/issue/FAI-667), [FAI-669](https://linear.app/flid/issue/FAI-669) |
| ENV-02 | Conforming | `ServingIdentity` and persisted state bind one Project, environment, and generation; `TestRepositorySaveValidatedIsIdempotentAndImmutable` proves immutable publication state. | — |
| ENV-03 | Conforming | Delivery publication and serving-state activation use base-generation/target-revision fences and compare-and-swap. Failure and stale-work tests in [`internal/deployment/lifecycle_test.go`](../../internal/deployment/lifecycle_test.go) and [`internal/runtimehost/lifecycle_test.go`](../../internal/runtimehost/lifecycle_test.go) preserve the prior active generation. | — |
| ENV-04 | Partial | Rollback creates a fresh plan from retained evidence and runtime scope checks reject Project/environment mismatch, but direct cross-scope rollback rejection should be part of the runtime boundary corpus. | [FAI-671](https://linear.app/flid/issue/FAI-671) |
| ENV-05 | Conforming | Delivery plan and release provenance store source digest/commit separately from base and resulting `GenerationID`; see [`internal/deployment/plan_delivery_plan.go`](../../internal/deployment/plan_delivery_plan.go) and [`internal/release/provenance_test.go`](../../internal/release/provenance_test.go). | — |

### Semantic composition

| Requirement | Disposition | Current evidence or conflict | Gap owner |
| --- | --- | --- | --- |
| SEM-01 | Partial | SemanticModel datasets resolve through the candidate-local Model map, and unknown/wrong-kind references fail in [`internal/project/compiler/project_flat_test.go`](../../internal/project/compiler/project_flat_test.go). There is no explicit foreign-Project qualifier rejection corpus. | [FAI-675](https://linear.app/flid/issue/FAI-675) |
| SEM-02 | Conforming | Compiled models are stored inside one graph/artifact, and active graph readers pin one exact runtime lease in [`internal/project/module/active_graph_test.go`](../../internal/project/module/active_graph_test.go); there is no resolver path to another active catalog. | — |
| SEM-03 | Partial | Source-to-Model-to-SemanticModel is the ordinary compiler path and no dbt runtime kind exists. Maintained dbt provenance-independence evidence is still absent. | [FAI-675](https://linear.app/flid/issue/FAI-675), [FAI-678](https://linear.app/flid/issue/FAI-678) |
| SEM-04 | Missing | ADR-0019 documents the upstream dbt boundary, but no fixture proves upstream dependencies become consumer-owned ordinary Sources/Models before LeapView compilation. | [FAI-678](https://linear.app/flid/issue/FAI-678) |
| SEM-05 | Partial | The current singleton runtime cannot resolve across active Projects, but no focused topology/foreign-reference test preserves this rule independently of future hosting changes. | [FAI-675](https://linear.app/flid/issue/FAI-675) |

### Isolation boundary

| Requirement | Disposition | Current evidence or conflict | Gap owner |
| --- | --- | --- | --- |
| ISO-01 | Conforming | Project-qualified application identity is carried inside an instance-scoped runtime/storage topology; singleton claim and runtime-host tests make the two boundaries explicit. | — |
| ISO-02 | Conforming | The singleton claim schema and runtime host admit one Project/environment per instance and reject a second Project. | — |
| ISO-03 | Partial | ADR-0018 is explicit, but public/adjacent documentation has not been audited and the Linear project overview still describes native publications and cross-Project imports contrary to the accepted v1 profile. | [FAI-677](https://linear.app/flid/issue/FAI-677) |

### Cross-Project boundary

| Requirement | Disposition | Current evidence or conflict | Gap owner |
| --- | --- | --- | --- |
| XPR-01 | Partial | Graph edges must resolve locally and compiler diagnostics reject unknown Models, but there is no explicit foreign-Project syntax and nondisclosure rejection corpus. | [FAI-675](https://linear.app/flid/issue/FAI-675) |
| XPR-02 | Partial | The generated Source location union contains ordinary path/object variants and no `projectOutput`/import-lock variant. A maintained negative schema/compile fixture is still required. | [FAI-675](https://linear.app/flid/issue/FAI-675) |
| XPR-03 | Conforming | Ordinary Sources use explicit Connection plus path/object identity and compile into one closed graph; no producer-specific Project identity is required. | — |
| XPR-04 | Conforming | Graph/artifact validation and leased active-graph reading use only the private candidate/generation; no code path resolves another LeapView Project catalog as serving authority. | — |
| XPR-05 | Conforming | ADR-0018 explicitly requires a separate future ADR and lists the necessary semantics; the v1 implementation contains no native import abstraction. | — |

### dbt mapping

| Requirement | Disposition | Current evidence or conflict | Gap owner |
| --- | --- | --- | --- |
| DBT-01 | Missing | ADR-0019 defines the mapping, but the reference dbt-repository-plus-LeapView deployment fixture does not exist. | [FAI-678](https://linear.app/flid/issue/FAI-678) |
| DBT-02 | Missing | Provenance types are separate from execution identity, but there is no issuer-owned Project UID flow or dbt provenance fixture proving these fields cannot replace it. | [FAI-667](https://linear.app/flid/issue/FAI-667), [FAI-678](https://linear.app/flid/issue/FAI-678) |
| DBT-03 | Conflicting | SemanticModels/Dashboards can live beside other resources, but compilation still requires authored `kind: Project` in `leapview.yaml`. | [FAI-666](https://linear.app/flid/issue/FAI-666) |
| DBT-04 | Partial | Target Connection bindings and ordinary Source locations already select environment-specific physical relations; the dbt mapping fixture is absent. | [FAI-675](https://linear.app/flid/issue/FAI-675), [FAI-678](https://linear.app/flid/issue/FAI-678) |
| DBT-05 | Missing | Runtime has no dbt Mesh resolver, as required, but there is no fixture showing upstream dbt dependencies only as provenance behind consumer-owned outputs. | [FAI-678](https://linear.app/flid/issue/FAI-678) |
| DBT-06 | Partial | The compiler already supports several ordinary Sources and Models in one closed graph. A maintained independently-produced multi-Source semantic fixture and live-reference rejection are absent. | [FAI-675](https://linear.app/flid/issue/FAI-675), [FAI-678](https://linear.app/flid/issue/FAI-678) |

## Follow-up ownership

| Issue | Sole implementation responsibility identified by this baseline |
| --- | --- |
| [FAI-666](https://linear.app/flid/issue/FAI-666) | One breaking Project-free authoring/compiler/graph/bundle cutover. |
| [FAI-667](https://linear.app/flid/issue/FAI-667) | Authority-owned UID issuance and durable local state; instance-admin bootstrap, atomic claim, and audit. |
| [FAI-669](https://linear.app/flid/issue/FAI-669) | Make the existing delivery plan the only unbound-bundle-to-target binding boundary and prove independent promotion. |
| [FAI-670](https://linear.app/flid/issue/FAI-670) | Instance-local ResourceUID allocation, reuse, kind fencing, tombstone, restore, and rollback. |
| [FAI-671](https://linear.app/flid/issue/FAI-671) | Audit and close only demonstrated Project-boundary gaps in runtime/API/cache/idempotency/storage/cleanup paths. |
| [FAI-675](https://linear.app/flid/issue/FAI-675) | Closed semantic resolver and unsupported native-import rejection corpus. |
| [FAI-677](https://linear.app/flid/issue/FAI-677) | Adjacent specification and public mental-model alignment, including the stale Linear project overview. |
| [FAI-678](https://linear.app/flid/issue/FAI-678) | dbt and independently produced multi-Source adoption fixtures. |
| [FAI-679](https://linear.app/flid/issue/FAI-679) | Replace pending links with final executable evidence, run protected validation, and only then mark ADR-0018 implemented. |

The duplicate issues FAI-668, FAI-672, FAI-673, FAI-674, and FAI-676 own no
independent work. Their concerns are covered by the surviving issues above.

## Cutover rule

LeapView is unreleased. FAI-666 must remove the authored Project/root-manifest
contract in one breaking change. This map proposes no compatibility mode,
parallel bundle version, shim, Project CRUD/registry, Project picker,
request-time Project context, multi-Project host, or native import framework.
