# Semantic access cache/lifecycle — FAI-645

Status: IMPLEMENTED / PARTIAL; focused cache/lifecycle qualification passes. This is the
requested cache/lifecycle slice, not completion of the consolidated issue's
diagnostic/audit or production cutover scope.

Stack: `ganesh/fai-645-cache-lifecycle-reconciliation` directly above
`ganesh/fai-642-consumer-enforcement` at
`b2c19581495a835de9a8e2712d37a2f194ff346e`. No downstream PR or milestone.

## Inventory and ownership

| Artifact | Owner / key | Authorization and lifecycle boundary |
| --- | --- | --- |
| Buffered query results / dashboard rows, totals, raw values | `materialize.queryResultCache` key: existing partition, result dependency, effective-policy fingerprint and governed query; retention in `resultcache.Scope` | Protected reuse adds consumer-derived semantic evidence to the existing dependency; checks before lookup, store and delivery |
| Shared Arrow execution | `resultcache.ExecutionScope`, generation plus exact result key | Same security dependency separates flights; every waiter revalidates before delivery; scope close drains generation work |
| Compiled semantic model / planner | Activation-owned compiled model and serving runtime | No separately discovered reusable request-plan cache; FAI-642 private plan admission remains mandatory, including cache hits |
| Bundles | Existing per-branch result keys and generation-local flights | Protected bundles remain incompatible; use governed individual queries |
| Spatial/opaque byte artifacts | Generation-owned immutable-byte scope and caller-generated tile key | Existing interface lacks complete authenticated lifecycle context; protected lookup/store/coalescing remains rejected |
| Filter options / suggestions | `dashboard/filter.OptionCache`, option context, dependencies, search and cursor | Protected shared cache remains disabled; request-local option cache and FAI-642 field admission remain |
| API responses / Arrow export | Existing semantic API and unbuffered Arrow consumer | Direct API queries bypass retained caching. Dynamic dashboard filter-options API currently uses dashboard query metadata and can enter the same governed Arrow cache; it does not bypass materialize admission. FAI-642 checks remain before delivery |
| Explore / agent / MCP results | Existing catalog lease and governed execution path | Agent query surface is excluded from result-cache admission; no separate suggestion cache found. Explore and shared query execution retain their existing surface policy |
| Dashboard refresh memo / sessions | Refresh-lease memo and principal/client/project/dashboard/serving/stream session key | Request/lease memoization and UI filter state, not retained analytical results; existing session CAS/expiry unchanged |
| API command replay | Existing protocol idempotency scope: principal/credential, method/path/key and body digest | Command-only replay, not query-result caching; existing replay reauthorization remains |
| Managed runtime views / extension artifacts | Immutable manifest/revision tree and verified artifact digests | Physical artifact caches, not semantic query results; existing leases, integrity checks and GC unchanged |
| Rollup / preaggregation | No reusable semantic rollup store found; catalog-statistics rollups are SQL CTEs | No protected rollup substitution admission added |

## Identity invariant

A protected cached artifact is reusable only under equivalent current
authorization and lifecycle evidence. A cache hit is not an authorization
decision and never substitutes for FAI-639 evaluation or FAI-641 barriers.

The existing `resultidentity.NewDependency` adds an optional, explicit
`semanticAccess` projection. Its existing serializer and digest domain are
unchanged; nil preserves historical unprotected canonical bytes and digests.
No publication, graph, release or semantic-value hashing behavior changes.
Dependency format v1 remains intentionally additive: protected entries did not
exist under FAI-642, the optional field participates in the existing digest,
and unprotected golden bytes remain identical. Consumers use the complete
opaque dependency digest, not a decoder that discards unknown fields.

Inputs:

- Existing instance-qualified authored semantic-model identity and kind,
  plus the existing authorization project scope matched to the result partition.
- Existing append-only resource history sequence and active bundle.
- Exact immutable publication version, profile and digest.
- Existing serving-state identity and principal identity.
- Role/grant control revision captured by the activation's authorization
  snapshot; a later live revision cannot be silently blessed under that snapshot.
- Semantic policy profile, registry revision/digest and attribute-control
  revision/digest, plus sorted definition/version/type/shape/source/value-digest
  projections. No raw attribute values or claim envelopes enter this projection.
- Existing semantic-model dependency digest covers authored grants and filters;
  existing execution, relation and binding identities remain. The existing
  query fingerprint retains actor/credential/mode isolation.

Timestamps, TTL and cache-generated identity counters are not authorization
evidence. Configured lifecycle evidence is copied at runtime construction and
cannot be inferred from the first cache lookup. Missing evidence preserves
uncached FAI-642 execution; malformed or stale configured evidence fails closed.

## Reconciliation

| Event | Required action / evidence |
| --- | --- |
| Role or grant mutation | Existing instance control revision changes; reject a cache request under the old activation revision |
| Semantic assignment or group resolution change | Existing coherent resolution changes control/value evidence; old dependency cannot be reused |
| Trusted claim change or revocation | Existing mapping/control revision participates; sources without a live trusted-claim cache authority are not admitted for reuse |
| Registry change | Existing compiled registry match fails closed; requires a valid new compile/activation context |
| Tombstone | Live ledger state is not ACTIVE; reject reuse and delivery |
| Restore | Existing history sequence increases; old context remains rejected even for the same authored ID and bundle |
| Publication replacement / rollback | Exact publication evidence and active bundle/history sequence must match activation; historical immutable publication is not permission to reuse an old lifecycle context |
| Wrong instance or kind | Reject identity/publication scope mismatch; never rebind by authored ID alone |
| Concurrent read/write and authority change | Validate around lookup/store/delivery; remove the addressed entry on detected mismatch; existing scope generation fencing handles concurrent invalidation |

