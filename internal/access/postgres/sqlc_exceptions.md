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
- `access_core.go`: password/token/session creation and verification include
  secret hashing, HMAC fingerprints, database-clock expiry checks, and
  revocation cascades.  Their writes are deliberately grouped in caller-owned
  transactions.  Membership and identity upserts use guarded `EXISTS`/
  `NOT EXISTS` statements whose atomicity is part of the authorization
  boundary.  Dynamic group filters and security predicates remain fixed in
  hand-authored SQL.
- `access_extended.go`: disabling/revoking a principal updates several
  append-only tables in one transaction; avatar deduplication and preference
  replacement similarly require multi-write orchestration.  Keep the guarded
  statements together with their domain validation.
- `authoring_auth.go`: device/authoring session issuance, refresh rotation,
  and desktop token renewal combine database-clock reads, `FOR UPDATE` locks,
  guarded transitions, and credential hashing.  Splitting these into
  generated calls would weaken replay/fencing semantics, so only their leaf
  reads may move in a later slice.
- `scim.go`: SCIM reconciliation intentionally loops over principals/groups,
  computes the desired membership set in Go, and applies revocations and
  inserts in one transaction.  Dynamic filters and the `ANY(uuid[])` member
  predicate stay handwritten to preserve set-diff atomicity.
- `snapshot.go`: snapshot publication validates deterministic identities,
  computes the digest in Go, and writes immutable rows plus grants/policies in
  one transaction.  The transaction wrapper and conflict/error mapping stay
  handwritten.
- `instance_initialization.go`: initialization is a one-time, database-clock
  guarded marker used during process bootstrap; the surrounding transaction
  and idempotency handling remain in Go.

## Generated leaves

`queries/oauth.sql` and `queries/principal.sql` contain the stable OAuth
client/session/assertion leaves and principal reads.  `internal/db/*.go` is
generated with sqlc v1.30.0 and `sql_package: pgx/v5`; `postgres_store.go` and
`access_core.go` retain transaction ownership, fosite replay/error mapping,
secret handling, and domain conversion around those methods.

## Coverage

The PostgreSQL 18 integration suites in `internal/access/postgres/*_test.go`
exercise the principal reads and their revocation/clock invariants.  OAuth
replay, rotation, invalidation, and transaction behavior are covered by
`internal/access/http/mcpoauth/*_test.go`.  These tests intentionally use
direct admin SQL for schema/permission and invariant assertions; that test SQL
is not production repository code.
