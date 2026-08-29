# PostgreSQL access SQLC exceptions

The access PostgreSQL adapters use `db` for stable leaf DML and reads.  The
following SQL remains handwritten by design.  Each item is either part of the
security/transaction boundary or requires SQL that SQLC cannot represent as a
single named query without changing its semantics.

## Schema-owned SQL

`schema.sql` is migration input, not generated query code.  It owns schemas,
tables, indexes, constraints, append-only/revocation triggers, helper
functions, role grants, and the `DO` grant blocks.  SQLC must parse this file,
but must never replace or generate it.

## Repository SQL

- `repository.go`: `RecordAuditEvent` performs canonical replay detection in
  the caller's transaction (insert-if-absent, reread, and exact intent/digest
  comparison).  `auditEventColumns` is a fixed projection used by bounded
  reads; retaining it keeps audit payload mapping and conflict errors in Go.
- `access_audit.go`: filtered audit listing has optional predicates and a
  cursor tuple.  It is kept as one query so ordering and cursor bounds cannot
  drift; canonicalization and aggregate sequencing remain in Go.
- `scim.go`: SCIM reconciliation intentionally loops over principals/groups,
  computes the desired membership set in Go, and applies revocations and
  inserts in one transaction.  Dynamic filters and the `ANY(uuid[])` member
  predicate stay handwritten to preserve set-diff atomicity.
- `snapshot.go`: snapshot publication validates deterministic identities,
  computes the digest in Go, and writes immutable rows plus grants/policies in
  one transaction.  The transaction wrapper and conflict/error mapping stay
  handwritten.
## Generated leaves

`queries/oauth.sql`, `queries/principal.sql`, `queries/core_ops.sql`,
`queries/extended_ops.sql`, and `queries/authoring_ops.sql` contain the stable
OAuth, principal, core access, extended access, device authorization,
authoring credential, and instance-clock leaves. `internal/db/*.go` is
generated with sqlc v1.30.0 and `sql_package: pgx/v5`. The repository Go files
retain transaction ownership, fosite and authoring replay/error mapping,
secret handling, audit orchestration, state-machine checks, and domain
conversion around those generated methods.

## Coverage

The PostgreSQL 18 integration suites in `internal/access/postgres/*_test.go`
exercise principal, authorization, session, device, credential, replay,
revocation, and database-clock invariants. OAuth replay, rotation,
invalidation, and transaction behavior are covered by
`internal/access/http/mcpoauth/*_test.go`. These tests intentionally use direct
admin SQL for schema/permission and invariant assertions; that test SQL is not
production repository code.
