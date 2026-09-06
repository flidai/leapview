# PostgreSQL migration authority

This stacked branch retains its existing transaction-owned migration framework.
It does not adopt upstream main's Goose ledger or support importing that ledger.
Database and role provisioning remain outside this package.

## Execution and evidence

`Apply` requires a caller-owned READ COMMITTED transaction. It validates embedded
supersession metadata, acquires the database-scoped transaction advisory lock
`(1279612496, 1)`, reads the complete revision ledger, and validates a contiguous
known prefix before executing any migration DDL/DML. Inspection queries and lock
acquisition are the only SQL before planning. Applied exact tuples are skipped;
only pending artifacts run. Successful execution records the actual artifact ID
and exact-byte SHA-256. Final `Verify` requires the complete recognized history.
The caller commits on success and must roll back on every error. Lock errors
retain their PostgreSQL SQLSTATE and are reported explicitly. Context/transaction
timeouts bound lock waiting; there is no environment-dependent bypass.

Parameter and result casts in ledger recording deliberately use built-in types:
a failed fresh bootstrap can roll back domain creation and retry on the same
connection without stale pgx prepared-statement domain OIDs.

An absent ledger is accepted only for an empty database. An empty existing ledger,
history gaps, unknown IDs/checksums, future revisions, or nonempty untracked
databases fail closed. A valid shorter prefix is a supported upgrade, not a
history gap. `Verify` does not adopt or repair history and is not a general schema
drift auditor.

## Revision 002 supersession

Deployment investigation could not prove whether the original artifact was
applied to production, staging, or shared environments. No successful deployment
was found, but manual deployments cannot be excluded. Therefore the original
`002_project_identity_ledger.sql` remains byte-for-byte immutable.

[`002_supersession.json`](002_supersession.json) is the immutable declaration:

| Revision | Artifact ID | SHA-256 |
| --- | --- | --- |
| 2, historical | `002_project_identity_ledger` | `42f0dc3dacbbf4fa06aef8e2fcbcb6d3559fd97e2907a18fc596c1c4c01152f1` |
| 2, replacement | `002_project_identity_ledger_replacement` | `61385b011fa9d235f97dacfc783682a01d869bc06f043013b9ff0988dcd147e4` |

The replacement differs only in the two invalid `GRANT ... ON TABLES` statements,
which become `ON TABLE`. New databases record the replacement tuple, never the
original checksum for corrected SQL. Existing original tuples are preserved and
skipped. No other tuple is accepted. The declaration and actual revision row
together identify the historical artifact and the selected execution lineage.
Regression tests freeze both artifacts and the declaration. There is no manual
mark-repaired API, arbitrary checksum allowlist, or history rewriting command.

## Forward convergence

Revision 013 validates required identity tables, ownership, primary keys, and
enabled identity guards before applying the intended grants/revocations. It checks
effective runtime and read-only table privileges, including inherited privileges;
unexpected excess privileges fail rather than silently being accepted. It also
grants runtime SELECT on the revision ledger for readiness verification and adds
a TRUNCATE guard alongside the existing UPDATE/DELETE guard. No identity data,
publication schema, or lifecycle behavior is rewritten.

Revision 014 corrects the publication CHECK-name collision between immutable
003 and 008. It takes an ACCESS EXCLUSIVE publication-table lock, reports the
existing constraint definitions, and preflights every row before adding one
validated evidence-binding CHECK. Historical checks (including any stronger
deployment-local restrictions) are retained unchanged; their names are not
treated as proof of their definitions. The new CHECK enforces all seven intended
008 bindings, including digest equality, with no new canonicalization authority.
An unexpected collision with the new constraint name fails closed.

Corrupt rows abort the transaction with SQLSTATE `23514`; no row contents are
included in the error. No schema/revision change commits and no publication is
rewritten or deleted. Investigate with an authorized operator and use a reviewed
restore/forward recovery plan; do not disable immutable guards or rewrite ledger
checksums. This migration can block publication traffic while scanning existing
rows, so use a maintenance window and caller-owned lock/transaction timeouts.

## Supported paths and recovery

### Revision 014 operational review

