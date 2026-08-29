
-- Durable jobs capability schema. The control-plane baseline creates the
-- schema; CREATE SCHEMA keeps this capability migration independently
-- applicable to a clean PostgreSQL database used by conformance tests.
CREATE SCHEMA IF NOT EXISTS jobs;

CREATE TABLE IF NOT EXISTS jobs.job (
    id                      text PRIMARY KEY,
    kind                    text NOT NULL,
    workload_class          text NOT NULL CHECK (workload_class IN ('background', 'control')),
    principal_id            text NOT NULL CHECK (principal_id = btrim(principal_id) AND length(principal_id) BETWEEN 1 AND 256),
    group_ids               text[] NOT NULL DEFAULT '{}'::text[],
    resource_kind           text NOT NULL,
    resource_id             text NOT NULL,
    estimated_memory_bytes  bigint NOT NULL CHECK (estimated_memory_bytes > 0),
    payload                 jsonb NOT NULL CHECK (octet_length(payload::text) <= 1048576),
    request_digest          text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    status                  text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    attempt_count           bigint NOT NULL DEFAULT 0 CHECK (attempt_count >= 0 AND attempt_count <= max_attempts),
    max_attempts            bigint NOT NULL DEFAULT 3 CHECK (max_attempts BETWEEN 1 AND 100),
    lease_owner             text NOT NULL DEFAULT '',
    lease_expires_at        timestamptz,
    lease_generation        bigint NOT NULL DEFAULT 0 CHECK (lease_generation >= 0),
    available_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    error                   jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (octet_length(error::text) <= 65536),
    created_at              timestamptz NOT NULL DEFAULT clock_timestamp(),
    started_at              timestamptz,
    finished_at             timestamptz,
    CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 256),
    CHECK (kind = btrim(kind) AND length(kind) BETWEEN 1 AND 128),
    CHECK (resource_kind = btrim(resource_kind) AND length(resource_kind) BETWEEN 1 AND 128),
    CHECK (resource_id = btrim(resource_id) AND length(resource_id) BETWEEN 1 AND 256),
    CHECK (array_position(group_ids, NULL) IS NULL),
    CHECK (cardinality(group_ids) <= 256),
    CHECK ((status = 'running' AND lease_owner <> '' AND lease_expires_at IS NOT NULL)
        OR (status <> 'running' AND lease_owner = '' AND lease_expires_at IS NULL)),
    CHECK ((status IN ('succeeded', 'failed', 'cancelled')) = (finished_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS job_claim_idx
    ON jobs.job (workload_class, status, available_at, created_at, id);
CREATE INDEX IF NOT EXISTS job_principal_order_idx
    ON jobs.job (workload_class, principal_id, created_at, id);

CREATE OR REPLACE FUNCTION jobs.guard_job_insert()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, jobs
AS $$
DECLARE
    now_ts timestamptz := clock_timestamp();
BEGIN
    -- Runtime may enqueue only a fresh queued request.  All lifecycle,
    -- attempt, lease and terminal evidence is established by Claim/terminal
    -- transitions, never supplied by an INSERT caller.
    IF NEW.status <> 'queued'
       OR NEW.attempt_count <> 0
       OR NEW.max_attempts <> 3
       OR NEW.lease_generation <> 0
       OR NEW.lease_owner <> ''
       OR NEW.lease_expires_at IS NOT NULL
       OR NEW.started_at IS NOT NULL
       OR NEW.finished_at IS NOT NULL
       OR NEW.error IS DISTINCT FROM '{}'::jsonb THEN
        RAISE EXCEPTION 'job inserts must begin as empty queued records';
    END IF;
    NEW.created_at := now_ts;
    NEW.available_at := now_ts;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS job_insert_guard ON jobs.job;
CREATE TRIGGER job_insert_guard
    BEFORE INSERT ON jobs.job
    FOR EACH ROW EXECUTE FUNCTION jobs.guard_job_insert();

-- One durable row per claim. The row captures both the attempt number and
-- fencing generation; heartbeat and terminal transitions close its outcome.
CREATE TABLE IF NOT EXISTS jobs.attempt (
    job_id             text NOT NULL REFERENCES jobs.job(id) ON DELETE CASCADE,
    attempt_number     bigint NOT NULL CHECK (attempt_number > 0),
    fencing_generation bigint NOT NULL CHECK (fencing_generation > 0),
    owner              text NOT NULL CHECK (owner = btrim(owner) AND length(owner) BETWEEN 1 AND 256),
    leased_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    lease_expires_at   timestamptz NOT NULL,
    started_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at        timestamptz,
    outcome            text NOT NULL DEFAULT 'running' CHECK (outcome IN ('running', 'succeeded', 'failed', 'cancelled', 'expired', 'retrying')),
    retry_at           timestamptz,
    error              jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (octet_length(error::text) <= 65536),
    PRIMARY KEY (job_id, attempt_number),
    UNIQUE (job_id, fencing_generation),
    CHECK ((outcome = 'running') = (finished_at IS NULL)),
    CHECK ((outcome = 'retrying') = (retry_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS attempt_retry_idx ON jobs.attempt (retry_at) WHERE outcome = 'retrying';

CREATE OR REPLACE FUNCTION jobs.guard_attempt_insert()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, jobs
AS $$
DECLARE
    now_ts timestamptz := clock_timestamp();
BEGIN
    IF NEW.outcome <> 'running'
	   OR NEW.finished_at IS NOT NULL
	   OR NEW.retry_at IS NOT NULL
	   OR NEW.owner = ''
	   OR NEW.error IS DISTINCT FROM '{}'::jsonb
	   OR NEW.lease_expires_at <= now_ts
	   OR NEW.lease_expires_at > now_ts + interval '24 hours' THEN
        RAISE EXCEPTION 'attempt inserts must begin as active leased claims';
    END IF;
    -- Timestamps are evidence of the database transaction, not caller input.
    NEW.leased_at := now_ts;
    NEW.started_at := now_ts;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS attempt_insert_guard ON jobs.attempt;
CREATE TRIGGER attempt_insert_guard
    BEFORE INSERT ON jobs.attempt
    FOR EACH ROW EXECUTE FUNCTION jobs.guard_attempt_insert();

CREATE TABLE IF NOT EXISTS jobs.event_sequence (
    resource_kind text NOT NULL,
    resource_id   text NOT NULL,
    next_event_id bigint NOT NULL CHECK (next_event_id > 0),
    PRIMARY KEY (resource_kind, resource_id)
);

CREATE OR REPLACE FUNCTION jobs.guard_event_sequence()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, jobs
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.next_event_id <> 1 THEN
            RAISE EXCEPTION 'event sequence must begin at one';
        END IF;
    ELSIF NEW.resource_kind IS DISTINCT FROM OLD.resource_kind
       OR NEW.resource_id IS DISTINCT FROM OLD.resource_id
       OR NEW.next_event_id <> OLD.next_event_id + 1 THEN
        RAISE EXCEPTION 'event sequence must advance exactly once';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS event_sequence_guard ON jobs.event_sequence;
CREATE TRIGGER event_sequence_guard
    BEFORE INSERT OR UPDATE ON jobs.event_sequence
    FOR EACH ROW EXECUTE FUNCTION jobs.guard_event_sequence();

CREATE TABLE IF NOT EXISTS jobs.event (
    resource_kind text NOT NULL,
    resource_id   text NOT NULL,
    event_id      bigint NOT NULL CHECK (event_id > 0),
    event_type    text NOT NULL,
    event_key     text NOT NULL DEFAULT '',
    data          jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (octet_length(data::text) <= 1048576),
    created_at    timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (resource_kind, resource_id, event_id),
	CHECK (resource_kind = btrim(resource_kind) AND length(resource_kind) BETWEEN 1 AND 128),
	CHECK (resource_id = btrim(resource_id) AND length(resource_id) BETWEEN 1 AND 256),
	CHECK (event_type = btrim(event_type) AND length(event_type) BETWEEN 1 AND 128),
	CHECK (jsonb_typeof(data) = 'object'),
    CHECK (event_key = '' OR (event_key = btrim(event_key) AND length(event_key) <= 256))
);

-- Empty event keys are intentionally reusable; a partial index gives workflow
-- keys idempotency without imposing uniqueness on ordinary append-only events.
CREATE UNIQUE INDEX IF NOT EXISTS event_resource_key_idx
    ON jobs.event (resource_kind, resource_id, event_key)
    WHERE event_key <> '';

CREATE OR REPLACE FUNCTION jobs.guard_event_insert()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, jobs
AS $$
BEGIN
    IF NEW.event_id <= 0 OR NEW.resource_kind <> btrim(NEW.resource_kind)
       OR NEW.resource_id <> btrim(NEW.resource_id)
       OR NEW.event_type <> btrim(NEW.event_type)
       OR NEW.event_key <> btrim(NEW.event_key) THEN
        RAISE EXCEPTION 'event insert identity is not canonical';
    END IF;
    NEW.created_at := clock_timestamp();
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS event_insert_guard ON jobs.event;
CREATE TRIGGER event_insert_guard
    BEFORE INSERT ON jobs.event
    FOR EACH ROW EXECUTE FUNCTION jobs.guard_event_insert();

CREATE OR REPLACE FUNCTION jobs.reject_event_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, jobs
AS $$
BEGIN
    RAISE EXCEPTION 'job events are append-only';
END;
$$;

DROP TRIGGER IF EXISTS event_append_only ON jobs.event;
CREATE TRIGGER event_append_only
    BEFORE UPDATE OR DELETE ON jobs.event
    FOR EACH ROW EXECUTE FUNCTION jobs.reject_event_mutation();

-- Durable job state is changed through the repository state machine.  The
-- trigger is defense in depth for a role that accidentally receives a wider
-- UPDATE grant: request identity, attempts, fences and terminal evidence may
-- not be rewritten or a terminal row reopened.  Lease takeover and retry are
-- the only transitions that may advance a running row.
CREATE OR REPLACE FUNCTION jobs.guard_job_update()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, jobs
AS $$
BEGIN
    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.kind IS DISTINCT FROM OLD.kind
       OR NEW.workload_class IS DISTINCT FROM OLD.workload_class
       OR NEW.principal_id IS DISTINCT FROM OLD.principal_id
       OR NEW.group_ids IS DISTINCT FROM OLD.group_ids
       OR NEW.resource_kind IS DISTINCT FROM OLD.resource_kind
       OR NEW.resource_id IS DISTINCT FROM OLD.resource_id
       OR NEW.estimated_memory_bytes IS DISTINCT FROM OLD.estimated_memory_bytes
       OR NEW.payload IS DISTINCT FROM OLD.payload
       OR NEW.request_digest IS DISTINCT FROM OLD.request_digest
       OR NEW.max_attempts IS DISTINCT FROM OLD.max_attempts
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'job request identity is immutable';
    END IF;

    IF OLD.status IN ('succeeded', 'failed', 'cancelled') THEN
        IF NEW IS DISTINCT FROM OLD THEN
            RAISE EXCEPTION 'terminal job is immutable';
        END IF;
    ELSIF OLD.status = 'queued' THEN
        IF NEW.status NOT IN ('queued', 'running', 'cancelled') THEN
            RAISE EXCEPTION 'invalid queued job transition';
        END IF;
        IF NEW.status = 'queued' AND NEW IS DISTINCT FROM OLD THEN
            RAISE EXCEPTION 'queued job updates must be no-ops';
        END IF;
        IF NEW.status = 'running' AND (NEW.attempt_count <> OLD.attempt_count + 1 OR NEW.lease_generation <> OLD.lease_generation + 1) THEN
            RAISE EXCEPTION 'claim must advance attempt and fence exactly once';
        END IF;
        IF NEW.status = 'running' AND (
            NEW.available_at IS DISTINCT FROM OLD.available_at OR
            NEW.finished_at IS DISTINCT FROM OLD.finished_at OR
            NEW.error IS DISTINCT FROM OLD.error OR
			NEW.started_at IS NULL OR
			NEW.lease_owner = '' OR NEW.lease_expires_at IS NULL OR
			NEW.lease_expires_at <= clock_timestamp() OR
			NEW.lease_expires_at > clock_timestamp() + interval '24 hours') THEN
            RAISE EXCEPTION 'claim changed fields outside the lease transition';
        END IF;
        IF NEW.status = 'running' THEN
            NEW.started_at := COALESCE(OLD.started_at, clock_timestamp());
        END IF;
        IF NEW.status = 'cancelled' AND (
            NEW.available_at IS DISTINCT FROM OLD.available_at OR
            NEW.started_at IS DISTINCT FROM OLD.started_at OR
            NEW.error IS DISTINCT FROM OLD.error OR
            NEW.attempt_count IS DISTINCT FROM OLD.attempt_count OR
            NEW.lease_generation IS DISTINCT FROM OLD.lease_generation OR
            NEW.finished_at IS NULL) THEN
            RAISE EXCEPTION 'queued cancellation changed immutable job fields';
        END IF;
        IF NEW.status = 'queued' AND (NEW.lease_owner <> '' OR NEW.lease_expires_at IS NOT NULL) THEN
            RAISE EXCEPTION 'queued job cannot retain a lease';
        END IF;
    ELSIF OLD.status = 'running' THEN
        IF NEW.status NOT IN ('running', 'queued', 'succeeded', 'failed', 'cancelled') THEN
            RAISE EXCEPTION 'invalid running job transition';
        END IF;
        IF NEW.status = 'running' THEN
            IF NEW.lease_owner = '' OR NEW.lease_expires_at IS NULL THEN
                RAISE EXCEPTION 'running job requires an active lease';
            END IF;
            IF NEW.lease_owner = OLD.lease_owner THEN
                IF NEW.lease_generation = OLD.lease_generation AND NEW.attempt_count = OLD.attempt_count THEN
                    IF NEW.status IS DISTINCT FROM OLD.status
                       OR NEW.available_at IS DISTINCT FROM OLD.available_at
                       OR NEW.started_at IS DISTINCT FROM OLD.started_at
                       OR NEW.finished_at IS DISTINCT FROM OLD.finished_at
                       OR NEW.error IS DISTINCT FROM OLD.error THEN
                        RAISE EXCEPTION 'heartbeat changed fields outside the lease';
                    END IF;
                    IF NEW.lease_expires_at < OLD.lease_expires_at OR NEW.lease_expires_at <= clock_timestamp() THEN
                        RAISE EXCEPTION 'heartbeat cannot shorten a live lease';
                    END IF;
                    NULL; -- heartbeat renewal
                ELSIF NEW.lease_generation <> OLD.lease_generation + 1 OR NEW.attempt_count <> OLD.attempt_count + 1 OR OLD.lease_expires_at > clock_timestamp() THEN
                    RAISE EXCEPTION 'takeover must advance attempt and fence after expiry';
                END IF;
            ELSIF NEW.lease_generation <> OLD.lease_generation + 1 OR NEW.attempt_count <> OLD.attempt_count + 1 OR OLD.lease_expires_at > clock_timestamp() THEN
                    RAISE EXCEPTION 'heartbeat cannot change attempt or fence';
            END IF;
			IF NEW.available_at IS DISTINCT FROM OLD.available_at OR NEW.finished_at IS DISTINCT FROM OLD.finished_at OR NEW.error IS DISTINCT FROM OLD.error OR NEW.started_at IS DISTINCT FROM OLD.started_at OR NEW.lease_expires_at <= clock_timestamp() OR NEW.lease_expires_at > clock_timestamp() + interval '24 hours' THEN
                RAISE EXCEPTION 'takeover changed fields outside the lease';
            END IF;
        ELSIF NEW.status = 'queued' THEN
            IF NEW.attempt_count <> OLD.attempt_count OR NEW.lease_generation <> OLD.lease_generation THEN
                RAISE EXCEPTION 'retry cannot change attempt or fence';
            END IF;
			IF NEW.started_at IS DISTINCT FROM OLD.started_at OR NEW.finished_at IS DISTINCT FROM OLD.finished_at OR NEW.lease_owner <> '' OR NEW.lease_expires_at IS NOT NULL THEN
				RAISE EXCEPTION 'retry changed fields outside the retry transition';
			END IF;
			IF NEW.available_at > clock_timestamp() + interval '24 hours' THEN
				RAISE EXCEPTION 'retry availability exceeds the bounded delay';
			END IF;
        ELSIF NEW.attempt_count <> OLD.attempt_count OR NEW.lease_generation <> OLD.lease_generation THEN
            RAISE EXCEPTION 'terminal transition cannot change attempt or fence';
        ELSIF NEW.available_at IS DISTINCT FROM OLD.available_at OR NEW.started_at IS DISTINCT FROM OLD.started_at THEN
            RAISE EXCEPTION 'terminal transition changed immutable job fields';
        ELSIF NEW.lease_owner <> '' OR NEW.lease_expires_at IS NOT NULL THEN
            RAISE EXCEPTION 'non-running job cannot retain a lease';
        END IF;
    END IF;
    IF NEW.status IN ('succeeded', 'failed', 'cancelled') AND OLD.status NOT IN ('succeeded', 'failed', 'cancelled') THEN
        NEW.finished_at := clock_timestamp();
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS job_lifecycle_guard ON jobs.job;
CREATE TRIGGER job_lifecycle_guard
    BEFORE UPDATE ON jobs.job
    FOR EACH ROW EXECUTE FUNCTION jobs.guard_job_update();

CREATE OR REPLACE FUNCTION jobs.guard_attempt_update()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, jobs
AS $$
BEGIN
    IF NEW.job_id IS DISTINCT FROM OLD.job_id
       OR NEW.attempt_number IS DISTINCT FROM OLD.attempt_number
       OR NEW.fencing_generation IS DISTINCT FROM OLD.fencing_generation
       OR NEW.owner IS DISTINCT FROM OLD.owner
       OR NEW.leased_at IS DISTINCT FROM OLD.leased_at
       OR NEW.started_at IS DISTINCT FROM OLD.started_at THEN
        RAISE EXCEPTION 'attempt identity is immutable';
    END IF;
    IF OLD.outcome <> 'running' AND NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'terminal attempt is immutable';
    END IF;
    IF OLD.outcome = 'running' AND NEW.outcome NOT IN ('running', 'succeeded', 'failed', 'cancelled', 'expired', 'retrying') THEN
        RAISE EXCEPTION 'invalid attempt transition';
    END IF;
    IF OLD.outcome = 'running' AND NEW.outcome = 'running' AND NEW.lease_expires_at < OLD.lease_expires_at THEN
        RAISE EXCEPTION 'heartbeat cannot shorten an attempt lease';
    END IF;
    IF OLD.outcome = 'running' AND NEW.outcome <> 'running' THEN
        NEW.finished_at := clock_timestamp();
        IF NEW.outcome <> 'retrying' THEN
            NEW.retry_at := NULL;
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS attempt_lifecycle_guard ON jobs.attempt;
CREATE TRIGGER attempt_lifecycle_guard
    BEFORE UPDATE ON jobs.attempt
    FOR EACH ROW EXECUTE FUNCTION jobs.guard_attempt_update();

-- A bounded operational view keeps queue health observable without copying
-- payloads.  Consumers can distinguish work that is queued, currently leased,
-- expired and retrying/dead-lettered while retaining the immutable job row.
CREATE OR REPLACE VIEW jobs.job_observability AS
SELECT
    j.id,
    j.kind,
    j.workload_class,
    j.principal_id,
    j.status,
    j.attempt_count,
    j.max_attempts,
    j.lease_owner,
    j.lease_expires_at,
    j.available_at,
    CASE
        WHEN j.status = 'running' AND j.lease_expires_at <= clock_timestamp() THEN 'expired'
        WHEN j.status = 'running' AND j.started_at <= clock_timestamp() - interval '1 hour' THEN 'stuck'
        WHEN j.status = 'running' THEN 'running'
        WHEN j.status = 'queued' AND j.attempt_count > 0 THEN 'retrying'
        WHEN j.status = 'failed' AND j.attempt_count >= j.max_attempts THEN 'dead_letter'
        ELSE j.status
    END AS health,
    COALESCE(a.retry_count, 0)::bigint AS retry_count,
    COALESCE(a.expired_count, 0)::bigint AS expired_count,
    a.last_retry_at
FROM jobs.job j
LEFT JOIN LATERAL (
    SELECT count(*) FILTER (WHERE outcome = 'retrying') AS retry_count,
           count(*) FILTER (WHERE outcome = 'expired') AS expired_count,
           max(retry_at) FILTER (WHERE outcome = 'retrying') AS last_retry_at
    FROM jobs.attempt
    WHERE job_id = j.id
) a ON true;

-- Only terminal rows older than the caller's cutoff are eligible.  The
-- function locks and deletes one bounded batch, and is the sole delete surface
-- granted to the runtime role.  Attempts are removed by the FK cascade.
CREATE OR REPLACE FUNCTION jobs.prune(p_before timestamptz, p_batch_limit integer)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, jobs
AS $$
DECLARE
    v_cutoff timestamptz;
    v_removed bigint;
BEGIN
    IF p_before IS NULL OR p_batch_limit IS NULL OR p_batch_limit < 1 OR p_batch_limit > 1000 THEN
        RAISE EXCEPTION 'job prune cutoff and batch limit are required (1..1000)';
    END IF;
    v_cutoff := LEAST(p_before, clock_timestamp());
    WITH doomed AS (
        SELECT id
        FROM jobs.job
        WHERE status IN ('succeeded', 'failed', 'cancelled')
          AND finished_at IS NOT NULL
          AND finished_at <= v_cutoff
        ORDER BY finished_at, id
        FOR UPDATE SKIP LOCKED
        LIMIT p_batch_limit
    )
    DELETE FROM jobs.job j
    USING doomed d
    WHERE j.id = d.id;
    GET DIAGNOSTICS v_removed = ROW_COUNT;
    RETURN v_removed;
END;
$$;

-- No object in this capability is ambiently callable.  The conditional role
-- grants let the isolated schema run before the deployment role bundle exists;
-- the control-plane migration reapplies the same grants after role creation.
REVOKE ALL ON SCHEMA jobs FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA jobs FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA jobs FROM PUBLIC;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_owner') THEN
        GRANT ALL ON ALL TABLES IN SCHEMA jobs TO leapview_control_owner;
        GRANT ALL ON ALL FUNCTIONS IN SCHEMA jobs TO leapview_control_owner;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_migrator') THEN
        GRANT ALL ON ALL TABLES IN SCHEMA jobs TO leapview_control_migrator;
        GRANT ALL ON ALL FUNCTIONS IN SCHEMA jobs TO leapview_control_migrator;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA jobs TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON jobs.job, jobs.attempt, jobs.event_sequence, jobs.event TO leapview_control_runtime;
        GRANT SELECT ON jobs.job_observability TO leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION jobs.prune(timestamptz, integer) TO leapview_control_runtime;
        REVOKE DELETE ON jobs.job, jobs.attempt, jobs.event_sequence, jobs.event FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA jobs TO leapview_control_readonly;
        REVOKE SELECT ON jobs.job, jobs.attempt, jobs.event_sequence, jobs.event FROM leapview_control_readonly;
        GRANT SELECT ON jobs.job_observability TO leapview_control_readonly;
    END IF;
END
$$;

COMMENT ON VIEW jobs.job_observability IS
    'Bounded queue health projection: running, expired, retrying and dead-letter evidence';
