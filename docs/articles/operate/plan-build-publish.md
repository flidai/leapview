# Operate the plan → build → publish delivery contract

Production delivery is target-bound and immutable. The supported sequence is:

```text
plan (target revision) → build (attempt-qualified snapshot) → publish (exact candidate)
```

`dev` remains an optional private watch/preview loop. It is not a production
serving path. A publish request never rebuilds moving source or selects a
different project target in the browser.

## Verification and troubleshooting

Run `leapview validate` before creating a plan, and inspect the plan and build
digests before publication. Build runs synchronously and returns a durable
status; publish only after a sealed candidate ID is returned. A stale plan,
expired attestation, or target fence conflict is a safe no-op: create a new
plan from the intended source snapshot. Use `leapview rollback GENERATION_ID`
for an approved, retained generation when governed recovery is required.

## Bootstrap a fresh target

The first production start remains administrable even when delivery is not
ready. The readiness endpoint reports stable, non-secret diagnostic codes such
as `missing_physical_pool_admission`, `physical_pool_not_admitted`,
`target_revision_missing`, `migrated_serving_state_identity_missing`, and
`indeterminate_publication_state`.
Repair these with the native operator command; do not edit PostgreSQL control
rows or DuckLake metadata by hand.

1. Generate a complete local or MinIO qualification result for the exact
   DuckDB/DuckLake runtime, extension, catalog format, storage implementation,
   and object-naming contract. The producer emits the versioned checklist,
   named observation digests, and canonical evidence digest.
2. Review the artifact and the target-owned, non-secret pool identity. The
   target's durable `instance.id` is written to the namespace ownership marker;
   a second metadata database (including one restored from a clone) cannot
   claim the same pool or deletion lease.
3. Dry-run the admission (the default) and then apply it only after review:

   ```sh
   leapview admin delivery pool bootstrap \
     --pool pool-identity.json \
     --evidence shared-pool-evidence.json

   leapview admin delivery pool bootstrap \
     --pool pool-identity.json \
     --evidence shared-pool-evidence.json \
     --apply
   ```

The command stores opaque credential references only. It rejects an incomplete
or unknown checklist, tuple mismatch, stale digest, and a conflicting namespace
without mutating the pool. Apply authenticates the control migrator and the
separate DuckLake catalog migrator, verifies both database/role identities,
creates or verifies the namespace ownership marker, initializes the exact
hash-qualified PostgreSQL DuckLake metadata schema, and provisions distinct
runtime and maintenance grants. The immutable pool admission, catalog identity,
and runtime compatibility rows commit in one caller-owned PostgreSQL control
transaction. External marker and catalog steps are deterministic exact-replay
operations, so a retry converges after a crash without admitting changed
identity. A failed evidence check leaves no pool, catalog, or admission row.

The catalog ID and RFC 9562 UUID are derived deterministically from the physical
pool. The catalog database and global DuckLake catalog format version are read
from the authenticated catalog rather than supplied by an operator. Runtime and
maintenance credentials cannot create schemas or mutate the registered catalog
identity.

One physical namespace has one deletion authority. Separate LeapView instance
authorities must not independently bootstrap the same storage namespace. Use
one shared PostgreSQL control authority (or an external ownership/fencing
service) for a shared pool; otherwise choose a distinct namespace and isolation
boundary.

The local/MinIO conformance artifact does not grant cross-instance ownership by
itself.

## Plan, build, and publish

Plan records the exact target, project/environment, base generation and target
revision, execution inputs, provenance, policy, qualification, and rollback
class. Build leases the admitted physical pool and writes only to the immutable
relation namespace derived from its attempt identity and fencing epoch. Before
one DuckLake transaction commits, it records the attempt, request, plan, pool,
and fence in persistent snapshot commit metadata. Qualification attaches that
exact snapshot read-only, verifies its relation manifest and compiled closure,
and records one immutable snapshot seal plus the serving artifact identity.

Publish accepts only that exact plan digest, candidate ID, compatibility
evidence, snapshot seal, and base target revision. A stale revision is rejected;
it is never silently rebased. Activation advances the one PostgreSQL active
pointer with compare-and-swap revision evidence and appends the canonical event
and immutable audit evidence in the same control transaction.

No query can select a preparing candidate or an unverified seal. Production is
clean-install only: there is no SQLite migration chain, catalog-object repair
path, or file-catalog fallback. The serving runtime resolves the active
generation to its exact snapshot seal while the snapshot is attached.

## Rollback and garbage collection

Rollback selects a retained immutable generation and advances the same target
revision CAS. It does not rebuild a project, rewrite a catalog, or delete the
current generation first. Keep the prior generation's lease and root until the
replacement is verified.

Snapshot retirement locks the PostgreSQL retention record and moves it out of
`live` only after candidate, generation, rollback, recovery, and other durable
roots are absent. Existing query leases may drain, but no new root or lease can
be created after retirement begins. A fenced maintenance transaction freezes
the exact snapshot set before DuckLake expires it; replay never widens that
set. Newly discovered snapshots without control records remain quarantined for
the configured attempt/orphan grace while their persistent commit markers are
reconciled. DuckLake remains authoritative for table, file, delete-file, and
snapshot membership; PostgreSQL stores lifecycle evidence and roots, not a
duplicate physical manifest.

## Qualification lanes and support boundary

The local lane is part of the pull-request contract. The MinIO lane requires
the `ducklake_minio && duckdb_arrow` build tags and an available container
runtime. Treat a MinIO result as supported only when that lane has run for the
exact image and contract. A local artifact cannot be substituted for MinIO
evidence, and an unrun lane must remain visible as unsupported rather than
being reported as passed.
