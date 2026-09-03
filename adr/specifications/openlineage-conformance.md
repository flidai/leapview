# OpenLineage projection conformance specification

Status: implemented

Profile: `leapview.dev/openlineage-projection/v1`

Standard: OpenLineage 2.0.2 object model and pinned standard facets

Direction and level: export-only projection, document

Last updated: 2026-09-03

Governing decisions: [ADR-0014](../0014-adopt-an-asset-selected-refresh-pipeline-contract.md)
and [ADR-0016](../0016-adopt-standards-aligned-data-contracts-and-interchange.md)

## Boundary

- **OL-01:** The single refresh-owned package at
  [`internal/refresh/openlineage`](../../internal/refresh/openlineage) projects
  an immutable Pipeline and PipelineRun into the OpenLineage Job, Run, and
  Dataset object model. It is an export-only value boundary; it does not own
  execution, retry, authorization, approval, publication, or rollback.
- **OL-02:** Pipeline identity and plan evidence map to Job and its
  `leapView_pipeline` facet. PipelineRun lifecycle, invocation, nominal time,
  and parent-child Model execution map to Run facets. Source inputs and
  materialized Model relations map to Datasets; a Model definition is not a
  Dataset solely because it is present in the project graph.
- **OL-03:** Standard schema, dataset-version, quality, statistics, column
  lineage, nominal-time, and parent facets are populated from existing
  LeapView authorities when evidence is supplied. Contract publications reuse
  the [`contractprojection`](../../internal/project/contractprojection)
  canonical projection and [`identityledger`](../../internal/project/identityledger)
  publication evidence; gate, catalog-statistics, and PlanIR values remain
  owned by [`release`](../../internal/release),
  [`catalogstats`](../../internal/analytics/catalogstats), and
  [`planir`](../../internal/analytics/query/planir).
- **OL-04:** The event schema is pinned to OpenLineage 2.0.2. Standard facet
  schema URLs are version-pinned, and LeapView custom facet keys use the
  `leapView_` prefix with version-pinned `LeapView` facet schemas.
- **OL-05:** The projection does not originate contract or delivery identities.
  It uses the existing `contractprojection` authority to verify the RFC 8785
  bytes and SHA-256 digest carried by `identityledger` publication evidence.
  UUID-v5 run IDs are an OpenLineage identifier mapping, not a replacement
  contract or delivery digest.
- **OL-06:** No second lineage package, event graph, collector, network
  client, or transport is introduced. The `Exporter` interface is a narrow
  caller-owned port for a future forwarding implementation and is not a
  collector or delivery guarantee.

## Evidence ledger

| Requirement | Implementation | Validation | Remaining limitations |
|---|---|---|---|
| OL-01 | One refresh-owned `openlineage` projection package with immutable Pipeline/PipelineRun mapping functions. | `go test ./internal/refresh/openlineage -count=1`; `internal/platform/architecture/openlineage_conformance_test.go:TestOpenLineageProjectionUsesPublishedContracts`; `...:TestOpenLineageHasOneProjectionAndNoTransportPath`. | No runtime event dispatcher or external collector is included. |
| OL-02 | Existing Job/Run/Dataset mapper, including Pipeline facets, invocation and nominal-time evidence, and child Model parent references. | Focused OpenLineage package tests for pipeline, scheduled, and child-run mapping; schema validation tests in the package. | Unsupported or absent caller evidence remains absent; no independent dependency graph is emitted. |
| OL-03 | Existing contract publication, gate, catalog-statistics, and PlanIR values are projected into standard facets through exact shared contract imports. | `internal/platform/architecture/openlineage_conformance_test.go:TestOpenLineageProjectionUsesPublishedContracts`; focused package tests for publication, quality, statistics, and column-lineage facets. | Projection does not persist, acquire, or manufacture evidence; callers must provide evidence already authorized by the owning subsystem. |
| OL-04 | Pinned OpenLineage event/facet schemas under [`internal/refresh/openlineage/schema`](../../internal/refresh/openlineage/schema) and versioned custom facet URLs in the projection. | Focused package schema/object-model tests; `internal/platform/architecture/openlineage_conformance_test.go:TestOpenLineageHasOneProjectionAndNoTransportPath`. | This is document-level projection conformance only, not transport/emission conformance. |
| OL-05 | Contract and publication fields are consumed and verified through existing `contractprojection` and `identityledger` authorities; OpenLineage retains only its UUID-v5 run identifier mapping. | `internal/platform/architecture/openlineage_conformance_test.go:TestOpenLineageReusesExistingIdentityAuthorities`; existing contract projection and publication authority tests. | No new canonicalization profile, digest algorithm, or cross-language identity corpus is introduced here. |
| OL-06 | Exact package-scoped architecture allowances and a single projection/transport guard keep lineage ownership in refresh and prohibit network/collector dependencies. | `internal/platform/architecture/openlineage_conformance_test.go`; `go test ./internal/platform/architecture -run 'OpenLineage|ProductionImportsFollowCapabilityGraph|DeclaredCapabilityGraphIsAcyclic' -count=1`. | Forwarding, collector interoperability, import, round-trip, and delivery guarantees remain future capability-gated work. |

The implementation therefore claims only OpenLineage 2.0.2 **projection/document**
conformance for the supported object and facet subset. It does not claim
collector, transport, import, round-trip, or end-to-end emission conformance.
LeapView's append-only identity, delivery, and audit ledgers remain the local
authorities even when their evidence is represented in an OpenLineage event.
