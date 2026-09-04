# Storage architecture

## Summary

Production and development serving use one PostgreSQL control plane and one
process-owned DuckDB `DatabaseInstance`. That DuckDB instance is the sole
client of one PostgreSQL-backed DuckLake catalog and executes bounded serving
reads and refresh transactions over DuckLake-managed Parquet files. Isolated
development/evaluation fixtures may use embedded SQLite and a local DuckLake
catalog; those adapters are test-only and are never selected by application
composition or production serving.

Runtime generations do not own DuckDB engines or catalog attachments. They own
immutable compiled plans, an exact DuckLake snapshot id, a process-memory L1
cache scope, and snapshot protection. No L2 or L3 cache is admitted; a future
L2 tier requires independent identity, security, recovery, multi-node,
retention, and rebuildability evidence.

## Storage ownership

Production control-plane and DuckLake metadata live in PostgreSQL. Local and
evaluation fixtures use the node-local layout below:

```text
.leapview/
  leapview.db               # local/evaluation SQLite control-plane fixture
  ducklake/catalog.duckdb   # local DuckDB-backed DuckLake metadata catalog
  data/                     # DuckLake-managed Parquet files
  artifacts/                # immutable project bundles
  runtime/                  # ephemeral extracted artifacts
  tmp/duckdb/               # bounded DuckDB temporary storage
```

PostgreSQL owns projects, releases, deployments, active serving pointers,
authorization, durable jobs, idempotency, leases, and audit records in
production. SQLite remains an explicit adapter for local/evaluation fixtures;
the documentation site's immutable search index is separate from the control
plane and generated from the embedded documentation catalog.

DuckLake owns analytical schemas, snapshots, changesets, statistics, schema
evolution, and physical-file manifests. Production metadata is PostgreSQL-
backed; local/evaluation fixtures may use the local DuckLake catalog file.
Parquet owns materialized analytical data. Disposable query caches are never
authoritative.

Local fixture catalog files are process-private writable state. Independent
DuckDB instances, LeapView nodes, or hosts never open or mount them
concurrently. Production catalog access is target-owned and PostgreSQL-backed;
any future shared catalog variant requires a separate product decision and
protocol.

## Process-owned execution

The node constructs DuckDB exactly once during process composition. Its bounded connection pool shares one scheduler, buffer manager, catalog view, extension set, access policy, memory limit, temporary-storage limit, and thread limit.

Workload admission bounds and fairly schedules logical operations before they acquire a DuckDB connection. An admitted operation retains one connection for its complete query bundle or refresh transaction. The connection pool has no second admission queue.

Runtime retirement closes generation-scoped cache state and releases snapshot protection. It never closes or replaces the process DuckDB instance. A fatal instance failure fails readiness and terminates the process for supervisor restart.

## Serving snapshots

Every active serving state points to one exact DuckLake snapshot. Every physical relation in a serving plan—including facts, joins, subqueries, and generated bundle plans—is snapshot-qualified:

```sql
SELECT ...
FROM (FROM lake.model.orders AT (VERSION => 42)) orders
LEFT JOIN (FROM lake.model.customers AT (VERSION => 42)) customers
  ON orders.customer_id = customers.id
```

A request resolves one runtime generation once and holds its lease until the logical operation finishes. Old and new generations may query different snapshots concurrently through the same DuckDB instance.

## Refresh and activation

A refresh:

1. Resolves and validates bounded, finalized source artifacts.
2. Runs its planned table changes in one DuckLake transaction.
3. Commits an immutable candidate snapshot with ownership and fencing metadata.
4. Validates the candidate without making it serving state.
5. Activates the candidate's exact snapshot id in the control-plane transaction.
6. Publishes the prepared runtime atomically after durable activation.

DuckLake commit and control-plane activation are deliberately not one
cross-store transaction. Production activation is PostgreSQL-backed; local
and evaluation fixtures use their explicit SQLite adapter. If activation fails
after the DuckLake commit, the candidate is an orphan: it is never implicitly
active and is safe to retry or reclaim. Restart reconciliation classifies
candidates from durable ownership, fencing, and active-pointer state rather
than catalog recency.

A failed refresh leaves the previous active snapshot unchanged.

## Resource and access boundaries

Node-wide configuration bounds DuckDB memory, temporary storage, threads, connection concurrency, retained query results, and cache data. Workload admission provides fairness across classes and project resources; it is not a hard per-project CPU or intermediate-memory partition. Workloads requiring hard isolation use separate deployments or OS/container resource domains.

Maintained DuckDB connectors acquire declared external sources only inside admitted refresh sessions. Each refresh pins one connection, stages remote data into connection-local temporary tables, validates the schema, and removes attachments and temporary secrets before the single-writer DuckLake commit. Serving and Data Explorer paths are snapshot-only and never resolve source credentials. DuckDB permits only LeapView's fixed official signed extension allowlist; automatic loading, unsigned extensions, authored extension names, and custom repositories remain disabled.

## Retention and recovery

Retention protects every snapshot referenced by an active or leased runtime generation. Only unprotected snapshots may expire; physical cleanup follows DuckLake metadata and remains distinct from snapshot expiration.

Backup and restore cover the PostgreSQL control plane and PostgreSQL-backed
DuckLake catalog in production. Local/evaluation backups cover their explicit
SQLite and DuckDB fixture equivalents, together with Parquet data, artifacts,
and required configuration. Recovery validates every active pointer against an
existing protected snapshot before the node becomes ready.

## Acceptance criteria

- The serving process constructs exactly one DuckDB instance.
- A held snapshot-pinned read does not prevent a refresh commit through another connection in that instance.
- Separate runtime generations can concurrently observe their exact old and new snapshots.
- No serving plan contains an unqualified physical relation.
- Runtime retirement cannot close the node DuckDB instance.
- Aggregate analytical work remains within the configured node envelope.
- Failed activation never makes an orphan candidate visible.
- Retention never deletes a snapshot protected by an active runtime lease.
