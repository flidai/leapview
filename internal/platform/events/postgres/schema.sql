-- Durable event/fan-out capability (FAI-561).
--
-- The PostgreSQL control-plane baseline owns the event tables. This file is
-- the complete final capability definition and is safe to apply to an
-- isolated conformance database.
CREATE SCHEMA IF NOT EXISTS event;

-- These CREATE TABLE statements are intentionally IF NOT EXISTS: the clean
-- control-plane baseline normally creates them first, while capability tests
-- can apply this file on their own.
CREATE TABLE IF NOT EXISTS event.event_log (
    event_id uuid PRIMARY KEY,
    scope_id text NOT NULL,
    aggregate_type text NOT NULL,
    aggregate_id text NOT NULL,
    aggregate_version bigint NOT NULL CHECK (aggregate_version > 0),
    event_type text NOT NULL,
    schema_version bigint NOT NULL CHECK (schema_version > 0),
    occurred_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    correlation_id uuid,
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object' AND octet_length(payload::text) <= 65536),
    CHECK (octet_length(scope_id) BETWEEN 1 AND 255),
    CHECK (octet_length(aggregate_type) BETWEEN 1 AND 255),
    CHECK (octet_length(aggregate_id) BETWEEN 1 AND 255),
    CHECK (octet_length(event_type) BETWEEN 1 AND 255),
    UNIQUE (scope_id, aggregate_type, aggregate_id, aggregate_version)
);

CREATE TABLE IF NOT EXISTS event.event_aggregate (
    scope_id text NOT NULL,
    aggregate_type text NOT NULL,
    aggregate_id text NOT NULL,
    next_version bigint NOT NULL DEFAULT 1 CHECK (next_version > 0),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (octet_length(scope_id) BETWEEN 1 AND 255),
    CHECK (octet_length(aggregate_type) BETWEEN 1 AND 255),
    CHECK (octet_length(aggregate_id) BETWEEN 1 AND 255),
    PRIMARY KEY (scope_id, aggregate_type, aggregate_id)
);

CREATE TABLE IF NOT EXISTS event.event_fanout_registry (
    registry_id boolean PRIMARY KEY DEFAULT true CHECK (registry_id),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
INSERT INTO event.event_fanout_registry (registry_id) VALUES (true)
ON CONFLICT (registry_id) DO NOTHING;

CREATE TABLE IF NOT EXISTS event.event_consumer (
    consumer_id uuid PRIMARY KEY,
    consumer_key text NOT NULL UNIQUE,
    lifecycle text NOT NULL CHECK (lifecycle IN ('backfilling', 'enabled', 'paused', 'retired')),
    replay_from timestamptz NOT NULL,
    frontier_event_id uuid,
    frontier_occurred_at timestamptz,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object' AND octet_length(metadata::text) <= 16384),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (octet_length(consumer_key) BETWEEN 1 AND 255)
);

CREATE TABLE IF NOT EXISTS event.event_delivery (
    consumer_id uuid NOT NULL REFERENCES event.event_consumer(consumer_id) ON DELETE CASCADE,
    event_id uuid NOT NULL REFERENCES event.event_log(event_id) ON DELETE CASCADE,
    status text NOT NULL CHECK (status IN ('pending', 'claimed', 'succeeded', 'dead_letter', 'waived')),
    attempts bigint NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    claimed_by text,
    claim_generation bigint NOT NULL DEFAULT 0 CHECK (claim_generation >= 0),
    claimed_until timestamptz,
    terminal_at timestamptz,
    evidence jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 32768),
    CHECK ((status = 'claimed') = (claimed_by IS NOT NULL AND claimed_until IS NOT NULL)),
    CHECK ((status IN ('succeeded', 'dead_letter', 'waived')) = (terminal_at IS NOT NULL)),
    CHECK (claimed_by IS NULL OR octet_length(claimed_by) BETWEEN 1 AND 255),
    PRIMARY KEY (consumer_id, event_id)
);

CREATE TABLE IF NOT EXISTS event.event_retention_root (
    root_id uuid PRIMARY KEY,
    consumer_id uuid REFERENCES event.event_consumer(consumer_id) ON DELETE CASCADE,
    replay_from timestamptz NOT NULL,
    replay_until timestamptz,
    replay_until_event_id uuid,
    state text NOT NULL CHECK (state IN ('live', 'retiring', 'expired')),
    frontier_event_id uuid,
    frontier_occurred_at timestamptz,
    evidence jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 16384),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX IF NOT EXISTS event_log_scope_order_idx
    ON event.event_log (scope_id, occurred_at, event_id);
CREATE INDEX IF NOT EXISTS event_log_replay_order_idx
    ON event.event_log (occurred_at, event_id);
CREATE INDEX IF NOT EXISTS event_delivery_claim_idx
    ON event.event_delivery (consumer_id, status, available_at, event_id);
CREATE INDEX IF NOT EXISTS event_retention_root_live_idx
    ON event.event_retention_root (replay_from, replay_until)
    WHERE state <> 'expired';

-- The floor is a durable operational cursor.  It may advance only as far as
-- the oldest live replay root and never substitutes for delivery completion.
CREATE TABLE IF NOT EXISTS event.event_retention_floor (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    floor_at timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00'::timestamptz,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
INSERT INTO event.event_retention_floor (singleton) VALUES (true)
ON CONFLICT (singleton) DO NOTHING;

CREATE OR REPLACE FUNCTION event.reject_event_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'durable event log is immutable';
END;
$$;
DROP TRIGGER IF EXISTS event_log_immutable ON event.event_log;
CREATE TRIGGER event_log_immutable
    BEFORE UPDATE ON event.event_log
    FOR EACH ROW EXECUTE FUNCTION event.reject_event_update();
