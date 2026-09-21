# Schema 24 Convergence

## Problem

Goose records applied migration numbers, not migration content. Two repositories produced different migration 023 files, so a database reporting revision 23 does not by itself prove compatibility with the current runtime. The live audit lineage applied dashboard authoring deletion support, while current main applied the recovery qualification ledger.

## Schema 23 Lineages

### Live Audit Lineage

Commit `938ff6b68726b72959eae5bdb2696954b890b528` introduced `023_dashboard_authoring_delete.sql`. It creates `dashboard.authoring_delete_commands` and `dashboard.authoring_delete_dashboard(...)`. The exact historical fixture used by the qualification test has SHA-256 `2c4fadac6aa63193883e655218f938f8e7f66f85c54a773e95fa9061e10efcf2`.

### Current Main Lineage

The branch is rebased on main revision `00084131fcdeaab0ea8800446fe334c04b23c8a1`, which contains `023_recovery_qualification_ledger.sql`. It creates the recovery schedule, enqueue cursor, occurrence, execution-attempt, and evidence-attempt relations together with their indexes, guards, retention function, triggers, and role grants.

## Migration Design

Revision 024 establishes the current runtime contract by replaying the recovery-ledger `Up` definition additively. `CREATE TABLE IF NOT EXISTS`, `CREATE INDEX IF NOT EXISTS`, `INSERT ... ON CONFLICT DO NOTHING`, and `CREATE OR REPLACE FUNCTION` make the operation safe for both known lineages. Trigger replacement is metadata-only and installs the reviewed trigger definitions without deleting table rows. The migration does not alter or remove dashboard authoring objects.

## Migration 024

`internal/platform/postgres/migrations/024_schema_23_convergence.sql` contains the exact recovery-ledger `Up` contract from main revision 023 plus a deliberately prohibited `Down` path. A downgrade cannot safely infer which revision-23 lineage preceded convergence, so it raises `schema 24 convergence is forward-only; destructive down is forbidden`.

## Data Preservation

The live-lineage test preserves a project identity and a populated dashboard delete-command fence through the upgrade. The main-lineage test preserves a project identity, recovery schedule, and linked recovery occurrence. Revision 024 contains no data-bearing `DROP` or `DELETE` operations.

## Two-Lineage Qualification

`TestSchema24ConvergesBothRevision23Lineages` constructs each revision-23 state in PostgreSQL 18, applies revision 024 through the production `ApplyGoose` entry point, verifies revision 24, runs `VerifyGoose`, checks the recovery schema contract and runtime-role access, and asserts the seeded records remain unchanged.

## Fresh Database Qualification

`TestSchema24FreshDatabaseAndForwardOnlyDown` migrates an empty PostgreSQL 18 database through the complete embedded sequence to revision 24, verifies the recovery schema and runtime compatibility, and proves that a requested downgrade is rejected without changing the recorded revision.

## CurrentRevision

`internal/platform/postgres/migrations/goose.go` now declares `CurrentRevision = 24`. `VerifyGoose` continues to compare the applied version with the embedded migration target and requires every migration status to be applied.

## Artifact Qualification Fix

The Main artifacts qualification checkout lacked ignored sqlc output packages. `image:qualify:production` invoked `api:generate` and then compiled `cmd/leapviewctl`, but it never invoked the repository's pinned `db:generate` task. The task now runs `db:generate` before `api:generate` and qualification. A Taskfile contract test locks in that ordering.

## CI Validation

The following local validation passed on the focused branch:

- `go test ./internal/platform/postgres/migrations -run 'TestSchema24' -count=1 -v`
- `go test ./internal/platform/postgres/migrations -count=1`
- `bun test scripts/frontend_ci_contract.test.ts`
- `task db:generate`
- `task db:verify`
- `task deploy:check`
- `task ci`, including the real PostgreSQL 18 conformance inventory and generated-contract checks

The GitHub Main artifacts workflow result is recorded below after it completes.

## Qualified Artifact

Pending the normal Main artifacts workflow. The exact source revision, immutable image digest, provenance, admission, and qualification result will be recorded here after success.

## Remaining Limitations

Revision 024 deliberately preserves the audit lineage's dashboard-delete objects as historical state; current main does not depend on or recreate those audit-only objects on databases that never had them. This change qualifies the database and runtime artifact only. It does not deploy either demo host, move data, change DNS, or change a runtime pin.
