-- Durable event-log leaf queries. Repository code retains validation,
-- domain mapping, caller-owned transactions, and replay orchestration.

-- name: CurrentTransactionIsolation :one
SELECT current_setting('transaction_isolation')::text AS transaction_isolation;

-- name: LockEventIdentity :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(event_id), 0));

-- name: GetEventByID :one
SELECT event_id::text, scope_id, aggregate_type, aggregate_id, aggregate_version,
       event_type, schema_version, occurred_at,
       COALESCE(correlation_id::text, ''::text)::text AS correlation_id, payload::text
FROM event.event_log
WHERE event_id = sqlc.arg(event_id)::uuid;

-- name: EventPayloadEqual :one
SELECT e.payload = sqlc.arg(payload)::jsonb AS equal
FROM event.event_log AS e
WHERE e.event_id = sqlc.arg(event_id)::uuid;

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

-- name: LockFanoutRegistryForKeyShare :one
SELECT registry_id FROM event.event_fanout_registry
WHERE registry_id = true FOR KEY SHARE;

-- name: LockFanoutRegistryForUpdate :one
SELECT registry_id FROM event.event_fanout_registry
WHERE registry_id = true FOR UPDATE;

-- name: ListFanoutConsumers :many
SELECT c.consumer_id::text
FROM event.event_consumer AS c
WHERE c.lifecycle IN ('backfilling', 'enabled', 'paused')
  AND EXISTS (
      SELECT 1
      FROM event.event_consumer_aggregate AS a
      WHERE a.consumer_id = c.consumer_id
        AND a.aggregate_type = sqlc.arg(aggregate_type)
  )
ORDER BY c.consumer_id;

-- name: InsertEventDelivery :exec
INSERT INTO event.event_delivery (consumer_id, event_id, status, available_at)
VALUES (sqlc.arg(consumer_id)::uuid, sqlc.arg(event_id)::uuid, 'pending', clock_timestamp())
ON CONFLICT (consumer_id, event_id) DO NOTHING;

-- name: NotifyEvent :exec
SELECT pg_notify(sqlc.arg(channel), sqlc.arg(payload));

-- name: CountActiveConsumers :one
SELECT count(*) FROM event.event_consumer WHERE lifecycle <> 'retired';

-- name: GetRetentionFloorForShare :one
SELECT floor_at FROM event.event_retention_floor WHERE singleton = true FOR SHARE;

-- name: GetLatestEventBoundary :one
SELECT occurred_at, event_id::text
FROM event.event_log
ORDER BY occurred_at DESC, event_id DESC
LIMIT 1;

-- name: InsertConsumer :exec
INSERT INTO event.event_consumer
    (consumer_id, consumer_key, lifecycle, replay_from, metadata)
VALUES (sqlc.arg(consumer_id)::uuid, sqlc.arg(consumer_key), 'backfilling',
        sqlc.arg(replay_from), sqlc.arg(metadata)::jsonb);

-- name: InsertConsumerAggregate :exec
INSERT INTO event.event_consumer_aggregate (consumer_id, aggregate_type)
VALUES (sqlc.arg(consumer_id)::uuid, sqlc.arg(aggregate_type));

-- name: InsertRetentionRoot :exec
INSERT INTO event.event_retention_root
    (root_id, consumer_id, replay_from, replay_until, replay_until_event_id, state, evidence)
VALUES (sqlc.arg(root_id)::uuid, sqlc.arg(consumer_id)::uuid, sqlc.arg(replay_from),
        sqlc.arg(replay_until), sqlc.narg(replay_until_event_id)::uuid,
        'live', sqlc.arg(evidence)::jsonb);

-- name: GetConsumerLifecycleForUpdate :one
SELECT lifecycle FROM event.event_consumer
WHERE consumer_id = sqlc.arg(consumer_id)::uuid FOR UPDATE;

-- name: GetRetentionRootForUpdate :one
SELECT root_id::text, replay_from
FROM event.event_retention_root
WHERE consumer_id = sqlc.arg(consumer_id)::uuid AND state = 'live'
ORDER BY created_at DESC LIMIT 1 FOR UPDATE;

-- name: GetRetentionReplayCursor :one
SELECT replay_until, COALESCE(replay_until_event_id::text, ''::text)::text AS replay_until_event_id,
       frontier_occurred_at, COALESCE(frontier_event_id::text, ''::text)::text AS frontier_event_id