This is read-through reconciliation, not an event delivery subsystem. Old
entries may remain physically retained until normal eviction when they are
never requested again, but their old identities cannot satisfy current
admission. When a cache-boundary guard observes stale admission, the addressed
entry is removed using the existing scope API. Earlier lifecycle preflight
can reject before an address is derived. TTL is not the safety mechanism.

The ledger reader observes identity, history sequence and immutable publication
in one PostgreSQL repeatable-read/read-only transaction. The composition adapter
reads the separate monotonic Access revision before and after that observation;
a crossing control mutation fails closed. Consumers recheck again at the
execution/result boundary. This does not promise atomic revocation of data
already delivered, or a distributed transaction spanning client delivery.
An authority mutation may occur between the pre-store check and memory store;
the post-store check removes that old-context entry. Even during that window,
all protected readers and coalesced waiters must pass their own current-state
checks. The guarantee is guarded reuse/delivery, not atomic cross-store writes.

## Remaining boundaries

FAI-641 and FAI-642 remain partially qualified. FAI-648 owns exhaustive
cross-consumer/plan-shape qualification. FAI-649 still owns production semantic
activation and the neutral verification design gap: this layer does not wire
production activation or fabricate an activation principal. Protected bundles,
opaque bytes and shared option-cache reuse remain unavailable rather than
being admitted without evidence. FAI-632 remains blocked by the broader chain.

The consolidated FAI-645 issue also covers durable audit/diagnostic expansion
and policy lifecycle approval; those were not requested here and are not
claimed complete. Existing audit/cache observations remain in use.

## Validation

Commands below ran with the repository toolchain. PostgreSQL commands used
Docker with `LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=1`; they did not silently
skip live database coverage.

| Command / scope | Result |
| --- | --- |
| `go test -tags=duckdb_arrow ./internal/analytics/resultidentity ./internal/analytics/resultcache ./internal/analytics/query/... ./internal/analytics/materialize ./internal/dashboard/queryauthz ./internal/dashboard/semanticapi -count=1` | PASS: cache, planner and consumer integration |
| `go test ./internal/project/identityledger/... -count=1` | PASS: live PostgreSQL 18 lifecycle/publication suite |
| `go test ./internal/access/snapshot ./internal/access/module ./internal/access/postgres -run 'Test(.*ControlRevision\|.*Control.*PostgreSQL18\|.*AuthorizationControl\|.*FromControlState)' -count=1 -v` | PASS: live role/grant revision reader and snapshot qualification |
| `go test ./internal/access/module ./internal/project/module ./internal/analytics/resultidentity -run 'Test(AuthorizationControlRevisionModule\|SemanticCacheLifecycle\|SemanticDependency)' -count=1` | PASS: authority adapter and deterministic identity |
| `go test -race ./internal/analytics/materialize ./internal/analytics/resultidentity ./internal/analytics/resultcache -run 'Test(ProtectedSemanticCache\|QueryCacheGuard\|QueryCacheInvalidationRacing\|SemanticDependency\|ExecutionScope)' -count=1` | PASS: final cache race qualification |
| `go test -race ./internal/access/module ./internal/access/snapshot ./internal/analytics/resultidentity ./internal/project/module -run 'Test(AuthorizationControlRevision\|FromControlState\|SemanticDependency\|SemanticCacheLifecycle)' -count=1` | PASS: authority and identity race checks |
| `go test ./internal/access/snapshot ./internal/platform/architecture -count=1` | PASS: final snapshot serialization and architecture checks |
| `task generated:check docs:check` | PASS |
| `task ci` | PASS (exit 0, 22m54s): normal full PR contract, including admin 21/21, site 51/51, broad Go suites and mandatory live PostgreSQL conformance |

Event evidence is deliberately split between authority and cache-boundary tests:

- `TestControlAuthorityPostgreSQL18` proves actual role/grant mutations advance
  the existing revision; `TestProtectedSemanticCacheRejectsLifecycleTransitions`
  proves a changed authorization revision rejects reuse.
- `TestProtectedSemanticCacheRejectsStaleAuthoritySnapshots` covers registry,
  control and effective attribute changes. Claim revocation is tested by
  `TestProtectedSemanticCacheClaimRevocationBypassesThenDenies`; no retained
  claim-backed entry is admitted.
- `TestReadLifecycleEvidenceTracksRestoreAndRollback` proves live ledger
  transitions, including tombstone, preserve publication evidence while
  advancing history. `TestProtectedSemanticCacheUsesNewBindingAfterRestoreOrRollback`
  proves sequence-only changes require a new activation context and a cache
  miss even when the original bundle and publication are reused.
- `TestSemanticCacheLifecycleAdapter` rejects inactive resources, mismatched
  publication/scope and crossing control mutations.
- `TestSemanticDependencyRotatesForSecurityAndLifecycle` and the existing
  unprotected golden test prove isolation without changing ordinary digests.
- Concurrent read/control update, miss/lifecycle update, post-store rejection
  and invalidation are exercised by the focused `-race` command above.

Two intermediate architecture failures were new integration defects: an
analytics-to-Project validation dependency and an obsolete cache-bypass marker.
Both were corrected without relaxing ownership rules or security assertions;
the final architecture suite passes.

The final full CI run had no remaining regression, baseline failure or
environment limitation. Passing CI does not upgrade FAI-641/642 or complete
the consolidated FAI-645 acceptance criteria. FAI-648, FAI-649 and FAI-632
remain unstarted with their Linear dependency relationships unchanged.
