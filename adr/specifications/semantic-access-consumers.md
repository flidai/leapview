# Semantic consumer enforcement — FAI-642 checkpoint

Status: **IMPLEMENTED / PARTIAL qualification; not enabled for production activation.**

Stack: `ganesh/fai-642-consumer-enforcement` branches from
`ganesh/postgres-qualification-hardening` at
`a923b3b9fcb15b9e36914dde2ba6ce8857bac502`. No downstream feature or PR
is included in this checkpoint.

## Consumer inventory and invariant

Compiled requirements → planner barriers → consumer admission → returned data.
Consumer admission does not replace resource authorization or evaluate a
second policy language.

| Consumer | Existing boundary | Checkpoint / remaining work |
| --- | --- | --- |
| Dashboard rows, totals, visualizations, raw values | `queryauthz` governor → materialize | Request-local consumer and physical-plan gate added; complete dashboard equivalence remains unqualified |
| API / Arrow exports | Query authorization → native Arrow executor | Same plan gate; check authority before schema/record delivery; protected HTTP planner success/denial fixtures pass, production activation remains deferred |
| Bundles | Per-branch governor and retained-result reuse | Protected bundles return incompatible before execution; callers must use governed branch execution; protected bundle reuse remains unavailable |
| Filter options | HTTP option cache → governed typed query | Field admission precedes static/dynamic options; shared option reuse disabled when protected or admission capability absent |
| Spatial tiles | Governed query plus immutable-byte cache | Protected immutable-byte lookup/store/coalescing rejected; positive tile coverage remains unqualified |
| API discovery/explain and visual specs | Context-free planner plus resource checks | Model checks, target-authorizer ports, filtered lists and conservative whole-projection denial added; no new evaluator |
| Explore | Authorized project catalog and typed preview executor | Same-snapshot compiled-policy discovery, including auxiliary field metadata; execution converges on materialize |
| Agent / MCP | Shared project catalog and governed tool execution | Catalog visibility callback uses its existing lease; result execution converges on materialize |
| Public/embedded dashboards | Publication-scoped synthetic caller | A document/publication cannot supply authenticated semantic attribute authority; positive principal-bound embedding coverage remains incomplete |
| Background | Agent workloads use existing background admission | Missing authenticated authority denied in focused execution tests; no new scheduler or transport added |
| Source/Model preview | Separately authorized authoring path | No semantic policy evaluator added; protected semantic views reject physical-preview substitution |

## Implemented ownership boundaries

- Access returns principal/group effective attributes, registry and control state
  from one owned PostgreSQL repeatable-read/read-only transaction. It reuses
  existing repository resolution and canonical values. Missing authentication,
  development bypass, inactive principals, cross-subject responses and
  caller-owned transactions fail closed. Trusted-claim source adapters are not
  implemented by this checkpoint.
- Query constructs a detached consumer from the existing compiled planner and
  FAI-639 evaluation context. A private, in-process admission capability is
  propagated only after FAI-641 security sealing. It is not a durable ID, hash,
  serialized field or cache authority. Graph validation, scan/barrier topology
  and exact renderer SQL/arguments/columns are checked before execution against
  the private envelope captured at admission. Re-rendering a mutated projection
  cannot bless it; the existing total-row rewrite validates its input admission
  before retaining provenance. No new hash or canonical representation is used.
- Materialize uses a borrowed request-local planner view, retaining database
  lease ownership. It rechecks principal/registry/control/value evidence before
  execution and buffered-result release, and before Arrow callbacks. Already
  delivered batches cannot be retracted; this is not lifecycle reconciliation.
- Protected result-cache reuse is bypassed. Protected bundles and immutable
  bytes are rejected rather than admitted under incomplete cache identities.
  This does not implement FAI-645 invalidation, reconciliation or cache policy.
- Visual-spec descriptions use one request-bound planner to check every model
  dataset, dimension binding and metric without execution. Because the spec
  projection has no member-filtering contract, an inaccessible unrelated model
  member can conservatively deny the whole description. Document titles/layout
  remain document-authorized metadata; sharing does not authorize query data.

## Deferred production activation boundary — FAI-649

`materialize.Runtime.VerifySemantic` invokes
`Planner.PrepareRepresentativePlans` on the activation planner. Representative
plans call normal governed `Plan`, but activation has no authenticated consumer
context. A registry-aware neutral planner consequently denies protected plans.
The explicit relationship verification helper additionally constructs an
ordinary planner without registry context.

`TestProtectedSemanticActivationVerificationNeedsSeparateCompilerDesign`
reproduces the first boundary with a discovered-schema fixture. A fabricated
principal, unrestricted planner, or skip-security flag would not be an
acceptable repair. A compiler/planner-owned, non-consumer verification design
is needed before production activation can be wired under FAI-649. This does
not transfer activation verification into consumer ownership. The composition root
therefore does **not** call `SetSemanticAccessAuthority` in this checkpoint.

