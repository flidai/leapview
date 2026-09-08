# FAI-649 pre-activation readiness audit

Evidence-only review on 2026-09-07, resumed 2026-09-08, of `ganesh/fai-648-audit-closure` at
`7ae7153ece3ab9667eb075a134f344ae5bff8647`, stacked above
`ganesh/fai-648-gap-closure`. No activation, authorization, DataPolicy,
migration, provider or cache behavior is changed by this review.

## Storage incident and recovery

The previous normal `task ci` retry failed in
`TestMinIOParquetSourceRefreshContract` at
[`integration_minio_source_test.go`](../../internal/app/integration_minio_source_test.go):49.
Its initial fixture upload returned HTTP 507 `XMinioStorageFull`; MinIO reported
that its minimum free-drive threshold had been reached. This failure occurred
before the source refresh assertions, not in semantic access evaluation.

| Check | Observation |
| --- | --- |
| `df -h / /tmp /var/lib/docker`; `df -i /` | Root and Docker share the 301 GB disk: about 1 GB available, rounded 100% usage. Inodes were only 20% used. `/tmp` is a separate tmpfs and cannot restore Docker disk capacity. |
| `du -x -h --max-depth=1` on `.cache`, `/var/tmp`, and worktrees | About 81 GB of caches, 27 GB of temporary assets and 35 GB of worktrees. These totals are inventory, not proof that all contents are disposable. |
| `docker system df -v` | Shared BuildKit state about 21 GB and retained runtime images; no retained MinIO test container. Persistent PostgreSQL containers/volumes belong to other work and were preserved. |
| Existing `startMinIO` fixture and previous failure log | The fixture calls `testcontainers.CleanupContainer`; the failure log records stop and termination. No MinIO container cleanup defect was demonstrated. |
| `go env GOCACHE GOMODCACHE GOTMPDIR`; cache `README`; process inventory | The active Go build cache was `/home/codex/.cache/go-build`, about 21 GB. Its README explicitly recommends `go clean -cache`. No Go compiler/linker/test processes were running when cleanup began. |
| `GOCACHE=/home/codex/.cache/go-build go clean -cache` | Exit 0; cache reduced to 12 KB and available disk rose to about 22 GB. These are rebuildable artifacts, not source or retained qualification/publication evidence. |

Classification: **B — infrastructure limitation**. The shared long-lived runner
exhausted storage headroom; repository MinIO cleanup worked. No repository
behavior change is justified by the observed failure. No timeout, threshold,
test assertion, Docker image, database, tool download or other worktree was
changed or removed. Existing GitHub CI uses separate ephemeral runners for its
Go/frontend lanes; this local run uses the normal bounded Taskfile composition.

## Full CI execution

The normal `task ci` ran after recovery with elapsed time recorded by
`/usr/bin/time -p`. It **failed with exit 201 after 428.39 seconds (7m08s)**.
The existing repository toolchain/Docker group and
`LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=1` were used. No lane or test was skipped
to work around the incident; later gates were not reached after the failure.

| Lane / gate | Observed result |
| --- | --- |
| Shared preparation | PASS: extension preparation/checks, generation and app/site asset builds completed. |
| APIGen | PASS: Go tests and TypeSpec tests (61/61). |
| Frontend core/reports/chat/data | PASS, including admin 21/21 and dashboard 58/58. |
| Frontend site | FAIL: `visual showcase remains visibly rendered in light and dark themes` exceeded its unchanged 30,000 ms budget; elapsed report 72,913.62 ms. A subsequent browser cleanup hook failed. Suite summary: 50 pass, 2 fail, 1 error. |
| Go packages/application shards | Package pass results and all four application shard pass results were recorded. Child output continued after the parent Task failure; this is not a completed aggregate Go-lane result. |
| External MinIO and required PostgreSQL conformance | NOT REACHED in this attempt. The prior independent PostgreSQL qualification remains historical evidence, not a new execution claim. |
| Final `generated:check` | NOT REACHED in this attempt. Generation during preparation is not a replacement for this final gate. |

Failure classification: **C — non-reproducing/flaky browser test**, with exact
cause unestablished. Browser diagnostics included missing map asset responses;
those messages alone do not establish why the process stalled. On 2026-09-08,
unchanged `bun run test:site:prepared` passed 51/51, exit 0, in 76.25 seconds.
No browser/frontend/test configuration file changed. An isolated pass does not
replace a successful complete CI run.

