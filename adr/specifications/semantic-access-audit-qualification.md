# LIF-06 semantic decision audit qualification

Layer: `ganesh/fai-648-audit-closure`, parent
`ganesh/fai-648-gap-closure` at `5662faf99946f654877b9ad9eaaa15f7f8ad6bfd`.

Status: LIF-06 PASS on the executed focused evidence below; aggregate full CI is
blocked by the shared runner's storage exhaustion. This qualifies LIF-06 only,
not production activation, DataPolicy cutover, or whole-ADR acceptance.

## Normative requirement and inventory

[LIF-06](semantic-access-policy-conformance.md) requires durable records to bind
the request principal, delegated actor, semantic generation, dataset and member
identities, normalized policy IDs, attribute-set version, decision and denial
reason. The parent already implemented these fields and mandatory persistence
on four shared consumer boundaries. Its PARTIAL disposition cited unqualified
consumer/provider coverage and activation. Activation is a separate requirement;
it is not necessary to invent a new audit event for an unimplemented surface.

| Requirement | Parent evidence | Missing proof or behavior | Owner / closure |
| --- | --- | --- | --- |
| Bound redacted decisions before disclosure | Query observer, strict Access DTO, materialize/discovery/catalog/Explorer handoff | API tests used unaudited synthetic consumers; no explicit all-constructor inventory | Existing consumer owners: audit-specific adapter and shared-executor tests; architecture inventory |
| Replay matches the original decision | PostgreSQL read validates strict metadata and existing intent digest | Another validly digested row was not compared against an expected decision | Access/PostgreSQL: exact expected-event verifier, no new identity or digest |
| Historical decision remains truthful through a lifecycle race | FAI-645 read-through fences and PostgreSQL lifecycle tests | Audit retention and original revisions were not asserted across races and renewed bindings | Materialize/Project integration: tests using existing authority/lifecycle fences |

## Invariant and retained binding

At every reviewed protected semantic-consumer constructor,
the existing evaluator's detached, redacted observation must be persisted before
that decision permits a plan, member projection, cache result, or output. A
recorder error fails closed. No adapter can select a different policy evaluator
or an unaudited protected consumer through route, surface, or cache metadata.

| Required binding | Existing retained authority |
| --- | --- |
| Instance | `SemanticDecisionEvidence.InstanceID`, from trusted serving/control composition |
| Request subject and delegated actor | Canonical `PrincipalID` plus separate `ActorPrincipalID`; ViewAs retains the existing administrator check |
| Resource and semantic generation | Canonical `ResourceRef` and `graph.ServingIdentity` (project, environment, generation) |
| Dataset/member and policy | Target dataset/dimension/metric, normalized grant/filter identities and outcomes, existing semantic-model digest |
| Attribute-set version | Registry/control profile, revision and digest; sorted definition ID/version/type/shape/source/value-digest projection |
| Decision | Allowed/denied outcome and closed denial-reason vocabulary |
| Integrity | Existing Access canonical metadata and PostgreSQL `intent_digest` |

SQL, predicates, raw attributes, claim contents and credentials are not copied
into semantic audit metadata. An invalid unresolved selector is not a member
identity: its request fails closed without fabricating a member decision.
The audit event action is `semantic_access.evaluated`; `success` means the
semantic decision allowed its target, not that query output was delivered.

Lifecycle sequence, active bundle and publication identity remain owned by the
existing `resultidentity.SemanticLifecycle` and ledger. A later mutation may
fence delivery after an allowed decision has been recorded. That historical
decision remains valid evidence of its original revision. It never becomes a
cache entry, authorization token, current approval, or substitute lifecycle fence.

## Replay and freshness

`ReadSemanticDecisionAuditEvent` reconstructs and verifies retained evidence
without consulting today's registry or control state. Historical records must
remain readable after tombstone, restore, rollback or a revision update.

`VerifySemanticDecisionAuditEvent` additionally requires an exact expected
`access.CanonicalAuditEvent`. After existing digest verification, it compares
all identity, principal, resource, capability, outcome, request/correlation and
canonical metadata fields. A valid record from another instance, generation,
revision, policy, member or decision cannot substitute for that expected event.
Formatting differences in metadata are normalized by the existing encoder.

The expected event is already-bound caller evidence, not a request to evaluate
policy or discover current state. No live admission path consumes audit replay.
Current execution freshness is independently enforced by the existing FAI-645
guards, including after audit persistence and on each protected Arrow batch.
An old record is therefore valid history but cannot authorize stale output.

## Consumer inventory

Four owners create production protected consumers: materialize, queryauthz,
project catalog visibility, and Data Explorer. Queryauthz has two constructor
sites (planner binding and direct target discovery). The architecture test
inventories all five calls and requires an explicit observer at each.

