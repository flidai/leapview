# ADR-0008: Use one immutable DuckLake catalog per candidate

Status: accepted

Decision date: 2026-08-17

Implementation: in progress (controlled rollout)

Deciders: LeapView maintainers

Supersedes: none

Superseded by: [ADR-0020](0020-adopt-a-postgresql-centered-target-data-architecture.md),
private file-backed catalog and catalog-object mechanics only; the
candidate-isolation, immutable-publication, exact-identity, fencing, lease,
retention, and reconciliation decisions remain accepted

Related: [ADR-0007](0007-adopt-plan-driven-project-delivery.md),
[ADR-0009](0009-separate-control-and-physical-transactions.md), the
[project-delivery conformance specification](specifications/project-delivery-conformance.md),
the [DuckLake snapshot model](https://ducklake.select/docs/stable/duckdb/usage/snapshots/),
[snapshot expiration](https://ducklake.select/docs/stable/duckdb/maintenance/expire_snapshots),
[file enumeration](https://ducklake.select/docs/stable/duckdb/metadata/list_files),
[data inlining](https://ducklake.select/docs/stable/duckdb/advanced_features/data_inlining),
and [catalog backup and recovery](https://ducklake.select/docs/stable/duckdb/guides/backups_and_recovery)

## Context and problem statement

ADR-0007 requires each successful build to seal an immutable private candidate
that can be previewed and qualified without changing active serving state. Two
candidates built from the same base must not see each other's unpublished
changes, while unchanged physical data should be reused without copying it.

DuckLake provides ACID transactions, catalog-wide snapshots, time travel, and
immutable Parquet data files. It does not provide branch semantics within one
catalog. A shared mutable catalog therefore cannot make its linear snapshot
history mean several independent candidate histories.

An earlier design compensated by placing uniquely named candidate schemas over
immutable versioned relations in one shared catalog, then duplicating the
candidate closure and physical ownership graph in SQLite. That can be made
correct, but it makes LeapView maintain a second physical manifest around
DuckLake and requires reconciliation at every cross-store physical commit.

DuckLake already separates its metadata catalog from its data storage. A
closed catalog can be copied while its referenced Parquet files remain in one
stable object pool. Independent catalog copies then provide candidate isolation
and naturally retain references to unchanged files. Changed writes create new
immutable objects in the shared pool.

The question is whether the DuckLake catalog itself can be the complete
immutable physical manifest for one candidate or generation.

## Decision drivers

- Two same-base candidates must remain isolated even when built concurrently.
- One candidate must resolve one complete and internally consistent data state.
- Unchanged data should be reused by reference rather than copied.
- Publication and rollback should select already sealed physical state.
- LeapView should not duplicate DuckLake's table-to-file manifest in SQLite.
- Candidate construction and retry must tolerate process failure without
  exposing partial state.
- Retention must remain correct when independent catalogs share physical files.
- The design must work with local and object-backed catalog artifacts and data
  pools.
- Storage identity, credentials, encryption, region, and cleanup policy must
  remain target-controlled.

## Considered options

### Replace shared logical tables in one catalog

Each build could continue using mutable names such as
`model.<logical-name>` and bind its candidate to the resulting catalog
snapshot. This was rejected because one linear history does not isolate
independent candidates. Later snapshots can include unrelated commits, and
same-name replacement creates conflicts rather than branches.

### Use candidate schemas over immutable versioned relations

Each candidate could receive a complete private view schema over uniquely named
physical relations in one shared catalog. This isolates candidates, and the
prototype verified that it can work. It was rejected because candidate
membership, physical identity, ownership, snapshot pins, and file retention
then require a second authoritative registry outside DuckLake. Much of the
complexity exists only to emulate catalog branches.

### Copy both catalog and data for every candidate

This provides an obvious isolation boundary but copies unchanged Parquet data,
multiplies storage and transfer cost, and defeats the desired zero-copy reuse.

### Adopt Nessie and Iceberg branching

A prototype verified isolated branches, atomic multi-table commits, exact
compare-and-swap, and rollback. It also introduced a separate catalog service,
Iceberg table metadata, version-compatibility work, and external garbage
collection. LeapView does not need that machinery after catalog artifacts
provide the required isolation over DuckLake. Nessie and Iceberg remain a
future option if LeapView requires cross-engine access or native shared-catalog
branch operations.

### Clone the catalog and share one physical pool

Each candidate builds in a private writable copy of an exact base catalog. All
compatible catalogs reference one stable object pool. Unchanged files remain
referenced by the copied metadata, while changed writes create new objects.
Before sealing, inherited and intermediate DuckLake snapshots are expired
without deleting files. The resulting immutable catalog contains exactly one
queryable state. This option is selected as a LeapView compatibility contract,
not as a topology promised by DuckLake. The current source implementation and
prototype support independent catalogs writing UUIDv7-named objects into one
pool, but every DuckDB or DuckLake extension upgrade must pass the shared-pool
conformance suite before it is admitted.

## Decision outcome

Each candidate owns one private writable DuckLake catalog during construction.
A successful build seals that catalog as an immutable artifact. Publication
creates a generation that points to the exact same artifact; it does not copy
or rewrite the catalog.

The central invariant is:

> A sealed LeapView candidate or generation DuckLake catalog contains exactly
> one retained DuckLake snapshot. DuckLake snapshot history is build-local
> implementation state and is removed before sealing. LeapView generations,
> rather than DuckLake internal snapshots, provide retained historical serving
> states.

The sealed catalog is the authoritative physical manifest of the tables,
views, data files, delete files, schemas, and compatible interpretation needed
for that state. SQLite records the artifact identity and lifecycle roots but
does not duplicate per-table file membership or physical-output ownership.

### Physical pools

LeapView defines a target-controlled `PhysicalPool` abstraction. Its durable
identity covers at least:

- storage location and namespace;
- storage implementation and compatibility contract;
- region, tenant, and isolation boundary;
- encryption and non-secret credential-reference policy; and
- garbage-collection and retention policy.

Every catalog sharing physical objects must belong to the same pool and the
same LeapView retention authority. A child candidate and its base must use the
same physical pool for zero-copy reuse. A catalog must never refer to an object
in a pool whose root set is controlled by another independent collector.

The pool compatibility contract pins the DuckDB runtime, DuckLake extension,
catalog format, storage implementation, and object-naming behavior validated by
LeapView. All pool writers use an admitted compatibility tuple. Changing any
member of that tuple requires the upgrade conformance gate before the new tuple
may read or write a shared pool.

Catalog records contain `physical_pool_id`, catalog content digest, immutable
object key, size, format and runtime compatibility, and base artifact identity.
Raw storage paths and secret values are not public project contracts.

Pool migration is an explicit copy or rebuild operation that produces a new
catalog artifact. Changing a catalog's pool after seal is forbidden.

### Legal candidate bases

A candidate build never clones an open mutable catalog file. It may begin only
from:

1. an immutable, sealed catalog artifact whose digest and physical pool are
   verified; or
2. a database-native consistent logical copy whose exact source state is
   recorded and which is closed before candidate mutation begins.

Normal candidate creation uses the first path. A byte-for-byte file copy is
allowed only when the source is already closed and immutable, the compatibility
tuple has passed the clone conformance test, and the copied digest and read-only
open are verified. This is a version-gated LeapView optimization, not an
upstream cloning guarantee. When LeapView must produce a fresh consistent copy
from a live or native database, it uses DuckLake's documented logical
`COPY FROM DATABASE` path and closes the result before mutation. Filesystem copy
semantics are never used as a snapshot mechanism for a live metadata database.

### Private construction and reuse

The working catalog is private to one build attempt. It may use several
DuckLake transactions and intermediate snapshots because none are visible as a
candidate. Changed models use normal logical names inside that private catalog
and write new immutable objects to the shared physical pool. Unchanged models
remain backed by the exact files referenced by the cloned base catalog.

Planning still computes canonical execution identities to decide which models
must be rebuilt. Those identities include every result-affecting transformation,
upstream state, input version or bound, materialization semantic, declared
nondeterministic input, connector, adapter, executable contract, policy, and
runtime compatibility. They are planning and evidence records, not a second
physical ownership registry.

Provenance-only metadata, approval state, ownership, and secret rotation do not
force rebuilding unless they change execution semantics. Undeclared
nondeterminism disables reuse. An observed source is reusable only when its
connector supplies a stable equivalence token accepted by target policy.

Data inlining is disabled for LeapView-managed materializations. LeapView sets
`ducklake_default_data_inlining_row_limit = 0` and attaches a working catalog
with `DATA_INLINING_ROW_LIMIT 0`, but attach-time configuration alone is not
sufficient: persisted global, schema, and table options take precedence.
LeapView enumerates `catalog.options()`, rejects or clears every effective
nonzero `data_inlining_row_limit`, and records the effective option state.

A legacy or migrated catalog may already contain inlined inserts or deletes.
Before normalization, LeapView explicitly flushes every affected table using a
table-scoped operation or an equivalent path that cannot be skipped by an
`auto_compact` setting. It then verifies that no live table has inlined data or
deletes. Validation does not infer safety from a documented default because the
current DuckLake documentation is inconsistent about that default. Catalog
artifacts therefore contain metadata only; managed physical data belongs in the
pool. This keeps file reachability enumerable and prevents catalog bytes from
becoming a second, exceptional data-storage path.

### Snapshot normalization

After materialization completes, LeapView explicitly enumerates the working
catalog's snapshots and expires every snapshot except the final one. Expiration
is metadata-only at this stage. LeapView does not invoke file cleanup.

The normalized catalog is rejected unless:

- exactly one snapshot remains and it is the current snapshot;
- all expected logical relations and contracts are present;
- qualification reads that exact current state;
- every effective persisted data-inlining option is zero and no live table
  contains inlined inserts or deletes;
- all `data_file` and `delete_file` references are enumerable and belong to the
  declared physical pool; and
- no callable or scheduled `CHECKPOINT` or maintenance capability, and no
  persisted maintenance default when invoked, can perform physical cleanup
  against the shared pool.

Internal time travel within a sealed catalog is intentionally unsupported.
Preview, rollback, comparison, and historical serving select distinct retained
catalog artifacts instead.

### Sealing and immutability

Once normalized and qualified, the metadata database is safely closed and its
bytes are hashed. LeapView records the digest and intended immutable object key
before upload. The catalog is uploaded using create-only conditional semantics,
preferably under a content-addressed key such as
`catalogs/sha256/<digest>.ducklake`.

An existing key is accepted only when its bytes and required metadata match the
recorded digest. Catalog artifacts are never overwritten. The ready transition
binds the candidate, physical pool, catalog digest, and qualification evidence
in SQLite.

No write-capable attachment is permitted after seal. Serving, preview,
comparison, and GC attach sealed catalogs read-only. Publication and rollback
change only SQLite generation references.

DuckLake's catalog-level `CHECKPOINT` is not used as a generic persistence step
for shared-pool catalogs. In the pinned DuckLake implementation it may run
snapshot expiration, compaction, old-file cleanup, and orphan cleanup. Sealing
uses a controlled close or metadata-database copy path that cannot invoke
physical cleanup.

### Physical deletion boundary

The pinned implementation and conformance evidence permit independent catalogs
to create and read objects in one physical pool; DuckLake does not promise this
topology as a stable public contract. The catalogs cannot independently delete
from the pool. From catalog A's perspective, a file used only by catalog B is an
orphan. Therefore all per-catalog physical cleanup is forbidden against a
shared LeapView pool, including:

- `ducklake_cleanup_old_files`;
- `ducklake_delete_orphaned_files`;
- externally scheduled maintenance and persisted maintenance defaults whenever
  they are invoked; and
- any `CHECKPOINT`, compaction, or maintenance path that can transitively
  execute cleanup.

Only the global LeapView collector defined by ADR-0009 may delete shared-pool
objects. Explicit snapshot expiration during pre-seal normalization is allowed
because it mutates only the private metadata catalog and performs no file
deletion.

### Qualification and access

Candidate preview and qualification attach the exact normalized catalog
read-only and apply the reviewer's live grants, row policies, column policies,
and resource permissions. Candidate ownership grants neither storage access nor
source credentials, production-viewer impersonation, or policy bypass.

Catalog artifacts and physical pools are inaccessible outside
target-controlled serving and maintenance capabilities. A catalog is rejected
when its runtime, extension, format, encryption, or physical-pool contract is
not compatible with the target.

### Prototype evidence

A focused prototype used LeapView's pinned DuckDB driver, two independent
catalog copies, and one MinIO-backed physical pool.

- Two same-base catalogs concurrently replaced the same logical table without
  leaking changes or colliding on object names.
- Unchanged tables reused the exact base Parquet objects; changed tables wrote
  disjoint objects.
- A sealed catalog served remotely over HTTP remained readable while its data
  remained in the shared object pool.
- Publication and rollback changed only a simulated active catalog pointer,
  while an old lease continued to identify the base artifact.
- A current-state file union across normalized catalogs found seven live files
  and only the deliberately injected eighth orphan.
- Before normalization, the current-state union missed a file required by an
  inherited snapshot. Expiring snapshots `[0, 1]` left exactly `[2]`, preserved
  the current state, and made the inherited state intentionally unavailable.
- Candidate A's native orphan-cleanup dry run classified two live files owned
  by candidate B as orphaned. This proves that catalog-local cleanup cannot
  govern a shared pool.

Source review additionally confirmed that DuckLake file enumeration returns
both data and delete files for a selected state, and that DuckLake checkpoint
maintenance can call catalog-local cleanup functions. DuckLake source at
`3d8e24a83efdbb4620a108eebb590cda4f502e82` creates data object names with
random UUIDv7 identifiers. That explains the prototype's collision-free writes,
but it remains implementation evidence rather than an upstream guarantee for
the shared-writer topology. The verification also read DuckDB source at
`11dc00c898535281d454593997e49f50d5185df6` and DuckLake documentation at
`e99d5209190bb848ae5254d148df160423f6f672`; LeapView currently pins
`duckdb-go` `v2.10504.0` (DuckDB 1.5.4).

## Prohibited shortcuts

The implementation must not:

- build two candidates in one mutable DuckLake catalog;
- clone an open or mutating metadata database by copying its file bytes;
- treat raw catalog byte copying as a portable upstream snapshot mechanism;
- seal a catalog containing more than one retained snapshot;
- use DuckLake internal snapshots as LeapView generation history;
- enable data inlining for LeapView-managed materializations;
- rely only on attach-time or default inlining configuration without verifying
  persisted scopes and existing inlined data;
- mutate or overwrite a sealed catalog artifact;
- change a sealed catalog's physical pool;
- duplicate DuckLake's file manifest as an authoritative SQLite physical-output
  graph;
- publish by rewriting tables, views, snapshots, catalogs, or data files;
- expose catalog or pool credentials outside target authorization;
- execute catalog-local physical cleanup against a shared pool; and
- upgrade any pool writer's DuckDB runtime or DuckLake extension without
  passing the compatibility conformance gate.

## Consequences

Candidate isolation becomes the ordinary isolation of independent metadata
catalogs. Unchanged data is reused without copying, normal logical relation
names remain usable, and one sealed catalog describes one complete data state.
Publication and rollback become small control-plane pointer changes.

LeapView no longer needs candidate view schemas, globally versioned relation
names, per-output ownership records, per-output reference counts, or exact
reconciliation of every build-local DuckLake transaction. The execution plan
and qualification record remain in SQLite, while DuckLake remains authoritative
for physical membership.

The cost is one metadata artifact per distinct candidate state and a
LeapView-owned global collector. Catalog copy, upload, download, open latency,
and size must be measured at realistic project scale. Shared-pool correctness
also depends on complete root enumeration and strict removal of every
catalog-local cleanup capability. It additionally depends on a pinned runtime
and extension compatibility tuple because shared independent writers are a
verified LeapView contract rather than a documented DuckLake topology.

Pre-seal crashes may leave duplicate or orphaned objects. That is an accepted
space leak, not a correctness failure, because no partial catalog can become
ready and ADR-0009 eventually collects unreachable objects after fencing and a
grace period.

Nessie and Iceberg are not selected. They may be reconsidered if metadata
artifact cloning becomes operationally dominant, independent writers must
share one live catalog, LeapView needs native branch merge semantics, or other
engines must author the same tables.

## Confirmation

Conformance requires evidence that:

- same-base candidates remain isolated under concurrent writes to the same and
  different logical tables;
- unchanged relations reuse exact object references and changed relations write
  distinct objects;
- legal cloning never observes a partially written catalog;
- byte-copy cloning, when enabled, verifies a closed immutable source, exact
  digest, and read-only open under the admitted compatibility tuple;
- effective data-inlining settings are zero at every persisted scope and legacy
  inlined inserts and deletes are flushed before seal;
- every sealed artifact contains exactly one retained snapshot and remains
  byte-immutable;
- seal retries converge through a recorded digest and create-only object key;
- remote read-only attachment resolves the complete expected state;
- publication and rollback perform no physical mutation;
- no DuckLake checkpoint, scheduled maintenance, persisted maintenance default,
  or direct cleanup can delete from a shared pool;
- every DuckDB or DuckLake extension upgrade reruns same-table and
  different-table concurrent writes, new-object disjointness and collision
  checks, abort cleanup isolation, remote sealed reads, cross-catalog orphan
  classification, and global mark completeness; and
- the global retention, lease, crash, and garbage-collection cases in ADR-0009
  and the companion specification pass.
