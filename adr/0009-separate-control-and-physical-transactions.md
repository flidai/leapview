# ADR-0009: Separate control state from immutable physical catalogs

Status: accepted

Decision date: 2026-08-17

Implementation: pending

Deciders: LeapView maintainers

Supersedes: none

Related: [ADR-0007](0007-adopt-plan-driven-project-delivery.md),
[ADR-0008](0008-isolate-ducklake-candidate-physical-state.md), the
[project-delivery conformance specification](specifications/project-delivery-conformance.md),
the [DuckLake transaction model](https://ducklake.select/docs/stable/duckdb/advanced_features/transactions),
[snapshot expiration](https://ducklake.select/docs/stable/duckdb/maintenance/expire_snapshots),
[file enumeration](https://ducklake.select/docs/stable/duckdb/metadata/list_files),
and [file cleanup](https://ducklake.select/docs/stable/duckdb/maintenance/cleanup_of_files)

## Context and problem statement

ADR-0007 defines immutable plans, candidates, and generations. ADR-0008 makes
one normalized, immutable DuckLake catalog artifact the complete physical state
of a candidate and its published generation. Compatible catalog artifacts share
immutable data and delete files in one physical pool.

This design spans several durability boundaries:

- SQLite owns plans, build attempts, approvals, candidate lifecycle, active
  generation, rollback retention, query leases, and garbage-collection fences.
- A private DuckLake metadata database owns build-local analytical transactions
  until the candidate is sealed.
- Object storage owns immutable catalog artifacts and physical pool objects.

These systems cannot participate in one transaction. A build can write Parquet
objects and crash before sealing its catalog. A catalog upload can succeed while
its acknowledgement or following SQLite transition is lost. Publication can
commit in SQLite while the caller loses the response. Garbage collection can
race with a new query lease, candidate seal, rollback promise, or publication.

The earlier design reconciled every DuckLake transaction through commit
metadata and mirrored every physical output and reference in SQLite. Private
working catalogs make that unnecessary. Build-local transactions are not
externally visible, and an unfinished working catalog can be abandoned. The
first exactly identified physical state that matters is the immutable catalog
artifact at the seal boundary.

The question is where LeapView must provide exact recovery and how it can
delete unreferenced shared-pool objects without either a distributed
transaction or a second physical manifest.

## Decision drivers

- A partial or unqualified build must never become a candidate.
- Pre-seal work may be repeated or abandoned without reconstructing every
  DuckLake transaction.
- A seal with an ambiguous upload result must be exactly reconcilable.
- Retrying a completed seal, publication, rollback, or retirement must converge
  on the same result.
- SQLite must constrain lifecycle identity and serialize conflicting control
  transitions.
- DuckLake catalogs must remain the only authority for table-to-file membership.
- Query leases and retained generations must protect both catalog artifacts and
  every physical object they reference.
- No independent catalog may delete from a shared physical pool.
- Crashed builds and uploads must eventually become safely collectible.
- A future multi-process topology must not depend on process-local mutexes for
  correctness.

## Considered options

### Reconcile every build-local DuckLake transaction

LeapView could persist an operation in SQLite, place its identity in DuckLake
commit metadata, and recover every lost transaction acknowledgement by finding
the exact resulting snapshot. This was rejected for private candidate builds.
It provides exactly-once semantics before anything is publishable, preserves
intermediate states that are intentionally removed at seal, and recreates a
cross-store state machine for disposable work.

Exact per-transaction recovery may still be appropriate for a future mutable
physical operation whose partial result is externally visible. It is not the
default candidate-build protocol.

### Store lifecycle and ownership in DuckLake tables

This could place control records near analytical state but would not remove the
SQLite boundary. Plans, approvals, active serving state, and high-churn query
leases still require constrained control transactions. It would also turn each
lease or lifecycle update into analytical catalog history and create two
authorities. This option was rejected.

### Treat every orphan object as a failed transaction

LeapView could attempt to prevent any physical object from outliving the exact
transaction that created it. Object storage and catalog sealing do not provide
that atomicity. This would require distributed commit without improving the
user-visible guarantee. The option was rejected.

### Make the immutable catalog seal the exact physical boundary

Before seal, physical work is at-least-once and an abandoned attempt may leave
unreferenced objects. At seal, LeapView records an exact digest and immutable
object key, performs a create-only upload, verifies the artifact, and binds it
to the candidate in SQLite. After seal, the artifact is immutable and every
lifecycle transition is exact and idempotent. A fenced global collector removes
old unreachable objects. This option is selected.

## Decision outcome

SQLite is authoritative for why a physical state exists, whether it is ready,
and whether it remains a garbage-collection root. A sealed DuckLake catalog is
authoritative for which tables and files constitute that physical state. Object
storage is authoritative for the immutable bytes at the recorded keys.

LeapView provides at-least-once construction before seal and exact identity from
seal onward. Orphan files are acceptable. Partially exposed candidates are not.

### SQLite authority

The control schema contains records logically equivalent to:

- physical pools and their storage, compatibility, credential-reference, and
  GC policy, including the admitted DuckDB runtime and DuckLake extension
  compatibility tuple;
- build attempts with canonical plan and execution inputs, base catalog,
  physical pool, owner lease, state, and terminal result;
- candidate catalog seals with content digest, immutable key, size, runtime and
  format compatibility, final file-closure evidence, and qualification digest;
- candidates, prepared generations, active generations, pending publications,
  rollback promises, and retention exceptions that root catalog artifacts;
- query leases that root one exact candidate or generation and catalog digest;
  and
- GC cycles, pool writer leases or epochs, mark evidence, delete intents, and
  terminal results.

Exact schema names may follow package ownership. IDs and digests are unique,
foreign keys preserve ownership, states use checked transitions, and conflicting
changes use compare-and-swap or an equivalent serialized transaction.

SQLite does not contain an authoritative copy of DuckLake table membership,
data-file membership, delete-file membership, or per-output reference counts.
It may retain hashes, counts, and sampled evidence for verification and audit,
but the read-only catalog artifact remains the source used to construct the GC
mark set.

### Build and seal protocol

A build attempt follows these phases:

1. **Building.** A SQLite transaction creates the attempt, binds its canonical
   plan and execution inputs, exact immutable base artifact, physical pool, and
   writer lease. The worker creates a private working catalog and performs
   materialization. Before managed writes, it verifies effective data-inlining
   options across global, schema, and table scopes. Legacy inlined inserts and
   deletes are flushed explicitly table by table. Several DuckLake transactions
   and retries are permitted.
2. **Normalizing.** The worker explicitly expires every inherited and
   intermediate snapshot except the final snapshot, without invoking physical
   cleanup. The attempt fails if exactly one current snapshot does not remain.
3. **Validating.** Contracts, tests, audits, data diffs, runtime compatibility,
   zero effective inlining settings, absence of live inlined data, and complete
   `data_file` and `delete_file` enumeration are evaluated against that
   normalized state. Blocking failure produces no ready candidate.
4. **Preparing the seal.** The metadata database is detached and safely closed
   through a path that cannot trigger DuckLake catalog-level cleanup. LeapView
   hashes the final bytes. A SQLite transaction changes the attempt to
   `sealing` and records the digest, size, content-addressed create-only object
   key, physical pool, and verification evidence before upload begins.
5. **Sealing.** LeapView conditionally creates the catalog object. Success or an
   ambiguous response is reconciled by reading that exact key and verifying its
   bytes, digest, size, and required object metadata. A mismatching existing
   object is corruption, never an overwrite target.
6. **Ready.** After remote read-only verification of the artifact and its file
   closure, one SQLite transaction creates or completes the immutable candidate,
   binds its qualification evidence, and releases the build writer lease.

A canonical successful build seals at most one candidate. Candidate identity
is established by the successful seal, not by intermediate DuckLake snapshot
numbers or object writes.

The seal transition is the physical exactly-once boundary. Reusing a sealing
identity with a different digest, pool, key, or canonical inputs is a conflict.
Retrying the same recorded seal returns or completes the same result.

### Pre-seal failure and retry

A crash before the durable `sealing` record leaves no candidate. The working
catalog can be discarded and the canonical work retried from the exact sealed
base. LeapView does not have to discover or finish the last build-local DuckLake
transaction. Duplicate computation and unreferenced physical objects are
accepted outcomes.

A crash after the `sealing` record resumes reconciliation of that exact digest
and key. It never uploads different bytes under the same key and never silently
switches the attempt to a newly built artifact. If the recorded local artifact
is lost before upload and cannot be reproduced byte-for-byte, the attempt fails
without a candidate; a new attempt starts from the base with a new seal record.

Expired build leases mark attempts abandoned but do not immediately delete
their possible objects. Global GC collects objects that remain unreachable
after the configured grace period and fencing rules.

### Publication and rollback

Publication is entirely a SQLite control-plane transaction because the
candidate's physical state is already immutable. It verifies the exact ready
candidate, plan, approval, qualification, artifact digest, physical pool, base
generation, and target revision; records the publication result; atomically
compare-and-swaps the active generation and target revision; and establishes
the new generation root.

Publication performs no DuckLake or object-store mutation. A lost response is
reconciled from the publication record and active pointer. Retrying the same
canonical publication returns the committed result or proves that it did not
commit before repeating the same SQLite transition.

Rollback selects another retained qualified generation through the same
control-plane fencing. It does not alter, recreate, or normalize a catalog.

### Query leases and catalog retirement

A query first resolves one candidate or generation to an exact catalog digest
and acquires a lease in SQLite before attaching the catalog. Lease acquisition
and catalog retirement serialize through the same durable fence:

- if the lease commits first, the catalog remains a GC root until release or
  expiry;
- if retirement wins first, new leases fail and callers must resolve another
  retained generation.

Heartbeats and releases remain SQLite operations and create no DuckLake
snapshots. A process-local mutex may optimize a single-process deployment, but
the durable transaction is the correctness boundary.

A lease protects the catalog artifact and all files reachable from it. There
are no independent per-table or per-output query leases.

### Global mark-and-sweep garbage collection

Only LeapView's physical-pool collector may delete shared-pool objects. Each GC
cycle is scoped to exactly one `PhysicalPool` and follows this protocol:

1. In a SQLite transaction, acquire the pool's GC lease or epoch and capture a
   stable root set. Roots include sealing or indeterminate seal records, ready
   candidates, prepared and active generations, pending or indeterminate
   publications, rollback windows, retention exceptions, active query leases,
   and any other protective state. Unsealed working catalogs are protected by
   writer leases rather than treated as immutable roots.
2. Serialize the destructive phase with physical-pool writers. A simple
   implementation waits for all writer leases to finish. An epoch-based
   implementation may proceed only when it can prove that in-flight and newer
   objects are excluded from deletion.
3. Verify every rooted catalog artifact by digest, attach it read-only, enumerate
   every visible base table, and union every non-null `data_file` and
   `delete_file` returned by `ducklake_list_files`. Under the admitted
   compatibility tuple, sealed catalogs have one retained snapshot and no live
   inlined data, so current-state enumeration is the complete `.parquet` and
   `.puffin` live set.
4. Mark the rooted catalog artifact objects themselves and any other declared
   pool metadata. Record a digest of the root and mark sets as cycle evidence;
   do not create an authoritative per-file ownership registry.
5. List pool objects and select only objects absent from the mark set, absent
   from protected in-flight namespaces, and older than the configured orphan
   and reader grace periods.
6. Before deletion, revalidate the GC epoch, root revision, query-lease fence,
   writer fence, and candidate/publication state. Any relevant change aborts or
   restarts the cycle.
7. Persist the exact bounded delete intent, delete idempotently, and reconcile an
   ambiguous response by checking the intended object keys and storage
   postcondition.

Catalog artifacts and physical data may use separate object namespaces, but
they remain governed by the same root transition. Deleting a catalog root and
deleting its now-unreachable files need not happen in one storage request; the
grace period makes intermediate leaks safe.

An implementation may cache a catalog's verified file-set digest to reduce
repeated enumeration. The cache is evidence only and must be invalidated by a
catalog digest change. GC can always rebuild truth from the immutable catalog.

### Native DuckLake maintenance prohibition

DuckLake cleanup reasons about one catalog. A file referenced only by another
catalog in the same pool is therefore indistinguishable from an orphan.
Consequently the following are forbidden for every catalog attached to a
shared LeapView pool:

- `ducklake_cleanup_old_files`;
- `ducklake_delete_orphaned_files`;
- externally scheduled cleanup and persisted maintenance defaults whenever
  maintenance is invoked;
- ungoverned compaction or maintenance that deletes physical objects; and
- DuckLake catalog-level `CHECKPOINT`, because the pinned implementation can
  invoke expiration, compaction, old-file cleanup, and orphan cleanup.

This prohibition does not rely on an autonomous DuckLake background cleaner;
source review found none. The reachable hazards are explicit cleanup,
`CHECKPOINT`, externally scheduled maintenance, and persisted defaults applied
when a maintenance path is invoked.

Pre-seal snapshot expiration is the narrow exception: LeapView supplies the
exact versions, verifies that the latest snapshot remains, and invokes no file
cleanup. Any compaction that creates replacement files must occur while the
catalog is private and must leave old-file deletion to global GC.

Normal authors, serving runtimes, and preview sessions receive no capability
that can bypass these rules. Administrative bypass is unsupported repair
activity and requires a full global reachability audit afterward.

### Refresh and restatement

A refresh or restatement that changes physical data produces a new private
candidate catalog and follows the same build, normalization, qualification, and
seal protocol. It records run identity, requested and effective intervals or
watermarks, resolved inputs, strategy, and qualification evidence in SQLite.

A sealed generation catalog is never refreshed in place. Scheduling and worker
leases remain control state; output membership remains in the new catalog.

## Prohibited shortcuts

The implementation must not:

- expose a working catalog as a candidate before its exact seal completes;
- require exact recovery of disposable build-local DuckLake snapshots merely
  to avoid acceptable orphan files;
- classify a lost catalog upload acknowledgement as immediate failure;
- overwrite or reuse an immutable key for different catalog bytes;
- mark a candidate ready before remote artifact and file-closure verification;
- maintain authoritative physical file membership in both SQLite and DuckLake;
- publish or roll back by mutating DuckLake or object storage;
- acquire a query lease after catalog retirement wins the fence;
- begin destructive GC from a stale or incomplete root set;
- delete an unmarked object without writer exclusion, age protection, and
  pre-delete root revalidation;
- let a process-local mutex be the only multi-process fence; or
- invoke catalog-local cleanup against a shared physical pool.

## Consequences

The design moves exactly-once recovery to the boundary users actually observe:
the immutable candidate seal. Build-local commits can be retried naturally,
and crashes leak storage rather than partially exposing a generation. A single
content digest joins SQLite intent, immutable catalog bytes, and remote
verification.

SQLite remains a correctness dependency, but its graph becomes smaller. It
owns catalog roots, lifecycle state, leases, and GC fences rather than every
physical table and reuse edge. DuckLake remains the sole physical manifest, and
object storage remains an immutable byte store.

The cost is potentially repeated computation after pre-seal failure, temporary
orphan storage, content-addressed catalog upload, global object enumeration,
and a custom mark-and-sweep collector. Long builds may delay destructive GC in
the simple writer-exclusion implementation. More concurrent collection requires
a carefully proven epoch protocol rather than weaker locking. Shared-pool
writing is also version-gated: a new DuckDB runtime or DuckLake extension cannot
join a pool until the compatibility conformance suite passes.

Backing up SQLite remains important because it records why catalog roots must
be retained. Repair after control-state loss must conservatively discover and
verify immutable catalog artifacts before any pool deletion.

## Confirmation

Conformance requires evidence that:

- crashes before seal never produce a ready candidate and may be retried from
  the exact base without build-local snapshot reconciliation;
- crashes and lost acknowledgements at every post-digest seal boundary converge
  on the recorded immutable key or fail without substitution;
- a mismatching existing catalog object is rejected and never overwritten;
- ready candidates verify one snapshot, catalog digest, physical pool, complete
  file closure, and qualification evidence;
- publication and rollback are exact SQLite-only transitions;
- a long-running query remains readable through concurrent publication and GC
  while its generation lease is live;
- lease-versus-retirement and root-versus-GC races have one durable winner;
- GC marks both data and delete files from every rooted catalog and deletes only
  old objects outside the globally revalidated union;
- crashed builds and uploads become collectible without risking live files;
- no direct, scheduled, checkpoint-triggered, persisted-default, or
  catalog-local cleanup path is reachable for a shared pool;
- every DuckDB or DuckLake extension upgrade reruns concurrent same-table and
  different-table writes, object-name collision and disjointness checks, abort
  isolation, remote sealed reads, cross-catalog orphan classification, and
  global mark completeness before admission to a physical pool; and
- the full failure, concurrency, and end-to-end matrix in the companion
  specification passes for local and object-backed deployments.
