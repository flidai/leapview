# PostgreSQL operations and high availability

Use this runbook to operate the PostgreSQL services used by a production
instance and to coordinate a provider-managed failover. It covers the control
plane and the PostgreSQL-backed DuckLake catalog. Parquet and other managed
objects remain under their object-store provider's recovery contract; keep the
recovery points consistent with PostgreSQL as described in [Backup and
restore](/docs/guides/operate/backup-restore).

LeapView does not operate a PostgreSQL cluster, choose a primary, rotate a
provider credential, or install an alerting stack. The database operator or
managed service owns those actions. The application exposes bounded pool
telemetry so that an operator can correlate application pressure with the
provider's database telemetry.

## Ownership boundary

The application and the database provider answer different questions:

| Signal | Owner | Use it for |
| --- | --- | --- |
| Named pool gauges and acquire counters | LeapView `/metrics` | Detect pool saturation, queueing, churn, and failed acquisition. |
| `pg_stat_statements`, `pg_stat_activity`, `pg_locks`, and server logs | PostgreSQL exporter or managed-service metrics | Find expensive SQL, long transactions, blockers, waits, and deadlocks. |
| WAL/replication LSNs, replay delay, slots, and archive status | PostgreSQL exporter or managed-service metrics | Prove that a standby and recovery point meet the declared RPO. |
| CPU, memory, I/O, storage, connection limits, autovacuum, and bloat | PostgreSQL exporter or managed-service metrics | Gate capacity changes and maintenance. |

Do not add `pg_stat_*` queries to request handlers to replace the provider
telemetry. Keep query labels normalized and low-cardinality; never put SQL
text, user identity, project ID, or credential material in a metric label.

Watermill metrics are conditional. No production consumer is admitted by the
current deployment contract, so framework or adapter metrics must not be used
as a production SLO or as evidence that a consumer is running.

## Application pool metrics and alerts

Every retained PostgreSQL pool is exported with a stable `pool` label. The
current names are `control_runtime`, `control_maintenance`,
`control_readonly` (when configured), `ducklake_runtime`, and
`ducklake_maintenance`. The metric family is:

- `leapview_postgres_pool_max_connections`
- `leapview_postgres_pool_total_connections`
- `leapview_postgres_pool_acquired_connections`
- `leapview_postgres_pool_idle_connections`
- `leapview_postgres_pool_constructing_connections`
- `leapview_postgres_pool_acquire_count_total`
- `leapview_postgres_pool_acquire_duration_seconds_total`
- `leapview_postgres_pool_empty_acquire_count_total`
- `leapview_postgres_pool_canceled_acquire_count_total`

Counters are cumulative. Use `rate()` or `increase()` over a bounded window;
an acquisition average is
`rate(leapview_postgres_pool_acquire_duration_seconds_total[10m]) /
rate(leapview_postgres_pool_acquire_count_total[10m])`. A missing optional
readonly pool is expected and is not an alert by itself.

Start with these alert conditions and tune them only after recording a normal
load baseline:

1. **Pool saturation (warning):** acquired divided by max is at least 0.90
   for 10 minutes. **Page** at 0.98 for 5 minutes. Ignore a pool whose max is
   zero (it is disabled or not configured).
2. **No headroom (page):** idle is zero and acquired is at least 0.95 of max
   for 5 minutes, or `rate(leapview_postgres_pool_empty_acquire_count_total[5m])`
   is greater than zero for 5 minutes. Correlate with request errors before
   increasing a pool; a larger pool cannot fix a database at its connection
   or CPU limit.
3. **Canceled acquisition (warning):**
   `rate(leapview_postgres_pool_canceled_acquire_count_total[5m]) > 0` for
   10 minutes. Page when it coincides with empty acquisitions or user-visible
   timeouts. This usually indicates upstream cancellation or a queue timeout.
4. **Acquire latency (warning):** the ten-minute average derived from the two
   duration/count counters is above 2 seconds for 10 minutes, or is more than
   twice the established baseline. Page when it is above 5 seconds with empty
   acquisitions. This is a pool-wait signal, not a query-latency signal.
