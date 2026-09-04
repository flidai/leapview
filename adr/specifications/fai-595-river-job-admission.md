# FAI-595 River job execution boundary

Status: accepted mutable companion specification

Governing decision: [ADR-0020](../0020-adopt-a-postgresql-centered-target-data-architecture.md)

## Decision

River OSS owns operational job execution. LeapView owns product job identity,
authorization, canonical request digests, audit and event records, product
history, result evidence, and workload admission. `jobs.job_history` is the
product-owned history record; River's `public.river_*` rows are operational
queue and worker state. The current tree has one execution path and no custom
PostgreSQL runner. Production support remains gated on the `release.finalize`
compatibility proof and its recorded evidence. Dual writes, shadow workers,
and durable fallback queues are prohibited.

River is not an event bus and does not own refresh occurrences, deployment
approvals, release records, serving activation, DuckLake build evidence, or
retention roots.

## Transaction boundaries

- Enqueue uses River `InsertTx` in the caller-owned product transaction.
  Rollback leaves neither product intent nor a River job.
- A worker opens one PostgreSQL transaction for the product terminal mutation
  and River `JobCompleteTx` or `JobCancelTx`, then commits once.
- A committed terminal transaction wins over a later worker return path.
- Product retries address the same product ID and canonical request digest.
  River's numeric job ID is operational and never becomes a public identity.
- Raw internal errors are logged privately. Workers return only bounded,
  sanitized classifications because River persists returned errors.

## Typed jobs

Every kind has a typed `JobArgs` value containing only the stable product
identity and canonical request digest needed to reload authoritative state.
Large payloads, credentials, user-provided SQL, and mutable snapshots do not
belong in River args.

River uniqueness is an operational duplicate-suppression aid, not the durable
product idempotency authority. Product repositories independently reject a
reused identity with a different digest.

## Workload admission

River selects a job before worker middleware acquires LeapView workload
admission. Queue and worker counts must therefore be bounded so blocked
admission cannot create unbounded goroutines or memory. The existing scheduler
continues to enforce principal, group, and workload-class fairness. Cancellation
must release both the workload lease and database transaction.

## Compatibility proof

The `release.finalize` proof must run against PostgreSQL 18 and demonstrate:

1. caller-owned enqueue commit and rollback;
2. typed args with stable release ID and request digest;
3. successful finalization and River completion in one transaction;
4. retry after a transient sanitized failure;
5. process-loss/replay idempotency without a second terminal product effect;
6. conflicting request digest rejection;
7. cancellation and workload-lease release;
8. multi-worker execution without duplicate finalization;
9. River operational-row cleanup while `jobs.job_history` and release history
   remain queryable; and
10. absence of writes to the removed custom queue.

A failed proof blocks production support. The current tree has one clean
repository-wide River path for the admitted job kinds; the custom runner,
queue schema, SQLite backend, and dual-run lifecycle are removed. The proof
above remains the qualification evidence for retaining that production
boundary, including River cleanup with `jobs.job_history` preserved.