The explicit relation lock is `ACCESS EXCLUSIVE` on
`project.contract_publication`, across **all instances**, not a row lock or an
instance-scoped lock. It conflicts with ordinary SELECT as well as publication
INSERT and DDL, and lasts until the caller commits or rolls back. RESET ROLE does
not release it. Apply also holds the database-local migration advisory lock;
recording revision 014 writes `platform.schema_revision` and DDL updates system
catalogs. Revision 014 does not explicitly lock or rewrite the identity/access
tables or DuckLake tables. Older pending revisions may acquire their own locks
in the same transaction. See PostgreSQL 18's [lock modes](https://www.postgresql.org/docs/18/explicit-locking.html)
and [constraint validation](https://www.postgresql.org/docs/18/sql-altertable.html).

Expect two full-table validation passes: the corruption preflight and ADD CHECK
validation, with hashing and repeated UTF-8/JSON conversion proportional to stored
canonical data. No table rewrite or index build is requested. Wall time depends
on row/byte volume, CPU, I/O and lock contention; fixture suite duration is **not**
a production estimate. No persistent environment size or production timing was
available for this review. Measure both passes on a representative restored copy
before approving a maintenance window.

An operator may run the exact SELECT-only corruption predicate from revision 014
on a read-only connection before maintenance, and inspect `pg_get_constraintdef`
and table size. This is advisory only: a successful unlocked check can become
stale. Do not move/omit the locked preflight or manually install the CHECK ahead
of the runner. Publishing and replay/read traffic will wait or time out while
the lock is held; blocked requests can also consume pool slots and affect other
control operations. Already-running analytics that do not use this relation are
not directly locked, but uninterrupted application availability is not promised.

Safe maintenance sequence (operator-owned; not executed against production by
qualification tests):

1. Confirm the exact database, PostgreSQL 18 version, recognized revision prefix,
   original/replacement 002 lineage and matching deployment artifact. Retain a
   verified backup/restore point and its matching application/configuration.
2. Inspect existing constraints and publication volume with an authorized reader;
   run the advisory corruption check and rehearse the normal migration path on a
   restored copy. Stop for corruption or unknown ledger history. Do not print
   canonical publication contents or credentials into general deployment logs.
3. Drain publication readers/writers, workers and long-running control
   transactions; prevent old application processes from restarting mid-upgrade.
   Have an authorized operator inspect `pg_stat_activity`, `pg_locks` and
   `pg_blocking_pids` if acquisition waits. Do not automatically terminate clients.
4. Run the matching artifact's existing native initialization/migration path
   (`leapview admin initialize --format json`, with reviewed production config
   and protected output). `adminpostgres.prepareSchema` calls Apply in one
   caller-owned transaction and commits immediately after verification. Use the
   migrator credential with existing owner-role membership, not runtime or a new
   superuser authority. Do not execute migration SQL manually without its ledger.
5. Keep lock and statement budgets explicit and bounded. The native migrator
   inherits `LEAPVIEW_POSTGRES_CONTROL_LOCK_TIMEOUT` (default 5s),
   `LEAPVIEW_POSTGRES_CONTROL_STATEMENT_TIMEOUT` (30s), and idle-transaction
   timeout (1m). Approve any maintenance-process-specific budget from rehearsal;
   do not globally disable limits. A timeout/error requires rollback, inspection
   and a fresh runner retry, not a fabricated applied record.
6. Before reopening traffic, verify the full ledger with the matching artifact,
   revision 014's ID/checksum, and a validated
   `contract_publication_evidence_binding_check`. Confirm existing immutable
   publication evidence is unchanged and exercise approved replay/read smoke
   checks. Start only the matching runtime artifact and then resume traffic.

Before commit, failure rolls back the new CHECK and revision record; original
publication data and history remain intact. After commit there is no supported
down migration or automatic old-binary rollback: older Verify implementations do
not recognize revision 014. Use a reviewed forward recovery or restore the
matching pre-upgrade database/application pair (including a plan for any writes
since backup). Do not delete revision rows, disable validation/immutable guards,
edit historical checksums, or silently discard corrupt publication evidence.

### Recognized installation paths

- Empty database: 001 → replacement 002 → 003–014.
- Valid recorded prefix without 002: apply replacement 002 and pending successors.
- Valid original-002 prefix: retain original evidence and apply pending successors.
- Valid replacement-002 prefix: retain replacement evidence and apply successors.
- Complete recognized history: verify and skip all migration SQL.
- Unknown, incomplete/non-contiguous, or manually altered history: stop for
  investigation. Do not edit checksums or fabricate revision records.

Failed transactions leave previously committed state intact. Retry after removing
the failure cause; tests exercise rollback of both DDL and revision records and
retry using the same pool. Concurrent migrators serialize until commit/rollback;
another database is independent.

There is **no downgrade compatibility claim**. Older binaries do not recognize
the replacement tuple and retain the historical replay defect. Retain tested
backups and the matching application artifact; recover using an operator-reviewed
restore or forward correction, never by deleting revision rows. Upstream Goose
integration needs a separately reviewed migration-authority reconciliation.

## Qualification

Run with Docker available and `LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=1`:

```sh
go test ./internal/platform/postgres/migrations -run '^TestMigrationSupersessionPostgreSQL18$' -count=1 -v
go test ./internal/platform/postgres/migrations -run '^TestContractPublicationCorrectionPostgreSQL18$' -count=1 -v
go test ./internal/platform/postgres/migrations -count=1
go test ./internal/project/identityledger/... ./internal/access/... ./internal/platform/architecture ./internal/app/adminpostgres -count=1
task test:go:postgres-conformance
task generated:check
task ci
git diff --check
```

The focused matrix covers fresh/bootstrap-prefix installs, both recorded lineages,
exact replay, invalid history, absent/empty ledgers, unsupported isolation, schema
guard drift, excess effective privilege, transactional rollback/retry, runtime
verification, immutable history, lock timeout, concurrent execution and separate
database locks. The historical-tuple fixture is explicitly synthetic; it is not
evidence that invalid SQL ever executed successfully.

Broader qualification results and independent blockers are recorded in the
[ADR-0016 evidence ledger](../../../../adr/specifications/data-contract-versioning-conformance.md)
and [ADR-0017 planner evidence](../../../../adr/specifications/semantic-access-planner.md).