5. **Connection churn (warning):** constructing connections remains non-zero
   for 10 minutes, or total connections repeatedly falls and rebuilds. Check
   provider reachability, TLS, credentials, and server connection limits before
   changing pool sizes.

Inspect all five pool names when an alert fires. The maintenance pools are
intentionally separate from request traffic; saturation there can block
migrations or retention work without making interactive pools look full.

## Provider telemetry and thresholds

Install the PostgreSQL exporter supplied by the managed service, or operate an
equivalent exporter with a least-privilege monitoring role. Keep these checks
outside the application process:

- **Query load:** enable `pg_stat_statements` through the provider-supported
  mechanism. Alert when normalized total or mean execution time, calls, temp
  bytes, or I/O exceeds the service baseline for 15 minutes. Review the top
  statements before changing indexes or pool limits; do not copy query
  literals into labels.
- **Long transactions and locks:** warn at a transaction age of 5 minutes and
  page at 15 minutes, unless the workload's documented maintenance window is
  longer. Page on a lock wait over 30 seconds, and on repeated deadlocks (at
  least one in two consecutive five-minute windows). Identify the blocker and
  owning role before terminating anything.
- **WAL and replication:** warn when replay delay reaches 50% of the declared
  RPO and page when it exceeds that RPO (a 60-second RPO would use 30 seconds
  and 60 seconds). Alert on a failed or stalled WAL archive for 5 minutes and
  on replication slots that retain WAL beyond the provider's storage budget.
- **Autovacuum and bloat:** warn when dead tuples exceed 20% of a table and
  continue rising for 30 minutes. Page when a table approaches 80% of the
  provider's transaction-ID/freeze-age limit, autovacuum is blocked for 15
  minutes, or bloat consumes the declared storage headroom. Use provider
  maintenance controls; do not run unreviewed `VACUUM FULL` during traffic.
- **Capacity:** warn at 80% sustained CPU, I/O, or connection-limit use for
  15 minutes and page at 90%. Warn below 20% free storage and page below 10%,
  or sooner when WAL growth can exhaust the disk before the next maintenance
  window. Include memory pressure, IOPS/throughput limits, backup-window
  duration, and object-store bandwidth in the same capacity review.

Thresholds are starting points, not a promise that every provider exposes the
same units. Store the chosen RPO, RTO, connection budget, and storage budget
with the service record and make the page conditions reference those values.

## TLS, roles, and credential rotation

Use separate, least-privilege identities for the control runtime,
control-maintenance/migrator, optional control-readonly, DuckLake runtime, and
DuckLake maintenance paths. Runtime identities should have only the DML and
schema usage required by the running service. Maintenance and one-shot
migration identities may have narrowly scoped DDL; a migrator must not remain
in the request path. A readonly identity must not be able to mutate metadata,
leases, jobs, or access state. Do not run LeapView with a PostgreSQL superuser.

Require TLS with server certificate and hostname verification (the provider's
`verify-full` equivalent) and keep CA material in the deployment secret store.
For a rotation:

1. Create a new role or password with the same narrowly scoped grants and
   validate it against the intended database and TLS endpoint.
2. Publish the new secret to the deployment mechanism, restart or roll the
   instance so every named pool reconnects, and verify `/readyz`, pool acquire
   counters, and a representative governed query.
3. Confirm no pool is still using the old credential, then revoke it at the
   provider. Terminate existing sessions when immediate revocation is required.

Never put a connection URL, password, certificate key, or token in logs,
metrics, tickets, or incident chat. LeapView does not claim zero-downtime
rotation or dynamic credential leases; the operator must verify the rollout.

## Rolling maintenance and migration fencing

Before a provider patch, failover rehearsal, or schema migration:

1. Announce a change window and record a recent, restorable PostgreSQL and
   DuckLake/object-store recovery point. Confirm the standby is caught up and
   capacity has headroom.
