# Saved Data Exploration: main-readiness report

Tracking: [FAI-769](https://linear.app/flid/issue/FAI-769/reconcile-saved-data-exploration-with-current-main-and-prepare-green).

## Current status

Implementation and local validation are complete. The main-target PR and
exact-head GitHub checks are the next gate. This report does not authorize a
main merge or a production deployment.

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

1. Open the main-target PR and verify all required GitHub checks on its exact
   head; investigate and patch any real failure with normal commits/pushes.
2. Obtain Jacob's final product/code review. Keep FAI-769 in review rather than
   declaring production release complete.
3. Only after separate approval, follow the repository's final merge and
   deployment process, including the required migration/release checks.

This is a code/schema migration tested with disposable PostgreSQL databases.
No live database was changed and no historical SQLite data was imported.
An existing SQLite data transfer would require a separately validated plan and
backup; the PostgreSQL schema upgrade is not such a transfer.
