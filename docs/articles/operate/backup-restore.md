# Backup and restore

Use LeapView administrative commands and backend-native protection so the control plane, analytical catalog, analytical files, and managed source objects can be recovered to a consistent point.

## Define recovery objectives

Before choosing a schedule, record the maximum acceptable data loss (RPO) and recovery time (RTO). Include application metadata, active project state, analytical tables, managed revisions, identity/access state, and required external stores.

Dashboard source history belongs in Git, but Git cannot restore user principals, grants, active deployments, refresh state, or analytical data. Instance backups and project version control solve different problems.

## Create an instance backup

Write the archive outside the active instance directory:

```sh
leapview admin backup --out /srv/backups/leapview-2026-07-16.tar
```

The output path must not already exist. Record a checksum, creation time, LeapView version, storage-backend configuration, and the identity of any corresponding external catalog or object-store recovery point.

`--database-only` intentionally captures only the platform database. It is useful for narrow administration but is not a complete analytical recovery artifact.

## Protect external stores

For the local backend, the coordinated archive captures the local instance boundary according to the deployment contract. For S3 managed data, enable bucket versioning and independent backup or replication; the application archive contains metadata and cache, not authoritative bucket objects.

If DuckLake catalog or analytical data uses a remote backend, use its native consistent backup mechanism. Retain encryption keys and secret-manager recovery procedures separately from the encrypted data they unlock.

## Validate continuously

Automate these checks after backup creation:

- archive exists, is non-empty, and is readable only by intended operators;
- recorded checksum matches;
- external store backups cover the expected point;
- off-host retention and lifecycle rules are active;
- enough free capacity remains for the next backup and restore staging;
- retention matches policy without deleting the only good copy.

Periodically restore into an isolated environment. Open representative project resources, run analytical queries, inspect active revisions, and create a fresh post-restore backup. Test both ordinary recovery and the loss of a full node.

## Prepare a restore

Choose a maintenance window, stop traffic and writes, validate the archive checksum, confirm version compatibility, and ensure enough space for both current and restored state. Preserve the current instance before replacement:

```sh
leapview admin restore \
  --from /srv/backups/leapview-2026-07-16.tar \
  --current-out /srv/backups/pre-restore-2026-07-17.tar \
  --confirm
```

`--confirm` is required because the operation replaces configured instance state. Do not restore over a running instance by unpacking files manually.

## Verify before reopening traffic

After the process starts:

1. Check liveness and readiness.
2. Verify administrator and expected principals without changing grants.
3. Confirm the bound instance environment and active serving pointers.
4. Confirm current managed revision digests.
5. Run a representative semantic and dashboard query.
6. Inspect refresh and audit history.
7. Check storage cleanup in dry-run mode.
8. Create and validate a new backup.

If a remote object store or catalog was restored independently, reconcile its point with LeapView metadata before serving. Preserve the failed and pre-restore artifacts until the incident is closed.

See [Storage and recovery](/docs/guides/data/storage-recovery) and the generated [`admin backup`](/docs/cli/admin-backup) and [`admin restore`](/docs/cli/admin-restore) references.