At resume, shared disk availability had fallen back to about 1.8 GB; during the
isolated retry it fell below 300 MB while unrelated Go/sqlc/test jobs were
active. The rebuilt default Go cache was only about 2.6 GB, so its regrowth
alone does not explain the consumed headroom. The environment no longer has
stable capacity for another full run. A follow-up inventory found the separate
shared `/home/codex/.cache/lv-gocache` had grown from about 15 GB to 27 GB;
this explains part, not necessarily all, of the renewed pressure. Do not clear active jobs' caches or
remove their worktrees/databases. Restore stable runner capacity or use an
isolated runner before retrying the complete normal contract.

Local artifacts: `/var/tmp/fai649-readiness-ci.log`,
`/var/tmp/fai649-readiness-ci.time` and
`/var/tmp/fai649-readiness-site-retry.log`. They are execution logs, not
immutable publication evidence. **Full CI qualification remains open.**

Final evidence validation on 2026-09-08:

- `task docs:check` exited 1 while compiling Arrow: `no space left on device`.
- `go test ./internal/platform/architecture -run '^TestSemantic(QualificationMatrixCoversEveryRequirement|DecisionAudit)' -count=1`
  exited 1 during linking with the same error; the tests did not execute.
- Both are category B infrastructure limitations, not passing validation or
  demonstrated assertion regressions. The previous layer's passing suites are
  not substituted for these failed attempts.
- `git diff --check` passed. Only evidence Markdown is changed in this review.
- A read-only Node filesystem check verified all 372 local Markdown links in
  the three changed evidence documents. This is not a substitute for the
  blocked docs or architecture suites.

## Prerequisite checklist

Linear statuses below were read on 2026-09-07; FAI-616/648/649 and their direct
relations were rechecked on 2026-09-08. They are observations from this audit,
not branch qualification. Older status lists in the ledgers are historical
checkpoints, not the current Linear state.
In particular, a globally Done issue does not prove this stack's entire
production profile. Existing named-test results are retained in the linked
[qualification matrix](semantic-access-qualification.md); no status is promoted
from code existence alone.

| Input | Linear | Current-stack evidence | Remaining acceptance boundary |
| --- | --- | --- | --- |
| FAI-616 control boundary | In Progress | Production Access requires the PostgreSQL live `ControlStore`; public/control-boundary ownership is enforced by `TestFAI616LiveControlAuthorityBoundary` in [architecture tests](../../internal/platform/architecture/postgres_access_conformance_test.go). | IMPLEMENTED / PARTIAL in ADR-0016; direct FAI-649 blocker with no whole-issue acceptance claim. FAI-617, not FAI-616, owns active runtime projection/restart/reconciliation qualification. |
| FAI-617 identity lifecycle | In Progress | PostgreSQL tombstone, explicit restore, rollback, instance isolation and immutable replay are qualified. [Lifecycle integration](../../internal/project/module/semantic_qualification_postgres_test.go) retains original audit records while rejecting stale runtimes. | Whole milestone remains PARTIAL: repository/coordinator fixtures do not prove production-process restart/activation and reference reconciliation. |
| FAI-637 registry and trusted attributes | Done | Access owns typed definitions, assignments and coherent resolution. [Provider qualification](../../internal/access/trustedclaims/provider_qualification_test.go) proves the common fail-closed verifier/envelope boundary. | ATT-02/03/10/11 and real provider-to-consumer trust remain PARTIAL. SAML/OIDC/embed/service-token cryptographic adapters and their production semantic context are not supplied by the common test verifier. |
| FAI-639 compiler | Done | One typed grant/filter compiler/evaluator; generated contracts and candidate registry qualification feed the existing planner. | Trusted provider expiry-to-evaluator and all transitive consumer cases remain bounded by partial FLT/ENF evidence. Candidate compilation is not production activation verification. |
| FAI-641 planner | Done | Typed barriers and sealed rewrite checks; PLN-01/03/05/06/08 have executed evidence. | PLN-02/04/07/09 remain PARTIAL: complete operation, rollup/coalescing, consumer and cache-lineage qualification is not established. |
| FAI-642 consumers | In Review | Four existing owners and five constructor sites; LIF-06 now PASS. API, discovery, catalog, Explorer and shared executor evidence remain explicitly compositional. | Full admitted route/provider equivalence remains partial; rejected bundles/opaque bytes are not positive support. Neutral protected activation verification intentionally fails closed. |
| FAI-645 cache lifecycle | In Progress | Lifecycle/authority cache fences, PostgreSQL mutation tests and immutable policy evidence are implemented and scoped-qualified. | Production `OpenProject` does not supply `SemanticCacheByModel`; protected reuse is not activated. Broader cache classes, diagnostics, policy-event correlation and approval acceptance remain partial. |
| FAI-648 qualification | In Progress | 55 PASS / 45 PARTIAL / 2 FAIL; LIF-06 is PASS after exact audit replay and consumer/race qualification. | IMPLEMENTED / PARTIAL overall. CI success alone cannot complete the 45 partial normative rows or qualify activation. Direct FAI-649 blocker. |