2. Check `/readyz`, pool saturation, long transactions, lock waits, and active
   delivery generations. Drain conflicting deployments, refreshes, and
   retention jobs; stop new writes when the change requires it.
3. Admit one migration owner only. Run the versioned one-shot migration with
   the maintenance identity and maintenance pool; never run two migrations
   concurrently or leave the migrator pool serving requests. Use the
   deployment's upgrade/preflight checks and record their result.
4. Verify schema compatibility, readiness, pool acquisition, active
   generation, authorization, and a bounded read/write smoke test before
   resuming traffic. Keep the old application version only while the schema
   contract says it is compatible.

If a migration is irreversible or fails after a partial change, stop writers
and use the provider-native restore/PITR decision. Do not edit control-plane
rows by hand or treat a larger connection pool as a migration fix.

## Failover validation

Failover is initiated and fenced by the provider. After promotion, validate in
this order:

1. Confirm the new endpoint, certificate chain, database identity, expected
   major version, and `pg_is_in_recovery()`/read-only state. Ensure the old
   primary is fenced so two writers cannot exist.
2. Roll or restart application instances with the new endpoint. Verify
   `/healthz`, `/readyz`, and that each expected pool's acquire counter rises
   without empty or canceled acquisitions. Confirm control and DuckLake pools
   resolve to their intended databases, not merely to a reachable PostgreSQL
   server.
3. Run one bounded metadata read/write transaction, one governed analytical
   query, and one dashboard request. Verify active project and generation,
   authorization, managed-data revision, DuckLake catalog lease, and audit/job
   writes.
4. Check WAL/replay lag, backup/PITR continuity, and provider incident state.
   Record timestamps, endpoint/database identities, pool evidence, and smoke
   results. Rehearse the same validation against staging before relying on it
   in production.

Do not declare success from a successful TCP connection alone. A split-brain
or a stale DuckLake catalog can accept connections while serving incorrect
state.

## Production recovery CLI handoff

The provider owns backup/PITR, object-store restore, and the probes that prove
those operations. LeapView's production Admin CLI records that proof in a
PostgreSQL recovery frontier; it never performs provider I/O. First restore the
selected PostgreSQL and object points far enough that the control plane and
referenced objects are available. Keep writes and traffic stopped, then use
this exact qualification sequence:

1. **Prepare the frontier and hold.** Supply a prepared, immutable recovery-set
   document to the production maintenance command:

   ```sh
   leapview admin recovery prepare \
     --set /secure/recovery-set.json \
     --expires-at 2026-10-01T12:00:00Z
   ```

   `--set` and `--expires-at` are required. The expiry must be a future RFC3339
   timestamp; the optional `--retain-root-id` is a canonical UUID and defaults
   to the set ID. Prepare strictly validates a bounded set document, then
   atomically creates the prepared recovery frontier and its finite physical
   recovery-retention hold (a live `recovery` root) in one PostgreSQL
   transaction. It does not invoke PostgreSQL PITR, DuckLake, object-store, or
   any other provider API.

2. **Validate with providers.** With the finite hold installed, use the
   PostgreSQL operator and DuckLake/object-store providers to check the exact
   control/DuckLake database identities and recovery frontiers,
   object URI/version/digest and provider frontier values, and serving relation
   namespace/manifest/closure. Produce the typed evidence envelope from those
   external checks. No LeapView process performs these probes.

3. **Record one exact validation attempt.** Run:

   ```sh
   leapview admin recovery validate \
     --set-id 018f3f83-7b2f-7b37-9f9e-000000000010 \
     --attempt-id 018f3f83-7b2f-7b37-9f9e-000000000021 \
     --validator operator@example.com \
     --evidence /secure/recovery-validation.json
   ```

   All four flags are required; `--validator` is a canonical identity no
   longer than 255 bytes. Evidence is a maximum 65,536-byte strict v1 JSON
   envelope with exact keys (unknown and duplicate/case-variant keys are
   rejected), canonical digests, and identities matching the selected set and
   attempt. Validation starts or resumes the exact fenced attempt, persists the
   canonical evidence digest, and records `passed` for a valid envelope;
   malformed or mismatched evidence is rejected rather than published. It only
   reads/writes PostgreSQL and never contacts a provider.

