# Storage and recovery

LeapView separates application state, analytical metadata, analytical files,
managed source objects, and ephemeral runtime data. Production authority lives
in PostgreSQL and a PostgreSQL-backed DuckLake catalog; Parquet and managed
source objects remain in their configured object stores.

## Storage ownership

The `leapview_control` PostgreSQL database owns users, grants, projects,
environments, deployments, jobs, event and audit records, lineage projections,
cache coordination, leases, and active serving pointers. The separately owned
`leapview_ducklake` database contains DuckLake metadata: analytical schemas,
snapshots, changesets, statistics, and physical-file manifests. Parquet files,
managed-data objects, immutable serving artifacts, and optional shared-cache
objects hold the bytes described by those authorities. Runtime directories and
L1/L2 query caches are disposable.

A recoverable analytical state therefore requires the control-database
recovery point and the matching DuckLake-database and object-store recovery
points.
Backing up only a local `leapview.db`, a catalog file, or copied Parquet objects
does not recover a production target.

## Native recovery boundary

Use PostgreSQL's native backup/PITR facilities for control-plane recovery. Use
the DuckLake/catalog and object-store provider's native snapshot, versioning,
replication, or backup facilities for analytical and managed-data objects.
Coordinate the selected points and retain the encryption keys and secret-store
procedures required to restore them.

The removed offline administrative backup, restore, and storage-cleanup
commands are not production recovery or cleanup procedures. LeapView currently
has no product-owned local file archive that substitutes for PostgreSQL
backup/PITR. Follow the [PostgreSQL operations
guide](/docs/guides/operate/postgresql-operations) and [Backup and restore
guide](/docs/guides/operate/backup-restore) for the complete operational
procedure; do not invent CLI flags or manually rewrite catalog and pointer
metadata.

## Verify a recovery

Keep writes stopped while native recovery runs. Start LeapView with the matching
image and configuration only after both PostgreSQL databases and the
corresponding object-store points are restored. Follow the backup and restore
guide's `leapview admin recovery prepare`, `validate`, and `publish` sequence
for one immutable recovery-set document and its provider-produced evidence.
That sequence checks the exact control, DuckLake, object, active-generation,
snapshot-seal, and compatibility frontier before traffic can resume. Then
verify authorization, managed-data revisions, representative semantic queries,
and dashboards. Preserve recovery evidence and the failed state until
verification is complete.

Development and evaluation fixtures may use embedded SQLite and a local DuckLake
catalog, but those adapters are not a production fallback. Test fixture backup
and restore belongs to the fixture harness, not this production runbook.
