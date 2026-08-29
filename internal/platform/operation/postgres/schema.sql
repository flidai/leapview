-- Idempotent operation capability schema.
-- This schema is deliberately self-contained: callers apply it from their
-- own migration transaction and no transaction control is embedded here.
CREATE SCHEMA IF NOT EXISTS platform;

-- The baseline declares platform.operation. This capability-owned definition
-- is the final shape consumed by this repository and can be applied in an
-- isolated capability database before the platform migration is assembled.
CREATE TABLE IF NOT EXISTS platform.operation (
    operation_id          uuid PRIMARY KEY,
    scope_id              text NOT NULL CHECK (scope_id = btrim(scope_id) AND octet_length(scope_id) BETWEEN 1 AND 255),
    operation_type        text NOT NULL DEFAULT 'idempotency' CHECK (operation_type = btrim(operation_type) AND octet_length(operation_type) BETWEEN 1 AND 255),
    idempotency_key       text NOT NULL CHECK (idempotency_key = btrim(idempotency_key) AND octet_length(idempotency_key) BETWEEN 1 AND 512),
    request_digest        text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    state                 text NOT NULL CHECK (state IN ('pending', 'completed', 'failed', 'indeterminate')),
    owner_id              text NOT NULL CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255),
    lease_expires_at      timestamptz NOT NULL,
    fencing_generation    bigint NOT NULL CHECK (fencing_generation > 0),
    outcome              jsonb NOT NULL DEFAULT '{}'::jsonb,
    attempt_id            uuid,
    attempt_identity      text CHECK (attempt_identity IS NULL OR (attempt_identity = btrim(attempt_identity) AND octet_length(attempt_identity) BETWEEN 1 AND 512)),
    attempt_evidence      jsonb,
    resolution_evidence   jsonb,
    created_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    terminal_at           timestamptz,
    retention_interval    interval NOT NULL CHECK (retention_interval > interval '0' AND retention_interval <= interval '365 days'),
    expires_at            timestamptz NOT NULL,
    UNIQUE (scope_id, idempotency_key),
    CONSTRAINT idempotency_operation_outcome_json CHECK (jsonb_typeof(outcome) = 'object' AND octet_length(outcome::text) <= 32768),
    CONSTRAINT idempotency_operation_attempt_evidence_json CHECK (attempt_evidence IS NULL OR (jsonb_typeof(attempt_evidence) = 'object' AND octet_length(attempt_evidence::text) <= 32768)),
    CONSTRAINT idempotency_operation_resolution_evidence_json CHECK (resolution_evidence IS NULL OR (jsonb_typeof(resolution_evidence) = 'object' AND octet_length(resolution_evidence::text) <= 32768)),
    CONSTRAINT idempotency_operation_attempt_identity_pair CHECK ((attempt_id IS NULL) = (attempt_identity IS NULL)),
    CONSTRAINT idempotency_operation_attempt_evidence_pair CHECK (attempt_evidence IS NULL OR attempt_id IS NOT NULL),
    CONSTRAINT idempotency_operation_indeterminate_attempt CHECK (state <> 'indeterminate' OR attempt_id IS NOT NULL),
    CONSTRAINT idempotency_operation_resolution_pair CHECK (resolution_evidence IS NULL OR (attempt_id IS NOT NULL AND state IN ('completed', 'failed'))),
    CONSTRAINT idempotency_operation_terminal_at CHECK (
        (state = 'pending' AND terminal_at IS NULL) OR (state <> 'pending' AND terminal_at IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS idempotency_operation_expiry_idx
    ON platform.operation (expires_at)
    WHERE state <> 'pending';

CREATE INDEX IF NOT EXISTS idempotency_operation_attempt_idx
    ON platform.operation (attempt_id)
    WHERE attempt_id IS NOT NULL;

-- INSERT is guarded separately from UPDATE: a role with INSERT cannot
-- manufacture terminal evidence or choose timestamps/fences outside the
-- repository state machine.
CREATE OR REPLACE FUNCTION platform.guard_operation_insert()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
DECLARE
    now_ts timestamptz := clock_timestamp();
BEGIN
    IF NEW.state <> 'pending'
       OR NEW.fencing_generation <> 1
       OR NEW.terminal_at IS NOT NULL
       OR NEW.outcome IS DISTINCT FROM '{}'::jsonb
       OR NEW.attempt_id IS NOT NULL
       OR NEW.attempt_identity IS NOT NULL
       OR NEW.attempt_evidence IS NOT NULL
       OR NEW.resolution_evidence IS NOT NULL THEN
        RAISE EXCEPTION 'operation inserts must begin as empty pending records';
    END IF;
    IF NEW.lease_expires_at <= now_ts
       OR NEW.lease_expires_at > now_ts + interval '24 hours' THEN
        RAISE EXCEPTION 'operation lease expiry is outside the bounded window';
    END IF;
    IF NEW.retention_interval <= interval '0 days'
       OR NEW.retention_interval > interval '365 days' THEN
        RAISE EXCEPTION 'operation retention interval is outside the bounded window';
    END IF;
    NEW.created_at := now_ts;
    NEW.updated_at := now_ts;
    NEW.expires_at := now_ts + NEW.retention_interval;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS operation_insert_guard ON platform.operation;
CREATE TRIGGER operation_insert_guard
    BEFORE INSERT ON platform.operation
    FOR EACH ROW EXECUTE FUNCTION platform.guard_operation_insert();

-- Runtime cleanup is deliberately a bounded SECURITY DEFINER surface. The
-- runtime role never receives direct DELETE, preserving pending/indeterminate
-- evidence for reconciliation and audit.
CREATE OR REPLACE FUNCTION platform.prune_operations(p_before timestamptz, p_limit integer)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, platform
AS $$
DECLARE
    cutoff timestamptz;
    removed bigint;
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 1000 THEN
        RAISE EXCEPTION 'operation prune limit must be between 1 and 1000';
    END IF;
    cutoff := LEAST(COALESCE(p_before, clock_timestamp()), clock_timestamp());
    WITH doomed AS (
        SELECT scope_id, idempotency_key
        FROM platform.operation
        WHERE state IN ('completed', 'failed')
          AND expires_at <= cutoff
        ORDER BY expires_at
        LIMIT p_limit
    )
    DELETE FROM platform.operation o
    USING doomed d
    WHERE o.scope_id = d.scope_id
      AND o.idempotency_key = d.idempotency_key;
    GET DIAGNOSTICS removed = ROW_COUNT;
    RETURN removed;
END;
$$;

-- Keep operation identity and the lease state machine authoritative even for
-- direct SQL callers. Repository transitions remain the normal mutation path,
-- but this guard prevents accidental fencing rollback or terminal reopening.
CREATE OR REPLACE FUNCTION platform.guard_operation_update()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    IF NEW.operation_id IS DISTINCT FROM OLD.operation_id
       OR NEW.scope_id IS DISTINCT FROM OLD.scope_id
       OR NEW.operation_type IS DISTINCT FROM OLD.operation_type
       OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key
       OR NEW.request_digest IS DISTINCT FROM OLD.request_digest
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.retention_interval IS DISTINCT FROM OLD.retention_interval THEN
        RAISE EXCEPTION 'operation identity is immutable';
    END IF;

    IF NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'operation updated_at must be monotonic';
    END IF;
    IF NEW.fencing_generation < OLD.fencing_generation THEN
        RAISE EXCEPTION 'operation fencing generation cannot decrease';
    END IF;

    IF OLD.state = 'pending' AND NEW.state = 'pending' THEN
        IF NEW.outcome IS DISTINCT FROM OLD.outcome
           OR NEW.terminal_at IS DISTINCT FROM OLD.terminal_at
           OR NEW.resolution_evidence IS DISTINCT FROM OLD.resolution_evidence
           OR NEW.expires_at IS DISTINCT FROM OLD.expires_at THEN
            RAISE EXCEPTION 'pending operation evidence is immutable';
        END IF;
        IF OLD.attempt_id IS NULL AND OLD.attempt_identity IS NULL THEN
            IF (NEW.attempt_id IS NULL) <> (NEW.attempt_identity IS NULL) THEN
                RAISE EXCEPTION 'pending attempt identity must be a pair';
            END IF;
        ELSIF NEW.attempt_id IS DISTINCT FROM OLD.attempt_id
           OR NEW.attempt_identity IS DISTINCT FROM OLD.attempt_identity THEN
            RAISE EXCEPTION 'pending attempt identity can only bind once';
        END IF;
        IF NEW.owner_id IS DISTINCT FROM OLD.owner_id THEN
            IF NEW.fencing_generation <> OLD.fencing_generation + 1
               OR OLD.attempt_id IS NOT NULL
               OR NEW.lease_expires_at <= OLD.lease_expires_at THEN
                RAISE EXCEPTION 'pending takeover must advance the fence exactly once';
            END IF;
        ELSIF NEW.fencing_generation <> OLD.fencing_generation THEN
            RAISE EXCEPTION 'pending owner fence is immutable';
        ELSIF NEW.lease_expires_at < OLD.lease_expires_at THEN
            RAISE EXCEPTION 'pending lease may only extend';
        END IF;
    ELSIF OLD.state = 'pending' AND NEW.state IN ('completed', 'failed') THEN
        IF NEW.fencing_generation <> OLD.fencing_generation
           OR NEW.owner_id IS DISTINCT FROM OLD.owner_id
           OR NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at
           OR NEW.attempt_id IS DISTINCT FROM OLD.attempt_id
           OR NEW.attempt_identity IS DISTINCT FROM OLD.attempt_identity
           OR NEW.attempt_evidence IS DISTINCT FROM OLD.attempt_evidence
           OR NEW.resolution_evidence IS DISTINCT FROM OLD.resolution_evidence THEN
            RAISE EXCEPTION 'terminal completion cannot change the fence';
        END IF;
    ELSIF OLD.state = 'pending' AND NEW.state = 'indeterminate' THEN
        IF NEW.fencing_generation <> OLD.fencing_generation + 1
           OR NEW.owner_id IS DISTINCT FROM OLD.owner_id
           OR NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at
           OR NEW.attempt_id IS DISTINCT FROM OLD.attempt_id
           OR NEW.attempt_identity IS DISTINCT FROM OLD.attempt_identity
           OR NEW.resolution_evidence IS DISTINCT FROM OLD.resolution_evidence THEN
            RAISE EXCEPTION 'indeterminate transition must advance the fence';
        END IF;
    ELSIF OLD.state = 'indeterminate' AND NEW.state IN ('completed', 'failed') THEN
        IF NEW.fencing_generation <> OLD.fencing_generation THEN
            RAISE EXCEPTION 'reconciliation cannot change the fence';
        END IF;
    ELSIF OLD.state = 'indeterminate' AND NEW.state = 'indeterminate' THEN
        IF NEW.owner_id IS DISTINCT FROM OLD.owner_id
           OR NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at
           OR NEW.fencing_generation IS DISTINCT FROM OLD.fencing_generation
           OR NEW.outcome IS DISTINCT FROM OLD.outcome
           OR NEW.attempt_id IS DISTINCT FROM OLD.attempt_id
           OR NEW.attempt_identity IS DISTINCT FROM OLD.attempt_identity
           OR NEW.attempt_evidence IS DISTINCT FROM OLD.attempt_evidence
           OR NEW.resolution_evidence IS DISTINCT FROM OLD.resolution_evidence
           OR NEW.terminal_at IS DISTINCT FROM OLD.terminal_at
           OR NEW.expires_at IS DISTINCT FROM OLD.expires_at THEN
            RAISE EXCEPTION 'indeterminate operation is immutable pending reconciliation';
        END IF;
    ELSIF OLD.state IN ('completed', 'failed') THEN
        IF NEW.state IS DISTINCT FROM OLD.state
           OR NEW.owner_id IS DISTINCT FROM OLD.owner_id
           OR NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at
           OR NEW.fencing_generation IS DISTINCT FROM OLD.fencing_generation
           OR NEW.outcome IS DISTINCT FROM OLD.outcome
           OR NEW.attempt_id IS DISTINCT FROM OLD.attempt_id
           OR NEW.attempt_identity IS DISTINCT FROM OLD.attempt_identity
           OR NEW.attempt_evidence IS DISTINCT FROM OLD.attempt_evidence
           OR NEW.resolution_evidence IS DISTINCT FROM OLD.resolution_evidence
           OR NEW.terminal_at IS DISTINCT FROM OLD.terminal_at
           OR NEW.expires_at IS DISTINCT FROM OLD.expires_at THEN
            RAISE EXCEPTION 'terminal operation is immutable';
        END IF;
    ELSE
        RAISE EXCEPTION 'invalid operation state transition from % to %', OLD.state, NEW.state;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS operation_lifecycle_guard ON platform.operation;
CREATE TRIGGER operation_lifecycle_guard
    BEFORE UPDATE ON platform.operation
    FOR EACH ROW EXECUTE FUNCTION platform.guard_operation_update();

-- Capability schemas may be applied before the deployment role bundle exists,
-- so grants are conditional. Once provisioned, runtime can mutate operation
-- rows and readonly can only inspect them; PUBLIC receives no access.
REVOKE ALL ON SCHEMA platform FROM PUBLIC;
REVOKE ALL ON TABLE platform.operation FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.prune_operations(timestamptz, integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.guard_operation_insert() FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.guard_operation_update() FROM PUBLIC;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_owner') THEN
        GRANT ALL ON TABLE platform.operation TO leapview_control_owner;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_migrator') THEN
        GRANT ALL ON TABLE platform.operation TO leapview_control_migrator;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA platform TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON TABLE platform.operation TO leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION platform.prune_operations(timestamptz, integer) TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA platform TO leapview_control_readonly;
        GRANT SELECT ON TABLE platform.operation TO leapview_control_readonly;
    END IF;
END
$$;

COMMENT ON TABLE platform.operation IS
    'Durable scoped idempotency records with owner leases and fencing generations';
COMMENT ON COLUMN platform.operation.outcome IS
    'Canonical JSON outcome. jsonb gives semantic exactness on replay and bounds persisted payload size';
