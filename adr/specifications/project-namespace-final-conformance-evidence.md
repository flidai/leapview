# ADR-0018 final conformance evidence reconciliation

Status: FAI-679 candidate evidence; ADR closure awaits protected validation and merge

Evidence snapshot: 2026-09-18

Repository baseline: `c10e7568944cd8ea734b30c09c3dfde899d1de3d`

Governing decision:
[ADR-0018](../0018-retain-project-as-the-durable-deployment-namespace.md)

Normative requirements:
[Project namespace conformance](project-namespace-conformance.md)

Original disposition map:
[ADR-0018 implementation delta](project-namespace-implementation-delta.md)

## Purpose and status rules

This inventory prepares FAI-679's final evidence review without claiming that
ADR-0018 is implemented before the exact merge candidate passes protected
validation. It records exactly one accountable implementation
owner for each of the 54 normative requirements, merged change and validation
evidence, and the genuine evidence gaps that still prevent closure.

`Baseline` means the requirement was already conforming when FAI-665 recorded
the delta and remains owned by the cited subsystem; it is an explicit owner,
not an unowned gap. A supporting PR in an evidence cell does not create a
second implementation owner.

The evidence states are:

- **Main**: the cited implementation is merged on the repository baseline.
- **Partial**: merged implementation covers a defined slice, but the full
  requirement still lacks maintained evidence.
- **Candidate**: the missing executable evidence is present on this isolated
  FAI-679 branch, but has not yet passed protected review and merge validation.
- **Gap**: required documentation alignment or maintained evidence is absent.

## Dependency snapshot

