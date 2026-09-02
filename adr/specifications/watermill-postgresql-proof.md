# FAI-591 Watermill PostgreSQL qualification

Status: package/transaction integration qualified; production consumer not admitted

Date: 2026-08-31

Related: [ADR-0018](../0018-adopt-a-postgresql-centered-target-data-architecture.md)

## Decision

Pin Watermill core `v1.5.3` and `watermill-sql/v4` `v4.1.5`. Use Watermill for
the application message/router boundary, while keeping LeapView's PostgreSQL
event log and consumer-delivery tables authoritative. The mature core Router
and custom PostgreSQL Subscriber adapter are selected for a future admitted
consumer; enrollment is conditional on a concrete product-owned bounded,
idempotent effect. No real consumer is admitted for the current PostgreSQL
target release, and the stock SQL transport must not become a second event log.

## Evidence

`internal/platform/events/watermillproof/proof_test.go` runs the stock SQL
publisher against a disposable PostgreSQL 18 container and a migration-owned,
test-local schema.  The test uses both pgx bridges exposed by the pinned
versions:

- `BeginnerFromPgx` starts a transaction through the caller-supplied pgx pool
  without introducing a separate `database/sql` pool or connection path.
- `TxFromPgx` gives a publisher the exact caller-owned `pgx.Tx`.
- The pinned `TxFromPgx` wrapper is execution-only because it does not retain a
  context. The caller commits or rolls back the original pgx transaction with
  its explicit context; the Watermill publisher does neither.
- A source mutation and `Publish` commit together; rolling the transaction
  back leaves neither row.
- A subscriber configured with `InitializeSchema: false` does not create its
  offset table; `Subscribe` fails until that migration-owned table exists.
- `AutoInitializeSchema` is disabled.  Enabling it on a `TxFromPgx` handle is
  rejected by Watermill because DDL could implicitly commit the transaction.

The test also records the stock PostgreSQL transport shape.  Its message table
uses a `BIGSERIAL` offset and `xid8 transaction_id` primary key, while its
offset table stores one `offset_acked`/transaction checkpoint per
`consumer_group`.  Topic-derived table names and integer offsets are useful
scan mechanics, but they cannot represent LeapView's UUIDv7 event identity,
aggregate-scoped version ordering, consumer fence generations, replay roots,
poison state, or retention floor.  These limitations are asserted directly
from the adapter's generated SQL.

## Consequence

The proof confirms transaction integration and package compatibility only. It
does not admit a production consumer or event runtime, and it does not make
multi-node consumer, lag/DLQ, restore, or runbook qualification a blocker for
the current PostgreSQL target. Those gates become required when a concrete
consumer is admitted under FAI-593. The proof does not authorize production
Watermill tables, dual writes, Watermill jobs, or replacement of the canonical
event repository. Migrations continue to own every transport table and both
Watermill schema-initialization flags remain disabled in production and tests.
