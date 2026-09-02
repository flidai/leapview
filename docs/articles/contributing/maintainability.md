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

## Ratcheting engineering-quality budgets

`task quality:budget:check` scans tracked and newly authored Go and TypeScript/JavaScript files. Generated, compiled, and vendored sources are excluded. The required Go-package CI lane runs the same check for every pull request and merge-queue candidate.

The policy in `.quality/engineering-budget.json` measures three dimensions for each source category:

| Category | Per-file review threshold | Why it is tracked |
| --- | ---: | --- |
| Go production | 800 lines | Flags production files likely to combine several reasons to change. |
| Go tests | 1,200 lines | Keeps broad characterization suites visible without applying the production threshold. |
| TypeScript/JavaScript production | 700 lines | Flags stateful UI or tooling modules that may need purpose-owned controllers. |
| TypeScript/JavaScript tests | 1,200 lines | Keeps large browser-contract suites visible while allowing cohesive scenario coverage. |

For every category, CI rejects increases in the number of files over the threshold, total lines above the threshold, or the largest authored file. Existing hotspots remain visible as debt; they are not retroactively declared well-sized. The gate also rejects increases in reviewed quality suppressions such as `nolint`, `nosec`, `eslint-disable`, and TypeScript ignore directives.

This is a ratchet, not an instruction to split code mechanically. A file may cross a review threshold only through an explicit policy change that explains why its invariants are clearer together. Reducing one hotspot does not silently authorize growing another: run `task quality:budget:update` after an improvement to tighten the committed baseline. The update command refuses increases by default.

When the gate fails:

1. Read the reported category and hotspot list.
2. Prefer extracting a purpose-owned collaborator or removing the new suppression.
3. Run focused characterization coverage and `task quality:budget:check`.
4. If cohesion genuinely requires a larger surface, review the policy increase explicitly; do not use the normal update task to hide it.

The budget intentionally does not grade naming, domain cohesion, or architectural correctness. Those remain review responsibilities backed by the ownership and behavior-preservation contracts on this page.

`task quality:trends:report` complements the fail-closed budgets with the ten highest Go decision-point counts and the most frequently changed authored source files across the last 200 first-parent commits. The report is diagnostic evidence: complexity and author count trigger boundary review, but do not mechanically reject a cohesive implementation.

### Reviewed exceptions

`.quality/engineering-exceptions.json` is the durable record for a cohesive authored file that intentionally remains above its review threshold. Every entry is path-scoped and must name its architectural kind, accountable owner, specific reason, review date, next review deadline, and a hard maximum line count. `task quality:exceptions:check` fails when the file grows past that ceiling, disappears without policy cleanup, or passes its review deadline.

Generated sources do not use authored-file exceptions. They are excluded only when their generated suffix, generated directory, or leading generated-file header makes the source boundary independently verifiable; the generator remains the reviewed implementation surface.

### Current decomposition evidence

The first governed extraction moves bootstrap, connection-scope, command-failure, and delivery-authorization policy from `internal/app/runtime_router.go` into `internal/app/runtime_router_policy.go`. The code remains in package `app`, so the change introduces no new dependency edge; existing bootstrap and canonical-authorization characterization tests protect behavior. The router hotspot falls from 2,277 to 1,890 lines, and the committed Go-production excess-line budget tightens by 387 lines.

## Engineering-quality delivery plan

The current baseline supports five independently verifiable milestones:

| Milestone | Deliverable | Verification |
| --- | --- | --- |
| Budget foundation | Ratcheting hotspot and suppression debt with actionable CI failures. | `task quality:budget:check` |
| Critical-package qualification | Explicit coverage floors and race expectations for access, runtime hosting, architecture enforcement, and project compilation. | `task quality:critical:coverage` and `task quality:critical:race` |
| Trend evidence | Current Go decision-point complexity and rolling ownership-change concentration for authored Go and browser source. | `task quality:trends:report` |
| Exception governance | Path-scoped, owned, justified, and review-dated exceptions for cohesive roots and generated sources. | `task quality:exceptions:check` |
| Targeted decomposition | Characterization-backed extraction of the highest-risk mixed-responsibility composition or routing root. | Focused package tests, architecture checks, and `task ci:full` |

The milestones are ordered dependencies. Measurement and exception rules land before structural extraction so each decomposition tightens an observable baseline. Coverage floors run in the pull-request package lane. Race expectations run in the full merge lane because the tagged compiler race suite takes roughly 106 seconds on the revalidated baseline.
