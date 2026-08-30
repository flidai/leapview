-- Query-audit capability schema (ADR-0016).
--
-- The control-plane baseline owns the audit schema in production.  This file
-- is also complete for an isolated conformance database and intentionally has
-- no transaction control or compatibility DDL.
CREATE SCHEMA IF NOT EXISTS audit;

CREATE TABLE IF NOT EXISTS audit.query_event (
    event_id            uuid PRIMARY KEY,
    retry_identity      text NOT NULL UNIQUE,
    project_id          text NOT NULL,
    principal_id        text NOT NULL,
    surface             text NOT NULL DEFAULT '',
    operation           text NOT NULL DEFAULT '',
    query_kind          text NOT NULL DEFAULT '',
    model_id            text NOT NULL DEFAULT '',
    target              text NOT NULL DEFAULT '',
    object_type         text NOT NULL DEFAULT '',
    object_id           text NOT NULL DEFAULT '',
    request_id          text NOT NULL DEFAULT '',
    correlation_id      text NOT NULL DEFAULT '',
    status              text NOT NULL DEFAULT '',
    duration_ms         bigint NOT NULL DEFAULT 0,
    queue_wait_ms       bigint NOT NULL DEFAULT 0,
    planning_ms         bigint NOT NULL DEFAULT 0,
    connection_wait_ms  bigint NOT NULL DEFAULT 0,
    database_ms         bigint NOT NULL DEFAULT 0,
    execution_ms        bigint NOT NULL DEFAULT 0,
    execution_state     text NOT NULL DEFAULT '',
    rows_returned       bigint NOT NULL DEFAULT 0,
    bytes_estimate      bigint NOT NULL DEFAULT 0,
    error               text NOT NULL DEFAULT '',
    sql_text            text NOT NULL DEFAULT '',
    plan_text           text NOT NULL DEFAULT '',
    query_json          jsonb NOT NULL DEFAULT '{}'::jsonb,
    search_document     tsvector NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (retry_identity = btrim(retry_identity) AND octet_length(retry_identity) BETWEEN 1 AND 512),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255),
    CHECK (principal_id = btrim(principal_id) AND octet_length(principal_id) BETWEEN 1 AND 255),
    CHECK (octet_length(surface) <= 128),
    CHECK (octet_length(operation) <= 256),
    CHECK (octet_length(query_kind) <= 128),
    CHECK (octet_length(model_id) <= 255),
    CHECK (octet_length(target) <= 512),
    CHECK (octet_length(object_type) <= 128),
    CHECK (octet_length(object_id) <= 512),
    CHECK (octet_length(request_id) <= 512),
    CHECK (octet_length(correlation_id) <= 512),
    CHECK (octet_length(status) <= 64),
    CHECK (duration_ms >= 0 AND queue_wait_ms >= 0 AND planning_ms >= 0
        AND connection_wait_ms >= 0 AND database_ms >= 0 AND execution_ms >= 0),
    CHECK (octet_length(execution_state) <= 64),
    CHECK (rows_returned >= 0 AND bytes_estimate >= 0),
    CHECK (octet_length(error) <= 16384),
    CHECK (octet_length(sql_text) <= 65536),
    CHECK (octet_length(plan_text) <= 65536),
    CHECK (jsonb_typeof(query_json) = 'object' AND octet_length(query_json::text) <= 65536)
);

-- Every supported exact predicate has a matching order suffix for bounded
-- keyset scans. Full-text search uses PostgreSQL's built-in simple dictionary
-- and a stored tsvector, so no extension is required.
CREATE INDEX IF NOT EXISTS query_event_project_order_idx
    ON audit.query_event (project_id, created_at DESC, event_id DESC);
CREATE INDEX IF NOT EXISTS query_event_principal_order_idx
    ON audit.query_event (principal_id, created_at DESC, event_id DESC);
CREATE INDEX IF NOT EXISTS query_event_surface_order_idx
    ON audit.query_event (surface, created_at DESC, event_id DESC);
CREATE INDEX IF NOT EXISTS query_event_status_order_idx
    ON audit.query_event (status, created_at DESC, event_id DESC);
CREATE INDEX IF NOT EXISTS query_event_kind_order_idx
    ON audit.query_event (query_kind, created_at DESC, event_id DESC);
CREATE INDEX IF NOT EXISTS query_event_created_order_idx
    ON audit.query_event (created_at DESC, event_id DESC);
CREATE INDEX IF NOT EXISTS query_event_target_idx ON audit.query_event (target);
CREATE INDEX IF NOT EXISTS query_event_request_idx ON audit.query_event (request_id) WHERE request_id <> '';
CREATE INDEX IF NOT EXISTS query_event_search_idx ON audit.query_event USING gin (search_document);

CREATE OR REPLACE FUNCTION audit.enforce_query_event()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        -- Occurrence is authority-owned.  Even a caller that has broad INSERT
        -- privileges cannot backdate or otherwise choose the evidence time.
        NEW.created_at := statement_timestamp();
        NEW.search_document := to_tsvector('simple', concat_ws(' ', NEW.target, NEW.sql_text, NEW.plan_text, NEW.error, NEW.query_json::text));
        RETURN NEW;
    END IF;
    -- Retention is the only controlled exception to append-only history. The
    -- owner function below sets this capability only for the duration of its
    -- invocation; direct UPDATE remains forbidden even to maintenance.
    IF TG_OP = 'DELETE'
       AND current_setting('audit.capability', true) = 'maintenance'
       AND session_user = 'leapview_control_maintenance' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'query audit history is append-only';
