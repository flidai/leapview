-- Durable event/fan-out capability (FAI-561).
--
-- The PostgreSQL control-plane baseline owns the event tables. This file is
-- the complete final capability definition and is safe to apply to an
-- isolated conformance database.
CREATE SCHEMA IF NOT EXISTS event;

-- Capability schemas are deny-by-default.  The control-plane migration grants
-- the narrowly scoped runtime/readonly privileges after applying this file;
-- these revokes also protect isolated deployments that apply the capability
-- schema directly.
REVOKE ALL ON SCHEMA event FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA event FROM PUBLIC;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA event FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA event FROM PUBLIC;

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

-- Event rows are immutable to the application runtime but remain eligible for
-- bounded retention.  This owner-executed function is the only delete surface:
-- it advances the durable floor and checks replay roots plus unresolved
-- deliveries in the same transaction before deleting one bounded batch.
CREATE OR REPLACE FUNCTION event.prune_event_log(
    p_before timestamptz,
    p_batch_limit integer
)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, event
AS $$
DECLARE
    v_floor timestamptz;
    v_target timestamptz;
    v_oldest_root timestamptz;
    v_blocked timestamptz;
    v_removed bigint;
BEGIN
    IF p_before IS NULL THEN
        RAISE EXCEPTION 'event prune cutoff is required';
    END IF;
    IF p_batch_limit < 1 OR p_batch_limit > 1000 THEN
        RAISE EXCEPTION 'event prune batch limit must be between 1 and 1000';
    END IF;

    SELECT floor_at INTO STRICT v_floor
    FROM event.event_retention_floor
    WHERE singleton = true
    FOR UPDATE;

    -- Retention follows the authoritative database clock; a caller cannot
    -- advance the floor into the future with a forged cutoff.
    v_target := LEAST(p_before, clock_timestamp());
    SELECT min(replay_from) INTO v_oldest_root
    FROM event.event_retention_root
    WHERE state <> 'expired';
    IF v_oldest_root IS NOT NULL AND v_oldest_root < v_target THEN
        v_target := v_oldest_root;
    END IF;

    SELECT min(e.occurred_at) INTO v_blocked
    FROM event.event_log e
    WHERE e.occurred_at < v_target
      AND NOT EXISTS (
          SELECT 1 FROM event.event_retention_root r
          WHERE r.state <> 'expired'
            AND e.occurred_at >= r.replay_from
            AND (r.replay_until IS NULL OR e.occurred_at <= r.replay_until)
      )
      AND EXISTS (
          SELECT 1 FROM event.event_delivery d
          WHERE d.event_id = e.event_id
            AND d.status IN ('pending', 'claimed', 'dead_letter')
      );
    IF v_blocked IS NOT NULL AND v_blocked < v_target THEN
        v_target := v_blocked;
    END IF;

    IF v_target > v_floor THEN
        UPDATE event.event_retention_floor
        SET floor_at = v_target, updated_at = clock_timestamp()
        WHERE singleton = true;
    END IF;

    WITH doomed AS (
        SELECT e.event_id
        FROM event.event_log e
        WHERE e.occurred_at < v_target
          AND NOT EXISTS (
              SELECT 1 FROM event.event_retention_root r
              WHERE r.state <> 'expired'
                AND e.occurred_at >= r.replay_from
                AND (r.replay_until IS NULL OR e.occurred_at <= r.replay_until)
          )
          AND NOT EXISTS (
              SELECT 1 FROM event.event_delivery d
              WHERE d.event_id = e.event_id
                AND d.status IN ('pending', 'claimed', 'dead_letter')
          )
        ORDER BY e.occurred_at, e.event_id
        LIMIT p_batch_limit
    )
    DELETE FROM event.event_log e
    USING doomed
    WHERE e.event_id = doomed.event_id;
    GET DIAGNOSTICS v_removed = ROW_COUNT;
    RETURN v_removed;
END;
$$;
REVOKE ALL ON FUNCTION event.prune_event_log(timestamptz, integer) FROM PUBLIC;

CREATE OR REPLACE FUNCTION event.reject_event_update()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    RAISE EXCEPTION 'durable event log is immutable';
END;
$$;
REVOKE ALL ON FUNCTION event.reject_event_update() FROM PUBLIC;

-- occurred_at is authority-owned rather than caller supplied.  Keeping this
-- invariant in the database protects direct INSERT paths (including a role
-- whose INSERT privilege is intentionally narrow) as well as Repository.
CREATE OR REPLACE FUNCTION event.set_event_occurred_at()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    NEW.occurred_at := clock_timestamp();
    RETURN NEW;
END;
$$;
REVOKE ALL ON FUNCTION event.set_event_occurred_at() FROM PUBLIC;
DROP TRIGGER IF EXISTS event_log_occurred_at_owned ON event.event_log;
CREATE TRIGGER event_log_occurred_at_owned
    BEFORE INSERT ON event.event_log
    FOR EACH ROW EXECUTE FUNCTION event.set_event_occurred_at();

DROP TRIGGER IF EXISTS event_log_immutable ON event.event_log;
CREATE TRIGGER event_log_immutable
    BEFORE UPDATE ON event.event_log
    FOR EACH ROW EXECUTE FUNCTION event.reject_event_update();

-- Retention is an operational capability, not request-serving authority.
-- Runtime can append and process events through its table grants, but only a
-- separately authenticated maintenance process may advance the retention
-- floor and remove fully satisfied history.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        REVOKE EXECUTE ON FUNCTION event.prune_event_log(timestamptz, integer) FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA event TO leapview_control_maintenance;
        GRANT EXECUTE ON FUNCTION event.prune_event_log(timestamptz, integer) TO leapview_control_maintenance;
    END IF;
END
$$;
