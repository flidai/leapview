# Saved Data Exploration: main-readiness report

Tracking: [FAI-769](https://linear.app/flid/issue/FAI-769/reconcile-saved-data-exploration-with-current-main-and-prepare-green).

## September 11 merge-queue remediation

PR #543 remains open and unmerged. Two merge-queue candidates
(`d7bcc8c` and `f57c593d`) passed the ordinary PR/security lanes but failed the
same merge-only Data Explorer recovery journey. After Preview Retry/Reset, a
full navigation restored the browser command sequence to zero while the Go
lifecycle retained the tab client's later sequence, so the Analyze configure
response was correctly rejected as stale. Requeueing the unchanged head was
stopped after the deterministic second failure.

The reviewed correction keeps a tab-scoped semantic command clock in
`sessionStorage`, with an in-memory fallback, and aligns the body client ID with
the existing request-header identity. Configure/Run sequences remain monotonic
across remounts; Stop retains the addressed Run sequence; filter suggestions
remain on their independent lane; malformed storage is ignored and sequence
exhaustion fails closed. Current main `1027b08bf` was merged normally into the
feature branch at `0b4ce97f3`; no force push was used.

The exact browser journey then exposed two pre-existing recovery-test/UI
contracts that the stale-sequence failure had masked. Recovery had attempted
Retry with an invalid empty query, and recovery notices shared a grid row with
the empty/result body. The follow-up now selects and awaits a governed field
before injecting a retryable error, gives notices and preserved results distinct
rows, offers Reset without Retry for invalid specs, and parses the actual posted
`dataExplorerCommand` rather than scanning unrelated Datastar signal state.
Real Playwright coverage verifies both a physically clickable valid Retry and
the invalid Reset-only state. The result styles/recovery renderer were extracted
from the oversized route component, reducing TypeScript production excess from
19,077 to 19,016 against the unchanged 19,080 budget.

Independent code review found no findings in either the sequence correction or
the recovery/layout follow-up. The full Data Explorer suite, client/controller
tests, app typecheck, quality budget, and exact 13-route DatastarLit QA pass. The
approved visual-update task refreshed only four compact dashboard snapshots;
independent image review confirmed the differences are limited to the intended
Explore handoff and current header/action controls, with no chart, card, filter,
spacing, or overflow regression. Visual thresholds were not changed.

Two local `task ci:full` attempts passed generation, database verification,
APIGen, Go/application/PostgreSQL and the affected feature suites before
unrelated five-second browser setup timeouts closed their shared Chromium
processes (first Admin, then Dashboard Builder). Those suites subsequently
passed four and three isolated full-file runs respectively. A frontend-only
rerun later timed out in a third unrelated dashboard file while the 16-CPU host
was at load 17.7 and another workspace was running an eight-core SQL generator
and `task ci`. These interrupted aggregates are not recorded as green and no
timeout was relaxed. A clean full gate and exact-head hosted checks remain
required after shared-host contention clears.

Current correction commits are `f6ad03cbc` (durable sequence), `535081193`
(actionable recovery layout/contracts), and `f96631349` (reviewed compact
baselines). FAI-769 remains In Review until the exact pushed head clears all
required checks and the approved merge queue completes.

## September 10 follow-up integration with main `85bdf484e`

Main advanced after the `60dc6f238` checkpoint passed 28 hosted checks (four
conditional skips). Those results apply only to that earlier head.

This forward merge preserves main's released migration
`008_managed_provider_version_observation.sql` and advances the unmerged saved
exploration migration to `009_saved_explorations.sql`. The upgrade regression
starts at released revision eight. No released migration is rewritten and no
live database is migrated. Dashboard drawer coverage retains main's explicit
signal-state waits and reusable Datastar module handle together with the
feature branch's deferred-visual mounting readiness.

Focused validation passed: the full dashboard DOM file (59 tests), 15 independent
drawer-test repetitions, and the PostgreSQL migrations package with conformance
required (including fresh install and the 8-to-9 upgrade). The read-only
review-agent review found no actionable defects in the resolved changes against
both parents, including the automatic semantic-access/cache and application
composition merges. Full `task ci` passed on code checkpoint `b2c3253f5`, including
generation, Go/application tests, required PostgreSQL conformance, all frontend
lanes, and generated-file verification. Hosted checks must pass on the pushed
head; earlier green results below are historical evidence. FAI-769 stays
In Review. No force push, main merge, auto-merge, or queue enrollment is authorized
by this conflict-resolution handoff.

The first hosted run on `1e851b88c` exposed a one-line TypeScript-test quality
budget overrun (8961 excess lines versus an unchanged 8960 allowance). This
was a real gate failure, despite the passing local `task ci`: the local aggregate
Go lane does not invoke the hosted package lane's quality-budget check. The
follow-up deduplicates three signal-readiness polls without removing conditions,
waits, or assertions. Independent review found no defects; the unchanged budget
passes at 8960, as do exception/trend checks and critical-package coverage.
The affected drawer test passed 15 independent runs after the cleanup.
Local/hosted quality-lane parity remains a tooling follow-up.

## September 10 independent-review corrections

Independent review of `d8153f1` identified six correctness defects despite that
checkpoint's green CI. This follow-up covers bucket-aware drill predicates,
clearing optional query settings, pivot-window edits, browser-shaped pivot
handoff, time-filter/grouping parity, and independent suggestion transport.
Regression tests must exercise composed controls and the bundled Datastar
transport, not only isolated helpers. Suggestion responses must not replace
semantic query state or cancel an active Run.

The adapter now accepts matching redundant browser selections, rejects
conflicting selections, and keeps out-of-axis time ranges as filters. Clearing
time/pivot uses explicit client removal markers and server signal tombstones;
the canonical persisted spec contract is unchanged. Date and timestamp bucket
drills create typed half-open ranges. Zoned timestamp bucket drills fail closed
until the interaction contract carries the named timezone; no timezone is
guessed. Suggestions use independent cancellation scopes and narrow, freshness-
checked response patches rather than replacing the semantic result envelope.

Focused authoring and HTTP tests passed, including five race-enabled runs of
suggestion endpoint/lifecycle/response-lease tests. Canonical patch serialization
tests cover both clearing and populated-spec preservation. The unchanged quality
budget passes after extracting the transport logic and browser regressions into
bounded files. All 37 focused control/drill tests passed with
`TZ=America/Los_Angeles`. Actual bundled-Datastar browser regressions confirm
independent suggestion cancellation, a surviving delayed Run, cleared errors,
retained results, reachable Stop, and time/pivot/limit edits surviving response
patches. These tests are included in the normal frontend CI script.

Full local `task ci` passed exit 0 on the reviewed correction checkpoint,
including PostgreSQL conformance, all frontend shards, and generated-file
consistency (`/tmp/fai769-review-fixes-ci.log`). Exact-head hosted results are
recorded on PR #543 and FAI-769 after the normal push. The earlier green checks
below are historical evidence. FAI-769 stays In Review. No database migration
changes, force push, main merge, auto-merge, or merge-queue enrollment are part
of this correction pass.

### Hosted validation follow-up

Hosted checks on `63ed273` exposed two integration problems. The dependency
security job failed before scanning because parallel TypeSpec generators
repopulated the same bundled-package cache. Separately, GitHub tested against
newer main `ff5398ad7`, and the combined Go production code exceeded the existing
quality budget by 22 lines. These are failures, not accepted checks.

Main was merged normally into the PR branch at `852ebc4`; this does not merge
the PR into main. Targeted semantic-model, query-planner, and exploration-adapter
tests passed on that combination. The follow-up fixes use the built vendored
TypeSpec emitter and a behavior-preserving extraction, keeping security gates
and quality thresholds unchanged. The renewed full local `task ci` run passed
generation, APIGen, quality, Go/PostgreSQL application conformance, and the
core/reports/chat frontend shards, then failed in the new transport browser
fixture: releasing the delayed response raced route teardown (`Route is already
handled!`). This run is not recorded as green
(`/tmp/fai769-integrated-review-ci.log`). The test-only correction waits for
started Run/Stop handlers before removing routes, without changing production
transport behavior or weakening the cancellation assertions. Repeated focused
tests, frontend revalidation, final generated checks, and exact-head hosted
gates are required before the review handoff.

The corrected transport fixture passed 10 consecutive focused runs. Frontend
revalidation passed its transport and canonical-clear regressions, then hit a
separate admin-page navigation timeout; the unchanged data shard passed on a
fresh run. No timeout or assertion was relaxed. Before the final push, main
advanced again to `489798fa7` (viewport-gated shared visual mounting). The normal
integration merge `5eea2c629` preserves both dashboard Explore actions and
`defer-mount`, with assertions for both. Test-script and route-QA conflicts retain
both parents' coverage. JavaScript vulnerability evidence was regenerated from
live registries for the final merged package manifest. Frontend, generated-file,
quality, and exact-head hosted validation must cover this newest combination.

Final local frontend revalidation (all five shards) and `generated:check` passed
exit 0 on the viewport-integrated checkpoint
(`/tmp/fai769-viewport-final-validation.log`). The unchanged quality budget also
passes. Independent review found no additional concrete viewport integration
regression: exploration and chat preview hosts remain eager, while dashboards
retain deferred mounting and authenticated Explore links. The earlier full
`task ci` failure remains documented above; its Go/PostgreSQL stages passed,
and subsequent changes are frontend/test/integration evidence only. The final
hosted CI and Security gates must still be checked on the pushed head.
Final architecture, security workflow-contract, and dependency-evidence unit
tests also passed (`/tmp/fai769-final-contracts.log`). No gate was bypassed.

Hosted validation of `92f201adf` passed the Security gate, including dependency
policy, but exposed a lean-APIGen gap: a global emitter override reached the
standalone example before the checkout emitter was built. The override is now
scoped to the nine generation tasks only; standalone APIGen tests retain their
bundled-toolchain fallback. Contract tests reject both global leakage and an
override on either lean-test entry point. Fresh-cache Go example/compiler tests
passed. The full lean lane then passed exit 0 with fresh cache
`/tmp/fai769-apigen-cold.BevPI9` (`/tmp/fai769-lean-apigen-cold.log`), followed by
`generated:check` (`/tmp/fai769-scoped-generator-validation.log`) and both
generation contract tests (41 assertions). The failed hosted APIGen run remains
historical evidence, not an accepted gate.

On `b9a4ce962`, hosted APIGen and Security passed. The application lane exposed
a literal Taskfile-layout assertion, not a change in dependency ordering;
moving the UI generator's scoped environment below its dependency list preserves
the expected layout. All application `TestAPIGen*` tests and the 41 generation
contract assertions passed after this correction.

The reports lane also reported a destroyed browser context in the agent-drawer
test. Repeated local tracing observed intermittent timeouts but did not prove
the hosted error's exact cause. Independent review did identify a missing
precondition after main's viewport change: the fixture inspects nested renderers
after only awaiting a host update. It now uses the canonical
`ensureVisualizationsMounted()` wait before inspection, retaining all assertions
and timeouts. The final hosted reports result is still required; browser-runner
instability must not be described as definitively resolved without evidence.
The full reports shard passed after the readiness adaptation (dashboard DOM
65/65, visuals 5/5, host 13/13, builder 64/64). Bun's single-process
`--rerun-each=15` still produced a run-11 timeout; a separate verification using
15 independent Bun processes passed 15/15 with no timeout
(`/tmp/fai769-drawer-independent-1.log` through `-15.log`). This distinguishes
successful independent stress evidence from unresolved repeat-runner behavior.

## September 10 current-main reconciliation

Main advanced to `372039da7` after the September 9 exact-head verification,
introducing chart formatting, contract-publication evidence, ODCS/OpenLineage,
and closed resource-graph references. The previous 28 green hosted checks on
`e940363d1` do not validate this new combination.

This normal merge preserves released migrations 001–007 and renumbers only the
unmerged saved-exploration migration to 008, without changing its SQL contents.
Validation must cover fresh installation and released revision 7 to 8. Earlier
development databases using unmerged saved migrations 003, 005, 006, or 007 need
explicit migration-history inspection/reconciliation; no live database work or
automatic history rewrite is included.

Catalog-browser conflicts are reviewed as a union of saved-exploration behavior
and main's updated fixtures. JavaScript vulnerability evidence is refreshed from
the live registries using canonical tooling, not chosen from either stale parent.
Generation exposed removed dashboard KPI-threshold and point-series fields;
the exploration adapter was reconciled with the new canonical contract. Legacy
KPI thresholds and newly unsupported dashboard formatting fail explicitly at
handoff rather than being silently discarded. Supported KPI ranges, comparison,
goal, and trend bindings retain round-trip coverage. Dashboard signal schema
version now matches visualization version 14 in TypeSpec; bindings and OpenAPI
were regenerated canonically.

Generation, migration tests (25.481s), catalog browser tests (29 passed), adapter
regressions, app/contract typechecking, security-runner contracts, architecture
checks (26.758s), and the unchanged quality budget passed. Broader required-PG
exploration/authoring integration tests also passed.

Independent review then identified preview paths that silently discarded authored
field formats and KPI ranges/comparison/goal/trend settings. The initial CI run on
`e423e3699` was deliberately stopped during database verification, not counted as
a pass. Preview corrections preserve supported field formats and scalar KPI
ranges/comparison/goal bindings. Unsupported trend/threshold requests produce
warnings and a usable table fallback; table and chart formats remain independent.
Malformed visualization pointers and conflicting formats receive regression
coverage. Final review and focused visualization tests passed (0.091s).
Main's subsequent exact-version managed-object retrieval change (`ab9f8e124`)
was integrated by a clean normal merge, without changing migration numbering.
Restarted full CI subsequently passed on `d8153f1`; all 28 hosted checks
succeeded, with four conditional skips. That checkpoint was then superseded by
the independent-review corrections above.

The PR must remain unmerged, with auto-merge disabled and no merge
queue enrollment, until explicit user approval. No force push is permitted.

## September 9 RecoverySet v3 base refresh

Main advanced again to `285514606` with RecoverySet v3 while validation ran.
The preceding merge `7545fe9fa` completed full `task ci` successfully (exit 0;
`/tmp/fai769-project-boundary-ci.log`). Pushed head `e2bfb5963` also completed
all 28 hosted checks, including CI gate and Security gate. These green results
are historical and do not validate the new recovery integration.

The current normal merge preserves main's released migrations 001–006 and moves
only the unmerged saved-exploration migration to 007. Required validation covers
fresh installation and released revision 6 to 7, retaining the ResourceUID and
RecoverySet v3 objects and role boundaries. Canonical generation passed; required
PostgreSQL migration tests passed (34.100s), and both affected refresh tests passed
three runs (25.381s). The refresh resolution matches main's stronger independent
lock-probe regression and removes superseded polling helpers. Root reviewed both
delegated resolutions and the automatic schema/baseline merge.

Full `task ci` on merge `c7c091bb9` passed all Go/native packages, core frontend,
reports, and chat, then stopped in the data shard on an admin-browser custom-element
startup timeout (overall exit 201). Subsequent tests failed because the browser
had closed. A fresh admin run timed out at a different component startup; the
isolated test passed three runs. Two instrumented complete admin runs passed
(21 tests each), with no page/console errors, failed requests, missing assets,
or observed crashes; diagnostics were removed. The unmodified complete data
shard then passed. The site shard separately hit a five-second startup timeout
and browser-closure cascade; a fresh unchanged full site run passed all 53 tests.
Shared-host load was approximately 38 runnable tasks across 16 CPUs, a possible
contributor but not a proven root cause. No timeout, assertion, or product change
was made to hide these failures. Browser startup reliability remains a caveat.

All constituent local CI stages passed, including generated-file checks, but
the original complete invocation exited 201, not 0. Native application shards
passed in 72.326s, 86.782s, 94.919s, and 97.117s. Exact-head hosted checks remain
pending; PR #543 remains unmerged with auto-merge disabled.
Evidence: `/tmp/fai769-recovery-merge-ci.log`,
`/tmp/fai769-recovery-merge-generated-check.log`,
`/tmp/fai769-admin-diagnostic-1.log`, `/tmp/fai769-admin-diagnostic-2.log`,
`/tmp/fai769-recovery-browser-remaining.log` (data passed; site failed), and
`/tmp/fai769-recovery-site-rerun.log` (site passed).

Development databases that applied earlier unmerged saved-exploration migrations
003, 005, or 006 require migration-history inspection and explicit reconciliation;
none is interchangeable with the corresponding released main migration. No
automatic history rewrite, live migration, reset, or data transfer is included.

## September 9 project-boundary base refresh

While hosted checks ran on `e2bfb5963`, main advanced to `4435ea6c1` with
server-bound Project selector/locator enforcement. The PR remained conflict-free
but became behind. A subsequent normal merge includes that security-boundary
change without modifying its guards. Focused Project and SavedExploration tests
passed in access/module, app, analytics/cache, and app/api/protocol. The prior
full-CI success below validates the preceding merge; combined full CI for this
base subsequently passed, as recorded above. No merge into main, force push,
or live database operation is authorized or performed.

## September 9 ResourceUID integration checkpoint

Reconciliation now includes main `4b9ee86fb`: the released ResourceUID registry
and hosted full-validation overlap. This supersedes the migration numbering in
the historical checkpoints below. Released migrations 001–005 remain unchanged;
only the unmerged saved-exploration migration moves from 005 to 006. Validation
must cover a fresh installation and upgrade from released revision 5 to 6,
preserving the ResourceUID registry.

Development databases that already applied an earlier unmerged saved-exploration
migration numbered 003 or 005 need migration-history inspection and explicit
reconciliation before using this chain. No automatic history rewrite, live
database migration, reset, or data transfer is included.

The refresh-test merge preserves main's scoped, deterministic lease-expiration
fixture and the additional publishing-fence concurrency regression. Main's CI
overlap and the native application-test sharding correction are both retained.
Merge commit `e42208b33` passed canonical `task generate` and full `task ci`
(exit 0), including ordinary Go, required PostgreSQL conformance, all browser
shards, and generated-file checks. Native application shards passed in 64.031s,
70.033s, 71.770s, and 84.224s. The previously intermittent dashboard URL-state
and site-search checks passed in this complete run; this is not proof that their
historical reliability caveats are permanently fixed.

The full migration package also passed separately with PostgreSQL conformance
required (19.861s), covering fresh installation and released-5-to-6 upgrade.
Both affected refresh tests passed three runs; independent review found no lost
CI coverage or schema inputs. Evidence: `/tmp/fai769-registry-merge-ci.log`,
`/tmp/fai769-registry-merge-migrations.log`, and
`/tmp/fai769-registry-merge-generate.log`.
Hosted exact-head validation remains pending. PR #543 remains unmerged, with no
force push or automatic merge enabled.

## September 9 hosted-CI follow-up

On head `aa38ee0aa`, hosted Go application validation reached its package-wide
ten-minute timeout in the PostgreSQL conformance lane. The named test,
`TestAuditedQueryMetricsRecordsExecutionError`, had run for only five seconds
and was still committing a legacy SQLite fixture migration; no assertion failed.
Both query-audit tests passed three consecutive targeted local runs (9.816s).

The ordinary application lane already uses stable shards, but the native lane
reran the entire application package in one process. A coverage-preserving
native application sharding correction now uses matching build tags, required
PostgreSQL conformance, unchanged timeouts, and two separate phases bounded to
four test workers. The real 335-test inventory maps exactly once across shards
of 84, 84, 84, and 83 tests, including the tagged warehouse qualification. The
existing MinIO test still runs in its separate external lane.

Runner/sharder contracts passed three consecutive runs, including an inherited
required/skip environment; tests verify per-worker required flags, discovery
failure handling, and completion/failure propagation. Canonical generation and
the quality budget passed. Full `task ci` passed all Go suites, including native
application shards in 65.394s, 71.223s, 74.582s, and 86.114s. It then stopped on
an intermittent browser-context destruction during the dashboard URL-tombstone
test, before assertions. Three independent focused reruns and the complete
reports shard passed; instrumentation did not evidence a product navigation
defect. No speculative browser patch or assertion retry was added. This remains
a browser-test reliability caveat rather than a proven fix. Generated checks
and the chat/data shards passed separately. The site shard initially hit a
search-status timing failure ("Searching…" between wait and assertion); a fresh
complete site run passed all 53 tests with no source changes. Both intermittent
browser failures remain reliability caveats. All constituent local CI stages
have passed, but the original full invocation did not exit successfully.
Evidence: `/tmp/fai769-native-shards-ci.log` (overall exit 201),
`/tmp/fai769-native-shards-reports-rerun.log`, and
`/tmp/fai769-native-shards-contracts.log`, and
`/tmp/fai769-native-shards-site-rerun.log`.
Exact-head hosted validation remains pending. No production behavior or test
timeout was changed.
The PR remains unmerged and is not yet green on GitHub.

## September 9 conflict-resolution checkpoint

Main advanced to `4f677d4b9` with the interactive dashboard builder. PR #543
is being reconciled through a normal merge commit, preserving the builder
and saved-exploration functionality. The prior green result below is
historical and does not validate this new merge.

Main's released PostgreSQL migrations 003 and 004 remain byte-identical.
The unmerged saved-exploration migration is now 005, with revision-4-to-5
upgrade coverage. No deployed database or historical data was changed.
If a development database previously applied this PR's unmerged migration
003, inspect and reconcile its migration history before applying the new
chain; it is not equivalent to main's revision 3. No automatic history rewrite,
database reset, or data transfer is included in this conflict resolution.

The merge also preserves both API/signal contract families, builder catalog
updates and saved handoff routes. Append now supplies canonical generated
security-retention audit metadata required by main's capability guards,
keeping the durable command/event identity distinct from trace IDs. The
visualization decoder preserves received schema versions. A self-contained
visual-frame component was extracted to satisfy the unchanged quality budget.

Focused migration, authoring/HTTP/application, architecture, contract/typecheck
and dashboard DOM tests passed. The first full `task ci` run failed required
PostgreSQL conformance: a losing concurrent update incorrectly returned not-found
instead of stale-revision. The failed run was stopped. A deterministic row-lock
barrier reproduced the failure against the old query. The fix locks lifecycle
metadata before reading its current revision in a fresh statement snapshot,
without loading payload bytes; the regression passed five consecutive runs.
This addresses the inconsistent-snapshot behavior documented for PostgreSQL
[Read Committed](https://www.postgresql.org/docs/18/transaction-iso.html#XACT-READ-COMMITTED).
The full saved PostgreSQL suite also passed under Go's race detector. The second
full CI run passed the saved concurrency regression but failed main's existing
refresh test: its 10-millisecond lease expired before admission completed.
Separate frontend validation found a catalog test assuming UTC while the local
browser used Europe/Berlin. Reviewed test-only corrections use a two-second
admission lease, server-observed expiry, exact PostgreSQL blocker verification,
and an explicit UTC browser context. Production lease guards and localized
rendering remain unchanged. All frontend shards passed, and the engineering
quality budget remains unchanged and passing. Both refresh fencing tests passed
five consecutive runs each, retaining the stale-writer assertions.

The final `task ci` run passed every Go, PostgreSQL conformance, and frontend
lane. Its last `generated:check` step rejected staged changes because it requires
a committed worktree; regeneration produced no unstaged changes. That final
gate passed separately on normal merge commit `9ac314d5e` with a clean worktree;
the final quality-budget check also passed. Thus all required local CI stages
passed, while the original full invocation's exit status remains recorded
accurately below. Evidence:
`/tmp/fai769-conflict-task-ci-3.log` (test lanes passed; overall exit 201 at the
clean-worktree check), `/tmp/fai769-conflict-native-race.log`, and
`/tmp/fai769-refresh-fence-after-barrier.log`, and
`/tmp/fai769-conflict-generated-final.log` (exit 0).
Exact-head CI must be checked after the reviewed merge
is pushed. No force push, main merge, or auto-merge is allowed.

## Previous checkpoint (September 8)

Implementation and full local CI validation reached a stable checkpoint.
The main-target [PR #543](https://github.com/flidai/leapview/pull/543) is open
and unmerged. Exact-head GitHub checks remain the next gate; initial security
findings are being corrected and revalidated. This report does not authorize
a main merge or a production deployment.

Linear milestones 1–3 are complete. Milestone 4 remains in progress for final
integration/release readiness and Jacob's review. No new Jacob comments were
found in the latest project/issue check.

## Reviewed changes

- Migrated saved explorations to native PostgreSQL/pgx persistence: immutable
  revisions, project-scoped reads, bounded pagination, concurrent revision
  checks, durable idempotent replay and atomic canonical Access auditing.
- Added forward PostgreSQL migration 003 and startup revision verification,
  preserving immutable migrations 001 and 002 and existing runtime-role
  boundaries. Runtime cannot disable guards or perform owner DDL.
- Wired native persistence into production through the analytics module and
  the existing runtime pool/Access audit authority. Missing or typed-nil saved
  services fail closed.
- Retired only the superseded saved SQLite adapter, migration 096, generated
  query entry and replaced tests. Main's other SQLite fixtures remain at 095.
  Removed source is recoverable in Git.
- Preserved current main's portable resource manifests, semantic authorization,
  dashboard visuals and command identity contracts. Optional-dataset/multi-root
  agent contexts now use the immutable policy-bound planner.
- Corrected merged browser command syntax, verified the merged route/auth
  inventory, and aligned the dashboard signal declaration with the existing
  visualization version 11 producer. Added regression coverage instead of
  relaxing validation or quality budgets.

## Validation

Code checkpoint: `aa686efaa35c267823f26e1e63bdd16eaa9dd7bf`.

| Check | Result |
| --- | --- |
| Full `task ci` | Passed, exit 0 |
| Full PostgreSQL conformance within CI | Passed |
| All Go application shards and package suites | Passed |
| API generator Go and TypeSpec suites (63 TypeSpec tests) | Passed |
| Frontend core, reports, chat, data and site shards | Passed |
| SQL generation/vet/audit and PostgreSQL baseline query preparation | Passed |
| Generated public contract snapshots | Passed |
| Complete architecture audit | Passed |
| Native saved repository and app audit adapter under Go race detector | Passed |
| Concurrent CAS, replay, project isolation and metadata-only reads | Passed; strengthened cases repeated three times |
| Direct-SQL tamper, timestamp, schema mirror and migration-role tests | Passed |
| Protected multi-root grant denial/allow control | Passed |
| Final engineering quality budget | Passed without raising limits |

Local evidence: `/tmp/fai769-task-ci-4.log` ends with `CI_EXIT_STATUS=0`;
race evidence is in `/tmp/fai769-native-race.log`.

The subsequent security-correction patch was reviewed separately: focused
HTTP and query-lowering tests, the maximum-selection/range-expansion test
under the race detector, dependency-evidence validator tests, the full
`security:source` task and the quality budget passed. JavaScript evidence was
refreshed through the live repository tool across all five graphs; existing
findings and lockfile hashes were unchanged. Remote checks on the corrected
head must pass before this PR can be described as green.

The corrected head cleared the remote security gates. A subsequent data
frontend job timed out during its browser setup before running any tests.
The affected fixture now allows 15 seconds for setup; product assertions and
test-body timeouts are unchanged. Three independent local suite invocations
passed (13 tests each). Nonstandard same-process `--rerun-each=3` stress runs
remain unstable, including browser-close timeouts after successful startup;
that result is not counted as passing and remains a test-harness follow-up.
The normal isolated-process CI lane still requires exact-head verification.

Earlier CI attempts exposed the integration fixes listed above. One attempt
ended with signal exit 143 before completion; it was not counted as passing.
The final complete run supersedes those attempts. Earlier feature-only release
QA in `saved-exploration-release-qa.md` is historical evidence, not the basis
for this main-readiness claim.

## Branch and merge controls

- Feature input: `d34602459af1ad8ddf831822baedd3c45bf8e0c5`.
- Current main integrated: `a97e07e35abb11a9f97052c28a3d8a1855c850bb`.
- Work branch: `codex/saved-exploration-main-readiness`.
- Main was brought into the feature branch through normal merge commits.
- No force push, main merge, auto-merge or merge-queue enrollment.
- Main-target PR must stay open and unmerged for Jacob.

## Remaining steps and rollout limits

1. Verify all required GitHub checks on PR #543's exact head; investigate and
   patch failures with normal commits/pushes. Initial remote blockers were a
   high-entropy UUID test fixture flagged by Gitleaks, stale JavaScript
   manifest evidence after test-script changes, and CodeQL allocation-size
   arithmetic alerts. No security gate or suppression policy was weakened.
2. Obtain Jacob's final product/code review. Keep FAI-769 in review rather than
   declaring production release complete.
3. Only after separate approval, follow the repository's final merge and
   deployment process, including the required migration/release checks.

This is a code/schema migration tested with disposable PostgreSQL databases.
No live database was changed and no historical SQLite data was imported.
An existing SQLite data transfer would require a separately validated plan and
backup; the PostgreSQL schema upgrade is not such a transfer.
