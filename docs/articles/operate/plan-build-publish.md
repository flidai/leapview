# Operate the plan → build → publish delivery contract

Production delivery is target-bound and immutable. The supported sequence is:

```text
plan (target revision) → build (private candidate) → publish (exact candidate)
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
Repair these with the offline operator command; do not edit SQLite rows by hand.

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
without mutating the pool. Create-and-admit is one SQLite transaction and is
safe to retry after a crash. A failed evidence check leaves no half-created
pool or admission row.

One physical namespace has one deletion authority. Separate LeapView instance
databases must not independently bootstrap the same storage namespace. Use one
shared control database (or an external ownership/fencing service) for a
shared pool; otherwise choose a distinct namespace and isolation boundary.
The local/MinIO conformance artifact does not grant cross-instance ownership by
itself.

## Plan, build, and publish

Plan records the exact target, project/environment, base generation and target
revision, execution inputs, provenance, policy, qualification, and rollback
class. Build leases the admitted physical pool and produces a private
candidate. Physical work uses a copy-on-write catalog and ends by sealing one
immutable catalog object, serving artifact, and serving-state identity.

Publish accepts only that exact plan digest, candidate ID, compatibility
evidence, and base target revision. A stale revision is rejected; it is never
silently rebased. Activation performs a compare-and-swap pointer update. The
append-only delivery event ledger records plan, qualification/approval,
build/seal, publication, activation, retirement, rollback, lease, and GC
outcomes with actor and digest evidence.

### Historical `reuse_snapshot` provenance

Provenance written by older releases may contain the historical
`dataMode: reuse_snapshot` literal. Those immutable records remain readable
for audit and evidence review, but they are not accepted for a new plan,
candidate build, or publication. The controlled-rebuild diagnostic identifies
the record; rebuild and requalify the candidate under the canonical
`reuse_base` mode before publishing. Never edit the persisted provenance JSON
or recompute its bound digests in place: retain the historical row and create
new, canonical evidence through the normal plan → build → publish flow.

No query can select a preparing candidate or an unverified seal. Rows migrated
from an older schema that lack serving-state identity remain inspectable but
cannot become ready or active until repaired. Production composition exposes
only the sealed serving factory; the legacy process catalog is not a fallback.

## Rollback and garbage collection

Rollback selects a retained immutable generation and advances the same target
revision CAS. It does not rebuild a project, rewrite a catalog, or delete the
current generation first. Keep the prior generation's lease and root until the
replacement is verified.

GC marks roots, leases, generations, candidates, and in-flight publications
before deleting anything. A pool fence and epoch prevent an expired writer or
GC worker from acting after restart. The sealed DuckLake catalog is authoritative
for physical membership; SQLite stores control evidence and roots, never file
membership or reference counts. Native DuckLake cleanup/checkpoint operations
are rejected on shared pools.

## Qualification lanes and support boundary

The local lane is part of the pull-request contract. The MinIO lane requires
the `ducklake_minio && duckdb_arrow` build tags and an available container
runtime. Treat a MinIO result as supported only when that lane has run for the
exact image and contract. A local artifact cannot be substituted for MinIO
evidence, and an unrun lane must remain visible as unsupported rather than
being reported as passed.
