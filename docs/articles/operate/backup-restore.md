# Backup and restore

LeapView production recovery is a coordinated, provider-native operation. The
control plane is PostgreSQL and analytical state is a PostgreSQL-backed DuckLake
catalog plus its Parquet/object-store data. The production Admin CLI records and
gates one exact recovery frontier; it does not replace the PostgreSQL or
object-store provider's backup and restore tooling. The removed offline
administrative backup and restore commands are not a supported production
recovery path.

## Recovery objectives

Record an RPO and RTO for application metadata, active project state,
analytical snapshots, managed-data revisions, identity/access state, and every
external store. Keep project source in Git, but do not treat Git as a backup of
database state or analytical data.

## Native protection boundary

Use the PostgreSQL operator's supported backup and point-in-time recovery (PITR)
mechanism for the control plane. Protect the DuckLake catalog and Parquet or
object-store data with their provider-native snapshot, versioning, replication,
or backup mechanism. Select recovery points that are mutually consistent and
retain the encryption keys and secret-manager procedures needed to restore
them.

LeapView does not provide a product-owned local SQLite/file archive that can be
used as a PostgreSQL target backup. Follow [PostgreSQL operations and high
availability](/docs/guides/operate/postgresql-operations) for the provider
ownership boundary, alert conditions, maintenance fencing, credential
rotation, and failover checks. Do not claim that a local archive or copied
Parquet files are a supported restore artifact.

## Recovery qualification and publication

First use the native providers to restore the selected PostgreSQL and object
recovery points far enough that the control plane and referenced objects are
available. Keep writes stopped, then run the following qualification sequence.

1. Prepare an immutable recovery-set JSON document containing the restored
   control and DuckLake recovery identities, delivery pointer, snapshot seal,
   catalog commit, object roots, and compatibility tuple. Record it in
   PostgreSQL with the production maintenance identity:

   ```sh
   leapview admin recovery prepare \
     --set /secure/recovery-set.json \
     --expires-at 2026-10-01T12:00:00Z
   ```

   `--set` and `--expires-at` are required. The expiry is an RFC3339 timestamp
   in the future and makes the recovery hold finite; `--retain-root-id` is an
   optional canonical UUID and otherwise defaults to the recovery-set ID.
   Prepare validates the bounded set document and atomically inserts the
   prepared frontier and its finite physical recovery-retention hold (a live
   `recovery` root) in one PostgreSQL transaction. It performs no PostgreSQL
   PITR, DuckLake, object-store, or other provider I/O.

2. With the finite hold installed, use the PostgreSQL operator and the
   DuckLake/object-store providers to perform the external checks. Prove the
   control and DuckLake database
   identities and recovery frontiers, every object URI/version/digest and
   provider recovery frontier, and the relation namespace, manifest, and
   closure digests named by the prepared set. Keep those provider-produced
   observations as a typed evidence envelope; LeapView does not run these
   probes.

3. Record that evidence against one exact, fenced validation attempt:

   ```sh
   leapview admin recovery validate \
     --set-id 018f3f83-7b2f-7b37-9f9e-000000000010 \
     --attempt-id 018f3f83-7b2f-7b37-9f9e-000000000021 \
     --validator operator@example.com \
     --evidence /secure/recovery-validation.json
   ```

   `--set-id`, `--attempt-id`, `--validator`, and `--evidence` are required;
   `--validator` must be a canonical identity of at most 255 bytes. Evidence
   is bounded to 65,536 bytes and must be the strict v1 JSON envelope: exact
   fields only, no unknown or duplicate (including case-variant) keys, and
   canonical identities/digests that match the selected set and attempt. The
   command transports and records a valid envelope, computes its canonical
   result digest, and completes the attempt as `passed`; malformed or
   mismatched evidence is rejected rather than published. It does not contact
   a provider. Retries must use the same attempt and exact evidence.

4. Publish only that exact passed attempt under the set's fencing epoch:

   ```sh
   leapview admin recovery publish \
     --set-id 018f3f83-7b2f-7b37-9f9e-000000000010 \
     --publisher operator@example.com \
     --fence-epoch 42 \
     --validation-attempt-id 018f3f83-7b2f-7b37-9f9e-000000000021
   ```

   Publication is a fenced `prepared` → `published` control-plane transition;
   it never selects a latest set and performs no provider I/O.

5. Set `LEAPVIEW_RECOVERY_SET_ID` to the published set ID, restart LeapView
   with the restored configuration/image, and wait for `/readyz` before
   admitting traffic. Readiness reads only that exact ID and requires its
   published status, passed immutable attempt/result, target pointer and
   revision, generation/publication, snapshot seal and catalog identity,
   admitted compatibility tuple, and serving-artifact identities to match the
   active PostgreSQL projections. The check is read-only and performs no
   object-store or other provider probes. An unset variable runs the ordinary
   startup checks; it never means “latest recovery set.”

The `--expires-at` hold is retired by native PostgreSQL maintenance when its
deadline elapses. Maintenance advances the root monotonically from `live` to
`retiring`, then to `expired` only after its configured grace and exact reader
leases have drained. Do not edit retention rows or delete physical objects by
hand; immutable snapshot seals remain historical evidence after reachability
expires.

## Restore and verify

The sequence above keeps writes stopped through provider restore, validation,
publication, and restart. Set `LEAPVIEW_RECOVERY_SET_ID` before that restart and
use `/readyz` as the pre-traffic gate; LeapView never selects the latest set.
Before admitting traffic, verify the instance identity, active
project/deployment pointers, authorization state, managed-data revisions,
representative semantic queries, and dashboards. Preserve the failed state and
all provider and LeapView recovery evidence until the incident is closed.

For an empty or development SQLite fixture, use the fixture's own test harness;
that path is not production recovery. The commands above are production-only
and require the PostgreSQL maintenance configuration; they do not provide an
offline restore or a local archive.
