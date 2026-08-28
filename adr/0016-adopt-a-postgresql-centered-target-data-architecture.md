# ADR-0016: Adopt a PostgreSQL-centered target data architecture

Status: accepted

Decision date: 2026-08-28

Implementation: pending (clean-slate target architecture)

Deciders: LeapView maintainers

Supersedes:

- [ADR-0009](0009-separate-control-and-physical-transactions.md),
  control-store selection only; its cross-store transaction and reconciliation
  invariants remain in force; and
- [ADR-0008](0008-isolate-ducklake-candidate-physical-state.md), private
  file-backed catalog and immutable catalog-object mechanics only; its
  candidate-isolation, immutable-publication, exact-identity, fencing, lease,
  retention, and reconciliation invariants remain in force.

Related: [ADR-0005](0005-use-project-wide-resource-graph.md),
[ADR-0015](0015-adopt-durable-audit-and-compliance-controls.md), the
[storage architecture](../docs/storage-architecture-spec.md), and the
[dependency-aware dashboard query cache](https://linear.app/flid/project/dependency-aware-dashboard-query-cache-e23514b5f578/overview)

## Context and problem statement

LeapView's current implementation places control-plane authority in
one node-local SQLite database. It owns projects, releases, deployments, active
serving pointers, access state, durable jobs, idempotency, leases, and audit
records. One process-owned DuckDB instance reads one private DuckLake catalog
and executes governed analytical work over DuckLake-managed Parquet files.

That architecture has strong single-process correctness and intentionally
separates control transitions from immutable physical publication. Its target
topology is nevertheless bounded to one writable application node. A second
node cannot safely share the SQLite authority or private catalog, durable work
and cache coordination are node-local, and recovery is coupled to a local
filesystem bundle. These are architectural constraints rather than evidence
that SQLite has failed under the current load.

LeapView also has three emerging cross-node needs:

- durable domain events that are atomic with the state changes they describe;
- a queryable, versioned projection of the compiler-owned resource and lineage
  graph; and
- dependency-addressed query-result reuse whose correctness does not depend on
  process lifetime or best-effort invalidation.

PostgreSQL can serve all three coordination needs, but it also offers features
that could blur existing boundaries. `LISTEN`/`NOTIFY` is not a durable event
log, recursive SQL does not make the database the compiler's graph authority,
and transactional tables are not an appropriate bulk store for disposable
Arrow results. The target architecture must therefore state both what
PostgreSQL owns and what it must not absorb.

LeapView has no live production state that must be upgraded in place. A
dialect-preserving port would therefore preserve obsolete migration history,
duplicate delivery authorities, file-catalog protocols, process-session
idempotency assumptions, and single-writer claim patterns without providing
product value.

This ADR defines LeapView's clean-slate end-state storage, delivery,
coordination, event, lineage, and cache topology. It does not prescribe the
task-by-task implementation plan, but it does prohibit compatibility work that
would keep the superseded SQLite or file-catalog architecture alive.

## Decision drivers

- Support multiple application and DuckDB compute nodes without sharing a
  writable local database file.
- Establish database-enforced authority boundaries between the application
  control plane and the DuckLake metadata catalog from the first deployment.
- Preserve one transactional authority for control state, authorization,
  idempotency, jobs, leases, active pointers, events, and immutable audit.
- Commit a durable domain event atomically with its source mutation and
  permit disconnected consumers to reconcile safely.
- Retain the compiler's canonical project graph as authority while making
  historical and active lineage queryable across nodes.
- Make cache validity derive from immutable dependency identity, policy
  identity, and canonical query identity rather than event delivery or TTL.
- Keep analytical data and large result payloads out of the transactional
  control database.
- Replace catalog-file sealing with exact DuckLake snapshot qualification while
  preserving ADR-0008 and ADR-0009's isolation, cross-store publication,
  orphan reconciliation, fencing, retention, and garbage-collection rules.
- Remove legacy projections and duplicated mutable authorities rather than
  porting them to PostgreSQL.
- Provide high availability, remote backup, point-in-time recovery, and
  operational observability for durable application state.
- Avoid adding a dedicated broker, graph database, or distributed cache before
  its semantics are required independently of PostgreSQL.

## Considered options

### Retain SQLite and the private DuckLake catalog as the target

This keeps deployment self-contained and continues to fit one writable node.
It does not provide a sound shared authority for multiple hosts, durable
cross-node work coordination, or remote DuckLake clients. Adding file locking,
replication, or copied databases around SQLite would create a product-owned
distributed protocol without giving LeapView PostgreSQL's concurrency and
recovery model.

### Use PostgreSQL for every form of state

PostgreSQL could store the control plane, event payloads, graph data, DuckLake
metadata, analytical rows, and cached Arrow bytes. A single technology is not
a single workload. Large analytical scans and disposable cache churn would
compete with control transactions, generate avoidable WAL and vacuum pressure,
inflate backups, and weaken the distinction between authoritative and
reconstructible state.

### Introduce a specialized service for each concern

LeapView could adopt PostgreSQL for control state, Kafka or NATS for events,
Redis for caching, and a property-graph database for lineage. This creates
additional authorities and delivery boundaries before the product requires
their independent scaling or query semantics. A transactional outbox would
still be required to bridge PostgreSQL mutations to an external broker.

### Use PostgreSQL as the durable coordination substrate with purpose-built
analytical and cache tiers

PostgreSQL owns transactional application state, durable domain events, work
coordination, lineage projections, and small shared cache metadata. DuckLake
owns analytical metadata and snapshot history, object storage owns immutable
analytical and cache objects, DuckDB performs analytical execution, and local
memory or disk owns disposable hot cache state. This option is selected.

## Decision outcome

Adopt the following target topology:

```text
Authored project files
        |
        v
Compiler-owned canonical artifacts and ProjectGraph
        |
        +--------------------------+
        |                          |
        v                          v
Application nodes ----------> PostgreSQL HA service
        |                      ├── leapview_control database
        |                      │   ├── capability-owned schemas
        |                      │   ├── jobs and durable events
        |                      │   ├── lineage projection
        |                      │   └── shared cache metadata
        |                      └── leapview_ducklake database
        |                          └── DuckLake-owned catalog schema
        |
        +----> DuckDB compute nodes ----> DuckLake snapshots
        |                                      |
        |                                      v
        +----> L1 memory cache            Object storage
        +----> L2 node-local cache        ├── Parquet and delete files
                                          ├── immutable artifacts
                                          └── optional shared cache objects
```

One managed, highly available PostgreSQL service is the default target, with
two databases from the first deployment:

- `leapview_control` contains application-owned schemas such as `access`,
  `delivery`, `refresh`, `event`, `audit`, `lineage`, `cache`, and `agent`; and
- `leapview_ducklake` contains only the DuckLake-owned metadata schema and
  narrowly scoped operational inspection surfaces.

The databases have separate owners, runtime roles, credentials, connection
pools, migration authorities, and timeout/resource policies. The application
runtime role has no direct write privilege on DuckLake internal tables, and
the DuckLake role has no access to control schemas. The two databases may later
move to separate PostgreSQL clusters for measured workload or failure
isolation without changing their contracts. LeapView must not depend on a
transaction spanning the control database, DuckLake database, or object
storage.

### Clean PostgreSQL baseline and capability ownership

The target starts from one authored PostgreSQL baseline. LeapView does not
translate or replay the SQLite migration chain, preserve temporary or legacy
tables, or maintain a production repository abstraction that can select either
database engine. Subsequent schema changes are forward migrations from that
baseline.

Production persistence uses PostgreSQL-native connections and generated query
code, including native time, UUID, JSON, array, byte, and transaction types.
Capability schemas own their tables and migration DDL. Cross-capability atomic
work passes one PostgreSQL transaction to the participating capability
repositories; it does not authorize ad hoc cross-schema SQL in handlers.

Schema types follow these rules:

- instants use `timestamptz` and durations use `interval`;
- opaque internal identities use `uuid`, normally time-ordered UUIDv7, while
  authored resource identities remain validated text; see PostgreSQL's
  [UUID functions](https://www.postgresql.org/docs/current/functions-uuid.html);
- monotonic counters, snapshot identifiers, byte counts, revisions, and
  fencing epochs use `bigint`;
- shared semantic values such as canonical resource IDs and SHA-256 digests use
  constrained PostgreSQL domains with column-level nullability;
- bounded, versioned documents use `jsonb`; relational identity, membership,
  routing, state, and query predicates use typed columns or child tables; and
- secrets, verifiers, and binary digests use `bytea` when their contract is
  binary rather than encoded text.

Database roles and grants enforce ownership. Row-level security is optional
defense in depth for a future shared multi-tenant control database; it is not a
second implementation of LeapView's compiler-owned governed data policies.

### PostgreSQL control authority

The PostgreSQL control database becomes the sole durable runtime authority for:

- instance, project and environment identity;
- principals, sessions, credentials, groups, grants, policies and access
  audit state;
- releases, canonical delivery plans, candidates, generations, approvals,
  active serving pointers, rollback and retention roots;
- durable refresh, deployment, maintenance and cache-warming jobs;
- idempotency records, leases, fencing tokens and compare-and-swap revisions;
- immutable audit events and transactional domain events;
- admitted immutable lineage projections and their active pointers; and
- shared cache manifests, fill leases, watermarks and retention metadata.

Typed columns and constraints own identity, routing, tenancy, state, time, and
fencing. `jsonb` may hold versioned, bounded metadata and event payloads; it is
not a substitute for relational identity or transition constraints.

The application continues to authorize governed operations before analytical
execution and before cache lookup. Database policy must not become a divergent
second implementation of LeapView's compiled data-policy semantics.

### One canonical delivery authority

The target contains one mutable delivery model. It does not port the legacy
`project_candidates`, `project_deployments`, mutable `serving_states`,
`project_active_serving_states`, or their compatibility projections alongside
the canonical `delivery_*` lifecycle.

The logical control records are:

```text
delivery_target
delivery_plan
delivery_build_attempt
delivery_snapshot_seal
delivery_candidate
delivery_generation
delivery_publication
delivery_active_pointer
delivery_approval
delivery_lease
delivery_retention_root
```

Compiled serving artifacts, authorization snapshots, resource graphs, and
dashboard publication declarations are immutable, digest-addressed inputs to a
delivery generation. They do not carry a second activation state machine. The
single `delivery_active_pointer` row is the authoritative project/environment
serving selection; generation lifecycle status is evidence and must not form a
competing active pointer.

Publication locks the exact target row, verifies its expected revision, and
advances the pointer and transactional event in one control transaction.
Compare-and-swap revisions remain explicit external API evidence, but
PostgreSQL row locks and constraints replace SQLite writer-reservation and
busy-snapshot protocols inside the transaction.

### DuckLake snapshot seals replace catalog-file seals

The PostgreSQL-backed DuckLake catalog is long-lived and owned per admitted
physical pool. Builds use candidate-qualified schemas or physical relation
names so concurrent candidates never contend for or mutate the same logical
serving namespace. A successful build commits one DuckLake transaction and
captures the exact committed snapshot.

A qualified physical seal is identified logically by:

```text
physical pool identity
+ DuckLake catalog identity
+ DuckLake snapshot ID
+ candidate-qualified relation namespace
+ exact relation-manifest digest
+ compatibility digest
+ serving-artifact digest
```

Qualification attaches or queries that exact snapshot read-only, verifies the
compiled relation closure and compatibility contract, and records immutable
evidence in `delivery_snapshot_seal`. Publication and rollback only change the
control pointer; they never copy, upload, rewrite, or promote catalog metadata.

The target therefore has no catalog-object path, catalog-file byte size,
catalog-file upload acknowledgement, downloaded catalog cache, or catalog-file
digest as generation identity. A snapshot seal may retain a canonical manifest
digest for verification and explanation, but DuckLake remains authoritative
for schema, table, file, delete-file, statistics, and snapshot membership.

Control-plane roots and active-query leases bind exact DuckLake snapshots.
Fenced maintenance expires only snapshots absent from candidates, generations,
rollback windows, active queries, and recovery holds. DuckLake schedules
unreferenced files for deletion; cleanup and orphan collection run only after
the configured reader and failure grace. LeapView records the maintenance
decision and outcome but does not duplicate DuckLake's per-file manifest as a
second authority.

### Idempotency and operation outcomes

Public mutation idempotency is part of the domain transaction rather than a
process-session lease wrapped around an unrelated transaction. A unique
`(scope, idempotency_key)` operation record binds the canonical request digest,
domain operation identity, state, and replayable outcome.

Where the result is wholly inside the control database, reservation, domain
mutation, event, and completed operation outcome commit atomically. A duplicate
request with the same digest replays the result; a different digest conflicts.
A healthy pending request remains pending or may be awaited for a bounded
period. Seeing a different application process or session never proves that an
operation was abandoned.

For DuckLake or object-store effects, the operation record binds the durable
plan/build/publication identity. Restart reconciliation determines the outcome
from exact external and control evidence before retrying, completing, or
marking it indeterminate. A lease may coordinate the reconciler, but lease
expiry alone never authorizes repeating an external effect without that
identity check.

### Durable events, audit, and process fan-out

A domain mutation and its durable event commit in one PostgreSQL transaction.
The event log itself is the transactional outbox; an additional same-database
intent-to-event materialization step is not part of the target. The durable
event contains an opaque UUIDv7 identity, project or instance scope, aggregate
identity and version, event type and schema version, occurred time, correlation
identity, and a bounded canonical payload. Consumers process events at least
once and must be idempotent.

Aggregate versions are allocated by locking and advancing the aggregate row or
another explicit version row. `MAX(sequence) + 1` is prohibited. A global
identity or commit position may support scanning, but it does not imply
business order across aggregates.

Durable broadcast delivery uses rows keyed by `(consumer_id, event_id)`.
Delivery rows for enabled consumers are created in the source transaction; a
new consumer's historical rows are backfilled before it is enabled. Each
consumer claims only its own rows atomically, records attempts and terminal
outcome, and may replay by event identity. This avoids treating a global
sequence watermark as a safe commit-order cursor across concurrent
transactions. Consumers that only need current authority may instead reconcile
that authority and use events as wake-up hints.

Compliance audit events are inserted directly and immutably through the source
mutation's transaction. The runtime role receives append-only access and
cannot update or delete audit history. Consumer delivery state is introduced
only when an audit export or another asynchronous sink exists; it is not
required to make an audit event visible inside the same database.

`LISTEN`/`NOTIFY` is only a low-latency wake-up mechanism. A notification
contains an opaque event or reconciliation key, never the authoritative event
payload or sensitive metadata. A listener establishes `LISTEN`, commits it,
then reads durable state before relying on subsequent notifications. On
disconnect or restart it reconciles the event log, consumer checkpoint or
delivery state, or current aggregate state. Missed, folded, or duplicate
notifications cannot change correctness. PostgreSQL documents both the
transaction behavior and
the setup race of this mechanism in [`NOTIFY`](https://www.postgresql.org/docs/current/sql-notify.html)
and [`LISTEN`](https://www.postgresql.org/docs/current/sql-listen.html).

Competing durable workers claim queue-like rows atomically with a locking
selection and `UPDATE ... RETURNING`; `FOR UPDATE SKIP LOCKED` avoids workers
waiting on already-claimed rows, as described by the PostgreSQL
[`SELECT` locking clause](https://www.postgresql.org/docs/current/sql-select.html#SQL-FOR-UPDATE-SHARE).
Candidate enumeration followed by a separate claim is not the production
correctness path. Durable leases and fencing tokens remain required when work
survives the claiming transaction. Broadcast consumers do not share one
competing claim. They use consumer-specific delivery state or
reconcile current authority. Strict global event order is not inferred from a
PostgreSQL sequence; ordering requirements are explicit and normally scoped to
an aggregate.

The existing bounded, in-process Pagestream broker remains the final delivery
mechanism for browser signal patches. PostgreSQL wakes or reconciles
application nodes; it does not transport every user-specific SSE patch.

An external broker may later consume the same event log through a dispatcher or
logical decoding when independent retention, throughput, stream processing,
or cross-region consumers justify it. It does not replace the transactional
event write.

### Versioned resource and lineage graph

The compiler-owned `ProjectGraph` and its canonical digest remain authoritative
for resource identity and dependencies. PostgreSQL stores an immutable,
digest-bound relational projection with records logically equivalent to:

```text
lineage_graph(graph_digest, project_id, compiler_version, created_at)
lineage_node(graph_digest, resource_id, resource_kind, identity_digest, properties)
lineage_edge(graph_digest, from_resource_id, to_resource_id, edge_kind)
lineage_closure(graph_digest, ancestor_id, descendant_id, minimum_depth) -- optional
```

Edges retain LeapView's canonical direction: a resource points to the resource
on which it depends. Both edge directions are indexed. Admission verifies that
the normalized nodes and edges reproduce the compiler artifact and digest.
Active lineage advances with the admitted serving artifact; historical graphs
remain immutable according to retention policy.

PostgreSQL recursive CTEs provide transitive traversal and cycle detection.
An immutable closure table may be calculated once per graph digest when impact
analysis or lineage browsing warrants it. Cache lookup does not recursively
traverse PostgreSQL: activation evidence supplies a query-specific dependency
digest before execution. PostgreSQL 18's recursive `SEARCH` and `CYCLE`
facilities are sufficient for the target; the architecture does not require
`ltree`, Apache AGE, or an external graph database. See the PostgreSQL
[recursive-query documentation](https://www.postgresql.org/docs/current/queries-with.html).

Future standards-based PostgreSQL property-graph support may expose the same
relational nodes and edges without changing their storage or authority; the
[PostgreSQL 19 property-graph documentation](https://www.postgresql.org/docs/19/ddl-property-graphs.html)
describes that relational-view model. The target does not depend on an
unreleased PostgreSQL version or extension.

Product resource lineage, operational publication lineage, and physical row
history remain distinct. PostgreSQL owns the first two projections. DuckLake
owns snapshots, files and its row/change history; DuckLake row identifiers or
change feeds do not substitute for compiler-derived transformation lineage.

### Dependency-addressed query-result cache

Cache correctness is established by a versioned key containing:

```text
stable production or candidate partition
+ dependency digest
+ effective policy fingerprint
+ canonical query digest
+ cache-key format version
```

The dependency digest covers the compiled semantic model, exact referenced
physical relation revisions, connection binding, result-affecting execution
identity and settings, and result format. Dashboard presentation and serving
generation identity are excluded. Missing or unverifiable evidence fails
closed to execution without reuse.

Consequently, production cache ownership is not scoped by serving-state or
generation ID. Runtime generations acquire leases on entries in the stable
production partition; candidate partitions remain isolated by candidate
identity. Closing or draining one runtime releases its local leases without
invalidating otherwise compatible entries.

Events may proactively evict or warm entries, but key rotation is the
correctness boundary. Missing an event can retain unreachable bytes until
eviction; it cannot make an obsolete result match a new request. Authorization
and revocation checks occur before lookup.

The target cache hierarchy is:

1. **L1 process memory.** Retain Arrow results with exact byte and entry
   accounting, independent lookup leases, bounded admission, cancellation-safe
   singleflight, and stable production or isolated candidate partitions.
2. **L2 node-local disk, optional.** Store immutable content-addressed result
   files. SQLite may index path, digest, size, expiry and approximate recency.
   Both files and index are disposable and must be rebuildable without an
   authoritative local mirror of PostgreSQL.
3. **L3 shared cache, optional.** Store immutable result objects in object
   storage. PostgreSQL stores only manifests, ownership, dependency identity,
   fill leases, fencing and retention metadata.

Large Arrow payloads, dashboard result rows, and per-hit recency writes do not
belong in PostgreSQL. Shared cache publication writes a content-addressed
object before committing its manifest. A failed manifest commit may leave an
orphan object, which is reclaimed after the configured grace and fencing
protocol. Retirement removes or tombstones reachability before asynchronous
physical deletion. This follows ADR-0009's existing immutable publication and
orphan-reconciliation model.

Shared fill ownership uses a persisted, expiring fence acquired with a unique
insert or locked transition. `NOTIFY` may wake waiters after manifest commit;
waiters always re-read the manifest. Shared cache manifests are ordinary logged
tables because they must survive failover. PostgreSQL unlogged tables are not a
shared cache tier: their crash truncation and lack of standby replication do
not match this contract. See PostgreSQL's
[`UNLOGGED` table contract](https://www.postgresql.org/docs/current/sql-createtable.html#SQL-CREATETABLE-UNLOGGED).

Mutable external or streaming sources contribute an explicit watermark or
custom cache-key value with a bounded lookup TTL. Failure to establish that
identity bypasses reuse. Immutable managed snapshots rotate by content identity
and do not receive an artificial TTL.

### DuckLake, DuckDB and object storage

DuckLake continues to own analytical schemas, snapshots, changesets,
statistics, schema evolution, row/change history, and file manifests. Its
metadata catalog moves to the isolated `leapview_ducklake` PostgreSQL database,
using a DuckLake-owned metadata schema suitable for multiple remote clients;
DuckLake recommends PostgreSQL for a multi-user lakehouse in
its [catalog selection guidance](https://ducklake.select/docs/stable/duckdb/usage/choosing_a_catalog_database).

Object storage owns immutable Parquet and delete files, compiled serving
artifacts, and optional shared cache objects. It does not contain a serialized
DuckLake catalog in the target architecture. DuckDB remains replaceable
analytical compute and does not become a durable control authority. DuckLake
snapshot commits, object writes, and control-plane activation remain separate
transactions with explicit candidates, snapshot seals, digests, leases,
reconciliation, and garbage collection.

One committed DuckLake transaction represents one snapshot. Serving opens the
exact qualified snapshot recorded by the active generation rather than the
latest catalog state; DuckLake documents exact snapshot selection in its
[snapshot guidance](https://ducklake.select/docs/stable/duckdb/usage/snapshots).
Concurrent build conflicts follow DuckLake's catalog
conflict and retry contract, while candidate-qualified relation namespaces
prevent independent builds from overwriting the same logical serving objects.
Snapshot expiry is catalog-wide maintenance and is therefore driven by the
complete set of control-plane retention roots, not by an age-only default.

DuckLake data inlining may optimize small analytical inserts or deletes inside
its catalog. It is DuckLake-managed physical state, not a query-result cache or
permission for application modules to write analytical rows directly into the
catalog. See [DuckLake data inlining](https://ducklake.select/docs/stable/duckdb/advanced_features/data_inlining).

### Operations, recovery, and conformance environment

PostgreSQL 18 or later is the production baseline. Production uses a managed
high-availability service or an equivalently tested installation with TLS,
automated failover, continuous WAL archiving, encrypted backups, and regular
point-in-time recovery exercises following PostgreSQL's
[continuous-archiving guidance](https://www.postgresql.org/docs/current/continuous-archiving.html).
Application startup validates server version, required extensions, schema
revision, role privileges, and read/write intent before advertising readiness.

Connection budgets are assigned per capability and workload rather than per
handler. Interactive control requests, background workers, event consumers,
cache coordination, and DuckLake catalog clients use separately observable
pools with bounded acquisition, statement, lock, and idle-transaction
timeouts. External I/O and analytical execution do not hold an open control
transaction. Long-running maintenance is resumable and uses persisted progress,
leases, and fencing.

Operational telemetry covers pool saturation, transaction age, lock waits,
deadlocks, statement latency, autovacuum health, table and index growth, WAL
generation, replication lag, event backlog, job age, and cache-fill contention.
`pg_stat_statements`, PostgreSQL activity and wait views, and structured
application operation identities provide the initial diagnostic surface. See
the [`pg_stat_statements` documentation](https://www.postgresql.org/docs/current/pgstatstatements.html).

A recovery set covers both PostgreSQL databases and versioned object storage.
It records the recovery point for each durable store and the control-plane
retention roots expected at that point. Before serving, recovery validation
must prove that every selected generation resolves its immutable artifact,
exact DuckLake snapshot seal, relation closure, and required objects. Missing
evidence blocks readiness or requires an explicit, audited recovery selection
of a retained verifiable generation; the system must not silently bind a
generation to the latest DuckLake snapshot. L1 and L2 cache state is excluded
from backup, and L3 cache objects may be discarded when their logged manifests
cannot be verified.

Repository and integration conformance run against the supported PostgreSQL
version with real concurrent connections. SQLite fixtures may test pure domain
interfaces, but passing them is not evidence for PostgreSQL locking, isolation,
roles, failover, notification, or recovery behavior.

### SQLite's target role

SQLite is removed as a production control-plane authority and as a DuckLake
catalog for shared deployments. It may remain in bounded, explicitly
non-authoritative roles:

- an optional L2 node-cache index;
- isolated tests and fixtures that exercise an interface without claiming
  production conformance; and
- packaging or tooling state whose loss cannot alter a target instance.

There is no production fallback from PostgreSQL to SQLite, no asynchronous
SQLite replica of mutable PostgreSQL authority, and no long-lived dual-write
mode. Because LeapView has no live production state, the target includes no
SQLite import, upgrade, cutover, or backward-compatibility contract.

### Prohibited shortcuts

- Do not use `LISTEN`/`NOTIFY` as the only record of a domain event.
- Do not route per-client Pagestream patches through PostgreSQL.
- Do not make event delivery or eager deletion part of cache validity.
- Do not store bulk Arrow results or analytical fact data in control tables.
- Do not reconstruct query dependencies by traversing lineage on every cache
  lookup.
- Do not make PostgreSQL's lineage projection authoritative over the compiler
  artifact from which it was admitted.
- Do not use session advisory locks as a substitute for persisted leases and
  fencing around long-running or externally visible work.
- Do not join control activation and DuckLake/object publication into an
  assumed cross-store transaction.
- Do not let the shared DuckLake schema and application control schemas share
  unrestricted ownership roles.
- Do not serialize, upload, or download PostgreSQL-backed DuckLake catalogs as
  generation artifacts.
- Do not keep the legacy deployment, active-serving-state, candidate, or
  catalog-file models as mutable PostgreSQL projections.
- Do not allocate aggregate event versions with `MAX(sequence) + 1`.
- Do not treat a different process session or an expired lease as proof that an
  idempotent external operation is safe to repeat.
- Do not use PostgreSQL unlogged tables for cache manifests or other state that
  must survive failover.

## Consequences

The target supports multiple application and compute nodes with one remotely
recoverable control authority. Durable mutation, domain events, lineage
admission, job scheduling, leases and active pointers can use PostgreSQL
transactions and constraints. Cache entries can survive a serving-generation
change or, when L2/L3 is enabled, a process restart without making disposable
bytes authoritative.

PostgreSQL becomes required production infrastructure. LeapView must own
connection-pool sizing, schema and role isolation, migrations, vacuum and WAL
monitoring, backup and point-in-time recovery, failover testing, and explicit
compatibility policy. A database outage affects all control-plane mutations,
so readiness and degraded read behavior must be deliberate.

The architecture still contains unavoidable cross-store boundaries.
PostgreSQL, DuckLake and object storage do not commit atomically. Candidate
publication, shared-cache manifests and physical garbage collection must retain
the same create-before-reference, immutable identity, orphan tolerance,
fencing, and reconciliation discipline already required by ADR-0009.

At-least-once events create duplicate-delivery and retention obligations.
Consumers must be idempotent, poison or stalled delivery must be visible, and
notification health must not be confused with durable backlog health. Logical
decoding, if later enabled, adds replication-slot retention and failover
operations.

Persisting lineage creates a derived copy that must be verified against its
canonical artifact. Closure storage may amplify graph size. These costs are
accepted because immutable graph identities make verification and retention
deterministic and because the projection supports lineage, impact analysis,
operational inspection, and cache explanation across nodes.

The cache remains deliberately heterogeneous. Memory is fastest, local files
are node-bound, object storage is shared, and PostgreSQL coordinates their
identity. This is more explicit than one cache table, but avoids imposing the
latency and write amplification of a transactional database on every cache hit
or result byte.

The clean baseline intentionally discards migration compatibility with the
pre-release SQLite implementation and file-backed DuckLake catalog. Existing
tests and documentation are updated as each target capability lands; they do
not define a supported mixed architecture. Implementation progress belongs in
Linear; this ADR records the destination and its invariants.

## Confirmation

- PostgreSQL repository conformance suites cover constraints, foreign keys,
  compare-and-swap transitions, concurrent claims, lease fencing, idempotency,
  restart, and transaction rollback for every durable capability currently
  backed by SQLite.
- Audit and domain-event tests prove mutation/event atomicity, at-least-once
  idempotency, listener startup reconciliation, disconnect recovery, duplicate
  notification tolerance, terminal delivery visibility, and payload redaction.
- Multi-node tests prove that durable workers do not execute one lease
  concurrently, stale owners cannot publish, and active serving transitions
  converge after a missed notification or node restart.
- Lineage conformance reconstructs the canonical `ProjectGraph` from persisted
  nodes and edges, verifies its digest, tests upstream and downstream closure,
  rejects cycles where the resource contract requires a DAG, and binds the
  active graph to the exact admitted artifact.
- Query-cache tests prove that dashboard-only changes reuse compatible results;
  semantic, relation, binding, policy, execution and format changes miss; a
  missed event cannot return stale data; and authorization is evaluated before
  lookup.
- Cache storage tests prove L2 loss is rebuildable, L3 publication never exposes
  an uncommitted or mismatched object, stale fill owners are fenced, orphan
  objects are reclaimed, and PostgreSQL contains no bulk Arrow payloads.
- DuckLake qualification tests exercise a PostgreSQL-backed catalog with
  concurrent remote clients while preserving exact snapshot seals, relation
  closure, retention and active-query leases.
- Backup, point-in-time recovery and failover exercises restore PostgreSQL and
  object storage to a state whose active pointers, lineage digests, catalog
  snapshot seals, immutable artifacts and retention roots all verify before
  readiness.
- Architecture checks reject production SQLite control composition, mutable
  dual-write authority, shared application/DuckLake ownership, legacy delivery
  projections, catalog-file generation artifacts, and direct application
  writes of analytical or cache payload data to control tables.
