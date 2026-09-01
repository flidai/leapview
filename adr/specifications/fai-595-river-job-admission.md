# FAI-595 River job admission

Status: accepted mutable companion specification

Date: 2026-09-01

Governing decision: [ADR-0016](../0016-adopt-a-postgresql-centered-target-data-architecture.md)

Related: [FAI-592 canonical Watermill envelope](watermill-canonical-envelope.md),
[FAI-593 Watermill Router/subscriber runtime](watermill-router-runtime.md),
[FAI-594 product histories and canonical events](fai-594-product-histories-and-canonical-events.md),
and [workload admission conformance](workload-admission-conformance.md).

## Decision

River is a preferred future generic worker runtime, not an immediate target or
cutover requirement. **No current production job kind is eligible for River.**
All current kinds remain on the capability-owned PostgreSQL `jobs.job`,
`jobs.attempt`, and runner path. River can be proposed for one kind at a time
only after the candidate proof gates below pass and a reviewed cutover names
that kind. A framework dependency, a passing package experiment, or a
Watermill integration does not itself admit a production job.

The candidate must preserve the product contract while deleting more custom
queue/worker machinery than the adapters add. River execution rows are
mechanics, not product identity, event history, authorization, publication
evidence, or terminal-state authority.

### Existing durable retry contract

