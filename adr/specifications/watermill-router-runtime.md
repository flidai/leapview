# FAI-593 canonical Watermill Router/subscriber runtime

Status: accepted mutable companion specification

Last updated: 2026-08-31

Governing decision: [ADR-0016](../0016-adopt-a-postgresql-centered-target-data-architecture.md)

Related: [FAI-592 canonical Watermill envelope](watermill-canonical-envelope.md)
and [FAI-591 PostgreSQL qualification](watermill-postgresql-proof.md)

Implementation status and delivery evidence are tracked in the FAI-593 Linear
work; this specification records the runtime contract, not project progress.

## Purpose and authority

This specification fixes the boundary between LeapView's durable event
authority and Watermill's in-process router. The PostgreSQL `event_log`,
`event_delivery`, and persisted consumers (including their aggregate filters,
enrollment/replay roots, attempts, leases, fences, terminal state, and
retention decisions) are the sole authority for delivery, retry, dead-letter,
replay, and retention. Watermill-owned SQL transport tables, offsets, or a
second mutable delivery authority are not allowed in production.

The four current Watermill topics are the allowlisted capability topics
`agent`, `dashboard`, `delivery`, and `release`. They are not conceptual
control-, lineage-, or cache-topics. No concrete lineage or cache consumer is
implied by this specification.

`message.UUID`, the canonical envelope, and the strict metadata contract are
defined by FAI-592. Metadata is exactly `{topic}`; handlers and middleware
must not add, rewrite, or derive metadata (including correlation or delay
metadata).

## Subscriber and claim protocol

The production adapter is a custom Watermill `message.Subscriber` over the
canonical tables. It must satisfy all of the following:

- Registry-fenced append, enrollment, backfill, and retirement transactions
  use PostgreSQL `READ COMMITTED`. The canonical repository rejects stronger
  isolation before doing event work because their transaction-wide snapshots
  cannot observe the post-fence consumer boundary.
- A claim transaction atomically selects one consumer-specific
  `event_delivery` row, records the attempt and lease, and binds the exact
  worker identity and claim-generation fence. The transaction commits before
  the message is emitted to Watermill and is closed before any handler runs.
  Its SQL deadline is no longer than the configured recovery margin, so an
  overlong claim rolls back instead of committing after its useful lease
  window has already been consumed.
- Every claimed-state completion or retry transition matches both the worker
  and claim-generation fences. A stale worker, generation, or lease cannot
  complete or retry a delivery. Authorized replay and waiver use their own
  explicit terminal-state preconditions and evidence.
- `Nack` records a replayable failure in a short transaction. The next attempt
  performs a fresh claim (and therefore a fresh fence and lease); it never
  re-emits a message under the old claim.
- A successful handler commits its idempotent domain effect and terminal
  `Complete` transition before the subscriber permits Router `Ack`. If that
  commit fails, the message is nacked and never acknowledged.
- Process loss, disconnect, or an expired lease is recovered by the successor's
  durable claim transaction. That claim fences the old owner, advances the
  claim generation, and makes no use of an in-memory cursor or Watermill
  offset.

Subscriber shutdown stops new claims and waits for its bounded
acknowledgement/retry persistence watchers. Unfinished delivery claims are not
rewritten during shutdown; they remain recoverable through lease expiry.

The subscriber may use polling and a PostgreSQL notification as a wake-up hint,
but correctness always comes from reconciling `event_log` and
`event_delivery`. A transaction is never held across message emission,
handler execution, or Router middleware.

## Router and middleware boundary

Use the Watermill core `Router` and only the mature, explicitly configured
`Retry`, `Recoverer`, `Timeout`, handler-level `Prometheus`, and `slog`
middleware. The Prometheus handler middleware is built with the injected
registerer; `AddPrometheusRouterMetrics` is prohibited because its subscriber
decorator starts per-message Ack/Nack goroutines that can outlive subscriber
shutdown. No publisher or subscriber decorators are installed. All bounded
in-process behavior must not become a second attempt, poison, replay, or
retention authority; durable attempt and terminal decisions remain in
`event_delivery`.

