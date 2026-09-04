-- Canonical transactional event-log capability.
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
    -- PostgreSQL 18's uuid_extract_version primitive keeps direct SQL writes
    -- inside the event authority's UUIDv7 identity contract. COALESCE also
    -- rejects UUIDs with a non-RFC-9562 variant, for which the extractor
    -- returns NULL. Canonical lower-case spelling is enforced by the Go
    -- repository before the uuid value is handed to PostgreSQL.
    CONSTRAINT event_log_event_id_uuidv7_ck CHECK (COALESCE(uuid_extract_version(event_id), 0) = 7),
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

CREATE INDEX IF NOT EXISTS event_log_scope_order_idx
    ON event.event_log (scope_id, occurred_at, event_id);
CREATE INDEX IF NOT EXISTS event_log_replay_order_idx
    ON event.event_log (occurred_at, event_id);
-- Event rows are immutable to the application runtime but remain eligible for
-- bounded retention. This owner-executed function is the only delete surface.
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
    v_target timestamptz;
    v_removed bigint;
BEGIN
    IF p_before IS NULL THEN
        RAISE EXCEPTION 'event prune cutoff is required';
    END IF;
    IF p_batch_limit < 1 OR p_batch_limit > 1000 THEN
        RAISE EXCEPTION 'event prune batch limit must be between 1 and 1000';
    END IF;

    -- Retention follows the authoritative database clock; a caller cannot
    -- delete future records with a forged cutoff.
    v_target := LEAST(p_before, clock_timestamp());

    WITH doomed AS (
        SELECT e.event_id
        FROM event.event_log e
        WHERE e.occurred_at < v_target
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
