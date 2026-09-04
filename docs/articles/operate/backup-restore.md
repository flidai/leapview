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

Use a reviewed service record rather than an implied default. At minimum, name
the provider, region/cluster, backup policy, encryption-key owner, declared RPO,
declared RTO, and the last measured drill for each boundary:

| Boundary | Recovery point to prove | Recovery time to measure |
| --- | --- | --- |
| Control PostgreSQL | WAL/PITR timestamp or provider backup ID, including the control database identity and timeline | Provider restore complete, credentials accepted, and the exact recovery set published |
| DuckLake PostgreSQL catalog | Catalog database identity, provider PITR point, catalog version, and DuckLake snapshot ID | Catalog reachable and the snapshot seal/closure validates |
| DuckLake/object roots and serving artifacts | Object URI, immutable version/generation, digest, and provider restore/versioning evidence | Every root readable and `/readyz` passes after the selected frontier is published |
| Credential and key material | Secret-manager/provider version, role or service identity, TLS CA/certificate, and key references (never secret values) | Secret references restored, pools reconnect, and a bounded governed query succeeds |

Measure RPO from the latest durable provider point to the incident boundary and
RTO from the start of provider restore to the first admitted request. A drill
that only opens a TCP connection, restores one database, or proves a copied
file is not an RPO/RTO result. Keep the provider's native operation IDs and
timestamps with the LeapView recovery evidence; do not put passwords, bearer
tokens, private keys, or raw connection URLs in that record.

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

## Provider-native restore drill

Run this drill in an isolated target with the same PostgreSQL major, DuckLake
extension/specification, object naming contract, encryption domains, and
credential-provider integration as production. The provider operator owns the
restore APIs and can substitute the managed service's equivalent commands.

1. Select one mutually consistent PostgreSQL PITR timestamp (or provider
   backup ID) for the control and DuckLake databases. Confirm WAL/archive
   continuity, the database/cluster identities, and the provider timeline before
   allowing the restore to proceed.
2. Restore the control database and DuckLake catalog with the provider-native
   PITR workflow. Restore each immutable object root and serving-artifact root
   by its provider version/generation, then verify the object digest and
   encryption-key version. Do not replace a missing version with the latest
   object.
3. Restore the secret-manager references required by the named control and
   DuckLake pools, including the role credentials, TLS CA/certificate chain,
   and key-encryption references. Resolve the exact retained provider version;
   a newly rotated secret is a new recovery input and must be recorded as such.
   If the secret manager itself was unavailable, recover it using its native
   export/replication procedure before starting LeapView. Never copy secret
   values into the recovery-set JSON or evidence.
4. Keep writes and traffic stopped. Run `leapview admin recovery prepare`, the
   external provider probes, `leapview admin recovery validate`, and
   `leapview admin recovery publish` from the qualification sequence below.
   Capture provider operation IDs, point-in-time/timeline identities, object
   version IDs, key versions, and command output with secrets redacted.
5. Set `LEAPVIEW_RECOVERY_SET_ID`, restart, and gate traffic on `/readyz`.
   Verify a metadata read/write transaction, one governed DuckLake query, one
   representative dashboard, access policy evaluation, and managed-data
   revision visibility. Record measured RPO/RTO and retain failed-state
   evidence until the incident or drill review is closed.

The repository can validate the immutable frontier, evidence digest, and
active-pointer bindings, but it cannot honestly prove a provider PITR,
object-store version restore, encryption-key recovery, or secret-manager
restore in this local test environment. Those steps remain external-provider
admission gates; a local unit or PostgreSQL conformance test must not be
reported as completion of this drill. The active serving seal carries the
object URI and digest, while provider version/frontier identifiers are retained
in the recovery-set evidence; readiness deliberately does not re-probe those
providers. A new provider version or frontier therefore requires a new
recovery-set validation before publication.

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
