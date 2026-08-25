# Backup and restore

Use LeapView administrative commands and backend-native protection so the control plane, analytical catalog, analytical files, and managed source objects can be recovered to a consistent point.

## Define recovery objectives

Before choosing a schedule, record the maximum acceptable data loss (RPO) and recovery time (RTO). Include application metadata, active project state, analytical tables, managed revisions, identity/access state, and required external stores.

Dashboard source history belongs in Git, but Git cannot restore user principals, grants, active deployments, refresh state, or analytical data. Instance backups and project version control solve different problems.

## Create an instance backup

Write the archive outside the active instance directory:

```sh
install -d -m 0700 /srv/backups
leapview admin backup --out /srv/backups/leapview-2026-07-16.tar.gz
```

Use a dedicated backup directory rather than a shared output directory. The direct backup path enforces mode `0700` on its parent and `0600` on the completed archive even when the process umask is permissive. Compose and supported host installations apply the same private directory, archive, checksum, and systemd `UMask=0077` contract.

The output path must not already exist. Record a checksum, creation time, LeapView version, storage-backend configuration, and the identity of any corresponding external catalog or object-store recovery point.

`--database-only` intentionally captures only the platform database. It is useful for narrow administration but is not a complete analytical recovery artifact.

## Protect external stores

For the local backend, the coordinated archive captures the local instance boundary according to the deployment contract. For S3 managed data, enable bucket versioning and independent backup or replication; the application archive contains metadata and cache, not authoritative bucket objects.

If DuckLake catalog or analytical data uses a remote backend, use its native consistent backup mechanism. Retain encryption keys and secret-manager recovery procedures separately from the encrypted data they unlock.

### Record an executable S3 recovery point

First stop writes and use the object store's native versioning, snapshot, replication, or inventory mechanism to select an immutable recovery point. The point must cover the exact S3 identity configured for LeapView: provider, credential-free endpoint, region, bucket, and prefix. Keep credentials in the deployment secret store; neither input below contains credentials.

For example, record the selected point and a stable evidence key in `/srv/backups/source-external-recovery.json`:

```json
[
  {
    "role": "managed-data",
    "recoveryPoint": "inventory-sha256:7c0d8f4e.../2026-07-16T02:00:00Z",
    "evidenceKey": "managed-data-2026-07-16"
  }
]
```

Create the application archive only after that point exists:

```sh
leapview admin backup \
  --out /srv/backups/leapview-2026-07-16.tar.gz \
  --external-recovery-points /srv/backups/source-external-recovery.json
```

Manifest v2 binds that recovery point and evidence key to the configured provider, endpoint, region, bucket, and prefix. Backup fails when S3 is configured and the exact external point is absent.

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

Choose a maintenance window, stop traffic and writes, validate the archive checksum, confirm version compatibility, and ensure enough space for archive validation, restored state, and the current-state checkpoint. Do not restore over a running instance or unpack files manually.

For an external-data restore, use this sequence:

1. Read the selected backup's manifest or preflight output and identify every `externalPrerequisites` entry. Restore the native object-store recovery point into the exact provider, endpoint, region, bucket, and prefix named by the backup before replacing LeapView state.
2. Independently verify the restored object-store point. Map each manifest evidence key to the exact verified recovery-point value in `/srv/backups/source-external-evidence.json`:

   ```json
   {
     "managed-data-2026-07-16": "inventory-sha256:7c0d8f4e.../2026-07-16T02:00:00Z"
   }
   ```

3. If the target home already contains an instance, select a new native recovery point for its current external store before restore. Record it in `/srv/backups/current-external-recovery.json`; this point belongs to the safety checkpoint, not the source backup:

   ```json
   [
     {
       "role": "managed-data",
       "recoveryPoint": "inventory-sha256:91be36a2.../2026-07-17T01:30:00Z",
       "evidenceKey": "managed-data-before-restore-2026-07-17"
     }
   ]
   ```

4. Run read-only preflight with the same archive, evidence, checkpoint path, and current recovery-point inputs intended for restore:

   ```sh
   leapview admin restore \
     --from /srv/backups/leapview-2026-07-16.tar.gz \
     --current-out /srv/backups/pre-restore-2026-07-17.tar.gz \
     --external-evidence /srv/backups/source-external-evidence.json \
     --current-external-recovery-points /srv/backups/current-external-recovery.json \
     --preflight-only
   ```

   Continue only when the JSON plan has `allowed: true` and its archive digest, external prerequisites, target topology, checkpoint topology, capacity values, replacement inventory, and target-tree checksum match the incident plan. Preflight creates no checkpoint and mutates no target state.

5. Run the restore with identical inputs, replacing only `--preflight-only` with `--confirm`:

```sh
leapview admin restore \
  --from /srv/backups/leapview-2026-07-16.tar.gz \
  --current-out /srv/backups/pre-restore-2026-07-17.tar.gz \
  --external-evidence /srv/backups/source-external-evidence.json \
  --current-external-recovery-points /srv/backups/current-external-recovery.json \
  --confirm
```

Actual restore reruns the same preflight validation before mutation and fails if the archive or target changed. It then writes the current-instance checkpoint with the current external topology before replacing state. Preserve both the checkpoint archive and its native external recovery point until the incident is closed.

For a genuinely empty target, omit `--current-out` and `--current-external-recovery-points`; there is no current state to checkpoint. `--current-out -` creates and validates a temporary checkpoint and then discards it, so use it only when an independently durable rollback copy is explicitly unnecessary.

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

For a plan-delivery restore, keep traffic stopped until the control-plane
checks pass. Verify that every configured production target has an admitted
physical pool, a target revision, and an active generation with a non-empty
serving-state identity. Rows with missing identity or a publication in the
`indeterminate` state remain non-serving until the original request is
reconciled; never create a replacement request merely to probe the result.
If any check is missing or ambiguous, leave readiness failed, preserve the
backup, and repair or restore again from the last validated point.

See [Storage and recovery](/docs/guides/data/storage-recovery) and the generated [`admin backup`](/docs/cli/admin-backup) and [`admin restore`](/docs/cli/admin-restore) references.
