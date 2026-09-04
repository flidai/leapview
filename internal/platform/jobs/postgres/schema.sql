-- LeapView-owned product history for asynchronous operations.
-- River owns operational queueing, claims, retries, leases, scheduling, and
-- worker lifecycle in its public.river_* tables. These rows remain after
-- River cleanup so public IDs, authorization, evidence, and event history do
-- not depend on the executor's retention policy.
CREATE SCHEMA IF NOT EXISTS jobs;

CREATE TABLE IF NOT EXISTS jobs.job_history (
    id                     text PRIMARY KEY,
    kind                   text NOT NULL,
    workload_class         text NOT NULL CHECK (workload_class IN ('control', 'background')),
    principal_id           text NOT NULL,
    group_ids              jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(group_ids) = 'array'),
    partition_key          text NOT NULL,
    resource_kind          text NOT NULL,
    resource_id            text NOT NULL,
    estimated_memory_bytes bigint NOT NULL CHECK (estimated_memory_bytes > 0),
    payload                jsonb NOT NULL CHECK (jsonb_typeof(payload) IN ('object', 'array')),
    request_digest         text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    river_job_id           bigint UNIQUE,
    status                 text NOT NULL DEFAULT 'queued'
                           CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    attempt_count          integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    created_at             timestamptz NOT NULL DEFAULT clock_timestamp(),
    started_at             timestamptz,
    finished_at            timestamptz,
    error                  jsonb,
    CHECK (id = btrim(id) AND octet_length(id) BETWEEN 1 AND 255),
    CHECK (kind = btrim(kind) AND octet_length(kind) BETWEEN 1 AND 255),
    CHECK (principal_id = btrim(principal_id) AND octet_length(principal_id) BETWEEN 1 AND 256),
    CHECK (partition_key = btrim(partition_key) AND octet_length(partition_key) BETWEEN 1 AND 512),
    CHECK (resource_kind = btrim(resource_kind) AND octet_length(resource_kind) BETWEEN 1 AND 255),
    CHECK (resource_id = btrim(resource_id) AND octet_length(resource_id) BETWEEN 1 AND 255),
    CHECK ((status IN ('queued', 'running') AND finished_at IS NULL) OR
           (status IN ('succeeded', 'failed', 'cancelled') AND finished_at IS NOT NULL)),
    CHECK ((status = 'failed' AND error IS NOT NULL) OR status <> 'failed')
);

CREATE INDEX IF NOT EXISTS job_history_active_partition_idx
    ON jobs.job_history (partition_key, created_at, id)
    WHERE status IN ('queued', 'running');
CREATE INDEX IF NOT EXISTS job_history_resource_idx
    ON jobs.job_history (resource_kind, resource_id, created_at, id);
CREATE INDEX IF NOT EXISTS job_history_terminal_idx
    ON jobs.job_history (finished_at, id)
    WHERE status IN ('succeeded', 'failed', 'cancelled');

CREATE OR REPLACE FUNCTION jobs.guard_job_history_update()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, jobs
-- +goose StatementBegin
AS $$
BEGIN
    IF ROW(OLD.id, OLD.kind, OLD.workload_class, OLD.principal_id, OLD.group_ids,
           OLD.partition_key, OLD.resource_kind, OLD.resource_id,
           OLD.estimated_memory_bytes, OLD.payload, OLD.request_digest,
           OLD.created_at)
       IS DISTINCT FROM
       ROW(NEW.id, NEW.kind, NEW.workload_class, NEW.principal_id, NEW.group_ids,
           NEW.partition_key, NEW.resource_kind, NEW.resource_id,
           NEW.estimated_memory_bytes, NEW.payload, NEW.request_digest,
           NEW.created_at) THEN
        RAISE EXCEPTION 'product job identity is immutable';
    END IF;
    IF OLD.river_job_id IS NOT NULL AND NEW.river_job_id IS DISTINCT FROM OLD.river_job_id THEN
        RAISE EXCEPTION 'bound River job identity is immutable';
    END IF;
    IF NEW.attempt_count < OLD.attempt_count THEN
        RAISE EXCEPTION 'product job attempt count cannot decrease';
    END IF;
    IF OLD.status IN ('succeeded', 'failed', 'cancelled') AND NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'terminal product job history is immutable';
    END IF;
    IF OLD.status = 'queued' AND NEW.status NOT IN ('queued', 'running', 'failed', 'cancelled') THEN
        RAISE EXCEPTION 'invalid queued product job transition';
    END IF;
    IF OLD.status = 'running' AND NEW.status NOT IN ('queued', 'running', 'succeeded', 'failed', 'cancelled') THEN
        RAISE EXCEPTION 'invalid running product job transition';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS job_history_lifecycle_guard ON jobs.job_history;
