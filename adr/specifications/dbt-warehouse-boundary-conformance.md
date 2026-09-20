# dbt warehouse-boundary conformance map

Status: partial; Project-boundary evidence merged, Azure qualification workflow added but not run live

Last updated: 2026-09-18 (main `add1b99142cefed1c57f0650ea5f0fc004743b27`)

Governing decision:
[ADR-0019](../0019-integrate-dbt-at-the-warehouse-contract-boundary.md)

Prerequisite:
[ADR-0018](../0018-retain-project-as-the-durable-deployment-namespace.md) and
[Project namespace conformance](project-namespace-conformance.md)

## Scope and implementation rule

ADR-0019 is composition over the ordinary LeapView data-resource and delivery
paths. It adds a maintained producer example, orchestration, physical contract
CI, and evidence. It adds no dbt runtime kind, parser, scheduler, public CLI,
publication service, release envelope, or compatibility algorithm.

The merged ADR-0018 path now supplies Project-free discovery (FAI-666),
issuer-owned durable ProjectUID bootstrap (FAI-667), delivery-bound target
binding (FAI-669), instance-local ResourceUID allocation (FAI-670), and
closed semantic resolution (FAI-675). FAI-678 proves the dbt plus independent
CRM source graph closes under one ProjectUID and exact generation. Merged
FAI-679 adds executable API identity, selector-fence, and authorization-filtering
evidence. These are current-main implementation and test claims, not a claim
that ADR-0019 or its live Azure profile is complete. ADR-0018's formal
implementation-status declaration is maintained separately.

Evidence classifications used below:

- **Proven local** means repository tests or the local showcase exercise the
  behavior without a live cloud control plane.
- **Structurally validated — not live Azure** means the authored workflow,
  commands, and CI wiring are parsed and checked, but live Azure OIDC, RBAC,
  storage retention, and checksum behavior are not claimed.
- **Merged Project-boundary evidence** means issuer bootstrap, delivery binding,
  ResourceUID/generation closure, and API fencing are present on current main;
  it does not establish live Azure qualification.
- **Proven live Azure** requires a successful protected-default-branch run of
  `.github/workflows/dbt-warehouse-boundary-azure-qualification.yml`, three
  distinct OIDC identities, successful checksum-preserving reads, and the
  expected data-plane authorization denials. No such run is recorded yet.

## Confirmation map

| ADR-0019 confirmation | Maintained implementation evidence | Status or remaining item |
| --- | --- | --- |
| Ordinary Connection → Source → Model → SemanticModel → Dashboard graph serves dbt output | `examples/dbt-warehouse-boundary/`; `TestDBTMultiSourceProjectClosure`; compiler and runtime packages under `internal/project/compiler`, `internal/analytics/duckdb`, and `internal/dashboard/runtime` | Proven local with Project-free conventional resource discovery and one issuer-owned ProjectUID. |
| One local command builds dbt, external Parquet, and starts LeapView | `task dbt:warehouse`; `scripts/dbt-warehouse-boundary.sh`; `scripts/dev-server.sh` | Proven local. |
| Explicit resources compile and serve without dbt artifacts or dbt in LeapView | Example LeapView YAML contains no artifact reference; `TestDBTWarehouseBoundaryDoesNotEnterLeapViewRuntime` in `internal/platform/architecture/dbt_boundary_test.go` | Proven local. |
| Production invokes LeapView only after successful build and complete publication | `.github/workflows/dbt-warehouse-boundary-reference.yml`; architecture workflow assertions | Structurally validated — not live Azure; target credentials and IAM scopes remain operator requirements. |
| Ordinary versus coordinated consistency is honest | Producer-neutral fixture and [integration guide](../../docs/articles/integrate/dbt-warehouse-boundary.md) | Proven local and structurally documented without a marker protocol. |
| Schema, type, grain, and checks reject while prior generation serves | `internal/analytics/gates/gates_test.go`; `internal/app/integration_minio_source_test.go`; producer-neutral fixture; `internal/runtimehost/lifecycle_test.go` | Proven local by ADR-0010 and existing runtimehost recovery. |
| Stable field IDs and readable labels require no physical cosmetic transform | Example Models and `semantic-models/sales.yaml` | Proven local. |
| Optional metadata import reconciles metadata and physical state | None | Deferred by decision; this conditional confirmation does not apply until an importer is proposed. |
| Serving and refresh require no dbt executable, repository, artifacts, or credentials | Runtime module/image architecture assertion; dbt dependencies are isolated under `examples/` and CI | Proven local by runtime/module assertions. |
| Source read, serving write, and semantic policy boundaries remain distinct | Azure reference and qualification workflows, `internal/platform/architecture/dbt_azure_qualification_test.go`, existing scoped Azure secret tests in `internal/analytics/duckdb/source_test.go`, and integration guide | Structurally validated — not live Azure; three distinct OIDC identities and safe negative data-plane probes are maintained, but a successful protected-main execution is still required. |
| MetricFlow/dbt Semantic Layer definitions do not silently become LeapView definitions | No artifact parser or dbt semantic dependency exists; architecture assertion | Implemented by absence and explicit deferral. |
| Every SemanticModel dataset resolves inside the same Project candidate/generation | `task dbt:warehouse:proof`; `TestDBTMultiSourceProjectClosure`; `TestDBTProofDeliveryPlanRequiresProductionEvidence`; `TestPostgresResourceUIDMultiSourceProjectClosure`; ADR-0018 SEM-01/SEM-02 evidence in `project-namespace-conformance.md` | Proven locally for a dbt-package-derived mart plus an independent CRM publication. The graph closes under one issuer ProjectUID and one exact generation ResourceUID inventory. Missing and ambiguous Source mappings fail before delivery. |
| dbt output has no alternate authorization path | `TestDBTMultiSourceProjectClosure`; ADR-0017 semantic-access compiler, consumer, and PlanIR barrier suites | Proven locally. The exact dbt/CRM semantic mapping rejects unbound execution, admits a request-bound consumer, installs barriers on both dataset scans, and fails closed for denied target-owned attributes. |