The retained runner already has an explicit durable retry path. A handler opts
into replay with `jobs.Retryable`, which returns a `jobs.RetryError`; the runner
persists `Retry` under the current claim fence and bounded delay. An ordinary,
unclassified handler error remains terminal by design and is persisted through
`Fail`; River is not needed to correct that policy. The implementation and
focused evidence are [`RetryError`/`Retryable`](../../pkg/jobs/job.go), the
runner's retry-versus-terminal branch
([`executeClaimedWithTimeout`](../../pkg/jobs/runner.go)), and
[`TestRunnerDurablyRequeuesExplicitRetryableFailure`](../../pkg/jobs/runner_test.go#L121).
A River candidate must preserve this explicit classification, durable terminal
evidence, and fence checks rather than broadening every error into a retry.

## Current production matrix

The matrix is intentionally conservative. “Keep” means the existing
capability-owned PostgreSQL runner is the sole queue and execution authority;
it is not a deferred dual-run or shadow mode.

| Job kind | Capability-owned product authority | River decision now | Candidate proof focus |
| --- | --- | --- | --- |
| `agent.run` | Agent conversation/run/message/event history and run terminal state | **Keep; not eligible** | Atomic run activation and enqueue; stable run ID/request digest; streaming output and cancellation; principal/group fairness; stale-worker fencing and exactly-once terminalization of model/tool effects. |
| `upload.finalize` | Managed-data upload session, immutable revision/files, bindings, retention and reconciliation evidence | **Keep; not eligible** | Enqueue and immutable revision commit in one caller-owned transaction; object-store retry/lost-ack reconciliation; content and request digests; upload lease/cancel/recovery fencing; no duplicate revision or binding. |
| `release.finalize` | Release record, candidate provenance, build/publication evidence and release history | **Keep; not eligible** | Exact candidate/plan/provenance identity; transactional enqueue/completion; immutable evidence and external artifact recovery; bounded admission/fairness; stale attempt cannot finalize a different release. |
| `deployment.activate` + `delivery.approval.activate` (approval activation) | Delivery/deployment plans, approvals, publications, serving generations, active pointers and leases | **Keep; not eligible** | Approval binds one exact plan/candidate; activation CAS and lost-response reconciliation; multi-capability transaction boundaries; reader/lease fencing, cancellation and recovery; no second activation or publication. |
| `refresh_pipeline` / `child_run` (child runs) | Refresh schedules, operations, runs, attempts, occurrences, publication links and parent/child history | **Keep; not eligible** | Scheduler and recovery leases; concurrency policy and fair admission; root/child tree transitions and aggregate completion; snapshot/publication fencing; cancellation, supersession and restart without duplicate occurrence. |

No row in this table may be moved to River by configuration alone. A future
proposal may narrow a row to a new kind, but it must retain the same product
identity, history, authorization, and terminal evidence.

## Watermill-not-jobs boundary

Watermill is the in-process router and handler boundary for canonical
`event.event_log` deliveries. The canonical event and enrolled delivery rows
are written in the source transaction; a subscriber claims a delivery and
projects a deterministic message. Watermill `Ack` follows the handler's
idempotent domain effect and durable delivery completion. Watermill topics,
offsets, attempts, and acknowledgements are transport mechanics only.

Jobs are not Watermill messages. A Watermill handler that needs long-running
work may enqueue a capability-owned job and acknowledge only after that
enqueue transaction commits. Until a kind is explicitly admitted by this
specification, that enqueue targets the PostgreSQL jobs runner, never River.
River must not be used as an event bus, a broadcast-consumer checkpoint, or a
replacement for canonical event delivery, replay, dead-letter, or retention
state.

### Retained `jobs.event` and product history

`jobs.event` is an append-only, capability-owned product history keyed by job
resource and event sequence. It is retained indefinitely until a separate
owner-approved floor, legal-hold rule, bounded maintenance operation, and
recovery evidence are admitted. A worker lease expiry, River retry, queue
cleanup, or process restart is never permission to delete it. Agent, managed
data, release, deployment, and refresh histories remain readable under their
own ACL, identity, cursor, and retention contracts even when no worker is
running. A transport acknowledgement does not substitute for a missing
history commit.

## Candidate proof gates

A River candidate is a pass/fail architectural gate, not an implementation
plan. Evidence is required for the exact job kind and its capability flow:

1. **Transactional enqueue and completion.** Source mutation plus enqueue uses
   the caller-owned PostgreSQL transaction. Domain terminal state and job
   completion are idempotent across commit, rollback, timeout, and lost
   acknowledgement; retries cannot create a second product effect.
2. **Stable identity and digest.** The capability's operation/resource ID and
   canonical request or payload digest remain stable across retries and nodes.
   A River job ID, attempt number, queue name, or transport offset is not a
   public identity or cursor. Digest drift is rejected before execution.
3. **Admission and fairness.** The adapter preserves `pkg/workload` class,
   principal, group, memory, queue, deadline, reservation, and deterministic
   fairness semantics. Capacity, cancellation, and shutdown remove accounted
   work exactly once; a busy actor cannot starve an eligible actor.
4. **Fencing, cancellation, and recovery.** Lease/claim generations bind the
   worker to the exact operation. Stale workers cannot complete, publish, or
   retry; cancellation is durable; process loss, lease expiry, restart, and
   multi-node takeover reconcile to one terminal outcome.
5. **History and evidence.** Product histories, `jobs.event`, audit rows,
   immutable publication/revision evidence, and retention roots remain owned by
   their capabilities. River cleanup never prunes history or changes ACL,
   ordering, or cursor semantics.
6. **Operational and migration safety.** Migrations, rollback, observability,
   retry/dead-letter behavior, licensing, and failure drills are proved for the
   candidate. The result removes more custom machinery than the adapter adds
   and has one durable queue authority and one runtime path.

## No dual authority or implicit cutover

Before admission, `jobs.job`/`jobs.attempt` and the capability-owned runner are
the sole mutable execution authority for every matrix row. Candidate testing
must not shadow-write River rows, run two claimers, or introduce a durable
fallback. After a successful review, a migration names one kind and one
authority, drains or reconciles old work under its existing identity, and
proves that the predecessor can no longer claim new work. A failed, partial,
or unrun gate leaves the current runner in place. There is no production mode
in which River and the PostgreSQL runner both decide completion.

## Confirmation evidence

The focused candidate suite must exercise the exact matrix row against real
PostgreSQL connections and multiple workers. It must cover transaction
rollback and lost acknowledgement, stable ID/digest replay, admission and
fairness limits, cancellation, stale-fence rejection, lease recovery,
retry/dead-letter/terminal evidence, restart and multi-node takeover. It must
also assert that `jobs.event` and capability histories remain append-only and
retained, that Watermill never subscribes to job history, and that no second
queue authority is written. Until those checks pass for one named kind, the
River status remains “preferred future runtime; no current production kind
eligible.”
