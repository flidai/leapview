# PostgreSQL access SQLC exceptions

The access PostgreSQL adapters use generated, capability-owned SQLC leaves for
all reachable static reads and writes. One migration-only exception remains;
it is deliberately not a repository data-access query.

## Schema-owned SQL

`schema.sql` is migration input, not generated query code. It owns schemas,
tables, indexes, constraints, append-only/revocation triggers, helper
functions, role grants, and the `DO` grant blocks. SQLC must parse this file,
but must never replace or generate it.

## Repository SQL

- `repository.go:ApplySchema` executes the embedded `schema.sql` migration
  document in a caller-owned transaction. This is schema DDL, not a static
  repository leaf, and remains the sole raw `Exec` exception.

## Generated leaves

`queries/oauth.sql`, `queries/principal.sql`, `queries/core_ops.sql`,
`queries/extended_ops.sql`, `queries/authoring_ops.sql`,
`queries/scim_ops.sql`, `queries/snapshot_ops.sql`, and `queries/audit_ops.sql`
contain the stable OAuth, principal, core access, extended access, device
authorization, authoring credential, instance-clock, SCIM, snapshot, and audit
leaves. `internal/db/*.go` is generated with sqlc v1.30.0 and
`sql_package: pgx/v5`. Repository Go files retain transaction ownership,
fosite and authoring replay/error mapping, secret handling, audit orchestration,
state-machine checks, and domain conversion around those generated methods.

## Coverage

The PostgreSQL 18 integration suites in `internal/access/postgres/*_test.go`
exercise principal, authorization, session, device, credential, replay,
revocation, SCIM reconciliation, snapshot immutability, audit filtering, and
database-clock invariants. OAuth replay, rotation, invalidation, and
transaction behavior are covered by `internal/access/http/mcpoauth/*_test.go`.
These tests intentionally use direct admin SQL for schema/permission and
invariant assertions; that test SQL is not production repository code.