CREATE TRIGGER job_history_lifecycle_guard
    BEFORE UPDATE ON jobs.job_history
    FOR EACH ROW EXECUTE FUNCTION jobs.guard_job_history_update();

CREATE TABLE IF NOT EXISTS jobs.event_sequence (
    resource_kind text NOT NULL,
    resource_id   text NOT NULL,
    next_event_id bigint NOT NULL DEFAULT 1 CHECK (next_event_id > 0),
    PRIMARY KEY (resource_kind, resource_id)
);

CREATE TABLE IF NOT EXISTS jobs.event (
    resource_kind text NOT NULL,
    resource_id   text NOT NULL,
    event_id      bigint NOT NULL CHECK (event_id > 0),
    event_type    text NOT NULL,
    event_key     text NOT NULL,
    data          jsonb NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (resource_kind, resource_id, event_id),
    UNIQUE (resource_kind, resource_id, event_key),
    CHECK (resource_kind = btrim(resource_kind) AND octet_length(resource_kind) BETWEEN 1 AND 255),
    CHECK (resource_id = btrim(resource_id) AND octet_length(resource_id) BETWEEN 1 AND 255),
    CHECK (event_type = btrim(event_type) AND octet_length(event_type) BETWEEN 1 AND 255),
    CHECK (event_key = btrim(event_key) AND octet_length(event_key) BETWEEN 1 AND 512),
    CHECK (jsonb_typeof(data) = 'object')
);

CREATE OR REPLACE FUNCTION jobs.reject_event_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, jobs
-- +goose StatementBegin
AS $$
BEGIN
    RAISE EXCEPTION 'job events are append-only';
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS event_append_only ON jobs.event;
CREATE TRIGGER event_append_only
    BEFORE UPDATE OR DELETE ON jobs.event
    FOR EACH ROW EXECUTE FUNCTION jobs.reject_event_mutation();

CREATE OR REPLACE FUNCTION jobs.prune(p_before timestamptz, p_batch_limit integer)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, jobs
-- +goose StatementBegin
AS $$
DECLARE
    removed bigint;
BEGIN
    IF p_before IS NULL OR p_batch_limit < 1 OR p_batch_limit > 1000 THEN
        RAISE EXCEPTION 'invalid jobs prune arguments';
    END IF;
    WITH doomed AS (
        SELECT id
          FROM jobs.job_history
         WHERE status IN ('succeeded', 'failed', 'cancelled')
           AND finished_at <= p_before
         ORDER BY finished_at, id
         LIMIT p_batch_limit
         FOR UPDATE SKIP LOCKED
    )
    DELETE FROM jobs.job_history h USING doomed d WHERE h.id = d.id;
    GET DIAGNOSTICS removed = ROW_COUNT;
    RETURN removed;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON SCHEMA jobs FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA jobs FROM PUBLIC;
REVOKE ALL ON FUNCTION jobs.guard_job_history_update(), jobs.reject_event_mutation(),
    jobs.prune(timestamptz, integer) FROM PUBLIC;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA jobs TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON jobs.job_history, jobs.event_sequence, jobs.event TO leapview_control_runtime;
        REVOKE DELETE ON jobs.job_history, jobs.event_sequence, jobs.event FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA jobs TO leapview_control_maintenance;
        GRANT EXECUTE ON FUNCTION jobs.prune(timestamptz, integer) TO leapview_control_maintenance;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA jobs TO leapview_control_readonly;
        GRANT SELECT ON jobs.job_history TO leapview_control_readonly;
        REVOKE SELECT ON jobs.event_sequence, jobs.event FROM leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA jobs TO leapview_control_backup;
        GRANT SELECT ON jobs.job_history, jobs.event_sequence, jobs.event TO leapview_control_backup;
    END IF;
END
$$;
-- +goose StatementEnd