4. **Publish under the exact fence.** After validation succeeds, run:

   ```sh
   leapview admin recovery publish \
     --set-id 018f3f83-7b2f-7b37-9f9e-000000000010 \
     --publisher operator@example.com \
     --fence-epoch 42 \
     --validation-attempt-id 018f3f83-7b2f-7b37-9f9e-000000000021
   ```

   Publish changes only that set from `prepared` to `published` under its
   positive fencing epoch and exact passed attempt. It does not choose a latest
   set or perform provider I/O.

5. **Gate restart and readiness.** Set `LEAPVIEW_RECOVERY_SET_ID` to the
   published set ID, restart the instance with the restored configuration and
   image, and admit traffic only after `/readyz` succeeds. Readiness reads that
   exact set and checks its publication, immutable validation result, target
   pointer/revision, generation/publication, native snapshot seal/catalog,
   admitted compatibility tuple, and serving-artifact identities against the
   active PostgreSQL projections. It is read-only and performs no object-store
   or other provider probes. If the variable is unset, normal startup checks
   run; there is no implicit latest-set selection.

The finite recovery hold is maintained by native PostgreSQL maintenance. Once
`--expires-at` elapses, maintenance retires the live recovery root and later
marks it `expired` after the configured grace and exact reader leases drain.
This monotonic lifecycle protects the selected DuckLake snapshot without
requiring manual row edits or object deletion; seals remain immutable history.

## Capacity gates for delivery and background work

Use the following gates before a release, migration, or planned load test:

| Workload | Gate before admitting load | Stop or reduce load when |
| --- | --- | --- |
| Delivery and serving generations | Runtime and DuckLake pools stay below 80% acquired/max; no empty acquisitions; readiness and the operator delivery snapshot are healthy. | Any pool is at 90% for 10 minutes, a lease/fencing check fails, or serving roots are indeterminate. |
| Jobs, refreshes, and retention | Maintenance pool has idle headroom; job claim latency and lease-expiry backlog are draining; provider CPU/WAL/storage remain below warning thresholds. | Claims queue, lease expiry, or deadlocks rise for 10 minutes, or maintenance work competes with request traffic. |
| Lineage and metadata projections | Control runtime/read-only pools have at least 20% headroom; projection lag and query latency are at baseline; bounded payload sizes are observed. | Projection lag grows for 15 minutes, lock waits exceed 30 seconds, or metadata writes need retries. |
| L3/object and query cache | Cache/object-store capacity, eviction rate, and network bandwidth have 20% headroom; PostgreSQL stores only catalog/metadata. | Evictions, object failures, WAL/storage growth, or cache churn threaten the RPO/RTO or database headroom. |

Run delivery, jobs, lineage, and L3 checks together: passing the pool gate does
not prove that CPU, I/O, storage, object-store bandwidth, or query plans can
carry the proposed load. Record the measured limit and rollback signal in the
change plan.

## Incident and recovery handoff

Capture `/readyz`, authenticated `/metrics`, image/configuration revisions with
secret values removed, provider dashboard links, database and endpoint
identities, replication/WAL evidence, and relevant logs. Classify the first
failure as pool pressure, PostgreSQL server pressure, provider failover, or
DuckLake/object-store drift. Apply only reversible mitigations that are in the
change plan (for example, draining refresh traffic); do not blindly increase
`max_connections`, terminate unknown sessions, delete objects, or edit
metadata rows.

For data loss, catalog drift, or an RPO/RTO breach, keep traffic stopped and
follow [Backup and restore](/docs/guides/operate/backup-restore) and [Delivery
reachability and recovery boundaries](/docs/guides/operate/delivery-recovery).
The provider's native recovery and change-management procedures remain the
authority; LeapView supplies verification evidence but does not orchestrate
failover, backups, credential rotation, or alert delivery.
