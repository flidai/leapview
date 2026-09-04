# dbt warehouse-boundary conformance map

Status: partial; Project namespace prerequisite pending

Last updated: 2026-09-04

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

The profile remains partial while ADR-0018 is pending: current portable source
still uses an authored `kind: Project` manifest. The example follows the
currently implemented compiler contract and must migrate to Project-free
discovery with ADR-0018. ADR-0019 must not be marked implemented before that
prerequisite is satisfied.

## Confirmation map

| ADR-0019 confirmation | Maintained implementation evidence | Status or remaining item |
| --- | --- | --- |
| Ordinary Connection → Source → Model → SemanticModel → Dashboard graph serves dbt output | `examples/dbt-warehouse-boundary/`; compiler and runtime packages under `internal/project/compiler`, `internal/analytics/duckdb`, and `internal/dashboard/runtime` | Implemented; remove the root Project manifest when ADR-0018 BND-01/BND-02 lands. |
| One local command builds dbt, external Parquet, and starts LeapView | `task dbt:warehouse`; `scripts/dbt-warehouse-boundary.sh`; `scripts/dev-server.sh` | Implemented. |
| Explicit resources compile and serve without dbt artifacts or dbt in LeapView | Example LeapView YAML contains no artifact reference; `internal/platform/architecture/dbt_boundary_test.go` | Implemented. |
| Production invokes LeapView only after successful build and complete publication | `.github/workflows/dbt-warehouse-boundary-reference.yml`; architecture workflow assertions | Implemented reference workflow; target credentials remain operator configuration. |
| Ordinary versus coordinated consistency is honest | Producer-neutral fixture and [integration guide](../../docs/articles/integrate/dbt-warehouse-boundary.md) | Implemented without a marker protocol. |
| Schema, type, grain, and checks reject while prior generation serves | `internal/analytics/gates/gates_test.go`; `internal/app/integration_minio_source_test.go`; producer-neutral fixture; `internal/runtimehost/lifecycle_test.go` | Implemented by ADR-0010 and existing runtimehost recovery. |
| Stable field IDs and readable labels require no physical cosmetic transform | Example Models and `semantic-models/sales.yaml` | Implemented. |
| Optional metadata import reconciles metadata and physical state | None | Deferred by decision; this conditional confirmation does not apply until an importer is proposed. |
| Serving and refresh require no dbt executable, repository, artifacts, or credentials | Runtime module/image architecture assertion; dbt dependencies are isolated under `examples/` and CI | Implemented. |
| Source read, serving write, and semantic policy boundaries remain distinct | Azure workflow, existing scoped Azure secret tests in `internal/analytics/duckdb/source_test.go`, and integration guide | Implemented as target/operator contract; live Azure authorization remains deployment-specific evidence. |
| MetricFlow/dbt Semantic Layer definitions do not silently become LeapView definitions | No artifact parser or dbt semantic dependency exists; architecture assertion | Implemented by absence and explicit deferral. |
| Every SemanticModel dataset resolves inside the same Project candidate/generation | ADR-0018 SEM-01/SEM-02 evidence in `project-namespace-conformance.md` | Blocked on full ADR-0018 conformance; current graph resolution remains Project-local. |

## Existing capability composition

| Boundary responsibility | Existing authority reused |
| --- | --- |
| Azure Blob and scoped DuckDB secret | connector registry and `internal/analytics/duckdb/source.go` |
| Parquet discovery and materialization | typed Source path contracts and `internal/analytics/duckdb/materialize.go` |
| Source schema/freshness and Model output/grain/checks | `internal/analytics/gates` (ADR-0010) |
| Candidate construction and qualification | `internal/app/runtimefactory/delivery_runner.go` and `internal/analytics/candidatecatalog` |
| Atomic activation, retained generations, rollback, leases, recovery | `internal/runtimehost`, `internal/deployment`, and `internal/servingstate` |
| Local non-interactive orchestration | `leapview dev --once --no-browser` via `scripts/dev-server.sh` |
| Project/environment identity and target bindings | existing deployment and connection-binding contracts; final Project-free source semantics pending ADR-0018 |

Physical Parquet is the CI contract evidence. dbt's manifest and run-results
files are neither parsed nor admitted. The producer-neutral fixture deliberately
does not mention dbt, so the same lifecycle evidence applies to another
producer.