The runtime execution tracker is the outer lifecycle frame and has a
stop-accepting gate; inside it, the functional middleware order is handler
Prometheus instrumentation, topic capture, one outer handler deadline,
bounded in-process Retry, Recoverer inside Retry, and durable completion
immediately around the handler. Recoverer therefore turns a panic into a
retryable handler error, while the outer deadline bounds the complete local
retry window. Watermill's Timeout cancels the message context; the completion
boundary also rejects a handler that ignores cancellation and returns a late
nil result, so a deadline-expired effect cannot become `Complete`/`Ack`.

The runtime is an application lifecycle component. Fatal monitoring starts
before startup pre-subscribes every canonical Subscriber and verifies its
persisted enrollment. A subscriber failure during a later subscriber's
enrollment therefore cancels preflight and cannot be reported as Router
readiness. Only successful preflight invokes `Router.Run`; an enrollment
failure never starts Watermill's handler wait-group or startup watcher. An
internal prepared-subscriber adapter passes those already-verified channels to
the Router while registrations remain canonical `*Subscriber` values.
Subscriber and Router failures are forwarded through the process fatal
channel. Shutdown first closes canonical subscribers and the Router, then waits
for the runtime execution tracker (including `Complete`) before returning.
`RouterConfig.CloseTimeout` is an alert/soft bound: if it expires while a user
handler is still running, safe component `Stop` continues waiting for execution
ownership. User handlers are required to honor their context so this final
drain can complete before PostgreSQL pool teardown.

Registrations accept only the canonical PostgreSQL-backed Subscriber and
Watermill's no-publisher consumer handler type. A concrete registration,
stable consumer identity, or aggregate filter is created only for an approved
idempotent consuming effect; the existence of a topic or producer alone does
not justify a placeholder consumer that would block retention.

The following are prohibited in the canonical path:

- stock `watermill-sql` production publisher/subscriber transport, transport
  offsets, or schema initialization;
- `PoisonQueue` or a second poison topic;
- `InstantAck` and `IgnoreErrors`;
- metadata-mutating `CorrelationID` or `Delay` middleware (canonical
  correlation is envelope data, and delay/retry state is PostgreSQL state);
- a second product-owned router or an unbounded translation queue.

## Bounded operation and failure semantics

`max_in_flight` is a positive, bounded per-subscriber/router limit. Poll,
retry/backoff, handler, acknowledgement, claim-lease, recovery, and shutdown
deadlines are explicit and positive, with bounded batch/resend values. The
configuration records an inequality such as

`0 < handler_deadline < acknowledgement_deadline < claim_lease - recovery_margin`,

where `recovery_margin` covers commit, scheduling, and clock-skew grace. A
claim lease that cannot outlive the acknowledgement and recovery window is
invalid. No zero-value deadline may silently disable these protections.

At-least-once delivery is expected. Bounded attempts that cannot succeed enter
a visible dead-letter terminal state in `event_delivery`; that state remains
retention-blocking until an authorized resolution or audited waiver. An exact
single-delivery replay is by canonical consumer and event identity, requires
non-empty operator evidence, and takes the fan-out registry plus consumer
lifecycle fences before changing a terminal row to `pending`. That pending row
is the exact replay's retention authority until a fresh claim resolves it;
creating a second range root for the same row would add redundant lifecycle
state. Enrollment or any future range replay instead uses a persisted replay
root until its bounded backfill completes. Pruning `event_log` or
`event_delivery` is forbidden while any applicable delivery, range replay root,
or unresolved dead letter remains.

Handlers keep transactions short. Work that may exceed the handler deadline is
admitted as a River job and acknowledged only after that admission transaction
commits; it is not executed as a long-running Watermill handler.

Pagestream remains the browser SSE transport. Watermill events may wake or
reconcile application state, but per-client Pagestream patches never travel
through PostgreSQL event delivery or the Router.

## Observability and conformance

Watermill Prometheus and `slog` instrumentation covers Router, subscriber,
handler, Ack/Nack, failure, timeout, and latency. LeapView metrics additionally
expose backlog, in-flight count, claim/lease age, fence rejects, lease
recoveries, attempts, dead letters, replay roots, and retention-floor blockers.

Conformance must demonstrate claim-before-emit and no transaction-through-handler,
exact worker/generation fencing, fresh claim after Nack, Complete-commit-before-
Ack, lost-process lease recovery, bounded in-flight/deadlines, dead-letter
retention blocking, replay identity, shutdown drain, and absence of every
prohibited Watermill transport/middleware path. A lane not run is unsupported;
implementation status belongs in Linear.
