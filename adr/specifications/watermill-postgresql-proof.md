# FAI-591 Watermill PostgreSQL qualification — superseded

Status: superseded by FAI-720 and ADR-0020.

The qualification established that the stock Watermill SQL transport could not
be LeapView's canonical transactional event authority. No real asynchronous
consumer was subsequently admitted, so the proof package and both Watermill
dependencies were removed.

The retained contract is the direct caller-owned PostgreSQL event append:
UUIDv7 identity, aggregate ordering, exact producer-retry idempotency, bounded
payload validation, immutable history, and database integrity. Future
transport selection begins from a named consumer's requirements and does not
inherit this superseded package choice.