The FAI-678 proof adds adoption evidence, not runtime authority. dbt resolves
its upstream package and materializes the consumer-owned mart before the
physical boundary. LeapView receives Parquet plus ordinary Connection and
Source resources; it neither imports dbt artifacts nor resolves live dbt Mesh
references. The second CRM publication has a separate Connection root, while
both Sources close into the same SemanticModel and Dashboard generation.

The merged FAI-679 API evidence covers response identity
(`TestReleaseResponsePreservesBoundProjectAndGenerationIdentity`), the
production-composed router's Project selector fence including
`/chats/references/search`
(`TestProjectBoundarySelectorFenceCoversPublicRouteInventory`), and
principal-filtered agent suggestions through the real catalog adapter
(`TestSearchReferencesAutocompleteFiltersByAuthorization`). ProjectUID is
bootstrapped by the issuer before target interaction; the workload Project
selector requests the exact already-bound Project scope and does not mint
Project identity. The dbt proof validates a production-shaped delivery plan.
Separately, `TestPostgresResourceUIDMultiSourceProjectClosure` validates exact
ResourceUID and graph-kind bindings against the compiled multi-source graph.

## Existing capability composition

| Boundary responsibility | Existing authority reused |
| --- | --- |
| Azure Blob and scoped DuckDB secret | connector registry and `internal/analytics/duckdb/source.go` |
| Parquet discovery and materialization | typed Source path contracts and `internal/analytics/duckdb/materialize.go` |
| Source schema/freshness and Model output/grain/checks | `internal/analytics/gates` (ADR-0010) |
| Candidate construction and qualification | `internal/app/runtimefactory/delivery_runner.go` and `internal/analytics/candidatecatalog` |
| Atomic activation, retained generations, rollback, leases, recovery | `internal/runtimehost`, `internal/deployment`, and `internal/servingstate` |
| Local non-interactive orchestration | `leapview dev --once --no-browser` via `scripts/dev-server.sh` |
| Project/environment identity and target bindings | `internal/app/cli/project_bootstrap.go` and its persistence tests; delivery-bound plan in `TestDBTProofDeliveryPlanRequiresProductionEvidence`; `TestPostgresResourceUIDMultiSourceProjectClosure`; FAI-679 API boundary tests above |
| Semantic authorization over consumer outputs | existing ADR-0017 semantic-access consumer, compiled policy, target-owned attribute authority, and PlanIR SecurityBarrier path |

Physical Parquet is the CI contract evidence. dbt's manifest and run-results
files are neither parsed nor admitted; a targeted metadata-only case proves
they cannot substitute for the physical relation. The fixture's discovery,
gate, and activation behavior remains producer-neutral and applies to any
Parquet producer. IAM scopes for producer read/write, Source read, and serving
write are operator requirements; this map does not claim live Azure RBAC or
checksum evidence. A failed upload may leave an orphan partial publication
prefix. It remains unselected and must be handled by storage lifecycle
retention, not recovery mutation.

## Remaining qualification

The local one-command path, copyable production reference, dbt-free runtime,
and exact-candidate Project/ResourceUID/generation checks have maintained
repository evidence above. Merged PR #645 passed its hosted `CI gate`,
`Security gate`, and `dbt physical contract (PR)` checks; those checks do not
qualify the separate live Azure boundary.

The FAI-688 qualification workflow adds no runtime protocol. Its
run/attempt/Git-SHA prefix and SHA-256 comparisons are external qualification
evidence only. Conditional create and overwrite probes use an impossible ETag
and require HTTP 403 with Azure's `AuthorizationPermissionMismatch` code,
while the delete probe addresses a nonexistent object; these probes cannot
mutate admitted producer data even if
the tested role is accidentally broader than expected. The DuckLake probe
writes and removes only a dedicated LeapView-owned qualification object. A
successful protected-main run is still required before any live-cloud row can
be marked proven.

FAI-688 still requires live Azure evidence for distinct producer-write,
Source-read, and DuckLake-write identities; effective OIDC and RBAC scopes
including denied writes and cross-scope reads; uploaded Parquet byte checksums
and exact file-set/provenance verification; immutable-prefix and retention
behavior; and live failure/recovery qualification without replacing a valid
serving generation or mutating producer objects. The authored reference and
local tests cannot establish these cloud guarantees. ADR-0019 remains partial
until that qualification and the final exact-candidate checks are complete.
