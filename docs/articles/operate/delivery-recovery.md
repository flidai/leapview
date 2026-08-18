# Delivery reachability audit and bounded repair

Use this runbook when plan-driven delivery reports a missing, incomplete, or
indeterminate serving state, or when an upgrade leaves a physical-pool root in
question. The durable root in the platform SQLite database and the immutable
catalog object are the authorities. Do not infer a root from an object-store
listing, a filename, or a dashboard response.

The sequence is:

```text
capture evidence → make a consistent backup → stop writers → audit →
repair dry-run → review evidence → bounded quarantine apply → verify/rollback
```

Keep the instance non-serving while the audit or repair is in progress. A
failed check is a stop condition; it is not permission to retry with a new
identity.

## Capture evidence and make a rollback boundary

Before stopping the process, save the readiness response, image digest,
configuration revision (without secret values), deployment/target IDs, and
logs. The authenticated operator snapshot is useful for incident correlation:

```text
GET /api/v1/projects/{project}/delivery/operator
```

It reports the target revision, active generation, admitted pools, roots,
reader/writer leases, GC cycles, delete intents, and degraded reasons. The
offline audit remains authoritative for catalog reachability.

Create a complete instance backup outside `LEAPVIEW_HOME` and record its
checksum before any repair:

```sh
leapview admin backup --out /srv/backups/leapview-before-delivery-repair.tar
sha256sum /srv/backups/leapview-before-delivery-repair.tar \
  > /srv/backups/leapview-before-delivery-repair.tar.sha256
```

The full archive covers the control plane and configured in-home DuckLake,
artifact, runtime, and local managed-data boundaries. `--database-only` is
only a control-plane checkpoint; it is not a recoverable analytical point
unless the corresponding catalog and data backup is independently consistent:

```sh
leapview admin backup \
  --out /srv/backups/leapview-control-before-delivery-repair.db \
  --database-only
```

Stop the LeapView process and all delivery/build/refresh workers using the
deployment's maintenance mechanism. Preserve the current volume, logs, and
backup until the incident is closed.

## Run the read-only reachability audit

The audit enumerates the active durable root set for one admitted physical
pool, then verifies each exact catalog object and its read-only DuckLake
closure. It acquires no destructive offline lock, does not migrate SQLite,
and has no object-store deletion path:

```sh
leapview admin delivery audit --pool-id POOL_ID
```

The output is stable text evidence that can be attached to the incident:

```text
mode: audit
pool_id: POOL_ID
root_revision: 42
root_count: 1
root: published/GENERATION_ID
status: active
catalog_digest: sha256:...
object_key: catalogs/sha256/....ducklake
created_at: 2026-08-18T12:00:00Z
candidate_id:
generation_id: GENERATION_ID
lease_id:
expires_at:
data_files: 12
delete_files: 0
verification: passed
```

For every root, the audit binds the root to its admitted compatibility tuple,
reads the immutable catalog bytes, checks the catalog and object metadata
digests, opens DuckLake read-only, requires one retained snapshot and zero
live inlining, and verifies every referenced data/delete object is present in
the same immutable pool. It fails closed on a missing or changed SQLite root,
missing/corrupt objects, incompatible admission evidence, an ambiguous root,
or an unknown closure. A failed audit emits no successful result; preserve the
backup and follow the applicable recovery branch below.

For an S3 physical pool, DuckLake also requires the target-scoped credential
bootstrap used for the target's analytical runtime. The offline adapter never
falls back to ambient process credentials; if that bootstrap is unavailable,
the audit and repair fail closed and the target's normal credential/bootstrap
configuration must be restored before retrying.

## Prepare an exact repair dry-run

Only repair a root printed by a successful audit. Copy every identity exactly;
do not derive an object key from a digest or substitute a newer timestamp.
`--created-at` and `--expires-at` must be RFC3339 UTC timestamps with `Z` (the
CLI rejects a non-UTC offset). The required and optional fields are:

| Flag | Source | Requirement |
| --- | --- | --- |
| `--pool-id` | `pool_id` | Required; exact admitted physical-pool identity. |
| `--kind` | `root` prefix | Required; `build`, `candidate`, `published`, `rollback`, `lease`, `retained`, or `quarantined`. |
| `--source-id` | `root` suffix | Required; exact durable source identity. |
| `--catalog-digest` | `catalog_digest` | Required; immutable `sha256:` digest. |
| `--object-key` | `object_key` | Required; exact immutable catalog key. |
| `--created-at` | `created_at` | Required; RFC3339 UTC. |
| `--candidate-id` | `candidate_id` | Include when printed. |
| `--generation-id` | `generation_id` | Include when printed. |
| `--lease-id` | `lease_id` | Include when printed. |
| `--status` | `status` | Usually `active`; default is `active`. |
| `--expires-at` | `expires_at` | Include when non-empty; RFC3339 UTC. |

