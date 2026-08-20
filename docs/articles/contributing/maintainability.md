# Maintainability refactor contract

LeapView favors explicit capability ownership and behavior-preserving extraction over arbitrary file-size limits. A large file is a refactor candidate when it combines several reasons to change, independent state machines, optional production dependencies, or unrelated side effects. Cohesive state machines and exhaustive translators may remain large when splitting them would hide their invariants.

## Required workflow

For a structural refactor:

1. Identify the public API, generated contract, transaction boundary, lifecycle ordering, and observable diagnostics that must remain stable.
2. Add characterization coverage before moving behavior that is only tested indirectly.
3. Extract along a domain family, state-machine boundary, lifecycle collaborator, or generation phase. Prefer another file in the owning package before creating another package.
4. Keep authoritative transitions in their owning coordinator. Do not distribute a state machine among generic helpers.
5. Run the focused package or browser tests, generated checks when contracts are involved, `git diff --check`, and `task ci`.

Every refactor must preserve capability ownership, the declared acyclic dependency graph, CSRF and authorization boundaries, deterministic generation, error contracts, and exactly-once cleanup.

## Audited extraction boundaries

| Surface | Intended seam | Invariants and focused verification |
| --- | --- | --- |
| `internal/app/composition.go` | Capability-specific typed builders | Construction and cleanup order; production dependencies fail closed; `go test ./internal/app/...` |
| `internal/app/runtime_router.go` | Feature route bundles | Route stability, authorization, audit, explicit development bypass; `go test ./internal/app/...` |
| `internal/access/http/handler.go` | Current principal, tokens, sessions, principals, service principals, groups, audit | Generated API, authorization, redaction, audit and status codes; `go test ./internal/access/...` |
| `internal/deployment/sqlite/plan_delivery_repository.go` | Plan/build, seal/candidate, publication/generation, leases/retention, GC | SQLite transactions, compare-and-set and idempotency; `go test ./internal/deployment/...` |
| `internal/project/ui/develop.go` | Project list, connections, asset detail, lineage, refresh and versions | Typed signal shapes, CSRF commands and rendering; `go test ./internal/project/...` |
| `internal/dashboard/semanticapi/handler.go` | Semantic resource and query operation families | Governed-query authorization and generated responses; `go test ./internal/dashboard/...` |
| `internal/runtimehost/manager.go` | Lease heartbeat/release queue and cleanup workers | Manager remains the generation authority; exactly-once close and drain; runtime-host race tests |
| `web/components/dashboard/dashboard-page.ts` | Navigation, filter, optimistic-interaction and agent controllers | Signal/event/URL compatibility, rollback and listener disposal; focused dashboard DOM tests |
| `web/components/data/data-explorer.ts` | Query-builder, panel and agent-state controllers | Governed commands, reset behavior, pointer cleanup and safe storage decoding; focused explorer tests |
| `web/components/dashboard/table/report-table.ts` | Virtual blocks, selection, columns and formatting | Stale-result rejection, event and keyboard behavior; focused table tests |
| `internal/dashboard/compiler/document_compile.go` | Visualization-family compilation | Canonical IR, deterministic diagnostics and result schemas; compiler tests and generated checks |
| `internal/analytics/query/planir/graph.go` | Build, normalize, validate and traverse phases | Graph validity and deterministic ordering; query planner tests |
| `pkg/duckdbsql/decode.go` | AST node families | Exhaustiveness, locations and diagnostics; `go test ./pkg/duckdbsql/...` |
| `pkg/apigen/typespec/src/on-emit.ts` and `pkg/apigen/emit/servergo/servergo.go` | Discovery, normalization, validation, naming, rendering and emission | Byte-stable generated artifacts; generator tests and `task generated:check` |
| `pkg/workload/workload.go` | Admission/fairness, deadlines/cancellation and accounting | Queue order, balanced release, shutdown and leak freedom; workload race tests |
| `site/web/site-page.ts` | Stable shell, routes and browser interactions | URLs, metadata, accessibility and listener disposal; site build and route QA |
| `desktop/src/application.ts` | Startup, authentication/navigation, window ownership and shutdown | Security model, recovery and exactly-once cleanup; desktop CI lane |

## Review signals

Useful review signals are reduced reasons to change, narrower test setup, explicit dependencies, locally visible transitions, and smaller diffs for ordinary product work. Line count alone is not an acceptance criterion. Generated files are evaluated at their source generator, not as handwritten hotspots.