| Dependency | Repository evidence | Current effect |
| --- | --- | --- |
| FAI-670 | [PR #542](https://github.com/flidai/leapview/pull/542), head `a4f7efa53`, merged as `c9e6482a8`; required CI and Security gates passed | Merged. RID lifecycle evidence is authoritative on main; acceptance evidence and Linear status were reconciled to Done on 2026-09-13. |
| FAI-671 | [PR #546](https://github.com/flidai/leapview/pull/546), head `01fc491eb`, merged as `4435ea6c1`; required CI and Security gates passed | Merged. The maintained runtime-boundary audit deliberately identifies its non-exhaustive BND/API evidence. |
| FAI-675 | [PR #549](https://github.com/flidai/leapview/pull/549), head `fbe7ffb37`, merged as `372039da7`; required CI and Security gates passed | Merged. Closed-graph and unsupported-foreign-reference evidence is available on main. |
| FAI-677 | [PR #587](https://github.com/flidai/leapview/pull/587) aligns the maintained architecture and public integration, security, operations, and recovery documentation | ISO-03's documentation mismatch is resolved: clients cannot enumerate or switch Projects, and Project scope comes from the server-bound authority. |
| FAI-678 | [PR #581](https://github.com/flidai/leapview/pull/581), head `e1c56b830`, merged as `f4ad4a032`; exact merge-queue CI and Security passed | Merged. dbt and independent multi-Source adoption evidence is available on main. |
| FAI-679 | [PR #587](https://github.com/flidai/leapview/pull/587) contains this reconciliation inventory and the maintained-documentation audit | Three non-exhaustive API requirement inventories remain open. Protected CI and Security results, including CodeQL path classification and selected SAST, are the merge evidence for this documentation-only reconciliation. |

## Requirement evidence map

| Requirement | Sole implementation owner | Change evidence | Validation evidence | Current status / remaining evidence |
| --- | --- | --- | --- | --- |
| PRJ-01 | FAI-667 | [#499](https://github.com/flidai/leapview/pull/499) / `fdba57d29` | PostgreSQL atomic replay/conflict and concurrent same-tuple bootstrap tests | — (Main) |
| PRJ-02 | FAI-667 | [#499](https://github.com/flidai/leapview/pull/499) / `fdba57d29` | Issuer mint-once, external UID, and replacement-before-HTTP tests | — (Main) |
| PRJ-03 | FAI-667 | [#499](https://github.com/flidai/leapview/pull/499) / `fdba57d29` | Unauthenticated bootstrap rejection and instance-admin bootstrap contract tests | — (Main) |
| PRJ-04 | FAI-667 | [#499](https://github.com/flidai/leapview/pull/499) / `fdba57d29` | Atomic claim/audit replay, conflict audit, and audit-failure rollback tests | — (Main) |
| PRJ-05 | FAI-667 | [#499](https://github.com/flidai/leapview/pull/499) / `fdba57d29` | Conflicting Project/environment bootstrap preserves immutable claim state | — (Main) |
| PRJ-06 | FAI-667 | [#499](https://github.com/flidai/leapview/pull/499) / `fdba57d29` | Authority reuse across targets; independent-environment planning in [#512](https://github.com/flidai/leapview/pull/512) | — (Main) |
| PRJ-07 | Baseline | [#474](https://github.com/flidai/leapview/pull/474) inventory / current serving-state repository | Environment-qualified active-pointer CAS and foreign-Project rejection tests | — (Main) |
| PRJ-08 | FAI-667 | [#499](https://github.com/flidai/leapview/pull/499) / `fdba57d29` | Development publishing bootstraps and reuses issuer identity before Project work | — (Main) |
| PRJ-09 | Baseline | Bound-Project read contract and Project-free browser navigation on main | Generated API inventory and Develop route tests | — (Main) |
| BND-01 | FAI-666 | [#495](https://github.com/flidai/leapview/pull/495) / `5d766dc4f` | Six-kind schema/discovery and forbidden Project/access/publication authoring tests | — (Main) |
| BND-02 | FAI-666 | [#495](https://github.com/flidai/leapview/pull/495) / `5d766dc4f` | Recursive deterministic conventional-directory discovery tests | — (Main) |
| BND-03 | FAI-666 | [#495](https://github.com/flidai/leapview/pull/495) / `5d766dc4f` | Portable deterministic bundle and checkout-independent digest tests | — (Main) |
| BND-04 | FAI-669 | [#512](https://github.com/flidai/leapview/pull/512) / `62d039399` | Exact persisted-plan, stale-target, rehashed-plan, and lock-order admission tests | — (Main) |
| BND-05 | FAI-666 | [#495](https://github.com/flidai/leapview/pull/495) / `5d766dc4f` | Rootless graph, missing endpoint, global ID uniqueness, cycle, and deterministic-byte tests | — (Main) |
| BND-06 | FAI-671 | [#546](https://github.com/flidai/leapview/pull/546) / `4435ea6c1`; delivery and lifecycle support in [#512](https://github.com/flidai/leapview/pull/512) and [#542](https://github.com/flidai/leapview/pull/542) | The maintained [BND-06 boundary matrix](project-namespace-bnd06-boundary-matrix.md) maps all ten named boundaries to authority, constraints, and tests. Real PostgreSQL/DuckLake and managed-blob GC qualifications prove cross-Project cleanup safety. | — (Implemented) |
| BND-07 | FAI-669 | [#512](https://github.com/flidai/leapview/pull/512) / `62d039399` | Same bundle planned independently for multiple environment targets without copied target state | — (Main) |
| RID-01 | FAI-666 | [#495](https://github.com/flidai/leapview/pull/495) / `5d766dc4f` | Duplicate authored ID across kinds rejected before graph construction | — (Main) |
| RID-02 | FAI-670 | [#542](https://github.com/flidai/leapview/pull/542) / `c9e6482a8` | PostgreSQL first-activation allocation and sealed-inventory admission qualification | — (Main) |
| RID-03 | FAI-670 | [#542](https://github.com/flidai/leapview/pull/542) / `c9e6482a8` | Instance/Project isolation and independent random UID allocation qualification | — (Main) |
| RID-04 | FAI-670 | [#542](https://github.com/flidai/leapview/pull/542) plus canonical evidence from [#534](https://github.com/flidai/leapview/pull/534) | Exact instance, Project, authored identity, kind, contract evidence, and generation binding tests | — (Main) |
| RID-05 | FAI-670 | [#542](https://github.com/flidai/leapview/pull/542) / `c9e6482a8` | Stable replay/reuse and kind-conflict rollback qualification | — (Main) |
| RID-06 | FAI-670 | [#542](https://github.com/flidai/leapview/pull/542) / `c9e6482a8` | `TestPostgresResourceUIDAdmissionAndActivationQualification` covers immutable tombstone, authorized exact-generation restore, same-scope historical rollback, atomic audit, and rollback safety | — (Main) |
| RID-07 | FAI-670 | [#542](https://github.com/flidai/leapview/pull/542) / `c9e6482a8` | Registry-key validation excludes paths, names, environments, dbt identifiers, and artifact hashes | — (Main) |
| API-01 | FAI-671 | [#546](https://github.com/flidai/leapview/pull/546) / `4435ea6c1`; FAI-679 candidate | Exact-identity [surface inventory](#api-01-public-project-identity-inventory) links maintained deployment, authorization, audit, lineage, catalog, and generation tests; FAI-679 adds public release and catalog identity assertions | — (Candidate) |
| API-02 | FAI-671 | [#546](https://github.com/flidai/leapview/pull/546) / `4435ea6c1`; FAI-679 candidate | [Composed router inventory](../../internal/app/project_boundary_test.go) checks pre-dispatch selector rejection on mounted browser, agent browser, and generated API routes plus all four generated query bodies; [browser commands and search signals](../../internal/project/http/creator_commands_test.go) retain the bound Project with forged selector fields; the [release command body](../../internal/release/module/module_test.go) rejects a foreign `projectId` | — (Candidate) |
| API-03 | FAI-669 | [#512](https://github.com/flidai/leapview/pull/512) / `62d039399`; #546 supplies supporting generated-locator coverage | Foreign scope and missing claim rejected before source I/O, repository mutation, or workload admission | — (Main) |
| API-04 | Baseline | Explicit serving identity in authorization snapshots and runtime installation | Capability, malformed identity, and authorization-install fail-closed tests | — (Main) |
| API-05 | FAI-671 | [#546](https://github.com/flidai/leapview/pull/546) / `4435ea6c1`; FAI-679 candidate | [Authorization-filtered surface inventory](#api-05-bound-project-and-authorization-filtered-surface-inventory) links maintained catalog, browser, audit, candidate, and error tests; FAI-679 strengthens catalog search/list filtering and follows a denied browser resource through discovery, search, lineage, and detail errors | — (Candidate) |
| API-06 | FAI-671 | [#546](https://github.com/flidai/leapview/pull/546) / `4435ea6c1` plus the API-06 completion change | The maintained [cache/idempotency boundary audit](project-cache-idempotency-boundary-audit.md) maps every consumer; Project/target/environment/candidate partitions, generation byte scopes, protected authorization identities, authoritative HTTP replay scope, native refresh generation scope, exact replay, and fail-closed resolver tests cover the complete identity set | — (Implemented) |
| API-07 | Baseline | Runtime-host and serving-state admission on main; #546 adds durable claim validation | Mixed Project/environment and foreign active-state rejection tests | — (Main) |
| ENV-01 | FAI-669 | [#512](https://github.com/flidai/leapview/pull/512) / `62d039399` | Same Project UID and source bundle planned independently for dev/staging/production targets | — (Main) |
| ENV-02 | Baseline | Immutable serving-generation repository on main | Project/environment binding and immutable idempotent publication tests | — (Main) |
| ENV-03 | Baseline | Target revision/base-generation fences and activation CAS on main | Stale delivery and failed-gate evidence preserve prior active generation | — (Main) |
| ENV-04 | FAI-670 | [#542](https://github.com/flidai/leapview/pull/542) / `c9e6482a8`; delivery binding from [#512](https://github.com/flidai/leapview/pull/512) is supporting evidence | ResourceUID qualification selects exact retained generations, preserves same-scope UID history, and rejects foreign Project/instance restore or lookup | — (Main) |
| ENV-05 | Baseline | Delivery plan/release provenance on main; #512 preserves separation | Source digest/commit remains distinct from generation and execution identity | — (Main) |
| SEM-01 | FAI-675 | [#549](https://github.com/flidai/leapview/pull/549) / `372039da7` | Missing, foreign-qualified, wrong-kind, and incomplete semantic dependency rejection corpus | — (Main) |
| SEM-02 | Baseline | Immutable graph lease and active serving-state graph reader on main | Exact-generation graph pinning and scope-mismatch tests | — (Main) |
| SEM-03 | FAI-675 | [#549](https://github.com/flidai/leapview/pull/549) / `372039da7` | Provenance cannot create a semantic resolver alias; ordinary Models retain normal runtime rules | — (Main) |
| SEM-04 | FAI-678 | [#581](https://github.com/flidai/leapview/pull/581) / `f4ad4a032` | `TestDBTMultiSourceProjectClosure` resolves the upstream package before Parquet handoff and rejects package-, Project-, and missing live Model references without a runtime dbt resolver | — (Main) |
| SEM-05 | FAI-675 | [#549](https://github.com/flidai/leapview/pull/549) / `372039da7` | Closed topology and foreign-reference tests are independent of future hosting topology | — (Main) |
| ISO-01 | Baseline | Project-qualified authorization plus instance-bound runtime/claim evidence on main | Authorization snapshot and bound runtime tests | — (Main) |
| ISO-02 | Baseline | Singleton Project/environment runtime topology on main | Second-Project and environment-mismatch admission rejection tests | — (Main) |
| ISO-03 | FAI-677 | [PR #587](https://github.com/flidai/leapview/pull/587) aligns [`spec.md`](../../spec.md) and maintained public documentation with the merged Project-free source, singleton claim, and instance-local ResourceUID model | The architecture and public guides explicitly state that Project identity is server-bound, clients cannot enumerate or switch Projects, and exact `{project}` parameters assert rather than select scope; documentation and generated checks passed, protected CI and Security passed, selected SAST passed, and CodeQL's path classifier excluded the documentation-only change | — (Main) |
| XPR-01 | FAI-675 | [#549](https://github.com/flidai/leapview/pull/549) / `372039da7`; [#581](https://github.com/flidai/leapview/pull/581) supplies upstream-package coverage | Deterministic foreign-qualified reference rejection without foreign catalog disclosure | — (Main) |
| XPR-02 | FAI-675 | [#549](https://github.com/flidai/leapview/pull/549) / `372039da7` | Unsupported `projectOutput` Source variant fails closed without selector disclosure | — (Main) |
| XPR-03 | Baseline | Ordinary Connection-backed Sources and reusable closed source bundles on main | Source-to-Model-to-SemanticModel closed graph compilation tests | — (Main) |
| XPR-04 | Baseline | Exact active serving-state graph lease on main | Runtime generation pinning and Project scope-mismatch tests | — (Main) |
| XPR-05 | Baseline | ADR-0018 cross-Project boundary requires a separate future ADR | Specification review verifies no native import authority in this profile | — (Main) |
| DBT-01 | FAI-678 | [#581](https://github.com/flidai/leapview/pull/581) / `f4ad4a032` | `task dbt:warehouse:proof` and `TestDBTMultiSourceProjectClosure` exercise the reference adoption path | — (Main) |
| DBT-02 | FAI-678 | [#581](https://github.com/flidai/leapview/pull/581) / `f4ad4a032` | The proof rejects dbt project, repository, invocation, manifest, target, and path values from portable identity while reusing one issuer ProjectUID | — (Main) |
| DBT-03 | FAI-666 | [#495](https://github.com/flidai/leapview/pull/495) / `5d766dc4f` | Conventional-directory dbt/LeapView example contains no Project manifest | — (Main) |
| DBT-04 | FAI-678 | [#581](https://github.com/flidai/leapview/pull/581) / `f4ad4a032` | The same portable bundle is proven against distinct dev/prod target Connection and Source bindings without changing Project identity | — (Main) |
| DBT-05 | FAI-678 | [#581](https://github.com/flidai/leapview/pull/581) / `f4ad4a032` | Upstream package resolution occurs before the physical warehouse handoff; no dbt Mesh runtime resolver is introduced | — (Main) |
| DBT-06 | FAI-678 | [#581](https://github.com/flidai/leapview/pull/581) / `f4ad4a032` | The proof compiles two Connections and Sources from independent producers into one closed semantic graph; PostgreSQL qualification preserves the complete generation ResourceUID inventory | — (Main) |

## ISO-03 maintained-documentation audit

The FAI-679 audit searched maintained architecture and public documentation for
Project listing, discovery, selection, switching, and same-process
multi-Project claims. It corrected the unsupported collection endpoint and
Project-discovery workflow in the [API quickstart](../../docs/articles/integrate/api-quickstart.md),
the Project-list hierarchy in the [agent integration](../../docs/articles/integrate/agent.md)
and [agent tool guide](../../docs/articles/integrate/agent-tools.md), and
ambiguous same-instance language in the integration overview, authorization,
OIDC, SCIM, token, upgrade, troubleshooting, and storage/recovery guides.

The resulting maintained contract is consistent throughout:

- Project identity is bound by the target's authoritative server context.
- Clients cannot enumerate, select, or switch arbitrary Projects.
- `/api/v1/projects/{project}` asserts the expected bound identity; there is no
  `/api/v1/projects` collection endpoint.
- Catalog search and root listing operate inside the active Project graph and
  never return a set of Projects from which the caller chooses.
- Separate instances may reuse enterprise identities, but that does not imply
  same-process multi-Project hosting.

PR #587's documentation and generated checks, protected CI and Security results,
selected SAST results, and CodeQL documentation-only path classification are
the validation evidence for this audit. No runtime, API, compiler, deployment,
ResourceUID, dbt, or recovery behavior changed.

## API-01 public Project identity inventory

The following maintained tests cover the public identity chain. Each assertion
uses the server-bound Project or the exact serving identity; a name, path,
environment, or client selector cannot substitute for that identity.

| Surface | Executable identity evidence |
| --- | --- |
| Deployment | [`TestNativeCoordinatorPostgresPublishCandidatePersistsEvidenceAndReplays`](../../internal/deployment/module/native_coordinator_pg_test.go) checks the Project in the queued activation and public deployment event. |
| Authorization | [`TestActiveProjectResourceIsExactCanonicalReference`](../../internal/app/canonical_authorization_test.go) and [`TestDeliveryAuthorizationRequiresEveryAffectedResource`](../../internal/app/canonical_authorization_test.go) require the bound Project resource and its affected resources. |
| Audit | [`TestProjectAuditProducerPersistsThroughScopedEndpoint`](../../internal/app/project_boundary_test.go) reads the emitted Project identity through the scoped API; [`TestAccessExtendedPostgreSQL18AuthorityBoundaries`](../../internal/access/postgres/access_extended_test.go) rejects foreign-Project rows. |
| Lineage | [`TestActivationLineageVerifierAdapterResolvesExactBinding`](../../internal/app/deploymentpostgres/activation_lineage_test.go) verifies the target/Project/generation tuple at activation. |
| Catalog | [`TestListManagedConnectionsAuthenticatesBeforeCatalogAndFilters`](../../internal/release/module/catalog_api_test.go) asserts the public Project ID on each allowed connection, while [`TestDashboardCatalogPageIncludesAuthoredAndRepositoryManagedDashboards`](../../internal/project/http/browser_test.go) binds dashboard discovery to the Project. |
| Generation | [`TestReleaseResponsePreservesBoundProjectAndGenerationIdentity`](../../internal/release/module/module_test.go) asserts the public release identity; [`TestProvenanceBindsExactGenerationAndBaseIdentity`](../../internal/release/generation_test.go) rejects cross-Project base evidence. |

## API-02 server-bound request inventory

[`TestProjectBoundarySelectorFenceCoversPublicRouteInventory`](../../internal/app/project_boundary_test.go)
walks the production-composed router, including authenticated agent browser
routes such as `/chats/references/search`, and sends each selected browser,
query, search, agent, and release route a client Project selector. It verifies
the shared ingress rejects the selector before dispatch. It fails if any of
the four current generated query body operations is missing or a new one
appears without a body test. The same
file tests encoded, repeated, and alternate selector spellings; actual
dashboard page, dashboard visual, semantic query, semantic explain, and agent
JSON bodies reject a foreign `projectId`. The browser
[`PipelineCommand` test](../../internal/project/http/creator_commands_test.go)
shows a forged signal cannot change the bound command invocation's Project;
the [browser search test](../../internal/project/http/browser_test.go) shows a
forged search signal cannot change the filtered Project result. The
[`CreateRelease` command body](../../internal/release/module/module_test.go)
rejects a foreign `projectId`. API search has a GET contract, so there is no
API search mutation body to accept. The narrow unclaimed-target ProjectUID bootstrap and the
platform audit filter remain separate, explicitly tested exceptions; neither
switches a serving request's Project.

## API-05 bound Project and authorization-filtered surface inventory

| Surface | Executable filter/concealment evidence |
| --- | --- |
| List | [`TestSearchUsesDirectAndGroupGrantsAndDoesNotEnumerateDeniedResources`](../../internal/project/catalog/catalog_test.go) now uses matching allowed and denied models and asserts root list filtering; [`TestListManagedConnectionsAuthenticatesBeforeCatalogAndFilters`](../../internal/release/module/catalog_api_test.go) covers the public connection list. |
| Search | The same catalog test rejects the matching denied model; [`TestSearchFailsClosedForAuthenticationCatalogAndInvalidKinds`](../../internal/release/module/search_test.go) covers public API errors. |
| Discovery | [`TestAssetsFilterUnauthorizedSiblingAndEdges`](../../internal/project/http/browser_test.go) follows the catalog-filtered active graph into the browser bootstrap; [`TestSemanticCatalogDiscoveryAndDirectReferenceShareGate`](../../internal/project/catalog/semantic_visibility_test.go) covers semantic-resource discovery. |
| Autocomplete | Browser `/models/search` in `TestAssetsFilterUnauthorizedSiblingAndEdges` uses the same filtered graph; [`TestSearchReferencesAutocompleteFiltersByAuthorization`](../../internal/agent/module/search_test.go) exercises agent suggestions through the real catalog adapter and per-principal grant snapshot for both search and bare mentions. There is no separate Project-switching autocomplete endpoint. |
| Audit | [`TestProjectAuditProducerPersistsThroughScopedEndpoint`](../../internal/app/project_boundary_test.go) and [`TestAccessExtendedPostgreSQL18AuthorityBoundaries`](../../internal/access/postgres/access_extended_test.go) cover the bound audit API and foreign-row exclusion. |
| Lineage | `TestAssetsFilterUnauthorizedSiblingAndEdges` verifies a denied node and edge do not reappear in the lineage signal. |
| Errors | The same browser test makes a denied detail indistinguishable from an unknown detail; [`TestProjectBoundaryGeneratedLocatorsCannotRetarget`](../../internal/app/project_boundary_test.go) conceals foreign Project locators. |

## Ownership audit

- Requirements mapped: **54**.
- Unique requirement identifiers: **54**.
- Requirements without an accountable owner: **0**.
- Requirements with more than one accountable owner: **0**.
- Requirements with merged maintained implementation evidence: **51**.
- Requirements with FAI-679 candidate evidence: **3** (`API-01`, `API-02`, and
  `API-05`); these are not yet merged or protected-validated.
- Requirements with partial evidence after the candidate tests: **0**, subject
  to the validation gate below.
- Requirements resolved by the documentation-only reconciliation: **1**
  (`ISO-03`).
- Requirements with an unaddressed gap: **0**.
- Supporting evidence that crosses issue boundaries is labeled as supporting
  rather than being assigned a second owner.

## Final blockers

Local FAI-679 candidate validation on 2026-09-18 passed `task generate`,
`task generated:check`, `task docs:check`, `task quality:budget:check`,
`git diff --check`, the focused Project boundary/catalog/browser/release/audit/
agent/architecture tests, and the existing PostgreSQL deployment-lineage and
access-audit tests. These are local results, not protected merge-candidate
checks.

ADR-0018 must remain `pending` until the FAI-679 candidate evidence above
passes its relevant focused, architecture, generated/documentation, and quality
checks, then the exact reviewed merge candidate passes the repository's
protected CI and Security gates. Only then should the ADR implementation status
change; this branch does not claim protected validation or merge.
FAI-670, FAI-671, FAI-675, and FAI-678 are no longer implementation merge
blockers. Their merged evidence replaces the stale provisional and gap claims
from the previous snapshot.