Run without `--apply` first. This repeats the exact SQLite-root, artifact,
digest, and closure checks but does not acquire the destructive lock or mutate
rows:

```sh
leapview admin delivery repair \
  --pool-id POOL_ID \
  --kind published \
  --source-id GENERATION_ID \
  --generation-id GENERATION_ID \
  --catalog-digest 'sha256:...' \
  --object-key 'catalogs/sha256/....ducklake' \
  --status active \
  --created-at '2026-08-18T12:00:00Z'
```

The command has one fixed action: `quarantine`. There is no `--action`, raw
object key, catalog rewrite, or delete flag. A dry-run error means the root
changed, is absent, or its physical closure is not provable; do not apply a
command that did not pass this check.

## Apply the bounded quarantine

After reviewing the audit output, backup checksum, root identity, and dry-run
logs, repeat the exact command with `--apply`:

```sh
leapview admin delivery repair \
  --pool-id POOL_ID \
  --kind published \
  --source-id GENERATION_ID \
  --generation-id GENERATION_ID \
  --catalog-digest 'sha256:...' \
  --object-key 'catalogs/sha256/....ducklake' \
  --status active \
  --created-at '2026-08-18T12:00:00Z' \
  --apply
```

Apply re-verifies before opening the writable control-plane connection and
records the bounded quarantine action as `offline-admin`. For a `build` root,
the seal is failed; for a `candidate`, its lifecycle is failed; for a
`retained`, `lease`, or existing `quarantined` root, the registry row is
retired. A `published` or `rollback` root is never silently removed from the
active serving pointer. Quarantine leaves the catalog object, Parquet files,
and DuckLake metadata untouched and creates an auditable protection root.

There is deliberately no physical deletion in this command. Do not follow a
repair with `leapview admin storage cleanup --apply` as a substitute for
reachability repair. GC may only act later through its own fenced mark,
revalidate, and conditional-delete protocol after the root is no longer
protected.

## Recovery branches and fail-closed rules

### Incomplete seals or missing serving identity

An unverified/preparing seal, candidate without a serving-state identity, or
legacy generation with an empty identity is not a serving root. Do not edit
SQLite status or identity columns. If the audit can prove the exact `build`
root and closure, quarantine it with the bounded command; otherwise rebuild
and reseal with the current target revision, or restore the last validated
backup.

### Missing root or object

An object that exists without the exact durable SQLite root is not repairable:
the repair command rejects root drift even when bytes happen to match. A
missing catalog object, digest mismatch, missing referenced file, or unknown
DuckLake closure also fails the audit and repair dry-run. Preserve both the
backup and the physical namespace, then reconcile from the original durable
publication or restore. Never fabricate a root row or move/copy Parquet files.

### Incompatible application or catalog upgrade

If persistent state remains backward compatible, roll back to the prior
immutable application artifact and verify readiness. If a migration changed
SQLite or DuckLake incompatibly, do not start the old binary against the new
state. Restore the complete validated instance backup instead, preserving the
failed state and upgrade logs. Follow [Upgrades and migrations](upgrades) for
the application rollback boundary.

### SQLite-control loss

When SQLite is unavailable or corrupt, the audit and repair commands cannot
establish an authoritative root and must not be run against a guessed object.
Keep traffic stopped. Restore into the configured instance boundary with an
explicit checkpoint of the current state:

```sh
leapview admin restore \
  --from /srv/backups/leapview-before-delivery-repair.tar \
  --current-out /srv/backups/leapview-control-loss-current.tar \
  --confirm
```

Use `--database-only` only when the analytical catalog, Parquet/object store,
and control backup are known to be the same recovery point. After restore,
rerun the audit, verify readiness and a representative governed query, then
use the normal rollback command for a retained generation when appropriate:

```sh
leapview rollback GENERATION_ID
```

Do not create a replacement publication to probe an indeterminate outcome;
reconcile the original durable publication or restore again.

## Verification after repair or rollback

1. Keep the maintenance boundary until `leapview admin delivery audit --pool-id POOL_ID` passes.
2. Confirm the expected active generation, target revision, pool admission, and serving-state identity.
3. Check that the quarantine event and actor appear in the audit/event stream.
4. Run one representative governed semantic query and dashboard check.
5. Preserve the audit output, dry-run/apply output, backup checksum, and logs with the incident record.

If any identity, digest, closure, admission, or recovery-point check is
ambiguous, leave readiness failed and stop. The safe outcome is retained
state plus evidence, not deletion or a guessed repair.
