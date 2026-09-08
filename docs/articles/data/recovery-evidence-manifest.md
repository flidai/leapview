# Recovery evidence manifests

FAI-520 defines a small, evidence-only contract for qualifying historical
managed-object recovery. An observation manifest records the exact managed
revision files, provider object versions, bytes, and one PostgreSQL boundary.
It is not a restore command, an activation request, or an authenticity proof.

This validates recovery evidence binding. It does not prove successful physical disaster recovery.

## Trusted capture boundary

The source-side port is
`manageddata.ProjectionCaptureSource`:

```go
CaptureManagedProjection(context.Context) (manageddata.CapturedProjection, error)
```

`internal/manageddata/postgres.Repository.CaptureManagedProjection` is the
trusted implementation. `internal/recoveryset/postgres.Capture` adapts its
result to an opaque `CapturedObservation`; callers cannot provide or replace
the marker, PostgreSQL identities, or managed inventory.

The capture source is a privileged composition dependency, not request input.
An arbitrary implementation of that port is not a trusted authority. Parsed
attestation bytes alone establish neither provenance nor backup availability.

Capture uses one repeatable-read PostgreSQL transaction. It locks the managed
observation tables, reads the reachability epoch before and after a complete,
ordered walk of ready revisions and their files, and rejects recovery or an
epoch change. It creates one `pg_create_restore_point` marker, verifies WAL
flush through that exact marker on the same transaction connection, and only
then commits and releases the locks. The bounded capture fails if marker flush
cannot be confirmed; inserting a restore-point record alone is not durability.
Trusted capture also requires PostgreSQL `fsync=on` before creating the marker
and while confirming flush. Qualification containers explicitly enable it;
the usual test-container performance default is not durability evidence.
The resulting boundary contains database identity, numeric system identity,
timeline, canonical WAL LSN, restore-point name, and the inventory digest.
There is no provider I/O in this transaction. The frozen projection is the
managed-data authority plus its WAL marker; provider version observations are
captured separately and must not be inferred from provider listings.
Capture is bounded to 30 seconds and can block managed-data writers while
waiting for the marker to flush. This qualification does not provide a
non-blocking production capture scheduler or prove backup/WAL-archive retention.

## Manifest contract

`internal/recoveryset/observation` owns these JSON fields:

- `Boundary`: protocol version, database/system identities, timeline, LSN,
  restore-point name, and inventory digest.
- `Inventory`: schema version and ready `Revision` records. Each revision has
  its operational ID, canonical managed `ManifestDigest`, and `File` records.
- `File`: logical path, raw lowercase SHA-256, exact `s3://bucket/key` storage
  key, and byte size.
- `Objects`: one `Observation` for each inventory file. Each `ProviderObject`
  records endpoint, region, bucket, key, non-null version ID, raw SHA-256, and
  size.

`Manifest.Validate` validates each existing managed-data manifest and digest,
rejects duplicate revision/file/observation identities, and requires a full
bijection between inventory files and observations. Provider bucket/key,
hash, and size must match the declared storage key and file. Provider
endpoints must be HTTPS or loopback HTTP and contain no credentials.

`CanonicalJSON` sorts revisions, files, and observations only after duplicate
rejection. Aggregate digests use lowercase `sha256:` identities; file hashes
remain raw lowercase SHA-256 values. `Parse` and `ParseBoundary` are bounded
(8 MiB), strict snake_case JSON parsers: unknown or duplicate keys, case
aliases, omitted zero-valued fields, null scalars, and omitted arrays are
rejected. Empty inventories and object closures use explicit `[]` arrays.

## Off-host evidence retention

`internal/recoveryset/observationstore.Store.Save` accepts the opaque captured
projection, exact provider observations, and a required retention horizon. It
writes immutable manifest, boundary, and descriptor objects to the configured
evidence bucket using content digests and checks the existing object rather
than overwriting it. `Reload` rechecks the exact provider version, size, body
hash, descriptor bindings, and retention before returning evidence.

Source objects and evidence objects must have S3 Object Lock `COMPLIANCE`
retention through the required horizon. Retention is checked again during
reload; an expired or weaker mode is failure, never a latest-version fallback.
The initial store supports one configured source endpoint, region, and bucket.
An inventory spanning unsupported provider namespaces fails rather than being
silently reduced to a partial closure.
The recovery caller chooses the required horizon; the provider enforces
COMPLIANCE retention on each exact version. The store does not assume that
versioning protects against deletion, and it does not extend source retention
on the caller's behalf. Managed-data garbage collection cannot delete protected
versions before that horizon. Partial captures can leave retained, unreferenced
evidence objects; they are not accepted frontiers and cannot be reclaimed before
their provider retention expires. No new garbage-collection workflow is added.
The store is an off-host evidence store only and does not publish a frontier,
activate serving state, or participate in startup.

Recovery-set schema 2 may bind manifest, boundary, and descriptor digests plus
the exact boundary for qualification-only off-host persistence. Existing
schema-1 SQL publication/startup rejects managed-evidence fields, and schema 2
does not add an activation path.

## Validation and qualification

Run against the disposable qualification fixtures with Docker and the normal
repository toolchain. These commands exercise contracts and evidence checks;
they do not perform SQL restore or PostgreSQL PITR:

```sh
LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=true go test -tags='duckdb_arrow fai520qualification' ./internal/manageddata/... ./internal/recoveryset/... -count=1
LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=true go test -tags='duckdb_arrow fai520qualification' ./internal/recoveryset/... -count=2 -v
git diff --check
```

Record the manifest digest, boundary digest, descriptor digest, provider
endpoint/region/bucket/key/version, byte size/hash, and retention horizon from
the qualification output. Do not treat those observations as production
recovery authority until the required independent off-host evidence and
operational restore plan exist.