FROM event.event_retention_root
WHERE root_id = sqlc.arg(root_id)::uuid;

-- name: ListBackfillEvents :many
SELECT event_id::text, occurred_at
FROM event.event_log
WHERE occurred_at >= sqlc.arg(replay_from)
  AND (sqlc.arg(has_until)::boolean = false
       OR occurred_at < sqlc.arg(replay_until)
       OR (occurred_at = sqlc.arg(replay_until)
           AND event_id <= sqlc.narg(replay_until_event_id)::uuid))
  AND (sqlc.arg(has_frontier)::boolean = false
       OR occurred_at > sqlc.arg(frontier_at)
       OR (occurred_at = sqlc.arg(frontier_at)
           AND event_id > sqlc.narg(frontier_event_id)::uuid))
  AND EXISTS (
      SELECT 1
      FROM event.event_consumer_aggregate AS a
      WHERE a.consumer_id = sqlc.arg(consumer_id)::uuid
        AND a.aggregate_type = event.event_log.aggregate_type
  )
ORDER BY occurred_at, event_id
LIMIT sqlc.arg(p_limit);

-- name: InsertBackfillDelivery :execresult
INSERT INTO event.event_delivery (consumer_id, event_id, status, available_at)
VALUES (sqlc.arg(consumer_id)::uuid, sqlc.arg(event_id)::uuid, 'pending', clock_timestamp())
ON CONFLICT (consumer_id, event_id) DO NOTHING;

-- name: AdvanceConsumerFrontier :exec
UPDATE event.event_consumer
SET frontier_event_id = sqlc.arg(event_id)::uuid,
    frontier_occurred_at = sqlc.arg(occurred_at), updated_at = clock_timestamp()
WHERE consumer_id = sqlc.arg(consumer_id)::uuid;

-- name: AdvanceRetentionFrontier :exec
UPDATE event.event_retention_root
SET frontier_event_id = sqlc.arg(event_id)::uuid, frontier_occurred_at = sqlc.arg(occurred_at)
WHERE root_id = sqlc.arg(root_id)::uuid;

-- name: EnableConsumer :exec
UPDATE event.event_consumer SET lifecycle = 'enabled', updated_at = clock_timestamp()
WHERE consumer_id = sqlc.arg(consumer_id)::uuid;

-- name: ExpireRetentionRoot :exec
UPDATE event.event_retention_root SET state = 'expired'
WHERE root_id = sqlc.arg(root_id)::uuid;

-- name: CountUnresolvedDeliveries :one
SELECT count(*) FROM event.event_delivery
WHERE consumer_id = sqlc.arg(consumer_id)::uuid
  AND status IN ('pending', 'claimed', 'dead_letter');

-- name: WaiveDeliveries :exec
UPDATE event.event_delivery
SET status = 'waived', terminal_at = clock_timestamp(), claimed_by = NULL,
    claimed_until = NULL, evidence = sqlc.arg(evidence)::jsonb
WHERE consumer_id = sqlc.arg(consumer_id)::uuid
  AND status IN ('pending', 'claimed', 'dead_letter');

-- name: RetireConsumer :exec
UPDATE event.event_consumer
SET lifecycle = 'retired', updated_at = clock_timestamp()
WHERE consumer_id = sqlc.arg(consumer_id)::uuid;

-- name: ExpireConsumerRoots :exec
UPDATE event.event_retention_root
SET state = 'expired', evidence = sqlc.arg(evidence)::jsonb
WHERE consumer_id = sqlc.arg(consumer_id)::uuid AND state <> 'expired';

-- name: SetConsumerLifecycle :execresult
UPDATE event.event_consumer
SET lifecycle = sqlc.arg(lifecycle), updated_at = clock_timestamp()
WHERE consumer_id = sqlc.arg(consumer_id)::uuid
  AND lifecycle IN ('enabled', 'paused');

-- name: GetConsumerLifecycleForShare :one
SELECT lifecycle FROM event.event_consumer
WHERE consumer_id = sqlc.arg(consumer_id)::uuid FOR SHARE;