| Consumer | Decision path | Audit persisted? | Identity bound? | Control bound? | Registry bound? | Replay verified? | Status |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Materialization/query execution | `materialize.admitSemanticConsumer` → planner | Yes, shared observer | Yes, trusted runtime/subject | Yes | Yes | Shared Access reader/verifier | PASS |
| Cache reuse | Admission and planning before lookup | Yes, fresh event on hit | Yes | Yes | Yes | Shared Access reader/verifier | PASS |
| Discovery/explain | `queryauthz.SemanticPlanner` / `AuthorizeSemanticTarget` | Yes | Yes, snapshot/actor | Yes | Yes | Shared event contract | PASS |
| Catalog/search/agent catalog | `SemanticCatalogVisibility` | Yes | Yes, exact catalog lease | Yes | Yes | Shared event contract | PASS |
| Data Explorer | Bound definition → consumer | Yes | Yes, exact definition lease | Yes | Yes | Shared event contract | PASS |
| Semantic API | Queryauthz; materialize for data | Shared owners | Shared owners | Yes | Yes | Shared event contract | PASS, compositional |
| Dashboard/shared/scheduled execution | Governed runtime → materialize | Shared owner | Authenticated execution | Yes | Yes | Shared event contract | PASS, compositional |
| Agent/MCP tools | Catalog/API dispatch/governed visual runtime | Shared owners | Shared owners | Yes | Yes | Shared event contract | PASS, compositional |
| Public/embed query output | Publication governor → materialize | Shared owner | Semantic subject still required | Yes | Yes | Shared event contract | PASS, compositional |
| Semantic data export | No separate implemented adapter | N/A | N/A | N/A | N/A | N/A | NOT APPLICABLE |
| Model-list summary/source export/static embed shell | Resource-read or authoring, no semantic decision | N/A | Existing resource authorization | N/A | N/A | N/A | NOT APPLICABLE to LIF-06 |

Shared event-contract verification is compositional: the adapter must reach the
reviewed observer with the correct canonical binding, and Access persistence
and replay verify that same strict event contract. It does not claim separate
PostgreSQL fixtures for every route. Provider credential/activation qualification
is separate; no anonymous identity or delegated execution mode is fabricated.
Model summaries contain only ID/title/description, not dataset/member data.
These exclusions do not promote the broader ENF qualification rows.
The evaluator/observer supplies authoritative normalized member, policy and
revision evidence. Access validates its strict shape, ordering and reason codes;
replay verifies retained integrity and binding, not live policy eligibility.

The shared-owner proof uses the following named tests, not the existence of a
constructor alone:

| Owner / boundary | Decision and persistence test evidence |
| --- | --- |
| Materialize and cache | [semantic_consumer_test.go](../../internal/analytics/materialize/semantic_consumer_test.go): `TestProtectedSemanticAuditRecordsAllowedAndDeniedDecisions`, `TestProtectedSemanticAuditFailureAndMissingConfigRejectBeforeExecution`, `TestProtectedSemanticAuditRunsOnResultCacheHit` |
| Discovery and delegated actor | [semantic_audit_test.go](../../internal/dashboard/queryauthz/semantic_audit_test.go): `TestSemanticDiscoveryAuditFailsClosed`, `TestSemanticDiscoveryAuditBindsAuthorizedDelegatedActor` |
| Catalog | [semantic_catalog_test.go](../../internal/project/module/semantic_catalog_test.go): `TestSemanticCatalogVisibilityRequiresAuditForProtectedModel`, `TestSemanticCatalogVisibilityFailsClosedOnAuditWriteFailure` |
| Explorer | [data_explorer_authorization_test.go](../../internal/project/http/data_explorer_authorization_test.go): `TestDataExplorerAccessPredicateRequiresAuditForProtectedModel`, `TestDataExplorerSignalsReturns503WhenSemanticAuditWriteFails` |
| API adapter | [semantic_audit_qualification_test.go](../../internal/dashboard/semanticapi/semantic_audit_qualification_test.go): `TestSemanticAPIAuditRecordsBoundMemberDiscoveryAndDeniedTargets`, `TestSemanticAPIAuditRecordsWholeModelProjection`, `TestSemanticAPIAuditRecordsExplainAndQueryBeforeOutput`, `TestSemanticAPIAuditWriteFailurePreventsGovernedOutputs` |
| Shared execution surface metadata | [semantic_audit_surfaces_test.go](../../internal/analytics/materialize/semantic_audit_surfaces_test.go): `TestSemanticAuditSharedExecutionSurfacesCannotBypassPersistence`; these are executor tests, not separate route/provider fixtures |
| Durable handoff and retained replay | [semantic_qualification_postgres_test.go](../../internal/project/module/semantic_qualification_postgres_test.go): `TestSemanticQualificationPostgreSQL18LifecycleAndAuthority`; [semantic_decision_audit_test.go](../../internal/access/postgres/semantic_decision_audit_test.go): `TestReadSemanticDecisionAuditEventPostgreSQL18RuntimeVerifiesRetention`, `TestVerifySemanticDecisionAuditEventRequiresExactExpectedBinding`, `TestReadSemanticDecisionAuditEventRejectsStoredDigestTampering` |

## Validation

Executed on 2026-09-07. Docker qualification uses the existing pinned
PostgreSQL 18 fixture with `LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=1`.

