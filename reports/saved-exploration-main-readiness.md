# Saved Data Exploration: main integration review

Tracking: FAI-769. This report records preparation for Jacob's review, not
authorization to merge or release.

## Inputs and controls

- Feature input: `d34602459af1ad8ddf831822baedd3c45bf8e0c5` (PR #539).
- Main input: `a1c287f409620172938c03637daad646c6a29c64`.
- Main has 39 commits outside the feature branch; initial merge has 32
  conflicted paths. Earlier feature-only green checks do not validate this
  integration.
- Work branch: `codex/saved-exploration-main-readiness`.
- Normal merge history and normal pushes only. Main-target PR must remain
  unmerged, with no auto-merge or merge-queue enrollment.

## Integration plan

1. Reconcile authored contracts, semantic-access policy, runtime wiring and
   explorer state without restoring retired production persistence paths.
2. Regenerate derived contracts from the merged sources; independently review
   conflict resolutions and meaningful nonconflicting integration changes.
3. Run focused regression tests, then `task ci` and the explicit quality budget
   at a stable checkpoint. Record failures and fixes without weakening gates.
4. Open a main-target PR and verify required checks on its exact head. Prepare
   Jacob's final review request; do not merge.

## Resolution decisions

- Keep main's dependency scanner implementation, controlled JavaScript
  evidence/refresh workflow, tests and 45-minute CI timeout. The feature's
  interim live-audit retry implementation is superseded by that workflow.
- Preserve both dashboard Explore-from tests and main's separate dashboard
  visuals/date-picker test lanes.
- Do not restore removed analytics/admin SQLite query paths. Saved-state
  storage and production wiring require explicit compatibility review.

## Validation status

Integration in progress. Main-target readiness has not yet been established.
The release QA evidence in `saved-exploration-release-qa.md` describes the
earlier feature checkpoint; it is not a substitute for this validation.

- Dependency-security tooling focused tests: passed.
- `task db:generate` and `task api:generate`: passed.
- Full generation initially found duplicate `minItems`/`maxItems` handling
  after the source merge; the generator source is fixed and its focused tests
  pass. A complete generation run remains pending native runtime integration.
- Focused exploration tests pass the pure domain/application packages, but
  the SQLite repository tests do not compile because main removed the old
  Access SQLite `RecordAuditIntent` implementation.

## Required additional persistence work

Main's production access module now requires injected PostgreSQL persistence;
the old database/audit fields have been removed from runtime composition.
The feature input constructed a SQLite repository. Main's architectural rules
prohibit using SQLite adapters in production.

Readiness therefore requires a native PostgreSQL saved repository, schema
provisioning, atomic Access audit integration, production composition and
concurrency/replay/isolation regressions. This is a substantive persistence
migration, not a cosmetic merge resolution. Do not substitute an in-memory
repository, disable saved functionality, or weaken architecture gates.

No main-target PR has been opened yet, and nothing has been merged or pushed
to main. Local reconciliation is preserved for continuation.

## PostgreSQL migration authorization and checkpoints

The user explicitly approved native PostgreSQL migration. Work is tracked in
FAI-769 and remains subject to review and validation:

1. Native pgx repository and schema: immutable revisions, project isolation,
   concurrent CAS, durable idempotent replay and same-transaction audit.
2. Capability-owned production persistence and audit composition; a reviewed
   forward Goose migration, without changing the immutable baseline.
3. Real PostgreSQL repository and mounted handoff regressions, regenerated
   contracts, stable local CI and exact-head main-target checks.

This authorizes code migration and disposable test databases, not changes to
any live deployment or transfer/deletion of user data. No production data
conversion has been performed. Main merge and auto-merge remain prohibited.

## Migration review checkpoint

- Native repository, forward migration and production audit wiring are being
  implemented in separate delegated workstreams. They are not yet accepted as
  complete; PostgreSQL concurrency and schema-tampering tests remain required.
- Canonical agent context now retains main's semantic-access checks for the
  typed exploration spec, including scoped filters, sort aliases and pivot
  references. Focused context and authorized-definition tests pass.
- The native persistence package has an exact analytics adapter classification;
  no SQLite architecture exception was added. Focused boundary tests pass.
- Main's API protocol requires explicit idempotency and cursor-signing ports.
  The HTTP replay unit fixture now supplies those ports; revoked-access replay
  remains denied without dispatching another mutation. The focused test passes.
- Mounted PostgreSQL dashboard setup exposed an incompatible legacy command-ID
  producer. The fix and stable-retry regression are in progress, not waived.
- Broader explorer tests exposed a detached compiled-model projection regression
  and changed missing-binding diagnostics. These are under review before CI.
- Operational documentation distinguishes the PostgreSQL schema upgrade from a
  historical SQLite data import. No live-data migration has been run.
- Full APIGen Go package tests pass; the TypeSpec suite passes all 63 tests.
  App API contract tests pass with 200 aggregate operations (main's 192 plus
  eight saved-exploration operations), without restoring removed operations.
- The full architecture audit caught module-facade import violations in the
  first native wiring draft and a missing Docker sqlc-output copy. The Docker
  input is fixed; native wiring is being routed through analytics/module.
  Legacy saved SQLite files remain until replacement conformance is verified.
- Full `task generate` now passes, including database bindings, public
  contracts and documentation fixtures.
- Native PostgreSQL core tests and all 11 ported legacy repository cases pass.
  Stronger reviewer assertions for scoped pagination, corrupted-payload
  metadata-only reads and exact stale-revision concurrent CAS pass three runs.
- Review corrected the inverted `includeArchived` SQL predicate and bound
  canonical audit scope/actor/request digest to trusted mutation evidence.
- PostgreSQL-mounted dashboard append/publish/reopen passes. Native command
  identities now reuse real client UUIDv7 idempotency keys; a new tracing ID
  cannot change the durable retry identity, and missing client keys fail closed.
- Remaining gates include direct-SQL operation-snapshot integrity, migration
  role/upgrade conformance and assembled saved app workflows. The first native
  app-fixture run still returns unavailable for saves; this is being diagnosed,
  not suppressed. Full CI and main-target remote checks have not run yet.

## Post-migration checkpoint

- The assembled saved API, composition and mounted monthly workflow subset
  now passes against native PostgreSQL persistence. The mounted fixture also
  needed the analytics-owned saved UI bindings after service injection.
- Direct-SQL lifecycle and replay-snapshot conformance now passes. Review is
  tightening valid late-evening timestamps, nanosecond ordering and runtime
  role restrictions before the final migration test run.
- Removed the superseded saved SQLite repository, its migration/tests and
  generated query entry after native replacement tests passed. Existing
  platform SQLite fixtures remain untouched; Git retains the removed source.
- The quality budget passes without a policy increase: the numeric-literal
  and array-bound regression moved into the existing constraints test suite,
  whose three tests pass.
- Independent review found that agent context incorrectly rejected canonical
  queries without an explicit starting dataset. A governed multi-root fix and
  denied-member regressions are in progress; no authorization bypass is allowed.
- Remote main advanced to `a97e07e35abb11a9f97052c28a3d8a1855c850bb`
  while integration was underway. Reconcile that additional recovery-evidence
  commit by normal merge before the stable CI checkpoint.
- No main-target PR, remote push, main merge, live migration or data import has
  occurred. Full CI and exact-head remote checks remain outstanding.

## Accepted native persistence validation

- Full saved PostgreSQL package and assembled saved app subset pass with
  mandatory PostgreSQL conformance enabled (no unavailable-database skips).
- Native repository and application audit adapter pass under Go's race
  detector (`84.583s` and `4.448s` respectively).
- Migration 2-to-3 upgrade, canonical schema mirror and role boundaries pass.
  Runtime cannot disable triggers or drop tables; readonly writes are denied
  with the expected permission error, rather than an unrelated SQL failure.
- Timestamp tests include valid 23:59 nanoseconds, invalid hour 24/second 60,
  invalid calendar dates and lifecycle/archive timestamp ordering.
- UUID and opaque actor audit identities are retained in their appropriate
  fields. A mismatched principal rejects the mutation and rolls back lifecycle,
  revision, operation-ledger and audit rows. This follows Access's explicit
  optional UUID principal plus opaque actor contract.
- Final `task db:generate` passes after SQLite retirement. The module API
  boundary test passes; a final complete architecture audit is running.
- Optional-dataset and overlapping pivot contexts now pass through the same
  immutable, policy-bound planner as query execution. Agent package tests pass;
  an additional protected-grant regression is being added before CI.

The final complete architecture audit passes (`24.523s`). The protected-grant
regression also passes: the same valid multi-root exploration is denied without
the required grant and accepted with its matching authority assignment. Source
integration is ready for a local merge commit and stable CI, not yet for a
green-PR claim. The staged diff's sole whitespace warning is an existing blank
EOF line in incoming main's `internal/platform/ci/ci-health-audit.md`; that
unrelated generated audit document was preserved unchanged.

## Stable CI findings and follow-up

- The first stable run exposed a missing closing brace in the merged browser
  command helper. The reviewed fix preserves UUIDv7 command identities and
  explorer tab headers; three focused tests and the browser bundle build pass.
- The next run was terminated with exit 143 before completion, without a
  reported test failure. It is not counted as a successful CI run. Validation
  was restarted in a tracked background process with an explicit exit marker.
- That run exposed stale merged route-inventory and legacy SQLite-tail
  expectations, plus a production dependency fixture missing the new required
  saved service. The route delta was reviewed for ownership/authentication
  before updating its digest; no route or authorization requirement was removed.
- Legacy SQLite remains at migration 095; native saved persistence lives in
  PostgreSQL migration 003. The sequence/restart tests now check that intended
  split and pass. The production fixture retains all earlier dependency checks,
  and the saved-service gate additionally rejects typed-nil interfaces.
- All focused fixes pass. Complete CI, frontend suites and exact-head remote
  checks remain outstanding; no PR or main merge has occurred.

Frontend validation exposed a stale dashboard signal declaration: it advertised
schema version 4 while the canonical visualization IR and backend producer use
11. The signal source now matches the existing producer, with signal/envelope
parity coverage and the canonical version constant in its fixture. App type
checking, 11 focused visualization tests and Go signal contract tests pass.
The reports, chat, data and site frontend shards all pass. A new full CI run
will validate these reviewed fixes together before any push or PR.