-- name: GetConsumerByID :one
SELECT consumer_id::text, consumer_key, lifecycle, replay_from,
       COALESCE(frontier_event_id::text, ''::text)::text AS frontier_event_id,
       frontier_occurred_at, metadata::text, created_at, updated_at
FROM event.event_consumer
WHERE consumer_id = sqlc.arg(consumer_id)::uuid
FOR SHARE;

-- name: ListConsumerAggregates :many
SELECT aggregate_type
FROM event.event_consumer_aggregate
WHERE consumer_id = sqlc.arg(consumer_id)::uuid
ORDER BY aggregate_type;

-- name: ClaimDeliveries :many
WITH candidates AS (
    SELECT consumer_id, event_id
    FROM event.event_delivery
    WHERE consumer_id = sqlc.arg(consumer_id)::uuid
      AND (status = 'pending' OR (status = 'claimed' AND claimed_until < clock_timestamp()))
      AND available_at <= clock_timestamp()
    ORDER BY available_at, event_id
    FOR UPDATE SKIP LOCKED
    LIMIT sqlc.arg(p_limit)
)
UPDATE event.event_delivery d
SET status = 'claimed', attempts = d.attempts + 1,
    claim_generation = d.claim_generation + 1,
    claimed_by = sqlc.arg(worker_id),
    claimed_until = clock_timestamp() + (sqlc.arg(lease_micros)::bigint * interval '1 microsecond'),
    terminal_at = NULL
FROM candidates c
WHERE d.consumer_id = c.consumer_id AND d.event_id = c.event_id
RETURNING d.event_id::text, d.status, d.attempts, d.claim_generation,
          d.available_at, d.claimed_by, d.claimed_until,
          d.terminal_at, d.evidence::text;

-- name: CompleteDelivery :execresult
UPDATE event.event_delivery
SET status = sqlc.arg(status), terminal_at = clock_timestamp(), claimed_by = NULL,
    claimed_until = NULL, evidence = sqlc.arg(evidence)::jsonb
WHERE consumer_id = sqlc.arg(consumer_id)::uuid AND event_id = sqlc.arg(event_id)::uuid
  AND status = 'claimed' AND claimed_by = sqlc.arg(worker_id)
  AND claim_generation = sqlc.arg(claim_generation) AND claimed_until > clock_timestamp();

-- name: ReplayDelivery :execresult
UPDATE event.event_delivery
SET status = 'pending', available_at = clock_timestamp(), claimed_by = NULL,
    claimed_until = NULL, terminal_at = NULL, evidence = sqlc.arg(evidence)::jsonb
WHERE consumer_id = sqlc.arg(consumer_id)::uuid AND event_id = sqlc.arg(event_id)::uuid
  AND status IN ('succeeded', 'dead_letter', 'waived');

-- name: RetryDelivery :execresult
UPDATE event.event_delivery
SET status = CASE WHEN attempts >= sqlc.arg(max_attempts) THEN 'dead_letter' ELSE 'pending' END,
    available_at = CASE WHEN attempts >= sqlc.arg(max_attempts) THEN available_at
                        ELSE clock_timestamp() + (sqlc.arg(delay_micros)::bigint * interval '1 microsecond') END,
    terminal_at = CASE WHEN attempts >= sqlc.arg(max_attempts) THEN clock_timestamp() ELSE NULL END,
    claimed_by = NULL, claimed_until = NULL, evidence = sqlc.arg(evidence)::jsonb
WHERE consumer_id = sqlc.arg(consumer_id)::uuid AND event_id = sqlc.arg(event_id)::uuid
  AND status = 'claimed' AND claimed_by = sqlc.arg(worker_id)
  AND claim_generation = sqlc.arg(claim_generation) AND claimed_until > clock_timestamp();

-- name: PruneEventLog :one
SELECT event.prune_event_log(sqlc.arg(before), sqlc.arg(batch));

-- name: GetRetentionFloor :one
SELECT floor_at FROM event.event_retention_floor WHERE singleton = true;

-- name: ListDeliveries :many
SELECT event_id::text, status, attempts, claim_generation, available_at,
       claimed_by, claimed_until, terminal_at, evidence::text
FROM event.event_delivery
WHERE consumer_id = sqlc.arg(consumer_id)::uuid
ORDER BY available_at, event_id
LIMIT sqlc.arg(p_limit);