## Evidence and remaining qualification

Focused tests cover coherent Access resolution, consumer allow/deny/missing
authority, principal mismatch, closed runtime, stale control, Arrow denial
before callbacks, rejected protected cache paths, forged/cross-consumer plans,
renderer-envelope tampering (including re-rendered mutations) and preserved
total-row provenance. API fixtures
exercise denied/missing-authority discovery and multiple aliases of one
physical field. These are not an end-to-end production qualification claim.

Remaining qualification boundaries:

1. Production activation and its verification dependency belong to FAI-649;
   no fabricated activation principal or consumer bypass is introduced here.
2. Exhaustive cross-consumer equivalence remains FAI-648, including positive
   public/embedding and provider-backed execution qualification. Protected
   bundles and byte tiles intentionally remain unavailable.
3. Lifecycle reconciliation, protected cache identities and invalidation remain
   FAI-645. This layer denies reuse rather than asserting lifecycle equivalence.
4. Full CI is not green for this checkpoint. Two normal attempts failed in
   unchanged browser tests; isolated retries passed. This is not a waiver of
   the full CI gate.

### Validation checkpoint (2026-09-06)

Commands used the repository's Go 1.25.14 toolchain. PostgreSQL tests ran with
Docker and `LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=1`; they did not skip.

| Command / suite | Result |
| --- | --- |
| `go test -tags=duckdb_arrow ./internal/analytics/query/... ./internal/analytics/materialize ./internal/dashboard/semanticapi -count=1` | PASS; planner provenance, lifecycle, buffered release, Arrow callbacks and HTTP allow/deny fixtures |
| `go test -tags=duckdb_arrow ./internal/dashboard/queryauthz ./internal/dashboard/module ./internal/dashboard/http -count=1` | PASS; discovery/projection ports, filter/spec responses and decorator propagation |
| `go test -tags=duckdb_arrow ./internal/project/module ./internal/project/catalog ./internal/project/http -count=1` | PASS; lease-bound catalog and Explore admission |
| `go test ./internal/access/postgres -run TestResolveSemanticAttributesPostgreSQL18 -count=1 -v` | PASS; three live PostgreSQL 18 tests |
| `go test -race ./internal/access/module -run 'Test.*SemanticAttribute' -count=1` | PASS |
| `go test -race ./internal/analytics/query/... ./internal/analytics/materialize -run 'Test(SemanticAccessConsumer\|SemanticConsumerAdmission\|ProtectedSemantic)' -count=1` | PASS for matching query/materialize tests; no matching PlanIR tests in this selection |
| `go test -race ./internal/analytics/query/planir -count=1` | PASS; complete PlanIR package |
| `go test ./internal/platform/architecture -count=1` | PASS; 21.352s; repeated with `duckdb_arrow` after final HTTP correction, 20.300s |
| `task generated:check docs:check` | PASS |
| `git diff --check` and Go formatting | PASS |
| `task ci` (first attempt) | FAIL after 2:40.98; unchanged windowed-table browser test read `block` from an absent request after a fixed 120ms wait. Isolated `bun run test:windowed-table`: 4/4 PASS; same test passed in second CI attempt |
| `task ci` (second attempt) | FAIL after 4:50.85; unchanged site CLI-outline browser test exceeded its 5000ms test budget, with subsequent browser-closed errors. Admin 21/21 passed. Isolated `bun run test:site:prepared`: 51/51 PASS in 64.08s |

Both CI failures are consistent with existing browser timing/resource
sensitivity, not a demonstrated consumer regression: no browser source,
assertion, timeout, CI configuration or PostgreSQL gate changed. Shared-host
load exceeded 30 with only approximately 2–4 GB memory available and concurrent
Go/build activity from other worktrees. Correlation is not proof of the exact
resource cause. The full workflow therefore remains unqualified.
The second attempt's Go package subprocess continued after Task had already
exited with status 201; it was explicitly interrupted after confirming it
belonged to this worktree. Remaining broad suites are not claimed as passed.
After the final missing-authority HTTP status correction, complete dashboard
HTTP/queryauthz, project HTTP/module and architecture packages passed again
with `-tags=duckdb_arrow -count=1`.

An additional broad optional race run passed materialize and queryauthz but
hit the default ten-minute package budget in an existing Access SQLite SCIM
fixture setup. No race was reported. Focused new Access race tests passed;
the broad result remains a test-budget/environment limitation, not a verified
parent-branch baseline pass.

FAI-641 stays IMPLEMENTED / PARTIAL; FAI-662 remains QUALIFIED on its existing
evidence. FAI-645 is not started: this checkpoint does not establish complete
consumer/lifecycle equivalence. FAI-632 remains blocked by the broader ADR
qualification chain, including FAI-648/649. Migration history, publication
evidence and canonicalization authorities are unchanged.