| Evidence | Command | Result |
| --- | --- | --- |
| Exact expected binding, valid-row substitution, strict metadata, digest tampering and runtime immutability | `go test ./internal/access/postgres -run '^Test(Read\|Record\|Verify)SemanticDecisionAuditEvent' -count=1 -v` | PASS; `TestVerifySemanticDecisionAuditEventRequiresExactExpectedBinding` adds independent member/policy/decision comparisons |
| Existing query, cache and consumer owners | `go test -tags=duckdb_arrow -p 2 ./internal/analytics/query/... ./internal/analytics/materialize ./internal/dashboard/queryauthz ./internal/analytics/resultidentity ./internal/project/module ./internal/project/http ./internal/access -count=1` | PASS |
| Real lifecycle/authority changes and retained original events | `go test -tags=duckdb_arrow ./internal/project/module -run '^TestSemanticQualificationPostgreSQL18LifecycleAndAuthority$' -count=1 -v` | PASS; captures records before mutations, verifies originals afterward, and checks fresh miss/hit decisions at each renewed binding |
| API discovery, model projection, explain/query, semantic-recorder failure and metadata-only summaries | `go test -race -tags=duckdb_arrow ./internal/dashboard/semanticapi -count=1` | PASS; queryauthz-backed fixtures use canonical UUIDs and exact control snapshots |
| Independent control/registry/tombstone/restore/rollback races and shared surface labels | `go test -race -tags=duckdb_arrow ./internal/analytics/materialize -run '^Test(ProtectedSemanticAudit\|SemanticAuditSharedExecution)' -count=1` | PASS; `TestProtectedSemanticAuditBindsDecisionAcrossAuthorityAndLifecycleRaces` and `TestSemanticAuditSharedExecutionSurfacesCannotBypassPersistence` |
| Required PostgreSQL gate, including exact audit verification | `task test:go:postgres-conformance` | PASS, no test skips |
| Constructor inventory, shared observer and unchanged authority ownership | `go test ./internal/platform/architecture -count=1` | PASS; `TestSemanticDecisionAuditCoversEveryProductionConsumerConstructor` |
| Generated snapshots | `task generated:check` | PASS |
| Documentation | `task docs:check` | PASS, including final evidence and ledger checks |
| Full PR contract | `task ci` | NOT GREEN: first run exited 201 in the unchanged admin browser test; normal retry exited 201 at MinIO fixture upload with HTTP 507 `XMinioStorageFull` |

The first full CI run exited 201 in the frontend lane: `personal API tokens
use capability selectors` timed out after the unchanged 5,000 ms budget
(5,015.12 ms), followed by browser-closed failures. No frontend files changed.
An unchanged isolated `bun run test:admin-page` retry passed 21/21; the same test
took 593.02 ms. This is a non-reproducing browser execution failure, provisionally
category C; the exact resource or browser cause is not established. The isolated
retry is not a substitute for a complete `task ci` result. Logs are
`/var/tmp/lif06-ci.log` and `/var/tmp/lif06-admin-isolated.log`.

The normal full-CI retry passed the admin 21/21 (the formerly failing test took
610.08 ms) and site 51/51 suites. It then exited 201 in `ci:lane:go` at
`TestMinIOParquetSourceRefreshContract`: the initial fixture `PutObject` at
`internal/app/integration_minio_source_test.go:49` failed with HTTP 507
`XMinioStorageFull` after 2.22 seconds. The service explicitly reported its
minimum free-drive threshold was reached. `df -h / /var/lib/docker` showed the
same 301 GB filesystem with about 1.2 GB available and 100% rounded usage;
available space during this run also fell below 1 GB. This is category C,
an observed shared-runner storage limitation, not a demonstrated LIF-06
regression. No application/MinIO code changed. The retry log is
`/var/tmp/lif06-ci-retry.log`.

The independent required PostgreSQL 18 gate and scoped audit suites passed;
their results do not claim a green aggregate CI. No shared caches or other
tasks' files were deleted. Restore runner storage headroom before another full
CI attempt; do not bypass the MinIO storage safeguard or skip the test. LIF-06's
normative evidence is complete, while full-CI qualification remains blocked and
FAI-648 overall remains IMPLEMENTED / PARTIAL. These local logs are execution
artifacts, not immutable publication evidence.

Review corrected test-only defects: a legacy API snapshot lacked its required
control identity; an early discovery test expected HTTP 404 instead of the
existing empty HTTP 200 collection; combined race mutations could mask an
individual guard; and a post-mutation read could not prove retention against
the original captured event. These are category A test-integration issues,
not baseline product failures. The corrected tests retain the actual fail-closed
behavior and assert exact empty output or original evidence as appropriate.
A worker shell initially lacked Go on PATH (category C tool environment);
the required tests subsequently executed with the repository toolchain.
No assertions, test gates, migration behavior or timeout values were weakened.

STR-08 and ENF-06 retain their FAI-649-owned DataPolicy cutover failures.
FAI-649 and FAI-632 are not started or marked ready by this evidence.
