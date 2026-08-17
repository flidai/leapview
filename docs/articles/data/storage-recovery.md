# Storage and recovery

LeapView separates application state, analytical metadata, analytical files, managed source objects, and ephemeral runtime data. Recovery succeeds only when the authoritative boundaries are backed up consistently.

## Storage ownership

The control-plane database owns application state such as users, grants, projects, environments, deployments, jobs, audit records, and active serving pointers.

DuckLake owns analytical table schemas, snapshots, changesets, statistics, and physical file manifests. Parquet files hold the analytical table data described by that catalog. DuckDB is the execution engine; it is not the authority for a second copy of serving state.

Managed-data storage owns staged source objects and immutable revision manifests. Runtime extraction directories and temporary files are caches or work areas and should be reconstructable.

Do not back up only `leapview.db` and assume the rest can always be reconstructed. The control-plane pointer without its referenced DuckLake catalog and Parquet files is incomplete. Likewise, copied Parquet files without the catalog metadata do not form a recoverable analytical state.

## Local-backend backup

Create a coordinated instance archive:

```sh
leapview admin backup --out /srv/backups/leapview-backup.tar
```

Use `--database-only` only for a deliberately limited operation; it is not a complete analytical instance backup. Record the application version, configuration, storage backend, archive checksum, and creation time with the backup.

For a local managed-data backend, the coordinated instance backup includes the local authoritative object store and LeapView metadata according to the deployment contract. Ensure the destination is outside the live instance directory and is protected with appropriate permissions and retention.

## Object-storage boundary

With an S3-style managed-data backend, the instance archive does not replace bucket-native protection of authoritative objects. Enable versioning and an independent backup or replication policy. Recovery requires both LeapView metadata and the corresponding object versions.

Analytical catalog and Parquet backups must form a mutually consistent recovery point with the node's control-plane backup.

## Validate backups

A successful command exit is only the first check. Regularly verify:

- the archive exists and has the expected non-zero size;
- its checksum is recorded and can be revalidated;
- required external bucket/catalog backups cover the same period;
- retention protects enough copies from operator error and corruption;
- an isolated restore can start and query representative dashboards.

Practice restores on a schedule. A backup that has never been restored is an untested assumption.

## Restore safely

Stop serving traffic or enter the supported maintenance boundary before replacing an instance. Restore from a validated archive and ask the command to preserve the current instance first:

```sh
leapview admin restore \
  --from /srv/backups/leapview-backup.tar \
  --current-out /srv/backups/pre-restore.tar \
  --confirm
```

Restore into the configured instance boundary, not a live runtime subdirectory selected ad hoc. The command requires explicit confirmation because it replaces current state.

After restoration:

1. Start the application with the matching supported configuration.
2. Verify health and storage attachment.
3. Confirm the bound instance environment, projects, users, grants, and active serving pointers.
4. Open representative dashboards and run semantic queries.
5. Confirm managed revision lookups and refresh history.
6. Run a non-destructive backup or maintenance inspection.

If any authoritative external store was restored to a different point, reconcile it before admitting traffic. Do not repair metadata by manually changing snapshot IDs or moving Parquet files.

Read [Storage architecture](/docs/storage-architecture) for the detailed model and [Backup and restore](/docs/guides/operate/backup-restore) for the production runbook. Use the generated [`admin backup`](/docs/cli/admin-backup) and [`admin restore`](/docs/cli/admin-restore) references for exact flags and accepted arguments.
