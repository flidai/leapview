-- Canonical event-log queries. The repository executes these statements
-- directly so event writes stay on the caller-owned transaction; no consumer,
-- delivery, claim, replay, or dead-letter query surface is exposed.

-- name: LockEventIdentity :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(event_id), 0));

-- name: GetEventByID :one
SELECT event_id::text, scope_id, aggregate_type, aggregate_id, aggregate_version,
       event_type, schema_version, occurred_at,
       COALESCE(correlation_id::text, ''::text)::text AS correlation_id, payload::text
FROM event.event_log
WHERE event_id = sqlc.arg(event_id)::uuid;

-- name: EnsureEventAggregate :exec
INSERT INTO event.event_aggregate (scope_id, aggregate_type, aggregate_id, next_version)
VALUES (sqlc.arg(scope_id), sqlc.arg(aggregate_type), sqlc.arg(aggregate_id), 1)
ON CONFLICT (scope_id, aggregate_type, aggregate_id) DO NOTHING;

-- name: AllocateAggregateVersion :one
UPDATE event.event_aggregate
SET next_version = next_version + 1, updated_at = clock_timestamp()
WHERE scope_id = sqlc.arg(scope_id) AND aggregate_type = sqlc.arg(aggregate_type)
  AND aggregate_id = sqlc.arg(aggregate_id)
RETURNING (next_version - 1)::bigint AS version;

-- name: InsertEvent :one
INSERT INTO event.event_log
    (event_id, scope_id, aggregate_type, aggregate_id, aggregate_version,
     event_type, schema_version, occurred_at, correlation_id, payload)
VALUES (sqlc.arg(event_id)::uuid, sqlc.arg(scope_id), sqlc.arg(aggregate_type),
        sqlc.arg(aggregate_id), sqlc.arg(aggregate_version), sqlc.arg(event_type),
        sqlc.arg(schema_version), clock_timestamp(), sqlc.narg(correlation_id)::uuid,
        sqlc.arg(payload)::jsonb)
RETURNING occurred_at, payload::text;

-- name: PruneEventLog :one
SELECT event.prune_event_log(sqlc.arg(before), sqlc.arg(batch));
