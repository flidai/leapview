# Semantic-access planner enforcement

Status: implemented planner slice; broader qualification pending

Issue: FAI-641. Governing decision: [ADR-0017](../0017-adopt-a-looker-aligned-semantic-access-contract.md).

## Authority and invariant

`NewSemanticAccessPlanner` binds a detached evaluation context to a registry-aware
`CompiledModel`. The existing FAI-639 evaluator remains the sole decision owner.
The ordinary constructor cannot compile a protected model without that authority;
a protected compiled planner without evaluation context fails closed.

After existing source lowering and bundle sharing, `securePlanGraph` evaluates
dataset and requested member/lineage requirements and inserts one distinct
`planir.SecurityBarrier` immediately above each source occurrence. In a
policy-bearing model this conservatively includes unrestricted sibling datasets:
their barriers have no row predicate but preserve occurrence identity. Models
without semantic-access requirements retain their existing plans.

Implicit relationship targets become explicit scan/barrier inputs to the existing
`TraverseRelationship`. Routed access filters retain the compiled typed route and
use correlated `EXISTS` over independently governed targets. No security SQL is
constructed by the planner. The existing DuckDB renderer lowers typed predicates
to placeholders and materialized barrier CTEs before ordinary filters, joins,
aggregation, derived operations, and envelopes. Parameter order follows CTE order.
Security-only fields and route join keys participate in existing dependencies.

Each protected scan has a private requirement shared with exactly one barrier.
Validation rejects direct scan consumers, absent/duplicate barriers, and changed
source relations, predicates, routes, or barrier metadata. The completed lowering
seals scans, barriers, and explicit traversals using structural comparisons, not
a new hash or canonicalization authority. `ValidateSecurityRewrite` also compares
the original and proposed graph's retained private evidence. This is an in-process
planner invariant, not a serialized authorization credential or execution token.

## Transformation audit

| Transformation | Rule |
| --- | --- |
| Source lowering, relationship expansion, aliasing | Occurs before sealing; each resulting occurrence receives its evaluated barrier. New scans or implicit traversals after sealing reject. |
| Bundle/coalesced source sharing | Existing planner shares before security placement; one shared physical scan has one effective barrier. |
| Predicate pushdown, reorder, simplification | Ordinary predicates may change downstream of barriers. Moving, deleting, or changing a security predicate rejects, even if claimed equivalent. No security-pushdown proof optimization is implemented. |
| Join reorder or target substitution | Sealed explicit traversal signatures are immutable. Rewrites changing those edges reject. |
| Projection pruning | Downstream projections may change; pruning the protected scan/barrier metadata rejects. |
| Subquery flattening | Barrier source remains materialized; no planner rewrite may replace it with an ordinary filter. |
| Aggregation and metric expansion | Existing expansion precedes placement; aggregate/derived inputs consume barrier outputs. Required metric/dimension grants cannot be bypassed by output aliases. |
| Totals, spatial and analytical envelopes | Existing graph wrappers retain the seal. Tests cover totals, histogram/distribution, spatial metadata and aggregate tiles; source-producing spatial builders invoke the same placement boundary. |
| Rollup/cache/source substitution | Post-seal substitution rejects. Admitting substitutes under a matching lifecycle/authorization identity is deferred to FAI-645. |

## Verification and limits

Focused commands:

```sh
go test ./internal/analytics/query/planir ./internal/analytics/query -count=1
go test -tags=duckdb_arrow ./internal/project/compiler -count=1
go test ./internal/platform/architecture -count=1
```

`planir/security_test.go` includes exact SQL goldens plus DuckDB execution for
inner/left/right/full joins, self-join occurrences, many-to-many cardinality,
parameter ordering, totals, and rejection of weakening rewrites.
`security_plan_test.go` checks actual restricted results for local, routed,
multi-hop, reverse and sibling-alias filters, pre-null-extension filtering,
member grant denial, deterministic canonical plans, derived metrics, row/raw/count
planning, analytical/spatial envelopes and shared bundle sources. The architecture test guards reuse of the
typed evaluator and existing renderer path.

Known limits are explicit: cyclic access-filter dataset dependencies reject
instead of being approximated; ambiguous routes reject; existing fanout safety
restrictions remain in force (low-level many-to-many execution does not enable
unsafe authored aggregate traversals). Sealing intentionally rejects otherwise
plausible source rewrites until a preservation proof exists. Direct model-relation
resolution is still a trusted planner option, not new rollup/cache admission.

This is not consumer enforcement, discovery/suggestion authorization (FAI-642),
control-revision/cache/lifecycle qualification (FAI-645), exhaustive consumer
equivalence (FAI-648/649), or live PostgreSQL 18 qualification. FAI-632 is not
started. No authority, schema, transport, or publication behavior from completed
ADR-0016 layers changes.

## Qualification record (2026-09-06)

| Command | Result |
| --- | --- |
| `go test ./internal/analytics/query/planir ./internal/analytics/query -count=1` | Passed, including DuckDB restricted-result tests. |
| `go test -tags=duckdb_arrow ./internal/analytics/query/... -count=1` | Passed. |
| `go test -tags=duckdb_arrow ./internal/project/compiler -count=1` | Passed. The initial invocation without the required build tag failed fixture initialization; this was a command configuration error, not a regression. |
| `go test ./internal/platform/architecture -count=1` | Passed. |
| `go test -race ./internal/analytics/query/planir ./internal/analytics/query -run 'SecurityBarrier\|SecurityPlanner\|SemanticAccessPlanner' -count=1` | Passed. |
| `task generated:check` | Passed; no public generated snapshot changes. |
| `task docs:check` | Passed. |
| `task ci` | Frontend, APIGen, Go package sweep and all four application shards passed. The run then failed at the required PostgreSQL 18 gate with Docker socket permission denied (environment limitation, not a green full-CI result). The final added derived-result assertion was separately rerun in the complete query suite and focused race suite. |
| `go vet ./internal/analytics/query/planir ./internal/analytics/query` | Passed. |
| `gofmt -l internal/analytics/query` and `git diff --check` | Passed, no formatting or whitespace findings. |
| `task test:go:postgres-conformance` | Environment-blocked: required PostgreSQL 18 container cannot access Docker socket (permission denied). Gate unchanged; no live PostgreSQL pass claimed. |

The live Linear audit keeps FAI-641 separate from its consumer successor. FAI-642
is Backlog and blocked by FAI-641/636/639. FAI-645 is Backlog and blocked by
FAI-639/636 (it does not currently have a direct FAI-642 blocking edge). FAI-648
depends on FAI-641/642/637/645; FAI-649 depends on FAI-648/616. FAI-632 still
requires FAI-648/649 plus its other active ADR-0016 prerequisites. No downstream
issue is started by this layer. FAI-639 remains In Progress in Linear despite its
source-reviewed parent commit; this audit does not silently mark earlier layers
qualified.
