# Saved Data Exploration: main-readiness report

Tracking: [FAI-769](https://linear.app/flid/issue/FAI-769/reconcile-saved-data-exploration-with-current-main-and-prepare-green).

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
gate must be rerun after the normal merge commit. Evidence:
`/tmp/fai769-conflict-task-ci-3.log` (test lanes passed; overall exit 201 at the
clean-worktree check), `/tmp/fai769-conflict-native-race.log`, and
`/tmp/fai769-refresh-fence-after-barrier.log`.
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
