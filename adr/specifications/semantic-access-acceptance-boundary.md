# Semantic-access acceptance boundary — FAI-648

Status: **IMPLEMENTED / PARTIAL qualification; not a production enablement**.

This acceptance layer is stacked on `ganesh/fai-648-ci-qualification-fixes` at
`661213b26965db83b9ede1f0b50351207632b7fc`. The parent passed canonical hosted
[CI run 34191713543](https://github.com/flidai/leapview/actions/runs/34191713543)
on Ubuntu 24.04 in 21m21s. That result is parent evidence, not a test result for
this layer and not production deployment evidence.

## Meaning of admission

This is the proposed bounded activation input for FAI-649, not a runtime
allowlist, new authority, or waiver of ADR-0017. **No protected production
profile is enabled today.** A positively qualified component below can be an
activation building block; it is not independently sufficient to admit a
provider, route, or query. Production composition and admission verification
remain required. The normative matrix stays **55 PASS / 45 PARTIAL / 2 FAIL**.
Unqualified combinations do not become supported merely by combining passing
unit tests. Narrowing this profile does not mark the broader Linear acceptance
criterion, “every consumer and plan shape,” complete.

### Bounded profile and exclusions

| Dimension | Positively exercised building block | Excluded from proposed initial activation | Evidence and admission limit |
| --- | --- | --- | --- |
| Attribute provider | Existing authenticated principal context with Access-owned durable direct/group assignments, typed registry and coherent resolution | Claim-derived SAML, OIDC, embed and service-token attributes; request-supplied attributes; development bypass or synthetic publication principal | [semantic_attribute_resolution_test.go](../../internal/access/module/semantic_attribute_resolution_test.go), [semantic_qualification_postgres_test.go](../../internal/project/module/semantic_qualification_postgres_test.go). This selects an attribute source, not a new authentication mechanism. Crypto envelope fixtures do not qualify a real provider adapter. |
| Query | Registry-bound dataset/member grants and typed filters; single governed aggregate/count, rows and raw values | Arbitrary SQL/physical preview substituted for a protected semantic query; unsupported rollup substitution; protected multi-query bundle execution | [security_plan_test.go](../../internal/analytics/query/security_plan_test.go), [semantic_consumer_test.go](../../internal/analytics/materialize/semantic_consumer_test.go). Aggregate restriction executes against DuckDB; topology-only tests do not prove every output mode. |
| Plan | Sealed source occurrences with distinct SecurityBarrier nodes before ordinary downstream operations; tested relationship and joined-side null-extension cases | Security-barrier removal/mutation, post-seal source substitution, unrestricted representative-plan fallback | [security_plan_test.go](../../internal/analytics/query/security_plan_test.go) and [planner specification](semantic-access-planner.md). Derived, histogram, distribution and spatial planner fixtures are component evidence only; not blanket consumer admission. PLN-02/04/07/09 remain PARTIAL. |
| Consumer | Request-bound shared materialize execution; governed dashboard/API seams; Arrow delivery with authority checks before callbacks; persisted semantic audit | Public/embedded synthetic callers as semantic principals; unattended callers lacking authenticated authority; unrestricted alternate executors | [semantic_consumer_test.go](../../internal/analytics/materialize/semantic_consumer_test.go), [semantic_audit_surfaces_test.go](../../internal/analytics/materialize/semantic_audit_surfaces_test.go), [consumer inventory](semantic-access-consumers.md). Explorer/agent/MCP/catalog reuse is compositional evidence, not route-by-route production qualification. |
| Export | Governed typed Arrow result delivery using the existing consumer boundary | Protected opaque byte/tile exports, bundle exports, physical-preview bypass and any alternate export bypassing admission | [semantic_consumer_test.go](../../internal/analytics/materialize/semantic_consumer_test.go). Contract metadata exports such as ODCS/OpenLineage are not permission to export query results. Already delivered Arrow records cannot be retracted. |
| Cache | Optional guarded buffered/Arrow result reuse with activation-bound identity/publication/registry/control evidence and durable assignments; uncached governed execution when cache configuration is absent | Protected bundle and opaque-byte reuse; shared protected option-cache reuse; claim-source retained reuse without a live cache authority; new rollup caches | [semantic_cache_test.go](../../internal/analytics/materialize/semantic_cache_test.go), [cache inventory](semantic-access-cache-lifecycle.md). Unsupported cache-source reuse is bypassed, not an authorization bypass. Stale configured lifecycle state rejects execution/delivery. Production cache binding is not wired. |

### Fail-closed contract, not a documentation-only gate

Existing tests prove missing authority, stale policy/control/lifecycle state,
forged plans, protected bundles and immutable-byte paths reject; source kinds
without a matching trusted verifier cannot produce an admitted envelope.
The [provider qualification tests](../../internal/access/trustedclaims/provider_qualification_test.go)
prove the generic envelope boundary, not provider signatures or production
configuration. Source type recognition is not provider admission.

Some excluded combinations have only component coverage, not a distinct runtime
capability switch. This document does **not** assert an exclusion is enforced by
its prose. FAI-649 must prove that its actual enabled routes cannot reach those
combinations, using existing admission boundaries or leaving the surface
disabled. It must not simply enable every shape accepted by the planner. Until
that verification exists, these paths remain **unsupported**, not partially
supported production capabilities. Existing legacy DataPolicy behavior remains
unchanged; this profile does not retrospectively authorize or disable it.

## FAI-616 control-boundary acceptance

**Authority/removal boundary: implemented, with executable qualification.**
The live issue requires removal of authored/public Project and control-resource
authority, not creation of a new public role-assignment mutation API. Its
absence is therefore not evidence of a second authority or a missing removal.
This finding does not promote the entire issue or claim public write support.

| Surface | Supported ownership / API | Explicit boundary and evidence |
| --- | --- | --- |
| Roles and role assignments | `access.ControlStore` and PostgreSQL own instance-qualified assignment CRUD/revocation, audit and revision CAS. Platform-wide administration roles remain separate. Public `GET /api/v1/roles` lists canonical role definitions, not assignments. | No public role-assignment mutation operation or admin command is exposed; repository methods are not HTTP endpoints. [control.go](../../internal/access/postgres/control.go), [access.tsp](../../api/typespec/access.tsp), [access_administration.go](../../internal/admin/settings/access_administration.go). `TestControlAuthorityPostgreSQL18` exercises the durable authority. |
| Grants | Generated instance-bound `/api/v1/grants` list/create/get/update/delete dispatches to Access control handlers; request actor, internal target scope, idempotency and revision evidence are retained. | No authored grant or browser-selected instance authority. [authorization_grants_test.go](../../internal/access/http/authorization_grants_test.go), [grant_dispatch_test.go](../../internal/access/module/grant_dispatch_test.go) cover dispatch, concurrency and instance scope. |
| Removed authored/public control resources | Conventional resource discovery rejects Project, Group, RoleBinding, Grant and DashboardPublication authoring; canonical access routes are instance-bound. | [resource_discovery_test.go](../../internal/project/compiler/resource_discovery_test.go) `TestDiscoverAuthoredResourcesRejectsRemovedAuthoringSurfaces`, [schema_test.go](../../internal/project/schema/schema_test.go) `TestValidateBytesRejectsRemovedPublicAuthoringKinds` and [generated API surface tests](../../internal/app/api_apigen_test.go). Legitimate internal Project IDs and deployment/managed-data target routes remain. |
| Live authority composition | PostgreSQL `ControlStore`, not historical graph/snapshot copies, owns mutable live grants and assignments. | `TestFAI616LiveControlAuthorityBoundary` in [postgres_access_conformance_test.go](../../internal/platform/architecture/postgres_access_conformance_test.go) enforces ownership. Snapshot DataPolicy compatibility remains until FAI-649. |

Prior documentation conflated the intended control-plane API direction with
current role-assignment write support. Public role-assignment mutation is
**unsupported**, not a partially supported API and not an invitation to restore
authored RoleBinding. A future requirement to expose it needs explicit API
acceptance; no endpoint is added in this qualification layer.

## Production composition evidence

| Owner | Implemented | Qualified evidence | Production evidence missing |
| --- | --- | --- | --- |
| FAI-617 | PostgreSQL identity authority, explicit restore, immutable history, current control projection, startup/recovery ownership | Real PostgreSQL tombstone/restore/rollback, instance isolation and cache-state rejection in `TestSemanticQualificationPostgreSQL18LifecycleAndAuthority`; activation/replay and same-generation reconciliation fixtures | A deployed process restart/cutover using protected principal, publication and cache bindings; operational activation recovery and fresh-authority rollback proof. Generic restart tests are not this proof. |
| FAI-641 | Compiled typed requirements, sealed barriers, rewrite rejection | Restricted aggregate/relationship execution and joined-side null-extension; source-shape topology tests | Exact enabled query/plan combinations and neutral protected activation verification. `TestProtectedSemanticActivationVerificationNeedsSeparateCompilerDesign` currently expects denial; no fabricated principal is acceptable. |
| FAI-642 | Shared consumer admission, result/Arrow rechecks and durable audit | Allowed/denied/missing-authority, stale revision, callback, tamper and shared-surface fixtures | Production principal-bound composition and positive route equivalence for each enabled consumer. `SetSemanticAccessAuthority` is not called by production composition. |
| FAI-645 | Lifecycle reader and guarded result dependencies; stale-state fencing; new binding after restore/rollback | Cache reuse, lifecycle races, stale registry/control, instance/project mismatch and PostgreSQL integration | `OpenProject` does not supply `SemanticCacheByModel`; any enabled cache needs exact activation-time configuration and operational reconciliation evidence. Missing configuration allows governed uncached execution, not unsafe reuse. |

Production currently wires candidate registry checks and audit in
[composition.go](../../internal/app/composition.go); candidate compilation is
not principal resolution. The guarded composition boundary is visible in
[project_runtime.go](../../internal/analytics/module/project_runtime.go).
PostgreSQL gate execution proves repository behavior, not production deployment.

## Activation contract consumed by FAI-649

This is an evidence checklist, not a new persisted schema or evaluator. Use
existing authorities and refuse admission if a required input is absent,
indeterminate, mismatched or stale.

| Required input | Existing owner | Required activation proof |
| --- | --- | --- |
| Approved publication | FAI-622 immutable publication and existing deployment decision ownership | Bind an actual approval decision to exact instance/authored identity/kind/version/profile/canonical digest and retained policy evidence. `ContractPolicyApprovalInput` is explanation, not approval. Do not infer approval from a major version or a successful publication. |
| Compatibility/security classification | FAI-622 unified classifier and typed registry input | Retain behavioral/security dimensions, affected graph and policy evidence; reject indeterminate decisions and enforce required approval for widening. No second classifier or digest. |
| Provider profile | FAI-637 Access authority | Existing authenticated principal; coherent direct/group assignments and registry state for the admitted profile. No client-supplied attribute authority. Other provider adapters stay excluded until independently qualified. |
| Consumer/plan/export profile | FAI-641/642 | Name the enabled routes and exact query shapes; prove barriers, consumer admission and audit before disclosure, and prove excluded surfaces cannot bypass them. Resolve neutral verification within compiler/planner ownership. |
| Lifecycle authority | FAI-617 | Current ACTIVE instance-qualified resource, immutable kind, exact active bundle/history sequence and publication; current live control projection. Re-read on restart/recovery/rollback; historical permission evidence cannot resurrect revoked grants. |
| Cache lifecycle | FAI-645 | Either explicitly governed uncached execution or activation-bound guarded cache configuration with current registry/control/value and publication/lifecycle evidence. Never initialize authority from a first cache hit or TTL. |

Apply the [policy-evidence reader-before-writer rules](semantic-access-policy-evidence.md)
before emitting newer evidence. Immutable publication/audit records are not
rewritten during cutover or rollback. Binary rollback is safe only when the
reader still enforces the retained policy profile; successful decoding alone
is insufficient. DataPolicy transition must identify untranslatable expressions
and masking and fail closed instead of silently broadening access. Actual
cutover, migration guidance, approval wiring and production verification remain
FAI-649 work; none is implemented here.

## Readiness decision

STR-08 and ENF-06 remain FAIL, owned by FAI-649, not circular prerequisites for
starting its implementation. This profile does not complete FAI-648 or waive
FAI-616. Its authority/removal ambiguity is resolved above; no new public role
mutation is a prerequisite under the current issue wording. Remaining upstream
consumer/plan acceptance must be reconciled with the live Linear
dependency graph before declaring FAI-649 ready. No downstream issue is started
or completed by this document.

## Qualification execution for this layer

No missing rejection test was found: existing executable cases already cover
unknown/mismatched providers, missing authority, forged plans, unsupported
protected bundles/bytes, and stale lifecycle/revision state. No duplicate tests
or production changes are added merely to make this a code-bearing layer.

Local commands use the repository Go 1.25.14 toolchain and `TMPDIR=/var/tmp`.
The initial default `/tmp` attempt failed before tests with `disk quota exceeded`;
the same small suites passed using the alternate temporary directory. This is
an environment limitation, not a test failure or waiver. Docker socket access
is unavailable locally; PostgreSQL must execute in the canonical hosted gate,
not be silently skipped. No caches, databases or worktrees were deleted.

| Command | Result |
| --- | --- |
| `go test ./internal/platform/architecture ./internal/access/trustedclaims -count=1` | PASS with `TMPDIR=/var/tmp` |
| `go test ./internal/analytics/query ./internal/analytics/materialize ./internal/access/module -run 'Semantic\|SecurityPlanner' -count=1` | PASS; includes semantic compiler/planner, consumer and lifecycle/cache tests in these packages |
| `task generated:check` | PASS; no generated snapshot changes |
| `task docs:check` | PASS; Mermaid, generated documentation and rendered link checks |
| `go test ./internal/project/compiler -run '^TestProtectedContractRegistryPlannerAndCacheQualification$' -count=1` | PASS; generated contract through governed DuckDB/Arrow and cache seam |
| `go test ./internal/project/compiler ./internal/project/schema ./internal/access/module ./internal/access/http -run 'TestDiscoverAuthoredResourcesRejectsRemovedAuthoringSurfaces\|TestValidateBytesRejectsRemovedPublicAuthoringKinds\|TestDispatchAPIGenGrantOperationsReachControlHandlers\|TestGrant' -count=1` | PASS; removal, canonical dispatch and grant authority fixtures |
| `git diff --check` | PASS |
| Canonical hosted `CI` workflow | Run on this layer after commit; retain the exact run/SHA and PostgreSQL lane results in FAI-648 Linear evidence. The parent result above is not substituted for this run. |