The eight issue descriptions and relations were read from Linear. FAI-649 is
Backlog, directly blocked by FAI-616 and FAI-648, and blocks FAI-632. No statuses,
dependencies or ownership were changed.

## Activation boundary and ownership

Separate upstream acceptance work from FAI-649's intended deliverables; do not
require FAI-649-owned cutover behavior as a circular prerequisite to its own
implementation.

1. **Upstream acceptance:** verify FAI-616's public/control authority boundary,
   retain FAI-617's separate production lifecycle qualification, and close or
   explicitly scope the FAI-648 provider/plan/consumer
   partials for the admitted profile. Common trust-envelope tests are not
   cryptographic provider qualification. Unsupported surfaces remain gated.
2. **Production verification and wiring (FAI-649):**
   [composition](../../internal/app/composition.go) configures candidate registry
   and semantic audit but does not call `SetSemanticAccessAuthority`.
   [Runtime construction](../../internal/analytics/module/project_runtime.go)
   also does not supply activation-bound semantic cache configurations.
   Protected runtime admission fails closed. The existing
   `TestProtectedSemanticActivationVerificationNeedsSeparateCompilerDesign`
   records the neutral representative-planning boundary: resolve it with the
   owning compiler/planner, never a fabricated principal or bypass.
3. **Approval acceptance (FAI-649):**
   [ContractPolicyApprovalInput](../../internal/project/module/contract_policy_plan.go)
   retains classification, graph and policy-evidence digests but is not an
   approval. Bind those exact identities/revisions to the existing deployment
   approval decision; a version bump or restatement must not clear widening
   requirements. The optional publication registry reader is exercised by
   integration fixtures, not configured by current production composition.
4. **DataPolicy transition (FAI-649):** STR-08 remains FAIL because standalone
   DataPolicy is still schema/CUE-valid. ENF-06's failing clause is the retained
   second authored policy language; Source/Model preview authorization itself
   is not the missing clause. Retained paths include
   [schema](../../internal/project/schema/schema.go),
   [resource discovery](../../internal/project/compiler/resource_discovery.go),
   [manifest](../../internal/project/manifest/project.go),
   [Access API](../../api/typespec/access.tsp), and
   [query policy composition](../../internal/dashboard/queryauthz/policy_composition.go).
   Cutover must cover schemas, compiler/runtime, fixtures, UI/API and migration
   documentation after upstream consumers/controls qualify. It must preserve
   the unsupported break-glass/masking boundary.
5. **Rollback evidence:** existing PostgreSQL historical restore/rollback and
   audit replay are positive evidence, not missing implementation. Production
   semantic activation/restart/rollback still needs proof of fresh authority,
   cache and approval binding. Follow the existing
   [policy-evidence reader compatibility rules](semantic-access-policy-evidence.md):
   deploy v3 readers before writers and do not rewrite immutable history to
   make an older binary appear compatible.

## Readiness decision

**FAI-649 is not yet ready for an unconditional implementation/activation start.**
Its scope is understood, but independent upstream qualification remains open.
STR-08 and ENF-06 are expected FAI-649 deliverables, not additional circular
upstream blockers. Resolve FAI-616/648 acceptance and the admitted provider and
consumer profile before authorizing that next layer. This audit does not start
FAI-649, mark FAI-648 complete, make FAI-632 ready, merge another branch, or
change the 55/45/2 matrix.
