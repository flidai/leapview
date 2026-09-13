# FAI-671 isolated runtime boundary evidence

This records the bounded FAI-671 implementation slice of
[ADR-0018](../0018-retain-project-as-the-durable-deployment-namespace.md), not
final Project namespace conformance. FAI-670 and FAI-671 are now merged; the
non-exhaustive inventories below remain explicit evidence limitations.

## Extraction inventory

Base: `a97e07e35abb11a9f97052c28a3d8a1855c850bb`. Historical commit
`9fa6851e1a7108af1e4b93802e817614b3d0fedd` is a reference, not a merged branch.

| Historical surface | Treatment in this slice |
| --- | --- |
| `internal/access/module/apigen.go` and boundary tests | Adapt locator rejection at the existing authorizer |
| `internal/app/runtime_router_routes.go` and boundary tests | Adapt shared query-selector rejection |
| `internal/app/composition.go` and claim tests | Adapt validation of existing durable claim evidence |
| `internal/analytics/cache/key_test.go` | Extract and extend partition assertions into `project_boundary_test.go`; no cache production changes |
| `internal/app/api/protocol/project_boundary_test.go` | Reuse locator partition and replay reauthorization coverage |
| Historical evidence matrix / architecture link test | Replace with this limited matrix; do not import wider completion claims |
| Historical delta map and API operation-count updates | Exclude |
| All `internal/deployment/module/*` changes | Exclude, including rollback and publication replay |
| All `internal/release/*` changes | Exclude, including finalization and candidate provenance |

There is no ResourceUID registry, activation, rollback, deployment binding,
schema, migration, compiler identity, or canonical contract digest change.

## Boundary matrix

| Boundary | Existing authority and changed omission | Maintained evidence |
| --- | --- | --- |
| API-02 shared ingress | Singleton claim/runtime stays authoritative; reject query Project selectors before domain dispatch. Preserve the exact platform audit filter and bootstrap body. This does not claim to audit every request body. | `TestProjectBoundaryRejectsRequestSelectorsBeforeDispatch`, `TestProjectBoundaryPreservesBootstrapAndPlatformAuditFilter` in [app tests](../../internal/app/project_boundary_test.go) |
| API-03 generated locators | Principal/platform authentication alone did not constrain Project locators. Resolve the existing server-bound `CurrentProjectID`, reject disagreement with a populated runtime-host identity, and leave resource snapshot and bootstrap/delivery authorizers intact. A valid resolver does not require an allocated runtime host. | `TestAPIGenProjectBoundaryPrincipalAndPlatformLocators` in [authorizer tests](../../internal/access/module/project_boundary_test.go); `TestProjectBoundaryGeneratedLocatorsCannotRetarget` in [app tests](../../internal/app/project_boundary_test.go); existing `TestPostgresRefreshRouteJourney` in [PostgreSQL journey tests](../../internal/app/postgres_refresh_journey_test.go) |
| API-07 startup | Existing claim reader checks environment; now also validates the claim's identity and durable evidence. | `TestReadClaimedProjectFailsClosedAndChecksEnvironment` in [claim tests](../../internal/app/composition_project_claim_test.go) |
| API-06 cache partition | Existing sealed partition includes target, Project, environment, and candidate identity; preserve the dependency-addressed cache design. No UID or generation token is substituted for contract/data identity. | `TestProjectBoundaryCacheKeysDoNotCollide`, `TestProjectBoundaryCacheRejectsMissingScope` in [cache tests](../../internal/analytics/cache/project_boundary_test.go); policy rotation in [key tests](../../internal/analytics/cache/key_test.go) |
| API-06 idempotency | Existing protocol partitions by authenticated caller/credential, method, and path, validates request digest, and reauthorizes replay. Protocol-only tests do not claim multiple Projects can be served by one instance. | `TestProjectBoundaryIdempotencySeparatesLocatorsAndReauthorizes` in [protocol tests](../../internal/app/api/protocol/project_boundary_test.go) |

## Remaining boundaries

FAI-670 owns ResourceUID allocation, tombstone/restore audit and lifecycle
qualification. Those guarantees are merged and independently evidenced; this
slice does not assess or change them.
Release/deployment/rollback, storage/retention/cleanup, and the exhaustive
catalog/lineage/audit/metadata/error-surface audit remain outside this extraction.
Authorization and generation evidence at cache consumers still require the
remaining end-to-end FAI-671 audit; partition tests alone do not prove it.

Project is a product namespace, not a claim of same-process multi-Project
isolation. Existing instance, claim, serving identity, and authorization
mechanisms are preserved; no parallel Project-context abstraction is added.
