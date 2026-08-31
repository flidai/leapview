# FAI-592 canonical Watermill envelope

Status: envelope and canonical producer boundary admitted; producer migration in progress

Date: 2026-08-31

Related: [ADR-0016](../0016-adopt-a-postgresql-centered-target-data-architecture.md)

## Decision

The canonical PostgreSQL event repository is the only producer authority. A
producer supplies the exact caller-owned transaction to the Watermill boundary,
which appends one event and the enrolled consumers' delivery rows. The stored
event is projected deterministically into a Watermill message; no Watermill SQL
table, generic transport offset, second publish, or pre-commit dispatch exists.

Watermill's `message.Publisher` is deliberately not used for this write. Its
interface has no transaction parameter, while PostgreSQL finalizes the UUIDv7
event identity, aggregate version, occurrence time, and JSONB payload inside the
transaction. The Watermill Router starts from the subscriber side after a
canonical delivery is claimable.

## Envelope contract

- Four allowlisted topics group agent, dashboard, delivery, and release events.
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
PostgreSQL 18. The production constructor accepts only the concrete canonical
event repository.

The PostgreSQL event authority independently enforces UUIDv7 at the repository
and table boundaries and returns the stored JSONB representation. This prevents
direct canonical rows that cannot be presented to Watermill and prevents the
producer and subscriber from observing different envelope bytes.

## Remaining work

FAI-592 remains open until every canonical deployment, release, dashboard,
agent, and approval producer uses this boundary and its mutation/event rollback
test passes. FAI-593 owns the Watermill Subscriber and Router path over fenced
delivery claims, including acknowledgement, lost-ack redelivery, retry,
dead-letter, replay, retention, shutdown, and observability conformance.
