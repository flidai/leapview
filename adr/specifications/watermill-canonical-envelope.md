# FAI-592 canonical Watermill envelope

Status: envelope and canonical producer boundary admitted; consumer enrollment conditional

Date: 2026-08-31

Related: [ADR-0016](../0016-adopt-a-postgresql-centered-target-data-architecture.md)

## Decision

The canonical PostgreSQL event repository is the only producer authority. A
producer supplies the exact caller-owned transaction to the Watermill boundary,
which appends one event and, only for explicitly admitted consumers, their
delivery rows. The stored event is projected deterministically into a
Watermill message; no Watermill SQL table, generic transport offset, second
publish, or pre-commit dispatch exists.

Production consumer enrollment is conditional on a concrete product-owned,
bounded, idempotent effect. No real consumer is admitted for the current
PostgreSQL target release. Do not invent a placeholder read model or export to
exercise the adapter. Owner projections remain synchronous in the source
transaction; the Router/subscriber runtime and its readiness/operations burden
begin only after a consumer admission.

Watermill's `message.Publisher` is deliberately not used for this write. Its
interface has no transaction parameter, while PostgreSQL finalizes the UUIDv7
event identity, aggregate version, occurrence time, and JSONB payload inside the
transaction. The Watermill Router starts from the subscriber side after a
canonical delivery is claimable.

## Envelope contract

- Four allowlisted topics group agent, dashboard, delivery, and release events;
  topic vocabulary does not itself enroll a consumer.
- `message.UUID` is the canonical lowercase UUIDv7 event identity.
- Envelope version, scope, aggregate identity and version, event type and
  schema version, UTC occurrence time, optional canonical correlation UUID, and
  the bounded JSON object are required and strictly decoded.
- The sole metadata key is the allowlisted topic. Domain, authorization, and
  payload data never enter Watermill metadata.
- PostgreSQL's stored `jsonb` text is the payload projection. Numeric-equivalent
  retries are idempotent and initial, replayed, and subsequently read messages
  therefore use byte-identical payloads.

## Evidence

`internal/platform/events/watermill` proves input preflight before append,
exactly one authority call, deterministic encoding/decoding, topic and metadata
rejection, size bounds, rollback atomicity, UUIDv7 identity, idempotent replay,
conflict detection, canonical fan-out, and absence of `watermill_*` tables on
PostgreSQL 18. The producer constructor accepts only the concrete canonical
event repository. Subscriber/router conformance is qualification-only until a
concrete effect is admitted.

The PostgreSQL event authority independently enforces UUIDv7 at the repository
and table boundaries and returns the stored JSONB representation. This prevents
direct canonical rows that cannot be presented to Watermill and prevents the
producer and subscriber from observing different envelope bytes.

`jobs.event` and the Agent-local event history are not alternate Watermill
transports. They are capability-owned workflow/progress and product read
histories with separate API cursor contracts; Watermill never subscribes to
them and they cannot acknowledge or retain a canonical delivery. FAI-594 owns
the exact history inventory and removal of any record that proves to be a true
duplicate rather than a distinct product projection.

## Remaining work

FAI-592 remains open until every canonical deployment, release, dashboard,
agent, and approval producer uses this boundary and its mutation/event rollback
test passes. FAI-593 owns adapter conformance over fenced delivery claims.
Lost-ack redelivery, retry, dead-letter, replay, retention, shutdown,
multi-node/lag, restore, and runbook gates become required only when a concrete
consumer is admitted; they are not blockers for the current PostgreSQL target
release.
