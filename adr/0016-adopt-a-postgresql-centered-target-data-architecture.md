# ADR-0016: Adopt a PostgreSQL-centered target data architecture

Status: accepted

Decision date: 2026-08-28

Implementation: in progress (clean-slate target architecture; draft PR #386)

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
[dependency-aware dashboard query cache](https://linear.app/flid/project/dependency-aware-dashboard-query-cache-e23514b5f578/overview).
The conditional River admission decision is recorded in the [FAI-595 River
job admission specification](specifications/fai-595-river-job-admission.md).

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
- Prefer mature framework machinery for message routing and generic background
  execution where it preserves LeapView's transactional, fencing, replay, and
  retention contracts; product-owned code should express product invariants,
  not rebuild generic routers and worker runtimes.

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

#### Generated PostgreSQL query leaves

`sqlc` with the PostgreSQL engine and `sql_package: pgx/v5` is the default
authoring path for every static PostgreSQL DML or query leaf. A static leaf has
fixed identifiers, predicates, and result shape known at compile time. Query
files, schemas, and the pinned sqlc version are the sources of truth; generated
Go is a deterministic build artifact and is never hand-edited. `sqlc` is a
typed SQL compiler, not an ORM: it emits pgx methods and row types, but does
not own domain entities, repositories, transaction boundaries, or lifecycle
semantics.

Repositories and domain services remain responsible for invariants,
authorization, transaction ownership and commit/rollback, state machines,
idempotency and retry policy, domain conversion, and error mapping. Generated
methods run on the repository's caller-owned `pgx/v5` connection or
transaction; generated code must not silently begin, commit, or roll back a
domain transaction.

Raw SQL is an exception and is limited to:

- embedded schema or migration SQL, including DDL, guards, triggers, and
  grants;
- truly dynamic identifier or result-shape SQL, after identifiers and all
  other inputs are validated; and
- explicitly analyzer-incompatible statements, including PostgreSQL
  session/protocol control such as `LISTEN` or `UNLISTEN`, when their reason
  cannot be represented by a sqlc query.

Every exception is classified adjacent to the call with a
`sqlc-exception:<class>` marker or in a maintained capability-owned exception
inventory. The classification records the rationale, owner, input-binding or
identifier-safety proof, and focused verification. Convenience, awkward Go
types, or a short static statement are not exceptions; static PostgreSQL
DML/query leaves remain sqlc queries.

LeapView does not use sqlc Cloud. Generation is local, deterministic, pinned to
the repository's tool version, and requires no remote service or network. CI
generates and compiles the query output, fails when generation leaves a diff,
audits all raw-SQL exception markers and inventories, and runs database-backed
prepare/vet checks plus PostgreSQL 18 integration suites wherever the query or
locking contract requires them.

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
delivery_approval_request
delivery_approval_decision
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
approval is never candidate-wide authority. Whether approval is required and
the positive approval-policy revision are explicit governance inputs to the
canonical delivery plan and therefore participate in its digest. An immutable
approval request binds one pending publication, target, candidate, generation,
request digest, expected target revision, policy revision, requester
credential, and bounded database-clock expiry. Approval, denial, and revocation
are append-only, monotonically revised decisions with reviewer credential
evidence and enforced separation of duty. Activation accepts only the latest
effective decision for that exact publication scope; expired, revoked,
mismatched, or superseded
evidence fails closed. Approval request, decision, operation outcome, domain
event, and audit evidence commit through the same caller-owned PostgreSQL
transaction.

PostgreSQL row locks and constraints replace SQLite writer-reservation and
busy-snapshot protocols inside the transaction.

### DuckLake snapshot seals replace catalog-file seals

The PostgreSQL-backed DuckLake catalog is long-lived and owned per admitted
physical pool. Every build execution uses an immutable physical namespace
qualified by candidate identity, `delivery_build_attempt_id`, and writer
fencing epoch. Schemas and physical relation names derive from that identity so
concurrent candidates, retried executions, and successor attempts never contend
for or mutate the same serving namespace. A successful build commits one
DuckLake transaction and captures the exact committed snapshot. Before that
commit, the build sets persistent DuckLake commit metadata containing the
`delivery_build_attempt_id`, canonical request digest, plan digest, and marker
schema version, together with the physical-pool identity and writer fencing
epoch. The marker is bounded canonical JSON in `commit_extra_info`;
DuckLake exposes it with the snapshot after restart, unlike
`last_committed_snapshot()`, which is connection-local. See DuckLake's
[snapshot and commit-message guidance](https://ducklake.select/docs/stable/duckdb/usage/snapshots).

A qualified physical seal is identified logically by:

```text
physical pool identity
+ DuckLake catalog identity
+ DuckLake snapshot ID
+ candidate-attempt-and-fence-qualified relation namespace
+ exact relation-manifest digest
+ compatibility digest
+ serving-artifact digest
```

Qualification attaches or queries that exact snapshot read-only, verifies the
compiled relation closure and compatibility contract, and records immutable
evidence in `delivery_snapshot_seal`. Publication and rollback only change the
control pointer; they never copy, upload, rewrite, or promote catalog metadata.
`delivery_snapshot_seal` is historical qualification evidence, not a physical
retention root. It remains immutable after the referenced snapshot is
legitimately expired and then describes evidence that is no longer executable.
`delivery_retention_root` records current candidate, generation, rollback, and
recovery reachability; active-query leases are the drainable runtime roots.

Qualification evidence always records the exact DuckDB version, DuckLake
extension version, DuckLake specification version, and catalog-schema version.
The compatibility digest instead represents the normalized execution and
storage contract: required capabilities, semantic version constraints,
catalog/specification contract, and result-affecting settings. Exact patch
identity enters that digest only when the compatibility policy declares it
semantic.

The target therefore has no catalog-object path, catalog-file byte size,
catalog-file upload acknowledgement, downloaded catalog cache, or catalog-file
digest as generation identity. A snapshot seal may retain a canonical manifest
digest for verification and explanation, but DuckLake remains authoritative
for schema, table, file, delete-file, statistics, and snapshot membership.

Control-plane roots and active-query leases bind exact DuckLake snapshots. A
snapshot-retention record has `live`, `retiring`, `expiring`, `expired`,
`quarantined`, and `cleanup-complete` states. Every
transaction that creates or extends a candidate root, generation root, rollback
root, recovery hold, or active-query lease locks that retention record and
requires `live` in the same control transaction.

Retirement locks the same record and may begin only after all non-draining
candidate, generation, rollback, recovery, and other durable retention roots
are absent. In that transaction it verifies their absence and changes `live` to
`retiring`, atomically preventing every new root or lease. Query leases acquired
while the snapshot was `live` may remain and drain. External expiration begins
only after those query leases and the reader/failure grace have cleared. One
fenced control transaction freezes the exact maintenance set, claims every row
as `expiring`, and persists the set digest and per-snapshot children before any
DuckLake call. Replay uses only that frozen set and cannot absorb snapshots
that became eligible later. The maintenance session expires those explicit
snapshots, verifies their absence, and records `expired`. An indeterminate
expiration is reconciled against exact snapshot identity before it is retried.
Cleanup first persists the `quarantined` handoff, runs catalog-wide old-file
and orphan maintenance after the configured grace, and finally records
`cleanup-complete` for every claimed snapshot.

New DuckLake snapshots not yet bound to a control retention record remain in
reconciliation quarantine for the build/orphan grace while their persistent
attempt markers are resolved; absence of a control row does not permit immediate
expiration. DuckLake schedules unreferenced files for deletion; cleanup and
orphan collection run only after the configured file grace. LeapView records
the maintenance decision and outcome but does not duplicate DuckLake's per-file
manifest as a second authority. This is the same prevent-new-references,
remove-reachability, then delete invariant used for shared cache objects.

A physical pool is also the catalog retention and failure-isolation unit. It
contains exactly one long-lived DuckLake catalog and one dedicated object
namespace. The default scope is one project, environment, tenant-isolation
domain, region, security boundary, and retention policy; production and
candidate relations for that target may share it. Different tenants,
environments, regions, security boundaries, or retention policies do not share
a pool merely to improve reuse.

Admission defines split thresholds for retained bytes, oldest protected
snapshot age, snapshot and metadata counts, commit contention, qualification
latency, and tenant/security policy. Crossing a hard threshold provisions a
new pool for future generations rather than allowing unbounded retention
coupling. Existing generations remain bound to the old pool until their roots
retire; cross-pool reuse is never assumed.

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

For a DuckLake build, reconciliation searches persistent snapshot commit
metadata for the exact `delivery_build_attempt_id`. Exactly one snapshot with
the expected request and plan digests may complete the attempt. More than one
match or any digest mismatch quarantines the pool and attempt. A retry is
allowed only after reconciliation establishes that no matching snapshot exists
and the prior DuckDB session is positively known to have terminated or its
transaction to have aborted; connection-local `last_committed_snapshot()` is
not restart evidence. Positive termination evidence comes from the owner or
outcome of the DuckDB/catalog session, never from a control lease alone.

A control-plane lease or fencing epoch prevents a stale writer from sealing,
admitting, or publishing its result, but it does not abort an already-open
DuckLake transaction. Absence of a commit marker is therefore insufficient
while that transaction may still commit. If positive termination evidence is
unavailable, LeapView never repeats the same attempt. It marks that attempt
indeterminate and non-admissible, creates a successor attempt with a new UUID
and fencing epoch in a disjoint immutable physical namespace, and treats any
late old commit as fenced orphan state. The old marker can be reconciled and
retired, but can never satisfy the successor seal or relation closure.

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

The target selects the canonical PostgreSQL event log/delivery authority and
the mature Watermill Router plus custom Subscriber adapter, but production
consumer enrollment is conditional. A consumer may be admitted only when a
named product capability owns a bounded, idempotent effect with a reviewed
identity, authorization, retention, and recovery contract. As of this target
release no real production consumer is admitted. Do not invent a placeholder
consumer, read-model sink, or export merely to exercise Watermill. Owner
projections (including lineage, cache, audit, and product histories) remain
synchronous in their source transaction unless a later admission explicitly
proves an asynchronous effect.

Until that admission exists, the event log and producer-side event contract
are production concerns, while the Router/subscriber runtime, readiness,
backlog/lag/dead-letter metrics, restore exercises, and operator runbooks are
qualification-only. No production event runtime is started or advertised as
ready for an unadmitted consumer.

When a consumer is admitted, durable broadcast delivery uses rows keyed by
`(consumer_id, event_id)` and a consumer lifecycle of `backfilling`,
`enabled`, `paused`, and `retired`. `Backfilling`, `enabled`, and `paused`
consumers receive new delivery rows; `retired` consumers do not.
Event-producing transactions use `READ COMMITTED`.
The event repository rejects registry-fenced append, enrollment, backfill, and
retirement operations under stronger transaction isolation before performing
event work; their transaction-wide snapshots cannot satisfy this protocol.
They lock one fan-out registry row `FOR KEY SHARE` in a SQL statement that
completes before a subsequent statement reads the delivery-eligible consumers
and inserts their delivery rows. The separate statement gives the consumer scan
a snapshot taken after the fence was acquired. Fence acquisition and consumer
scan must not be combined in one statement or CTE unless an equivalent
visibility protocol is proven. PostgreSQL documents the per-command snapshot
and mixed-view behavior in its
[Read Committed contract](https://www.postgresql.org/docs/18/transaction-iso.html#XACT-READ-COMMITTED).

Enrollment and retirement lock that registry row `FOR UPDATE`, so their
boundary cannot race an in-flight event transaction while independent producers
can still proceed concurrently. Source mutation, event insertion, consumer
scan, and delivery insertion still commit in the same transaction.

The enrollment transaction creates the consumer as `backfilling` while holding
that fence, establishes an event-retention root over the admitted replay
interval, and commits before backfill begins. It then idempotently backfills the
interval in bounded transactions with
`INSERT ... ON CONFLICT DO NOTHING`, and finally changes the consumer to
`enabled`. Backfill progress advances a recorded frontier transactionally; the
event-retention floor cannot pass its root until the frontier completes or the
remaining interval receives an explicit audited waiver. The frontier is an
idempotent scan-progress cursor only; it implies neither PostgreSQL commit order
nor aggregate business order.

Retirement fences producers before stopping new fan-out; existing deliveries
must drain or receive an explicit audited waiver. Each consumer claims only its
own rows atomically, records attempts and terminal outcome, and may replay by
event identity. This avoids treating a global sequence watermark as a safe
commit-order cursor across concurrent transactions. Consumers that only need
current authority may instead reconcile that authority and use events as
wake-up hints.

Transactional fan-out is limited to a small, configured maximum number of
durable consumers. A consumer declares a replay start no earlier than the
published event-retention floor. Event and delivery rows may be pruned only
after that replay window closes and every applicable delivery is successfully
terminal, explicitly resolved, or audited as waived. Exhausted poison
deliveries enter a visible dead-letter terminal processing state, but that state
does not satisfy retention and prevents pruning until resolved or waived. A
large or dynamically growing subscriber set requires one external-broker
dispatcher consumer rather than unbounded `event × consumer` rows.

Compliance audit events are inserted directly and immutably through the source
mutation's transaction. The runtime role receives append-only access and
cannot update or delete audit history. Consumer delivery state is introduced
only when an approved asynchronous effect exists (for example, an audit
export); it is not required to make an owner projection or audit event visible
inside the same database. No such consumer is currently admitted.

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

### Framework boundary: Watermill for messages; jobs remain capability-owned

River is a preferred future generic runtime, not an immediate target or
cutover requirement. No current production job kind is eligible for River.
Until a kind passes the [FAI-595 admission specification](specifications/fai-595-river-job-admission.md),
the capability-owned PostgreSQL jobs tables and runner remain its sole
execution authority.

The target is framework-first above the PostgreSQL authority, but framework
tables and defaults do not redefine LeapView's durable contracts.

| Concern | Target owner | Framework role |
| --- | --- | --- |
| source mutation, aggregate version, canonical event envelope, and immutable product history | capability-owned PostgreSQL schema | the canonical producer appends once; owner projections stay synchronous in that transaction; an admitted consumer may receive a deterministic Watermill projection |
| delivery claim, attempt, acknowledgement, dead-letter state, replay root, and retention floor | canonical event-delivery schema | only an admitted consumer's Watermill Subscriber and Router drive the existing fenced transitions through the selected adapter |
| handler orchestration, approved retry/recovery/timeout middleware, and handler metrics | Watermill router, when admitted | LeapView supplies bounded policy and idempotent domain handlers; no production runtime/readiness/ops burden exists before admission; canonical correlation remains envelope data, not mutable Watermill metadata |
| compliance audit | append-only audit schema and audit writer | synchronous in the source transaction; outside Watermill unless a separately admitted asynchronous export effect exists |
| lineage and cache projections | lineage and cache capability schemas | synchronous owner projections in the source transaction; no Watermill topic, placeholder, or concrete consumer is implied |
| long-running commands and scheduled work | capability-owned PostgreSQL jobs tables and runner; River only for a future kind admitted by FAI-595 | never executed as a Watermill message handler |
| browser signal delivery | Pagestream and the instance-local SSE fan-out | outside Watermill; durable events only wake or reconcile application state |

[Watermill](https://watermill.io/docs/) is the selected standard
application-level Router and handler orchestration boundary for a future
admitted consumer. Domain code will consume typed LeapView events through
Watermill rather than a second product-owned router once a concrete bounded
idempotent effect is admitted. The canonical PostgreSQL `event_log`,
`event_delivery`, and persisted aggregate-filtered consumer records remain the
sole authority for claims, attempts, acknowledgement state, retries, replay,
dead letters, and retention.
The canonical event row, aggregate version, consumer enrollment fence, replay
root, delivery attempt, dead-letter state, and retention decision remain in
LeapView-owned PostgreSQL schemas.

`watermill-sql/v4` was the preferred PostgreSQL transport candidate because it
supports caller-supplied schema and offset adapters, consumer groups,
at-least-once delivery, and PostgreSQL transaction integration, including pgx
handles. Its [SQL Pub/Sub contract](https://watermill.io/pubsubs/sql/) also
documents an important boundary: the default adapter is an ordered integer
offset log with transaction-ID caveats. That default is not evidence for
LeapView's UUIDv7 event identities, aggregate-scoped order, per-consumer fenced
deliveries, backfill roots, poison resolution, or retention floor. It is not
the production transport. The target uses the canonical producer adapter for
the one transactional append and, only after admission, a small Watermill
`Subscriber` over the canonical delivery tables; it does not create a second
Watermill-owned event authority.

The [FAI-591 qualification](specifications/watermill-postgresql-proof.md)
qualifies Watermill core `v1.5.3` and `watermill-sql/v4` `v4.1.5` for package
and caller-owned transaction integration only. Its PostgreSQL 18 proof records
that the stock SQL tables and integer offsets are unsuitable as an event
authority. The [FAI-592 envelope decision](specifications/watermill-canonical-envelope.md)
admits the strict message projection and canonical producer boundary, but not
production consumer enrollment. The selected target is the Watermill core
Router with a deterministic canonical-event message projection and a custom
`message.Subscriber` over `event_log`/`event_delivery` and persisted
aggregate-filtered consumers when a concrete effect is admitted; the stock SQL
transport remains
qualification-only. The [FAI-593 runtime specification](specifications/watermill-router-runtime.md)
fixes claim-before-emit, exact worker/claim-generation fencing, fresh claims after
`Nack`, approved middleware, bounded deadlines, lease recovery, and terminal
completion-before-`Ack`. Lost-ack redelivery, dead-letter/replay retention, and
router adapter conformance are separate FAI-592/FAI-593 work and are not
claimed by this proof.

LeapView migrations own every transport table, index, role grant, and rollback.
The stock Watermill SQL publisher `AutoInitializeSchema` and subscriber
`InitializeSchema` are always disabled, including tests outside the framework
qualification fixture.
The SQL adapter's documented runtime initialization is therefore not a
production DDL path. The current topic vocabulary is exactly the allowlisted
capability families `agent`, `dashboard`, `delivery`, and `release`; they are
not conceptual control, lineage, or cache topics. No topic currently has a
production consumer. When one is admitted, its identity is a stable
migration-owned record, never a per-process UUID.

The message UUID is the canonical domain event UUID. Its versioned envelope
contains only event identity, scope, aggregate identity and version, event type
and schema version, occurrence and correlation identities, and a bounded
privacy-reviewed payload. Watermill's integer transport offset, when an
adapter needs one, is scan mechanics only. It is neither exposed as domain
identity nor used as business ordering. Aggregate version is the only default
ordering contract.

Watermill's `message.Publisher` is not used for the canonical producer write.
Its interface has no transaction parameter, while event ID, aggregate version,
occurrence time, and stored JSONB payload are finalized inside the caller-owned
PostgreSQL transaction. Binding a shared publisher to a transaction or
dispatching an in-memory message before commit would weaken the boundary. The
canonical producer instead appends the event and any delivery rows for
admitted consumers once, then the selected Subscriber reconstructs the
byte-identical Watermill message from that durable row after it is claimable.

Capability-owned `jobs.event` progress records and Agent-local conversation/run
history are product read models, not message transports. They have separate API
cursor and retention contracts, are never subscribed to by Watermill, and do
not acknowledge canonical deliveries. Their ownership and any genuinely
duplicate projections are audited by FAI-594; their existence does not create
a second asynchronous event authority.

For an admitted consumer, Watermill `Ack` occurs only after the handler's
idempotent domain effect and terminal `Complete` transition have committed.
`Nack` and process loss may redeliver, consistent with Watermill's documented
[at-least-once model](https://watermill.io/docs/pub-sub/#at-least-once-delivery);
each retry starts with a fresh durable claim, and process loss is handled by
lease recovery. The existing consumer lifecycle, replay, dead-letter, and pruning
invariants remain the acceptance contract for the adapter. If the adapter
requires dual writes, generic offsets as a second checkpoint, unbounded
translation state, or weaker fencing, it fails confirmation and the
purpose-built PostgreSQL event adapter remains behind Watermill's router
interfaces.

The transactional flows are:

1. A command locks its aggregate/version authority, writes the mutation and
   canonical event envelope, keeps owner projections in that same transaction,
   and, when a consumer is admitted, creates the delivery rows required by the
   fenced consumer registry. All rows commit through the caller-owned pgx
   transaction. The canonical producer adapter uses that exact transaction;
   no framework-owned row is committed separately.
2. When a consumer is admitted, its Subscriber claims one consumer-specific delivery with exact worker and
   claim-generation fences, commits that claim before emitting the message,
   reconstructs the Watermill message from the canonical event, and invokes
   the router without holding the claim transaction. The handler commits its
   idempotent effect and terminal `Complete` transition before returning
   success and allowing `Ack`; a failed terminal commit is nacked.
3. Handler error, process loss, or an expired acknowledgement deadline leaves
   or returns the delivery to a replayable state. Bounded attempts eventually
   enter visible dead-letter state; pruning remains blocked until resolution or an
   audited waiver.

For an admitted consumer, poll interval, resend interval, handler and
acknowledgement deadlines, batch size, positive bounded `max_in_flight`, claim
lease, recovery grace, and retry backoff are explicit bounded configuration
with production metrics. Deadlines are positive and the lease must outlive the
acknowledgement and recovery window (for example,
`handler_deadline < acknowledgement_deadline < claim_lease -
recovery_margin`); zero values cannot silently disable these protections.
Watermill handlers must keep their transaction short; long work persists a
request in the capability-owned jobs runner and acknowledges only that
admission transaction. A future River request is allowed only after one job
kind passes FAI-595; no current production handler assumes River.
The Router uses only the mature `Retry`, `Recoverer`, `Timeout`, `Prometheus`,
and `slog` middleware; metadata-mutating `CorrelationID` and `Delay`,
`PoisonQueue`, `InstantAck`, and `IgnoreErrors` are not part of the canonical
path. Publisher, subscriber, handler, ack/nack, failure, and latency signals
use Watermill's [Prometheus metrics integration](https://watermill.io/advanced/metrics/)
plus LeapView's delivery backlog, fence, dead-letter, lease-recovery, and
retention-floor metrics.

Watermill's [Forwarder](https://watermill.io/advanced/forwarder/) is deferred.
It becomes relevant only when a concrete external broker or cross-service
boundary requires forwarding the canonical PostgreSQL outbox. Introducing it
before that need would add another subscriber and operational path without
changing local correctness; it never replaces the canonical event write.

[River](https://github.com/riverqueue/river) remains the preferred future
generic runtime for a single admitted job kind whose domain completion can be
expressed through River's transactional enqueue, bounded attempts, scheduled
execution, uniqueness, queue isolation, cancellation, and retry model. It is
not the current worker target: no production kind has passed the candidate
gates, and every current kind stays on the purpose-built PostgreSQL runner.
The capability continues to own request identity, authorization, stable
request digests, immutable publication or refresh evidence, fencing, product
history, and terminal state. River may own generic execution mechanics only
after a reviewed adapter proves those contracts without a second authority.

River is not an event bus and does not replace broadcast consumer state. It
must not become the authority for refresh occurrences, refresh active pointers,
deployment approvals, delivery publications, DuckLake build attempts, or
retention roots. Those records remain capability-owned. A future candidate
must preserve transactional enqueue and completion, stable product identity
and canonical digest, workload admission and fairness, fencing/cancellation/
recovery, and must delete more custom machinery than it adds as adapters. The
complete candidate matrix and gates are in [FAI-595](specifications/fai-595-river-job-admission.md).

Framework admission is a pass/fail confirmation gate, not an invitation to
maintain two systems. There is one durable queue authority and one runtime
path per kind: the current PostgreSQL jobs authority until admission, or a
passing River adapter after an explicit cutover. Shadow writes, dual workers,
durable fallback authorities, or a translation layer larger than the runner it
replaces are rejected. A failed or unrun gate keeps the current
purpose-built PostgreSQL component.

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

`canonical_query_digest` represents result equivalence rather than query text.
It covers normalized executable query semantics, the type and value of every
bound parameter, result-shaping options, deterministic function inputs, and
all other result-affecting execution inputs. Two textually identical queries
with different typed parameter values must not share a key.

`effective_policy_fingerprint` covers the fully resolved, result-affecting
authorization context: tenant and principal identity where relevant, group and
attribute values, row predicates and their bound values, masks, grants, and
policy/compiler versions. Two authorized principals may share a result only
when this resolved context proves their results equivalent; authorization
before lookup alone is insufficient.

A query with an unrepresented volatile input is not cacheable. This includes
clock, randomness, session state, side-effecting or volatile functions, and an
external or streaming source without a verified revision/watermark. Reuse is
allowed only when the planner can either prove determinism or represent the
volatility explicitly in the dependency/query key with its declared validity
contract.

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
   admitted tenant/security/encryption domain, fill leases, fencing and
   retention metadata.

Content-addressed identity is not authorization. Every L3 manifest and object
key remains inside its admitted tenant, security, credential, region, and
encryption domain. Identical bytes do not by themselves authorize cross-domain
deduplication or object reachability; physical reuse is allowed only within the
same admitted storage-security domain.

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

LeapView pins one tested DuckDB, DuckLake extension, DuckLake specification,
and catalog-schema compatibility tuple. Ordinary runtime attachments keep
`AUTOMATIC_MIGRATION` disabled and fail closed on a version mismatch. Only a
dedicated migration process and role may change the DuckLake catalog schema;
it acquires the pool maintenance fence, drains writers and readers, verifies
backups and retained snapshot seals, performs the explicit migration, and
requalifies the pool before reopening it. The normal runtime role cannot opt
into migration. DuckLake documents the version-mismatch behavior and explicit
[`AUTOMATIC_MIGRATION`](https://ducklake.select/docs/stable/duckdb/guides/troubleshooting)
path.

Every runtime-tuple change requires pool qualification against the retained
snapshot seals that remain eligible for execution. A patch update may retain
the same compatibility digest only when this qualification proves that it
implements the same declared execution and storage contract; its exact version
still remains in immutable evidence. A semantic-contract change rotates the
compatibility digest, and an old seal is ineligible until rebuilt/resealed or
covered by an explicitly admitted compatibility bridge.

DuckLake data inlining may optimize small analytical inserts or deletes inside
its catalog. It is DuckLake-managed physical state, not a query-result cache or
permission for application modules to write analytical rows directly into the
catalog. See [DuckLake data inlining](https://ducklake.select/docs/stable/duckdb/advanced_features/data_inlining).

### Operations, recovery, and conformance environment

PostgreSQL 18.x is the initial production baseline. A later major becomes
supported only after repository, locking, notification, failover, backup, and
recovery conformance passes against that major. Production uses a managed
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

A recovery set records one recovery point per PostgreSQL cluster plus the
corresponding versioned object-storage recovery frontier. While both databases
share the default HA cluster, physical PITR restores them at one cluster-wide
recovery point; if they later move to separate clusters, the recovery set has
two PostgreSQL recovery points. It also records the control-plane retention
roots expected at those points. Before serving, recovery validation
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

Every bounded `jsonb` document has a versioned maximum serialized byte size for
its record type, enforced at the shared persistence boundary and by a database
constraint. This applies at least to domain-event payloads, lineage properties,
idempotency outcomes, cache metadata, and delivery evidence. Content exceeding
the limit is rejected or placed as an immutable object with a typed digest
reference; PostgreSQL TOAST is not the bounding mechanism.

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
- Do not treat Watermill's default integer offset or consumer group as proof of
  LeapView's aggregate ordering, consumer enrollment, replay, poison, fencing,
  or event-retention contracts.
- Do not dual-write events or jobs into both framework-owned and LeapView-owned
  mutable authorities during steady state.
- Do not use River as an event bus or move refresh occurrence, active-pointer,
  publication, approval, or retention-root authority into generic job rows.
- Do not enroll or retire a durable event consumer without the transactional
  fan-out registry fence.
- Do not combine fan-out fence acquisition and consumer scanning in one
  `READ COMMITTED` statement without proving equivalent post-fence visibility,
  or advance event retention past a backfill root.
- Do not treat a backfill frontier as commit or business ordering.
- Do not route per-client Pagestream patches through PostgreSQL.
- Do not make event delivery or eager deletion part of cache validity.
- Do not treat a cache content digest as authority for cross-security-domain
  object deduplication or reachability.
- Do not reuse a query result when typed bound values, resolved policy inputs,
  or volatility are absent from its equivalence evidence.
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
- Do not retry an indeterminate DuckLake build without reconciling its
  persistent commit marker and positively terminating the prior transaction;
  use a disjoint successor attempt when termination cannot be proven.
- Do not let two build executions share a candidate-only physical namespace;
  attempt identity and writer fencing epoch are part of that namespace.
- Do not treat immutable `delivery_snapshot_seal` evidence as a physical
  retention root.
- Do not enter `retiring` while a non-draining candidate, generation, rollback,
  recovery, or other durable hold exists.
- Do not create or extend any retention root after its snapshot enters
  `retiring`.
- Do not share a physical pool across tenant, environment, region, security, or
  retention boundaries, or allow a pool to grow past its admission thresholds.
- Do not enable DuckLake automatic catalog migration on ordinary runtime
  attachments.
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

When a consumer is admitted, at-least-once delivery creates duplicate-delivery
and retention obligations. That consumer must be idempotent, poison or stalled
delivery must be visible, and notification health must not be confused with
durable backlog health. Logical decoding, if later enabled, adds
replication-slot retention and failover operations. Transactional delivery
fan-out also adds one row per durable consumer and therefore intentionally
supports only a bounded consumer set. These consumer-specific operations are
not part of the current PostgreSQL target release.

Catalog isolation limits snapshot-retention coupling and security blast radius
at the cost of more catalogs and possible duplication when a future generation
moves to a new pool. Explicit DuckLake upgrade fencing adds operational work,
but prevents a routine application attachment from irreversibly migrating the
catalog beneath retained generations.

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
- Audit and domain-event tests prove source mutation/event atomicity, owner
  projection synchronicity, canonical envelope encoding, and absence of a
  Watermill SQL authority. Adapter/conformance tests also cover the selected
  subscriber's claim and completion protocol in qualification fixtures. Once a
  concrete consumer is admitted, its acceptance suite must additionally prove
  at-least-once idempotency, listener startup reconciliation, disconnect
  recovery, duplicate notification tolerance, terminal delivery visibility,
  replay retention, poison/dead-letter handling, and concurrent enrollment
  backfill fences; those tests are not a blocker for the current target.
- Multi-node tests prove that durable workers do not execute one lease
  concurrently, stale owners cannot publish, and active serving transitions
  converge after a missed notification or node restart. Event-consumer
  multi-node/lag, backup-restore, and operator-runbook drills are required only
  after a consumer admission.
- Framework conformance currently runs the adapter and canonical envelope
  invariants without admitting a production consumer. A future consumer must
  additionally pass the event runtime gates. A River candidate is tested separately, one job kind at a time,
  against the [FAI-595 gates](specifications/fai-595-river-job-admission.md):
  caller-owned enqueue/completion rollback, stable identity and digest,
  admission/fairness, cancellation and stale-worker fencing, retry/recovery,
  restart, and multi-node takeover. No candidate is authoritative until every
  gate passes, and tests prove that no second jobs authority is written after
  cutover.
- Lineage conformance reconstructs the canonical `ProjectGraph` from persisted
  nodes and edges, verifies its digest, tests upstream and downstream closure,
  rejects cycles where the resource contract requires a DAG, and binds the
  active graph to the exact admitted artifact.
- Query-cache tests prove that dashboard-only changes reuse compatible results;
  semantic, relation, binding, policy, execution and format changes miss; a
  missed event cannot return stale data; and authorization is evaluated before
  lookup. They also cover identical query text with distinct typed parameter
  values, two authorized principals with distinct row filters or masks, and
  volatile-query bypass unless every volatile input is represented.
- Cache storage tests prove L2 loss is rebuildable, L3 publication never exposes
  an uncommitted or mismatched object, stale fill owners are fenced, orphan
  objects are reclaimed, identical bytes cannot cross admitted storage-security
  domains, and PostgreSQL contains no bulk Arrow payloads.
- DuckLake qualification tests exercise a PostgreSQL-backed catalog with
  concurrent remote clients while preserving exact snapshot seals, relation
  closure, retention and active-query leases. Lost-commit-acknowledgement tests
  reconcile exactly zero or one persistent build marker and quarantine
  duplicates or digest mismatches. They prove that control fencing alone cannot
  authorize same-attempt retry, that positive termination permits it, and that
  an unprovable transaction produces a disjoint successor whose late predecessor
  commit is non-admissible. Namespace tests prove every execution includes its
  candidate, attempt, and fencing identity. Retirement races prove the
  transition fails while any non-draining candidate, generation, rollback,
  recovery, or other durable root exists; after transition no new root or query
  lease is granted, while only existing query leases drain before expiration.
- Retention tests prove an immutable snapshot seal survives physical expiration
  as historical evidence without keeping that snapshot reachable.
- Pool conformance proves the catalog isolation unit and split thresholds, pins
  and records the exact DuckDB/DuckLake runtime tuple as evidence, verifies the
  declared compatibility digest independently, rejects runtime automatic
  migration, and requalifies retained seals after any runtime-tuple or explicit
  catalog migration.
- Backup, point-in-time recovery and failover exercises restore PostgreSQL and
  object storage to a state whose active pointers, lineage digests, catalog
  snapshot seals, immutable artifacts and retention roots all verify before
  readiness. PostgreSQL conformance runs against every admitted major rather
  than assuming forward compatibility from 18.x.
- Architecture checks reject production SQLite control composition, mutable
  dual-write authority, shared application/DuckLake ownership, legacy delivery
  projections, catalog-file generation artifacts, and direct application
  writes of analytical or cache payload data to control tables. Persistence
  tests reject oversized bounded JSON documents and verify typed object
  references for intentionally externalized content.