END;
$$;

DROP TRIGGER IF EXISTS query_event_append_only ON audit.query_event;
CREATE TRIGGER query_event_append_only
    BEFORE UPDATE OR DELETE ON audit.query_event
    FOR EACH ROW EXECUTE FUNCTION audit.enforce_query_event();
DROP TRIGGER IF EXISTS query_event_database_time ON audit.query_event;
CREATE TRIGGER query_event_database_time
    BEFORE INSERT ON audit.query_event
    FOR EACH ROW EXECUTE FUNCTION audit.enforce_query_event();

-- The floor is durable evidence of the latest fully drained retention
-- boundary applied to query activity. It advances monotonically only after a
-- batch proves that no eligible rows remain at that boundary.
CREATE TABLE IF NOT EXISTS audit.query_event_retention_floor (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    floor_at timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00'::timestamptz,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
INSERT INTO audit.query_event_retention_floor (singleton) VALUES (true)
ON CONFLICT (singleton) DO NOTHING;

-- Retention cleanup is a bounded SECURITY DEFINER surface. Query events are
-- immutable evidence; no hold table or hold column exists in this capability,
-- so the only eligible rows are events at or before the reported cutoff. The
-- ordered, SKIP LOCKED batch lets operators drain a backlog without holding a
-- long transaction or exposing DELETE to request-serving roles.
CREATE OR REPLACE FUNCTION audit.prune_query_events(p_before timestamptz, p_limit integer)
RETURNS TABLE(cutoff timestamptz, floor_at timestamptz, removed bigint)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, audit
SET audit.capability = 'maintenance'
AS $$
BEGIN
    IF p_before IS NULL THEN
        RAISE EXCEPTION 'query event prune cutoff is required';
    END IF;
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 1000 THEN
        RAISE EXCEPTION 'query event prune limit must be between 1 and 1000';
    END IF;
    -- Never let a caller-supplied future cutoff remove events that have not
    -- reached their retention boundary yet. Returning this effective cutoff
    -- gives the retention job durable evidence of the time range applied.
    cutoff := LEAST(p_before, clock_timestamp());
    SELECT f.floor_at INTO STRICT floor_at
    FROM audit.query_event_retention_floor AS f
    WHERE f.singleton = true
    FOR UPDATE;
    WITH doomed AS (
        SELECT event_id
        FROM audit.query_event
        WHERE created_at <= cutoff
        ORDER BY created_at, event_id
        FOR UPDATE SKIP LOCKED
        LIMIT p_limit
    )
    DELETE FROM audit.query_event AS event_row
    USING doomed
    WHERE event_row.event_id = doomed.event_id;
    GET DIAGNOSTICS removed = ROW_COUNT;
    -- Keep the floor at its previous value while this boundary still has a
    -- backlog. This makes the reported floor a truthful replay/retention
    -- proof rather than evidence that only one batch happened to run.
    IF cutoff > floor_at
       AND NOT EXISTS (
           SELECT 1 FROM audit.query_event AS remaining
           WHERE remaining.created_at <= cutoff
       ) THEN
        UPDATE audit.query_event_retention_floor AS f
        SET floor_at = cutoff, updated_at = clock_timestamp()
        WHERE f.singleton = true;
        floor_at := cutoff;
    END IF;
    RETURN NEXT;
END;
$$;

-- The owner/migrator applies grants for its deployment roles.  Revoking the
-- PUBLIC defaults here prevents an accidentally broad role from mutating or
-- reading query evidence; explicit runtime grants remain deployment-owned.
REVOKE ALL ON SCHEMA audit FROM PUBLIC;
REVOKE ALL ON TABLE audit.query_event FROM PUBLIC;
REVOKE ALL ON TABLE audit.query_event_retention_floor FROM PUBLIC;
REVOKE ALL ON FUNCTION audit.enforce_query_event() FROM PUBLIC;
REVOKE ALL ON FUNCTION audit.prune_query_events(timestamptz, integer) FROM PUBLIC;

-- Least-privilege role grants are conditional so the standalone conformance
-- schema works before deployment role provisioning.  The runtime may append
-- and read; the readonly role can inspect only. Neither role can mutate or
-- remove history (the trigger also rejects owner-level UPDATE/DELETE).
DO $$
BEGIN
	IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_owner') THEN
		GRANT ALL ON audit.query_event TO leapview_control_owner;
	END IF;
	IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_migrator') THEN
		GRANT ALL ON audit.query_event TO leapview_control_migrator;
		GRANT ALL ON audit.query_event_retention_floor TO leapview_control_migrator;
	END IF;
	IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_owner') THEN
		GRANT ALL ON audit.query_event_retention_floor TO leapview_control_owner;
	END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA audit TO leapview_control_runtime;
        GRANT SELECT, INSERT ON audit.query_event TO leapview_control_runtime;
        REVOKE UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON audit.query_event FROM leapview_control_runtime;
        REVOKE EXECUTE ON FUNCTION audit.prune_query_events(timestamptz, integer) FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA audit TO leapview_control_maintenance;
        GRANT EXECUTE ON FUNCTION audit.prune_query_events(timestamptz, integer) TO leapview_control_maintenance;
        REVOKE ALL ON audit.query_event FROM leapview_control_maintenance;
        REVOKE ALL ON audit.query_event_retention_floor FROM leapview_control_maintenance;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA audit TO leapview_control_readonly;
        GRANT SELECT ON audit.query_event TO leapview_control_readonly;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON audit.query_event FROM leapview_control_readonly;
    END IF;
END
$$;
