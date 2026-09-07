# Delivery reachability and recovery boundaries

Use this guide when plan-driven delivery reports a missing, incomplete, or
indeterminate serving state. Keep the instance non-serving while collecting
evidence and stop writers before any recovery operation.

## Capture bounded evidence

Save the readiness response, image digest, configuration revision (without
secret values), deployment/target IDs, and relevant logs. The authenticated
operator snapshot remains the supported delivery view:

```text
GET /api/v1/projects/{project}/delivery/operator
```

It reports the target revision, active generation, admitted pools, serving
roots, leases, GC cycles, delete intents, and degraded reasons. Treat object
listings, filenames, and dashboard responses as observations only; do not infer
durable ownership from them.

## Recovery boundary

The offline delivery audit and repair commands have been removed. Do not invoke
old audit, repair, storage-cleanup, backup, or restore flags, edit control-plane
rows by hand, or delete catalog/Parquet objects to make readiness pass.

Production authority is the PostgreSQL control plane plus the matching
PostgreSQL-backed DuckLake catalog and object-store data. If a root, catalog, or
object is missing or has drifted, preserve the current state and use the
PostgreSQL operator's native backup/PITR and the DuckLake/object-store
provider's native snapshot, versioning, replication, or restore mechanism.
Those recovery points must be mutually consistent with the target metadata.

LeapView does not currently provide a product-owned local SQLite/file archive or
an offline delivery-repair workflow for a PostgreSQL target. Use [PostgreSQL
operations and high availability](/docs/guides/operate/postgresql-operations)
for provider ownership, capacity gates, maintenance fencing, and failover
validation. Leave readiness failed when authority or physical closure cannot
be proved and escalate with the captured evidence.

## Verify after recovery

Start the instance only after the native recovery point is restored and the
configuration matches the target. Verify the instance identity, active target
revision and generation, admitted physical pool, serving-state identity,
authorization, managed-data revisions, and one representative governed query
and dashboard. Use the normal `leapview rollback GENERATION_ID` command only
when the retained generation is known to be valid and the ordinary rollback
contract applies; it is not a substitute for database or object-store
recovery.

Preserve readiness responses, operator snapshots, native backup/PITR evidence,
catalog/object-store restore evidence, and logs with the incident record. If any
identity, digest, admission, or recovery-point check is ambiguous, keep traffic
stopped and do not create a replacement publication merely to probe the result.
