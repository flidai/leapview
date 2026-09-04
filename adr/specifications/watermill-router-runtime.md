# FAI-593 Watermill Router runtime — superseded

Status: superseded by FAI-720 and FAI-721.

No production asynchronous consumer owns this runtime, so the Router,
Subscriber, claim/retry/dead-letter/replay machinery, listener, metrics, and
operator lifecycle were removed. Pagestream remains the browser/SSE transport.

If a named asynchronous effect is admitted later, its throughput, ordering,
recovery, retention, and cross-service requirements determine whether a small
PostgreSQL worker or an external broker is appropriate.
