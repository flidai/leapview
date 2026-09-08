-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- +goose StatementBegin
DO $$
BEGIN
    IF (SELECT count(*) FROM pg_roles WHERE rolname IN (
        'leapview_control_owner', 'leapview_control_migrator',
        'leapview_control_runtime', 'leapview_control_maintenance',
        'leapview_control_readonly', 'leapview_control_backup'
    )) <> 6 THEN
        RAISE EXCEPTION 'PostgreSQL control roles must be provisioned before applying the baseline';
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT pg_has_role(current_user, 'leapview_control_owner', 'member') THEN
        RAISE EXCEPTION 'current migration role % must be a member of leapview_control_owner', current_user;
    END IF;
END
$$;
-- +goose StatementEnd

-- Apply has already assumed the NOLOGIN owner role. Objects therefore remain
-- owned by durable authority rather than by the migrator login whose
-- credentials can be rotated independently.
CREATE SCHEMA IF NOT EXISTS platform;

-- capability source: internal/platform/bootstrap/postgres/schema.sql
-- Clean-slate platform bootstrap authority (ADR-0020).
--
-- This capability owns only instance bootstrap state: settings required by
-- startup, one immutable instance identity/environment binding, and the
-- singleton project claim. It deliberately does not inspect delivery,
-- serving-state, managed-data, or product tables owned by other capabilities.

CREATE SCHEMA IF NOT EXISTS platform;

CREATE TABLE IF NOT EXISTS platform.setting (
    key        text PRIMARY KEY,
    value      text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (key = btrim(key) AND octet_length(key) BETWEEN 1 AND 255),
    CHECK (octet_length(value) <= 65536),
    CHECK (updated_at >= created_at)
);

CREATE TABLE IF NOT EXISTS platform.instance_identity (
    singleton_id smallint PRIMARY KEY CHECK (singleton_id = 1),
    instance_id  text NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (instance_id = btrim(instance_id)),
    CHECK (instance_id ~ '^(lvinst_[A-Za-z0-9_-]{32}|instance_[0-9a-f]{32})$')
);

CREATE TABLE IF NOT EXISTS platform.instance_environment (
    singleton_id smallint PRIMARY KEY CHECK (singleton_id = 1),
    environment  text NOT NULL,
    bound_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (environment = btrim(environment)
           AND octet_length(environment) BETWEEN 1 AND 255
           AND environment ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$')
);

CREATE TABLE IF NOT EXISTS platform.instance_project_claim (
    singleton_id smallint PRIMARY KEY CHECK (singleton_id = 1),
    project_id   text NOT NULL,
    environment  text NOT NULL,
    claimed_by   text NOT NULL,
    claimed_at   timestamptz NOT NULL,
    CHECK (project_id = btrim(project_id)
           AND octet_length(project_id) BETWEEN 1 AND 255
           AND project_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (environment = btrim(environment)
           AND octet_length(environment) BETWEEN 1 AND 255
           AND environment ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (claimed_by = btrim(claimed_by)
           AND octet_length(claimed_by) BETWEEN 1 AND 256
           AND claimed_by !~ '[[:cntrl:]]'),
    CHECK (claimed_at IS NOT NULL)
);

-- Settings are the only mutable bootstrap values. The identity, environment,
-- and project claim are immutable authorities; direct UPDATE/DELETE attempts
-- fail even for roles which accidentally receive table-level write grants.
CREATE OR REPLACE FUNCTION platform.reject_bootstrap_immutable_mutation()
RETURNS trigger
LANGUAGE plpgsql
-- +goose StatementBegin
AS $$
BEGIN
    RAISE EXCEPTION 'platform bootstrap identity and claims are immutable';
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS instance_identity_immutable ON platform.instance_identity;
CREATE TRIGGER instance_identity_immutable
    BEFORE UPDATE OR DELETE ON platform.instance_identity
    FOR EACH ROW EXECUTE FUNCTION platform.reject_bootstrap_immutable_mutation();

DROP TRIGGER IF EXISTS instance_environment_immutable ON platform.instance_environment;
CREATE TRIGGER instance_environment_immutable
    BEFORE UPDATE OR DELETE ON platform.instance_environment
    FOR EACH ROW EXECUTE FUNCTION platform.reject_bootstrap_immutable_mutation();

DROP TRIGGER IF EXISTS instance_project_claim_immutable ON platform.instance_project_claim;
CREATE TRIGGER instance_project_claim_immutable
    BEFORE UPDATE OR DELETE ON platform.instance_project_claim
    FOR EACH ROW EXECUTE FUNCTION platform.reject_bootstrap_immutable_mutation();

CREATE OR REPLACE FUNCTION platform.touch_setting_updated_at()
RETURNS trigger
LANGUAGE plpgsql
-- +goose StatementBegin
AS $$
BEGIN
    NEW.updated_at := clock_timestamp();
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS setting_updated_at ON platform.setting;
CREATE TRIGGER setting_updated_at
    BEFORE UPDATE ON platform.setting
    FOR EACH ROW EXECUTE FUNCTION platform.touch_setting_updated_at();

REVOKE ALL ON SCHEMA platform FROM PUBLIC;
REVOKE ALL ON TABLE platform.setting, platform.instance_identity,
    platform.instance_environment, platform.instance_project_claim FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.reject_bootstrap_immutable_mutation() FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.touch_setting_updated_at() FROM PUBLIC;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA platform TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON platform.setting TO leapview_control_runtime;
        GRANT SELECT, INSERT ON platform.instance_identity,
            platform.instance_environment, platform.instance_project_claim
            TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA platform TO leapview_control_readonly;
        GRANT SELECT ON platform.setting, platform.instance_identity,
            platform.instance_environment, platform.instance_project_claim
            TO leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA platform TO leapview_control_backup;
        GRANT SELECT ON platform.setting, platform.instance_identity,
            platform.instance_environment, platform.instance_project_claim
            TO leapview_control_backup;
    END IF;
END
$$;
-- +goose StatementEnd

-- capability source: internal/platform/operation/postgres/schema.sql
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

-- Native build recovery keeps the public operation row immutable after an
-- indeterminate outcome.  A successor execution is therefore an append-only
-- leaf carrying its own owner, lease and attempt fence.  The public row
-- remains the idempotency/replay identity; this table is executable state only.
CREATE TABLE IF NOT EXISTS platform.operation_successor_attempt (
    operation_id                 uuid NOT NULL REFERENCES platform.operation(operation_id) ON DELETE CASCADE,
    predecessor_attempt_id       uuid NOT NULL,
    predecessor_attempt_identity text NOT NULL CHECK (predecessor_attempt_identity = btrim(predecessor_attempt_identity) AND octet_length(predecessor_attempt_identity) BETWEEN 1 AND 512),
    attempt_id                   uuid NOT NULL,
    attempt_identity             text NOT NULL CHECK (attempt_identity = btrim(attempt_identity) AND octet_length(attempt_identity) BETWEEN 1 AND 512),
    owner_id                     text NOT NULL CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255),
    fencing_generation           bigint NOT NULL CHECK (fencing_generation > 0),
    lease_expires_at             timestamptz NOT NULL,
    state                        text NOT NULL CHECK (state IN ('pending', 'indeterminate', 'completed', 'failed')),
    attempt_evidence             jsonb,
    resolution_evidence          jsonb,
    created_at                   timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at                   timestamptz NOT NULL DEFAULT clock_timestamp(),
    terminal_at                  timestamptz,
    PRIMARY KEY (operation_id, predecessor_attempt_id),
    UNIQUE (operation_id, attempt_id),
    CONSTRAINT operation_successor_attempt_terminal_at CHECK (
        (state = 'pending' AND terminal_at IS NULL) OR (state <> 'pending' AND terminal_at IS NOT NULL)
    ),
    CONSTRAINT operation_successor_attempt_evidence_pair CHECK (attempt_evidence IS NULL OR attempt_id IS NOT NULL),
    CONSTRAINT operation_successor_attempt_resolution_pair CHECK (resolution_evidence IS NULL OR state IN ('completed', 'failed')),
    CONSTRAINT operation_successor_attempt_documents_json CHECK (
        (attempt_evidence IS NULL OR (jsonb_typeof(attempt_evidence) = 'object' AND octet_length(attempt_evidence::text) <= 32768))
        AND (resolution_evidence IS NULL OR (jsonb_typeof(resolution_evidence) = 'object' AND octet_length(resolution_evidence::text) <= 32768))
    )
);

CREATE INDEX IF NOT EXISTS operation_successor_attempt_current_idx
    ON platform.operation_successor_attempt (operation_id, created_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS operation_successor_attempt_one_pending_idx
    ON platform.operation_successor_attempt (operation_id)
    WHERE state = 'pending';

CREATE OR REPLACE FUNCTION platform.guard_operation_successor_insert()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, platform
-- +goose StatementBegin
AS $$
DECLARE
    operation_state text;
    operation_attempt uuid;
    operation_identity text;
    predecessor_state text;
    predecessor_identity text;
    predecessor_generation bigint;
    now_ts timestamptz := clock_timestamp();
BEGIN
    IF NEW.state <> 'pending' OR NEW.terminal_at IS NOT NULL
       OR NEW.attempt_evidence IS NOT NULL OR NEW.resolution_evidence IS NOT NULL THEN
        RAISE EXCEPTION 'operation successor inserts must begin as empty pending records';
    END IF;
    IF NEW.created_at > now_ts OR NEW.updated_at > now_ts
       OR NEW.updated_at IS DISTINCT FROM NEW.created_at
       OR NEW.lease_expires_at <= now_ts
       OR NEW.lease_expires_at > now_ts + interval '24 hours' THEN
        RAISE EXCEPTION 'operation successor timestamps or lease expiry are outside the bounded window';
    END IF;
    SELECT state, attempt_id, attempt_identity
      INTO operation_state, operation_attempt, operation_identity
      FROM platform.operation
     WHERE operation_id = NEW.operation_id
       FOR UPDATE;
    IF operation_state IS NULL OR operation_state <> 'indeterminate' THEN
        RAISE EXCEPTION 'operation successor requires an indeterminate public operation';
    END IF;
    IF NEW.predecessor_attempt_id = operation_attempt THEN
        predecessor_state := operation_state;
        predecessor_identity := operation_identity;
        predecessor_generation := (SELECT fencing_generation FROM platform.operation WHERE operation_id = NEW.operation_id);
    ELSE
        SELECT state, attempt_identity, fencing_generation
          INTO predecessor_state, predecessor_identity, predecessor_generation
          FROM platform.operation_successor_attempt
         WHERE operation_id = NEW.operation_id AND attempt_id = NEW.predecessor_attempt_id
         FOR UPDATE;
    END IF;
    -- Attempt IDs are globally unique across the public root and all
    -- successor leaves in one operation chain.  The table-level unique key
    -- covers existing leaves (and deliberately surfaces SQLSTATE 23505 to
    -- the repository); the explicit root check closes the gap between the
    -- public row and its append-only leaves.
    IF NEW.attempt_id = operation_attempt THEN
        RAISE EXCEPTION 'operation successor attempt ID is already used by the operation chain';
    END IF;
    IF predecessor_state IS NULL OR predecessor_state <> 'indeterminate'
       OR predecessor_identity IS NULL
       OR NEW.predecessor_attempt_identity IS DISTINCT FROM predecessor_identity
       OR NEW.attempt_id = NEW.predecessor_attempt_id
       OR NEW.attempt_identity = predecessor_identity THEN
        RAISE EXCEPTION 'operation successor predecessor or fence is invalid';
    END IF;
    IF predecessor_generation IS NULL OR predecessor_generation >= 9223372036854775807
       OR NEW.fencing_generation <> predecessor_generation + 1 THEN
        RAISE EXCEPTION 'operation successor fence must advance exactly once';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS operation_successor_insert_guard ON platform.operation_successor_attempt;
CREATE TRIGGER operation_successor_insert_guard
    BEFORE INSERT ON platform.operation_successor_attempt
    FOR EACH ROW EXECUTE FUNCTION platform.guard_operation_successor_insert();

CREATE OR REPLACE FUNCTION platform.guard_operation_successor_update()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
-- +goose StatementBegin
AS $$
BEGIN
    IF NEW.operation_id IS DISTINCT FROM OLD.operation_id
       OR NEW.predecessor_attempt_id IS DISTINCT FROM OLD.predecessor_attempt_id
       OR NEW.predecessor_attempt_identity IS DISTINCT FROM OLD.predecessor_attempt_identity
       OR NEW.attempt_id IS DISTINCT FROM OLD.attempt_id
       OR NEW.attempt_identity IS DISTINCT FROM OLD.attempt_identity
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.fencing_generation IS DISTINCT FROM OLD.fencing_generation
       OR NEW.owner_id IS DISTINCT FROM OLD.owner_id THEN
        RAISE EXCEPTION 'operation successor identity is immutable';
    END IF;
    IF NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'operation successor updated_at must be monotonic';
    END IF;
    IF NEW.updated_at > clock_timestamp() THEN
        RAISE EXCEPTION 'operation successor updated_at cannot be in the future';
    END IF;
    IF OLD.state = 'pending' AND NEW.state = 'pending' THEN
        IF NEW.attempt_evidence IS DISTINCT FROM OLD.attempt_evidence
           OR NEW.resolution_evidence IS DISTINCT FROM OLD.resolution_evidence
           OR NEW.terminal_at IS DISTINCT FROM OLD.terminal_at
           OR NEW.lease_expires_at < OLD.lease_expires_at THEN
            RAISE EXCEPTION 'pending operation successor evidence is immutable';
        END IF;
        IF NEW.lease_expires_at <= clock_timestamp()
           OR NEW.lease_expires_at > clock_timestamp() + interval '24 hours' THEN
            RAISE EXCEPTION 'pending operation successor lease expiry is outside the bounded window';
        END IF;
    ELSIF OLD.state = 'pending' AND NEW.state = 'indeterminate' THEN
        IF NEW.attempt_evidence IS NULL
           OR NEW.resolution_evidence IS DISTINCT FROM OLD.resolution_evidence
           OR NEW.terminal_at IS DISTINCT FROM NEW.updated_at THEN
            RAISE EXCEPTION 'operation successor indeterminate transition is invalid';
        END IF;
    ELSIF OLD.state IN ('pending', 'indeterminate') AND NEW.state IN ('completed', 'failed') THEN
        IF NEW.attempt_evidence IS DISTINCT FROM OLD.attempt_evidence
           OR NEW.resolution_evidence IS NULL
           OR NEW.terminal_at IS DISTINCT FROM NEW.updated_at
           OR NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at THEN
            RAISE EXCEPTION 'operation successor reconciliation is invalid';
        END IF;
        IF OLD.state = 'pending' AND OLD.lease_expires_at <= clock_timestamp() THEN
            RAISE EXCEPTION 'pending operation successor lease has expired';
        END IF;
    ELSIF OLD.state = NEW.state AND OLD.state IN ('indeterminate', 'completed', 'failed') THEN
        IF NEW.attempt_evidence IS DISTINCT FROM OLD.attempt_evidence
           OR NEW.resolution_evidence IS DISTINCT FROM OLD.resolution_evidence
           OR NEW.terminal_at IS DISTINCT FROM OLD.terminal_at
           OR NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at THEN
            RAISE EXCEPTION 'terminal operation successor is immutable';
        END IF;
    ELSE
        RAISE EXCEPTION 'invalid operation successor state transition from % to %', OLD.state, NEW.state;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS operation_successor_lifecycle_guard ON platform.operation_successor_attempt;
CREATE TRIGGER operation_successor_lifecycle_guard
    BEFORE UPDATE ON platform.operation_successor_attempt
    FOR EACH ROW EXECUTE FUNCTION platform.guard_operation_successor_update();

-- INSERT is guarded separately from UPDATE: a role with INSERT cannot
-- manufacture terminal evidence or choose timestamps/fences outside the
-- repository state machine.
CREATE OR REPLACE FUNCTION platform.guard_operation_insert()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
-- +goose StatementBegin
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
-- +goose StatementEnd

DROP TRIGGER IF EXISTS operation_insert_guard ON platform.operation;
CREATE TRIGGER operation_insert_guard
    BEFORE INSERT ON platform.operation
    FOR EACH ROW EXECUTE FUNCTION platform.guard_operation_insert();

-- Retention cleanup is deliberately a bounded SECURITY DEFINER surface. The
-- runtime role never receives direct DELETE or function EXECUTE, preserving
-- pending/indeterminate evidence for reconciliation and audit. Only the
-- separately authenticated maintenance role may invoke this function.
CREATE OR REPLACE FUNCTION platform.prune_operations(p_before timestamptz, p_limit integer)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, platform
-- +goose StatementBegin
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
        FOR UPDATE SKIP LOCKED
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
-- +goose StatementEnd

-- Keep operation identity and the lease state machine authoritative even for
-- direct SQL callers. Repository transitions remain the normal mutation path,
-- but this guard prevents accidental fencing rollback or terminal reopening.
CREATE OR REPLACE FUNCTION platform.guard_operation_update()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
-- +goose StatementBegin
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
    IF NEW.updated_at > clock_timestamp() THEN
        RAISE EXCEPTION 'operation updated_at cannot be in the future';
    END IF;
    IF NEW.fencing_generation < OLD.fencing_generation THEN
        RAISE EXCEPTION 'operation fencing generation cannot decrease';
    END IF;

    IF OLD.state = 'pending' AND NEW.state = 'pending' THEN
        IF NEW.outcome IS DISTINCT FROM OLD.outcome
           OR NEW.attempt_evidence IS DISTINCT FROM OLD.attempt_evidence
           OR NEW.terminal_at IS DISTINCT FROM OLD.terminal_at
           OR NEW.resolution_evidence IS DISTINCT FROM OLD.resolution_evidence
           OR NEW.expires_at IS DISTINCT FROM OLD.expires_at THEN
            RAISE EXCEPTION 'pending operation evidence is immutable';
        END IF;
        IF NEW.lease_expires_at <= clock_timestamp()
           OR NEW.lease_expires_at > clock_timestamp() + interval '24 hours' THEN
            RAISE EXCEPTION 'pending operation lease expiry is outside the bounded window';
        END IF;
        IF OLD.attempt_id IS NULL AND OLD.attempt_identity IS NULL THEN
            IF (NEW.attempt_id IS NULL) <> (NEW.attempt_identity IS NULL) THEN
                RAISE EXCEPTION 'pending attempt identity must be a pair';
            END IF;
        ELSIF NEW.attempt_id IS DISTINCT FROM OLD.attempt_id
           OR NEW.attempt_identity IS DISTINCT FROM OLD.attempt_identity THEN
            RAISE EXCEPTION 'pending attempt identity can only bind once';
        END IF;
        IF NEW.fencing_generation = OLD.fencing_generation + 1 THEN
            IF OLD.attempt_id IS NOT NULL
               OR OLD.lease_expires_at > clock_timestamp()
               OR NEW.lease_expires_at <= OLD.lease_expires_at THEN
                RAISE EXCEPTION 'pending takeover must advance the fence exactly once';
            END IF;
        ELSIF NEW.fencing_generation = OLD.fencing_generation THEN
            IF NEW.owner_id IS DISTINCT FROM OLD.owner_id THEN
                RAISE EXCEPTION 'pending owner change requires fenced takeover';
            END IF;
            IF OLD.lease_expires_at <= clock_timestamp() THEN
                RAISE EXCEPTION 'expired pending operation requires fenced takeover or reconciliation';
            END IF;
            IF NEW.lease_expires_at < OLD.lease_expires_at THEN
                RAISE EXCEPTION 'pending lease may only extend';
            END IF;
        ELSE
            RAISE EXCEPTION 'pending owner fence must remain stable or advance exactly once';
        END IF;
    ELSIF OLD.state = 'pending' AND NEW.state IN ('completed', 'failed') THEN
        IF NEW.fencing_generation <> OLD.fencing_generation
           OR NEW.owner_id IS DISTINCT FROM OLD.owner_id
           OR NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at
           OR NEW.attempt_id IS DISTINCT FROM OLD.attempt_id
           OR NEW.attempt_identity IS DISTINCT FROM OLD.attempt_identity
           OR NEW.attempt_evidence IS DISTINCT FROM OLD.attempt_evidence
           OR NEW.resolution_evidence IS DISTINCT FROM OLD.resolution_evidence
           OR OLD.lease_expires_at <= clock_timestamp()
           OR NEW.terminal_at IS DISTINCT FROM NEW.updated_at
           OR NEW.expires_at IS DISTINCT FROM NEW.updated_at + OLD.retention_interval THEN
            RAISE EXCEPTION 'terminal completion cannot change the fence';
        END IF;
    ELSIF OLD.state = 'pending' AND NEW.state = 'indeterminate' THEN
        IF NEW.fencing_generation <> OLD.fencing_generation + 1
           OR NEW.owner_id IS DISTINCT FROM OLD.owner_id
           OR NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at
           OR NEW.attempt_id IS DISTINCT FROM OLD.attempt_id
           OR NEW.attempt_identity IS DISTINCT FROM OLD.attempt_identity
           OR NEW.attempt_evidence IS NULL
           OR NEW.outcome IS DISTINCT FROM '{"code":"IDEMPOTENCY_OUTCOME_UNKNOWN","detail":"The original request outcome is indeterminate and requires reconciliation evidence"}'::jsonb
           OR NEW.resolution_evidence IS DISTINCT FROM OLD.resolution_evidence
           OR NEW.terminal_at IS DISTINCT FROM NEW.updated_at
           OR NEW.expires_at IS DISTINCT FROM NEW.updated_at + OLD.retention_interval THEN
            RAISE EXCEPTION 'indeterminate transition must advance the fence';
        END IF;
        IF OLD.attempt_id IS NULL THEN
            RAISE EXCEPTION 'indeterminate transition requires a bound attempt';
        END IF;
        -- Expired attempts carry caller-supplied, canonical evidence (for
        -- example native-build failure evidence). The transition query binds
        -- that evidence to the exact operation/owner/fence/attempt tuple;
        -- only the active-lease path reserves the lease-expiry sentinel.
        IF OLD.lease_expires_at > clock_timestamp()
           AND NEW.attempt_evidence IS NOT DISTINCT FROM '{"code":"IDEMPOTENCY_ATTEMPT_LEASE_EXPIRED","detail":"The operation lease expired after an external attempt was bound"}'::jsonb THEN
            RAISE EXCEPTION 'active attempt transition cannot claim lease-expiry evidence';
        END IF;
    ELSIF OLD.state = 'indeterminate' AND NEW.state IN ('completed', 'failed') THEN
        -- A public root with successor leaves may only be settled by the
        -- successor path, after its current leaf is terminal and carries the
        -- same state/evidence.  This preserves direct-SQL protection while
        -- allowing the repository's public->leaf ordered reconciliation.
        IF EXISTS (
               SELECT 1
               FROM platform.operation_successor_attempt successor
               WHERE successor.operation_id = OLD.operation_id
           )
           AND NOT EXISTS (
               SELECT 1
               FROM platform.operation_successor_attempt successor
               WHERE successor.operation_id = OLD.operation_id
                 AND successor.state = NEW.state
                 AND successor.resolution_evidence IS NOT DISTINCT FROM NEW.resolution_evidence
                 AND NOT EXISTS (
                     SELECT 1
                     FROM platform.operation_successor_attempt child
                     WHERE child.operation_id = successor.operation_id
                       AND child.predecessor_attempt_id = successor.attempt_id
                 )
           ) THEN
            RAISE EXCEPTION 'public operation root cannot reconcile after a successor exists';
        END IF;
        IF NEW.fencing_generation <> OLD.fencing_generation
           OR NEW.owner_id IS DISTINCT FROM OLD.owner_id
           OR NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at
           OR NEW.attempt_id IS DISTINCT FROM OLD.attempt_id
           OR NEW.attempt_identity IS DISTINCT FROM OLD.attempt_identity
           OR NEW.attempt_evidence IS DISTINCT FROM OLD.attempt_evidence
           OR NEW.resolution_evidence IS NULL
           OR NEW.terminal_at IS DISTINCT FROM NEW.updated_at
           OR NEW.expires_at IS DISTINCT FROM NEW.updated_at + OLD.retention_interval THEN
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
           OR NEW.updated_at IS DISTINCT FROM OLD.updated_at
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
-- +goose StatementEnd

DROP TRIGGER IF EXISTS operation_lifecycle_guard ON platform.operation;
CREATE TRIGGER operation_lifecycle_guard
    BEFORE UPDATE ON platform.operation
    FOR EACH ROW EXECUTE FUNCTION platform.guard_operation_update();

-- Capability schemas may be applied before the deployment role bundle exists,
-- so grants are conditional. Once provisioned, runtime can mutate operation
-- rows and readonly can only inspect them; PUBLIC receives no access.
REVOKE ALL ON SCHEMA platform FROM PUBLIC;
REVOKE ALL ON TABLE platform.operation FROM PUBLIC;
REVOKE ALL ON TABLE platform.operation_successor_attempt FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.prune_operations(timestamptz, integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.guard_operation_insert() FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.guard_operation_update() FROM PUBLIC;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_owner') THEN
        GRANT ALL ON TABLE platform.operation TO leapview_control_owner;
        GRANT ALL ON TABLE platform.operation_successor_attempt TO leapview_control_owner;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_migrator') THEN
        GRANT ALL ON TABLE platform.operation TO leapview_control_migrator;
        GRANT ALL ON TABLE platform.operation_successor_attempt TO leapview_control_migrator;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA platform TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON TABLE platform.operation TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON TABLE platform.operation_successor_attempt TO leapview_control_runtime;
        REVOKE EXECUTE ON FUNCTION platform.prune_operations(timestamptz, integer) FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA platform TO leapview_control_maintenance;
        GRANT EXECUTE ON FUNCTION platform.prune_operations(timestamptz, integer) TO leapview_control_maintenance;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA platform TO leapview_control_readonly;
        GRANT SELECT ON TABLE platform.operation, platform.operation_successor_attempt TO leapview_control_readonly;
    END IF;
END
$$;
-- +goose StatementEnd

COMMENT ON TABLE platform.operation IS
    'Durable scoped idempotency records with owner leases and fencing generations';
COMMENT ON COLUMN platform.operation.outcome IS
    'Canonical JSON outcome. jsonb gives semantic exactness on replay and bounds persisted payload size';

-- capability source: internal/platform/http/cursorsigning/postgres/schema.sql
-- PostgreSQL cursor-signing authority. Keys are append-only except for the
-- active/verification state transitions performed by the owning repository.
CREATE SCHEMA IF NOT EXISTS platform;

CREATE TABLE IF NOT EXISTS platform.api_cursor_signing_keys (
    key_id     text PRIMARY KEY CHECK (key_id = btrim(key_id) AND length(key_id) BETWEEN 1 AND 128),
    secret     bytea NOT NULL CHECK (octet_length(secret) >= 32 AND octet_length(secret) <= 4096),
    active     boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    verify_until timestamptz,
    CHECK ((active AND verify_until IS NULL) OR (NOT active AND verify_until IS NOT NULL))
    -- verify_until is the verification-until instant; expired rows are
    -- removed by the bounded security-definer prune function.
);

CREATE UNIQUE INDEX IF NOT EXISTS api_cursor_signing_keys_active_idx
    ON platform.api_cursor_signing_keys (active) WHERE active;

-- Key material is mutable only through the rotation transition. Retired keys
-- remain immutable until the bounded SECURITY DEFINER prune removes them.
CREATE OR REPLACE FUNCTION platform.guard_api_cursor_signing_key_insert()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
-- +goose StatementBegin
AS $$
BEGIN
    IF NEW.active IS DISTINCT FROM true
       OR NEW.verify_until IS NOT NULL THEN
        RAISE EXCEPTION 'cursor signing key inserts must be active with no verification expiry';
    END IF;
    NEW.created_at := clock_timestamp();
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION platform.guard_api_cursor_signing_key_update()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
-- +goose StatementBegin
AS $$
BEGIN
    IF NEW.key_id IS DISTINCT FROM OLD.key_id
       OR NEW.secret IS DISTINCT FROM OLD.secret
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'cursor signing key identity is immutable';
    END IF;
    IF NOT OLD.active THEN
        RAISE EXCEPTION 'retired cursor signing keys are immutable';
    END IF;
    IF OLD.active AND NEW.active THEN
        IF NEW.verify_until IS DISTINCT FROM OLD.verify_until THEN
            RAISE EXCEPTION 'active cursor signing key verification window is immutable';
        END IF;
    ELSIF OLD.active AND NOT NEW.active THEN
        IF NEW.verify_until IS NULL
           OR NEW.verify_until < clock_timestamp() + interval '15 minutes'
           OR NEW.verify_until > clock_timestamp() + interval '24 hours' THEN
            RAISE EXCEPTION 'retired cursor signing key verification window is invalid';
        END IF;
    ELSE
        RAISE EXCEPTION 'invalid cursor signing key state transition';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS api_cursor_signing_keys_insert_guard ON platform.api_cursor_signing_keys;
CREATE TRIGGER api_cursor_signing_keys_insert_guard
    BEFORE INSERT ON platform.api_cursor_signing_keys
    FOR EACH ROW EXECUTE FUNCTION platform.guard_api_cursor_signing_key_insert();

DROP TRIGGER IF EXISTS api_cursor_signing_keys_update_guard ON platform.api_cursor_signing_keys;
CREATE TRIGGER api_cursor_signing_keys_update_guard
    BEFORE UPDATE ON platform.api_cursor_signing_keys
    FOR EACH ROW EXECUTE FUNCTION platform.guard_api_cursor_signing_key_update();

CREATE OR REPLACE FUNCTION platform.prune_expired_cursor_signing_keys(p_limit integer)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, platform
-- +goose StatementBegin
AS $$
DECLARE
    removed bigint;
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 1000 THEN
        RAISE EXCEPTION 'cursor signing key prune limit must be between 1 and 1000';
    END IF;
    WITH doomed AS (
        SELECT key_id
        FROM platform.api_cursor_signing_keys
        WHERE NOT active
          AND verify_until IS NOT NULL
          AND verify_until <= clock_timestamp()
        ORDER BY verify_until, key_id
        FOR UPDATE SKIP LOCKED
        LIMIT p_limit
    )
    DELETE FROM platform.api_cursor_signing_keys
    WHERE key_id IN (SELECT key_id FROM doomed);
    GET DIAGNOSTICS removed = ROW_COUNT;
    RETURN removed;
END;
$$;
-- +goose StatementEnd

-- Metadata excludes secret bytes so readonly diagnostics cannot exfiltrate
-- HMAC material. Runtime reads the base table for process configuration.
CREATE OR REPLACE VIEW platform.api_cursor_signing_key_metadata AS
SELECT key_id, active, created_at, verify_until
FROM platform.api_cursor_signing_keys;

COMMENT ON TABLE platform.api_cursor_signing_keys IS
    'Durable HMAC cursor-signing key ring; exactly one active key signs new cursors';

-- Keep cursor key material out of ambient PUBLIC access. Runtime owns key
-- rotation/configuration; retention cleanup is maintenance-only; readonly
-- consumers may inspect metadata only.
REVOKE ALL ON SCHEMA platform FROM PUBLIC;
REVOKE ALL ON TABLE platform.api_cursor_signing_keys FROM PUBLIC;
REVOKE ALL ON TABLE platform.api_cursor_signing_key_metadata FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.prune_expired_cursor_signing_keys(integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.guard_api_cursor_signing_key_insert() FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.guard_api_cursor_signing_key_update() FROM PUBLIC;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_owner') THEN
        GRANT ALL ON TABLE platform.api_cursor_signing_keys TO leapview_control_owner;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_migrator') THEN
        GRANT ALL ON TABLE platform.api_cursor_signing_keys TO leapview_control_migrator;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA platform TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON TABLE platform.api_cursor_signing_keys TO leapview_control_runtime;
        REVOKE EXECUTE ON FUNCTION platform.prune_expired_cursor_signing_keys(integer) FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA platform TO leapview_control_maintenance;
        GRANT EXECUTE ON FUNCTION platform.prune_expired_cursor_signing_keys(integer) TO leapview_control_maintenance;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA platform TO leapview_control_readonly;
        GRANT SELECT ON TABLE platform.api_cursor_signing_key_metadata TO leapview_control_readonly;
    END IF;
END
$$;
-- +goose StatementEnd

-- capability source: internal/project/postgres/schema.sql
-- Clean-slate project identity authority (ADR-0020).
--
-- The compiler remains authoritative for the project graph.  This schema
-- stores only the durable project identity and the bounded authored metadata
-- needed by control-plane projections.  It deliberately does not recreate
-- the historical SQLite `projects` table or any serving-state projection.

CREATE SCHEMA IF NOT EXISTS project;

CREATE TABLE IF NOT EXISTS project.project_identity (
    project_id   text PRIMARY KEY,
    title        text NOT NULL,
    description  text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (
        project_id = btrim(project_id)
        AND octet_length(project_id) BETWEEN 1 AND 255
        AND project_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'
    ),
    CHECK (
        title = btrim(title)
        AND octet_length(title) BETWEEN 1 AND 255
    ),
    CHECK (octet_length(description) <= 4096),
    CHECK (updated_at >= created_at)
);

-- Project identity and its authored metadata are an immutable authority. A
-- replay with different metadata is rejected by the repository as a hard
-- conflict; direct UPDATE/DELETE attempts are rejected by the database too.
CREATE OR REPLACE FUNCTION project.reject_project_identity_mutation()
RETURNS trigger
LANGUAGE plpgsql
-- +goose StatementBegin
AS $$
BEGIN
    RAISE EXCEPTION 'project identity and authored metadata are immutable';
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS project_identity_immutable ON project.project_identity;
CREATE TRIGGER project_identity_immutable
    BEFORE UPDATE OR DELETE ON project.project_identity
    FOR EACH ROW EXECUTE FUNCTION project.reject_project_identity_mutation();

-- Capability schemas are never reachable through PUBLIC defaults. The
-- explicit runtime grant is deliberately limited to the operations needed by
-- identity ensure and reads; no UPDATE or DELETE privilege is granted.
REVOKE ALL ON SCHEMA project FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA project FROM PUBLIC;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA project FROM PUBLIC;

-- +goose StatementBegin
DO $$
DECLARE
    role_name text;
BEGIN
    FOREACH role_name IN ARRAY ARRAY[
        'leapview_control_runtime',
        'leapview_control_readonly'
    ] LOOP
        IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = role_name) THEN
            EXECUTE format('GRANT USAGE ON SCHEMA project TO %I', role_name);
            IF role_name = 'leapview_control_runtime' THEN
                EXECUTE format('GRANT SELECT, INSERT ON project.project_identity TO %I', role_name);
            ELSE
                EXECUTE format('GRANT SELECT ON project.project_identity TO %I', role_name);
            END IF;
        END IF;
    END LOOP;
END
$$;
-- +goose StatementEnd

-- Immutable source delivery authority.  Source bytes are deliberately not
-- stored here: object_key is a caller-verified reference into the object
-- storage capability and this schema stores only its typed identity.
CREATE TABLE IF NOT EXISTS project.source_blob (
    project_id                text NOT NULL,
    storage_security_domain   text NOT NULL,
    digest                    text NOT NULL,
    size_bytes                bigint NOT NULL,
    object_key                text NOT NULL,
    content_type              text NOT NULL,
    metadata_digest           text NOT NULL,
    created_at                timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (project_id, storage_security_domain, digest),
    UNIQUE (project_id, storage_security_domain, object_key),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255),
    CHECK (storage_security_domain = btrim(storage_security_domain) AND octet_length(storage_security_domain) BETWEEN 1 AND 255),
    CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (size_bytes BETWEEN 0 AND 16777216),
    CHECK (object_key = btrim(object_key) AND octet_length(object_key) BETWEEN 1 AND 2048),
    CHECK (content_type = btrim(content_type) AND octet_length(content_type) BETWEEN 1 AND 255),
    CHECK (metadata_digest ~ '^sha256:[0-9a-f]{64}$')
);

CREATE TABLE IF NOT EXISTS project.source_snapshot (
    snapshot_id                    uuid PRIMARY KEY,
    project_id                     text NOT NULL,
    storage_security_domain        text NOT NULL,
    source_digest                  text NOT NULL,
    project_file                   text NOT NULL,
    project_digest                 text NOT NULL,
    project_artifact_object_key    text NOT NULL,
    project_artifact_digest        text NOT NULL,
    project_artifact_size_bytes    bigint NOT NULL,
    manifest_object_key            text NOT NULL,
    manifest_object_digest         text NOT NULL,
    manifest_object_size_bytes     bigint NOT NULL,
    compiler_version               text NOT NULL,
    schema_version                 bigint NOT NULL,
    state                          text NOT NULL DEFAULT 'building',
    sealed_at                      timestamptz,
    created_at                     timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (project_id, storage_security_domain, source_digest),
    UNIQUE (snapshot_id, project_id, storage_security_domain),
    UNIQUE (snapshot_id, source_digest),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255),
    CHECK (storage_security_domain = btrim(storage_security_domain) AND octet_length(storage_security_domain) BETWEEN 1 AND 255),
    CHECK (source_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (project_file = btrim(project_file) AND octet_length(project_file) BETWEEN 1 AND 1024),
    CHECK (project_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (project_artifact_object_key = btrim(project_artifact_object_key) AND octet_length(project_artifact_object_key) BETWEEN 1 AND 2048),
    CHECK (project_artifact_digest ~ '^sha256:[0-9a-f]{64}$' AND project_artifact_size_bytes BETWEEN 0 AND 67108864),
    CHECK (manifest_object_key = btrim(manifest_object_key) AND octet_length(manifest_object_key) BETWEEN 1 AND 2048),
    CHECK (manifest_object_digest ~ '^sha256:[0-9a-f]{64}$' AND manifest_object_size_bytes BETWEEN 0 AND 67108864),
    CHECK (compiler_version = btrim(compiler_version) AND octet_length(compiler_version) BETWEEN 1 AND 255),
    CHECK (schema_version > 0),
    CHECK (state IN ('building', 'sealed')),
    CHECK ((state = 'sealed') = (sealed_at IS NOT NULL))
);

CREATE TABLE IF NOT EXISTS project.source_snapshot_entry (
    snapshot_id                 uuid NOT NULL,
    project_id                  text NOT NULL,
    storage_security_domain     text NOT NULL,
    path                        text NOT NULL,
    digest                      text NOT NULL,
    size_bytes                  bigint NOT NULL,
    ordinal                     integer NOT NULL,
    PRIMARY KEY (snapshot_id, path),
    UNIQUE (snapshot_id, ordinal),
    FOREIGN KEY (snapshot_id, project_id, storage_security_domain)
        REFERENCES project.source_snapshot(snapshot_id, project_id, storage_security_domain) ON DELETE RESTRICT,
    FOREIGN KEY (project_id, storage_security_domain, digest)
        REFERENCES project.source_blob(project_id, storage_security_domain, digest) ON DELETE RESTRICT,
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255),
    CHECK (storage_security_domain = btrim(storage_security_domain) AND octet_length(storage_security_domain) BETWEEN 1 AND 255),
    CHECK (path = btrim(path) AND octet_length(path) BETWEEN 1 AND 1024),
    CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (size_bytes BETWEEN 0 AND 16777216),
    CHECK (ordinal >= 0)
);

CREATE TABLE IF NOT EXISTS project.source_attestation (
    attestation_id       uuid PRIMARY KEY,
    snapshot_id          uuid NOT NULL,
    source_digest        text NOT NULL,
    attestation_digest   text NOT NULL,
    payload              jsonb NOT NULL,
    revision             text NOT NULL DEFAULT '',
    repository           text NOT NULL DEFAULT '',
    ref                  text NOT NULL DEFAULT '',
    change_id            text NOT NULL DEFAULT '',
    created_at           timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (snapshot_id, attestation_digest),
    FOREIGN KEY (snapshot_id, source_digest)
        REFERENCES project.source_snapshot(snapshot_id, source_digest) ON DELETE RESTRICT,
    CHECK (source_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (attestation_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (jsonb_typeof(payload) = 'object' AND octet_length(payload::text) <= 16384),
    CHECK (octet_length(revision) <= 1024 AND octet_length(repository) <= 1024 AND octet_length(ref) <= 1024 AND octet_length(change_id) <= 1024)
);

CREATE TABLE IF NOT EXISTS project.source_sync_plan (
    plan_id                    uuid PRIMARY KEY,
    operation_id               uuid NOT NULL UNIQUE,
    project_id                 text NOT NULL,
    storage_security_domain    text NOT NULL,
    owner_id                   text NOT NULL,
    candidate_key              text NOT NULL,
    source_digest              text NOT NULL,
    project_file               text NOT NULL,
    request_digest             text NOT NULL,
    state                      text NOT NULL DEFAULT 'open',
    expires_at                 timestamptz NOT NULL,
    created_at                 timestamptz NOT NULL DEFAULT clock_timestamp(),
    committed_at               timestamptz,
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255),
    CHECK (storage_security_domain = btrim(storage_security_domain) AND octet_length(storage_security_domain) BETWEEN 1 AND 255),
    CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255),
    CHECK (candidate_key = btrim(candidate_key) AND octet_length(candidate_key) BETWEEN 1 AND 512),
    CHECK (source_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (project_file = btrim(project_file) AND octet_length(project_file) BETWEEN 1 AND 1024),
    CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (state IN ('open', 'committed', 'expired')),
    CHECK (expires_at > created_at AND expires_at <= created_at + interval '5 minutes'),
    CHECK ((state = 'committed') = (committed_at IS NOT NULL))
);

CREATE TABLE IF NOT EXISTS project.source_sync_plan_entry (
    plan_id                  uuid NOT NULL,
    path                     text NOT NULL,
    digest                   text NOT NULL,
    size_bytes               bigint NOT NULL,
    ordinal                  integer NOT NULL,
    PRIMARY KEY (plan_id, path),
    UNIQUE (plan_id, ordinal),
    FOREIGN KEY (plan_id) REFERENCES project.source_sync_plan(plan_id) ON DELETE RESTRICT,
    CHECK (path = btrim(path) AND octet_length(path) BETWEEN 1 AND 1024),
    CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (size_bytes BETWEEN 0 AND 16777216),
    CHECK (ordinal >= 0)
);

CREATE OR REPLACE FUNCTION project.reject_source_immutable_mutation()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, project AS $$
BEGIN
    RAISE EXCEPTION 'project source history is immutable';
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION project.guard_source_sync_plan_mutation()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, project AS $$
BEGIN
    IF NEW.plan_id IS DISTINCT FROM OLD.plan_id OR NEW.operation_id IS DISTINCT FROM OLD.operation_id
       OR NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.storage_security_domain IS DISTINCT FROM OLD.storage_security_domain
       OR NEW.owner_id IS DISTINCT FROM OLD.owner_id OR NEW.candidate_key IS DISTINCT FROM OLD.candidate_key
       OR NEW.source_digest IS DISTINCT FROM OLD.source_digest OR NEW.project_file IS DISTINCT FROM OLD.project_file
       OR NEW.request_digest IS DISTINCT FROM OLD.request_digest OR NEW.expires_at IS DISTINCT FROM OLD.expires_at
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'source synchronization plan identity is immutable';
    END IF;
    IF OLD.state <> 'open' OR NEW.state NOT IN ('committed', 'expired') OR NEW.state = OLD.state THEN
        RAISE EXCEPTION 'source synchronization plan transition is invalid';
    END IF;
    IF NEW.state = 'committed' AND NEW.committed_at IS NULL THEN
        NEW.committed_at := clock_timestamp();
    END IF;
    IF NEW.state = 'expired' AND NEW.committed_at IS NOT NULL THEN
        RAISE EXCEPTION 'expired source synchronization plan cannot have committed_at';
    END IF;
    IF NEW.state = 'expired' AND clock_timestamp() < OLD.expires_at THEN
        RAISE EXCEPTION 'source synchronization plan has not expired';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION project.guard_source_sync_plan_entry_insert()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, project AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM project.source_sync_plan p
        WHERE p.plan_id = NEW.plan_id AND p.state = 'open'
          AND p.expires_at > clock_timestamp()
    ) THEN
        RAISE EXCEPTION 'source synchronization plan is not open';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION project.guard_source_snapshot_mutation()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, project AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'project source history is immutable';
    END IF;
    IF NEW.snapshot_id IS DISTINCT FROM OLD.snapshot_id OR NEW.project_id IS DISTINCT FROM OLD.project_id
       OR NEW.storage_security_domain IS DISTINCT FROM OLD.storage_security_domain OR NEW.source_digest IS DISTINCT FROM OLD.source_digest
       OR NEW.project_file IS DISTINCT FROM OLD.project_file OR NEW.project_digest IS DISTINCT FROM OLD.project_digest
       OR NEW.project_artifact_object_key IS DISTINCT FROM OLD.project_artifact_object_key
       OR NEW.project_artifact_digest IS DISTINCT FROM OLD.project_artifact_digest
       OR NEW.project_artifact_size_bytes IS DISTINCT FROM OLD.project_artifact_size_bytes
       OR NEW.manifest_object_key IS DISTINCT FROM OLD.manifest_object_key
       OR NEW.manifest_object_digest IS DISTINCT FROM OLD.manifest_object_digest
       OR NEW.manifest_object_size_bytes IS DISTINCT FROM OLD.manifest_object_size_bytes
       OR NEW.compiler_version IS DISTINCT FROM OLD.compiler_version OR NEW.schema_version IS DISTINCT FROM OLD.schema_version
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'project source snapshot identity is immutable';
    END IF;
    IF OLD.state <> 'building' OR NEW.state <> 'sealed' OR NEW.sealed_at IS NOT NULL THEN
        RAISE EXCEPTION 'project source snapshot transition is invalid';
    END IF;
    NEW.sealed_at := clock_timestamp();
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION project.guard_source_snapshot_child_insert()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, project AS $$
DECLARE parent_id uuid;
BEGIN
    parent_id := NEW.snapshot_id;
    IF NOT EXISTS (
        SELECT 1 FROM project.source_snapshot s
        WHERE s.snapshot_id = parent_id AND s.state = 'building'
    ) THEN
        RAISE EXCEPTION 'project source snapshot is not building';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS source_blob_immutable ON project.source_blob;
CREATE TRIGGER source_blob_immutable BEFORE UPDATE OR DELETE ON project.source_blob FOR EACH ROW EXECUTE FUNCTION project.reject_source_immutable_mutation();
DROP TRIGGER IF EXISTS source_snapshot_immutable ON project.source_snapshot;
CREATE TRIGGER source_snapshot_immutable BEFORE UPDATE OR DELETE ON project.source_snapshot FOR EACH ROW EXECUTE FUNCTION project.guard_source_snapshot_mutation();
DROP TRIGGER IF EXISTS source_snapshot_entry_immutable ON project.source_snapshot_entry;
CREATE TRIGGER source_snapshot_entry_immutable BEFORE UPDATE OR DELETE ON project.source_snapshot_entry FOR EACH ROW EXECUTE FUNCTION project.reject_source_immutable_mutation();
DROP TRIGGER IF EXISTS source_attestation_immutable ON project.source_attestation;
CREATE TRIGGER source_attestation_immutable BEFORE UPDATE OR DELETE ON project.source_attestation FOR EACH ROW EXECUTE FUNCTION project.reject_source_immutable_mutation();
DROP TRIGGER IF EXISTS source_sync_plan_entry_immutable ON project.source_sync_plan_entry;
CREATE TRIGGER source_sync_plan_entry_immutable BEFORE UPDATE OR DELETE ON project.source_sync_plan_entry FOR EACH ROW EXECUTE FUNCTION project.reject_source_immutable_mutation();
DROP TRIGGER IF EXISTS source_sync_plan_entry_admission ON project.source_sync_plan_entry;
CREATE TRIGGER source_sync_plan_entry_admission BEFORE INSERT ON project.source_sync_plan_entry FOR EACH ROW EXECUTE FUNCTION project.guard_source_sync_plan_entry_insert();
DROP TRIGGER IF EXISTS source_sync_plan_transition ON project.source_sync_plan;
CREATE TRIGGER source_sync_plan_transition BEFORE UPDATE ON project.source_sync_plan FOR EACH ROW EXECUTE FUNCTION project.guard_source_sync_plan_mutation();
DROP TRIGGER IF EXISTS source_snapshot_entry_admission ON project.source_snapshot_entry;
CREATE TRIGGER source_snapshot_entry_admission BEFORE INSERT ON project.source_snapshot_entry FOR EACH ROW EXECUTE FUNCTION project.guard_source_snapshot_child_insert();
DROP TRIGGER IF EXISTS source_attestation_admission ON project.source_attestation;
CREATE TRIGGER source_attestation_admission BEFORE INSERT ON project.source_attestation FOR EACH ROW EXECUTE FUNCTION project.guard_source_snapshot_child_insert();

REVOKE ALL ON TABLE project.source_blob, project.source_snapshot, project.source_snapshot_entry,
    project.source_attestation, project.source_sync_plan, project.source_sync_plan_entry FROM PUBLIC;
REVOKE ALL ON FUNCTION project.reject_source_immutable_mutation(), project.guard_source_sync_plan_mutation(),
    project.guard_source_sync_plan_entry_insert(), project.guard_source_snapshot_mutation(),
    project.guard_source_snapshot_child_insert() FROM PUBLIC;
-- +goose StatementBegin
DO $$
DECLARE role_name text;
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        EXECUTE 'REVOKE UPDATE, DELETE ON project.source_blob, project.source_snapshot, project.source_snapshot_entry, project.source_attestation, project.source_sync_plan_entry FROM leapview_control_runtime';
    END IF;
    FOREACH role_name IN ARRAY ARRAY['leapview_control_owner','leapview_control_migrator','leapview_control_runtime','leapview_control_readonly','leapview_control_backup'] LOOP
        IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = role_name) THEN
            EXECUTE format('GRANT USAGE ON SCHEMA project TO %I', role_name);
            IF role_name IN ('leapview_control_owner','leapview_control_migrator') THEN
                EXECUTE format('GRANT ALL ON ALL TABLES IN SCHEMA project TO %I', role_name);
            ELSIF role_name = 'leapview_control_runtime' THEN
                EXECUTE 'GRANT SELECT, INSERT ON project.source_blob, project.source_snapshot, project.source_snapshot_entry, project.source_attestation, project.source_sync_plan, project.source_sync_plan_entry TO leapview_control_runtime';
                EXECUTE 'GRANT UPDATE (state, committed_at) ON project.source_sync_plan TO leapview_control_runtime';
                EXECUTE 'GRANT UPDATE (state, sealed_at) ON project.source_snapshot TO leapview_control_runtime';
            ELSE
                EXECUTE format('GRANT SELECT ON ALL TABLES IN SCHEMA project TO %I', role_name);
            END IF;
        END IF;
    END LOOP;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        EXECUTE 'GRANT EXECUTE ON FUNCTION project.guard_source_sync_plan_mutation() TO leapview_control_runtime';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_owner') THEN
        EXECUTE 'GRANT EXECUTE ON FUNCTION project.reject_source_immutable_mutation(), project.guard_source_sync_plan_mutation(), project.guard_source_sync_plan_entry_insert(), project.guard_source_snapshot_mutation(), project.guard_source_snapshot_child_insert() TO leapview_control_owner';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_migrator') THEN
        EXECUTE 'GRANT EXECUTE ON FUNCTION project.reject_source_immutable_mutation(), project.guard_source_sync_plan_mutation(), project.guard_source_sync_plan_entry_insert(), project.guard_source_snapshot_mutation(), project.guard_source_snapshot_child_insert() TO leapview_control_migrator';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        REVOKE ALL ON SCHEMA project FROM leapview_control_maintenance;
        REVOKE ALL ON TABLE project.source_blob, project.source_snapshot, project.source_snapshot_entry,
            project.source_attestation, project.source_sync_plan, project.source_sync_plan_entry FROM leapview_control_maintenance;
    END IF;
END
$$;
-- +goose StatementEnd

-- capability source: internal/access/postgres/schema.sql
-- Clean PostgreSQL access authority baseline. This file is applied to an
-- empty control database by the access capability migration; it deliberately
-- contains no compatibility ALTERs or SQLite-era projections.
CREATE SCHEMA IF NOT EXISTS access;
CREATE SCHEMA IF NOT EXISTS audit;
CREATE TABLE IF NOT EXISTS access.platform_setting (
    key text PRIMARY KEY CHECK (key = btrim(key) AND length(key) BETWEEN 1 AND 255),
    value text NOT NULL CHECK (length(value)<=2048),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE IF NOT EXISTS audit.audit_event (
    audit_id uuid PRIMARY KEY,
    -- Delivery activation uses the same immutable identity for its durable
    -- domain event and audit record.  Keep that relationship on the
    -- access-owned audit authority rather than introducing a second audit
    -- table in the deployment capability.
    event_id uuid UNIQUE,
    actor_id text CHECK (actor_id IS NULL OR (actor_id = btrim(actor_id) AND length(actor_id) BETWEEN 1 AND 255)),
    scope_id text CHECK (scope_id IS NULL OR (scope_id = btrim(scope_id) AND length(scope_id) BETWEEN 1 AND 255)),
    principal_id uuid,
    source text NOT NULL CHECK (length(source)<=128),
    operation text NOT NULL CHECK (length(operation)<=255),
    action text NOT NULL CHECK (length(action)<=255),
    resource_kind text,
    resource_id text,
    project_id text CHECK (project_id IS NULL OR (project_id = btrim(project_id) AND length(project_id) BETWEEN 1 AND 255)),
    environment text CHECK (environment IS NULL OR (environment = btrim(environment) AND length(environment) BETWEEN 1 AND 128)),
    generation_id text CHECK (generation_id IS NULL OR (generation_id = btrim(generation_id) AND length(generation_id) BETWEEN 1 AND 255)),
    capability text NOT NULL DEFAULT '',
    outcome text NOT NULL DEFAULT 'success',
    request_digest text CHECK (request_digest IS NULL OR request_digest ~ '^sha256:[0-9a-f]{64}$'),
    request_id text CHECK (request_id IS NULL OR (request_id = btrim(request_id) AND length(request_id) BETWEEN 1 AND 256)),
    correlation_id text CHECK (correlation_id IS NULL OR (correlation_id = btrim(correlation_id) AND length(correlation_id) BETWEEN 1 AND 256)),
    aggregate_key text NOT NULL DEFAULT '',
    aggregate_sequence bigint NOT NULL DEFAULT 0,
    intent_digest text NOT NULL DEFAULT '',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata)='object' AND octet_length(metadata::text)<=32768),
    occurred_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX IF NOT EXISTS audit_event_retention_order_idx
    ON audit.audit_event (occurred_at, audit_id);

-- The floor is durable evidence of the policy boundary used by the last
-- bounded retention batch.  It is a cursor, not an authorization shortcut:
-- append-only producers continue to write audit_event directly.
CREATE TABLE IF NOT EXISTS audit.audit_retention_floor (
    retention_class text PRIMARY KEY CHECK (retention_class IN ('short', 'standard', 'security')),
    floor_at timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00'::timestamptz,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
INSERT INTO audit.audit_retention_floor (retention_class)
VALUES ('short'), ('standard'), ('security')
ON CONFLICT (retention_class) DO NOTHING;

-- Operational auth state has one monotonic floor. Audit events are final
-- immutable inserts on PostgreSQL (there is no same-database outbox).
CREATE TABLE IF NOT EXISTS access.access_retention_floor (
    retention_class text PRIMARY KEY CHECK (retention_class = 'auth_state'),
    floor_at timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00'::timestamptz,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
INSERT INTO access.access_retention_floor (retention_class)
VALUES ('auth_state')
ON CONFLICT (retention_class) DO NOTHING;

-- Audit history is immutable to runtime callers.  A deletion can only be
-- reached through the bounded SECURITY DEFINER function below, which sets a
-- transaction-local marker and is itself executable only by maintenance.
CREATE OR REPLACE FUNCTION audit.reject_audit_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
-- +goose StatementBegin
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        -- The database owns evidence time.  Even an owner or runtime caller
        -- supplying an explicit age cannot make a fresh event look old.
        NEW.occurred_at := statement_timestamp();
        RETURN NEW;
    END IF;
    IF TG_OP = 'DELETE'
       AND current_setting('audit.maintenance', true) = 'on'
       AND session_user = 'leapview_control_maintenance' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'audit history is immutable';
END;
$$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS audit_event_immutable ON audit.audit_event;
CREATE TRIGGER audit_event_immutable BEFORE INSERT OR UPDATE OR DELETE ON audit.audit_event FOR EACH ROW EXECUTE FUNCTION audit.reject_audit_mutation();

-- Retention is a maintenance capability, never a runtime table privilege.
-- Every invocation is capped by the database clock and one bounded candidate
-- batch.  Candidate rows are inspected and locked before eligibility is
-- decided; malformed retention envelopes are retained for operator review
-- rather than silently discarded.  Valid envelopes (short, standard, or
-- security) follow the explicitly supplied policy cutoff.
CREATE OR REPLACE FUNCTION audit.prune_audit_events(
    p_retention_class text,
    p_requested_cutoff timestamptz,
    p_batch_limit integer
)
RETURNS TABLE (
    retention_class text,
    requested_cutoff timestamptz,
    cutoff timestamptz,
    requested_limit integer,
    removed_count bigint,
    retained_floor timestamptz
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, audit
-- +goose StatementBegin
AS $$
DECLARE
    v_floor timestamptz;
    v_target timestamptz;
    v_remaining timestamptz;
    v_removed bigint := 0;
BEGIN
    IF session_user <> 'leapview_control_maintenance' THEN
        RAISE EXCEPTION 'audit retention requires the maintenance capability';
    END IF;
    IF p_retention_class IS NULL OR p_retention_class NOT IN ('short', 'standard', 'security') THEN
        RAISE EXCEPTION 'audit retention class must be short, standard, or security';
    END IF;
    IF p_requested_cutoff IS NULL THEN
        RAISE EXCEPTION 'audit retention cutoff is required';
    END IF;
    IF p_batch_limit IS NULL OR p_batch_limit < 1 OR p_batch_limit > 1000 THEN
        RAISE EXCEPTION 'audit retention batch limit must be between 1 and 1000';
    END IF;

    SELECT f.floor_at INTO STRICT v_floor
      FROM audit.audit_retention_floor f
     WHERE f.retention_class = p_retention_class
     FOR UPDATE;

    retention_class := p_retention_class;
    requested_cutoff := p_requested_cutoff;
    requested_limit := p_batch_limit;
    -- Never let an operator-provided future cutoff delete newly-written rows.
    v_target := GREATEST(v_floor, LEAST(p_requested_cutoff, clock_timestamp()));

    cutoff := v_target;

    -- The CTE first locks and inspects the exact rows to be removed.  The
    -- trigger marker is transaction-local and cannot be used by runtime SQL,
    -- which has neither DELETE nor EXECUTE privilege.
    PERFORM set_config('audit.maintenance', 'on', true);
    WITH candidates AS (
        SELECT e.audit_id, e.occurred_at, e.source, e.operation,
               e.action, e.outcome, e.metadata
         FROM audit.audit_event e
         WHERE e.occurred_at < v_target
           AND CASE
                 WHEN e.metadata ? 'retention' THEN e.metadata->>'retention' = p_retention_class
                 ELSE p_retention_class = 'standard'
               END
         ORDER BY e.occurred_at, e.audit_id
         FOR UPDATE SKIP LOCKED
         LIMIT p_batch_limit
    ), deleted AS (
        DELETE FROM audit.audit_event e
         USING candidates c
         WHERE e.audit_id = c.audit_id
         RETURNING e.audit_id
    )
    SELECT count(*) INTO v_removed FROM deleted;

    -- Derive the floor from the rows still visible after the bounded delete.
    -- This keeps a full backlog, or a row skipped by a concurrent lock, from
    -- being represented as already retained past the actual evidence.
    SELECT min(e.occurred_at) INTO v_remaining
      FROM audit.audit_event e
     WHERE e.occurred_at < v_target
       AND CASE
             WHEN e.metadata ? 'retention' THEN e.metadata->>'retention' = p_retention_class
             ELSE p_retention_class = 'standard'
           END;
    IF v_remaining IS NULL THEN
        v_remaining := v_target;
    END IF;
    IF v_remaining > v_floor THEN
        UPDATE audit.audit_retention_floor
           SET floor_at = v_remaining, updated_at = clock_timestamp()
         WHERE audit_retention_floor.retention_class = p_retention_class;
        v_floor := v_remaining;
    END IF;
    cutoff := v_target;
    removed_count := v_removed;
    retained_floor := v_floor;
    RETURN NEXT;
END;
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION audit.prune_audit_events(text, timestamptz, integer) FROM PUBLIC;

-- Remove expired or explicitly revoked access credentials in one bounded
-- batch. Every candidate is locked before deletion, so concurrent maintenance
-- workers do not double-delete and a locked backlog keeps the durable floor
-- below the requested boundary. Final audit events use their independent
-- class-based retention function above; PostgreSQL has no audit outbox.
CREATE OR REPLACE FUNCTION access.prune_auth_state(
    p_requested_cutoff timestamptz,
    p_batch_limit integer
)
RETURNS TABLE (
    requested_cutoff timestamptz,
    cutoff timestamptz,
    requested_limit integer,
    sessions_removed bigint,
    oauth_sessions_removed bigint,
    oauth_assertions_removed bigint,
    desktop_codes_removed bigint,
    device_authorizations_removed bigint,
    api_tokens_removed bigint,
    service_secrets_removed bigint,
    authoring_sessions_removed bigint,
    authoring_credentials_removed bigint,
    auth_state_floor timestamptz
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, access, audit
-- +goose StatementBegin
AS $$
DECLARE
    v_auth_floor timestamptz;
    v_target timestamptz;
    v_total bigint := 0;
    v_removed bigint := 0;
    v_remaining integer;
    v_auth_remaining boolean;
BEGIN
    IF session_user <> 'leapview_control_maintenance' THEN
        RAISE EXCEPTION 'access retention requires the maintenance capability';
    END IF;
    IF p_requested_cutoff IS NULL THEN
        RAISE EXCEPTION 'access retention cutoff is required';
    END IF;
    IF p_batch_limit IS NULL OR p_batch_limit < 1 OR p_batch_limit > 1000 THEN
        RAISE EXCEPTION 'access retention batch limit must be between 1 and 1000';
    END IF;

    SELECT floor_at INTO STRICT v_auth_floor
      FROM access.access_retention_floor
     WHERE retention_class = 'auth_state'
     FOR UPDATE;
    requested_cutoff := p_requested_cutoff;
    requested_limit := p_batch_limit;
    -- Database time is authoritative. A replay with an older requested
    -- cutoff must not widen the deletion predicate to the already-advanced
    -- floor; the floor itself remains monotonic as durable evidence.
    v_target := LEAST(p_requested_cutoff, clock_timestamp());
    cutoff := v_target;
    PERFORM set_config('access.maintenance', 'on', true);

    -- Inactive OAuth request state is opaque but no longer usable. Active
    -- sessions are retained even when old so token replay evidence remains
    -- available to the runtime verifier.
    v_remaining := p_batch_limit;
    WITH candidates AS (
        SELECT s.kind, s.signature
          FROM access.oauth_session s
         WHERE s.created_at < v_target AND s.active = false
         ORDER BY s.created_at, s.kind, s.signature
         FOR UPDATE SKIP LOCKED
         LIMIT v_remaining
    ), deleted AS (
        DELETE FROM access.oauth_session s USING candidates c
         WHERE s.kind = c.kind AND s.signature = c.signature
         RETURNING s.signature
    )
    SELECT count(*) INTO v_removed FROM deleted;
    oauth_sessions_removed := v_removed;
    v_total := v_total + v_removed;

    IF v_total < p_batch_limit THEN
        v_remaining := p_batch_limit - v_total::integer;
        WITH candidates AS (
            SELECT a.jti
              FROM access.oauth_client_assertion a
             WHERE a.expires_at < v_target
             ORDER BY a.expires_at, a.jti
             FOR UPDATE SKIP LOCKED
             LIMIT v_remaining
        ), deleted AS (
            DELETE FROM access.oauth_client_assertion a USING candidates c
             WHERE a.jti = c.jti
             RETURNING a.jti
        )
        SELECT count(*) INTO v_removed FROM deleted;
    ELSE
        v_removed := 0;
    END IF;
    oauth_assertions_removed := v_removed;
    v_total := v_total + v_removed;

    IF v_total < p_batch_limit THEN
        v_remaining := p_batch_limit - v_total::integer;
        WITH candidates AS (
            SELECT c.code_hash
              FROM access.desktop_authorization_code c
             WHERE c.expires_at < v_target
                OR (c.consumed_at IS NOT NULL AND c.consumed_at < v_target)
             ORDER BY c.expires_at, c.code_hash
             FOR UPDATE SKIP LOCKED
             LIMIT v_remaining
        ), deleted AS (
            DELETE FROM access.desktop_authorization_code c USING candidates d
             WHERE c.code_hash = d.code_hash
             RETURNING c.code_hash
        )
        SELECT count(*) INTO v_removed FROM deleted;
    ELSE
        v_removed := 0;
    END IF;
    desktop_codes_removed := v_removed;
    v_total := v_total + v_removed;

    IF v_total < p_batch_limit THEN
        v_remaining := p_batch_limit - v_total::integer;
        WITH candidates AS (
            SELECT d.id
              FROM access.device_authorization d
             WHERE d.expires_at < v_target
                OR (d.status = 'denied' AND d.denied_at IS NOT NULL AND d.denied_at < v_target)
                OR (d.status = 'consumed' AND d.consumed_at IS NOT NULL AND d.consumed_at < v_target)
             ORDER BY d.expires_at, d.id
             FOR UPDATE SKIP LOCKED
             LIMIT v_remaining
        ), deleted AS (
            DELETE FROM access.device_authorization d USING candidates c
             WHERE d.id = c.id
             RETURNING d.id
        )
        SELECT count(*) INTO v_removed FROM deleted;
    ELSE
        v_removed := 0;
    END IF;
    device_authorizations_removed := v_removed;
    v_total := v_total + v_removed;

    IF v_total < p_batch_limit THEN
        v_remaining := p_batch_limit - v_total::integer;
        WITH candidates AS (
            SELECT t.id
              FROM access.api_token t
             WHERE (t.expires_at < v_target)
                OR (t.revoked_at IS NOT NULL AND t.revoked_at < v_target)
             ORDER BY LEAST(t.expires_at, COALESCE(t.revoked_at, t.expires_at)), t.id
             FOR UPDATE SKIP LOCKED
             LIMIT v_remaining
        ), deleted AS (
            DELETE FROM access.api_token t USING candidates c
             WHERE t.id = c.id
             RETURNING t.id
        )
        SELECT count(*) INTO v_removed FROM deleted;
    ELSE
        v_removed := 0;
    END IF;
    api_tokens_removed := v_removed;
    v_total := v_total + v_removed;

    IF v_total < p_batch_limit THEN
        v_remaining := p_batch_limit - v_total::integer;
        WITH candidates AS (
            SELECT s.id
              FROM access.service_principal_secret s
             WHERE (s.expires_at < v_target)
                OR (s.revoked_at IS NOT NULL AND s.revoked_at < v_target)
             ORDER BY LEAST(s.expires_at, COALESCE(s.revoked_at, s.expires_at)), s.id
             FOR UPDATE SKIP LOCKED
             LIMIT v_remaining
        ), deleted AS (
            DELETE FROM access.service_principal_secret s USING candidates c
             WHERE s.id = c.id
             RETURNING s.id
        )
        SELECT count(*) INTO v_removed FROM deleted;
    ELSE
        v_removed := 0;
    END IF;
    service_secrets_removed := v_removed;
    v_total := v_total + v_removed;

    IF v_total < p_batch_limit THEN
        v_remaining := p_batch_limit - v_total::integer;
        WITH candidates AS (
            SELECT s.id
              FROM access.session s
             WHERE (s.expires_at < v_target)
                OR (s.revoked_at IS NOT NULL AND s.revoked_at < v_target)
             ORDER BY LEAST(s.expires_at, COALESCE(s.revoked_at, s.expires_at)), s.id
             FOR UPDATE SKIP LOCKED
             LIMIT v_remaining
        ), deleted AS (
            DELETE FROM access.session s USING candidates c
             WHERE s.id = c.id
             RETURNING s.id
        )
        SELECT count(*) INTO v_removed FROM deleted;
    ELSE
        v_removed := 0;
    END IF;
    sessions_removed := v_removed;
    v_total := v_total + v_removed;

    -- Credentials are children of authoring sessions and must drain first;
    -- a revoked parent invalidates its credentials even when their refresh
    -- expiry is still in the future.
    IF v_total < p_batch_limit THEN
        v_remaining := p_batch_limit - v_total::integer;
        WITH candidates AS (
            SELECT c.id
              FROM access.authoring_credential c
              JOIN access.authoring_session s ON s.id = c.session_id
             WHERE (s.revoked_at IS NOT NULL AND s.revoked_at < v_target)
                OR (c.replaced_at IS NOT NULL AND c.replaced_at < v_target)
                OR (c.refresh_expires_at IS NOT NULL AND c.refresh_expires_at < v_target)
                OR (c.refresh_expires_at IS NULL AND c.access_expires_at < v_target)
             ORDER BY c.created_at, c.id
             FOR UPDATE OF c SKIP LOCKED
             LIMIT v_remaining
        ), deleted AS (
            DELETE FROM access.authoring_credential c USING candidates d
             WHERE c.id = d.id
             RETURNING c.id
        )
        SELECT count(*) INTO v_removed FROM deleted;
    ELSE
        v_removed := 0;
    END IF;
    authoring_credentials_removed := v_removed;
    v_total := v_total + v_removed;

    IF v_total < p_batch_limit THEN
        v_remaining := p_batch_limit - v_total::integer;
        WITH candidates AS (
            SELECT s.id
              FROM access.authoring_session s
             WHERE (s.expires_at < v_target)
                OR (s.revoked_at IS NOT NULL AND s.revoked_at < v_target)
             ORDER BY LEAST(s.expires_at, COALESCE(s.revoked_at, s.expires_at)), s.id
             FOR UPDATE SKIP LOCKED
             LIMIT v_remaining
        ), deleted AS (
            DELETE FROM access.authoring_session s USING candidates c
             WHERE s.id = c.id
               AND NOT EXISTS (SELECT 1 FROM access.authoring_credential x WHERE x.session_id = s.id)
             RETURNING s.id
        )
        SELECT count(*) INTO v_removed FROM deleted;
    ELSE
        v_removed := 0;
    END IF;
    authoring_sessions_removed := v_removed;
    v_total := v_total + v_removed;

    -- Floors only advance when no eligible row remains. This is deliberately
    -- checked after the batch so a smaller limit or SKIP LOCKED row remains
    -- visible as backlog evidence to the next invocation.
    SELECT EXISTS (
        SELECT 1 FROM access.session s
         WHERE (s.expires_at < v_target) OR (s.revoked_at IS NOT NULL AND s.revoked_at < v_target)
        UNION ALL SELECT 1 FROM access.oauth_session s WHERE s.created_at < v_target AND s.active = false
        UNION ALL SELECT 1 FROM access.oauth_client_assertion a WHERE a.expires_at < v_target
        UNION ALL SELECT 1 FROM access.desktop_authorization_code c WHERE c.expires_at < v_target OR (c.consumed_at IS NOT NULL AND c.consumed_at < v_target)
        UNION ALL SELECT 1 FROM access.device_authorization d WHERE d.expires_at < v_target OR (d.status='denied' AND d.denied_at IS NOT NULL AND d.denied_at < v_target) OR (d.status='consumed' AND d.consumed_at IS NOT NULL AND d.consumed_at < v_target)
        UNION ALL SELECT 1 FROM access.api_token t WHERE t.expires_at < v_target OR (t.revoked_at IS NOT NULL AND t.revoked_at < v_target)
        UNION ALL SELECT 1 FROM access.service_principal_secret s WHERE s.expires_at < v_target OR (s.revoked_at IS NOT NULL AND s.revoked_at < v_target)
        UNION ALL SELECT 1 FROM access.authoring_credential c JOIN access.authoring_session s ON s.id=c.session_id WHERE (s.revoked_at IS NOT NULL AND s.revoked_at < v_target) OR (c.replaced_at IS NOT NULL AND c.replaced_at < v_target) OR (c.refresh_expires_at IS NOT NULL AND c.refresh_expires_at < v_target) OR (c.refresh_expires_at IS NULL AND c.access_expires_at < v_target)
        UNION ALL SELECT 1 FROM access.authoring_session s WHERE (s.expires_at < v_target OR (s.revoked_at IS NOT NULL AND s.revoked_at < v_target)) AND NOT EXISTS (SELECT 1 FROM access.authoring_credential c WHERE c.session_id=s.id)
    ) INTO v_auth_remaining;
    IF NOT v_auth_remaining AND v_target > v_auth_floor THEN
        UPDATE access.access_retention_floor
           SET floor_at = v_target, updated_at = clock_timestamp()
         WHERE retention_class = 'auth_state';
        v_auth_floor := v_target;
    END IF;

    cutoff := v_target;
    sessions_removed := COALESCE(sessions_removed, 0);
    oauth_sessions_removed := COALESCE(oauth_sessions_removed, 0);
    oauth_assertions_removed := COALESCE(oauth_assertions_removed, 0);
    desktop_codes_removed := COALESCE(desktop_codes_removed, 0);
    device_authorizations_removed := COALESCE(device_authorizations_removed, 0);
    api_tokens_removed := COALESCE(api_tokens_removed, 0);
    service_secrets_removed := COALESCE(service_secrets_removed, 0);
    authoring_sessions_removed := COALESCE(authoring_sessions_removed, 0);
    authoring_credentials_removed := COALESCE(authoring_credentials_removed, 0);
    auth_state_floor := v_auth_floor;
    RETURN NEXT;
END;
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION access.prune_auth_state(timestamptz, integer) FROM PUBLIC;

CREATE TABLE access.principal (
    id uuid PRIMARY KEY,
    principal_type text NOT NULL CHECK (principal_type IN ('user','service','system','dashboard_publication')),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled','pending')),
    email text NOT NULL DEFAULT '' CHECK (length(email) <= 320),
    display_name text NOT NULL DEFAULT '' CHECK (length(display_name) <= 512),
    disabled_at timestamptz,
    blocked_at timestamptz,
    revoked_at timestamptz,
    last_seen_at timestamptz,
    attributes jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(attributes)='object' AND octet_length(attributes::text)<=16384),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
    ,CHECK ((status='active' AND disabled_at IS NULL) OR status<>'active')
    ,CHECK (status<>'disabled' OR disabled_at IS NOT NULL OR revoked_at IS NOT NULL)
    ,CHECK (status<>'pending' OR disabled_at IS NULL)
    ,CHECK (revoked_at IS NULL OR status='disabled')
);
CREATE UNIQUE INDEX principal_email_active_key ON access.principal (lower(email)) WHERE email <> '' AND revoked_at IS NULL;

CREATE TABLE access.external_identity (
    id uuid PRIMARY KEY,
    principal_id uuid NOT NULL REFERENCES access.principal(id),
    provider text NOT NULL CHECK (provider = btrim(provider) AND length(provider) BETWEEN 1 AND 128),
    tenant_id text NOT NULL DEFAULT '' CHECK (tenant_id = btrim(tenant_id) AND length(tenant_id) <= 255),
    subject text NOT NULL CHECK (subject = btrim(subject) AND length(subject) BETWEEN 1 AND 512),
    user_name text NOT NULL DEFAULT '' CHECK (length(user_name) <= 320),
    external_id text NOT NULL DEFAULT '' CHECK (length(external_id) <= 512),
    email text NOT NULL DEFAULT '' CHECK (length(email) <= 320),
    display_name text NOT NULL DEFAULT '' CHECK (length(display_name) <= 512),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz
);
CREATE UNIQUE INDEX external_identity_active_key ON access.external_identity(provider, tenant_id, subject) WHERE revoked_at IS NULL;
CREATE UNIQUE INDEX external_identity_active_external_id ON access.external_identity(provider, tenant_id, external_id) WHERE external_id <> '' AND revoked_at IS NULL;

CREATE TABLE access.platform_role_binding (
    id uuid PRIMARY KEY,
    principal_id uuid NOT NULL REFERENCES access.principal(id),
    role text NOT NULL CHECK (role = 'platform_admin'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz
);
CREATE UNIQUE INDEX platform_role_binding_active_key ON access.platform_role_binding(principal_id, role) WHERE revoked_at IS NULL;

CREATE TABLE access.access_group (
    id uuid PRIMARY KEY,
    name text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 255),
    provider text NOT NULL DEFAULT '' CHECK (length(provider) <= 255),
    external_id text NOT NULL DEFAULT '' CHECK (length(external_id) <= 512),
    attributes jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(attributes)='object' AND octet_length(attributes::text)<=16384),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz
);
-- NULLIF intentionally permits multiple local ('','') groups while keeping
-- provider/external identities unique while active.
CREATE UNIQUE INDEX access_group_active_key ON access.access_group(provider, NULLIF(external_id,'')) WHERE revoked_at IS NULL;

CREATE TABLE access.principal_group (
    membership_id uuid PRIMARY KEY DEFAULT uuidv7(),
    principal_id uuid NOT NULL REFERENCES access.principal(id),
    group_id uuid NOT NULL REFERENCES access.access_group(id),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz
);
CREATE UNIQUE INDEX principal_group_active_key ON access.principal_group(principal_id, group_id) WHERE revoked_at IS NULL;

CREATE TABLE access.session (
    id uuid PRIMARY KEY,
    principal_id uuid NOT NULL REFERENCES access.principal(id),
    token_fingerprint bytea NOT NULL UNIQUE,
    verifier bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    last_seen_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz,
    kind text NOT NULL DEFAULT 'browser' CHECK (kind IN ('browser','desktop')),
    instance_id text NOT NULL DEFAULT '' CHECK (length(instance_id) <= 128),
    profile_id text NOT NULL DEFAULT '' CHECK (length(profile_id) <= 128),
    client_id text NOT NULL DEFAULT '' CHECK (length(client_id) <= 255),
    absolute_expires_at timestamptz,
    CHECK (expires_at > created_at),
    CHECK (absolute_expires_at IS NULL OR absolute_expires_at >= expires_at),
    CHECK (octet_length(token_fingerprint)=32),
    CHECK (octet_length(verifier) BETWEEN 32 AND 512),
    CHECK ((kind='browser' AND instance_id='' AND profile_id='' AND client_id='' AND absolute_expires_at IS NULL) OR (kind='desktop' AND instance_id<>'' AND profile_id<>'' AND client_id<>'' AND absolute_expires_at IS NOT NULL))
);
CREATE INDEX access_session_active_fp_idx ON access.session(token_fingerprint) WHERE revoked_at IS NULL;
CREATE INDEX access_session_principal_idx ON access.session(principal_id, created_at DESC);

CREATE TABLE access.local_credential (
    principal_id uuid PRIMARY KEY REFERENCES access.principal(id),
    verifier bytea NOT NULL,
    must_change boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    password_changed_at timestamptz,
    revoked_at timestamptz,
    CHECK (octet_length(verifier) BETWEEN 32 AND 512)
);

-- +goose StatementBegin
CREATE FUNCTION access.valid_capabilities(value jsonb) RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE item text; seen_items jsonb := '[]'::jsonb;
BEGIN
    IF value IS NULL THEN RETURN TRUE; END IF;
    IF jsonb_typeof(value) <> 'array' THEN RETURN FALSE; END IF;
    FOR item IN SELECT jsonb_array_elements_text(value) LOOP
        IF item NOT IN ('PROJECT_ADMIN','RESOURCE_USE','RESOURCE_READ','RESOURCE_EDIT','RESOURCE_MANAGE','RESOURCE_SHARE','RESOURCE_PUBLISH') THEN RETURN FALSE; END IF;
        IF seen_items ? item THEN RETURN FALSE; END IF;
        seen_items := seen_items || to_jsonb(item);
    END LOOP;
    RETURN TRUE;
END $$;
-- +goose StatementEnd

CREATE TABLE access.api_token (
    id uuid PRIMARY KEY,
    principal_id uuid NOT NULL REFERENCES access.principal(id),
    name text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 255),
    token_fingerprint bytea NOT NULL UNIQUE,
    verifier bytea NOT NULL,
    capabilities jsonb CHECK (access.valid_capabilities(capabilities)),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    last_used_at timestamptz,
    revoked_at timestamptz,
    CHECK (octet_length(token_fingerprint)=32),
    CHECK (octet_length(verifier) BETWEEN 32 AND 512),
    CHECK (expires_at > created_at AND expires_at <= created_at + interval '365 days')
);
CREATE INDEX access_api_token_principal_idx ON access.api_token(principal_id, created_at DESC);
CREATE INDEX access_api_token_active_fp_idx ON access.api_token(token_fingerprint) WHERE revoked_at IS NULL;

CREATE TABLE access.service_principal_secret (
    id uuid PRIMARY KEY,
    service_principal_id uuid NOT NULL REFERENCES access.principal(id),
    name text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 255),
    secret_fingerprint bytea NOT NULL UNIQUE,
    verifier bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz,
    CHECK (octet_length(secret_fingerprint)=32),
    CHECK (octet_length(verifier) BETWEEN 32 AND 512),
    CHECK (expires_at > created_at AND expires_at <= created_at + interval '365 days')
);
CREATE INDEX service_secret_principal_idx ON access.service_principal_secret(service_principal_id, created_at DESC);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_access_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'access history is append-only; revoke instead of delete'; END; $$;
-- +goose StatementEnd
CREATE OR REPLACE FUNCTION access.allow_maintenance_delete() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
-- +goose StatementBegin
AS $$
BEGIN
    IF current_setting('access.maintenance', true) = 'on'
       AND session_user = 'leapview_control_maintenance' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'access state deletion requires bounded maintenance';
END;
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_revocation_clear() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF OLD.revoked_at IS NOT NULL AND (NEW.revoked_at IS NULL OR NEW.revoked_at < OLD.revoked_at) THEN RAISE EXCEPTION 'revocation is monotonic'; END IF; RETURN NEW; END; $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_principal_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.id <> NEW.id OR OLD.principal_type <> NEW.principal_type OR OLD.created_at <> NEW.created_at THEN
        RAISE EXCEPTION 'principal identity is immutable';
    END IF;
    IF NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'principal updated_at is monotonic';
    END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_group_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF OLD.id <> NEW.id OR OLD.provider <> NEW.provider OR OLD.external_id <> NEW.external_id OR OLD.created_at <> NEW.created_at THEN RAISE EXCEPTION 'group identity is immutable'; END IF; RETURN NEW; END; $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_role_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF OLD.id<>NEW.id OR OLD.principal_id<>NEW.principal_id OR OLD.role<>NEW.role OR OLD.created_at<>NEW.created_at THEN RAISE EXCEPTION 'role identity is immutable'; END IF; RETURN NEW; END; $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_membership_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF OLD.membership_id<>NEW.membership_id OR OLD.principal_id<>NEW.principal_id OR OLD.group_id<>NEW.group_id OR OLD.created_at<>NEW.created_at THEN RAISE EXCEPTION 'membership identity is immutable'; END IF; RETURN NEW; END; $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_external_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF OLD.id<>NEW.id OR OLD.principal_id<>NEW.principal_id OR OLD.provider<>NEW.provider OR OLD.tenant_id<>NEW.tenant_id OR OLD.subject<>NEW.subject OR OLD.created_at<>NEW.created_at THEN RAISE EXCEPTION 'external identity is immutable'; END IF; RETURN NEW; END; $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_session_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF OLD.id<>NEW.id OR OLD.principal_id<>NEW.principal_id OR OLD.token_fingerprint<>NEW.token_fingerprint OR OLD.verifier<>NEW.verifier OR OLD.kind<>NEW.kind OR OLD.instance_id<>NEW.instance_id OR OLD.profile_id<>NEW.profile_id OR OLD.client_id<>NEW.client_id OR OLD.created_at<>NEW.created_at OR OLD.absolute_expires_at IS DISTINCT FROM NEW.absolute_expires_at THEN RAISE EXCEPTION 'session identity is immutable'; END IF; RETURN NEW; END; $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_token_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF OLD.id<>NEW.id OR OLD.principal_id<>NEW.principal_id OR OLD.name<>NEW.name OR OLD.token_fingerprint<>NEW.token_fingerprint OR OLD.verifier<>NEW.verifier OR OLD.capabilities IS DISTINCT FROM NEW.capabilities OR OLD.expires_at<>NEW.expires_at OR OLD.created_at<>NEW.created_at THEN RAISE EXCEPTION 'API token identity is immutable'; END IF; RETURN NEW; END; $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_service_secret_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF OLD.id<>NEW.id OR OLD.service_principal_id<>NEW.service_principal_id OR OLD.name<>NEW.name OR OLD.secret_fingerprint<>NEW.secret_fingerprint OR OLD.verifier<>NEW.verifier OR OLD.expires_at<>NEW.expires_at OR OLD.created_at<>NEW.created_at THEN RAISE EXCEPTION 'service secret identity is immutable'; END IF; RETURN NEW; END; $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_credential_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF OLD.principal_id<>NEW.principal_id OR OLD.created_at<>NEW.created_at THEN RAISE EXCEPTION 'credential identity is immutable'; END IF; RETURN NEW; END; $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_preference_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.preference_id<>NEW.preference_id OR OLD.principal_id<>NEW.principal_id OR OLD.theme<>NEW.theme OR OLD.created_at IS DISTINCT FROM NEW.created_at THEN
        RAISE EXCEPTION 'preference identity is immutable';
    END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_avatar_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.avatar_id<>NEW.avatar_id OR OLD.principal_id<>NEW.principal_id OR OLD.sha256<>NEW.sha256 OR OLD.media_type<>NEW.media_type OR OLD.size_bytes<>NEW.size_bytes OR OLD.width<>NEW.width OR OLD.height<>NEW.height OR OLD.updated_at<>NEW.updated_at THEN
        RAISE EXCEPTION 'avatar identity is immutable';
    END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_object_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'avatar object identity is immutable';
END; $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_consumption_rewind() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.consumed_at IS NOT NULL AND (NEW.consumed_at IS NULL OR NEW.consumed_at < OLD.consumed_at) THEN
        RAISE EXCEPTION 'consumption is monotonic';
    END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_device_authorization_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.id<>NEW.id OR OLD.client_id<>NEW.client_id OR OLD.device_code_hash<>NEW.device_code_hash OR OLD.user_code_hash<>NEW.user_code_hash OR OLD.target_id<>NEW.target_id OR OLD.project_id<>NEW.project_id OR OLD.capabilities IS DISTINCT FROM NEW.capabilities OR OLD.created_at<>NEW.created_at OR OLD.expires_at<>NEW.expires_at THEN
        RAISE EXCEPTION 'device authorization identity is immutable';
    END IF;
    IF OLD.status='pending' AND NEW.status NOT IN ('pending','approved','denied') THEN
        RAISE EXCEPTION 'invalid device authorization transition';
    ELSIF OLD.status='approved' AND NEW.status NOT IN ('approved','consumed') THEN
        RAISE EXCEPTION 'invalid device authorization transition';
    ELSIF OLD.status IN ('denied','consumed') AND NEW.status<>OLD.status THEN
        RAISE EXCEPTION 'invalid device authorization transition';
    END IF;
    IF OLD.approved_at IS NOT NULL AND (NEW.approved_at IS NULL OR NEW.approved_at < OLD.approved_at) THEN
        RAISE EXCEPTION 'approval timestamp is monotonic';
    END IF;
    IF OLD.denied_at IS NOT NULL AND (NEW.denied_at IS NULL OR NEW.denied_at < OLD.denied_at) THEN
        RAISE EXCEPTION 'denial timestamp is monotonic';
    END IF;
    IF OLD.consumed_at IS NOT NULL AND (NEW.consumed_at IS NULL OR NEW.consumed_at < OLD.consumed_at) THEN
        RAISE EXCEPTION 'consumption timestamp is monotonic';
    END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_authoring_credential_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.active = FALSE AND NEW.active <> FALSE THEN
        RAISE EXCEPTION 'authoring credential activation is not reversible';
    END IF;
    IF OLD.replaced_at IS NOT NULL AND (NEW.replaced_at IS NULL OR NEW.replaced_at < OLD.replaced_at) THEN
        RAISE EXCEPTION 'credential replacement timestamp is monotonic';
    END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd

CREATE TRIGGER principal_no_delete BEFORE DELETE ON access.principal FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER principal_identity_immutable BEFORE UPDATE ON access.principal FOR EACH ROW EXECUTE FUNCTION access.reject_principal_identity_rewrite();
CREATE TRIGGER principal_revocation_monotonic BEFORE UPDATE ON access.principal FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER group_no_delete BEFORE DELETE ON access.access_group FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER group_identity_immutable BEFORE UPDATE ON access.access_group FOR EACH ROW EXECUTE FUNCTION access.reject_group_identity_rewrite();
CREATE TRIGGER group_revocation_monotonic BEFORE UPDATE ON access.access_group FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER membership_no_delete BEFORE DELETE ON access.principal_group FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER membership_identity_immutable BEFORE UPDATE ON access.principal_group FOR EACH ROW EXECUTE FUNCTION access.reject_membership_identity_rewrite();
CREATE TRIGGER membership_revocation_monotonic BEFORE UPDATE ON access.principal_group FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER role_no_delete BEFORE DELETE ON access.platform_role_binding FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER role_identity_immutable BEFORE UPDATE ON access.platform_role_binding FOR EACH ROW EXECUTE FUNCTION access.reject_role_identity_rewrite();
CREATE TRIGGER role_revocation_monotonic BEFORE UPDATE ON access.platform_role_binding FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER session_no_delete BEFORE DELETE ON access.session FOR EACH ROW EXECUTE FUNCTION access.allow_maintenance_delete();
CREATE TRIGGER session_identity_immutable BEFORE UPDATE ON access.session FOR EACH ROW EXECUTE FUNCTION access.reject_session_identity_rewrite();
CREATE TRIGGER session_revocation_monotonic BEFORE UPDATE ON access.session FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER api_token_no_delete BEFORE DELETE ON access.api_token FOR EACH ROW EXECUTE FUNCTION access.allow_maintenance_delete();
CREATE TRIGGER api_token_identity_immutable BEFORE UPDATE ON access.api_token FOR EACH ROW EXECUTE FUNCTION access.reject_token_identity_rewrite();
CREATE TRIGGER api_token_revocation_monotonic BEFORE UPDATE ON access.api_token FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER service_secret_no_delete BEFORE DELETE ON access.service_principal_secret FOR EACH ROW EXECUTE FUNCTION access.allow_maintenance_delete();
CREATE TRIGGER service_secret_identity_immutable BEFORE UPDATE ON access.service_principal_secret FOR EACH ROW EXECUTE FUNCTION access.reject_service_secret_identity_rewrite();
CREATE TRIGGER service_secret_revocation_monotonic BEFORE UPDATE ON access.service_principal_secret FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER local_credential_no_delete BEFORE DELETE ON access.local_credential FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER local_credential_identity_immutable BEFORE UPDATE ON access.local_credential FOR EACH ROW EXECUTE FUNCTION access.reject_credential_identity_rewrite();
CREATE TRIGGER local_credential_revocation_monotonic BEFORE UPDATE ON access.local_credential FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER external_identity_no_delete BEFORE DELETE ON access.external_identity FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER external_identity_identity_immutable BEFORE UPDATE ON access.external_identity FOR EACH ROW EXECUTE FUNCTION access.reject_external_identity_rewrite();
CREATE TRIGGER external_identity_revocation_monotonic BEFORE UPDATE ON access.external_identity FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER platform_setting_no_mutation BEFORE UPDATE OR DELETE ON access.platform_setting FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();

-- Project-generation authorization is owned by access.  Snapshot rows and
-- their children are immutable evidence; mutable administrative bindings use
-- revocation tombstones so history can be reconciled without hard deletes.
CREATE TABLE access.authorization_snapshot (
    project_id text NOT NULL CHECK (project_id = btrim(project_id) AND length(project_id) BETWEEN 1 AND 255),
    environment text NOT NULL CHECK (environment = btrim(environment) AND length(environment) BETWEEN 1 AND 128),
    generation_id text NOT NULL CHECK (generation_id = btrim(generation_id) AND length(generation_id) BETWEEN 1 AND 255),
    digest text NOT NULL CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (project_id, environment, generation_id)
);

CREATE TABLE access.authorization_role_binding (
    id text NOT NULL CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 255),
    project_id text NOT NULL,
    environment text NOT NULL,
    generation_id text NOT NULL,
    subject_kind text NOT NULL CHECK (subject_kind IN ('principal','group')),
    subject_id text NOT NULL CHECK (subject_id = btrim(subject_id) AND length(subject_id) BETWEEN 1 AND 255),
    role text NOT NULL CHECK (role IN ('owner','admin','deployer','data_deployer','contributor','editor','member','viewer')),
    capabilities jsonb NOT NULL CHECK (access.valid_capabilities(capabilities) AND jsonb_typeof(capabilities)='array' AND octet_length(capabilities::text)<=2048),
    name text NOT NULL DEFAULT '' CHECK (length(name)<=255),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz,
    PRIMARY KEY (project_id, environment, generation_id, id),
    FOREIGN KEY (project_id, environment, generation_id) REFERENCES access.authorization_snapshot(project_id, environment, generation_id)
);
CREATE UNIQUE INDEX authorization_role_binding_active_key ON access.authorization_role_binding(project_id, environment, generation_id, subject_kind, subject_id, role) WHERE revoked_at IS NULL;
CREATE INDEX authorization_role_binding_subject_idx ON access.authorization_role_binding(project_id, environment, generation_id, subject_kind, subject_id);

CREATE TABLE access.authorization_grant (
    id text NOT NULL CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 255),
    project_id text NOT NULL,
    environment text NOT NULL,
    generation_id text NOT NULL,
    subject_kind text NOT NULL CHECK (subject_kind IN ('principal','group')),
    subject_id text NOT NULL CHECK (subject_id = btrim(subject_id) AND length(subject_id) BETWEEN 1 AND 255),
    resource_id text NOT NULL CHECK (resource_id = btrim(resource_id) AND length(resource_id) BETWEEN 1 AND 255),
    resource_kind text NOT NULL CHECK (resource_kind = btrim(resource_kind) AND length(resource_kind) BETWEEN 1 AND 128),
    capability text NOT NULL CHECK (capability IN ('PROJECT_ADMIN','RESOURCE_USE','RESOURCE_READ','RESOURCE_EDIT','RESOURCE_MANAGE','RESOURCE_SHARE','RESOURCE_PUBLISH')),
    name text NOT NULL DEFAULT '' CHECK (length(name)<=255),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz,
    PRIMARY KEY (project_id, environment, generation_id, id),
    FOREIGN KEY (project_id, environment, generation_id) REFERENCES access.authorization_snapshot(project_id, environment, generation_id)
);
CREATE UNIQUE INDEX authorization_grant_active_key ON access.authorization_grant(project_id, environment, generation_id, subject_kind, subject_id, resource_id, resource_kind, capability) WHERE revoked_at IS NULL;
CREATE INDEX authorization_grant_subject_idx ON access.authorization_grant(project_id, environment, generation_id, subject_kind, subject_id);
CREATE INDEX authorization_grant_resource_idx ON access.authorization_grant(project_id, environment, generation_id, resource_id, capability);

CREATE TABLE access.authorization_data_policy (
    id text NOT NULL CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 255),
    project_id text NOT NULL,
    environment text NOT NULL,
    generation_id text NOT NULL,
    resource_id text NOT NULL CHECK (resource_id = btrim(resource_id) AND length(resource_id) BETWEEN 1 AND 255),
    resource_kind text NOT NULL CHECK (resource_kind = btrim(resource_kind) AND length(resource_kind) BETWEEN 1 AND 128),
    subject_kind text CHECK (subject_kind IS NULL OR subject_kind IN ('principal','group')),
    subject_id text,
    policy_type text NOT NULL CHECK (policy_type IN ('row_filter','column_mask')),
    expression jsonb NOT NULL CHECK (jsonb_typeof(expression)='object' AND octet_length(expression::text)<=32768),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz,
    PRIMARY KEY (project_id, environment, generation_id, id),
    CHECK ((subject_kind IS NULL AND subject_id IS NULL) OR (subject_kind IS NOT NULL AND subject_id IS NOT NULL)),
    FOREIGN KEY (project_id, environment, generation_id) REFERENCES access.authorization_snapshot(project_id, environment, generation_id)
);
CREATE INDEX authorization_data_policy_resource_idx ON access.authorization_data_policy(project_id, environment, generation_id, resource_id);

CREATE TABLE access.authorization_revocation (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id text NOT NULL,
    environment text NOT NULL,
    generation_id text,
    subject_kind text CHECK (subject_kind IS NULL OR subject_kind IN ('principal','group')),
    subject_id text,
    resource_id text,
    capability text,
    reason text NOT NULL DEFAULT '' CHECK (length(reason)<=1024),
    revoked_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata)='object' AND octet_length(metadata::text)<=8192)
);

CREATE TABLE access.principal_preferences (
    preference_id uuid PRIMARY KEY DEFAULT uuidv7(),
    principal_id uuid NOT NULL REFERENCES access.principal(id),
    theme text NOT NULL DEFAULT 'system' CHECK (theme IN ('system','light','dark','dark_dimmed','light_colorblind','dark_colorblind','light_tritanopia','dark_tritanopia')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz
);
CREATE UNIQUE INDEX principal_preferences_active_key ON access.principal_preferences(principal_id) WHERE revoked_at IS NULL;

CREATE TABLE access.avatar_object (
    sha256 text PRIMARY KEY CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    object_key text NOT NULL CHECK (object_key = btrim(object_key) AND length(object_key) BETWEEN 1 AND 2048),
    media_type text NOT NULL CHECK (media_type='image/png'),
    size_bytes bigint NOT NULL CHECK (size_bytes > 0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE access.principal_avatar (
    avatar_id uuid PRIMARY KEY DEFAULT uuidv7(),
    principal_id uuid NOT NULL REFERENCES access.principal(id),
    sha256 text NOT NULL REFERENCES access.avatar_object(sha256),
    media_type text NOT NULL CHECK (media_type='image/png'),
    size_bytes bigint NOT NULL CHECK (size_bytes > 0),
    width integer NOT NULL CHECK (width=256),
    height integer NOT NULL CHECK (height=256),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz
);
CREATE UNIQUE INDEX principal_avatar_active_key ON access.principal_avatar(principal_id) WHERE revoked_at IS NULL;
CREATE TRIGGER avatar_object_no_delete BEFORE DELETE ON access.avatar_object FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER avatar_object_immutable BEFORE UPDATE ON access.avatar_object FOR EACH ROW EXECUTE FUNCTION access.reject_object_identity_rewrite();
CREATE TRIGGER principal_avatar_immutable BEFORE UPDATE ON access.principal_avatar FOR EACH ROW EXECUTE FUNCTION access.reject_avatar_identity_rewrite();
CREATE TRIGGER principal_preferences_immutable BEFORE UPDATE ON access.principal_preferences FOR EACH ROW EXECUTE FUNCTION access.reject_preference_identity_rewrite();

-- Desktop authorization codes are short-lived bearer artifacts.  The hash
-- is the identity; consumption is a monotonic tombstone and therefore safe
-- under competing redemption transactions.
CREATE TABLE access.desktop_authorization_code (
    code_hash bytea PRIMARY KEY CHECK (octet_length(code_hash)=32),
    principal_id uuid NOT NULL REFERENCES access.principal(id),
    client_id text NOT NULL CHECK (client_id='leapview-desktop'),
    instance_id text NOT NULL CHECK (length(instance_id) BETWEEN 1 AND 128),
    profile_id text NOT NULL CHECK (profile_id = btrim(profile_id) AND length(profile_id) BETWEEN 1 AND 128),
    redirect_uri text NOT NULL CHECK (length(redirect_uri)<=2048),
    code_challenge text NOT NULL CHECK (length(code_challenge) BETWEEN 43 AND 128),
    return_path text NOT NULL CHECK (length(return_path)<=2048),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (expires_at > created_at AND expires_at <= created_at + interval '10 minutes')
);
CREATE INDEX desktop_authorization_code_expiry_idx ON access.desktop_authorization_code(expires_at);

-- First-party CLI/device authorization is separate from desktop browser
-- codes because it carries an authoring scope and refresh-token family.
CREATE TABLE access.device_authorization (
    id text PRIMARY KEY CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 128),
    client_id text NOT NULL CHECK (client_id='leapview-cli'),
    device_code_hash text NOT NULL UNIQUE CHECK (device_code_hash ~ '^[0-9a-f]{64}$'),
    user_code_hash text NOT NULL UNIQUE CHECK (user_code_hash ~ '^[0-9a-f]{64}$'),
    target_id text NOT NULL CHECK (target_id = btrim(target_id) AND length(target_id)<=255),
    project_id text NOT NULL CHECK (project_id = btrim(project_id) AND length(project_id)<=255),
    capabilities jsonb NOT NULL CHECK (access.valid_capabilities(capabilities) AND jsonb_typeof(capabilities)='array' AND octet_length(capabilities::text)<=2048),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','denied','consumed')),
    principal_id uuid REFERENCES access.principal(id),
    expires_at timestamptz NOT NULL,
    poll_interval_seconds integer NOT NULL CHECK (poll_interval_seconds > 0),
    last_polled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    approved_at timestamptz,
    denied_at timestamptz,
    consumed_at timestamptz,
    CHECK (expires_at > created_at AND expires_at <= created_at + interval '24 hours'),
    CHECK ((status='pending' AND principal_id IS NULL AND approved_at IS NULL AND denied_at IS NULL AND consumed_at IS NULL)
        OR (status='approved' AND principal_id IS NOT NULL AND approved_at IS NOT NULL AND denied_at IS NULL AND consumed_at IS NULL)
        OR (status='denied' AND principal_id IS NOT NULL AND denied_at IS NOT NULL AND consumed_at IS NULL)
        OR (status='consumed' AND principal_id IS NOT NULL AND approved_at IS NOT NULL AND consumed_at IS NOT NULL))
);
CREATE INDEX device_authorization_expiry_idx ON access.device_authorization(expires_at);
CREATE TABLE access.authoring_session (
    id text PRIMARY KEY CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 128),
    kind text NOT NULL CHECK (kind IN ('human_cli','workload')),
    client_id text NOT NULL CHECK (length(client_id)<=255),
    principal_id uuid NOT NULL REFERENCES access.principal(id),
    target_id text NOT NULL CHECK (length(target_id)<=255),
    project_id text NOT NULL CHECK (length(project_id)<=255),
    capabilities jsonb NOT NULL CHECK (access.valid_capabilities(capabilities) AND jsonb_typeof(capabilities)='array' AND octet_length(capabilities::text)<=2048),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    last_used_at timestamptz,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    CHECK (expires_at > created_at AND expires_at <= created_at + interval '365 days')
);
CREATE INDEX authoring_session_principal_idx ON access.authoring_session(principal_id, created_at DESC);
CREATE TABLE access.authoring_credential (
    id text PRIMARY KEY CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 128),
    session_id text NOT NULL REFERENCES access.authoring_session(id),
    access_token_hash text NOT NULL UNIQUE CHECK (access_token_hash ~ '^[0-9a-f]{64}$'),
    refresh_token_hash text UNIQUE CHECK (refresh_token_hash IS NULL OR refresh_token_hash ~ '^[0-9a-f]{64}$'),
    access_expires_at timestamptz NOT NULL,
    refresh_expires_at timestamptz,
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    replaced_at timestamptz,
    CHECK (access_expires_at > created_at),
    CHECK ((refresh_token_hash IS NULL AND refresh_expires_at IS NULL) OR (refresh_token_hash IS NOT NULL AND refresh_expires_at IS NOT NULL AND refresh_expires_at > access_expires_at))
);
CREATE UNIQUE INDEX authoring_credential_active_session_idx ON access.authoring_credential(session_id) WHERE active;
CREATE INDEX authoring_credential_access_expiry_idx ON access.authoring_credential(access_expires_at);
CREATE INDEX authoring_credential_refresh_expiry_idx ON access.authoring_credential(refresh_expires_at) WHERE refresh_expires_at IS NOT NULL;

-- MCP OAuth state is owned by the access capability.  It is deliberately
-- separate from the browser/session credential tables: fosite request state
-- is opaque JSON, while client identity and token signatures remain typed and
-- uniquely indexed.  The runtime role may mutate these rows; no SQLite
-- compatibility projection exists on the PostgreSQL path.
CREATE TABLE access.oauth_client (
    id text PRIMARY KEY CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 255),
    name text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 255),
    redirect_uris jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(redirect_uris)='array' AND octet_length(redirect_uris::text)<=16384),
    grant_types jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(grant_types)='array' AND octet_length(grant_types::text)<=4096),
    response_types jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(response_types)='array' AND octet_length(response_types::text)<=4096),
    scopes jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(scopes)='array' AND octet_length(scopes::text)<=4096),
    audience jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(audience)='array' AND octet_length(audience::text)<=4096),
    public_client boolean NOT NULL DEFAULT false,
    secret_hash bytea,
    token_endpoint_auth_method text NOT NULL DEFAULT 'none' CHECK (token_endpoint_auth_method = btrim(token_endpoint_auth_method) AND length(token_endpoint_auth_method)<=64),
    principal_id uuid REFERENCES access.principal(id),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE access.oauth_session (
    kind text NOT NULL CHECK (kind IN ('authorize_code','access_token','refresh_token','pkce')),
    signature text NOT NULL CHECK (signature = btrim(signature) AND length(signature) BETWEEN 1 AND 512),
    request_id text NOT NULL CHECK (request_id = btrim(request_id) AND length(request_id) BETWEEN 1 AND 512),
    request_json jsonb NOT NULL CHECK (jsonb_typeof(request_json)='object' AND octet_length(request_json::text)<=131072),
    access_signature text NOT NULL DEFAULT '' CHECK (access_signature = btrim(access_signature) AND length(access_signature)<=512),
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (kind, signature)
);
CREATE INDEX oauth_session_request_idx ON access.oauth_session(kind, request_id);
CREATE TABLE access.oauth_client_assertion (
    jti text PRIMARY KEY CHECK (jti = btrim(jti) AND length(jti) BETWEEN 1 AND 512),
    expires_at timestamptz NOT NULL
);
CREATE INDEX oauth_client_assertion_expiry_idx ON access.oauth_client_assertion(expires_at);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_authorization_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME = 'authorization_snapshot' THEN
        IF OLD.project_id<>NEW.project_id OR OLD.environment<>NEW.environment OR OLD.generation_id<>NEW.generation_id OR OLD.digest<>NEW.digest OR OLD.created_at<>NEW.created_at THEN RAISE EXCEPTION 'authorization snapshot identity is immutable'; END IF;
    ELSIF TG_TABLE_NAME = 'authorization_role_binding' THEN
        IF OLD.id<>NEW.id OR OLD.project_id<>NEW.project_id OR OLD.environment<>NEW.environment OR OLD.generation_id<>NEW.generation_id OR OLD.subject_kind<>NEW.subject_kind OR OLD.subject_id<>NEW.subject_id OR OLD.role<>NEW.role OR OLD.capabilities IS DISTINCT FROM NEW.capabilities OR OLD.name<>NEW.name OR OLD.created_at<>NEW.created_at THEN RAISE EXCEPTION 'authorization role binding identity is immutable'; END IF;
    ELSIF TG_TABLE_NAME = 'authorization_grant' THEN
        IF OLD.id<>NEW.id OR OLD.project_id<>NEW.project_id OR OLD.environment<>NEW.environment OR OLD.generation_id<>NEW.generation_id OR OLD.subject_kind<>NEW.subject_kind OR OLD.subject_id<>NEW.subject_id OR OLD.resource_id<>NEW.resource_id OR OLD.resource_kind<>NEW.resource_kind OR OLD.capability<>NEW.capability OR OLD.name<>NEW.name OR OLD.created_at<>NEW.created_at THEN RAISE EXCEPTION 'authorization grant identity is immutable'; END IF;
    ELSIF TG_TABLE_NAME = 'authorization_data_policy' THEN
        IF OLD.id<>NEW.id OR OLD.project_id<>NEW.project_id OR OLD.environment<>NEW.environment OR OLD.generation_id<>NEW.generation_id OR OLD.resource_id<>NEW.resource_id OR OLD.resource_kind<>NEW.resource_kind OR OLD.subject_kind IS DISTINCT FROM NEW.subject_kind OR OLD.subject_id IS DISTINCT FROM NEW.subject_id OR OLD.policy_type<>NEW.policy_type OR OLD.expression IS DISTINCT FROM NEW.expression OR OLD.created_at<>NEW.created_at THEN RAISE EXCEPTION 'authorization policy identity is immutable'; END IF;
    END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd
CREATE TRIGGER authorization_snapshot_no_delete BEFORE DELETE ON access.authorization_snapshot FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER authorization_snapshot_immutable BEFORE UPDATE ON access.authorization_snapshot FOR EACH ROW EXECUTE FUNCTION access.reject_authorization_identity_rewrite();
CREATE TRIGGER authorization_role_binding_no_delete BEFORE DELETE ON access.authorization_role_binding FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER authorization_role_binding_immutable BEFORE UPDATE ON access.authorization_role_binding FOR EACH ROW EXECUTE FUNCTION access.reject_authorization_identity_rewrite();
CREATE TRIGGER authorization_role_binding_revocation_monotonic BEFORE UPDATE ON access.authorization_role_binding FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER authorization_grant_no_delete BEFORE DELETE ON access.authorization_grant FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER authorization_grant_immutable BEFORE UPDATE ON access.authorization_grant FOR EACH ROW EXECUTE FUNCTION access.reject_authorization_identity_rewrite();
CREATE TRIGGER authorization_grant_revocation_monotonic BEFORE UPDATE ON access.authorization_grant FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER authorization_data_policy_no_delete BEFORE DELETE ON access.authorization_data_policy FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER authorization_data_policy_immutable BEFORE UPDATE ON access.authorization_data_policy FOR EACH ROW EXECUTE FUNCTION access.reject_authorization_identity_rewrite();
CREATE TRIGGER authorization_data_policy_revocation_monotonic BEFORE UPDATE ON access.authorization_data_policy FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER authorization_revocation_append_only BEFORE UPDATE OR DELETE ON access.authorization_revocation FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER principal_preferences_no_delete BEFORE DELETE ON access.principal_preferences FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER principal_preferences_revocation_monotonic BEFORE UPDATE ON access.principal_preferences FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER principal_avatar_no_delete BEFORE DELETE ON access.principal_avatar FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER principal_avatar_revocation_monotonic BEFORE UPDATE ON access.principal_avatar FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER desktop_authorization_code_no_delete BEFORE DELETE ON access.desktop_authorization_code FOR EACH ROW EXECUTE FUNCTION access.allow_maintenance_delete();
CREATE TRIGGER device_authorization_no_delete BEFORE DELETE ON access.device_authorization FOR EACH ROW EXECUTE FUNCTION access.allow_maintenance_delete();
CREATE TRIGGER authoring_session_no_delete BEFORE DELETE ON access.authoring_session FOR EACH ROW EXECUTE FUNCTION access.allow_maintenance_delete();
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_authoring_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME='desktop_authorization_code' THEN
        IF OLD.code_hash<>NEW.code_hash OR OLD.principal_id<>NEW.principal_id OR OLD.client_id<>NEW.client_id OR OLD.instance_id<>NEW.instance_id OR OLD.profile_id<>NEW.profile_id OR OLD.redirect_uri<>NEW.redirect_uri OR OLD.code_challenge<>NEW.code_challenge OR OLD.return_path<>NEW.return_path OR OLD.expires_at<>NEW.expires_at OR OLD.created_at<>NEW.created_at THEN RAISE EXCEPTION 'desktop authorization identity is immutable'; END IF;
    ELSIF TG_TABLE_NAME='authoring_session' THEN
        IF OLD.id<>NEW.id OR OLD.kind<>NEW.kind OR OLD.client_id<>NEW.client_id OR OLD.principal_id<>NEW.principal_id OR OLD.target_id<>NEW.target_id OR OLD.project_id<>NEW.project_id OR OLD.capabilities IS DISTINCT FROM NEW.capabilities OR OLD.created_at<>NEW.created_at THEN RAISE EXCEPTION 'authoring session identity is immutable'; END IF;
    ELSIF TG_TABLE_NAME='authoring_credential' THEN
        IF OLD.id<>NEW.id OR OLD.session_id<>NEW.session_id OR OLD.access_token_hash<>NEW.access_token_hash OR OLD.refresh_token_hash IS DISTINCT FROM NEW.refresh_token_hash OR OLD.access_expires_at<>NEW.access_expires_at OR OLD.refresh_expires_at IS DISTINCT FROM NEW.refresh_expires_at OR OLD.created_at<>NEW.created_at THEN RAISE EXCEPTION 'authoring credential identity is immutable'; END IF;
    END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd
CREATE TRIGGER desktop_authorization_code_immutable BEFORE UPDATE ON access.desktop_authorization_code FOR EACH ROW EXECUTE FUNCTION access.reject_authoring_identity_rewrite();
CREATE TRIGGER desktop_authorization_code_consumption_monotonic BEFORE UPDATE ON access.desktop_authorization_code FOR EACH ROW EXECUTE FUNCTION access.reject_consumption_rewind();
CREATE TRIGGER device_authorization_immutable BEFORE UPDATE ON access.device_authorization FOR EACH ROW EXECUTE FUNCTION access.reject_device_authorization_rewrite();
CREATE TRIGGER authoring_session_immutable BEFORE UPDATE ON access.authoring_session FOR EACH ROW EXECUTE FUNCTION access.reject_authoring_identity_rewrite();
CREATE TRIGGER authoring_session_revocation_monotonic BEFORE UPDATE ON access.authoring_session FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER authoring_credential_no_delete BEFORE DELETE ON access.authoring_credential FOR EACH ROW EXECUTE FUNCTION access.allow_maintenance_delete();
CREATE TRIGGER authoring_credential_immutable BEFORE UPDATE ON access.authoring_credential FOR EACH ROW EXECUTE FUNCTION access.reject_authoring_identity_rewrite();
CREATE TRIGGER authoring_credential_transition BEFORE UPDATE ON access.authoring_credential FOR EACH ROW EXECUTE FUNCTION access.reject_authoring_credential_transition();

-- +goose StatementBegin
DO $$
BEGIN
    REVOKE ALL ON SCHEMA access FROM PUBLIC;
    REVOKE ALL ON ALL TABLES IN SCHEMA access FROM PUBLIC;
    REVOKE ALL ON ALL FUNCTIONS IN SCHEMA access FROM PUBLIC;
    REVOKE ALL ON SCHEMA audit FROM PUBLIC;
    REVOKE ALL ON TABLE audit.audit_event, audit.audit_retention_floor FROM PUBLIC;
    REVOKE ALL ON FUNCTION audit.reject_audit_mutation() FROM PUBLIC;
    REVOKE ALL ON FUNCTION audit.prune_audit_events(text, timestamptz, integer) FROM PUBLIC;
    REVOKE ALL ON FUNCTION access.prune_auth_state(timestamptz, integer) FROM PUBLIC;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_runtime') THEN
        EXECUTE 'GRANT USAGE ON SCHEMA access TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA access TO leapview_control_runtime';
        EXECUTE 'GRANT DELETE ON access.oauth_session, access.oauth_client_assertion TO leapview_control_runtime';
        EXECUTE 'GRANT EXECUTE ON FUNCTION access.valid_capabilities(jsonb) TO leapview_control_runtime';
        EXECUTE 'GRANT USAGE ON SCHEMA audit TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT, INSERT ON audit.audit_event TO leapview_control_runtime';
        EXECUTE 'REVOKE DELETE ON audit.audit_event, audit.audit_retention_floor FROM leapview_control_runtime';
        EXECUTE 'REVOKE EXECUTE ON FUNCTION audit.prune_audit_events(text, timestamptz, integer) FROM leapview_control_runtime';
        EXECUTE 'REVOKE EXECUTE ON FUNCTION access.prune_auth_state(timestamptz, integer) FROM leapview_control_runtime';
        EXECUTE 'REVOKE DELETE ON access.session, access.api_token, access.service_principal_secret, access.desktop_authorization_code, access.device_authorization, access.authoring_session, access.authoring_credential FROM leapview_control_runtime';
        EXECUTE 'REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON access.access_retention_floor FROM leapview_control_runtime';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_maintenance') THEN
        EXECUTE 'GRANT USAGE ON SCHEMA access, audit TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION audit.prune_audit_events(text, timestamptz, integer) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION access.prune_auth_state(timestamptz, integer) TO leapview_control_maintenance';
        EXECUTE 'REVOKE ALL ON audit.audit_event, audit.audit_retention_floor FROM leapview_control_maintenance';
        EXECUTE 'REVOKE ALL ON access.access_retention_floor FROM leapview_control_maintenance';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_readonly') THEN
        EXECUTE 'GRANT USAGE ON SCHEMA access TO leapview_control_readonly';
        EXECUTE 'GRANT SELECT ON ALL TABLES IN SCHEMA access TO leapview_control_readonly';
        EXECUTE 'REVOKE SELECT ON access.session, access.local_credential, access.api_token, access.service_principal_secret, access.desktop_authorization_code, access.device_authorization, access.authoring_credential, access.oauth_client, access.oauth_session, access.oauth_client_assertion FROM leapview_control_readonly';
        EXECUTE 'GRANT USAGE ON SCHEMA audit TO leapview_control_readonly';
        EXECUTE 'GRANT SELECT ON audit.audit_event, audit.audit_retention_floor TO leapview_control_readonly';
        EXECUTE 'REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON access.access_retention_floor FROM leapview_control_readonly';
    END IF;
END $$;
-- +goose StatementEnd

-- capability source: internal/admin/product/postgres/schema.sql
-- Clean-slate admin product identity authority (ADR-0020).
-- Product identity is mutable through revision-guarded writes; logo bytes are
-- intentionally external to this capability and only their validated metadata
-- is persisted here.

CREATE SCHEMA IF NOT EXISTS admin;

CREATE TABLE IF NOT EXISTS admin.product_identity (
    singleton_id     smallint PRIMARY KEY CHECK (singleton_id = 1),
    display_name     text NOT NULL CHECK (display_name = btrim(display_name)
                                           AND char_length(display_name) BETWEEN 1 AND 120),
    logo_sha256      text,
    logo_media_type  text,
    logo_size_bytes  bigint,
    logo_width       integer,
    logo_height      integer,
    revision         bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    updated_at       timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (logo_sha256 IS NULL AND logo_media_type IS NULL AND logo_size_bytes IS NULL
           AND logo_width IS NULL AND logo_height IS NULL
        OR logo_sha256 ~ '^[0-9a-f]{64}$'
           AND logo_media_type IN ('image/jpeg', 'image/png', 'image/webp')
           AND logo_size_bytes BETWEEN 1 AND 5242880
           AND logo_width BETWEEN 1 AND 2147483647
           AND logo_height BETWEEN 1 AND 2147483647),
    CHECK ((logo_sha256 IS NULL) = (logo_media_type IS NULL)
           AND (logo_sha256 IS NULL) = (logo_size_bytes IS NULL)
           AND (logo_sha256 IS NULL) = (logo_width IS NULL)
           AND (logo_sha256 IS NULL) = (logo_height IS NULL))
);

INSERT INTO admin.product_identity(singleton_id, display_name)
VALUES (1, 'LeapView')
ON CONFLICT (singleton_id) DO NOTHING;

-- Every product mutation must advance the revision exactly once. This keeps
-- direct SQL writes subject to the same optimistic-concurrency contract as
-- the repository and prevents silent edits that clients cannot observe.
CREATE OR REPLACE FUNCTION admin.guard_product_identity_revision()
RETURNS trigger
LANGUAGE plpgsql
-- +goose StatementBegin
AS $$
BEGIN
    IF NEW.singleton_id IS DISTINCT FROM OLD.singleton_id
       OR NEW.revision <> OLD.revision + 1
       OR NEW.updated_at <= OLD.updated_at THEN
        RAISE EXCEPTION 'product identity revision or singleton is invalid';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS product_identity_revision_guard ON admin.product_identity;
CREATE TRIGGER product_identity_revision_guard
    BEFORE UPDATE ON admin.product_identity
    FOR EACH ROW EXECUTE FUNCTION admin.guard_product_identity_revision();

REVOKE ALL ON SCHEMA admin FROM PUBLIC;
REVOKE ALL ON TABLE admin.product_identity FROM PUBLIC;
REVOKE ALL ON FUNCTION admin.guard_product_identity_revision() FROM PUBLIC;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA admin TO leapview_control_runtime;
        GRANT SELECT, UPDATE ON admin.product_identity TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA admin TO leapview_control_readonly;
        GRANT SELECT ON admin.product_identity TO leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA admin TO leapview_control_backup;
        GRANT SELECT ON admin.product_identity TO leapview_control_backup;
    END IF;
END
$$;
-- +goose StatementEnd

-- capability source: internal/dashboard/session/postgres/schema.sql
-- Native PostgreSQL dashboard-view session authority.
CREATE SCHEMA IF NOT EXISTS dashboard;

CREATE TABLE IF NOT EXISTS dashboard.view_session (
    id text PRIMARY KEY,
    project_id text NOT NULL DEFAULT '',
    publication_id text NOT NULL DEFAULT '',
    principal_or_client text NOT NULL,
    dashboard_id text NOT NULL,
    serving_state_id text NOT NULL,
    stream_instance_id text NOT NULL,
    key_json jsonb NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    state_json jsonb NOT NULL,
    expires_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK ((project_id = '' AND publication_id <> '') OR (project_id <> '' AND publication_id = '')),
    CHECK (project_id = btrim(project_id) AND char_length(project_id) <= 255 AND project_id !~ '[[:cntrl:]]'),
    CHECK (publication_id = btrim(publication_id) AND char_length(publication_id) <= 255 AND publication_id !~ '[[:cntrl:]]'),
    CHECK (principal_or_client = btrim(principal_or_client) AND char_length(principal_or_client) BETWEEN 1 AND 255 AND principal_or_client !~ '[[:cntrl:]]'),
    CHECK (dashboard_id = btrim(dashboard_id) AND char_length(dashboard_id) BETWEEN 1 AND 255 AND dashboard_id !~ '[[:cntrl:]]'),
    CHECK (serving_state_id = btrim(serving_state_id) AND char_length(serving_state_id) BETWEEN 1 AND 255 AND serving_state_id !~ '[[:cntrl:]]'),
    CHECK (stream_instance_id = btrim(stream_instance_id) AND char_length(stream_instance_id) BETWEEN 1 AND 255 AND stream_instance_id !~ '[[:cntrl:]]'),
    CHECK (jsonb_typeof(key_json) = 'object' AND octet_length(key_json::text) <= 16384),
    CHECK (jsonb_typeof(state_json) = 'object' AND octet_length(state_json::text) <= 1048576)
);

CREATE INDEX IF NOT EXISTS view_session_expiry_idx
    ON dashboard.view_session(expires_at);

REVOKE ALL ON SCHEMA dashboard FROM PUBLIC;
REVOKE ALL ON TABLE dashboard.view_session FROM PUBLIC;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA dashboard TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON dashboard.view_session TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA dashboard TO leapview_control_maintenance;
        GRANT SELECT, DELETE ON dashboard.view_session TO leapview_control_maintenance;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA dashboard TO leapview_control_readonly;
        GRANT SELECT ON dashboard.view_session TO leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA dashboard TO leapview_control_backup;
        GRANT SELECT ON dashboard.view_session TO leapview_control_backup;
    END IF;
END
$$;
-- +goose StatementEnd

-- capability source: internal/dashboard/usage/postgres/schema.sql
-- Native PostgreSQL dashboard viewer-day authority.
CREATE SCHEMA IF NOT EXISTS dashboard;

CREATE TABLE IF NOT EXISTS dashboard.view_day (
    project_id text NOT NULL,
    dashboard_id text NOT NULL,
    principal_id text NOT NULL,
    viewed_on date NOT NULL,
    page_id text NOT NULL,
    first_viewed_at timestamptz NOT NULL,
    last_viewed_at timestamptz NOT NULL,
    PRIMARY KEY (project_id, dashboard_id, principal_id, viewed_on),
    CHECK (project_id = btrim(project_id) AND char_length(project_id) BETWEEN 1 AND 255 AND project_id !~ '[[:cntrl:]]'),
    CHECK (dashboard_id = btrim(dashboard_id) AND char_length(dashboard_id) BETWEEN 1 AND 255 AND dashboard_id !~ '[[:cntrl:]]'),
    CHECK (principal_id = btrim(principal_id) AND char_length(principal_id) BETWEEN 1 AND 255 AND principal_id !~ '[[:cntrl:]]'),
    CHECK (page_id = btrim(page_id) AND char_length(page_id) BETWEEN 1 AND 255 AND page_id !~ '[[:cntrl:]]'),
    CHECK (last_viewed_at >= first_viewed_at)
);

CREATE INDEX IF NOT EXISTS view_day_recent_idx
    ON dashboard.view_day(last_viewed_at DESC, project_id, dashboard_id);

REVOKE ALL ON TABLE dashboard.view_day FROM PUBLIC;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA dashboard TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON dashboard.view_day TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA dashboard TO leapview_control_maintenance;
        GRANT SELECT, DELETE ON dashboard.view_day TO leapview_control_maintenance;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA dashboard TO leapview_control_readonly;
        GRANT SELECT ON dashboard.view_day TO leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA dashboard TO leapview_control_backup;
        GRANT SELECT ON dashboard.view_day TO leapview_control_backup;
    END IF;
END
$$;
-- +goose StatementEnd

-- capability source: internal/dashboard/appearance/postgres/schema.sql
-- Native PostgreSQL dashboard appearance override authority.
CREATE SCHEMA IF NOT EXISTS dashboard;

CREATE TABLE IF NOT EXISTS dashboard.appearance_override (
    project_id text NOT NULL,
    dashboard_id text NOT NULL,
    icon text,
    color text,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    updated_by text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (project_id, dashboard_id),
    CHECK (project_id = btrim(project_id) AND char_length(project_id) BETWEEN 1 AND 255 AND project_id !~ '[[:cntrl:]]'),
    CHECK (dashboard_id = btrim(dashboard_id) AND char_length(dashboard_id) BETWEEN 1 AND 255 AND dashboard_id !~ '[[:cntrl:]]'),
    CHECK (updated_by = btrim(updated_by) AND char_length(updated_by) <= 255 AND updated_by !~ '[[:cntrl:]]'),
    CHECK (icon IS NULL OR (icon = btrim(icon) AND char_length(icon) BETWEEN 1 AND 255 AND icon !~ '[[:cntrl:]]')),
    CHECK (color IS NULL OR color IN ('gray', 'blue', 'green', 'yellow', 'orange', 'red', 'purple', 'pink', 'coral'))
);

REVOKE ALL ON TABLE dashboard.appearance_override FROM PUBLIC;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA dashboard TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON dashboard.appearance_override TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA dashboard TO leapview_control_readonly;
        GRANT SELECT ON dashboard.appearance_override TO leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA dashboard TO leapview_control_backup;
        GRANT SELECT ON dashboard.appearance_override TO leapview_control_backup;
    END IF;
END
$$;
-- +goose StatementEnd

-- capability source: internal/dashboard/authoring/postgres/schema.sql
-- Native PostgreSQL dashboard authoring authority.
--
-- This capability is also applied in isolation by package tests, so project
-- and access foreign keys are attached conditionally below.  The production
-- baseline creates those authorities first and therefore always installs the
-- same constraints and owner trigger.
CREATE SCHEMA IF NOT EXISTS dashboard;

CREATE TABLE IF NOT EXISTS dashboard.authoring_dashboards (
    project_id text NOT NULL,
    dashboard_id text NOT NULL,
    owner_principal_id uuid NOT NULL,
    slug text NOT NULL,
    title text NOT NULL,
    semantic_model text NOT NULL,
    visibility text NOT NULL CHECK (visibility IN ('private','restricted','organization')),
    status text NOT NULL CHECK (status IN ('draft','published','archived')),
    -- Last audited domain-event identity.  Guarded mutation functions set it
    -- on every audited lifecycle transition; the deferred trigger below
    -- proves matching event and audit rows before a runtime transaction can
    -- commit.
    last_event_id uuid,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (project_id, dashboard_id),
    UNIQUE (project_id, slug),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255),
    CHECK (dashboard_id = btrim(dashboard_id) AND octet_length(dashboard_id) BETWEEN 1 AND 255),
    CHECK (slug = btrim(slug) AND octet_length(slug) BETWEEN 1 AND 128 AND slug ~ '^[a-z0-9][a-z0-9-]*$'),
    CHECK (title = btrim(title) AND octet_length(title) BETWEEN 1 AND 512),
    CHECK (semantic_model = btrim(semantic_model) AND octet_length(semantic_model) BETWEEN 1 AND 255),
    CHECK (updated_at >= created_at)
);

CREATE TABLE IF NOT EXISTS dashboard.authoring_revisions (
    project_id text NOT NULL,
    dashboard_id text NOT NULL,
    revision_id uuid NOT NULL,
    revision_number bigint NOT NULL CHECK (revision_number > 0),
    document_json jsonb NOT NULL CHECK (jsonb_typeof(document_json) = 'object' AND octet_length(document_json::text) <= 1048576),
    content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
    provenance_json jsonb NOT NULL CHECK (jsonb_typeof(provenance_json) = 'object' AND octet_length(provenance_json::text) <= 32768),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (project_id, dashboard_id, revision_id),
    UNIQUE (project_id, dashboard_id, revision_number),
    UNIQUE (project_id, dashboard_id, revision_id, revision_number, content_hash),
    FOREIGN KEY (project_id, dashboard_id) REFERENCES dashboard.authoring_dashboards(project_id, dashboard_id) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS dashboard.authoring_drafts (
    project_id text NOT NULL,
    dashboard_id text NOT NULL,
    draft_id uuid NOT NULL,
    revision_id uuid NOT NULL,
    revision_number bigint NOT NULL CHECK (revision_number > 0),
    content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
    provenance_json jsonb NOT NULL CHECK (jsonb_typeof(provenance_json) = 'object' AND octet_length(provenance_json::text) <= 32768),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (project_id, dashboard_id),
    UNIQUE (project_id, dashboard_id, draft_id),
    FOREIGN KEY (project_id, dashboard_id, revision_id, revision_number, content_hash)
        REFERENCES dashboard.authoring_revisions(project_id, dashboard_id, revision_id, revision_number, content_hash) ON DELETE RESTRICT,
    FOREIGN KEY (project_id, dashboard_id)
        REFERENCES dashboard.authoring_dashboards(project_id, dashboard_id) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS dashboard.authoring_compiled_revisions (
    project_id text NOT NULL,
    dashboard_id text NOT NULL,
    revision_id uuid NOT NULL,
    revision_number bigint NOT NULL CHECK (revision_number > 0),
    content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
    definition_json jsonb NOT NULL CHECK (jsonb_typeof(definition_json) = 'object' AND octet_length(definition_json::text) <= 1048576),
    definition_hash text NOT NULL CHECK (definition_hash ~ '^sha256:[0-9a-f]{64}$'),
    semantic_model_id text NOT NULL CHECK (semantic_model_id = btrim(semantic_model_id) AND octet_length(semantic_model_id) BETWEEN 1 AND 255),
    semantic_identity_json jsonb NOT NULL CHECK (jsonb_typeof(semantic_identity_json) = 'object' AND octet_length(semantic_identity_json::text) <= 32768),
    compiled_at timestamptz NOT NULL,
    PRIMARY KEY (project_id, dashboard_id, revision_id, revision_number, content_hash, definition_hash, semantic_model_id, semantic_identity_json),
    FOREIGN KEY (project_id, dashboard_id, revision_id, revision_number, content_hash)
        REFERENCES dashboard.authoring_revisions(project_id, dashboard_id, revision_id, revision_number, content_hash) ON DELETE RESTRICT,
    FOREIGN KEY (project_id, dashboard_id)
        REFERENCES dashboard.authoring_dashboards(project_id, dashboard_id) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS dashboard.authoring_published (
    project_id text NOT NULL,
    dashboard_id text NOT NULL,
    revision_id uuid NOT NULL,
    revision_number bigint NOT NULL CHECK (revision_number > 0),
    content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
    compiled_revision_id uuid NOT NULL,
    compiled_revision_number bigint NOT NULL CHECK (compiled_revision_number > 0),
    compiled_content_hash text NOT NULL CHECK (compiled_content_hash ~ '^sha256:[0-9a-f]{64}$'),
    compiled_definition_hash text NOT NULL CHECK (compiled_definition_hash ~ '^sha256:[0-9a-f]{64}$'),
    compiled_semantic_model_id text NOT NULL CHECK (compiled_semantic_model_id = btrim(compiled_semantic_model_id) AND octet_length(compiled_semantic_model_id) BETWEEN 1 AND 255),
    compiled_semantic_identity_json jsonb NOT NULL CHECK (jsonb_typeof(compiled_semantic_identity_json) = 'object' AND octet_length(compiled_semantic_identity_json::text) <= 32768),
    provenance_json jsonb NOT NULL CHECK (jsonb_typeof(provenance_json) = 'object' AND octet_length(provenance_json::text) <= 32768),
    published_at timestamptz NOT NULL,
    PRIMARY KEY (project_id, dashboard_id),
    FOREIGN KEY (project_id, dashboard_id, revision_id, revision_number, content_hash)
        REFERENCES dashboard.authoring_revisions(project_id, dashboard_id, revision_id, revision_number, content_hash) ON DELETE RESTRICT,
    FOREIGN KEY (project_id, dashboard_id, compiled_revision_id, compiled_revision_number, compiled_content_hash, compiled_definition_hash, compiled_semantic_model_id, compiled_semantic_identity_json)
        REFERENCES dashboard.authoring_compiled_revisions(project_id, dashboard_id, revision_id, revision_number, content_hash, definition_hash, semantic_model_id, semantic_identity_json) ON DELETE RESTRICT,
    FOREIGN KEY (project_id, dashboard_id)
        REFERENCES dashboard.authoring_dashboards(project_id, dashboard_id) ON DELETE RESTRICT,
    CHECK (revision_id = compiled_revision_id AND revision_number = compiled_revision_number AND content_hash = compiled_content_hash)
);

CREATE TABLE IF NOT EXISTS dashboard.authoring_commands (
    project_id text NOT NULL,
    dashboard_id text NOT NULL,
    command_id uuid NOT NULL,
    request_fingerprint text NOT NULL CHECK (request_fingerprint = btrim(request_fingerprint) AND octet_length(request_fingerprint) BETWEEN 1 AND 255),
    action text NOT NULL CHECK (action IN ('edit','publish','archive')),
    provenance_json jsonb NOT NULL CHECK (jsonb_typeof(provenance_json) = 'object' AND octet_length(provenance_json::text) <= 32768),
    occurred_at timestamptz NOT NULL,
    result_revision_id uuid,
    result_revision_number bigint,
    result_content_hash text,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (project_id, dashboard_id, command_id),
    FOREIGN KEY (project_id, dashboard_id) REFERENCES dashboard.authoring_dashboards(project_id, dashboard_id) ON DELETE RESTRICT,
    FOREIGN KEY (project_id, dashboard_id, result_revision_id, result_revision_number, result_content_hash)
        REFERENCES dashboard.authoring_revisions(project_id, dashboard_id, revision_id, revision_number, content_hash) ON DELETE RESTRICT,
    CHECK ((result_revision_id IS NULL AND result_revision_number IS NULL AND result_content_hash IS NULL)
        OR (result_revision_id IS NOT NULL AND result_revision_number > 0 AND result_content_hash ~ '^sha256:[0-9a-f]{64}$'))
);

CREATE TABLE IF NOT EXISTS dashboard.authoring_create_operations (
    project_id text NOT NULL,
    actor_id text NOT NULL CHECK (actor_id = btrim(actor_id) AND octet_length(actor_id) BETWEEN 1 AND 255),
    operation_kind text NOT NULL CHECK (operation_kind IN ('create','fork')),
    idempotency_key text NOT NULL CHECK (idempotency_key = btrim(idempotency_key) AND octet_length(idempotency_key) BETWEEN 1 AND 512),
    conversation_id text NOT NULL DEFAULT '' CHECK (octet_length(conversation_id) <= 512),
    tool_call_id text NOT NULL DEFAULT '' CHECK (octet_length(tool_call_id) <= 512),
    request_fingerprint text NOT NULL CHECK (request_fingerprint = btrim(request_fingerprint) AND octet_length(request_fingerprint) BETWEEN 1 AND 255),
    dashboard_id text NOT NULL,
    result_revision_id uuid NOT NULL,
    result_revision_number bigint NOT NULL CHECK (result_revision_number > 0),
    result_content_hash text NOT NULL CHECK (result_content_hash ~ '^sha256:[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (project_id, actor_id, operation_kind, idempotency_key),
    FOREIGN KEY (project_id, dashboard_id) REFERENCES dashboard.authoring_dashboards(project_id, dashboard_id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY (project_id, dashboard_id, result_revision_id, result_revision_number, result_content_hash)
        REFERENCES dashboard.authoring_revisions(project_id, dashboard_id, revision_id, revision_number, content_hash) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
);
CREATE TABLE IF NOT EXISTS dashboard.authoring_revalidation_attempts (
    project_id text NOT NULL,
    dashboard_id text NOT NULL,
    generation_id text NOT NULL CHECK (generation_id = btrim(generation_id) AND octet_length(generation_id) BETWEEN 1 AND 255),
    attempt_id uuid NOT NULL,
    generation_identity_json jsonb NOT NULL CHECK (jsonb_typeof(generation_identity_json) = 'object' AND octet_length(generation_identity_json::text) <= 32768),
    graph_digest text NOT NULL CHECK (graph_digest ~ '^sha256:[0-9a-f]{64}$'),
    dependency_ids_json jsonb NOT NULL CHECK (jsonb_typeof(dependency_ids_json) = 'array' AND octet_length(dependency_ids_json::text) <= 32768),
    authored_revision_id uuid NOT NULL,
    authored_revision_number bigint NOT NULL CHECK (authored_revision_number > 0),
    authored_content_hash text NOT NULL CHECK (authored_content_hash ~ '^sha256:[0-9a-f]{64}$'),
    prior_compiled_identity_json jsonb NOT NULL CHECK (jsonb_typeof(prior_compiled_identity_json) = 'object' AND octet_length(prior_compiled_identity_json::text) <= 32768),
    status text NOT NULL CHECK (status IN ('succeeded','failed')),
    error_code text CHECK (error_code IS NULL OR (error_code = btrim(error_code) AND octet_length(error_code) BETWEEN 1 AND 255)),
    error_message text CHECK (error_message IS NULL OR (error_message = btrim(error_message) AND octet_length(error_message) BETWEEN 1 AND 4096)),
    compiled_definition_hash text CHECK (compiled_definition_hash IS NULL OR compiled_definition_hash ~ '^sha256:[0-9a-f]{64}$'),
    compiled_semantic_model_id text CHECK (compiled_semantic_model_id IS NULL OR (compiled_semantic_model_id = btrim(compiled_semantic_model_id) AND octet_length(compiled_semantic_model_id) BETWEEN 1 AND 255)),
    compiled_semantic_identity_json jsonb CHECK (compiled_semantic_identity_json IS NULL OR (jsonb_typeof(compiled_semantic_identity_json) = 'object' AND octet_length(compiled_semantic_identity_json::text) <= 32768)),
    attempted_at timestamptz NOT NULL,
    PRIMARY KEY (project_id, dashboard_id, generation_id, attempt_id),
    FOREIGN KEY (project_id, dashboard_id) REFERENCES dashboard.authoring_dashboards(project_id, dashboard_id) ON DELETE RESTRICT,
    FOREIGN KEY (project_id, dashboard_id, authored_revision_id, authored_revision_number, authored_content_hash)
        REFERENCES dashboard.authoring_revisions(project_id, dashboard_id, revision_id, revision_number, content_hash) ON DELETE RESTRICT,
    FOREIGN KEY (project_id, dashboard_id, authored_revision_id, authored_revision_number, authored_content_hash, compiled_definition_hash, compiled_semantic_model_id, compiled_semantic_identity_json)
        REFERENCES dashboard.authoring_compiled_revisions(project_id, dashboard_id, revision_id, revision_number, content_hash, definition_hash, semantic_model_id, semantic_identity_json) ON DELETE RESTRICT,
    CHECK ((status = 'failed' AND error_code IS NOT NULL AND btrim(error_code) <> '' AND error_message IS NOT NULL AND btrim(error_message) <> '' AND compiled_definition_hash IS NULL AND compiled_semantic_model_id IS NULL AND compiled_semantic_identity_json IS NULL)
        OR (status = 'succeeded' AND error_code IS NULL AND error_message IS NULL AND compiled_definition_hash IS NOT NULL AND compiled_semantic_model_id IS NOT NULL AND btrim(compiled_semantic_model_id) <> '' AND compiled_semantic_identity_json IS NOT NULL AND jsonb_typeof(compiled_semantic_identity_json) = 'object'))
);

CREATE INDEX IF NOT EXISTS authoring_dashboards_project_idx ON dashboard.authoring_dashboards(project_id, semantic_model, status, visibility, slug, dashboard_id);
CREATE INDEX IF NOT EXISTS authoring_revisions_project_idx ON dashboard.authoring_revisions(project_id, dashboard_id, revision_number);
CREATE INDEX IF NOT EXISTS authoring_compiled_project_idx ON dashboard.authoring_compiled_revisions(project_id, dashboard_id, revision_number);
CREATE INDEX IF NOT EXISTS authoring_revalidation_project_idx ON dashboard.authoring_revalidation_attempts(project_id, dashboard_id, attempted_at DESC);

-- Authoring projections retain stable identities while their pointers advance.
-- Keep these invariants in the database as well as in the repository so a
-- capability role cannot rewrite a dashboard or bypass lifecycle/CAS rules
-- with an ad-hoc UPDATE.
CREATE OR REPLACE FUNCTION dashboard.guard_authoring_dashboard_update()
RETURNS trigger
LANGUAGE plpgsql
-- +goose StatementBegin
AS $$
BEGIN
    IF OLD.project_id IS DISTINCT FROM NEW.project_id
       OR OLD.dashboard_id IS DISTINCT FROM NEW.dashboard_id
       OR OLD.owner_principal_id IS DISTINCT FROM NEW.owner_principal_id
       OR OLD.created_at IS DISTINCT FROM NEW.created_at THEN
        RAISE EXCEPTION 'authoring dashboard identity is immutable';
    END IF;
    IF OLD.status = 'draft' AND NEW.status NOT IN ('draft','published','archived') THEN
        RAISE EXCEPTION 'invalid authoring dashboard lifecycle transition';
    ELSIF OLD.status = 'published' AND NEW.status NOT IN ('published','archived') THEN
        RAISE EXCEPTION 'invalid authoring dashboard lifecycle transition';
    ELSIF OLD.status = 'archived' AND NEW.status <> 'archived' THEN
        RAISE EXCEPTION 'invalid authoring dashboard lifecycle transition';
    END IF;
    IF NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'authoring dashboard updated_at is monotonic';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION dashboard.guard_authoring_draft_update()
RETURNS trigger
LANGUAGE plpgsql
-- +goose StatementBegin
AS $$
BEGIN
    IF OLD.project_id IS DISTINCT FROM NEW.project_id
       OR OLD.dashboard_id IS DISTINCT FROM NEW.dashboard_id
       OR OLD.draft_id IS DISTINCT FROM NEW.draft_id THEN
        RAISE EXCEPTION 'authoring draft identity is immutable';
    END IF;
    IF NEW.revision_number <> OLD.revision_number + 1 THEN
        RAISE EXCEPTION 'authoring draft revision must advance exactly one';
    END IF;
    IF NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'authoring draft updated_at is monotonic';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION dashboard.guard_authoring_published_update()
RETURNS trigger
LANGUAGE plpgsql
-- +goose StatementBegin
AS $$
BEGIN
    IF OLD.project_id IS DISTINCT FROM NEW.project_id
       OR OLD.dashboard_id IS DISTINCT FROM NEW.dashboard_id THEN
        RAISE EXCEPTION 'authoring published identity is immutable';
    END IF;
    IF NEW.revision_number < OLD.revision_number
       OR NEW.compiled_revision_number < OLD.compiled_revision_number THEN
        RAISE EXCEPTION 'authoring published revision cannot move backwards';
    END IF;
    IF NEW.published_at < OLD.published_at THEN
        RAISE EXCEPTION 'authoring published timestamp is monotonic';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS authoring_dashboards_mutation ON dashboard.authoring_dashboards;
CREATE TRIGGER authoring_dashboards_mutation
    BEFORE UPDATE ON dashboard.authoring_dashboards
    FOR EACH ROW EXECUTE FUNCTION dashboard.guard_authoring_dashboard_update();
DROP TRIGGER IF EXISTS authoring_drafts_mutation ON dashboard.authoring_drafts;
CREATE TRIGGER authoring_drafts_mutation
    BEFORE UPDATE ON dashboard.authoring_drafts
    FOR EACH ROW EXECUTE FUNCTION dashboard.guard_authoring_draft_update();
DROP TRIGGER IF EXISTS authoring_published_mutation ON dashboard.authoring_published;
CREATE TRIGGER authoring_published_mutation
    BEFORE UPDATE ON dashboard.authoring_published
    FOR EACH ROW EXECUTE FUNCTION dashboard.guard_authoring_published_update();

-- Application writes enter through owner-owned SECURITY DEFINER functions.
-- The runtime role deliberately has no projection-table DML privileges; these
-- entrypoints keep the state transition and its command/attempt evidence in
-- one statement while the caller retains the outer transaction (so audit and
-- domain-event adapters can still roll the entire operation back).
CREATE OR REPLACE FUNCTION dashboard.authoring_create_dashboard(
    p_project_id text, p_dashboard_id text, p_owner_principal_id uuid,
    p_slug text, p_title text, p_semantic_model text, p_visibility text,
    p_status text, p_revision_id uuid, p_revision_number bigint,
    p_document_json jsonb, p_content_hash text, p_provenance_json jsonb,
    p_created_at timestamptz, p_draft_id uuid, p_draft_provenance_json jsonb,
    p_operation_enabled boolean, p_actor_id text, p_operation_kind text,
    p_idempotency_key text, p_conversation_id text, p_tool_call_id text,
    p_request_fingerprint text, p_event_id uuid
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, dashboard
-- +goose StatementBegin
AS $$
DECLARE
    v_existing_fingerprint text;
    v_inserted bigint;
BEGIN
    IF p_status <> 'draft' THEN
        RAISE EXCEPTION 'authoring dashboard creation requires draft lifecycle status';
    END IF;
    IF p_operation_enabled THEN
        INSERT INTO dashboard.authoring_create_operations(
            project_id, actor_id, operation_kind, idempotency_key,
            conversation_id, tool_call_id, request_fingerprint,
            dashboard_id, result_revision_id, result_revision_number,
            result_content_hash
        ) VALUES (
            p_project_id, p_actor_id, p_operation_kind, p_idempotency_key,
            p_conversation_id, p_tool_call_id, p_request_fingerprint,
            p_dashboard_id, p_revision_id, p_revision_number, p_content_hash
        ) ON CONFLICT (project_id, actor_id, operation_kind, idempotency_key)
          DO NOTHING;
        GET DIAGNOSTICS v_inserted = ROW_COUNT;
        IF v_inserted = 0 THEN
            SELECT request_fingerprint INTO v_existing_fingerprint
              FROM dashboard.authoring_create_operations
             WHERE project_id = p_project_id AND actor_id = p_actor_id
               AND operation_kind = p_operation_kind
               AND idempotency_key = p_idempotency_key;
            IF v_existing_fingerprint IS DISTINCT FROM p_request_fingerprint THEN
                RAISE EXCEPTION 'authoring create operation request fingerprint differs';
            END IF;
            RETURN 0;
        END IF;
    END IF;

    INSERT INTO dashboard.authoring_dashboards(
        project_id, dashboard_id, owner_principal_id, slug, title,
        semantic_model, visibility, status, last_event_id
    ) VALUES (
        p_project_id, p_dashboard_id, p_owner_principal_id, p_slug, p_title,
        p_semantic_model, p_visibility, p_status, p_event_id
    );
    INSERT INTO dashboard.authoring_revisions(
        project_id, dashboard_id, revision_id, revision_number,
        document_json, content_hash, provenance_json, created_at
    ) VALUES (
        p_project_id, p_dashboard_id, p_revision_id, p_revision_number,
        p_document_json, p_content_hash, p_provenance_json, p_created_at
    );
    INSERT INTO dashboard.authoring_drafts(
        project_id, dashboard_id, draft_id, revision_id, revision_number,
        content_hash, provenance_json
    ) VALUES (
        p_project_id, p_dashboard_id, p_draft_id, p_revision_id,
        p_revision_number, p_content_hash, p_draft_provenance_json
    );
    RETURN 1;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION dashboard.authoring_append_draft(
    p_project_id text, p_dashboard_id text, p_slug text, p_title text,
    p_semantic_model text, p_visibility text, p_status text,
    p_revision_id uuid, p_revision_number bigint, p_document_json jsonb,
    p_content_hash text, p_provenance_json jsonb, p_created_at timestamptz,
    p_draft_provenance_json jsonb, p_expected_revision_id uuid,
    p_expected_revision_number bigint, p_expected_content_hash text,
    p_command_id uuid, p_request_fingerprint text, p_action text,
    p_command_provenance_json jsonb, p_occurred_at timestamptz,
    p_event_id uuid
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, dashboard
-- +goose StatementBegin
AS $$
DECLARE
    v_existing_fingerprint text;
    v_rows bigint;
    v_status text;
BEGIN
    SELECT status INTO v_status FROM dashboard.authoring_dashboards
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
     FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'authoring dashboard was not found'; END IF;
    SELECT request_fingerprint INTO v_existing_fingerprint
      FROM dashboard.authoring_commands
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
       AND command_id = p_command_id;
    IF FOUND THEN
        IF v_existing_fingerprint IS DISTINCT FROM p_request_fingerprint THEN
            RAISE EXCEPTION 'authoring command request fingerprint differs';
        END IF;
        RETURN 0;
    END IF;
    IF p_action <> 'edit' OR p_status IS DISTINCT FROM v_status OR v_status = 'archived' THEN
        RAISE EXCEPTION 'authoring draft append does not match lifecycle state';
    END IF;

    INSERT INTO dashboard.authoring_revisions(
        project_id, dashboard_id, revision_id, revision_number,
        document_json, content_hash, provenance_json, created_at
    ) VALUES (
        p_project_id, p_dashboard_id, p_revision_id, p_revision_number,
        p_document_json, p_content_hash, p_provenance_json, p_created_at
    );
    UPDATE dashboard.authoring_dashboards
       SET slug = p_slug, title = p_title, semantic_model = p_semantic_model,
           visibility = p_visibility, status = p_status,
           last_event_id = p_event_id,
           updated_at = clock_timestamp()
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id;
    UPDATE dashboard.authoring_drafts
       SET revision_id = p_revision_id, revision_number = p_revision_number,
           content_hash = p_content_hash,
           provenance_json = p_draft_provenance_json,
           updated_at = clock_timestamp()
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
       AND revision_id = p_expected_revision_id
       AND revision_number = p_expected_revision_number
       AND content_hash = p_expected_content_hash;
    GET DIAGNOSTICS v_rows = ROW_COUNT;
    IF v_rows <> 1 THEN RAISE EXCEPTION 'authoring draft compare-and-swap conflict'; END IF;

    INSERT INTO dashboard.authoring_commands(
        project_id, dashboard_id, command_id, request_fingerprint, action,
        provenance_json, occurred_at, result_revision_id,
        result_revision_number, result_content_hash
    ) VALUES (
        p_project_id, p_dashboard_id, p_command_id, p_request_fingerprint,
        p_action, p_command_provenance_json, p_occurred_at,
        p_revision_id, p_revision_number, p_content_hash
    );
    RETURN 1;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION dashboard.authoring_publish_dashboard(
    p_project_id text, p_dashboard_id text, p_slug text, p_title text,
    p_semantic_model text, p_visibility text, p_status text,
    p_revision_id uuid, p_revision_number bigint, p_content_hash text,
    p_definition_json jsonb, p_definition_hash text,
    p_compiled_semantic_model_id text, p_compiled_semantic_identity_json jsonb,
    p_compiled_at timestamptz, p_provenance_json jsonb, p_published_at timestamptz,
    p_command_id uuid, p_request_fingerprint text, p_action text,
    p_command_provenance_json jsonb, p_occurred_at timestamptz,
    p_event_id uuid
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, dashboard
-- +goose StatementBegin
AS $$
DECLARE
    v_existing_fingerprint text;
    v_existing_definition jsonb;
    v_status text;
BEGIN
    SELECT status INTO v_status FROM dashboard.authoring_dashboards
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
     FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'authoring dashboard was not found'; END IF;
    SELECT request_fingerprint INTO v_existing_fingerprint
      FROM dashboard.authoring_commands
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
       AND command_id = p_command_id;
    IF FOUND THEN
        IF v_existing_fingerprint IS DISTINCT FROM p_request_fingerprint THEN
            RAISE EXCEPTION 'authoring command request fingerprint differs';
        END IF;
        RETURN 0;
    END IF;
    IF p_action <> 'publish' OR p_status <> 'published' OR v_status NOT IN ('draft','published') THEN
        RAISE EXCEPTION 'authoring dashboard is not publishable';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM dashboard.authoring_drafts
         WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
           AND revision_id = p_revision_id
           AND revision_number = p_revision_number
           AND content_hash = p_content_hash
    ) THEN
        RAISE EXCEPTION 'authoring publish compare-and-swap conflict';
    END IF;
    INSERT INTO dashboard.authoring_compiled_revisions(
        project_id, dashboard_id, revision_id, revision_number, content_hash,
        definition_json, definition_hash, semantic_model_id,
        semantic_identity_json, compiled_at
    ) VALUES (
        p_project_id, p_dashboard_id, p_revision_id, p_revision_number,
        p_content_hash, p_definition_json, p_definition_hash,
        p_compiled_semantic_model_id, p_compiled_semantic_identity_json,
        p_compiled_at
    ) ON CONFLICT DO NOTHING;
    SELECT definition_json INTO v_existing_definition
      FROM dashboard.authoring_compiled_revisions
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
       AND revision_id = p_revision_id AND revision_number = p_revision_number
       AND content_hash = p_content_hash AND definition_hash = p_definition_hash
       AND semantic_model_id = p_compiled_semantic_model_id
       AND semantic_identity_json = p_compiled_semantic_identity_json;
    IF NOT FOUND OR v_existing_definition IS DISTINCT FROM p_definition_json THEN
        RAISE EXCEPTION 'authoring compiled revision identity is immutable';
    END IF;
    UPDATE dashboard.authoring_dashboards
       SET slug = p_slug, title = p_title, semantic_model = p_semantic_model,
           visibility = p_visibility, status = p_status,
           last_event_id = p_event_id,
           updated_at = clock_timestamp()
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id;
    INSERT INTO dashboard.authoring_published(
        project_id, dashboard_id, revision_id, revision_number, content_hash,
        compiled_revision_id, compiled_revision_number, compiled_content_hash,
        compiled_definition_hash, compiled_semantic_model_id,
        compiled_semantic_identity_json, provenance_json, published_at
    ) VALUES (
        p_project_id, p_dashboard_id, p_revision_id, p_revision_number,
        p_content_hash, p_revision_id, p_revision_number, p_content_hash,
        p_definition_hash, p_compiled_semantic_model_id,
        p_compiled_semantic_identity_json, p_provenance_json, p_published_at
    ) ON CONFLICT (project_id, dashboard_id) DO UPDATE SET
        revision_id = excluded.revision_id, revision_number = excluded.revision_number,
        content_hash = excluded.content_hash,
        compiled_revision_id = excluded.compiled_revision_id,
        compiled_revision_number = excluded.compiled_revision_number,
        compiled_content_hash = excluded.compiled_content_hash,
        compiled_definition_hash = excluded.compiled_definition_hash,
        compiled_semantic_model_id = excluded.compiled_semantic_model_id,
        compiled_semantic_identity_json = excluded.compiled_semantic_identity_json,
        provenance_json = excluded.provenance_json,
        published_at = excluded.published_at;
    INSERT INTO dashboard.authoring_commands(
        project_id, dashboard_id, command_id, request_fingerprint, action,
        provenance_json, occurred_at, result_revision_id,
        result_revision_number, result_content_hash
    ) VALUES (
        p_project_id, p_dashboard_id, p_command_id, p_request_fingerprint,
        p_action, p_command_provenance_json, p_occurred_at,
        p_revision_id, p_revision_number, p_content_hash
    );
    RETURN 1;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION dashboard.authoring_archive_dashboard(
    p_project_id text, p_dashboard_id text, p_expected_revision_id uuid,
    p_expected_revision_number bigint, p_expected_content_hash text,
    p_command_id uuid, p_request_fingerprint text, p_action text,
    p_command_provenance_json jsonb, p_occurred_at timestamptz,
    p_event_id uuid
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, dashboard
-- +goose StatementBegin
AS $$
DECLARE
    v_existing_fingerprint text;
    v_rows bigint;
BEGIN
    PERFORM 1 FROM dashboard.authoring_dashboards
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
     FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'authoring dashboard was not found'; END IF;
    SELECT request_fingerprint INTO v_existing_fingerprint
      FROM dashboard.authoring_commands
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
       AND command_id = p_command_id;
    IF FOUND THEN
        IF v_existing_fingerprint IS DISTINCT FROM p_request_fingerprint THEN
            RAISE EXCEPTION 'authoring command request fingerprint differs';
        END IF;
        RETURN 0;
    END IF;
    IF p_action <> 'archive' THEN
        RAISE EXCEPTION 'authoring archive requires archive command evidence';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM dashboard.authoring_drafts
         WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
           AND revision_id = p_expected_revision_id
           AND revision_number = p_expected_revision_number
           AND content_hash = p_expected_content_hash
    ) AND NOT EXISTS (
        SELECT 1 FROM dashboard.authoring_published
         WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
           AND revision_id = p_expected_revision_id
           AND revision_number = p_expected_revision_number
           AND content_hash = p_expected_content_hash
    ) THEN
        RAISE EXCEPTION 'authoring archive compare-and-swap conflict';
    END IF;
    UPDATE dashboard.authoring_dashboards
       SET status = 'archived', last_event_id = p_event_id, updated_at = clock_timestamp()
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
       AND status <> 'archived';
    GET DIAGNOSTICS v_rows = ROW_COUNT;
    IF v_rows <> 1 THEN RAISE EXCEPTION 'authoring dashboard lifecycle conflict'; END IF;
    INSERT INTO dashboard.authoring_commands(
        project_id, dashboard_id, command_id, request_fingerprint, action,
        provenance_json, occurred_at, result_revision_id,
        result_revision_number, result_content_hash
    ) VALUES (
        p_project_id, p_dashboard_id, p_command_id, p_request_fingerprint,
        p_action, p_command_provenance_json, p_occurred_at,
        p_expected_revision_id, p_expected_revision_number, p_expected_content_hash
    );
    RETURN 1;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION dashboard.authoring_commit_revalidation(
    p_project_id text, p_dashboard_id text, p_revision_id uuid,
    p_revision_number bigint, p_content_hash text, p_definition_json jsonb,
    p_definition_hash text, p_semantic_model_id text,
    p_semantic_identity_json jsonb, p_compiled_at timestamptz,
    p_generation_id text, p_attempt_id uuid, p_generation_identity_json jsonb,
    p_graph_digest text, p_dependency_ids_json jsonb,
    p_authored_revision_id uuid, p_authored_revision_number bigint,
    p_authored_content_hash text, p_prior_compiled_identity_json jsonb,
    p_attempted_at timestamptz, p_prior_compiled_revision_id uuid,
    p_prior_compiled_revision_number bigint, p_prior_compiled_content_hash text,
    p_prior_compiled_definition_hash text, p_prior_compiled_semantic_model_id text
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, dashboard
-- +goose StatementBegin
AS $$
DECLARE v_rows bigint;
BEGIN
    INSERT INTO dashboard.authoring_compiled_revisions(
        project_id, dashboard_id, revision_id, revision_number, content_hash,
        definition_json, definition_hash, semantic_model_id,
        semantic_identity_json, compiled_at
    ) VALUES (
        p_project_id, p_dashboard_id, p_revision_id, p_revision_number,
        p_content_hash, p_definition_json, p_definition_hash,
        p_semantic_model_id, p_semantic_identity_json, p_compiled_at
    ) ON CONFLICT DO NOTHING;
    INSERT INTO dashboard.authoring_revalidation_attempts(
        project_id, dashboard_id, generation_id, attempt_id,
        generation_identity_json, graph_digest, dependency_ids_json,
        authored_revision_id, authored_revision_number, authored_content_hash,
        prior_compiled_identity_json, status, compiled_definition_hash,
        compiled_semantic_model_id, compiled_semantic_identity_json, attempted_at
    ) VALUES (
        p_project_id, p_dashboard_id, p_generation_id, p_attempt_id,
        p_generation_identity_json, p_graph_digest, p_dependency_ids_json,
        p_authored_revision_id, p_authored_revision_number, p_authored_content_hash,
        p_prior_compiled_identity_json, 'succeeded', p_definition_hash,
        p_semantic_model_id, p_semantic_identity_json, p_attempted_at
    );
    UPDATE dashboard.authoring_published
       SET compiled_revision_id = p_revision_id,
           compiled_revision_number = p_revision_number,
           compiled_content_hash = p_content_hash,
           compiled_definition_hash = p_definition_hash,
           compiled_semantic_model_id = p_semantic_model_id,
           compiled_semantic_identity_json = p_semantic_identity_json
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
       AND revision_id = p_authored_revision_id
       AND revision_number = p_authored_revision_number
       AND content_hash = p_authored_content_hash
       AND compiled_revision_id = p_prior_compiled_revision_id
       AND compiled_revision_number = p_prior_compiled_revision_number
       AND compiled_content_hash = p_prior_compiled_content_hash
       AND compiled_definition_hash = p_prior_compiled_definition_hash
       AND compiled_semantic_model_id = p_prior_compiled_semantic_model_id
       AND compiled_semantic_identity_json = p_prior_compiled_identity_json;
    GET DIAGNOSTICS v_rows = ROW_COUNT;
    IF v_rows <> 1 THEN RAISE EXCEPTION 'authoring revalidation compare-and-swap conflict'; END IF;
    RETURN 1;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION dashboard.authoring_record_revalidation_failure(
    p_project_id text, p_dashboard_id text, p_generation_id text,
    p_attempt_id uuid, p_generation_identity_json jsonb, p_graph_digest text,
    p_dependency_ids_json jsonb, p_authored_revision_id uuid,
    p_authored_revision_number bigint, p_authored_content_hash text,
    p_prior_compiled_identity_json jsonb, p_error_code text,
    p_error_message text, p_attempted_at timestamptz
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, dashboard
-- +goose StatementBegin
AS $$
BEGIN
    INSERT INTO dashboard.authoring_revalidation_attempts(
        project_id, dashboard_id, generation_id, attempt_id,
        generation_identity_json, graph_digest, dependency_ids_json,
        authored_revision_id, authored_revision_number, authored_content_hash,
        prior_compiled_identity_json, status, error_code, error_message,
        attempted_at
    ) VALUES (
        p_project_id, p_dashboard_id, p_generation_id, p_attempt_id,
        p_generation_identity_json, p_graph_digest, p_dependency_ids_json,
        p_authored_revision_id, p_authored_revision_number, p_authored_content_hash,
        p_prior_compiled_identity_json, 'failed', p_error_code, p_error_message,
        p_attempted_at
    );
    RETURN 1;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION dashboard.guard_authoring_dashboard_evidence()
RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, dashboard, event, audit
-- +goose StatementBegin
AS $$
DECLARE v_ok boolean;
BEGIN
    IF NEW.last_event_id IS NULL THEN
        RAISE EXCEPTION 'authoring dashboard mutation requires canonical event identity';
    END IF;
    SELECT EXISTS (
        SELECT 1
          FROM event.event_log e
          JOIN audit.audit_event a ON a.event_id = e.event_id
         WHERE e.event_id = NEW.last_event_id
           AND e.scope_id = NEW.project_id
           AND e.aggregate_type = 'dashboard_authoring'
           AND e.aggregate_id = NEW.dashboard_id
           AND e.aggregate_version > 0
           AND a.scope_id = e.scope_id
           AND a.source = 'dashboard.authoring'
           AND a.capability = 'RESOURCE_EDIT'
           AND a.outcome = 'success'
           AND a.actor_id IS NOT NULL
           AND a.resource_kind = 'dashboard'
           AND a.resource_id = e.aggregate_id
           AND a.aggregate_key = ('dashboard_authoring:' || e.scope_id || ':' || e.aggregate_id)
           AND a.aggregate_sequence = e.aggregate_version
           -- event_log keeps only canonical UUID correlations.  Audit rows
           -- intentionally retain opaque request/client correlation values;
           -- event_id is the exact linkage when the event-side UUID is NULL.
           AND (a.correlation_id IS NOT DISTINCT FROM e.correlation_id::text
                OR (e.correlation_id IS NULL AND a.correlation_id IS NOT NULL
                    AND NOT (a.correlation_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$')))
           AND a.action = e.event_type
           AND a.metadata = e.payload)
       INTO v_ok;
    IF NOT v_ok THEN
        RAISE EXCEPTION 'authoring dashboard mutation requires linked canonical event and audit evidence';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS authoring_command_evidence_guard ON dashboard.authoring_commands;
DROP TRIGGER IF EXISTS authoring_create_evidence_guard ON dashboard.authoring_create_operations;
DROP TRIGGER IF EXISTS authoring_dashboard_evidence_guard ON dashboard.authoring_dashboards;
CREATE CONSTRAINT TRIGGER authoring_dashboard_evidence_guard
    AFTER INSERT OR UPDATE OF last_event_id ON dashboard.authoring_dashboards
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION dashboard.guard_authoring_dashboard_evidence();

-- Attach cross-capability authority relationships when their baseline tables
-- are present. Access owns the opaque UUID principal identity.
-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('project.project_identity') IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'authoring_dashboards_project_fk') THEN
        ALTER TABLE dashboard.authoring_dashboards
            ADD CONSTRAINT authoring_dashboards_project_fk FOREIGN KEY (project_id)
            REFERENCES project.project_identity(project_id) ON DELETE RESTRICT;
    END IF;
    IF to_regclass('access.principal') IS NOT NULL THEN
        IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'authoring_dashboards_owner_fk') THEN
            ALTER TABLE dashboard.authoring_dashboards
                ADD CONSTRAINT authoring_dashboards_owner_fk FOREIGN KEY (owner_principal_id)
                REFERENCES access.principal(id) ON DELETE RESTRICT;
        END IF;
    END IF;
END $$;
-- +goose StatementEnd

REVOKE ALL ON SCHEMA dashboard FROM PUBLIC;
REVOKE ALL ON TABLE dashboard.authoring_dashboards, dashboard.authoring_revisions, dashboard.authoring_drafts, dashboard.authoring_compiled_revisions, dashboard.authoring_published, dashboard.authoring_commands, dashboard.authoring_create_operations, dashboard.authoring_revalidation_attempts FROM PUBLIC;
REVOKE ALL ON FUNCTION dashboard.guard_authoring_dashboard_update() FROM PUBLIC;
REVOKE ALL ON FUNCTION dashboard.guard_authoring_draft_update() FROM PUBLIC;
REVOKE ALL ON FUNCTION dashboard.guard_authoring_published_update() FROM PUBLIC;
REVOKE ALL ON FUNCTION dashboard.authoring_create_dashboard(text,text,uuid,text,text,text,text,text,uuid,bigint,jsonb,text,jsonb,timestamptz,uuid,jsonb,boolean,text,text,text,text,text,text,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION dashboard.authoring_append_draft(text,text,text,text,text,text,text,uuid,bigint,jsonb,text,jsonb,timestamptz,jsonb,uuid,bigint,text,uuid,text,text,jsonb,timestamptz,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION dashboard.authoring_publish_dashboard(text,text,text,text,text,text,text,uuid,bigint,text,jsonb,text,text,jsonb,timestamptz,jsonb,timestamptz,uuid,text,text,jsonb,timestamptz,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION dashboard.authoring_archive_dashboard(text,text,uuid,bigint,text,uuid,text,text,jsonb,timestamptz,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION dashboard.authoring_commit_revalidation(text,text,uuid,bigint,text,jsonb,text,text,jsonb,timestamptz,text,uuid,jsonb,text,jsonb,uuid,bigint,text,jsonb,timestamptz,uuid,bigint,text,text,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION dashboard.authoring_record_revalidation_failure(text,text,text,uuid,jsonb,text,jsonb,uuid,bigint,text,jsonb,text,text,timestamptz) FROM PUBLIC;
REVOKE ALL ON FUNCTION dashboard.guard_authoring_dashboard_evidence() FROM PUBLIC;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_owner') THEN
 GRANT USAGE ON SCHEMA dashboard TO leapview_control_owner;
  GRANT ALL ON TABLE dashboard.authoring_dashboards, dashboard.authoring_revisions, dashboard.authoring_drafts, dashboard.authoring_compiled_revisions, dashboard.authoring_published, dashboard.authoring_commands, dashboard.authoring_create_operations, dashboard.authoring_revalidation_attempts TO leapview_control_owner;
  GRANT ALL ON FUNCTION dashboard.guard_authoring_dashboard_update(), dashboard.guard_authoring_draft_update(), dashboard.guard_authoring_published_update(), dashboard.guard_authoring_dashboard_evidence(), dashboard.authoring_create_dashboard(text,text,uuid,text,text,text,text,text,uuid,bigint,jsonb,text,jsonb,timestamptz,uuid,jsonb,boolean,text,text,text,text,text,text,uuid), dashboard.authoring_append_draft(text,text,text,text,text,text,text,uuid,bigint,jsonb,text,jsonb,timestamptz,jsonb,uuid,bigint,text,uuid,text,text,jsonb,timestamptz,uuid), dashboard.authoring_publish_dashboard(text,text,text,text,text,text,text,uuid,bigint,text,jsonb,text,text,jsonb,timestamptz,jsonb,timestamptz,uuid,text,text,jsonb,timestamptz,uuid), dashboard.authoring_archive_dashboard(text,text,uuid,bigint,text,uuid,text,text,jsonb,timestamptz,uuid), dashboard.authoring_commit_revalidation(text,text,uuid,bigint,text,jsonb,text,text,jsonb,timestamptz,text,uuid,jsonb,text,jsonb,uuid,bigint,text,jsonb,timestamptz,uuid,bigint,text,text,text), dashboard.authoring_record_revalidation_failure(text,text,text,uuid,jsonb,text,jsonb,uuid,bigint,text,jsonb,text,text,timestamptz) TO leapview_control_owner;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_migrator') THEN
 GRANT USAGE ON SCHEMA dashboard TO leapview_control_migrator;
  GRANT ALL ON TABLE dashboard.authoring_dashboards, dashboard.authoring_revisions, dashboard.authoring_drafts, dashboard.authoring_compiled_revisions, dashboard.authoring_published, dashboard.authoring_commands, dashboard.authoring_create_operations, dashboard.authoring_revalidation_attempts TO leapview_control_migrator;
  GRANT ALL ON FUNCTION dashboard.guard_authoring_dashboard_update(), dashboard.guard_authoring_draft_update(), dashboard.guard_authoring_published_update(), dashboard.guard_authoring_dashboard_evidence(), dashboard.authoring_create_dashboard(text,text,uuid,text,text,text,text,text,uuid,bigint,jsonb,text,jsonb,timestamptz,uuid,jsonb,boolean,text,text,text,text,text,text,uuid), dashboard.authoring_append_draft(text,text,text,text,text,text,text,uuid,bigint,jsonb,text,jsonb,timestamptz,jsonb,uuid,bigint,text,uuid,text,text,jsonb,timestamptz,uuid), dashboard.authoring_publish_dashboard(text,text,text,text,text,text,text,uuid,bigint,text,jsonb,text,text,jsonb,timestamptz,jsonb,timestamptz,uuid,text,text,jsonb,timestamptz,uuid), dashboard.authoring_archive_dashboard(text,text,uuid,bigint,text,uuid,text,text,jsonb,timestamptz,uuid), dashboard.authoring_commit_revalidation(text,text,uuid,bigint,text,jsonb,text,text,jsonb,timestamptz,text,uuid,jsonb,text,jsonb,uuid,bigint,text,jsonb,timestamptz,uuid,bigint,text,text,text), dashboard.authoring_record_revalidation_failure(text,text,text,uuid,jsonb,text,jsonb,uuid,bigint,text,jsonb,text,text,timestamptz) TO leapview_control_migrator;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_runtime') THEN
 GRANT USAGE ON SCHEMA dashboard TO leapview_control_runtime;
  GRANT SELECT ON dashboard.authoring_dashboards,dashboard.authoring_revisions,dashboard.authoring_drafts,dashboard.authoring_compiled_revisions,dashboard.authoring_published,dashboard.authoring_commands,dashboard.authoring_create_operations,dashboard.authoring_revalidation_attempts TO leapview_control_runtime;
  REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON dashboard.authoring_dashboards,dashboard.authoring_revisions,dashboard.authoring_drafts,dashboard.authoring_compiled_revisions,dashboard.authoring_published,dashboard.authoring_commands,dashboard.authoring_create_operations,dashboard.authoring_revalidation_attempts FROM leapview_control_runtime;
  GRANT EXECUTE ON FUNCTION dashboard.authoring_create_dashboard(text,text,uuid,text,text,text,text,text,uuid,bigint,jsonb,text,jsonb,timestamptz,uuid,jsonb,boolean,text,text,text,text,text,text,uuid) TO leapview_control_runtime;
  GRANT EXECUTE ON FUNCTION dashboard.authoring_append_draft(text,text,text,text,text,text,text,uuid,bigint,jsonb,text,jsonb,timestamptz,jsonb,uuid,bigint,text,uuid,text,text,jsonb,timestamptz,uuid) TO leapview_control_runtime;
  GRANT EXECUTE ON FUNCTION dashboard.authoring_publish_dashboard(text,text,text,text,text,text,text,uuid,bigint,text,jsonb,text,text,jsonb,timestamptz,jsonb,timestamptz,uuid,text,text,jsonb,timestamptz,uuid) TO leapview_control_runtime;
  GRANT EXECUTE ON FUNCTION dashboard.authoring_archive_dashboard(text,text,uuid,bigint,text,uuid,text, text,jsonb,timestamptz,uuid) TO leapview_control_runtime;
  GRANT EXECUTE ON FUNCTION dashboard.authoring_commit_revalidation(text,text,uuid,bigint,text,jsonb,text,text,jsonb,timestamptz,text,uuid,jsonb,text,jsonb,uuid,bigint,text,jsonb,timestamptz,uuid,bigint,text,text,text) TO leapview_control_runtime;
  GRANT EXECUTE ON FUNCTION dashboard.authoring_record_revalidation_failure(text,text,text,uuid,jsonb,text,jsonb,uuid,bigint,text,jsonb,text,text,timestamptz) TO leapview_control_runtime;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_readonly') THEN
  GRANT USAGE ON SCHEMA dashboard TO leapview_control_readonly;
  GRANT SELECT ON dashboard.authoring_dashboards,dashboard.authoring_revisions,dashboard.authoring_drafts,dashboard.authoring_compiled_revisions,dashboard.authoring_published,dashboard.authoring_commands,dashboard.authoring_create_operations,dashboard.authoring_revalidation_attempts TO leapview_control_readonly;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_backup') THEN
  GRANT USAGE ON SCHEMA dashboard TO leapview_control_backup;
  GRANT SELECT ON dashboard.authoring_dashboards,dashboard.authoring_revisions,dashboard.authoring_drafts,dashboard.authoring_compiled_revisions,dashboard.authoring_published,dashboard.authoring_commands,dashboard.authoring_create_operations,dashboard.authoring_revalidation_attempts TO leapview_control_backup;
 END IF;
END $$;
-- +goose StatementEnd

-- capability source: internal/dashboard/publication/postgres/schema.sql
-- Native PostgreSQL dashboard publication and public stream authority.
CREATE SCHEMA IF NOT EXISTS dashboard;

CREATE TABLE IF NOT EXISTS dashboard.publications (
 id uuid PRIMARY KEY,
 project_id text NOT NULL,
 name text NOT NULL,
 public_id text NOT NULL UNIQUE,
 dashboard text NOT NULL,
 default_page text NOT NULL,
 configuration_digest text NOT NULL CHECK (configuration_digest ~ '^sha256:[0-9a-f]{64}$'),
 allowed_origins_json jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(allowed_origins_json) = 'array' AND octet_length(allowed_origins_json::text) <= 32768),
 dependency_asset_ids_json jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(dependency_asset_ids_json) = 'array' AND octet_length(dependency_asset_ids_json::text) <= 32768),
 revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
 configured boolean NOT NULL DEFAULT true,
 active_serving_state_id text,
 suspended_at timestamptz,
 suspended_by text NOT NULL DEFAULT '',
 configured_at timestamptz,
 disabled_at timestamptz,
 rotated_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(project_id,name),
 CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255),
 CHECK (name = btrim(name) AND octet_length(name) BETWEEN 1 AND 255),
 CHECK (public_id = btrim(public_id) AND octet_length(public_id) BETWEEN 1 AND 255),
 CHECK (dashboard = btrim(dashboard) AND octet_length(dashboard) BETWEEN 1 AND 512),
 CHECK (default_page = btrim(default_page) AND octet_length(default_page) BETWEEN 1 AND 255),
 CHECK (suspended_by = btrim(suspended_by) AND octet_length(suspended_by) <= 255),
 CHECK (active_serving_state_id IS NULL OR (active_serving_state_id = btrim(active_serving_state_id) AND octet_length(active_serving_state_id) BETWEEN 1 AND 255)),
 CHECK (updated_at >= created_at),
 CHECK ((configured AND active_serving_state_id IS NOT NULL AND configured_at IS NOT NULL AND disabled_at IS NULL)
     OR (NOT configured AND active_serving_state_id IS NULL AND disabled_at IS NOT NULL)),
 CHECK ((suspended_at IS NULL AND suspended_by = '') OR (suspended_at IS NOT NULL AND suspended_by <> ''))
);

CREATE TABLE IF NOT EXISTS dashboard.publication_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 publication_id uuid NOT NULL REFERENCES dashboard.publications(id) ON DELETE RESTRICT,
 domain_event_id uuid NOT NULL,
 aggregate_version bigint NOT NULL CHECK (aggregate_version > 0),
 revision bigint NOT NULL CHECK (revision > 0),
 event_type text NOT NULL CHECK (event_type IN ('dashboard_publication.configured','dashboard_publication.configuration_changed','dashboard_publication.serving_state_changed','dashboard_publication.disabled','dashboard_publication.suspended','dashboard_publication.resumed','dashboard_publication.rotated')),
 actor_id text NOT NULL DEFAULT '' CHECK (actor_id = btrim(actor_id) AND octet_length(actor_id) <= 255),
 correlation_id text NOT NULL DEFAULT '' CHECK (correlation_id = btrim(correlation_id) AND octet_length(correlation_id) <= 255),
 serving_state_id text CHECK (serving_state_id IS NULL OR (serving_state_id = btrim(serving_state_id) AND octet_length(serving_state_id) BETWEEN 1 AND 255)),
 payload_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload_json) = 'object' AND octet_length(payload_json::text) <= 65536),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE (domain_event_id),
 UNIQUE (publication_id, aggregate_version),
 UNIQUE (publication_id, revision)
);
CREATE INDEX IF NOT EXISTS publication_events_publication_idx ON dashboard.publication_events(publication_id,id DESC);
CREATE INDEX IF NOT EXISTS publication_events_publication_revision_idx ON dashboard.publication_events(publication_id,revision DESC);

CREATE TABLE IF NOT EXISTS dashboard.publication_streams (
 publication_id uuid NOT NULL,
 stream_id text NOT NULL,
 public_id text NOT NULL,
 serving_state_id text NOT NULL,
 registration_id uuid NOT NULL,
 filters_json jsonb NOT NULL CHECK (jsonb_typeof(filters_json) = 'object' AND octet_length(filters_json::text) <= 32768),
 generation bigint NOT NULL DEFAULT 1 CHECK (generation > 0),
 expires_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(publication_id,stream_id),
 FOREIGN KEY (publication_id) REFERENCES dashboard.publications(id) ON DELETE RESTRICT,
 CHECK (stream_id = btrim(stream_id) AND octet_length(stream_id) BETWEEN 1 AND 255),
 CHECK (public_id = btrim(public_id) AND octet_length(public_id) BETWEEN 1 AND 255),
 CHECK (serving_state_id = btrim(serving_state_id) AND octet_length(serving_state_id) BETWEEN 1 AND 255)
);

-- Publication identity is stable for the lifetime of a row.  Every
-- projection mutation advances its optimistic revision exactly once; public
-- identifiers may only rotate together with their rotation timestamp.
CREATE OR REPLACE FUNCTION dashboard.guard_publication_update()
RETURNS trigger
LANGUAGE plpgsql
-- +goose StatementBegin
AS $$
BEGIN
    IF OLD.id IS DISTINCT FROM NEW.id
       OR OLD.project_id IS DISTINCT FROM NEW.project_id
       OR OLD.name IS DISTINCT FROM NEW.name
       OR OLD.created_at IS DISTINCT FROM NEW.created_at THEN
        RAISE EXCEPTION 'publication identity is immutable';
    END IF;
    IF NEW.revision <> OLD.revision + 1 THEN
        RAISE EXCEPTION 'publication revision must advance exactly one';
    END IF;
    IF OLD.public_id IS DISTINCT FROM NEW.public_id
       AND OLD.rotated_at IS NOT DISTINCT FROM NEW.rotated_at THEN
        RAISE EXCEPTION 'publication public_id changes require rotated_at change';
    END IF;
    IF OLD.rotated_at IS DISTINCT FROM NEW.rotated_at
       AND OLD.public_id IS NOT DISTINCT FROM NEW.public_id THEN
        RAISE EXCEPTION 'publication rotated_at changes require public_id change';
    END IF;
    IF NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'publication updated_at is monotonic';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION dashboard.guard_publication_stream_update()
RETURNS trigger
LANGUAGE plpgsql
-- +goose StatementBegin
AS $$
BEGIN
    IF OLD.publication_id IS DISTINCT FROM NEW.publication_id
       OR OLD.stream_id IS DISTINCT FROM NEW.stream_id THEN
        RAISE EXCEPTION 'publication stream primary key is immutable';
    END IF;
    IF NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'publication stream updated_at is monotonic';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS publications_mutation ON dashboard.publications;
CREATE TRIGGER publications_mutation
    BEFORE UPDATE ON dashboard.publications
    FOR EACH ROW EXECUTE FUNCTION dashboard.guard_publication_update();
DROP TRIGGER IF EXISTS publication_streams_mutation ON dashboard.publication_streams;
CREATE TRIGGER publication_streams_mutation
    BEFORE UPDATE ON dashboard.publication_streams
    FOR EACH ROW EXECUTE FUNCTION dashboard.guard_publication_stream_update();

-- Application runtime connections do not receive projection-table DML.  All
-- publication mutations enter through the owner-executed boundary below.
-- The boundary validates the canonical event and audit rows already appended
-- by the source transaction, then applies the CAS and projection event in one
-- statement-level transaction.  The event and audit schemas are baseline
-- prerequisites for invoking audited publication mutation functions.
CREATE OR REPLACE FUNCTION dashboard.check_publication_evidence(
    p_publication_id uuid,
    p_project_id text,
    p_name text,
    p_actor_id text,
    p_operation text,
    p_resource_kind text,
    p_resource_id text,
    p_domain_event_id uuid,
    p_aggregate_version bigint,
    p_event_type text,
    p_correlation_id text,
    p_payload jsonb,
    p_audit_metadata jsonb
) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, dashboard, event, audit
-- +goose StatementBegin
AS $$
DECLARE ok boolean;
BEGIN
    IF p_domain_event_id IS NULL OR p_aggregate_version IS NULL OR p_aggregate_version < 1
       OR NULLIF(btrim(p_operation), '') IS NULL
       OR NULLIF(btrim(p_resource_kind), '') IS NULL
       OR NULLIF(btrim(p_resource_id), '') IS NULL
       OR p_event_type IS NULL OR p_payload IS NULL OR p_audit_metadata IS NULL THEN
        RAISE EXCEPTION 'publication mutation evidence is required';
    END IF;
    SELECT EXISTS (
        SELECT 1 FROM event.event_log e
         WHERE e.event_id = p_domain_event_id
           AND e.scope_id = p_project_id
           AND e.aggregate_type = 'dashboard_publication'
           AND e.aggregate_id = p_publication_id::text
           AND e.aggregate_version = p_aggregate_version
           AND e.event_type = p_event_type
           AND e.schema_version = 1
           AND e.correlation_id IS NOT DISTINCT FROM NULLIF(p_correlation_id, '')::uuid
           AND e.payload = p_payload
    ) INTO ok;
    IF NOT ok THEN
        RAISE EXCEPTION 'publication mutation canonical domain event is missing or mismatched';
    END IF;
    SELECT EXISTS (
        SELECT 1 FROM audit.audit_event a
         WHERE a.event_id = p_domain_event_id
           AND a.scope_id = p_project_id
           AND a.actor_id = p_actor_id
           AND a.operation = p_operation
           AND a.source = 'dashboard.publication'
           AND a.action = p_event_type
           AND a.resource_kind = p_resource_kind
           AND a.resource_id = p_resource_id
           AND a.capability = 'RESOURCE_PUBLISH'
           AND a.outcome = 'success'
           AND a.correlation_id IS NOT DISTINCT FROM NULLIF(p_correlation_id, '')
           AND a.aggregate_key = 'dashboard_publication:' || p_project_id || ':' || p_name
           AND a.aggregate_sequence = p_aggregate_version
           AND a.metadata = p_audit_metadata
    ) INTO ok;
    IF NOT ok THEN
        RAISE EXCEPTION 'publication mutation audit evidence is missing or mismatched';
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION dashboard.record_publication_event(
    p_publication_id uuid,
    p_domain_event_id uuid,
    p_aggregate_version bigint,
    p_revision bigint,
    p_event_type text,
    p_actor_id text,
    p_correlation_id text,
    p_serving_state_id text,
    p_payload jsonb
) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, dashboard, event, audit
-- +goose StatementBegin
AS $$
DECLARE existing dashboard.publication_events%ROWTYPE;
BEGIN
    IF p_publication_id IS NULL OR p_domain_event_id IS NULL OR p_aggregate_version < 1 OR p_revision < 1
       OR p_event_type IS NULL OR p_payload IS NULL THEN
        RAISE EXCEPTION 'publication event identity is required';
    END IF;
    SELECT * INTO existing FROM dashboard.publication_events
     WHERE domain_event_id = p_domain_event_id;
    IF FOUND THEN
        IF existing.publication_id <> p_publication_id
           OR existing.aggregate_version <> p_aggregate_version
           OR existing.revision <> p_revision
           OR existing.event_type <> p_event_type
           OR existing.actor_id <> p_actor_id
           OR existing.correlation_id <> p_correlation_id
           OR existing.serving_state_id IS DISTINCT FROM NULLIF(p_serving_state_id, '')
           OR existing.payload_json <> p_payload THEN
            RAISE EXCEPTION 'publication event replay differs';
        END IF;
        RETURN;
    END IF;
    INSERT INTO dashboard.publication_events(
        publication_id, domain_event_id, aggregate_version, revision,
        event_type, actor_id, correlation_id, serving_state_id, payload_json
    ) VALUES (
        p_publication_id, p_domain_event_id, p_aggregate_version, p_revision,
        p_event_type, p_actor_id, p_correlation_id,
        NULLIF(p_serving_state_id, ''), p_payload
    );
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION dashboard.publication_payload(p_publication_id uuid, p_event_type text)
RETURNS jsonb
LANGUAGE sql SECURITY DEFINER
SET search_path = pg_catalog, dashboard
-- +goose StatementBegin
AS $$
    SELECT jsonb_build_object(
        'eventType', p_event_type,
        'publicationId', p.id::text,
        'projectId', p.project_id,
        'name', p.name,
        'publicId', p.public_id,
        'dashboard', p.dashboard,
        'defaultPage', p.default_page,
        'configurationDigest', p.configuration_digest,
        'allowedOrigins', p.allowed_origins_json,
        'dependencyAssetIds', p.dependency_asset_ids_json,
        'revision', p.revision,
        'configured', p.configured,
        'servingStateId', COALESCE(p.active_serving_state_id, '')
    ) FROM dashboard.publications p WHERE p.id = p_publication_id
$$;
-- +goose StatementEnd

-- p_operation is deliberately allow-listed.  The wrappers below expose only
-- narrow typed entrypoints to the runtime role; this implementation is kept
-- private to the owner/migrator capabilities.
CREATE OR REPLACE FUNCTION dashboard.mutate_publication(
    p_operation text,
    p_id uuid,
    p_project_id text,
    p_name text,
    p_public_id text,
    p_dashboard text,
    p_default_page text,
    p_configuration_digest text,
    p_allowed_origins_json jsonb,
    p_dependency_asset_ids_json jsonb,
    p_active_serving_state_id text,
    p_expected_revision bigint,
    p_actor_id text,
    p_domain_event_id uuid,
    p_aggregate_version bigint,
    p_event_type text,
    p_correlation_id text,
    p_payload jsonb,
    p_audit_operation text,
    p_audit_resource_kind text,
    p_audit_resource_id text,
    p_audit_metadata jsonb
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, dashboard, event, audit
-- +goose StatementBegin
AS $$
DECLARE
    v_row dashboard.publications%ROWTYPE;
    v_now timestamptz := clock_timestamp();
    v_project text := p_project_id;
    v_name text := p_name;
    v_id uuid := p_id;
    v_changed bigint;
    v_expected_payload jsonb;
BEGIN
    IF p_operation NOT IN ('create','suspend','resume','rotate','disable','update_configuration') THEN
        RAISE EXCEPTION 'unsupported publication mutation';
    END IF;
    IF (p_operation = 'create' AND p_event_type <> 'dashboard_publication.configured')
       OR (p_operation = 'suspend' AND p_event_type <> 'dashboard_publication.suspended')
       OR (p_operation = 'resume' AND p_event_type <> 'dashboard_publication.resumed')
       OR (p_operation = 'rotate' AND p_event_type <> 'dashboard_publication.rotated')
       OR (p_operation = 'disable' AND p_event_type <> 'dashboard_publication.disabled')
       OR (p_operation = 'update_configuration' AND p_event_type NOT IN ('dashboard_publication.configured','dashboard_publication.configuration_changed','dashboard_publication.serving_state_changed')) THEN
        RAISE EXCEPTION 'publication mutation event type does not match operation';
    END IF;
    IF p_operation <> 'create' AND (p_expected_revision IS NULL OR p_expected_revision < 1) THEN
        RAISE EXCEPTION 'publication expected revision must be positive';
    ELSIF p_operation <> 'create' AND p_expected_revision = 9223372036854775807 THEN
        RAISE EXCEPTION 'publication revision is exhausted';
    END IF;
    IF p_operation = 'create' THEN
        IF EXISTS (SELECT 1 FROM dashboard.publications WHERE id = p_id) THEN
            SELECT * INTO v_row FROM dashboard.publications WHERE id = p_id FOR UPDATE;
            IF v_row.revision = 1 AND EXISTS (SELECT 1 FROM dashboard.publication_events WHERE publication_id = p_id AND domain_event_id = p_domain_event_id) THEN
                v_expected_payload := dashboard.publication_payload(p_id, p_event_type);
                IF v_expected_payload IS DISTINCT FROM p_payload THEN RAISE EXCEPTION 'publication event payload does not describe projection'; END IF;
                PERFORM dashboard.check_publication_evidence(p_id, p_project_id, p_name, p_actor_id, p_audit_operation, p_audit_resource_kind, p_audit_resource_id, p_domain_event_id, p_aggregate_version, p_event_type, p_correlation_id, v_expected_payload, p_audit_metadata);
                PERFORM dashboard.record_publication_event(p_id, p_domain_event_id, p_aggregate_version, 1, p_event_type, p_actor_id, p_correlation_id, p_active_serving_state_id, p_payload);
                RETURN 1;
            END IF;
            RAISE EXCEPTION 'publication identity already exists';
        END IF;
        INSERT INTO dashboard.publications(
            id, project_id, name, public_id, dashboard, default_page,
            configuration_digest, allowed_origins_json, dependency_asset_ids_json,
            revision, configured, active_serving_state_id, configured_at
        ) VALUES (
            p_id, p_project_id, p_name, p_public_id, p_dashboard, p_default_page,
            p_configuration_digest, p_allowed_origins_json, p_dependency_asset_ids_json,
            1, true, NULLIF(p_active_serving_state_id, ''), v_now
        );
        v_expected_payload := dashboard.publication_payload(p_id, p_event_type);
        IF v_expected_payload IS DISTINCT FROM p_payload THEN RAISE EXCEPTION 'publication event payload does not describe projection'; END IF;
        PERFORM dashboard.check_publication_evidence(p_id, p_project_id, p_name, p_actor_id, p_audit_operation, p_audit_resource_kind, p_audit_resource_id, p_domain_event_id, p_aggregate_version, p_event_type, p_correlation_id, v_expected_payload, p_audit_metadata);
        PERFORM dashboard.record_publication_event(p_id, p_domain_event_id, p_aggregate_version, 1, p_event_type, p_actor_id, p_correlation_id, p_active_serving_state_id, p_payload);
        RETURN 1;
    END IF;

    IF p_operation = 'disable' THEN
        SELECT * INTO v_row FROM dashboard.publications WHERE id = p_id FOR UPDATE;
    ELSE
        SELECT * INTO v_row FROM dashboard.publications
         WHERE (p_id IS NOT NULL AND id = p_id)
            OR (p_id IS NULL AND project_id = p_project_id AND name = p_name)
         ORDER BY configured DESC LIMIT 1 FOR UPDATE;
    END IF;
    IF NOT FOUND THEN
        RETURN 0;
    END IF;
    v_project := v_row.project_id;
    v_name := v_row.name;
    v_id := v_row.id;
    -- A replay with the same domain-event identity is accepted only when the
    -- projection event is already complete at the expected next revision.
    IF v_row.revision = p_expected_revision + 1
       AND EXISTS (SELECT 1 FROM dashboard.publication_events WHERE publication_id = v_id AND domain_event_id = p_domain_event_id) THEN
        v_expected_payload := dashboard.publication_payload(v_id, p_event_type);
        IF v_expected_payload IS DISTINCT FROM p_payload THEN RAISE EXCEPTION 'publication event payload does not describe projection'; END IF;
        PERFORM dashboard.check_publication_evidence(v_id, v_project, v_name, p_actor_id, p_audit_operation, p_audit_resource_kind, p_audit_resource_id, p_domain_event_id, p_aggregate_version, p_event_type, p_correlation_id, v_expected_payload, p_audit_metadata);
        PERFORM dashboard.record_publication_event(v_id, p_domain_event_id, p_aggregate_version, v_row.revision, p_event_type, p_actor_id, p_correlation_id, COALESCE(v_row.active_serving_state_id,''), p_payload);
        RETURN 1;
    END IF;
    IF v_row.revision <> p_expected_revision
       OR (p_operation IN ('suspend','resume','rotate') AND NOT v_row.configured) THEN
        RETURN 0;
    END IF;

    IF p_operation = 'suspend' THEN
        UPDATE dashboard.publications SET revision = revision + 1,
            suspended_at = COALESCE(suspended_at, v_now), suspended_by = p_actor_id,
            updated_at = v_now WHERE id = v_id;
    ELSIF p_operation = 'resume' THEN
        UPDATE dashboard.publications SET revision = revision + 1,
            suspended_at = NULL, suspended_by = '', updated_at = v_now WHERE id = v_id;
    ELSIF p_operation = 'rotate' THEN
        UPDATE dashboard.publications SET revision = revision + 1,
            public_id = p_public_id, rotated_at = v_now, updated_at = v_now WHERE id = v_id;
    ELSIF p_operation = 'disable' THEN
        UPDATE dashboard.publications SET revision = revision + 1,
            configured = false, active_serving_state_id = NULL,
            disabled_at = v_now, updated_at = v_now WHERE id = v_id;
    ELSIF p_operation = 'update_configuration' THEN
        UPDATE dashboard.publications SET revision = revision + 1,
            dashboard = p_dashboard, default_page = p_default_page,
            configuration_digest = p_configuration_digest,
            allowed_origins_json = p_allowed_origins_json,
            dependency_asset_ids_json = p_dependency_asset_ids_json,
            configured = true, active_serving_state_id = NULLIF(p_active_serving_state_id, ''),
            configured_at = COALESCE(configured_at, v_now), disabled_at = NULL,
            updated_at = v_now WHERE id = v_id;
    END IF;
    GET DIAGNOSTICS v_changed = ROW_COUNT;
    IF v_changed <> 1 THEN
        RETURN 0;
    END IF;
    v_expected_payload := dashboard.publication_payload(v_id, p_event_type);
    IF v_expected_payload IS DISTINCT FROM p_payload THEN RAISE EXCEPTION 'publication event payload does not describe projection'; END IF;
    PERFORM dashboard.check_publication_evidence(v_id, v_project, v_name, p_actor_id, p_audit_operation, p_audit_resource_kind, p_audit_resource_id, p_domain_event_id, p_aggregate_version, p_event_type, p_correlation_id, v_expected_payload, p_audit_metadata);
    PERFORM dashboard.record_publication_event(v_id, p_domain_event_id, p_aggregate_version, p_expected_revision + 1, p_event_type, p_actor_id, p_correlation_id, COALESCE(p_active_serving_state_id, (SELECT active_serving_state_id FROM dashboard.publications WHERE id=v_id), ''), p_payload);
    RETURN 1;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION dashboard.suspend_publication(p_project_id text, p_name text, p_actor_id text, p_expected_revision bigint, p_domain_event_id uuid, p_aggregate_version bigint, p_correlation_id text, p_payload jsonb, p_audit_operation text, p_audit_resource_kind text, p_audit_resource_id text, p_audit_metadata jsonb)
-- +goose StatementBegin
RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, dashboard, event, audit AS $$
BEGIN RETURN dashboard.mutate_publication('suspend', NULL, p_project_id, p_name, '', '', '', '', '{}'::jsonb, '{}'::jsonb, '', p_expected_revision, p_actor_id, p_domain_event_id, p_aggregate_version, 'dashboard_publication.suspended', p_correlation_id, p_payload, p_audit_operation, p_audit_resource_kind, p_audit_resource_id, p_audit_metadata); END $$;
-- +goose StatementEnd
CREATE OR REPLACE FUNCTION dashboard.resume_publication(p_project_id text, p_name text, p_actor_id text, p_expected_revision bigint, p_domain_event_id uuid, p_aggregate_version bigint, p_correlation_id text, p_payload jsonb, p_audit_operation text, p_audit_resource_kind text, p_audit_resource_id text, p_audit_metadata jsonb)
-- +goose StatementBegin
RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, dashboard, event, audit AS $$
BEGIN RETURN dashboard.mutate_publication('resume', NULL, p_project_id, p_name, '', '', '', '', '{}'::jsonb, '{}'::jsonb, '', p_expected_revision, p_actor_id, p_domain_event_id, p_aggregate_version, 'dashboard_publication.resumed', p_correlation_id, p_payload, p_audit_operation, p_audit_resource_kind, p_audit_resource_id, p_audit_metadata); END $$;
-- +goose StatementEnd
CREATE OR REPLACE FUNCTION dashboard.rotate_publication(p_project_id text, p_name text, p_actor_id text, p_expected_revision bigint, p_public_id text, p_domain_event_id uuid, p_aggregate_version bigint, p_correlation_id text, p_payload jsonb, p_audit_operation text, p_audit_resource_kind text, p_audit_resource_id text, p_audit_metadata jsonb)
-- +goose StatementBegin
RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, dashboard, event, audit AS $$
BEGIN RETURN dashboard.mutate_publication('rotate', NULL, p_project_id, p_name, p_public_id, '', '', '', '{}'::jsonb, '{}'::jsonb, '', p_expected_revision, p_actor_id, p_domain_event_id, p_aggregate_version, 'dashboard_publication.rotated', p_correlation_id, p_payload, p_audit_operation, p_audit_resource_kind, p_audit_resource_id, p_audit_metadata); END $$;
-- +goose StatementEnd
CREATE OR REPLACE FUNCTION dashboard.disable_publication(p_id uuid, p_expected_revision bigint, p_actor_id text, p_domain_event_id uuid, p_aggregate_version bigint, p_correlation_id text, p_payload jsonb, p_audit_operation text, p_audit_resource_kind text, p_audit_resource_id text, p_audit_metadata jsonb)
-- +goose StatementBegin
RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, dashboard, event, audit AS $$
DECLARE v_project text; v_name text;
BEGIN SELECT project_id,name INTO v_project,v_name FROM dashboard.publications WHERE id=p_id; RETURN dashboard.mutate_publication('disable', p_id, v_project, v_name, '', '', '', '', '{}'::jsonb, '{}'::jsonb, '', p_expected_revision, p_actor_id, p_domain_event_id, p_aggregate_version, 'dashboard_publication.disabled', p_correlation_id, p_payload, p_audit_operation, p_audit_resource_kind, p_audit_resource_id, p_audit_metadata); END $$;
-- +goose StatementEnd
CREATE OR REPLACE FUNCTION dashboard.update_publication_configuration(p_id uuid, p_dashboard text, p_default_page text, p_configuration_digest text, p_allowed_origins_json jsonb, p_dependency_asset_ids_json jsonb, p_active_serving_state_id text, p_expected_revision bigint, p_actor_id text, p_domain_event_id uuid, p_aggregate_version bigint, p_event_type text, p_correlation_id text, p_payload jsonb, p_audit_operation text, p_audit_resource_kind text, p_audit_resource_id text, p_audit_metadata jsonb)
-- +goose StatementBegin
RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, dashboard, event, audit AS $$
DECLARE v_project text; v_name text;
BEGIN SELECT project_id,name INTO v_project,v_name FROM dashboard.publications WHERE id=p_id; RETURN dashboard.mutate_publication('update_configuration', p_id, v_project, v_name, '', p_dashboard, p_default_page, p_configuration_digest, p_allowed_origins_json, p_dependency_asset_ids_json, p_active_serving_state_id, p_expected_revision, p_actor_id, p_domain_event_id, p_aggregate_version, p_event_type, p_correlation_id, p_payload, p_audit_operation, p_audit_resource_kind, p_audit_resource_id, p_audit_metadata); END $$;
-- +goose StatementEnd
CREATE OR REPLACE FUNCTION dashboard.create_publication(p_id uuid, p_project_id text, p_name text, p_public_id text, p_dashboard text, p_default_page text, p_configuration_digest text, p_allowed_origins_json jsonb, p_dependency_asset_ids_json jsonb, p_active_serving_state_id text, p_actor_id text, p_domain_event_id uuid, p_aggregate_version bigint, p_correlation_id text, p_payload jsonb, p_audit_operation text, p_audit_resource_kind text, p_audit_resource_id text, p_audit_metadata jsonb)
-- +goose StatementBegin
RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, dashboard, event, audit AS $$
BEGIN RETURN dashboard.mutate_publication('create', p_id, p_project_id, p_name, p_public_id, p_dashboard, p_default_page, p_configuration_digest, p_allowed_origins_json, p_dependency_asset_ids_json, p_active_serving_state_id, 0, p_actor_id, p_domain_event_id, p_aggregate_version, 'dashboard_publication.configured', p_correlation_id, p_payload, p_audit_operation, p_audit_resource_kind, p_audit_resource_id, p_audit_metadata); END $$;
-- +goose StatementEnd

-- Stream liveness is operational state, not a user-visible domain mutation.
CREATE OR REPLACE FUNCTION dashboard.upsert_publication_stream(p_publication_id uuid, p_stream_id text, p_public_id text, p_serving_state_id text, p_registration_id uuid, p_filters_json jsonb, p_expires_at timestamptz)
-- +goose StatementBegin
RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, dashboard AS $$
BEGIN
 IF p_expires_at IS NULL OR p_expires_at <= clock_timestamp() OR p_expires_at > clock_timestamp()+interval '24 hours' THEN RAISE EXCEPTION 'publication stream expiry is outside the database-clock window'; END IF;
 INSERT INTO dashboard.publication_streams(publication_id,stream_id,public_id,serving_state_id,registration_id,filters_json,expires_at)
 VALUES(p_publication_id,p_stream_id,p_public_id,p_serving_state_id,p_registration_id,p_filters_json,p_expires_at)
 ON CONFLICT(publication_id,stream_id) DO UPDATE SET public_id=excluded.public_id,serving_state_id=excluded.serving_state_id,registration_id=excluded.registration_id,filters_json=excluded.filters_json,generation=1,expires_at=excluded.expires_at,updated_at=clock_timestamp();
END $$;
-- +goose StatementEnd
CREATE OR REPLACE FUNCTION dashboard.delete_stream_registration(p_publication_id uuid, p_stream_id text, p_registration_id uuid)
-- +goose StatementBegin
RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, dashboard AS $$ BEGIN DELETE FROM dashboard.publication_streams WHERE publication_id=p_publication_id AND stream_id=p_stream_id AND registration_id=p_registration_id; END $$;
-- +goose StatementEnd
CREATE OR REPLACE FUNCTION dashboard.expire_stream_registration(p_publication_id uuid, p_stream_id text, p_registration_id uuid)
-- +goose StatementBegin
RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, dashboard AS $$ DECLARE n bigint; BEGIN UPDATE dashboard.publication_streams SET expires_at=clock_timestamp(),updated_at=clock_timestamp() WHERE publication_id=p_publication_id AND stream_id=p_stream_id AND registration_id=p_registration_id; GET DIAGNOSTICS n=ROW_COUNT; RETURN n; END $$;
-- +goose StatementEnd
CREATE OR REPLACE FUNCTION dashboard.update_command_state(p_publication_id uuid, p_stream_id text, p_public_id text, p_serving_state_id text, p_registration_id uuid, p_current_generation bigint, p_filters_json jsonb, p_next_generation bigint, p_expires_at timestamptz)
-- +goose StatementBegin
RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, dashboard AS $$ DECLARE n bigint; v_now timestamptz := clock_timestamp(); BEGIN IF p_next_generation <> p_current_generation + 1 OR p_expires_at IS NULL OR p_expires_at <= v_now OR p_expires_at > v_now+interval '24 hours' THEN RAISE EXCEPTION 'publication stream generation or expiry is invalid'; END IF; UPDATE dashboard.publication_streams SET filters_json=p_filters_json,generation=p_next_generation,expires_at=p_expires_at,updated_at=v_now WHERE publication_id=p_publication_id AND stream_id=p_stream_id AND public_id=p_public_id AND serving_state_id=p_serving_state_id AND registration_id=p_registration_id AND generation=p_current_generation AND expires_at>v_now; GET DIAGNOSTICS n=ROW_COUNT; RETURN n; END $$;
-- +goose StatementEnd
CREATE OR REPLACE FUNCTION dashboard.extend_stream(p_publication_id uuid, p_stream_id text, p_public_id text, p_serving_state_id text, p_registration_id uuid, p_expires_at timestamptz)
-- +goose StatementBegin
RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, dashboard AS $$ DECLARE n bigint; v_now timestamptz := clock_timestamp(); BEGIN IF p_expires_at IS NULL OR p_expires_at <= v_now OR p_expires_at > v_now+interval '24 hours' THEN RAISE EXCEPTION 'publication stream expiry is outside the database-clock window'; END IF; UPDATE dashboard.publication_streams SET expires_at=p_expires_at,updated_at=v_now WHERE publication_id=p_publication_id AND stream_id=p_stream_id AND public_id=p_public_id AND serving_state_id=p_serving_state_id AND registration_id=p_registration_id AND expires_at>v_now; GET DIAGNOSTICS n=ROW_COUNT; RETURN n; END $$;
-- +goose StatementEnd
CREATE OR REPLACE FUNCTION dashboard.expire_publication_streams(p_publication_id uuid)
-- +goose StatementBegin
RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, dashboard AS $$ DECLARE n bigint; BEGIN UPDATE dashboard.publication_streams SET expires_at=clock_timestamp(),updated_at=clock_timestamp() WHERE publication_id=p_publication_id; GET DIAGNOSTICS n=ROW_COUNT; RETURN n; END $$;
-- +goose StatementEnd
CREATE OR REPLACE FUNCTION dashboard.prune_expired_publication_streams(p_now timestamptz, p_batch_limit integer)
-- +goose StatementBegin
RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, dashboard AS $$ DECLARE n bigint; v_now timestamptz := LEAST(COALESCE(p_now,clock_timestamp()),clock_timestamp()); BEGIN IF p_batch_limit IS NULL OR p_batch_limit < 1 OR p_batch_limit > 1000 THEN RAISE EXCEPTION 'publication maintenance batch limit must be between 1 and 1000'; END IF; WITH claimed AS (SELECT s.publication_id,s.stream_id FROM dashboard.publication_streams s WHERE s.expires_at<=v_now ORDER BY s.expires_at,s.publication_id,s.stream_id LIMIT p_batch_limit FOR UPDATE SKIP LOCKED) DELETE FROM dashboard.publication_streams t USING claimed c WHERE t.publication_id=c.publication_id AND t.stream_id=c.stream_id; GET DIAGNOSTICS n=ROW_COUNT; RETURN n; END $$;
-- +goose StatementEnd


-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('project.project_identity') IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'publications_project_fk') THEN
        ALTER TABLE dashboard.publications
            ADD CONSTRAINT publications_project_fk FOREIGN KEY (project_id)
            REFERENCES project.project_identity(project_id) ON DELETE RESTRICT;
    END IF;
END $$;
-- +goose StatementEnd

REVOKE ALL ON SCHEMA dashboard FROM PUBLIC;
REVOKE ALL ON TABLE dashboard.publications,dashboard.publication_events,dashboard.publication_streams FROM PUBLIC;
REVOKE ALL ON SEQUENCE dashboard.publication_events_id_seq FROM PUBLIC;
REVOKE ALL ON FUNCTION dashboard.guard_publication_update(), dashboard.guard_publication_stream_update(), dashboard.check_publication_evidence(uuid,text,text,text,text,text,text,uuid,bigint,text,text,jsonb,jsonb), dashboard.record_publication_event(uuid,uuid,bigint,bigint,text,text,text,text,jsonb), dashboard.publication_payload(uuid,text), dashboard.mutate_publication(text,uuid,text,text,text,text,text,text,jsonb,jsonb,text,bigint,text,uuid,bigint,text,text,jsonb,text,text,text,jsonb), dashboard.suspend_publication(text,text,text,bigint,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.resume_publication(text,text,text,bigint,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.rotate_publication(text,text,text,bigint,text,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.disable_publication(uuid,bigint,text,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.update_publication_configuration(uuid,text,text,text,jsonb,jsonb,text,bigint,text,uuid,bigint,text,text,jsonb,text,text,text,jsonb), dashboard.create_publication(uuid,text,text,text,text,text,text,jsonb,jsonb,text,text,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.upsert_publication_stream(uuid,text,text,text,uuid,jsonb,timestamptz), dashboard.delete_stream_registration(uuid,text,uuid), dashboard.expire_stream_registration(uuid,text,uuid), dashboard.update_command_state(uuid,text,text,text,uuid,bigint,jsonb,bigint,timestamptz), dashboard.extend_stream(uuid,text,text,text,uuid,timestamptz), dashboard.expire_publication_streams(uuid), dashboard.prune_expired_publication_streams(timestamptz,integer) FROM PUBLIC;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_owner') THEN
  GRANT USAGE ON SCHEMA dashboard TO leapview_control_owner;
  GRANT ALL ON TABLE dashboard.publications,dashboard.publication_events,dashboard.publication_streams TO leapview_control_owner;
  GRANT ALL ON SEQUENCE dashboard.publication_events_id_seq TO leapview_control_owner;
  GRANT ALL ON FUNCTION dashboard.guard_publication_update(), dashboard.guard_publication_stream_update(), dashboard.check_publication_evidence(uuid,text,text,text,text,text,text,uuid,bigint,text,text,jsonb,jsonb), dashboard.record_publication_event(uuid,uuid,bigint,bigint,text,text,text,text,jsonb), dashboard.publication_payload(uuid,text), dashboard.mutate_publication(text,uuid,text,text,text,text,text,text,jsonb,jsonb,text,bigint,text,uuid,bigint,text,text,jsonb,text,text,text,jsonb), dashboard.suspend_publication(text,text,text,bigint,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.resume_publication(text,text,text,bigint,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.rotate_publication(text,text,text,bigint,text,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.disable_publication(uuid,bigint,text,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.update_publication_configuration(uuid,text,text,text,jsonb,jsonb,text,bigint,text,uuid,bigint,text,text,jsonb,text,text,text,jsonb), dashboard.create_publication(uuid,text,text,text,text,text,text,jsonb,jsonb,text,text,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.upsert_publication_stream(uuid,text,text,text,uuid,jsonb,timestamptz), dashboard.delete_stream_registration(uuid,text,uuid), dashboard.expire_stream_registration(uuid,text,uuid), dashboard.update_command_state(uuid,text,text,text,uuid,bigint,jsonb,bigint,timestamptz), dashboard.extend_stream(uuid,text,text,text,uuid,timestamptz), dashboard.expire_publication_streams(uuid), dashboard.prune_expired_publication_streams(timestamptz,integer) TO leapview_control_owner;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_migrator') THEN
  GRANT USAGE ON SCHEMA dashboard TO leapview_control_migrator;
  GRANT ALL ON TABLE dashboard.publications,dashboard.publication_events,dashboard.publication_streams TO leapview_control_migrator;
  GRANT ALL ON SEQUENCE dashboard.publication_events_id_seq TO leapview_control_migrator;
  GRANT ALL ON FUNCTION dashboard.guard_publication_update(), dashboard.guard_publication_stream_update(), dashboard.check_publication_evidence(uuid,text,text,text,text,text,text,uuid,bigint,text,text,jsonb,jsonb), dashboard.record_publication_event(uuid,uuid,bigint,bigint,text,text,text,text,jsonb), dashboard.publication_payload(uuid,text), dashboard.mutate_publication(text,uuid,text,text,text,text,text,text,jsonb,jsonb,text,bigint,text,uuid,bigint,text,text,jsonb,text,text,text,jsonb), dashboard.suspend_publication(text,text,text,bigint,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.resume_publication(text,text,text,bigint,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.rotate_publication(text,text,text,bigint,text,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.disable_publication(uuid,bigint,text,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.update_publication_configuration(uuid,text,text,text,jsonb,jsonb,text,bigint,text,uuid,bigint,text,text,jsonb,text,text,text,jsonb), dashboard.create_publication(uuid,text,text,text,text,text,text,jsonb,jsonb,text,text,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.upsert_publication_stream(uuid,text,text,text,uuid,jsonb,timestamptz), dashboard.delete_stream_registration(uuid,text,uuid), dashboard.expire_stream_registration(uuid,text,uuid), dashboard.update_command_state(uuid,text,text,text,uuid,bigint,jsonb,bigint,timestamptz), dashboard.extend_stream(uuid,text,text,text,uuid,timestamptz), dashboard.expire_publication_streams(uuid) TO leapview_control_migrator;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_runtime') THEN
  GRANT USAGE ON SCHEMA dashboard TO leapview_control_runtime;
  GRANT SELECT ON dashboard.publications,dashboard.publication_events,dashboard.publication_streams TO leapview_control_runtime;
  REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON dashboard.publications,dashboard.publication_events,dashboard.publication_streams FROM leapview_control_runtime;
  GRANT EXECUTE ON FUNCTION dashboard.suspend_publication(text,text,text,bigint,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.resume_publication(text,text,text,bigint,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.rotate_publication(text,text,text,bigint,text,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.disable_publication(uuid,bigint,text,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.update_publication_configuration(uuid,text,text,text,jsonb,jsonb,text,bigint,text,uuid,bigint,text,text,jsonb,text,text,text,jsonb), dashboard.create_publication(uuid,text,text,text,text,text,text,jsonb,jsonb,text,text,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.upsert_publication_stream(uuid,text,text,text,uuid,jsonb,timestamptz), dashboard.delete_stream_registration(uuid,text,uuid), dashboard.expire_stream_registration(uuid,text,uuid), dashboard.update_command_state(uuid,text,text,text,uuid,bigint,jsonb,bigint,timestamptz), dashboard.extend_stream(uuid,text,text,text,uuid,timestamptz), dashboard.expire_publication_streams(uuid) TO leapview_control_runtime;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_maintenance') THEN
  GRANT USAGE ON SCHEMA dashboard TO leapview_control_maintenance;
  GRANT SELECT ON dashboard.publication_streams TO leapview_control_maintenance;
  GRANT EXECUTE ON FUNCTION dashboard.prune_expired_publication_streams(timestamptz,integer) TO leapview_control_maintenance;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_readonly') THEN
  GRANT USAGE ON SCHEMA dashboard TO leapview_control_readonly;
  GRANT SELECT ON dashboard.publications,dashboard.publication_events,dashboard.publication_streams TO leapview_control_readonly;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_backup') THEN
  GRANT USAGE ON SCHEMA dashboard TO leapview_control_backup;
  GRANT SELECT ON dashboard.publications,dashboard.publication_events,dashboard.publication_streams TO leapview_control_backup;
 END IF;
END $$;
-- +goose StatementEnd

-- capability source: internal/analytics/connectionbinding/postgres/schema.sql
-- PostgreSQL connection-binding capability schema (ADR-0020).
--
-- This schema stores target-scoped, non-secret connection state. Credential
-- values are resolved by the credential authority at runtime and are never
-- persisted here.
CREATE SCHEMA IF NOT EXISTS connection_binding;

CREATE OR REPLACE FUNCTION connection_binding.endpoint_is_valid(value jsonb)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE
STRICT
SET search_path = pg_catalog
-- +goose StatementBegin
AS $$
DECLARE
    key text;
    item jsonb;
    option_key text;
    option_value jsonb;
BEGIN
    IF jsonb_typeof(value) <> 'object'
       OR octet_length(value::text) NOT BETWEEN 2 AND 16384
       OR value - 'host' - 'port' - 'database' - 'objectScope' - 'sourceIdentity' - 'tlsMode' - 'options' <> '{}'::jsonb THEN
        RETURN false;
    END IF;
    FOR key, item IN SELECT * FROM jsonb_each(value) LOOP
        IF key IN ('host', 'database', 'objectScope', 'sourceIdentity', 'tlsMode') THEN
            IF jsonb_typeof(item) <> 'string' OR item #>> '{}' <> btrim(item #>> '{}') THEN
                RETURN false;
            END IF;
        ELSIF key = 'port' THEN
            IF jsonb_typeof(item) <> 'number' OR item #>> '{}' !~ '^[0-9]+$'
               OR (item #>> '{}')::numeric > 65535 THEN
                RETURN false;
            END IF;
        ELSIF key = 'options' THEN
            IF jsonb_typeof(item) <> 'object' THEN
                RETURN false;
            END IF;
            FOR option_key, option_value IN SELECT * FROM jsonb_each(item) LOOP
                IF option_key !~ '^[A-Za-z_][A-Za-z0-9_.-]{0,127}$'
                   OR jsonb_typeof(option_value) <> 'string'
                   OR lower(option_key) ~ '(password|secret|token|credential|private_key|access_key)' THEN
                    RETURN false;
                END IF;
            END LOOP;
        ELSE
            RETURN false;
        END IF;
    END LOOP;
    RETURN true;
END;
$$;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS connection_binding.target_connection_binding (
    id                       text PRIMARY KEY,
    target_id                text NOT NULL,
    connection_id            text NOT NULL,
    connector_kind           text NOT NULL,
    authentication_mode      text NOT NULL,
    project_id               text NOT NULL,
    environment              text NOT NULL,
    endpoint_json            jsonb NOT NULL,
    credential_project_id    text NOT NULL DEFAULT '',
    credential_environment   text NOT NULL DEFAULT '',
    credential_secret_path   text NOT NULL DEFAULT '',
    credential_secret_key    text NOT NULL DEFAULT '',
    enabled                  boolean NOT NULL,
    validated_version        text NOT NULL DEFAULT '',
    health                   text NOT NULL,
    health_reason            text NOT NULL DEFAULT '',
    last_validated_at        timestamptz,
    created_at               timestamptz NOT NULL,
    updated_at               timestamptz NOT NULL,
    revision                 bigint NOT NULL,
    CHECK (id = btrim(id) AND id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]{0,159}$'),
    CHECK (target_id = btrim(target_id) AND target_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]{0,159}$'),
    CHECK (connection_id = btrim(connection_id) AND octet_length(connection_id) BETWEEN 1 AND 255
        AND connection_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (connector_kind ~ '^[A-Za-z_][A-Za-z0-9_.:-]{0,127}$'
        AND connector_kind IN ('managed', 's3', 'r2', 'gcs', 'http', 'azure_blob', 'postgres', 'mysql', 'sqlite', 'ducklake', 'quack')),
    CHECK (authentication_mode IN ('none', 'external_bundle', 'workload_identity')),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255
        AND project_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (environment ~ '^[A-Za-z_][A-Za-z0-9_.:-]{0,127}$'),
    CHECK (connection_binding.endpoint_is_valid(endpoint_json)),
    CHECK (credential_project_id = btrim(credential_project_id) AND octet_length(credential_project_id) <= 255),
    CHECK (credential_environment = btrim(credential_environment) AND octet_length(credential_environment) <= 255),
    CHECK (credential_secret_path = btrim(credential_secret_path) AND octet_length(credential_secret_path) <= 1024),
    CHECK (credential_secret_key = btrim(credential_secret_key) AND octet_length(credential_secret_key) <= 255),
    CHECK (octet_length(validated_version) <= 255),
    CHECK (health IN ('pending', 'healthy', 'degraded', 'disabled')),
    CHECK ((health <> 'healthy') OR (btrim(validated_version) <> '' AND last_validated_at IS NOT NULL)),
    CHECK ((health <> 'degraded') OR health_reason ~ '^[A-Z0-9_]{1,64}$'),
    CHECK ((health = 'degraded') OR health_reason = ''),
    CHECK (octet_length(health_reason) <= 255),
    CHECK (revision > 0),
    CHECK (updated_at >= created_at),
    CHECK (last_validated_at IS NULL OR (last_validated_at >= created_at AND last_validated_at <= updated_at)),
    CHECK ((enabled AND health <> 'disabled') OR (NOT enabled AND health = 'disabled')),
    CHECK ((authentication_mode = 'external_bundle'
            AND credential_project_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'
            AND credential_environment <> ''
            AND credential_secret_path LIKE '/%'
            AND credential_secret_key <> '')
        OR (authentication_mode <> 'external_bundle'
            AND credential_project_id = '' AND credential_environment = ''
            AND credential_secret_path = '' AND credential_secret_key = ''))
);

CREATE UNIQUE INDEX IF NOT EXISTS target_connection_binding_scope_idx
    ON connection_binding.target_connection_binding (target_id, project_id, environment, connection_id);
CREATE INDEX IF NOT EXISTS target_connection_binding_health_idx
    ON connection_binding.target_connection_binding (target_id, environment, health, updated_at DESC);

-- The capability owns mutable revisions, but no caller can delete history or
-- replace identity columns. A stale Save is rejected by its optimistic
-- predicate in the generated query below.
CREATE OR REPLACE FUNCTION connection_binding.reject_identity_change()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, connection_binding
-- +goose StatementBegin
AS $$
BEGIN
    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.target_id IS DISTINCT FROM OLD.target_id
       OR NEW.connection_id IS DISTINCT FROM OLD.connection_id
       OR NEW.project_id IS DISTINCT FROM OLD.project_id
       OR NEW.environment IS DISTINCT FROM OLD.environment
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.revision <> OLD.revision + 1 THEN
        RAISE EXCEPTION 'connection binding identity or revision is immutable';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS target_connection_binding_identity_guard ON connection_binding.target_connection_binding;
CREATE TRIGGER target_connection_binding_identity_guard
    BEFORE UPDATE ON connection_binding.target_connection_binding
    FOR EACH ROW EXECUTE FUNCTION connection_binding.reject_identity_change();

-- No delete operation is part of the domain repository. Keep this invariant
-- true even for owner-level maintenance sessions.
CREATE OR REPLACE FUNCTION connection_binding.reject_delete()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, connection_binding
-- +goose StatementBegin
AS $$
BEGIN
    RAISE EXCEPTION 'connection binding history is not deletable';
END;
$$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS target_connection_binding_no_delete ON connection_binding.target_connection_binding;
CREATE TRIGGER target_connection_binding_no_delete
    BEFORE DELETE ON connection_binding.target_connection_binding
    FOR EACH ROW EXECUTE FUNCTION connection_binding.reject_delete();

REVOKE ALL ON SCHEMA connection_binding FROM PUBLIC;
REVOKE ALL ON TABLE connection_binding.target_connection_binding FROM PUBLIC;
REVOKE ALL ON FUNCTION connection_binding.endpoint_is_valid(jsonb) FROM PUBLIC;
REVOKE ALL ON FUNCTION connection_binding.reject_identity_change() FROM PUBLIC;
REVOKE ALL ON FUNCTION connection_binding.reject_delete() FROM PUBLIC;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_owner') THEN
        GRANT ALL ON SCHEMA connection_binding TO leapview_control_owner;
        GRANT ALL ON connection_binding.target_connection_binding TO leapview_control_owner;
        GRANT EXECUTE ON FUNCTION connection_binding.endpoint_is_valid(jsonb) TO leapview_control_owner;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_migrator') THEN
        GRANT USAGE ON SCHEMA connection_binding TO leapview_control_migrator;
        GRANT ALL ON connection_binding.target_connection_binding TO leapview_control_migrator;
        GRANT EXECUTE ON FUNCTION connection_binding.endpoint_is_valid(jsonb) TO leapview_control_migrator;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA connection_binding TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON connection_binding.target_connection_binding TO leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION connection_binding.endpoint_is_valid(jsonb) TO leapview_control_runtime;
        REVOKE DELETE, TRUNCATE, REFERENCES, TRIGGER ON connection_binding.target_connection_binding FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA connection_binding TO leapview_control_readonly;
        GRANT SELECT ON connection_binding.target_connection_binding TO leapview_control_readonly;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON connection_binding.target_connection_binding FROM leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA connection_binding TO leapview_control_backup;
        GRANT SELECT ON connection_binding.target_connection_binding TO leapview_control_backup;
    END IF;
END
$$;
-- +goose StatementEnd

-- capability source: internal/platform/events/postgres/schema.sql
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
-- +goose StatementBegin
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
-- +goose StatementEnd
REVOKE ALL ON FUNCTION event.prune_event_log(timestamptz, integer) FROM PUBLIC;

CREATE OR REPLACE FUNCTION event.reject_event_update()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
-- +goose StatementBegin
AS $$
BEGIN
    RAISE EXCEPTION 'durable event log is immutable';
END;
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION event.reject_event_update() FROM PUBLIC;

-- occurred_at is authority-owned rather than caller supplied.  Keeping this
-- invariant in the database protects direct INSERT paths (including a role
-- whose INSERT privilege is intentionally narrow) as well as Repository.
CREATE OR REPLACE FUNCTION event.set_event_occurred_at()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
-- +goose StatementBegin
AS $$
BEGIN
    NEW.occurred_at := clock_timestamp();
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
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
-- +goose StatementBegin
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
-- +goose StatementEnd

-- capability source: internal/manageddata/postgres/schema.sql
-- Clean-slate managed-data control authority (ADR-0020).
-- Object storage owns bytes and DuckLake owns analytical metadata.  These
-- tables contain only identity, admission, serving, lease and reconciliation
-- evidence.  The schema intentionally does not recreate legacy SQLite names.

CREATE SCHEMA IF NOT EXISTS managed_data;
CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA managed_data;

CREATE TABLE IF NOT EXISTS managed_data.collection (
    collection_id text PRIMARY KEY,
    project_id text NOT NULL,
    connection_id text NOT NULL,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','archived')),
    created_by text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    archived_at timestamptz,
    request_digest text NOT NULL,
    UNIQUE (project_id, connection_id),
    CHECK (collection_id = btrim(collection_id) AND octet_length(collection_id) BETWEEN 1 AND 255),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255),
    CHECK (connection_id = btrim(connection_id) AND octet_length(connection_id) BETWEEN 1 AND 255),
    CHECK (name = btrim(name) AND octet_length(name) BETWEEN 1 AND 255),
    CHECK (octet_length(description) <= 4096),
    CHECK ((status = 'archived') = (archived_at IS NOT NULL)),
    CHECK (octet_length(request_digest) BETWEEN 1 AND 255)
);

CREATE OR REPLACE FUNCTION managed_data.guard_collection_insert() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
-- +goose StatementBegin
AS $$
BEGIN
  IF NEW.status <> 'active' OR NEW.archived_at IS NOT NULL THEN
    RAISE EXCEPTION 'collection must begin in canonical active state';
  END IF;
  NEW.created_at := clock_timestamp();
  NEW.updated_at := NEW.created_at;
  RETURN NEW;
END $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS collection_insert_guard ON managed_data.collection;
CREATE TRIGGER collection_insert_guard BEFORE INSERT ON managed_data.collection FOR EACH ROW EXECUTE FUNCTION managed_data.guard_collection_insert();

CREATE TABLE IF NOT EXISTS managed_data.revision (
    revision_id text PRIMARY KEY,
    collection_id text NOT NULL REFERENCES managed_data.collection(collection_id) ON DELETE RESTRICT,
    sequence bigint NOT NULL CHECK (sequence > 0),
    digest text NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','ready','failed')),
    manifest jsonb NOT NULL,
    file_count bigint NOT NULL CHECK (file_count >= 0),
    size_bytes bigint NOT NULL CHECK (size_bytes >= 0),
    created_by text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    ready_at timestamptz,
    error text NOT NULL DEFAULT '',
    UNIQUE (collection_id, sequence),
    UNIQUE (collection_id, digest),
    UNIQUE (collection_id, revision_id),
    UNIQUE (collection_id, revision_id, digest),
    CHECK (octet_length(revision_id) BETWEEN 1 AND 255),
    CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (jsonb_typeof(manifest) = 'object' AND octet_length(manifest::text) <= 1048576),
    CHECK ((status = 'ready') = (ready_at IS NOT NULL)),
    CHECK (status <> 'failed' OR octet_length(error) > 0),
    CHECK (octet_length(error) <= 4096)
);

CREATE TABLE IF NOT EXISTS managed_data.revision_file (
    revision_id text NOT NULL REFERENCES managed_data.revision(revision_id) ON DELETE RESTRICT,
    logical_path text NOT NULL,
    size_bytes bigint NOT NULL CHECK (size_bytes >= 0),
    sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    storage_key text NOT NULL,
    media_type text NOT NULL DEFAULT '',
    etag text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (revision_id, logical_path),
    CHECK (logical_path = btrim(logical_path) AND octet_length(logical_path) BETWEEN 1 AND 1024),
    CHECK (octet_length(storage_key) BETWEEN 1 AND 2048),
    CHECK (octet_length(media_type) <= 255 AND octet_length(etag) <= 512)
);

CREATE TABLE IF NOT EXISTS managed_data.upload_session (
    upload_id text PRIMARY KEY,
    collection_id text NOT NULL REFERENCES managed_data.collection(collection_id) ON DELETE RESTRICT,
    base_revision_id text,
    revision_id text,
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open','committing','complete','aborted','expired','failed')),
    manifest jsonb NOT NULL,
    expected_file_count bigint NOT NULL CHECK (expected_file_count >= 0),
    expected_size_bytes bigint NOT NULL CHECK (expected_size_bytes >= 0),
    uploaded_file_count bigint NOT NULL DEFAULT 0 CHECK (uploaded_file_count >= 0),
    uploaded_size_bytes bigint NOT NULL DEFAULT 0 CHECK (uploaded_size_bytes >= 0),
    storage_backend text NOT NULL,
    staging_prefix text NOT NULL,
    created_by text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    completed_at timestamptz,
    cleanup_completed_at timestamptz,
    error text NOT NULL DEFAULT '',
    request_digest text NOT NULL,
    manifest_digest text NOT NULL,
    completion_digest text NOT NULL DEFAULT '',
    UNIQUE (collection_id, upload_id),
    FOREIGN KEY (collection_id, base_revision_id) REFERENCES managed_data.revision(collection_id, revision_id) ON DELETE RESTRICT,
    FOREIGN KEY (collection_id, revision_id) REFERENCES managed_data.revision(collection_id, revision_id) ON DELETE RESTRICT,
    CHECK (octet_length(upload_id) BETWEEN 1 AND 255),
    CHECK (jsonb_typeof(manifest) = 'object' AND octet_length(manifest::text) <= 1048576),
    CHECK (octet_length(storage_backend) BETWEEN 1 AND 255 AND storage_backend = btrim(storage_backend)),
    CHECK (octet_length(staging_prefix) BETWEEN 1 AND 2048),
    CHECK (uploaded_file_count <= expected_file_count AND uploaded_size_bytes <= expected_size_bytes),
    CHECK (expires_at > created_at),
    CHECK ((status = 'complete') = (revision_id IS NOT NULL AND completed_at IS NOT NULL)),
    CHECK (status <> 'failed' OR octet_length(error) > 0),
    CHECK (octet_length(error) <= 4096),
    CHECK (octet_length(request_digest) BETWEEN 1 AND 255),
    CHECK (manifest_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (completion_digest = '' OR completion_digest ~ '^sha256:[0-9a-f]{64}$')
);
CREATE INDEX IF NOT EXISTS upload_session_cleanup_idx ON managed_data.upload_session(status, cleanup_completed_at, updated_at, upload_id);
CREATE INDEX IF NOT EXISTS upload_session_expiry_idx ON managed_data.upload_session(status, expires_at);

-- Reachability is versioned by a tiny monotonic epoch. The epoch lets the
-- maintenance transaction prove that the manifest set observed before the
-- physical inventory walk is still current without rereading every JSONB
-- manifest while SHARE locks are held.
CREATE TABLE IF NOT EXISTS managed_data.reachability_epoch (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    epoch bigint NOT NULL DEFAULT 1 CHECK (epoch > 0)
);
INSERT INTO managed_data.reachability_epoch(singleton, epoch)
VALUES (true, 1)
ON CONFLICT (singleton) DO NOTHING;

CREATE INDEX IF NOT EXISTS revision_reachability_idx
    ON managed_data.revision (revision_id)
    WHERE status = 'ready';
CREATE INDEX IF NOT EXISTS upload_session_reachability_idx
    ON managed_data.upload_session (status, upload_id)
    WHERE status IN ('open', 'committing');

CREATE OR REPLACE FUNCTION managed_data.guard_upload_insert() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
-- +goose StatementBegin
AS $$
DECLARE cstatus text; bstatus text;
BEGIN
  -- Upload rows have one canonical admission shape.  The trigger overwrites
  -- timestamps so a runtime caller cannot backdate an upload or fabricate
  -- lifecycle evidence while still retaining the table defaults for direct
  -- owner/migrator inserts.
  IF NEW.status <> 'open' OR NEW.uploaded_file_count <> 0 OR NEW.uploaded_size_bytes <> 0
     OR NEW.revision_id IS NOT NULL OR NEW.completed_at IS NOT NULL
     OR NEW.cleanup_completed_at IS NOT NULL OR NEW.error <> '' OR NEW.completion_digest <> '' THEN
    RAISE EXCEPTION 'upload session must begin in canonical open state';
  END IF;
  NEW.created_at := clock_timestamp();
  NEW.updated_at := NEW.created_at;
  SELECT status INTO cstatus FROM managed_data.collection WHERE collection_id=NEW.collection_id;
  IF cstatus IS DISTINCT FROM 'active' THEN RAISE EXCEPTION 'upload session requires an active collection'; END IF;
  IF NEW.base_revision_id IS NOT NULL THEN
    SELECT r.status INTO bstatus FROM managed_data.revision r WHERE r.collection_id=NEW.collection_id AND r.revision_id=NEW.base_revision_id;
    IF bstatus IS DISTINCT FROM 'ready' THEN RAISE EXCEPTION 'base revision must be ready in the same collection'; END IF;
  END IF;
  RETURN NEW;
END $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS upload_insert_guard ON managed_data.upload_session;
CREATE TRIGGER upload_insert_guard BEFORE INSERT ON managed_data.upload_session FOR EACH ROW EXECUTE FUNCTION managed_data.guard_upload_insert();

CREATE TABLE IF NOT EXISTS managed_data.multipart_upload (
    multipart_id text PRIMARY KEY,
    upload_id text NOT NULL REFERENCES managed_data.upload_session(upload_id) ON DELETE RESTRICT,
    logical_path text NOT NULL,
    sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    size_bytes bigint NOT NULL CHECK (size_bytes >= 0),
    object_key text NOT NULL DEFAULT '',
    provider_upload_id text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'creating' CHECK (status IN ('creating','open','completing','completed','aborting','aborted','failed')),
    existing boolean NOT NULL DEFAULT false,
    idempotency_identity text NOT NULL,
    completion_identity text NOT NULL DEFAULT '',
    completion_request_hash text NOT NULL DEFAULT '',
    abort_identity text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    completed_at timestamptz,
    aborted_at timestamptz,
    error text NOT NULL DEFAULT '',
    UNIQUE (upload_id, idempotency_identity),
    CHECK (logical_path = btrim(logical_path) AND octet_length(logical_path) BETWEEN 1 AND 1024),
    CHECK (octet_length(idempotency_identity) BETWEEN 1 AND 255),
    CHECK (octet_length(object_key) <= 2048 AND octet_length(provider_upload_id) <= 512),
    CHECK (completion_identity = '' OR octet_length(completion_identity) <= 255),
    CHECK (completion_request_hash = '' OR completion_request_hash ~ '^[0-9a-f]{64}$'),
    CHECK (abort_identity = '' OR octet_length(abort_identity) <= 255),
    CHECK (status = 'creating' OR octet_length(object_key) > 0 OR status IN ('aborting','aborted')),
    CHECK (NOT existing OR status = 'completed'),
    CHECK ((status = 'completed') = (completed_at IS NOT NULL)),
    CHECK ((status = 'aborted') = (aborted_at IS NOT NULL)),
    CHECK (status <> 'failed' OR octet_length(error) > 0),
    CHECK (octet_length(error) <= 4096),
    CHECK (octet_length(completion_identity) <= 255 AND octet_length(abort_identity) <= 255)
);
CREATE TABLE IF NOT EXISTS managed_data.multipart_part (
    multipart_id text NOT NULL REFERENCES managed_data.multipart_upload(multipart_id) ON DELETE RESTRICT,
    part_number integer NOT NULL CHECK (part_number BETWEEN 1 AND 10000),
    size_bytes bigint NOT NULL CHECK (size_bytes > 0),
    sha256 text NOT NULL DEFAULT '' CHECK (sha256 = '' OR sha256 ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (multipart_id, part_number)
);
CREATE TABLE IF NOT EXISTS managed_data.multipart_digest_lease (
    sha256 text PRIMARY KEY CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    owner_id text NOT NULL,
    fencing_epoch bigint NOT NULL CHECK (fencing_epoch > 0),
    state text NOT NULL DEFAULT 'held' CHECK (state IN ('held','released')),
    lease_until timestamptz NOT NULL,
    CHECK (octet_length(owner_id) BETWEEN 1 AND 255)
);

CREATE TABLE IF NOT EXISTS managed_data.environment_pointer (
    collection_id text NOT NULL REFERENCES managed_data.collection(collection_id) ON DELETE RESTRICT,
    environment text NOT NULL,
    revision_id text NOT NULL,
    revision_digest text NOT NULL CHECK (revision_digest ~ '^sha256:[0-9a-f]{64}$'),
    deployment_id text NOT NULL,
    generation bigint NOT NULL CHECK (generation > 0),
    updated_by text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (collection_id, environment),
    FOREIGN KEY (collection_id, revision_id, revision_digest) REFERENCES managed_data.revision(collection_id, revision_id, digest) ON DELETE RESTRICT,
    CHECK (environment = btrim(environment) AND octet_length(environment) BETWEEN 1 AND 128),
    CHECK (octet_length(deployment_id) BETWEEN 1 AND 255 AND octet_length(updated_by) <= 255)
);

CREATE TABLE IF NOT EXISTS managed_data.binding_set (
    project_id text NOT NULL,
    environment text NOT NULL,
    generation_id text NOT NULL,
    binding_digest text NOT NULL CHECK (binding_digest ~ '^sha256:[0-9a-f]{64}$'),
    binding_count bigint NOT NULL CHECK (binding_count >= 0),
    installed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (project_id, environment, generation_id),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255),
    CHECK (environment = btrim(environment) AND octet_length(environment) BETWEEN 1 AND 128),
    CHECK (generation_id = btrim(generation_id) AND octet_length(generation_id) BETWEEN 1 AND 255)
);
CREATE TABLE IF NOT EXISTS managed_data.binding (
    project_id text NOT NULL,
    environment text NOT NULL,
    generation_id text NOT NULL,
    collection_id text NOT NULL REFERENCES managed_data.collection(collection_id) ON DELETE RESTRICT,
    revision_id text NOT NULL,
    bound_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (project_id, environment, generation_id, collection_id),
    FOREIGN KEY (project_id, environment, generation_id) REFERENCES managed_data.binding_set(project_id, environment, generation_id) ON DELETE RESTRICT,
    FOREIGN KEY (collection_id, revision_id) REFERENCES managed_data.revision(collection_id, revision_id) ON DELETE RESTRICT
);

CREATE OR REPLACE FUNCTION managed_data.guard_binding_insert() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
-- +goose StatementBegin
AS $$
DECLARE p text; st text;
BEGIN
  SELECT c.project_id, r.status INTO p, st
    FROM managed_data.collection c JOIN managed_data.revision r ON r.collection_id = c.collection_id
   WHERE c.collection_id = NEW.collection_id AND r.revision_id = NEW.revision_id;
  IF p IS NULL OR p <> NEW.project_id OR st <> 'ready' THEN RAISE EXCEPTION 'binding requires a ready revision in the same project and collection'; END IF;
  RETURN NEW;
END $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS binding_insert_guard ON managed_data.binding;
CREATE TRIGGER binding_insert_guard BEFORE INSERT ON managed_data.binding FOR EACH ROW EXECUTE FUNCTION managed_data.guard_binding_insert();

CREATE OR REPLACE FUNCTION managed_data.publish_binding_set(p_project text, p_environment text, p_generation text, p_digest text, p_count bigint, p_bindings jsonb)
-- +goose StatementBegin
RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, managed_data AS $$
DECLARE payload text; computed text; existing_digest text; existing_count bigint; total_count bigint; distinct_count bigint; cid text; rid text; st text; cp text;
BEGIN
  IF jsonb_typeof(p_bindings) <> 'array' OR p_count < 0 OR jsonb_array_length(p_bindings) <> p_count THEN RAISE EXCEPTION 'binding set count is invalid'; END IF;
  SELECT string_agg(x.cid || chr(31) || x.rid || chr(31), '' ORDER BY x.cid), count(*), count(DISTINCT x.cid) INTO payload, total_count, distinct_count
    FROM (SELECT value->>'collection_id' cid, value->>'revision_id' rid FROM jsonb_array_elements(p_bindings)) x;
  IF total_count <> p_count OR distinct_count <> p_count THEN RAISE EXCEPTION 'binding set contains duplicate or invalid collections'; END IF;
  computed := 'sha256:' || encode(digest(convert_to(coalesce(payload,''),'UTF8'),'sha256'),'hex');
  IF computed <> p_digest THEN RAISE EXCEPTION 'binding digest does not match rows'; END IF;
  IF EXISTS (SELECT 1 FROM managed_data.binding_set WHERE project_id=p_project AND environment=p_environment AND generation_id=p_generation) THEN
    SELECT binding_digest,binding_count INTO existing_digest,existing_count FROM managed_data.binding_set WHERE project_id=p_project AND environment=p_environment AND generation_id=p_generation;
    IF existing_digest <> p_digest OR existing_count <> p_count THEN RAISE EXCEPTION 'binding set conflicts with immutable generation evidence'; END IF;
    RETURN;
  END IF;
  FOR cid, rid IN SELECT value->>'collection_id', value->>'revision_id' FROM jsonb_array_elements(p_bindings) LOOP
    SELECT c.project_id, r.status INTO cp, st FROM managed_data.collection c JOIN managed_data.revision r ON r.collection_id=c.collection_id WHERE c.collection_id=cid AND r.revision_id=rid;
    IF cp IS DISTINCT FROM p_project OR st IS DISTINCT FROM 'ready' THEN RAISE EXCEPTION 'binding requires ready revision in same project'; END IF;
  END LOOP;
  INSERT INTO managed_data.binding_set(project_id,environment,generation_id,binding_digest,binding_count) VALUES(p_project,p_environment,p_generation,p_digest,p_count);
  INSERT INTO managed_data.binding(project_id,environment,generation_id,collection_id,revision_id)
    SELECT p_project,p_environment,p_generation,value->>'collection_id',value->>'revision_id' FROM jsonb_array_elements(p_bindings);
END $$;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS managed_data.lease (
    lease_key text PRIMARY KEY,
    owner_id text NOT NULL,
    fencing_epoch bigint NOT NULL CHECK (fencing_epoch > 0),
    state text NOT NULL DEFAULT 'held' CHECK (state IN ('held','released')),
    expires_at timestamptz NOT NULL,
    acquired_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    released_at timestamptz,
    CHECK (octet_length(lease_key) BETWEEN 1 AND 255 AND octet_length(owner_id) BETWEEN 1 AND 255),
    CHECK ((state='released') = (released_at IS NOT NULL))
);
CREATE TABLE IF NOT EXISTS managed_data.retention_root (
    root_id text PRIMARY KEY,
    project_id text NOT NULL,
    environment text NOT NULL,
    revision_id text,
    state text NOT NULL CHECK (state IN ('live','retiring','expired')),
    evidence jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    -- DuckLake snapshot retention/root ownership lives in the DuckLake
    -- capability.  This table intentionally admits only managed-data
    -- revision roots, avoiding a duplicate cross-database snapshot authority.
    CHECK (revision_id IS NOT NULL),
    CHECK (octet_length(root_id) BETWEEN 1 AND 255 AND octet_length(project_id) BETWEEN 1 AND 255 AND octet_length(environment) BETWEEN 1 AND 128),
    CHECK (octet_length(revision_id) BETWEEN 1 AND 255),
    CHECK (jsonb_typeof(evidence) = 'object' AND evidence <> '{}'::jsonb AND octet_length(evidence::text) <= 65536)
);

CREATE OR REPLACE FUNCTION managed_data.guard_lease_insert() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
-- +goose StatementBegin
AS $$
BEGIN
  IF NEW.state <> 'held' OR NEW.fencing_epoch <> 1 OR NEW.released_at IS NOT NULL
     OR NEW.expires_at <= clock_timestamp() OR NEW.expires_at > clock_timestamp()+interval '24 hours' THEN
    RAISE EXCEPTION 'lease must begin as a held epoch-one lease within the DB-clock bound';
  END IF;
  NEW.acquired_at := clock_timestamp();
  RETURN NEW;
END $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS lease_insert_guard ON managed_data.lease;
CREATE TRIGGER lease_insert_guard BEFORE INSERT ON managed_data.lease FOR EACH ROW EXECUTE FUNCTION managed_data.guard_lease_insert();

CREATE OR REPLACE FUNCTION managed_data.guard_digest_lease_insert() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
-- +goose StatementBegin
AS $$
BEGIN
  IF NEW.state <> 'held' OR NEW.fencing_epoch <> 1
     OR NEW.lease_until <= clock_timestamp() OR NEW.lease_until > clock_timestamp()+interval '24 hours' THEN
    RAISE EXCEPTION 'digest lease must begin as a held epoch-one lease within the DB-clock bound';
  END IF;
  RETURN NEW;
END $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS multipart_digest_insert_guard ON managed_data.multipart_digest_lease;
CREATE TRIGGER multipart_digest_insert_guard BEFORE INSERT ON managed_data.multipart_digest_lease FOR EACH ROW EXECUTE FUNCTION managed_data.guard_digest_lease_insert();

CREATE TABLE IF NOT EXISTS managed_data.reconciliation_evidence (
    evidence_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id text NOT NULL,
    environment text NOT NULL,
    object_key text NOT NULL,
    observed_state text NOT NULL,
    action text NOT NULL,
    evidence jsonb NOT NULL DEFAULT '{}'::jsonb,
    observed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (octet_length(object_key) BETWEEN 1 AND 2048),
    CHECK (jsonb_typeof(evidence) = 'object' AND evidence <> '{}'::jsonb AND octet_length(evidence::text) <= 65536)
);

CREATE OR REPLACE FUNCTION managed_data.guard_retention_root_insert() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
-- +goose StatementBegin
AS $$
DECLARE revision_project text;
BEGIN
  IF NEW.revision_id IS NOT NULL THEN
    SELECT c.project_id INTO revision_project
      FROM managed_data.revision r JOIN managed_data.collection c ON c.collection_id=r.collection_id
     WHERE r.revision_id=NEW.revision_id;
    IF revision_project IS NULL OR revision_project <> NEW.project_id THEN
      RAISE EXCEPTION 'retention revision must exist in the declared project';
    END IF;
  END IF;
  RETURN NEW;
END $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS retention_root_insert_guard ON managed_data.retention_root;
CREATE TRIGGER retention_root_insert_guard BEFORE INSERT ON managed_data.retention_root FOR EACH ROW EXECUTE FUNCTION managed_data.guard_retention_root_insert();

-- State transitions are enforced in the database as well as in the Go port,
-- so a compromised runtime client cannot resurrect terminal protocol rows or
-- decrease upload progress.
CREATE OR REPLACE FUNCTION managed_data.guard_upload_transition() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
-- +goose StatementBegin
AS $$
BEGIN
  IF NEW.upload_id <> OLD.upload_id OR NEW.collection_id <> OLD.collection_id
     OR NEW.base_revision_id IS DISTINCT FROM OLD.base_revision_id OR NEW.manifest IS DISTINCT FROM OLD.manifest OR NEW.expected_file_count <> OLD.expected_file_count
     OR NEW.expected_size_bytes <> OLD.expected_size_bytes OR NEW.storage_backend <> OLD.storage_backend
     OR NEW.staging_prefix <> OLD.staging_prefix OR NEW.created_by <> OLD.created_by
     OR NEW.expires_at <> OLD.expires_at OR NEW.request_digest <> OLD.request_digest OR NEW.created_at <> OLD.created_at THEN
    RAISE EXCEPTION 'upload identity and manifest are immutable';
  END IF;
  IF NEW.uploaded_file_count < OLD.uploaded_file_count OR NEW.uploaded_size_bytes < OLD.uploaded_size_bytes THEN
    RAISE EXCEPTION 'upload progress cannot decrease';
  END IF;
  IF OLD.status IN ('complete','aborted','expired','failed') THEN
    -- Terminal protocol rows are immutable.  The sole exception is the
    -- one-way cleanup marker, and even that is accepted only through the
    -- SECURITY DEFINER maintenance capability below.
    IF NEW.status <> OLD.status OR NEW.uploaded_file_count <> OLD.uploaded_file_count
       OR NEW.uploaded_size_bytes <> OLD.uploaded_size_bytes OR NEW.revision_id IS DISTINCT FROM OLD.revision_id
       OR NEW.completed_at IS DISTINCT FROM OLD.completed_at OR NEW.error IS DISTINCT FROM OLD.error
       OR NEW.completion_digest <> OLD.completion_digest THEN
      RAISE EXCEPTION 'terminal upload is immutable';
    END IF;
    IF NEW.cleanup_completed_at IS DISTINCT FROM OLD.cleanup_completed_at THEN
      IF OLD.cleanup_completed_at IS NOT NULL OR NEW.cleanup_completed_at IS NULL
         OR current_setting('managed_data.maintenance', true) <> 'on' OR current_user = session_user THEN
        RAISE EXCEPTION 'cleanup evidence requires bounded maintenance';
      END IF;
    END IF;
    NEW.updated_at := clock_timestamp();
    RETURN NEW;
  END IF;
  IF OLD.status = 'open' AND NEW.status NOT IN ('open','committing','aborted','expired') THEN RAISE EXCEPTION 'invalid upload transition'; END IF;
  IF OLD.status = 'committing' AND NEW.status NOT IN ('committing','complete','failed') THEN RAISE EXCEPTION 'invalid upload transition'; END IF;
  IF OLD.status IN ('complete','aborted','expired','failed') AND NEW.status <> OLD.status THEN RAISE EXCEPTION 'terminal upload cannot transition'; END IF;
  IF OLD.status = 'complete' AND NEW.completion_digest <> OLD.completion_digest THEN RAISE EXCEPTION 'completion identity is immutable'; END IF;
  IF NEW.cleanup_completed_at IS DISTINCT FROM OLD.cleanup_completed_at AND (current_setting('managed_data.maintenance', true) <> 'on' OR current_user = session_user) THEN RAISE EXCEPTION 'cleanup evidence requires bounded maintenance'; END IF;
  IF NEW.status <> 'failed' AND NEW.error IS DISTINCT FROM OLD.error THEN RAISE EXCEPTION 'upload error is only set by failed transition'; END IF;
  IF NEW.status <> 'complete' AND NEW.revision_id IS DISTINCT FROM OLD.revision_id THEN RAISE EXCEPTION 'revision binding requires completion'; END IF;
  IF NEW.status <> 'complete' AND NEW.completed_at IS DISTINCT FROM OLD.completed_at THEN RAISE EXCEPTION 'completion timestamp requires completion'; END IF;
  IF (NEW.status = 'committing' OR NEW.status = 'complete') AND EXISTS (
       SELECT 1 FROM managed_data.multipart_upload m
        WHERE m.upload_id=NEW.upload_id AND m.status NOT IN ('completed','aborted','failed')) THEN
    RAISE EXCEPTION 'upload cannot finalize while multipart children are nonterminal';
  END IF;
  IF NEW.status = 'complete' THEN
    IF NEW.revision_id IS NULL OR NEW.completion_digest = '' OR NEW.uploaded_file_count <> NEW.expected_file_count OR NEW.uploaded_size_bytes <> NEW.expected_size_bytes OR NEW.completed_at IS NULL THEN
      RAISE EXCEPTION 'completed upload requires ready revision, completion identity and exact progress';
    END IF;
    IF NOT EXISTS (
      SELECT 1 FROM managed_data.revision r
       WHERE r.revision_id=NEW.revision_id AND r.collection_id=NEW.collection_id AND r.status='ready'
         AND r.manifest IS NOT DISTINCT FROM NEW.manifest
         AND r.file_count=NEW.expected_file_count AND r.size_bytes=NEW.expected_size_bytes
         AND r.digest=NEW.manifest_digest
    ) THEN
      RAISE EXCEPTION 'completed upload requires matching ready revision manifest';
    END IF;
  END IF;
  NEW.updated_at := clock_timestamp();
  RETURN NEW;
END $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS upload_transition_guard ON managed_data.upload_session;
CREATE TRIGGER upload_transition_guard BEFORE UPDATE ON managed_data.upload_session FOR EACH ROW EXECUTE FUNCTION managed_data.guard_upload_transition();

CREATE OR REPLACE FUNCTION managed_data.guard_collection_transition() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
-- +goose StatementBegin
AS $$
BEGIN
  IF NEW.collection_id <> OLD.collection_id OR NEW.project_id <> OLD.project_id OR NEW.connection_id <> OLD.connection_id
     OR NEW.name <> OLD.name OR NEW.description <> OLD.description OR NEW.created_by <> OLD.created_by
     OR NEW.request_digest <> OLD.request_digest OR NEW.created_at <> OLD.created_at THEN
    RAISE EXCEPTION 'collection identity and authored metadata are immutable';
  END IF;
  IF OLD.status = 'archived' THEN
    IF NEW.status <> 'archived' OR NEW.archived_at IS DISTINCT FROM OLD.archived_at THEN RAISE EXCEPTION 'archived collection is immutable'; END IF;
  ELSIF NEW.status = 'active' THEN
    IF NEW.archived_at IS DISTINCT FROM OLD.archived_at THEN RAISE EXCEPTION 'active collection archive timestamp is immutable'; END IF;
  ELSIF NEW.status = 'archived' THEN
    IF OLD.archived_at IS NOT NULL THEN RAISE EXCEPTION 'collection archive timestamp is already set'; END IF;
    NEW.archived_at := clock_timestamp();
  ELSE
    RAISE EXCEPTION 'invalid collection transition';
  END IF;
  NEW.updated_at := clock_timestamp(); RETURN NEW;
END $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS collection_transition_guard ON managed_data.collection;
CREATE TRIGGER collection_transition_guard BEFORE UPDATE ON managed_data.collection FOR EACH ROW EXECUTE FUNCTION managed_data.guard_collection_transition();

CREATE OR REPLACE FUNCTION managed_data.guard_pointer_generation() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
-- +goose StatementBegin
AS $$
BEGIN
  IF NEW.collection_id <> OLD.collection_id OR NEW.environment <> OLD.environment THEN RAISE EXCEPTION 'pointer scope is immutable'; END IF;
  IF NEW.generation < OLD.generation THEN RAISE EXCEPTION 'pointer generation cannot decrease'; END IF;
  IF NEW.generation = OLD.generation AND (NEW.revision_id, NEW.revision_digest, NEW.deployment_id, NEW.updated_by) IS DISTINCT FROM (OLD.revision_id, OLD.revision_digest, OLD.deployment_id, OLD.updated_by) THEN
    RAISE EXCEPTION 'pointer evidence cannot change at the same generation';
  END IF;
  NEW.updated_at := clock_timestamp(); RETURN NEW;
END $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS pointer_generation_guard ON managed_data.environment_pointer;
CREATE TRIGGER pointer_generation_guard BEFORE UPDATE ON managed_data.environment_pointer FOR EACH ROW EXECUTE FUNCTION managed_data.guard_pointer_generation();
CREATE OR REPLACE FUNCTION managed_data.guard_pointer_revision() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
-- +goose StatementBegin
AS $$
DECLARE st text;
BEGIN
  SELECT status INTO st FROM managed_data.revision WHERE collection_id=NEW.collection_id AND revision_id=NEW.revision_id AND digest=NEW.revision_digest;
  IF st IS DISTINCT FROM 'ready' THEN RAISE EXCEPTION 'environment pointer requires matching ready revision'; END IF;
  RETURN NEW;
END $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS pointer_revision_guard ON managed_data.environment_pointer;
CREATE TRIGGER pointer_revision_guard BEFORE INSERT OR UPDATE ON managed_data.environment_pointer FOR EACH ROW EXECUTE FUNCTION managed_data.guard_pointer_revision();

-- Revisions are admitted in two phases. Identity and manifest fields are
-- immutable; only pending -> ready/failed is allowed. Files may be inserted
-- while pending, but never updated or deleted.
CREATE OR REPLACE FUNCTION managed_data.guard_revision_lifecycle() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
-- +goose StatementBegin
AS $$
DECLARE file_count bigint; total_size bigint;
BEGIN
  IF NEW.revision_id <> OLD.revision_id OR NEW.collection_id <> OLD.collection_id OR NEW.sequence <> OLD.sequence
     OR NEW.digest <> OLD.digest OR NEW.manifest IS DISTINCT FROM OLD.manifest OR NEW.file_count <> OLD.file_count
     OR NEW.size_bytes <> OLD.size_bytes OR NEW.created_by <> OLD.created_by OR NEW.created_at <> OLD.created_at THEN
    RAISE EXCEPTION 'revision identity and admitted metadata are immutable';
  END IF;
  IF OLD.status IN ('ready','failed') THEN RAISE EXCEPTION 'terminal revision is immutable'; END IF;
  IF OLD.status = 'pending' AND NEW.status NOT IN ('pending','ready','failed') THEN RAISE EXCEPTION 'invalid revision transition'; END IF;
  IF OLD.status IN ('ready','failed') AND NEW.status <> OLD.status THEN RAISE EXCEPTION 'terminal revision cannot transition'; END IF;
  IF NEW.status = 'ready' AND NEW.ready_at IS NULL THEN RAISE EXCEPTION 'ready revision requires admission timestamp'; END IF;
  IF NEW.status = 'failed' AND octet_length(NEW.error) = 0 THEN RAISE EXCEPTION 'failed revision requires error'; END IF;
  IF NEW.status = 'ready' THEN
    IF jsonb_typeof(NEW.manifest->'files') <> 'array' THEN
      RAISE EXCEPTION 'ready revision manifest must contain a files array';
    END IF;
    SELECT count(*), COALESCE(sum(size_bytes),0) INTO file_count,total_size FROM managed_data.revision_file WHERE revision_id=NEW.revision_id;
    IF file_count <> NEW.file_count OR total_size <> NEW.size_bytes THEN RAISE EXCEPTION 'ready revision file count or size does not match manifest'; END IF;
    IF file_count <> COALESCE(jsonb_array_length(NEW.manifest->'files'),0) THEN
      RAISE EXCEPTION 'ready revision file count does not match manifest files';
    END IF;
    IF EXISTS (
      SELECT 1
        FROM jsonb_to_recordset(COALESCE(NEW.manifest->'files','[]'::jsonb)) AS mf(path text, size bigint, sha256 text)
        LEFT JOIN managed_data.revision_file rf
          ON rf.revision_id=NEW.revision_id AND rf.logical_path=mf.path
       WHERE mf.path IS NULL OR mf.size IS NULL OR mf.sha256 IS NULL
          OR rf.revision_id IS NULL OR rf.size_bytes <> mf.size OR rf.sha256 <> mf.sha256
    ) THEN
      RAISE EXCEPTION 'ready revision file identity does not match manifest';
    END IF;
  END IF;
  RETURN NEW;
END $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS revision_lifecycle_guard ON managed_data.revision;
CREATE TRIGGER revision_lifecycle_guard BEFORE UPDATE ON managed_data.revision FOR EACH ROW EXECUTE FUNCTION managed_data.guard_revision_lifecycle();

CREATE OR REPLACE FUNCTION managed_data.guard_revision_insert() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
-- +goose StatementBegin
AS $$
BEGIN
  IF NEW.status <> 'pending' OR NEW.ready_at IS NOT NULL OR NEW.error <> '' THEN
    RAISE EXCEPTION 'revision must be admitted in pending state';
  END IF;
  RETURN NEW;
END $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS revision_insert_guard ON managed_data.revision;
CREATE TRIGGER revision_insert_guard BEFORE INSERT ON managed_data.revision FOR EACH ROW EXECUTE FUNCTION managed_data.guard_revision_insert();

-- Keep the reachability epoch authoritative inside the database. The
-- SECURITY DEFINER function is required because runtime roles may transition
-- uploads/revisions but must not be able to forge the epoch relation itself.
CREATE OR REPLACE FUNCTION managed_data.bump_reachability_epoch() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, managed_data
-- +goose StatementBegin
AS $$
DECLARE current_epoch bigint;
BEGIN
  SELECT epoch INTO current_epoch
    FROM managed_data.reachability_epoch
   WHERE singleton = true
   FOR UPDATE;
  IF NOT FOUND OR current_epoch IS NULL THEN
    RAISE EXCEPTION 'managed-data reachability epoch is missing';
  END IF;
  IF current_epoch = 9223372036854775807 THEN
    RAISE EXCEPTION 'managed-data reachability epoch exhausted';
  END IF;
  UPDATE managed_data.reachability_epoch SET epoch = current_epoch + 1 WHERE singleton = true;
  IF TG_OP = 'DELETE' THEN
    RETURN OLD;
  END IF;
  RETURN NEW;
END $$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS revision_reachability_epoch ON managed_data.revision;
CREATE TRIGGER revision_reachability_epoch
AFTER INSERT OR UPDATE OF status, manifest, digest, file_count, size_bytes
ON managed_data.revision FOR EACH ROW
EXECUTE FUNCTION managed_data.bump_reachability_epoch();

DROP TRIGGER IF EXISTS upload_reachability_epoch ON managed_data.upload_session;
CREATE TRIGGER upload_reachability_epoch
AFTER INSERT OR UPDATE OF status, manifest, expected_file_count, expected_size_bytes,
    revision_id
ON managed_data.upload_session FOR EACH ROW
EXECUTE FUNCTION managed_data.bump_reachability_epoch();

CREATE OR REPLACE FUNCTION managed_data.guard_revision_file() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
-- +goose StatementBegin
AS $$
DECLARE st text;
BEGIN
  IF TG_OP <> 'INSERT' THEN RAISE EXCEPTION 'revision files are immutable'; END IF;
  SELECT status INTO st FROM managed_data.revision WHERE revision_id = NEW.revision_id;
  IF st IS DISTINCT FROM 'pending' THEN RAISE EXCEPTION 'revision files may only be inserted while revision is pending'; END IF;
  RETURN NEW;
END $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS revision_file_guard ON managed_data.revision_file;
CREATE TRIGGER revision_file_guard BEFORE INSERT OR UPDATE OR DELETE ON managed_data.revision_file FOR EACH ROW EXECUTE FUNCTION managed_data.guard_revision_file();

CREATE OR REPLACE FUNCTION managed_data.guard_multipart_part() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
-- +goose StatementBegin
AS $$
DECLARE st text; ust text; expiry timestamptz;
BEGIN
  IF TG_OP = 'UPDATE' THEN RAISE EXCEPTION 'multipart part rows are immutable'; END IF;
  IF TG_OP = 'DELETE' THEN
    IF current_setting('managed_data.maintenance', true) <> 'on' OR current_user = session_user THEN RAISE EXCEPTION 'multipart part deletes require bounded maintenance'; END IF;
    RETURN OLD;
  END IF;
  SELECT m.status, s.status, s.expires_at INTO st, ust, expiry
    FROM managed_data.multipart_upload m JOIN managed_data.upload_session s ON s.upload_id=m.upload_id
   WHERE m.multipart_id = NEW.multipart_id;
  IF st IS DISTINCT FROM 'open' OR ust IS DISTINCT FROM 'open' OR expiry <= clock_timestamp() THEN RAISE EXCEPTION 'multipart parts require an open, unexpired upload'; END IF;
  RETURN NEW;
END $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS multipart_part_guard ON managed_data.multipart_part;
CREATE TRIGGER multipart_part_guard BEFORE INSERT OR UPDATE OR DELETE ON managed_data.multipart_part FOR EACH ROW EXECUTE FUNCTION managed_data.guard_multipart_part();

CREATE OR REPLACE FUNCTION managed_data.guard_multipart_upload() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
-- +goose StatementBegin
AS $$
BEGIN
  IF NEW.multipart_id <> OLD.multipart_id OR NEW.upload_id <> OLD.upload_id OR NEW.logical_path <> OLD.logical_path
     OR NEW.sha256 <> OLD.sha256 OR NEW.size_bytes <> OLD.size_bytes OR NEW.created_at <> OLD.created_at
     OR NEW.idempotency_identity <> OLD.idempotency_identity THEN RAISE EXCEPTION 'multipart upload identity is immutable'; END IF;
  IF (NEW.object_key <> OLD.object_key OR NEW.provider_upload_id <> OLD.provider_upload_id OR NEW.existing <> OLD.existing)
     AND OLD.status <> 'creating' THEN RAISE EXCEPTION 'multipart object identity is immutable after initialization'; END IF;
  IF (NEW.completion_identity <> OLD.completion_identity OR NEW.completion_request_hash <> OLD.completion_request_hash)
     AND NOT (OLD.status='open' AND NEW.status='completing') THEN RAISE EXCEPTION 'multipart completion identity is immutable'; END IF;
  IF NEW.abort_identity <> OLD.abort_identity AND NOT (NEW.status='aborting' AND OLD.status IN ('creating','open','failed')) THEN RAISE EXCEPTION 'multipart abort identity is immutable'; END IF;
  IF NEW.completed_at IS DISTINCT FROM OLD.completed_at AND NOT (NEW.status='completed' AND OLD.status IN ('creating','completing')) THEN RAISE EXCEPTION 'multipart completion timestamp is immutable'; END IF;
  IF NEW.aborted_at IS DISTINCT FROM OLD.aborted_at AND NOT (NEW.status='aborted' AND OLD.status='aborting') THEN RAISE EXCEPTION 'multipart abort timestamp is immutable'; END IF;
  IF NEW.error IS DISTINCT FROM OLD.error AND NOT (NEW.status='failed' AND OLD.status IN ('creating','open','completing')) THEN RAISE EXCEPTION 'multipart error is immutable'; END IF;
  IF NEW.status = 'completed' AND NOT NEW.existing AND OLD.status <> 'completing' THEN
    RAISE EXCEPTION 'non-existing multipart uploads require completing transition';
  END IF;
  IF NEW.status IN ('completing','completed') AND NOT NEW.existing
     AND (octet_length(NEW.completion_identity) = 0 OR octet_length(NEW.completion_request_hash) = 0) THEN
    RAISE EXCEPTION 'multipart completion requires idempotency identity and request hash';
  END IF;
  IF NEW.status IN ('completing','completed') AND NOT NEW.existing THEN
    DECLARE part_count bigint; part_size bigint;
    BEGIN
      SELECT count(*), COALESCE(sum(size_bytes),0) INTO part_count,part_size FROM managed_data.multipart_part WHERE multipart_id=NEW.multipart_id;
      IF (NEW.size_bytes > 0 AND part_count = 0) OR part_size <> NEW.size_bytes THEN
        RAISE EXCEPTION 'multipart parts do not match declared object size';
      END IF;
    END;
  END IF;
  IF OLD.status='creating' AND NEW.status NOT IN ('creating','open','completed','aborting','failed') THEN RAISE EXCEPTION 'invalid multipart transition'; END IF;
  IF OLD.status='open' AND NEW.status NOT IN ('open','completing','aborting','failed') THEN RAISE EXCEPTION 'invalid multipart transition'; END IF;
  IF OLD.status='completing' AND NEW.status NOT IN ('completing','completed','aborting','failed') THEN RAISE EXCEPTION 'invalid multipart transition'; END IF;
  IF OLD.status='aborting' AND NEW.status NOT IN ('aborting','aborted') THEN RAISE EXCEPTION 'invalid multipart transition'; END IF;
  IF OLD.status='failed' AND NEW.status NOT IN ('failed','aborting') THEN RAISE EXCEPTION 'invalid multipart transition'; END IF;
  IF OLD.status IN ('completed','aborted') AND (NEW.status <> OLD.status OR NEW.object_key<>OLD.object_key OR NEW.provider_upload_id<>OLD.provider_upload_id OR NEW.existing<>OLD.existing OR NEW.completion_identity<>OLD.completion_identity OR NEW.completion_request_hash<>OLD.completion_request_hash OR NEW.abort_identity<>OLD.abort_identity) THEN RAISE EXCEPTION 'terminal multipart upload cannot transition'; END IF;
  NEW.updated_at := clock_timestamp();
  RETURN NEW;
END $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS multipart_upload_guard ON managed_data.multipart_upload;
CREATE TRIGGER multipart_upload_guard BEFORE UPDATE ON managed_data.multipart_upload FOR EACH ROW EXECUTE FUNCTION managed_data.guard_multipart_upload();
CREATE OR REPLACE FUNCTION managed_data.guard_multipart_upload_insert() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
-- +goose StatementBegin
AS $$
DECLARE st text; expiry timestamptz;
BEGIN
  IF NEW.status <> 'creating' OR NEW.existing OR NEW.object_key <> '' OR NEW.provider_upload_id <> ''
     OR NEW.completion_identity <> '' OR NEW.completion_request_hash <> '' OR NEW.abort_identity <> ''
     OR NEW.completed_at IS NOT NULL OR NEW.aborted_at IS NOT NULL OR NEW.error <> '' THEN
    RAISE EXCEPTION 'multipart upload must begin in canonical creating state';
  END IF;
  NEW.created_at := clock_timestamp();
  NEW.updated_at := NEW.created_at;
  SELECT status, expires_at INTO st, expiry FROM managed_data.upload_session WHERE upload_id=NEW.upload_id;
  IF st IS DISTINCT FROM 'open' OR expiry <= clock_timestamp() THEN RAISE EXCEPTION 'multipart upload requires an open, unexpired upload'; END IF;
  RETURN NEW;
END $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS multipart_upload_insert_guard ON managed_data.multipart_upload;
CREATE TRIGGER multipart_upload_insert_guard BEFORE INSERT ON managed_data.multipart_upload FOR EACH ROW EXECUTE FUNCTION managed_data.guard_multipart_upload_insert();

CREATE OR REPLACE FUNCTION managed_data.guard_retention_root() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
-- +goose StatementBegin
AS $$
BEGIN
  IF NEW.root_id <> OLD.root_id OR NEW.project_id <> OLD.project_id OR NEW.environment <> OLD.environment
     OR NEW.revision_id IS DISTINCT FROM OLD.revision_id
     OR NEW.created_at <> OLD.created_at OR NEW.evidence IS DISTINCT FROM OLD.evidence THEN RAISE EXCEPTION 'retention root identity is immutable'; END IF;
  IF OLD.state='live' AND NEW.state NOT IN ('live','retiring') THEN RAISE EXCEPTION 'invalid retention transition'; END IF;
  IF OLD.state='retiring' AND NEW.state NOT IN ('retiring','expired') THEN RAISE EXCEPTION 'invalid retention transition'; END IF;
  IF OLD.state='expired' AND NEW.state <> 'expired' THEN RAISE EXCEPTION 'expired retention root cannot transition'; END IF;
  NEW.updated_at := clock_timestamp(); RETURN NEW;
END $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS retention_root_guard ON managed_data.retention_root;
CREATE TRIGGER retention_root_guard BEFORE UPDATE ON managed_data.retention_root FOR EACH ROW EXECUTE FUNCTION managed_data.guard_retention_root();
CREATE OR REPLACE FUNCTION managed_data.reject_append_only_update() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
-- +goose StatementBegin
AS $$ BEGIN RAISE EXCEPTION 'append-only evidence cannot be mutated'; END $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS reconciliation_evidence_guard ON managed_data.reconciliation_evidence;
CREATE TRIGGER reconciliation_evidence_guard BEFORE UPDATE OR DELETE ON managed_data.reconciliation_evidence FOR EACH ROW EXECUTE FUNCTION managed_data.reject_append_only_update();

CREATE OR REPLACE FUNCTION managed_data.guard_lease_fence() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
-- +goose StatementBegin
AS $$ BEGIN
  IF NEW.lease_key <> OLD.lease_key OR NEW.acquired_at <> OLD.acquired_at THEN RAISE EXCEPTION 'lease identity is immutable'; END IF;
  IF NEW.fencing_epoch < OLD.fencing_epoch THEN RAISE EXCEPTION 'lease fencing epoch cannot decrease'; END IF;
  IF NEW.fencing_epoch = OLD.fencing_epoch AND NEW.owner_id <> OLD.owner_id THEN RAISE EXCEPTION 'lease owner cannot change without a new fencing epoch'; END IF;
  IF OLD.state='released' AND NEW.state='held' AND NEW.fencing_epoch <= OLD.fencing_epoch THEN RAISE EXCEPTION 'released lease cannot be resurrected without a new fence'; END IF;
  IF OLD.state='held' AND NEW.state NOT IN ('held','released') THEN RAISE EXCEPTION 'invalid lease state transition'; END IF;
  IF NEW.released_at IS DISTINCT FROM OLD.released_at AND NOT ((OLD.state='held' AND NEW.state='released') OR (OLD.state='released' AND NEW.state='held')) THEN RAISE EXCEPTION 'lease release timestamp is immutable'; END IF;
  IF NEW.state='held' AND NEW.released_at IS NOT NULL THEN RAISE EXCEPTION 'held lease cannot have release timestamp'; END IF;
  IF NEW.state='held' AND NEW.expires_at <= clock_timestamp() THEN RAISE EXCEPTION 'held lease expiry must remain in the future'; END IF;
  IF NEW.state='held' AND NEW.expires_at < OLD.expires_at THEN RAISE EXCEPTION 'lease expiry cannot be shortened'; END IF;
  IF NEW.state='held' AND NEW.expires_at > clock_timestamp()+interval '24 hours' THEN RAISE EXCEPTION 'lease expiry exceeds maximum duration'; END IF;
  RETURN NEW;
END $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS lease_fence_guard ON managed_data.lease;
CREATE TRIGGER lease_fence_guard BEFORE UPDATE ON managed_data.lease FOR EACH ROW EXECUTE FUNCTION managed_data.guard_lease_fence();
DROP TRIGGER IF EXISTS multipart_digest_fence_guard ON managed_data.multipart_digest_lease;
CREATE OR REPLACE FUNCTION managed_data.guard_digest_lease_fence() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
-- +goose StatementBegin
AS $$ BEGIN
  IF NEW.sha256 <> OLD.sha256 THEN RAISE EXCEPTION 'digest lease identity is immutable'; END IF;
  IF NEW.fencing_epoch < OLD.fencing_epoch THEN RAISE EXCEPTION 'lease fencing epoch cannot decrease'; END IF;
  IF NEW.fencing_epoch = OLD.fencing_epoch AND NEW.owner_id <> OLD.owner_id THEN RAISE EXCEPTION 'lease owner cannot change without a new fencing epoch'; END IF;
  IF OLD.state='released' AND NEW.state='held' AND NEW.fencing_epoch <= OLD.fencing_epoch THEN RAISE EXCEPTION 'released digest lease cannot be resurrected without a new fence'; END IF;
  IF OLD.state='held' AND NEW.state NOT IN ('held','released') THEN RAISE EXCEPTION 'invalid digest lease state transition'; END IF;
  IF NEW.state='held' AND NEW.lease_until <= clock_timestamp() THEN RAISE EXCEPTION 'held digest lease expiry must remain in the future'; END IF;
  IF NEW.state='held' AND NEW.lease_until < OLD.lease_until AND OLD.lease_until > clock_timestamp() THEN RAISE EXCEPTION 'digest lease expiry cannot be shortened'; END IF;
  IF NEW.state='held' AND NEW.lease_until > clock_timestamp()+interval '24 hours' THEN RAISE EXCEPTION 'digest lease expiry exceeds maximum duration'; END IF;
  RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER multipart_digest_fence_guard BEFORE UPDATE ON managed_data.multipart_digest_lease FOR EACH ROW EXECUTE FUNCTION managed_data.guard_digest_lease_fence();

CREATE OR REPLACE FUNCTION managed_data.reject_immutable_mutation() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
-- +goose StatementBegin
AS $$ BEGIN RAISE EXCEPTION 'managed-data immutable evidence cannot be mutated'; END $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS revision_delete_guard ON managed_data.revision;
CREATE TRIGGER revision_delete_guard BEFORE DELETE ON managed_data.revision FOR EACH ROW EXECUTE FUNCTION managed_data.reject_immutable_mutation();
DROP TRIGGER IF EXISTS revision_immutable ON managed_data.revision;
DROP TRIGGER IF EXISTS binding_set_immutable ON managed_data.binding_set;
CREATE TRIGGER binding_set_immutable BEFORE UPDATE OR DELETE ON managed_data.binding_set FOR EACH ROW EXECUTE FUNCTION managed_data.reject_immutable_mutation();
DROP TRIGGER IF EXISTS binding_immutable ON managed_data.binding;
CREATE TRIGGER binding_immutable BEFORE UPDATE OR DELETE ON managed_data.binding FOR EACH ROW EXECUTE FUNCTION managed_data.reject_immutable_mutation();

-- Only bounded, clock-capped maintenance may delete metadata. Runtime roles
-- receive EXECUTE, never DELETE, on this function.
CREATE OR REPLACE FUNCTION managed_data.prune_upload_sessions(p_before timestamptz, p_limit integer)
-- +goose StatementBegin
RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, managed_data AS $$
DECLARE n bigint := 0; removed bigint; cutoff timestamptz; remaining integer;
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 1000 THEN RAISE EXCEPTION 'invalid prune bounds'; END IF;
    cutoff := LEAST(COALESCE(p_before, clock_timestamp()), clock_timestamp());
    remaining := p_limit;
    PERFORM set_config('managed_data.maintenance','on',true);
    WITH doomed AS (
      SELECT p.multipart_id, p.part_number FROM managed_data.multipart_part p
      JOIN managed_data.multipart_upload m ON m.multipart_id=p.multipart_id
      JOIN managed_data.upload_session s ON s.upload_id=m.upload_id
      WHERE p.updated_at <= cutoff AND m.status IN ('completed','aborted','failed')
        AND s.status IN ('complete','aborted','expired','failed') AND s.cleanup_completed_at IS NOT NULL
      ORDER BY p.updated_at,p.multipart_id,p.part_number FOR UPDATE OF p SKIP LOCKED LIMIT remaining
    )
    DELETE FROM managed_data.multipart_part p USING doomed d WHERE p.multipart_id=d.multipart_id AND p.part_number=d.part_number;
    GET DIAGNOSTICS removed = ROW_COUNT; n := n + removed; remaining := p_limit - n;
    IF remaining > 0 THEN
    WITH doomed AS (
      SELECT m.multipart_id FROM managed_data.multipart_upload m
      JOIN managed_data.upload_session s ON s.upload_id=m.upload_id
      WHERE m.updated_at <= cutoff AND m.status IN ('completed','aborted','failed')
        AND s.status IN ('complete','aborted','expired','failed') AND s.cleanup_completed_at IS NOT NULL
        AND NOT EXISTS (SELECT 1 FROM managed_data.multipart_part p WHERE p.multipart_id=m.multipart_id)
      ORDER BY m.updated_at,m.multipart_id FOR UPDATE OF m SKIP LOCKED LIMIT remaining
    )
    DELETE FROM managed_data.multipart_upload m USING doomed d WHERE m.multipart_id=d.multipart_id;
    GET DIAGNOSTICS removed = ROW_COUNT; n := n + removed; remaining := p_limit - n;
    END IF;
    IF remaining > 0 THEN
    WITH doomed AS (
      SELECT s.upload_id FROM managed_data.upload_session s
      WHERE s.status IN ('aborted','expired','failed') AND s.cleanup_completed_at IS NOT NULL
        AND s.updated_at <= cutoff
        AND NOT EXISTS (SELECT 1 FROM managed_data.multipart_upload m WHERE m.upload_id=s.upload_id)
      ORDER BY s.updated_at,s.upload_id FOR UPDATE SKIP LOCKED LIMIT remaining
    )
    DELETE FROM managed_data.upload_session s USING doomed d WHERE s.upload_id=d.upload_id;
    GET DIAGNOSTICS removed = ROW_COUNT; n := n + removed;
    END IF;
    RETURN n;
END $$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION managed_data.mark_upload_cleanup(p_upload_id text)
-- +goose StatementBegin
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, managed_data AS $$
BEGIN
  -- Cleanup acknowledgement is an operational capability, not a normal
  -- runtime write.  Only the dedicated maintenance role (or the schema
  -- owner/migrator, which already has administrative authority) may mint it.
  IF session_user NOT IN ('leapview_control_maintenance','leapview_control_owner','leapview_control_migrator') THEN
    RAISE EXCEPTION 'cleanup evidence requires the maintenance capability';
  END IF;
  PERFORM set_config('managed_data.maintenance','on',true);
  UPDATE managed_data.upload_session SET cleanup_completed_at=clock_timestamp(),updated_at=clock_timestamp()
    WHERE upload_id=p_upload_id AND status IN ('complete','aborted','expired','failed') AND cleanup_completed_at IS NULL;
  IF FOUND THEN
    RETURN true;
  END IF;
  -- Cleanup acknowledgement is idempotent. A retry after the durable marker
  -- was written must succeed, while unknown or non-terminal uploads still
  -- fail closed.
  RETURN EXISTS (
    SELECT 1 FROM managed_data.upload_session
     WHERE upload_id=p_upload_id
       AND status IN ('complete','aborted','expired','failed')
       AND cleanup_completed_at IS NOT NULL
  );
END $$;
-- +goose StatementEnd

REVOKE ALL ON SCHEMA managed_data FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA managed_data FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA managed_data FROM PUBLIC;
-- +goose StatementBegin
DO $$
DECLARE r text;
BEGIN
  FOREACH r IN ARRAY ARRAY['leapview_control_owner','leapview_control_migrator','leapview_control_maintenance','leapview_control_runtime','leapview_control_readonly','leapview_control_backup'] LOOP
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r) THEN
      EXECUTE format('GRANT USAGE ON SCHEMA managed_data TO %I', r);
      IF r IN ('leapview_control_owner','leapview_control_migrator') THEN
        EXECUTE format('GRANT ALL ON ALL TABLES IN SCHEMA managed_data TO %I', r);
        EXECUTE format('GRANT ALL ON ALL SEQUENCES IN SCHEMA managed_data TO %I', r);
        EXECUTE format('GRANT ALL ON ALL FUNCTIONS IN SCHEMA managed_data TO %I', r);
      ELSIF r = 'leapview_control_runtime' THEN
        EXECUTE 'GRANT SELECT, INSERT, UPDATE ON managed_data.collection, managed_data.upload_session, managed_data.multipart_upload, managed_data.multipart_digest_lease, managed_data.environment_pointer TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT, INSERT ON managed_data.multipart_part TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT, INSERT ON managed_data.revision_file TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT ON managed_data.binding_set, managed_data.binding TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT, INSERT, UPDATE ON managed_data.revision TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT, INSERT, UPDATE ON managed_data.lease, managed_data.retention_root TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT, INSERT ON managed_data.reconciliation_evidence TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT ON managed_data.reachability_epoch TO leapview_control_runtime';
        EXECUTE 'GRANT USAGE ON ALL SEQUENCES IN SCHEMA managed_data TO leapview_control_runtime';
        EXECUTE 'GRANT EXECUTE ON FUNCTION managed_data.publish_binding_set(text,text,text,text,bigint,jsonb) TO leapview_control_runtime';
      ELSIF r = 'leapview_control_maintenance' THEN
        EXECUTE 'GRANT EXECUTE ON FUNCTION managed_data.mark_upload_cleanup(text) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION managed_data.prune_upload_sessions(timestamptz, integer) TO leapview_control_maintenance';
      ELSIF r = 'leapview_control_readonly' THEN
        EXECUTE 'GRANT SELECT ON managed_data.collection, managed_data.revision, managed_data.revision_file, managed_data.upload_session, managed_data.binding_set, managed_data.binding, managed_data.retention_root, managed_data.reconciliation_evidence, managed_data.reachability_epoch TO leapview_control_readonly';
      ELSE
        EXECUTE 'GRANT SELECT ON ALL TABLES IN SCHEMA managed_data TO leapview_control_backup';
      END IF;
    END IF;
  END LOOP;
END $$;
-- +goose StatementEnd

-- capability source: internal/analytics/physicalpool/postgres/schema.sql
-- Clean-slate PostgreSQL physical-pool authority (ADR-0020).
--
-- DuckLake remains authoritative for table and object membership.  This
-- capability stores only the stable, non-secret namespace identity and
-- append-only conformance evidence used to admit a runtime tuple.
CREATE SCHEMA IF NOT EXISTS physical_pool;

CREATE TABLE IF NOT EXISTS physical_pool.physical_pools (
    id                    text PRIMARY KEY,
    identity_digest       text NOT NULL UNIQUE,
    storage_location      text NOT NULL,
    storage_namespace     text NOT NULL,
    storage_implementation text NOT NULL,
    object_naming_contract text NOT NULL,
    region                text NOT NULL DEFAULT '',
    tenant                text NOT NULL DEFAULT '',
    encryption_domain     text NOT NULL,
    isolation_boundary    text NOT NULL,
    encryption_key_ref    text NOT NULL DEFAULT '',
    credential_reference  text NOT NULL DEFAULT '',
    retention_authority   text NOT NULL,
    orphan_grace_period_seconds bigint NOT NULL CHECK (orphan_grace_period_seconds >= 0),
    reader_grace_period_seconds bigint NOT NULL CHECK (reader_grace_period_seconds >= 0),
    build_grace_period_seconds bigint NOT NULL CHECK (build_grace_period_seconds >= 0),
    retention_policy      jsonb NOT NULL,
    created_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (id = identity_digest AND id ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (storage_location = btrim(storage_location) AND octet_length(storage_location) BETWEEN 1 AND 2048),
    CHECK (storage_namespace = btrim(storage_namespace) AND octet_length(storage_namespace) BETWEEN 1 AND 512),
    CHECK (storage_implementation = btrim(storage_implementation) AND octet_length(storage_implementation) BETWEEN 1 AND 128),
    CHECK (object_naming_contract = btrim(object_naming_contract) AND octet_length(object_naming_contract) BETWEEN 1 AND 128),
    CHECK (region = btrim(region) AND octet_length(region) <= 255),
    CHECK (tenant = btrim(tenant) AND octet_length(tenant) <= 255),
    CHECK (encryption_domain = btrim(encryption_domain) AND octet_length(encryption_domain) BETWEEN 1 AND 255),
    CHECK (isolation_boundary = btrim(isolation_boundary) AND octet_length(isolation_boundary) BETWEEN 1 AND 255),
    CHECK (encryption_key_ref = btrim(encryption_key_ref) AND octet_length(encryption_key_ref) <= 512),
    CHECK (credential_reference = btrim(credential_reference) AND octet_length(credential_reference) <= 512),
    CHECK (retention_authority = btrim(retention_authority) AND octet_length(retention_authority) BETWEEN 1 AND 255),
    CHECK (jsonb_typeof(retention_policy) = 'object' AND octet_length(retention_policy::text) BETWEEN 2 AND 4096),
    CHECK (retention_policy = jsonb_build_object('orphan_grace_period_seconds', orphan_grace_period_seconds, 'reader_grace_period_seconds', reader_grace_period_seconds, 'build_grace_period_seconds', build_grace_period_seconds)),
    CHECK (retention_policy - 'orphan_grace_period_seconds' - 'reader_grace_period_seconds' - 'build_grace_period_seconds' = '{}'::jsonb)
);

-- A namespace is deletable only once.  Runtime, extension, or catalog-format
-- upgrades retain this key and append a new admission row instead.
CREATE UNIQUE INDEX IF NOT EXISTS physical_pool_namespace_idx
    ON physical_pool.physical_pools (storage_implementation, storage_location, storage_namespace);

CREATE TABLE IF NOT EXISTS physical_pool.physical_pool_admissions (
    pool_id              text NOT NULL REFERENCES physical_pool.physical_pools(id) ON DELETE CASCADE,
    compatibility_json   jsonb NOT NULL,
    duckdb_runtime       text NOT NULL,
    ducklake_extension   text NOT NULL,
    catalog_format       text NOT NULL,
    storage_implementation text NOT NULL,
    object_naming_contract text NOT NULL,
    evidence_json        jsonb NOT NULL,
    evidence_digest      text NOT NULL,
    compatibility_digest text NOT NULL,
    conformance_version  text NOT NULL,
    admitted_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (pool_id, evidence_digest),
    CHECK (evidence_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (compatibility_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (jsonb_typeof(compatibility_json) = 'object' AND octet_length(compatibility_json::text) BETWEEN 2 AND 4096),
    CHECK (jsonb_typeof(evidence_json) = 'object' AND octet_length(evidence_json::text) BETWEEN 2 AND 32768),
    CHECK (duckdb_runtime = btrim(duckdb_runtime) AND octet_length(duckdb_runtime) BETWEEN 1 AND 255),
    CHECK (ducklake_extension = btrim(ducklake_extension) AND octet_length(ducklake_extension) BETWEEN 1 AND 255),
    CHECK (catalog_format = btrim(catalog_format) AND octet_length(catalog_format) BETWEEN 1 AND 255),
    CHECK (storage_implementation = btrim(storage_implementation) AND octet_length(storage_implementation) BETWEEN 1 AND 128),
    CHECK (object_naming_contract = btrim(object_naming_contract) AND octet_length(object_naming_contract) BETWEEN 1 AND 128),
    CHECK (conformance_version = btrim(conformance_version) AND octet_length(conformance_version) BETWEEN 1 AND 255),
    CHECK (compatibility_json->>'duckdb_runtime' = duckdb_runtime
        AND compatibility_json->>'ducklake_extension' = ducklake_extension
        AND compatibility_json->>'catalog_format' = catalog_format
        AND compatibility_json->>'storage_implementation' = storage_implementation
        AND compatibility_json->>'object_naming_contract' = object_naming_contract),
    CHECK (evidence_json->'compatibility' = compatibility_json
        AND evidence_json->>'digest' = evidence_digest
        AND evidence_json->>'conformance_version' = conformance_version),
    UNIQUE (pool_id, compatibility_digest, evidence_digest)
);

CREATE INDEX IF NOT EXISTS physical_pool_admissions_compatibility_idx
    ON physical_pool.physical_pool_admissions (pool_id, compatibility_digest, admitted_at DESC);

-- This ledger records what the external namespace marker admitted. It is an
-- audit/restart aid only: a database row cannot prove that an object-store
-- marker was actually created, so callers must still invoke the marker's
-- conditional Acquire/Verify operation before deletion authority is granted.
CREATE TABLE IF NOT EXISTS physical_pool.namespace_ownership_claims (
    pool_id              text NOT NULL REFERENCES physical_pool.physical_pools(id) ON DELETE CASCADE,
    compatibility_digest text NOT NULL CHECK (compatibility_digest ~ '^sha256:[0-9a-f]{64}$'),
    evidence_digest      text NOT NULL CHECK (evidence_digest ~ '^sha256:[0-9a-f]{64}$'),
    owner_id             text NOT NULL CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255),
    claimed_at           timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (pool_id, compatibility_digest, evidence_digest)
        REFERENCES physical_pool.physical_pool_admissions(pool_id, compatibility_digest, evidence_digest)
        ON DELETE RESTRICT,
    PRIMARY KEY (pool_id, evidence_digest)
);

CREATE INDEX IF NOT EXISTS physical_pool_ownership_owner_idx
    ON physical_pool.namespace_ownership_claims (pool_id, owner_id, claimed_at DESC);

CREATE TABLE IF NOT EXISTS physical_pool.namespace_deletion_leases (
    singleton       boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    owner_id        text NOT NULL,
    token           uuid NOT NULL,
    expires_at      timestamptz NOT NULL,
    acquired_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255),
    CHECK (expires_at > acquired_at)
);

CREATE OR REPLACE FUNCTION physical_pool.reject_immutable_change()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, physical_pool
-- +goose StatementBegin
AS $$
BEGIN
    RAISE EXCEPTION 'physical-pool identity and admissions are immutable';
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS physical_pools_immutable ON physical_pool.physical_pools;
CREATE TRIGGER physical_pools_immutable
    BEFORE UPDATE OR DELETE ON physical_pool.physical_pools
    FOR EACH ROW EXECUTE FUNCTION physical_pool.reject_immutable_change();

DROP TRIGGER IF EXISTS physical_pool_admissions_immutable ON physical_pool.physical_pool_admissions;
CREATE TRIGGER physical_pool_admissions_immutable
    BEFORE UPDATE OR DELETE ON physical_pool.physical_pool_admissions
    FOR EACH ROW EXECUTE FUNCTION physical_pool.reject_immutable_change();

DROP TRIGGER IF EXISTS physical_pool_ownership_immutable ON physical_pool.namespace_ownership_claims;
CREATE TRIGGER physical_pool_ownership_immutable
    BEFORE UPDATE OR DELETE ON physical_pool.namespace_ownership_claims
    FOR EACH ROW EXECUTE FUNCTION physical_pool.reject_immutable_change();

-- Capability-owned ACLs.  Runtime can reconstruct and verify an admission but
-- cannot forge, replace, or delete one.  Provisioned owner/migrator roles own
-- the objects and retain migration authority; all other roles are explicit.
REVOKE ALL ON SCHEMA physical_pool FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA physical_pool FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA physical_pool FROM PUBLIC;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_migrator') THEN
        GRANT USAGE ON SCHEMA physical_pool TO leapview_control_migrator;
        GRANT SELECT, INSERT ON physical_pool.physical_pools, physical_pool.physical_pool_admissions, physical_pool.namespace_ownership_claims TO leapview_control_migrator;
        GRANT SELECT, INSERT, UPDATE, DELETE ON physical_pool.namespace_deletion_leases TO leapview_control_migrator;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA physical_pool TO leapview_control_runtime;
        GRANT SELECT ON physical_pool.physical_pools, physical_pool.physical_pool_admissions, physical_pool.namespace_ownership_claims, physical_pool.namespace_deletion_leases TO leapview_control_runtime;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON physical_pool.physical_pools, physical_pool.physical_pool_admissions, physical_pool.namespace_ownership_claims FROM leapview_control_runtime;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON physical_pool.namespace_deletion_leases FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA physical_pool TO leapview_control_maintenance;
        GRANT SELECT ON physical_pool.physical_pools, physical_pool.physical_pool_admissions, physical_pool.namespace_ownership_claims TO leapview_control_maintenance;
        GRANT SELECT, INSERT, UPDATE, DELETE ON physical_pool.namespace_deletion_leases TO leapview_control_maintenance;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON physical_pool.physical_pools, physical_pool.physical_pool_admissions, physical_pool.namespace_ownership_claims FROM leapview_control_maintenance;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA physical_pool TO leapview_control_readonly;
        GRANT SELECT ON physical_pool.physical_pools, physical_pool.physical_pool_admissions, physical_pool.namespace_ownership_claims, physical_pool.namespace_deletion_leases TO leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA physical_pool TO leapview_control_backup;
        GRANT SELECT ON physical_pool.physical_pools, physical_pool.physical_pool_admissions, physical_pool.namespace_ownership_claims, physical_pool.namespace_deletion_leases TO leapview_control_backup;
    END IF;
END
$$;
-- +goose StatementEnd

-- capability source: internal/deployment/postgres/schema.sql
-- Canonical PostgreSQL delivery authority (FAI-565).
--
-- This is a capability-owned, clean-slate schema.  It deliberately contains
-- no SQLite compatibility tables and does not duplicate DuckLake metadata.
-- The package is applied as one clean baseline by the delivery capability.

CREATE SCHEMA IF NOT EXISTS delivery;

CREATE TABLE IF NOT EXISTS delivery.delivery_target (
    target_id text PRIMARY KEY,
    project_id text NOT NULL,
    environment text NOT NULL,
    target_revision bigint NOT NULL DEFAULT 1 CHECK (target_revision > 0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (target_id = btrim(target_id) AND octet_length(target_id) BETWEEN 1 AND 255),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255),
    CHECK (environment = btrim(environment) AND octet_length(environment) BETWEEN 1 AND 255),
    UNIQUE (project_id, environment)
);

-- Target-owned fencing counter. Keeping the counter in a separate row avoids
-- conflating serving selection with lease state while preserving one lock
-- scope for epoch allocation.
CREATE TABLE IF NOT EXISTS delivery.delivery_target_fence (
    target_id text PRIMARY KEY REFERENCES delivery.delivery_target(target_id),
    next_fencing_epoch bigint NOT NULL DEFAULT 1 CHECK (next_fencing_epoch > 0)
);

-- Target-owned immutable revision allocators.  Each counter is advanced while
-- the row is locked by the admitting transaction, so plan/candidate/generation
-- revisions are serialized per target without MAX scans or advisory locks.
-- The counters are deliberately independent: a failed admission rolls back
-- only the caller transaction and therefore cannot consume a revision.
CREATE TABLE IF NOT EXISTS delivery.delivery_target_revision (
    target_id text PRIMARY KEY REFERENCES delivery.delivery_target(target_id),
    next_plan_revision bigint NOT NULL DEFAULT 1 CHECK (next_plan_revision > 0),
    next_candidate_revision bigint NOT NULL DEFAULT 1 CHECK (next_candidate_revision > 0),
    next_generation_revision bigint NOT NULL DEFAULT 1 CHECK (next_generation_revision > 0)
);

CREATE OR REPLACE FUNCTION delivery.create_target_revision_row()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO delivery.delivery_target_revision(target_id)
    VALUES (NEW.target_id)
    ON CONFLICT (target_id) DO NOTHING;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS delivery_target_revision_after_insert ON delivery.delivery_target;
CREATE TRIGGER delivery_target_revision_after_insert
AFTER INSERT ON delivery.delivery_target
FOR EACH ROW EXECUTE FUNCTION delivery.create_target_revision_row();

CREATE TABLE IF NOT EXISTS delivery.delivery_plan (
    plan_id uuid PRIMARY KEY,
    target_id text NOT NULL REFERENCES delivery.delivery_target(target_id),
    plan_revision bigint NOT NULL CHECK (plan_revision > 0),
    plan_digest text NOT NULL CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    compiled_graph_digest text NOT NULL CHECK (compiled_graph_digest ~ '^sha256:[0-9a-f]{64}$'),
    compiled_config_digest text NOT NULL CHECK (compiled_config_digest ~ '^sha256:[0-9a-f]{64}$'),
    security_domain_fingerprint text NOT NULL CHECK (security_domain_fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    artifact_digest text NOT NULL CHECK (artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    qualification_digest text NOT NULL CHECK (qualification_digest ~ '^sha256:[0-9a-f]{64}$'),
    qualification_required boolean NOT NULL,
    approval_required boolean NOT NULL,
    approval_policy_revision bigint NOT NULL CHECK (approval_policy_revision > 0),
    -- The complete canonical deployment.DeliveryPlan document is the
    -- execution contract. Digest/evidence columns above remain relational
    -- projections for indexed authority checks, but this document is what a
    -- native build rehydrates when it executes a persisted plan.
    plan_document jsonb NOT NULL
        CHECK (jsonb_typeof(plan_document) = 'object' AND octet_length(plan_document::text) <= 1048576),
    evidence jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 65536),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (target_id, plan_revision),
    UNIQUE (plan_id, target_id)
);

-- The candidate table is the admission record. It stores no mutable serving
-- pointer; only a qualified candidate can be used to create a generation.
CREATE TABLE IF NOT EXISTS delivery.delivery_candidate (
    candidate_id uuid PRIMARY KEY,
    target_id text NOT NULL REFERENCES delivery.delivery_target(target_id),
    plan_id uuid NOT NULL REFERENCES delivery.delivery_plan(plan_id),
    snapshot_seal_id uuid,
    status text NOT NULL DEFAULT 'building' CHECK (status = btrim(status) AND octet_length(status) BETWEEN 1 AND 32 AND status IN ('building','qualified','ready','admitted','rejected','retired')),
    candidate_revision bigint NOT NULL CHECK (candidate_revision > 0),
    artifact_digest text NOT NULL CHECK (artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    qualification_digest text CHECK (qualification_digest IS NULL OR qualification_digest ~ '^sha256:[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    qualified_at timestamptz,
    retired_at timestamptz,
    UNIQUE (target_id, candidate_revision),
    UNIQUE (candidate_id, plan_id),
    UNIQUE (candidate_id, target_id, plan_id),
    UNIQUE (candidate_id, target_id, snapshot_seal_id),
    FOREIGN KEY (plan_id, target_id) REFERENCES delivery.delivery_plan(plan_id, target_id),
    CHECK ((status IN ('building','ready') AND snapshot_seal_id IS NULL AND qualification_digest IS NULL AND qualified_at IS NULL)
        OR (status IN ('qualified','admitted') AND snapshot_seal_id IS NOT NULL AND qualification_digest IS NOT NULL AND qualified_at IS NOT NULL)
        OR (status = 'rejected')
        OR (status = 'retired' AND retired_at IS NOT NULL)),
    CHECK (retired_at IS NULL OR status = 'retired')
);

CREATE TABLE IF NOT EXISTS delivery.delivery_build_attempt (
    attempt_id uuid PRIMARY KEY,
    plan_id uuid NOT NULL REFERENCES delivery.delivery_plan(plan_id),
    candidate_id uuid REFERENCES delivery.delivery_candidate(candidate_id),
    owner_id text NOT NULL,
    physical_pool_id text NOT NULL CHECK (physical_pool_id = btrim(physical_pool_id) AND octet_length(physical_pool_id) BETWEEN 1 AND 255),
    -- Physical catalog identity is retained on canonical delivery attempts so
    -- DuckLake maintenance can fence exactly the running writer without a
    -- second attempt lifecycle ledger.
    catalog_id text NOT NULL CHECK (catalog_id = btrim(catalog_id) AND octet_length(catalog_id) BETWEEN 1 AND 255),
    fencing_epoch bigint NOT NULL CHECK (fencing_epoch > 0),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    plan_digest text NOT NULL CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    state text NOT NULL CHECK (state = btrim(state) AND octet_length(state) BETWEEN 1 AND 32 AND state IN ('running','committed','aborted','indeterminate','fenced')),
    namespace text NOT NULL CHECK (namespace = btrim(namespace) AND octet_length(namespace) BETWEEN 1 AND 512),
    lease_expires_at timestamptz NOT NULL,
    session_identity text NOT NULL CHECK (session_identity = btrim(session_identity) AND octet_length(session_identity) BETWEEN 1 AND 512),
    snapshot_id bigint CHECK (snapshot_id IS NULL OR snapshot_id > 0),
    commit_marker jsonb,
    termination_evidence jsonb,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at timestamptz,
    CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255),
    CHECK (commit_marker IS NULL OR (jsonb_typeof(commit_marker) = 'object' AND octet_length(commit_marker::text) <= 4096)),
    CHECK (termination_evidence IS NULL OR (jsonb_typeof(termination_evidence) = 'object' AND octet_length(termination_evidence::text) <= 32768)),
    CHECK ((state = 'running' AND finished_at IS NULL) OR (state <> 'running' AND finished_at IS NOT NULL)),
    CHECK ((state = 'running' AND snapshot_id IS NULL AND commit_marker IS NULL AND termination_evidence IS NULL)
        OR (state = 'committed' AND snapshot_id IS NOT NULL AND commit_marker IS NOT NULL AND termination_evidence IS NULL)
        OR (state IN ('aborted','indeterminate','fenced') AND snapshot_id IS NULL AND commit_marker IS NULL AND termination_evidence IS NOT NULL)),
    UNIQUE (attempt_id, candidate_id),
    UNIQUE (attempt_id, physical_pool_id, catalog_id),
    FOREIGN KEY (candidate_id, plan_id) REFERENCES delivery.delivery_candidate(candidate_id, plan_id)
);

-- A successor is admitted only from an explicitly reconciled predecessor.
-- Keeping the edge in its own immutable table means the predecessor attempt
-- row remains append-only after it is fenced, while normal commit/reconcile
-- transitions can reject late writes by checking this edge.
CREATE TABLE IF NOT EXISTS delivery.delivery_build_attempt_successor (
    predecessor_attempt_id uuid PRIMARY KEY REFERENCES delivery.delivery_build_attempt(attempt_id) ON DELETE RESTRICT,
    successor_attempt_id uuid NOT NULL UNIQUE REFERENCES delivery.delivery_build_attempt(attempt_id) ON DELETE RESTRICT,
    resolution_evidence jsonb NOT NULL
        CHECK (jsonb_typeof(resolution_evidence) = 'object' AND octet_length(resolution_evidence::text) <= 32768),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (predecessor_attempt_id <> successor_attempt_id)
);

-- A build artifact binding is the immutable hand-off from a durable build
-- attempt to the serving-state artifact that was produced by that attempt.
-- The attempt UUID is the sole identity: a retry may replay the exact row,
-- but can never replace its artifact or serving-state identity.
CREATE TABLE IF NOT EXISTS delivery.delivery_build_artifact_binding (
    attempt_id uuid PRIMARY KEY REFERENCES delivery.delivery_build_attempt(attempt_id) ON DELETE RESTRICT,
    serving_artifact_id text NOT NULL CHECK (serving_artifact_id = btrim(serving_artifact_id) AND octet_length(serving_artifact_id) BETWEEN 1 AND 255 AND serving_artifact_id ~ '^[A-Za-z0-9._:/-]+$'),
    serving_artifact_digest text NOT NULL CHECK (serving_artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    serving_state_id text NOT NULL CHECK (serving_state_id = btrim(serving_state_id) AND octet_length(serving_state_id) BETWEEN 1 AND 255 AND serving_state_id ~ '^[A-Za-z0-9._:/-]+$'),
    bound_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE IF NOT EXISTS delivery.delivery_snapshot_seal (
    seal_id uuid PRIMARY KEY,
    attempt_id uuid NOT NULL REFERENCES delivery.delivery_build_attempt(attempt_id),
    candidate_id uuid REFERENCES delivery.delivery_candidate(candidate_id),
    physical_pool_id text NOT NULL CHECK (physical_pool_id = btrim(physical_pool_id) AND octet_length(physical_pool_id) BETWEEN 1 AND 255),
    tenant_domain text NOT NULL CHECK (tenant_domain = btrim(tenant_domain) AND octet_length(tenant_domain) BETWEEN 1 AND 255),
    region text NOT NULL CHECK (region = btrim(region) AND octet_length(region) BETWEEN 1 AND 128),
    encryption_domain text NOT NULL CHECK (encryption_domain = btrim(encryption_domain) AND octet_length(encryption_domain) BETWEEN 1 AND 255),
    object_namespace text NOT NULL CHECK (object_namespace = btrim(object_namespace) AND octet_length(object_namespace) BETWEEN 1 AND 255),
    catalog_database text NOT NULL CHECK (catalog_database = btrim(catalog_database) AND octet_length(catalog_database) BETWEEN 1 AND 255),
    catalog_id text NOT NULL CHECK (catalog_id = btrim(catalog_id) AND octet_length(catalog_id) BETWEEN 1 AND 255),
    catalog_uuid text NOT NULL CHECK (catalog_uuid = btrim(catalog_uuid) AND octet_length(catalog_uuid) BETWEEN 1 AND 255),
    catalog_version bigint NOT NULL CHECK (catalog_version > 0),
    ducklake_snapshot_id bigint NOT NULL CHECK (ducklake_snapshot_id > 0),
    relation_namespace text NOT NULL CHECK (relation_namespace = btrim(relation_namespace) AND octet_length(relation_namespace) BETWEEN 1 AND 512),
    relation_manifest_digest text NOT NULL CHECK (relation_manifest_digest ~ '^sha256:[0-9a-f]{64}$'),
    closure_digest text NOT NULL CHECK (closure_digest ~ '^sha256:[0-9a-f]{64}$'),
    object_root text NOT NULL CHECK (object_root = btrim(object_root) AND octet_length(object_root) BETWEEN 1 AND 512),
    object_root_digest text NOT NULL CHECK (object_root_digest ~ '^sha256:[0-9a-f]{64}$'),
    artifact_root text NOT NULL CHECK (artifact_root = btrim(artifact_root) AND octet_length(artifact_root) BETWEEN 1 AND 512),
    artifact_root_digest text NOT NULL CHECK (artifact_root_digest ~ '^sha256:[0-9a-f]{64}$'),
    compiled_graph_digest text NOT NULL CHECK (compiled_graph_digest ~ '^sha256:[0-9a-f]{64}$'),
    compiled_config_digest text NOT NULL CHECK (compiled_config_digest ~ '^sha256:[0-9a-f]{64}$'),
    security_domain_fingerprint text NOT NULL CHECK (security_domain_fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    plan_digest text NOT NULL CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    compatibility_digest text NOT NULL CHECK (compatibility_digest ~ '^sha256:[0-9a-f]{64}$'),
    serving_artifact_id text NOT NULL CHECK (serving_artifact_id = btrim(serving_artifact_id) AND octet_length(serving_artifact_id) BETWEEN 1 AND 255),
    serving_artifact_digest text NOT NULL CHECK (serving_artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    duckdb_version text NOT NULL CHECK (duckdb_version = btrim(duckdb_version) AND octet_length(duckdb_version) BETWEEN 1 AND 128),
    runtime_version text NOT NULL CHECK (runtime_version = btrim(runtime_version) AND octet_length(runtime_version) BETWEEN 1 AND 128),
    ducklake_extension_version text NOT NULL CHECK (ducklake_extension_version = btrim(ducklake_extension_version) AND octet_length(ducklake_extension_version) BETWEEN 1 AND 128),
    ducklake_spec_version text NOT NULL CHECK (ducklake_spec_version = btrim(ducklake_spec_version) AND octet_length(ducklake_spec_version) BETWEEN 1 AND 128),
    catalog_schema_version text NOT NULL CHECK (catalog_schema_version = btrim(catalog_schema_version) AND octet_length(catalog_schema_version) BETWEEN 1 AND 128),
    qualification_evidence jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(qualification_evidence) = 'object' AND octet_length(qualification_evidence::text) <= 32768),
    qualified_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (attempt_id),
    UNIQUE (seal_id, candidate_id),
    UNIQUE (physical_pool_id, catalog_id, catalog_database, catalog_uuid, ducklake_snapshot_id),
    FOREIGN KEY (attempt_id, physical_pool_id, catalog_id)
        REFERENCES delivery.delivery_build_attempt(attempt_id, physical_pool_id, catalog_id),
    FOREIGN KEY (attempt_id, candidate_id) REFERENCES delivery.delivery_build_attempt(attempt_id, candidate_id)
);

-- Candidate and seal reference one another during the lifecycle.  Install the
-- nullable candidate->seal edge after both tables exist, preserving the clean
-- baseline while avoiding a circular CREATE TABLE dependency.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conname = 'delivery_candidate_snapshot_seal_fk'
           AND conrelid = 'delivery.delivery_candidate'::regclass
    ) THEN
        ALTER TABLE delivery.delivery_candidate
            ADD CONSTRAINT delivery_candidate_snapshot_seal_fk
            FOREIGN KEY (snapshot_seal_id, candidate_id)
                REFERENCES delivery.delivery_snapshot_seal(seal_id, candidate_id);
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS delivery.delivery_generation (
    generation_id uuid PRIMARY KEY,
    target_id text NOT NULL REFERENCES delivery.delivery_target(target_id),
    candidate_id uuid NOT NULL REFERENCES delivery.delivery_candidate(candidate_id),
    snapshot_seal_id uuid NOT NULL REFERENCES delivery.delivery_snapshot_seal(seal_id),
    plan_id uuid NOT NULL REFERENCES delivery.delivery_plan(plan_id),
    plan_digest text NOT NULL CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    artifact_root text NOT NULL CHECK (artifact_root = btrim(artifact_root) AND octet_length(artifact_root) BETWEEN 1 AND 512),
    artifact_root_digest text NOT NULL CHECK (artifact_root_digest ~ '^sha256:[0-9a-f]{64}$'),
    serving_artifact_digest text NOT NULL CHECK (serving_artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    compiled_graph_digest text NOT NULL CHECK (compiled_graph_digest ~ '^sha256:[0-9a-f]{64}$'),
    compiled_config_digest text NOT NULL CHECK (compiled_config_digest ~ '^sha256:[0-9a-f]{64}$'),
    security_domain_fingerprint text NOT NULL CHECK (security_domain_fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    generation_revision bigint NOT NULL CHECK (generation_revision > 0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (target_id, generation_revision),
    UNIQUE (generation_id, target_id, candidate_id, snapshot_seal_id),
    FOREIGN KEY (candidate_id, target_id, plan_id) REFERENCES delivery.delivery_candidate(candidate_id, target_id, plan_id),
    FOREIGN KEY (snapshot_seal_id, candidate_id) REFERENCES delivery.delivery_snapshot_seal(seal_id, candidate_id)
);

-- Explicit-revision APIs remain supported.  Keep allocator counters ahead of
-- any explicitly supplied revision so a later allocated admission cannot
-- collide with legacy/caller-assigned rows.
CREATE OR REPLACE FUNCTION delivery.advance_target_revision_counter()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_ARGV[0] = 'plan' THEN
        UPDATE delivery.delivery_target_revision
           SET next_plan_revision = GREATEST(next_plan_revision, NEW.plan_revision + 1)
         WHERE target_id = NEW.target_id;
    ELSIF TG_ARGV[0] = 'candidate' THEN
        UPDATE delivery.delivery_target_revision
           SET next_candidate_revision = GREATEST(next_candidate_revision, NEW.candidate_revision + 1)
         WHERE target_id = NEW.target_id;
    ELSIF TG_ARGV[0] = 'generation' THEN
        UPDATE delivery.delivery_target_revision
           SET next_generation_revision = GREATEST(next_generation_revision, NEW.generation_revision + 1)
         WHERE target_id = NEW.target_id;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS delivery_plan_revision_counter ON delivery.delivery_plan;
CREATE TRIGGER delivery_plan_revision_counter
AFTER INSERT ON delivery.delivery_plan
FOR EACH ROW EXECUTE FUNCTION delivery.advance_target_revision_counter('plan');

DROP TRIGGER IF EXISTS delivery_candidate_revision_counter ON delivery.delivery_candidate;
CREATE TRIGGER delivery_candidate_revision_counter
AFTER INSERT ON delivery.delivery_candidate
FOR EACH ROW EXECUTE FUNCTION delivery.advance_target_revision_counter('candidate');

DROP TRIGGER IF EXISTS delivery_generation_revision_counter ON delivery.delivery_generation;
CREATE TRIGGER delivery_generation_revision_counter
AFTER INSERT ON delivery.delivery_generation
FOR EACH ROW EXECUTE FUNCTION delivery.advance_target_revision_counter('generation');

CREATE TABLE IF NOT EXISTS delivery.delivery_publication (
    publication_id uuid PRIMARY KEY,
    target_id text NOT NULL REFERENCES delivery.delivery_target(target_id),
    generation_id uuid NOT NULL,
    expected_base_generation_id uuid,
    candidate_id uuid NOT NULL REFERENCES delivery.delivery_candidate(candidate_id),
    snapshot_seal_id uuid NOT NULL REFERENCES delivery.delivery_snapshot_seal(seal_id),
    expected_target_revision bigint NOT NULL CHECK (expected_target_revision > 0),
    result_target_revision bigint,
    actor_id text NOT NULL CHECK (actor_id = btrim(actor_id) AND octet_length(actor_id) BETWEEN 1 AND 255),
    state text NOT NULL CHECK (state = btrim(state) AND octet_length(state) BETWEEN 1 AND 32 AND state IN ('pending','committed','rejected','indeterminate')),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    committed_at timestamptz,
    CHECK ((state = 'pending' AND result_target_revision IS NULL AND committed_at IS NULL)
        OR (state = 'committed' AND result_target_revision IS NOT NULL AND committed_at IS NOT NULL)
        OR (state IN ('rejected','indeterminate') AND result_target_revision IS NULL AND committed_at IS NULL)),
    FOREIGN KEY (generation_id) REFERENCES delivery.delivery_generation(generation_id),
    FOREIGN KEY (expected_base_generation_id) REFERENCES delivery.delivery_generation(generation_id),
    FOREIGN KEY (generation_id, target_id, candidate_id, snapshot_seal_id) REFERENCES delivery.delivery_generation(generation_id, target_id, candidate_id, snapshot_seal_id),
    FOREIGN KEY (candidate_id, target_id, snapshot_seal_id) REFERENCES delivery.delivery_candidate(candidate_id, target_id, snapshot_seal_id),
    FOREIGN KEY (snapshot_seal_id, candidate_id) REFERENCES delivery.delivery_snapshot_seal(seal_id, candidate_id),
    UNIQUE (target_id, request_digest)
);

CREATE TABLE IF NOT EXISTS delivery.delivery_active_pointer (
    target_id text PRIMARY KEY REFERENCES delivery.delivery_target(target_id),
    generation_id uuid NOT NULL REFERENCES delivery.delivery_generation(generation_id),
    publication_id uuid NOT NULL REFERENCES delivery.delivery_publication(publication_id),
    changed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

-- A pointer carries only serving selection.  This deferred consistency check
-- keeps the selected publication/generation pair bound to one target and one
-- qualified candidate without duplicating candidate or seal columns here.

-- Native publication approval authority.  Approval requests are immutable
-- evidence for one exact pending publication; decisions (including
-- revocations) are append-only child rows. There is deliberately no candidate-
-- scoped approval projection in the clean-slate schema.
CREATE TABLE IF NOT EXISTS delivery.delivery_approval_request (
    request_id uuid PRIMARY KEY,
    publication_id uuid NOT NULL REFERENCES delivery.delivery_publication(publication_id) ON DELETE RESTRICT,
    target_id text NOT NULL CHECK (target_id = btrim(target_id) AND octet_length(target_id) BETWEEN 1 AND 255),
    candidate_id uuid NOT NULL,
    generation_id uuid NOT NULL,
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    expected_target_revision bigint NOT NULL CHECK (expected_target_revision > 0),
    policy_revision bigint NOT NULL CHECK (policy_revision > 0),
    requested_by text NOT NULL CHECK (requested_by = btrim(requested_by) AND octet_length(requested_by) BETWEEN 1 AND 255),
    request_credential_class text NOT NULL CHECK (request_credential_class IN ('human','workload','api_token','session')),
    request_credential_id text NOT NULL CHECK (request_credential_id = btrim(request_credential_id) AND octet_length(request_credential_id) BETWEEN 1 AND 255),
    request_credential_expires_at timestamptz NOT NULL,
    requested_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    operation_id uuid NOT NULL,
    event_id uuid NOT NULL,
    audit_id uuid NOT NULL,
    evidence jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 32768),
    CHECK (request_credential_expires_at > requested_at),
    CHECK (expires_at > requested_at AND expires_at <= requested_at + interval '24 hours'),
    UNIQUE (publication_id)
);

-- Per-request allocator for append-only decision revisions. The row is the
-- sole mutable counter; decisions themselves remain immutable history.
CREATE TABLE IF NOT EXISTS delivery.delivery_approval_revision (
    request_id uuid PRIMARY KEY REFERENCES delivery.delivery_approval_request(request_id) ON DELETE RESTRICT,
    next_revision bigint NOT NULL DEFAULT 1 CHECK (next_revision > 0)
);

CREATE TABLE IF NOT EXISTS delivery.delivery_approval_decision (
    decision_id uuid PRIMARY KEY,
    request_id uuid NOT NULL REFERENCES delivery.delivery_approval_request(request_id) ON DELETE RESTRICT,
    decision_revision bigint NOT NULL CHECK (decision_revision > 0),
    decision text NOT NULL CHECK (decision IN ('approved','denied','revoked')),
    decided_by text NOT NULL CHECK (decided_by = btrim(decided_by) AND octet_length(decided_by) BETWEEN 1 AND 255),
    decision_credential_class text NOT NULL CHECK (decision_credential_class IN ('human','workload','api_token','session')),
    decision_credential_id text NOT NULL CHECK (decision_credential_id = btrim(decision_credential_id) AND octet_length(decision_credential_id) BETWEEN 1 AND 255),
    decision_credential_expires_at timestamptz NOT NULL,
    decided_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    operation_id uuid NOT NULL,
    event_id uuid NOT NULL,
    audit_id uuid NOT NULL,
    evidence jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 32768),
    UNIQUE (request_id, decision_revision)
);

CREATE TABLE IF NOT EXISTS delivery.delivery_lease (
    lease_id uuid PRIMARY KEY,
    target_id text NOT NULL REFERENCES delivery.delivery_target(target_id),
    owner_id text NOT NULL CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255),
    fencing_epoch bigint NOT NULL CHECK (fencing_epoch > 0),
    state text NOT NULL DEFAULT 'active' CHECK (state = btrim(state) AND octet_length(state) BETWEEN 1 AND 32 AND state IN ('active','released','expired')),
    expires_at timestamptz NOT NULL,
    acquired_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    released_at timestamptz,
    UNIQUE (target_id, fencing_epoch),
    CHECK (expires_at > acquired_at),
    CHECK ((state = 'active' AND released_at IS NULL)
        OR (state IN ('released','expired') AND released_at IS NOT NULL))
);

CREATE TABLE IF NOT EXISTS delivery.delivery_retention_root (
    root_id uuid PRIMARY KEY,
    target_id text NOT NULL REFERENCES delivery.delivery_target(target_id),
    candidate_id uuid REFERENCES delivery.delivery_candidate(candidate_id),
    generation_id uuid REFERENCES delivery.delivery_generation(generation_id),
    snapshot_seal_id uuid REFERENCES delivery.delivery_snapshot_seal(seal_id),
    root_kind text NOT NULL CHECK (root_kind = btrim(root_kind) AND octet_length(root_kind) BETWEEN 1 AND 32 AND root_kind IN ('candidate','generation','rollback','recovery','query')),
    state text NOT NULL CHECK (state = btrim(state) AND octet_length(state) BETWEEN 1 AND 32 AND state IN ('live','retiring','expired')),
    expires_at timestamptz,
    evidence jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 16384),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    retired_at timestamptz,
    expired_at timestamptz,
    CHECK ((state = 'live' AND retired_at IS NULL AND expired_at IS NULL)
        OR (state = 'retiring' AND retired_at IS NOT NULL AND expired_at IS NULL)
        OR (state = 'expired' AND expired_at IS NOT NULL)),
    CHECK ((root_kind IN ('candidate','generation')
            AND candidate_id IS NOT NULL AND generation_id IS NOT NULL AND snapshot_seal_id IS NOT NULL)
        OR (root_kind NOT IN ('candidate','generation'))),
    FOREIGN KEY (generation_id, target_id, candidate_id, snapshot_seal_id)
        REFERENCES delivery.delivery_generation(generation_id, target_id, candidate_id, snapshot_seal_id),
    FOREIGN KEY (candidate_id, target_id, snapshot_seal_id)
        REFERENCES delivery.delivery_candidate(candidate_id, target_id, snapshot_seal_id)
);

-- One live generation root is the canonical reader-admission authority for a
-- generation. Historical roots remain immutable while retiring/expired, and
-- a later rollback activation may establish a fresh live root for the same
-- generation without reviving history.
CREATE UNIQUE INDEX IF NOT EXISTS delivery_one_live_generation_root_idx
    ON delivery.delivery_retention_root(generation_id)
    WHERE root_kind = 'generation' AND state = 'live';

-- Delivery-root admission must serialize with physical-retention retirement,
-- but runtime roles intentionally cannot lock or mutate the DuckLake ledger
-- directly. This narrow capability takes the physical row lock and returns
-- whether the seal still has an exact live retention record.
CREATE OR REPLACE FUNCTION delivery.lock_live_snapshot_retention(p_snapshot_seal_id uuid)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, delivery, ducklake
-- +goose StatementBegin
AS $$
DECLARE
    snapshot_state text;
BEGIN
    SELECT retention.state
      INTO snapshot_state
      FROM ducklake.snapshot_retention AS retention
      JOIN delivery.delivery_snapshot_seal AS seal
        ON seal.physical_pool_id = retention.physical_pool_id
       AND seal.catalog_id = retention.catalog_id
       AND seal.ducklake_snapshot_id = retention.snapshot_id
     WHERE seal.seal_id = p_snapshot_seal_id
     FOR UPDATE OF retention;
    RETURN FOUND AND snapshot_state = 'live';
END;
$$;
-- +goose StatementEnd

-- Recovery retention is an operator mutation, but the maintenance role is
-- intentionally denied direct INSERT on delivery_retention_root.  Keep this
-- narrow capability function SECURITY DEFINER and require the exact
-- target/generation/seal tuple so a recovery hold cannot be redirected to a
-- different physical snapshot.  Replays are checked by the repository after
-- this function returns; the function itself never mutates an existing row.
CREATE OR REPLACE FUNCTION delivery.create_recovery_retention_root(
    p_root_id uuid,
    p_target_id text,
    p_generation_id uuid,
    p_snapshot_seal_id uuid,
    p_expires_at timestamptz,
    p_evidence jsonb
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, delivery
-- +goose StatementBegin
AS $$
DECLARE
    tuple_candidate_id uuid;
    v_recovery_set_id uuid;
    v_frontier_digest text;
    existing_root delivery.delivery_retention_root;
BEGIN
    IF p_root_id IS NULL OR p_target_id IS NULL OR p_target_id <> btrim(p_target_id) OR btrim(p_target_id) = '' THEN
        RAISE EXCEPTION 'recovery retention root identity is required';
    END IF;
    IF p_generation_id IS NULL OR p_snapshot_seal_id IS NULL THEN
        RAISE EXCEPTION 'recovery retention root requires generation and snapshot seal';
    END IF;
    IF p_expires_at IS NULL OR p_expires_at <= clock_timestamp() THEN
        RAISE EXCEPTION 'recovery retention root expiry must be in the future';
    END IF;
    IF jsonb_typeof(p_evidence) <> 'object'
       OR (SELECT count(*) FROM jsonb_object_keys(p_evidence)) <> 2
       OR jsonb_typeof(p_evidence->'recovery_set_id') <> 'string'
       OR jsonb_typeof(p_evidence->'frontier_digest') <> 'string'
       OR btrim(p_evidence->>'recovery_set_id') = ''
       OR btrim(p_evidence->>'frontier_digest') = ''
       OR p_evidence->>'recovery_set_id' !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
       OR p_evidence->>'frontier_digest' !~ '^sha256:[0-9a-f]{64}$' THEN
        RAISE EXCEPTION 'recovery retention root evidence is incomplete';
    END IF;
    v_recovery_set_id := (p_evidence->>'recovery_set_id')::uuid;
    v_frontier_digest := p_evidence->>'frontier_digest';
    IF v_recovery_set_id::text <> p_evidence->>'recovery_set_id' THEN
        RAISE EXCEPTION 'recovery retention root set id is not canonical';
    END IF;
    SELECT generation.candidate_id
      INTO tuple_candidate_id
      FROM delivery.delivery_generation AS generation
     WHERE generation.generation_id = p_generation_id
       AND generation.target_id = p_target_id
       AND generation.snapshot_seal_id = p_snapshot_seal_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'recovery retention root target/generation/seal tuple is unknown';
    END IF;

    -- Require and lock the physical DuckLake retention row before
    -- creating/replaying the root. Recovery evidence must never synthesize
    -- retention authority that was absent from the admitted snapshot.
    IF NOT delivery.lock_live_snapshot_retention(p_snapshot_seal_id) THEN
        RAISE EXCEPTION 'recovery retention root snapshot retention is not live';
    END IF;

    SELECT * INTO existing_root
      FROM delivery.delivery_retention_root
     WHERE root_id = p_root_id
     FOR UPDATE;
    IF FOUND THEN
        -- Replays remain valid after a prepared set is published or
        -- superseded, but still require the exact immutable set identity.
        IF NOT EXISTS (
            SELECT 1
              FROM recovery.recovery_set AS selected
             WHERE selected.set_id = v_recovery_set_id
               AND selected.target_id = p_target_id
               AND selected.generation_id = p_generation_id
               AND selected.snapshot_seal_id = p_snapshot_seal_id
               AND selected.frontier_digest = v_frontier_digest
               AND selected.status IN ('prepared', 'published', 'superseded')
        ) THEN
            RETURN false;
        END IF;
        RETURN existing_root.root_kind = 'recovery'
           AND existing_root.state = 'live'
           AND existing_root.target_id = p_target_id
           AND existing_root.candidate_id = tuple_candidate_id
           AND existing_root.generation_id = p_generation_id
           AND existing_root.snapshot_seal_id = p_snapshot_seal_id
           AND existing_root.expires_at IS NOT DISTINCT FROM p_expires_at
           AND existing_root.evidence = p_evidence;
    END IF;

    -- A new root may only be created while the exact frontier is prepared.
    -- Once inserted, the root itself is immutable and replays above remain
    -- valid across later publication lifecycle transitions.
    IF NOT EXISTS (
        SELECT 1
          FROM recovery.recovery_set AS selected
         WHERE selected.set_id = v_recovery_set_id
           AND selected.target_id = p_target_id
           AND selected.generation_id = p_generation_id
           AND selected.snapshot_seal_id = p_snapshot_seal_id
           AND selected.frontier_digest = v_frontier_digest
           AND selected.status = 'prepared'
    ) THEN
        RAISE EXCEPTION 'recovery retention root evidence does not match a prepared recovery set';
    END IF;

    INSERT INTO delivery.delivery_retention_root(
        root_id, target_id, candidate_id, generation_id, snapshot_seal_id,
        root_kind, state, expires_at, evidence
    ) VALUES (
        p_root_id, p_target_id, tuple_candidate_id, p_generation_id,
        p_snapshot_seal_id, 'recovery', 'live', p_expires_at, p_evidence
    ) ON CONFLICT(root_id) DO NOTHING;
    SELECT * INTO existing_root
      FROM delivery.delivery_retention_root
     WHERE root_id = p_root_id
     FOR UPDATE;
    RETURN FOUND
       AND existing_root.root_kind = 'recovery'
       AND existing_root.state = 'live'
       AND existing_root.target_id = p_target_id
       AND existing_root.candidate_id = tuple_candidate_id
       AND existing_root.generation_id = p_generation_id
       AND existing_root.snapshot_seal_id = p_snapshot_seal_id
       AND existing_root.expires_at IS NOT DISTINCT FROM p_expires_at
       AND existing_root.evidence = p_evidence;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION delivery.reject_authority_history_mutation()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'delivery authority history is immutable';
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION delivery.reject_target_identity_mutation()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF current_user = 'leapview_control_runtime' THEN
        RAISE EXCEPTION 'delivery target mutation requires the activation capability';
    END IF;
    IF TG_OP = 'DELETE' OR NEW.target_id <> OLD.target_id OR NEW.project_id <> OLD.project_id
       OR NEW.environment <> OLD.environment THEN
        RAISE EXCEPTION 'delivery target identity is immutable';
    END IF;
    IF NEW.target_revision < OLD.target_revision THEN
        RAISE EXCEPTION 'delivery target revision cannot move backwards';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION delivery.reject_fence_counter_mutation()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' OR NEW.target_id <> OLD.target_id OR NEW.next_fencing_epoch < OLD.next_fencing_epoch THEN
        RAISE EXCEPTION 'delivery fencing counter is monotonic and owned by the authority';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION delivery.reject_target_revision_mutation()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' OR NEW.target_id <> OLD.target_id
       OR NEW.next_plan_revision < OLD.next_plan_revision
       OR NEW.next_candidate_revision < OLD.next_candidate_revision
       OR NEW.next_generation_revision < OLD.next_generation_revision THEN
        RAISE EXCEPTION 'delivery target revision counters are monotonic and immutable';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION delivery.reject_attempt_identity_mutation()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' OR NEW.attempt_id <> OLD.attempt_id OR NEW.plan_id <> OLD.plan_id
       OR NEW.candidate_id IS DISTINCT FROM OLD.candidate_id
       OR NEW.owner_id <> OLD.owner_id OR NEW.physical_pool_id <> OLD.physical_pool_id OR NEW.catalog_id <> OLD.catalog_id OR NEW.fencing_epoch <> OLD.fencing_epoch
       OR NEW.request_digest <> OLD.request_digest OR NEW.plan_digest <> OLD.plan_digest
       OR NEW.namespace <> OLD.namespace OR NEW.session_identity <> OLD.session_identity
       OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'delivery build attempt identity is immutable';
    END IF;
    IF OLD.state <> 'running' AND NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at THEN
        RAISE EXCEPTION 'terminal build attempt lease expiry is immutable';
    ELSIF OLD.state = 'running' AND NEW.state = 'running'
          AND NEW.lease_expires_at < OLD.lease_expires_at THEN
        RAISE EXCEPTION 'running build attempt lease expiry cannot move backwards';
    ELSIF OLD.state = 'running' AND NEW.state <> 'running'
          AND NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at THEN
        RAISE EXCEPTION 'terminal build attempt lease expiry is immutable';
    END IF;
    IF OLD.state = 'indeterminate' AND NEW.state NOT IN ('indeterminate','committed','aborted') THEN
        RAISE EXCEPTION 'indeterminate build attempt may only be reconciled to committed or aborted';
    END IF;
    IF OLD.state NOT IN ('running','indeterminate') AND (NEW.state <> OLD.state OR NEW.snapshot_id IS DISTINCT FROM OLD.snapshot_id
       OR NEW.commit_marker IS DISTINCT FROM OLD.commit_marker OR NEW.termination_evidence IS DISTINCT FROM OLD.termination_evidence
       OR NEW.finished_at IS DISTINCT FROM OLD.finished_at OR NEW.updated_at <> OLD.updated_at) THEN
        RAISE EXCEPTION 'terminal build attempt evidence is immutable';
    END IF;
    IF OLD.state IN ('running','indeterminate') AND NEW.state = OLD.state
       AND (NEW.snapshot_id IS DISTINCT FROM OLD.snapshot_id
            OR NEW.commit_marker IS DISTINCT FROM OLD.commit_marker
            OR NEW.termination_evidence IS DISTINCT FROM OLD.termination_evidence
            OR NEW.finished_at IS DISTINCT FROM OLD.finished_at) THEN
        RAISE EXCEPTION 'running build attempt evidence is immutable';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION delivery.reject_build_artifact_binding_mutation()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'delivery build artifact binding is immutable';
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION delivery.reject_build_attempt_successor_mutation()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'delivery build attempt successor link is immutable';
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS delivery_build_artifact_binding_immutable ON delivery.delivery_build_artifact_binding;
CREATE TRIGGER delivery_build_artifact_binding_immutable
BEFORE UPDATE OR DELETE ON delivery.delivery_build_artifact_binding
FOR EACH ROW EXECUTE FUNCTION delivery.reject_build_artifact_binding_mutation();

DROP TRIGGER IF EXISTS delivery_build_attempt_successor_immutable ON delivery.delivery_build_attempt_successor;
CREATE TRIGGER delivery_build_attempt_successor_immutable
BEFORE UPDATE OR DELETE ON delivery.delivery_build_attempt_successor
FOR EACH ROW EXECUTE FUNCTION delivery.reject_build_attempt_successor_mutation();

CREATE OR REPLACE FUNCTION delivery.reject_publication_mutation()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF current_user = 'leapview_control_runtime'
       AND (TG_OP = 'DELETE' OR NEW.state = 'committed'
            OR NEW.result_target_revision IS DISTINCT FROM OLD.result_target_revision
            OR NEW.committed_at IS DISTINCT FROM OLD.committed_at) THEN
        RAISE EXCEPTION 'delivery publication commit requires the activation capability';
    END IF;
    IF TG_OP = 'DELETE'
       OR NEW.publication_id <> OLD.publication_id
       OR NEW.target_id <> OLD.target_id
       OR NEW.generation_id <> OLD.generation_id
       OR NEW.candidate_id <> OLD.candidate_id
       OR NEW.snapshot_seal_id <> OLD.snapshot_seal_id
       OR NEW.expected_target_revision <> OLD.expected_target_revision
       OR NEW.actor_id <> OLD.actor_id
       OR NEW.request_digest <> OLD.request_digest
       OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'delivery publication identity is immutable';
    END IF;
    IF OLD.state <> 'pending' THEN
        IF NEW.state <> OLD.state OR NEW.result_target_revision IS DISTINCT FROM OLD.result_target_revision
           OR NEW.committed_at IS DISTINCT FROM OLD.committed_at THEN
            RAISE EXCEPTION 'terminal publication is immutable';
        END IF;
    ELSIF NEW.state NOT IN ('pending','committed','rejected','indeterminate') THEN
        RAISE EXCEPTION 'invalid publication transition';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION delivery.reject_candidate_mutation()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' OR NEW.candidate_id <> OLD.candidate_id OR NEW.target_id <> OLD.target_id
       OR NEW.plan_id <> OLD.plan_id OR NEW.candidate_revision <> OLD.candidate_revision
       OR NEW.artifact_digest <> OLD.artifact_digest OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'delivery candidate identity is immutable';
    END IF;
    IF OLD.status = 'building' AND NEW.status NOT IN ('building','ready','qualified','rejected') THEN
        RAISE EXCEPTION 'invalid candidate transition';
    ELSIF OLD.status = 'ready' AND NEW.status NOT IN ('ready','qualified','rejected') THEN
        RAISE EXCEPTION 'invalid candidate transition';
    ELSIF OLD.status = 'qualified' AND NEW.status NOT IN ('qualified','admitted','rejected','retired') THEN
        RAISE EXCEPTION 'invalid candidate transition';
    ELSIF OLD.status = 'admitted' AND NEW.status NOT IN ('admitted','retired') THEN
        RAISE EXCEPTION 'invalid candidate transition';
    ELSIF OLD.status = 'rejected' AND NEW.status NOT IN ('rejected','retired') THEN
        RAISE EXCEPTION 'invalid candidate transition';
    ELSIF OLD.status = 'retired' AND NEW.status <> 'retired' THEN
        RAISE EXCEPTION 'invalid candidate transition';
    END IF;
    IF OLD.status NOT IN ('building','ready') AND (NEW.snapshot_seal_id IS DISTINCT FROM OLD.snapshot_seal_id
       OR NEW.qualification_digest IS DISTINCT FROM OLD.qualification_digest
       OR NEW.qualified_at IS DISTINCT FROM OLD.qualified_at
       OR NEW.retired_at IS DISTINCT FROM OLD.retired_at) THEN
        RAISE EXCEPTION 'candidate qualification evidence is immutable';
    END IF;
    IF OLD.status IN ('building','ready') AND NEW.status <> 'qualified'
       AND (NEW.snapshot_seal_id IS DISTINCT FROM OLD.snapshot_seal_id
            OR NEW.qualification_digest IS DISTINCT FROM OLD.qualification_digest
            OR NEW.qualified_at IS DISTINCT FROM OLD.qualified_at) THEN
        RAISE EXCEPTION 'candidate qualification evidence requires qualification transition';
    END IF;
    IF OLD.retired_at IS NOT NULL AND NEW.retired_at IS DISTINCT FROM OLD.retired_at THEN
        RAISE EXCEPTION 'candidate retirement timestamp is immutable';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION delivery.guard_approval_request_insert()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    publication delivery.delivery_publication%ROWTYPE;
    durable_policy_revision bigint;
    now_ts timestamptz := clock_timestamp();
BEGIN
    SELECT * INTO STRICT publication
      FROM delivery.delivery_publication
     WHERE publication_id = NEW.publication_id
     FOR UPDATE;
    IF publication.state <> 'pending'
       OR publication.target_id <> NEW.target_id
       OR publication.generation_id <> NEW.generation_id
       OR publication.candidate_id <> NEW.candidate_id
       OR publication.request_digest <> NEW.request_digest
       OR publication.expected_target_revision <> NEW.expected_target_revision THEN
        RAISE EXCEPTION 'approval request must bind the exact pending publication';
    END IF;
    SELECT plan.approval_policy_revision
      INTO STRICT durable_policy_revision
      FROM delivery.delivery_generation g
      JOIN delivery.delivery_plan plan ON plan.plan_id = g.plan_id
     WHERE g.generation_id = publication.generation_id;
    IF NEW.policy_revision <> durable_policy_revision THEN
        RAISE EXCEPTION 'approval request policy revision differs from durable plan';
    END IF;
    IF NEW.expires_at <= now_ts OR NEW.expires_at > now_ts + interval '24 hours'
       OR NEW.expires_at > NEW.request_credential_expires_at THEN
        RAISE EXCEPTION 'approval request expiry is outside the database-clock window';
    END IF;
    IF NEW.request_credential_expires_at <= now_ts THEN
        RAISE EXCEPTION 'approval request credential is expired';
    END IF;
    IF NEW.requested_at IS DISTINCT FROM now_ts THEN
        NEW.requested_at := now_ts;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION delivery.create_approval_revision_row()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO delivery.delivery_approval_revision(request_id)
    VALUES (NEW.request_id)
    ON CONFLICT (request_id) DO NOTHING;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION delivery.guard_approval_decision_insert()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    request delivery.delivery_approval_request%ROWTYPE;
    publication_state text;
    now_ts timestamptz := clock_timestamp();
BEGIN
    SELECT p.state INTO STRICT publication_state
      FROM delivery.delivery_publication p
      JOIN delivery.delivery_approval_request r ON r.publication_id = p.publication_id
     WHERE r.request_id = NEW.request_id
     FOR UPDATE OF p;
    IF publication_state <> 'pending' THEN
        RAISE EXCEPTION 'approval decision requires a pending publication';
    END IF;
    SELECT * INTO STRICT request
      FROM delivery.delivery_approval_request
     WHERE request_id = NEW.request_id
     FOR UPDATE;
    IF request.expires_at <= now_ts OR NEW.decided_at > request.expires_at THEN
        RAISE EXCEPTION 'approval request is expired';
    END IF;
    IF NEW.decision IN ('approved','denied') AND NEW.decided_by = request.requested_by THEN
        RAISE EXCEPTION 'approval separation of duty violated';
    END IF;
    IF NEW.decision_credential_expires_at <= now_ts THEN
        RAISE EXCEPTION 'approval decision credential is expired';
    END IF;
    IF NEW.decided_at IS DISTINCT FROM now_ts THEN
        NEW.decided_at := now_ts;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION delivery.reject_approval_request_mutation()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'approval request identity is immutable';
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION delivery.reject_approval_decision_mutation()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'approval decision evidence is immutable';
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION delivery.guard_approval_revision_mutation()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' OR NEW.request_id <> OLD.request_id OR NEW.next_revision <> OLD.next_revision + 1 THEN
        RAISE EXCEPTION 'approval decision revision allocator is monotonic';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION delivery.check_active_pointer_consistency()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    pub_target text;
    pub_generation uuid;
    pub_candidate uuid;
    pub_seal uuid;
    gen_target text;
    gen_candidate uuid;
    gen_seal uuid;
    candidate_target text;
BEGIN
    SELECT target_id,generation_id,candidate_id,snapshot_seal_id
      INTO pub_target,pub_generation,pub_candidate,pub_seal
      FROM delivery.delivery_publication
     WHERE publication_id=NEW.publication_id;
    IF NOT FOUND OR pub_target <> NEW.target_id OR pub_generation <> NEW.generation_id THEN
        RAISE EXCEPTION 'active pointer publication identity differs';
    END IF;
    SELECT target_id,candidate_id,snapshot_seal_id
      INTO gen_target,gen_candidate,gen_seal
      FROM delivery.delivery_generation
     WHERE generation_id=NEW.generation_id;
    IF NOT FOUND OR gen_target <> NEW.target_id OR gen_candidate <> pub_candidate OR gen_seal <> pub_seal THEN
        RAISE EXCEPTION 'active pointer generation identity differs';
    END IF;
    SELECT target_id INTO candidate_target FROM delivery.delivery_candidate WHERE candidate_id=pub_candidate;
    IF NOT FOUND OR candidate_target <> NEW.target_id THEN
        RAISE EXCEPTION 'active pointer candidate identity differs';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- Runtime activation may not mutate serving selection directly. This
-- role-aware trigger remains effective even if a deployment accidentally
-- grants a broad table UPDATE privilege: SECURITY DEFINER activation runs
-- with the owner as current_user, while forged runtime writes are rejected.
CREATE OR REPLACE FUNCTION delivery.reject_runtime_active_pointer_mutation()
RETURNS trigger LANGUAGE plpgsql
-- +goose StatementBegin
AS $$
BEGIN
    IF current_user = 'leapview_control_runtime' THEN
        RAISE EXCEPTION 'delivery active pointer mutation requires the activation capability';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- The guarded repository path calls this narrow SECURITY DEFINER transition
-- after verifying lease, seal, approval, lineage, candidate, and retention-
-- root evidence. It performs the three mutable serving writes as one
-- database-owned transition and rechecks the immutable tuple, CAS revision,
-- and expected predecessor. Retrying an advanced tuple is rejected; Activate
-- handles a committed retry through its evidence replay path.
CREATE OR REPLACE FUNCTION delivery.commit_activation_transition(
    p_publication_id uuid,
    p_target_id text,
    p_generation_id uuid,
    p_expected_target_revision bigint,
    p_result_target_revision bigint
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, delivery
-- +goose StatementBegin
AS $$
DECLARE
    publication_target text;
    publication_generation uuid;
    publication_base_generation uuid;
    publication_expected_revision bigint;
    publication_state text;
    publication_candidate uuid;
    publication_seal uuid;
    target_revision bigint;
    target_active_generation uuid;
    generation_target text;
    generation_candidate uuid;
    generation_seal uuid;
    candidate_target text;
    candidate_status text;
    candidate_seal uuid;
    updated bigint;
BEGIN
    IF p_publication_id IS NULL OR p_target_id IS NULL OR p_target_id <> btrim(p_target_id)
       OR btrim(p_target_id) = '' OR p_generation_id IS NULL
       OR p_expected_target_revision IS NULL OR p_expected_target_revision <= 0
       OR p_result_target_revision IS NULL
       OR p_result_target_revision <> p_expected_target_revision + 1 THEN
        RAISE EXCEPTION 'activation transition identity or revision is invalid';
    END IF;

    SELECT publication.target_id, publication.generation_id,
           publication.expected_base_generation_id,
           publication.expected_target_revision, publication.state,
           publication.candidate_id, publication.snapshot_seal_id
      INTO publication_target, publication_generation,
           publication_base_generation, publication_expected_revision,
           publication_state, publication_candidate, publication_seal
      FROM delivery.delivery_publication AS publication
     WHERE publication.publication_id = p_publication_id
     FOR UPDATE;
    IF NOT FOUND OR publication_target <> p_target_id
       OR publication_generation <> p_generation_id
       OR publication_expected_revision <> p_expected_target_revision
       OR publication_state <> 'pending' THEN
        RAISE EXCEPTION 'activation publication tuple is not pending or differs';
    END IF;

    SELECT target.target_revision,
           (SELECT pointer.generation_id
              FROM delivery.delivery_active_pointer AS pointer
             WHERE pointer.target_id = target.target_id)
      INTO target_revision, target_active_generation
      FROM delivery.delivery_target AS target
     WHERE target.target_id = p_target_id
     FOR UPDATE;
    IF NOT FOUND OR target_revision <> p_expected_target_revision
       OR target_active_generation IS DISTINCT FROM publication_base_generation THEN
        RAISE EXCEPTION 'activation target CAS or predecessor differs';
    END IF;

    SELECT generation.target_id, generation.candidate_id,
           generation.snapshot_seal_id
      INTO generation_target, generation_candidate, generation_seal
      FROM delivery.delivery_generation AS generation
     WHERE generation.generation_id = p_generation_id;
    IF NOT FOUND OR generation_target <> p_target_id
       OR generation_candidate <> publication_candidate
       OR generation_seal <> publication_seal THEN
        RAISE EXCEPTION 'activation generation tuple differs';
    END IF;
    SELECT candidate.target_id, candidate.status, candidate.snapshot_seal_id
      INTO candidate_target, candidate_status, candidate_seal
      FROM delivery.delivery_candidate AS candidate
     WHERE candidate.candidate_id = publication_candidate;
    IF NOT FOUND OR candidate_target <> p_target_id
       OR candidate_seal <> publication_seal
       OR candidate_status NOT IN ('qualified', 'ready', 'admitted') THEN
        RAISE EXCEPTION 'activation candidate tuple is not qualified';
    END IF;

    UPDATE delivery.delivery_target AS target
       SET target_revision = p_result_target_revision,
           updated_at = clock_timestamp()
     WHERE target.target_id = p_target_id
       AND target.target_revision = p_expected_target_revision;
    GET DIAGNOSTICS updated = ROW_COUNT;
    IF updated <> 1 THEN
        RAISE EXCEPTION 'activation target revision CAS lost';
    END IF;

    INSERT INTO delivery.delivery_active_pointer(target_id, generation_id, publication_id)
    VALUES (p_target_id, p_generation_id, p_publication_id)
    ON CONFLICT (target_id) DO UPDATE
          SET generation_id = EXCLUDED.generation_id,
              publication_id = EXCLUDED.publication_id,
              changed_at = clock_timestamp();

    UPDATE delivery.delivery_publication
       SET state = 'committed',
           result_target_revision = p_result_target_revision,
           committed_at = clock_timestamp()
     WHERE publication_id = p_publication_id AND state = 'pending';
    GET DIAGNOSTICS updated = ROW_COUNT;
    IF updated <> 1 THEN
        RAISE EXCEPTION 'activation publication commit CAS lost';
    END IF;
    RETURN true;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION delivery.reject_lease_mutation()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' OR NEW.lease_id <> OLD.lease_id OR NEW.target_id <> OLD.target_id
       OR NEW.owner_id <> OLD.owner_id OR NEW.fencing_epoch <> OLD.fencing_epoch
       OR NEW.acquired_at <> OLD.acquired_at THEN
        RAISE EXCEPTION 'delivery lease identity is immutable';
    END IF;
    IF OLD.state <> 'active' AND (NEW.state <> OLD.state OR NEW.expires_at <> OLD.expires_at
       OR NEW.released_at IS DISTINCT FROM OLD.released_at) THEN
        RAISE EXCEPTION 'terminal lease is immutable';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION delivery.reject_root_mutation()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' OR NEW.root_id <> OLD.root_id OR NEW.target_id <> OLD.target_id
       OR NEW.candidate_id IS DISTINCT FROM OLD.candidate_id
       OR NEW.generation_id IS DISTINCT FROM OLD.generation_id
       OR NEW.snapshot_seal_id IS DISTINCT FROM OLD.snapshot_seal_id
       OR NEW.root_kind <> OLD.root_kind OR NEW.created_at <> OLD.created_at
       OR NEW.evidence IS DISTINCT FROM OLD.evidence THEN
        RAISE EXCEPTION 'delivery retention root identity is immutable';
    END IF;
    IF OLD.state = 'live' AND NEW.state NOT IN ('live','retiring') THEN
        RAISE EXCEPTION 'delivery retention root lifecycle is monotonic';
    ELSIF OLD.state = 'retiring' AND NEW.state NOT IN ('retiring','expired') THEN
        RAISE EXCEPTION 'delivery retention root lifecycle is monotonic';
    ELSIF OLD.state = 'expired' AND NEW.state <> 'expired' THEN
        RAISE EXCEPTION 'delivery retention root lifecycle is monotonic';
    END IF;
    IF NEW.state = 'live' AND (NEW.retired_at IS NOT NULL OR NEW.expired_at IS NOT NULL) THEN
        RAISE EXCEPTION 'live retention root cannot have terminal timestamps';
    ELSIF NEW.state = 'retiring' AND (NEW.retired_at IS NULL OR NEW.expired_at IS NOT NULL) THEN
        RAISE EXCEPTION 'retiring retention root requires retirement timestamp only';
    ELSIF NEW.state = 'expired' AND NEW.expired_at IS NULL THEN
        RAISE EXCEPTION 'expired retention root requires expiry timestamp';
    END IF;
    IF OLD.state IN ('retiring','expired') AND NEW.retired_at IS DISTINCT FROM OLD.retired_at THEN
        RAISE EXCEPTION 'retention root retirement timestamp is immutable';
    END IF;
    IF OLD.state = 'expired' AND NEW.expired_at IS DISTINCT FROM OLD.expired_at THEN
        RAISE EXCEPTION 'retention root expiry timestamp is immutable';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- Identity reads that participate in retention-root lifecycle transitions use
-- this narrow SECURITY DEFINER capability.  Serving roles intentionally lack
-- direct UPDATE/DELETE (and therefore PostgreSQL row-lock privileges), while
-- this function keeps the root locked until the caller's transaction commits.
CREATE OR REPLACE FUNCTION delivery.lock_retention_root(p_root_id uuid)
RETURNS TABLE (
    target_id text,
    candidate_id text,
    generation_id text,
    snapshot_seal_id text,
    root_kind text,
    state text,
    expires_at timestamptz
)
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, delivery
-- +goose StatementBegin
AS $$
    SELECT r.target_id,
           COALESCE(r.candidate_id::text, ''),
           COALESCE(r.generation_id::text, ''),
           COALESCE(r.snapshot_seal_id::text, ''),
           r.root_kind,
           r.state,
           r.expires_at
      FROM delivery.delivery_retention_root AS r
     WHERE r.root_id = p_root_id
     FOR UPDATE;
$$;
-- +goose StatementEnd

-- Retention-root lifecycle entry points.  Roots are capability-owned reach-
-- ability records: callers may only advance them live -> retiring -> expired.
-- Each function locks the root before checking state.  Serving-state reader
-- lease admission takes a FOR SHARE lock on the same root, so retirement's
-- FOR UPDATE lock closes the race in which a reader could be admitted after
-- the root has been selected for retirement.
CREATE OR REPLACE FUNCTION delivery.retire_retention_root(p_root_id uuid)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, delivery
-- +goose StatementBegin
AS $$
DECLARE
    root_state text;
    root_kind text;
    root_target_id text;
    root_candidate_id uuid;
    root_generation_id uuid;
    root_snapshot_seal_id uuid;
    root_expires_at timestamptz;
BEGIN
    SELECT r.state, r.root_kind, r.target_id, r.candidate_id, r.generation_id, r.snapshot_seal_id, r.expires_at
      INTO root_state, root_kind, root_target_id, root_candidate_id, root_generation_id, root_snapshot_seal_id, root_expires_at
      FROM delivery.delivery_retention_root AS r
     WHERE r.root_id = p_root_id
     FOR UPDATE;
    IF NOT FOUND THEN
        RETURN false;
    END IF;
    IF root_kind NOT IN ('candidate', 'generation', 'rollback', 'recovery', 'query') THEN
        RAISE EXCEPTION 'unsupported retention root kind %', root_kind;
    END IF;
    -- The runtime capability is intentionally narrow: a generation root that
    -- is still selected by the active pointer cannot be retired by an
    -- arbitrary root-id call. Activation updates the pointer before invoking
    -- this function while retaining the root lock, so predecessor retirement
    -- remains atomic without exposing a live active root to readers.
    IF root_kind = 'generation'
       AND EXISTS (
           SELECT 1
             FROM delivery.delivery_active_pointer active
            WHERE active.target_id = root_target_id
              AND active.generation_id = root_generation_id
       ) THEN
        RAISE EXCEPTION 'cannot retire the active generation retention root';
    END IF;
    -- Candidate roots may be retired only when activation has made their
    -- generation live, or when their explicit DB-owned governance deadline
    -- has elapsed. This prevents a caller that knows only a candidate UUID
    -- from dropping preview reachability early.
    IF root_kind IN ('candidate', 'recovery', 'query')
       AND NOT (
           (root_kind = 'candidate' AND EXISTS (
               SELECT 1
                 FROM delivery.delivery_active_pointer active
                WHERE active.target_id = root_target_id
                  AND active.generation_id = root_generation_id
           ))
           OR (root_expires_at IS NOT NULL AND root_expires_at <= clock_timestamp())
       ) THEN
        RAISE EXCEPTION 'retention root lacks activation or expired deadline evidence';
    END IF;
    -- Rollback roots are keyed by publication ID. Retirement is valid only
    -- after that exact publication reaches a terminal state and all immutable
    -- tuple evidence agrees with the root row.
    IF root_kind = 'rollback'
       AND NOT EXISTS (
           SELECT 1
             FROM delivery.delivery_publication publication
            WHERE publication.publication_id = p_root_id
              AND publication.target_id = root_target_id
              AND publication.candidate_id = root_candidate_id
              AND publication.generation_id = root_generation_id
              AND publication.snapshot_seal_id = root_snapshot_seal_id
              AND publication.state IN ('committed', 'rejected', 'indeterminate')
       ) THEN
        RAISE EXCEPTION 'rollback retention root lacks terminal publication evidence';
    END IF;
    -- Replaying retirement is a successful no-op after the same evidence
    -- checks above. Terminal roots cannot be moved backwards or re-retired.
    IF root_state = 'retiring' THEN
        RETURN true;
    ELSIF root_state <> 'live' THEN
        RETURN false;
    END IF;
    UPDATE delivery.delivery_retention_root
       SET state = 'retiring', retired_at = clock_timestamp()
     WHERE root_id = p_root_id AND state = 'live';
    RETURN FOUND;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION delivery.expire_retention_root(
    p_root_id uuid,
    p_grace interval DEFAULT interval '0 seconds'
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, delivery
-- +goose StatementBegin
AS $$
DECLARE
    root_state text;
    root_retired_at timestamptz;
    root_expires_at timestamptz;
    root_generation_id uuid;
    root_target_id text;
    root_candidate_id uuid;
    root_snapshot_seal_id uuid;
    db_now timestamptz;
    expected_snapshot bigint;
BEGIN
    IF p_grace IS NULL OR p_grace < interval '0 seconds' THEN
        RAISE EXCEPTION 'retention root expiry grace must be non-negative';
    END IF;
    -- Lock the root before inspecting reader leases.  Reader admission takes
    -- a share lock on this row, therefore a concurrent admission either wins
    -- before this lock (and is observed below) or is rejected after retirement.
    SELECT state, retired_at, expires_at, target_id, candidate_id, generation_id, snapshot_seal_id
      INTO root_state, root_retired_at, root_expires_at, root_target_id, root_candidate_id, root_generation_id, root_snapshot_seal_id
      FROM delivery.delivery_retention_root
     WHERE root_id = p_root_id
     FOR UPDATE;
    IF NOT FOUND THEN
        RETURN false;
    END IF;
    IF root_state = 'expired' THEN
        RETURN true;
    END IF;
    IF root_state <> 'retiring' OR root_retired_at IS NULL THEN
        RETURN false;
    END IF;
    db_now := clock_timestamp();
    -- A root's explicit expiry (for example, a candidate governance deadline)
    -- remains authoritative.  Retirement grace is evaluated from the DB
    -- retirement timestamp, never from an application/node clock.
    IF db_now < root_retired_at + p_grace
       OR (root_expires_at IS NOT NULL AND db_now < root_expires_at) THEN
        RETURN false;
    END IF;
    -- A stale/malformed retiring root must never make the currently active
    -- generation collectible. Reactivation is the exception: its fresh live
    -- generation root remains the canonical admission guard while this older
    -- immutable root is allowed to expire.
    IF root_generation_id IS NOT NULL
       AND EXISTS (
           SELECT 1 FROM delivery.delivery_active_pointer active
            WHERE active.generation_id = root_generation_id
       )
       AND NOT EXISTS (
           SELECT 1
             FROM delivery.delivery_retention_root active_root
            WHERE active_root.root_id <> p_root_id
              AND active_root.target_id = root_target_id
              AND active_root.candidate_id = root_candidate_id
              AND active_root.generation_id = root_generation_id
              AND active_root.snapshot_seal_id = root_snapshot_seal_id
              AND active_root.root_kind = 'generation'
              AND active_root.state = 'live'
              AND (active_root.expires_at IS NULL OR active_root.expires_at > db_now)
       ) THEN
        RETURN false;
    END IF;
    -- Only exact serving-state leases rooted at this generation/snapshot can
    -- delay expiry.  Expired leases no longer represent readers even if the
    -- maintenance marker has not yet been written.
    IF root_generation_id IS NOT NULL THEN
        SELECT s.ducklake_snapshot_id
          INTO expected_snapshot
          FROM delivery.delivery_generation g
          JOIN delivery.delivery_snapshot_seal s ON s.seal_id = g.snapshot_seal_id
         WHERE g.generation_id = root_generation_id
           AND (root_snapshot_seal_id IS NULL OR s.seal_id = root_snapshot_seal_id);
        IF expected_snapshot IS NULL THEN
            -- Corrupt/missing generation evidence fails closed.
            RETURN false;
        END IF;
        IF EXISTS (
            SELECT 1
              FROM serving_state.reader_lease l
             WHERE l.generation_id = root_generation_id
               AND l.ducklake_snapshot_id = expected_snapshot
               AND l.released_at IS NULL
               AND l.expires_at > db_now
        ) THEN
            RETURN false;
        END IF;
    END IF;
    UPDATE delivery.delivery_retention_root
       SET state = 'expired', expired_at = clock_timestamp()
     WHERE root_id = p_root_id AND state = 'retiring';
    RETURN FOUND;
END;
$$;
-- +goose StatementEnd

-- One bounded maintenance pass first retires candidate/recovery roots whose
-- explicit governance deadline has elapsed, then expires ready retiring roots. The
-- singular expiry function remains the final authority and rechecks reader
-- leases, active-generation protection, grace, and immutable seal identity
-- under the root lock. SKIP LOCKED lets parallel workers make progress
-- without waiting on activation or reader admission transactions.
CREATE OR REPLACE FUNCTION delivery.maintain_retention_roots(
    p_physical_pool_id text,
    p_catalog_id text,
    p_grace interval,
    p_limit integer
)
RETURNS TABLE(retired bigint, expired bigint)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, delivery
-- +goose StatementBegin
AS $$
DECLARE
    candidate record;
    db_now timestamptz;
BEGIN
    IF p_physical_pool_id IS NULL OR p_physical_pool_id <> btrim(p_physical_pool_id)
       OR octet_length(p_physical_pool_id) NOT BETWEEN 1 AND 255
       OR p_catalog_id IS NULL OR p_catalog_id <> btrim(p_catalog_id)
       OR octet_length(p_catalog_id) NOT BETWEEN 1 AND 255 THEN
        RAISE EXCEPTION 'retention root maintenance pool/catalog identity is invalid';
    END IF;
    IF p_grace IS NULL OR p_grace < interval '0 seconds' THEN
        RAISE EXCEPTION 'retention root maintenance grace must be non-negative';
    END IF;
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 1000 THEN
        RAISE EXCEPTION 'retention root maintenance limit must be between 1 and 1000';
    END IF;
    retired := 0;
    expired := 0;
    db_now := clock_timestamp();

    FOR candidate IN
        SELECT root.root_id
          FROM delivery.delivery_retention_root root
         WHERE root.root_kind IN ('candidate', 'recovery')
           AND root.state = 'live'
           AND root.expires_at IS NOT NULL
           AND root.expires_at <= db_now
           AND EXISTS (
               SELECT 1 FROM delivery.delivery_snapshot_seal seal
                WHERE seal.seal_id = root.snapshot_seal_id
                  AND seal.physical_pool_id = p_physical_pool_id
                  AND seal.catalog_id = p_catalog_id
           )
         ORDER BY root.expires_at, root.root_id
         FOR UPDATE SKIP LOCKED
         LIMIT p_limit
    LOOP
        IF delivery.retire_retention_root(candidate.root_id) THEN
            retired := retired + 1;
        END IF;
    END LOOP;

    -- Retirement timestamps use clock_timestamp(), so refresh the DB clock
    -- before evaluating zero-grace roots retired by this same bounded pass.
    db_now := clock_timestamp();
    FOR candidate IN
        SELECT root.root_id
          FROM delivery.delivery_retention_root root
         WHERE root.state = 'retiring'
           AND root.retired_at + p_grace <= db_now
           AND (root.expires_at IS NULL OR root.expires_at <= db_now)
           AND EXISTS (
               SELECT 1 FROM delivery.delivery_snapshot_seal scoped_seal
                WHERE scoped_seal.seal_id = root.snapshot_seal_id
                  AND scoped_seal.physical_pool_id = p_physical_pool_id
                  AND scoped_seal.catalog_id = p_catalog_id
           )
           AND (
               root.generation_id IS NULL
               OR (
                   EXISTS (
                       SELECT 1
                         FROM delivery.delivery_generation g
                         JOIN delivery.delivery_snapshot_seal seal
                           ON seal.seal_id = g.snapshot_seal_id
                        WHERE g.generation_id = root.generation_id
                          AND (root.snapshot_seal_id IS NULL OR root.snapshot_seal_id = seal.seal_id)
                   )
                   AND NOT EXISTS (
                       SELECT 1
                         FROM serving_state.reader_lease lease
                         JOIN delivery.delivery_generation g
                           ON g.generation_id = lease.generation_id
                         JOIN delivery.delivery_snapshot_seal seal
                           ON seal.seal_id = g.snapshot_seal_id
                        WHERE g.generation_id = root.generation_id
                          AND (root.snapshot_seal_id IS NULL OR root.snapshot_seal_id = seal.seal_id)
                          AND lease.ducklake_snapshot_id = seal.ducklake_snapshot_id
                          AND lease.released_at IS NULL
                          AND lease.expires_at > db_now
                   )
                   AND (
                       NOT EXISTS (
                           SELECT 1 FROM delivery.delivery_active_pointer active
                            WHERE active.generation_id = root.generation_id
                       )
                       OR EXISTS (
                           SELECT 1
                             FROM delivery.delivery_retention_root active_root
                            WHERE active_root.root_id <> root.root_id
                              AND active_root.target_id = root.target_id
                              AND active_root.candidate_id = root.candidate_id
                              AND active_root.generation_id = root.generation_id
                              AND active_root.snapshot_seal_id = root.snapshot_seal_id
                              AND active_root.root_kind = 'generation'
                              AND active_root.state = 'live'
                              AND (active_root.expires_at IS NULL OR active_root.expires_at > db_now)
                       )
                   )
               )
           )
         ORDER BY root.retired_at, root.root_id
         FOR UPDATE SKIP LOCKED
         LIMIT p_limit
    LOOP
        IF delivery.expire_retention_root(candidate.root_id, p_grace) THEN
            expired := expired + 1;
        END IF;
    END LOOP;
    RETURN NEXT;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS delivery_target_identity_immutable ON delivery.delivery_target;
CREATE TRIGGER delivery_target_identity_immutable BEFORE UPDATE OR DELETE ON delivery.delivery_target
    FOR EACH ROW EXECUTE FUNCTION delivery.reject_target_identity_mutation();
DROP TRIGGER IF EXISTS delivery_fence_counter_monotonic ON delivery.delivery_target_fence;
CREATE TRIGGER delivery_fence_counter_monotonic BEFORE UPDATE OR DELETE ON delivery.delivery_target_fence
    FOR EACH ROW EXECUTE FUNCTION delivery.reject_fence_counter_mutation();
DROP TRIGGER IF EXISTS delivery_target_revision_monotonic ON delivery.delivery_target_revision;
CREATE TRIGGER delivery_target_revision_monotonic BEFORE UPDATE OR DELETE ON delivery.delivery_target_revision
    FOR EACH ROW EXECUTE FUNCTION delivery.reject_target_revision_mutation();
DROP TRIGGER IF EXISTS delivery_plan_history_immutable ON delivery.delivery_plan;
CREATE TRIGGER delivery_plan_history_immutable BEFORE UPDATE OR DELETE ON delivery.delivery_plan
    FOR EACH ROW EXECUTE FUNCTION delivery.reject_authority_history_mutation();
DROP TRIGGER IF EXISTS delivery_seal_history_immutable ON delivery.delivery_snapshot_seal;
CREATE TRIGGER delivery_seal_history_immutable BEFORE UPDATE OR DELETE ON delivery.delivery_snapshot_seal
    FOR EACH ROW EXECUTE FUNCTION delivery.reject_authority_history_mutation();
DROP TRIGGER IF EXISTS delivery_generation_history_immutable ON delivery.delivery_generation;
CREATE TRIGGER delivery_generation_history_immutable BEFORE UPDATE OR DELETE ON delivery.delivery_generation
    FOR EACH ROW EXECUTE FUNCTION delivery.reject_authority_history_mutation();
DROP TRIGGER IF EXISTS delivery_publication_history_immutable ON delivery.delivery_publication;
CREATE TRIGGER delivery_publication_history_immutable BEFORE DELETE ON delivery.delivery_publication
    FOR EACH ROW EXECUTE FUNCTION delivery.reject_authority_history_mutation();
DROP TRIGGER IF EXISTS delivery_attempt_identity_immutable ON delivery.delivery_build_attempt;
CREATE TRIGGER delivery_attempt_identity_immutable BEFORE UPDATE OR DELETE ON delivery.delivery_build_attempt
    FOR EACH ROW EXECUTE FUNCTION delivery.reject_attempt_identity_mutation();
DROP TRIGGER IF EXISTS delivery_publication_immutable ON delivery.delivery_publication;
CREATE TRIGGER delivery_publication_immutable BEFORE UPDATE OR DELETE ON delivery.delivery_publication
    FOR EACH ROW EXECUTE FUNCTION delivery.reject_publication_mutation();
DROP TRIGGER IF EXISTS delivery_candidate_immutable ON delivery.delivery_candidate;
CREATE TRIGGER delivery_candidate_immutable BEFORE UPDATE OR DELETE ON delivery.delivery_candidate
    FOR EACH ROW EXECUTE FUNCTION delivery.reject_candidate_mutation();
DROP TRIGGER IF EXISTS delivery_lease_immutable ON delivery.delivery_lease;
CREATE TRIGGER delivery_lease_immutable BEFORE UPDATE OR DELETE ON delivery.delivery_lease
    FOR EACH ROW EXECUTE FUNCTION delivery.reject_lease_mutation();
DROP TRIGGER IF EXISTS delivery_root_immutable ON delivery.delivery_retention_root;
CREATE TRIGGER delivery_root_immutable BEFORE UPDATE OR DELETE ON delivery.delivery_retention_root
    FOR EACH ROW EXECUTE FUNCTION delivery.reject_root_mutation();
DROP TRIGGER IF EXISTS delivery_approval_request_insert_guard ON delivery.delivery_approval_request;
CREATE TRIGGER delivery_approval_request_insert_guard BEFORE INSERT ON delivery.delivery_approval_request
    FOR EACH ROW EXECUTE FUNCTION delivery.guard_approval_request_insert();
DROP TRIGGER IF EXISTS delivery_approval_revision_after_insert ON delivery.delivery_approval_request;
CREATE TRIGGER delivery_approval_revision_after_insert AFTER INSERT ON delivery.delivery_approval_request
    FOR EACH ROW EXECUTE FUNCTION delivery.create_approval_revision_row();
DROP TRIGGER IF EXISTS delivery_approval_request_immutable ON delivery.delivery_approval_request;
CREATE TRIGGER delivery_approval_request_immutable BEFORE UPDATE OR DELETE ON delivery.delivery_approval_request
    FOR EACH ROW EXECUTE FUNCTION delivery.reject_approval_request_mutation();
DROP TRIGGER IF EXISTS delivery_approval_decision_insert_guard ON delivery.delivery_approval_decision;
CREATE TRIGGER delivery_approval_decision_insert_guard BEFORE INSERT ON delivery.delivery_approval_decision
    FOR EACH ROW EXECUTE FUNCTION delivery.guard_approval_decision_insert();
DROP TRIGGER IF EXISTS delivery_approval_decision_immutable ON delivery.delivery_approval_decision;
CREATE TRIGGER delivery_approval_decision_immutable BEFORE UPDATE OR DELETE ON delivery.delivery_approval_decision
    FOR EACH ROW EXECUTE FUNCTION delivery.reject_approval_decision_mutation();
DROP TRIGGER IF EXISTS delivery_approval_revision_monotonic ON delivery.delivery_approval_revision;
CREATE TRIGGER delivery_approval_revision_monotonic BEFORE UPDATE OR DELETE ON delivery.delivery_approval_revision
    FOR EACH ROW EXECUTE FUNCTION delivery.guard_approval_revision_mutation();
DROP TRIGGER IF EXISTS delivery_active_pointer_consistency ON delivery.delivery_active_pointer;
CREATE CONSTRAINT TRIGGER delivery_active_pointer_consistency AFTER INSERT OR UPDATE ON delivery.delivery_active_pointer
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION delivery.check_active_pointer_consistency();
DROP TRIGGER IF EXISTS delivery_active_pointer_runtime_guard ON delivery.delivery_active_pointer;
CREATE TRIGGER delivery_active_pointer_runtime_guard BEFORE INSERT OR UPDATE OR DELETE ON delivery.delivery_active_pointer
    FOR EACH ROW EXECUTE FUNCTION delivery.reject_runtime_active_pointer_mutation();
CREATE INDEX IF NOT EXISTS delivery_lease_active_idx ON delivery.delivery_lease(target_id, state, expires_at);
CREATE UNIQUE INDEX IF NOT EXISTS delivery_lease_one_active_idx ON delivery.delivery_lease(target_id) WHERE state = 'active';
CREATE INDEX IF NOT EXISTS delivery_generation_target_idx ON delivery.delivery_generation(target_id, generation_revision);
CREATE INDEX IF NOT EXISTS delivery_generation_candidate_idx ON delivery.delivery_generation(candidate_id);
CREATE INDEX IF NOT EXISTS delivery_seal_attempt_idx ON delivery.delivery_snapshot_seal(attempt_id);
CREATE INDEX IF NOT EXISTS delivery_root_snapshot_idx ON delivery.delivery_retention_root(snapshot_seal_id, state);
CREATE INDEX IF NOT EXISTS delivery_approval_request_publication_idx ON delivery.delivery_approval_request(publication_id, requested_at DESC, request_id DESC);
CREATE INDEX IF NOT EXISTS delivery_approval_decision_request_idx ON delivery.delivery_approval_decision(request_id, decision_revision DESC, decision_id DESC);

-- Delivery authority evidence is never reachable through PUBLIC defaults.  The
-- applying role remains the owner and therefore retains full control; deploy
-- roles must be granted the minimum required privileges explicitly by the
-- surrounding migration.
REVOKE ALL ON SCHEMA delivery FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA delivery FROM PUBLIC;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA delivery FROM PUBLIC;
GRANT USAGE ON SCHEMA delivery TO CURRENT_USER;
GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA delivery TO CURRENT_USER;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA delivery TO CURRENT_USER;
REVOKE UPDATE, DELETE ON delivery.delivery_build_attempt_successor FROM CURRENT_USER;
GRANT SELECT, INSERT ON delivery.delivery_build_attempt_successor TO CURRENT_USER;

-- Runtime activation and maintenance expiry use the narrow capability entry
-- points above; neither role needs direct retention-root UPDATE/DELETE access.
REVOKE ALL ON FUNCTION delivery.retire_retention_root(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION delivery.expire_retention_root(uuid, interval) FROM PUBLIC;
REVOKE ALL ON FUNCTION delivery.maintain_retention_roots(text, text, interval, integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION delivery.create_recovery_retention_root(uuid, text, uuid, uuid, timestamptz, jsonb) FROM PUBLIC;
REVOKE ALL ON FUNCTION delivery.lock_retention_root(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION delivery.lock_live_snapshot_retention(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION delivery.commit_activation_transition(uuid, text, uuid, bigint, bigint) FROM PUBLIC;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT EXECUTE ON FUNCTION delivery.lock_retention_root(uuid) TO leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION delivery.lock_live_snapshot_retention(uuid) TO leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION delivery.retire_retention_root(uuid) TO leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION delivery.commit_activation_transition(uuid, text, uuid, bigint, bigint) TO leapview_control_runtime;
        -- Expiry is maintenance/drain-owned. Runtime activation may retire
        -- predecessors but cannot force their terminal expiry.
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA delivery TO leapview_control_maintenance;
        GRANT SELECT ON delivery.delivery_retention_root TO leapview_control_maintenance;
        GRANT EXECUTE ON FUNCTION delivery.lock_retention_root(uuid) TO leapview_control_maintenance;
        GRANT EXECUTE ON FUNCTION delivery.lock_live_snapshot_retention(uuid) TO leapview_control_maintenance;
        GRANT EXECUTE ON FUNCTION delivery.retire_retention_root(uuid) TO leapview_control_maintenance;
        GRANT EXECUTE ON FUNCTION delivery.expire_retention_root(uuid, interval) TO leapview_control_maintenance;
        GRANT EXECUTE ON FUNCTION delivery.maintain_retention_roots(text, text, interval, integer) TO leapview_control_maintenance;
        GRANT EXECUTE ON FUNCTION delivery.create_recovery_retention_root(uuid, text, uuid, uuid, timestamptz, jsonb) TO leapview_control_maintenance;
    END IF;
END;
$$;
-- +goose StatementEnd

-- capability source: internal/servingstate/postgres/schema.sql
-- Immutable serving-generation evidence.  Delivery owns lifecycle status and
-- active selection; this capability stores only the generation bundle that a
-- delivery transaction admits and the reader leases rooted at its snapshot
-- seal.  There is intentionally no serving-state status or pointer table.
CREATE SCHEMA IF NOT EXISTS serving_state;

CREATE TABLE IF NOT EXISTS serving_state.bundle (
    generation_id uuid PRIMARY KEY REFERENCES delivery.delivery_generation(generation_id) ON DELETE RESTRICT,
    project_id text NOT NULL CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255 AND project_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    environment text NOT NULL CHECK (environment = btrim(environment) AND octet_length(environment) BETWEEN 1 AND 128 AND environment ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    artifact_id text NOT NULL CHECK (artifact_id = btrim(artifact_id) AND artifact_id = 'artifact-' || substr(artifact_digest, 8) AND octet_length(artifact_id) BETWEEN 1 AND 255),
    artifact_digest text NOT NULL CHECK (artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    compiled_graph_digest text NOT NULL CHECK (compiled_graph_digest ~ '^sha256:[0-9a-f]{64}$'),
    artifact_format text NOT NULL CHECK (artifact_format = 'tar.gz'),
    -- Immutable object-storage key/locator. This is deliberately not a
    -- production filesystem path; filesystem paths never enter this native
    -- schema.
    artifact_locator text NOT NULL CHECK (artifact_locator = btrim(artifact_locator) AND artifact_locator = 'serving-artifacts/' || substr(artifact_digest, 8) || '.tar.gz' AND octet_length(artifact_locator) BETWEEN 1 AND 2048),
    storage_security_domain text NOT NULL CHECK (storage_security_domain = btrim(storage_security_domain) AND octet_length(storage_security_domain) BETWEEN 1 AND 512 AND storage_security_domain !~ '[[:cntrl:]]'),
    artifact_content_type text NOT NULL CHECK (artifact_content_type = 'application/gzip'),
    artifact_metadata_digest text NOT NULL CHECK (artifact_metadata_digest ~ '^sha256:[0-9a-f]{64}$'),
    manifest_json jsonb NOT NULL CHECK (jsonb_typeof(manifest_json) = 'object' AND octet_length(manifest_json::text) <= 1048576),
    project_digest text NOT NULL CHECK (project_digest ~ '^sha256:[0-9a-f]{64}$'),
    access_policy_json jsonb NOT NULL CHECK (jsonb_typeof(access_policy_json) = 'object' AND octet_length(access_policy_json::text) <= 1048576),
    dashboard_publications_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(dashboard_publications_json) = 'object' AND octet_length(dashboard_publications_json::text) <= 1048576),
    dashboard_appearances_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(dashboard_appearances_json) = 'object' AND octet_length(dashboard_appearances_json::text) <= 1048576),
    size_bytes bigint NOT NULL CHECK (size_bytes BETWEEN 1 AND 67108864),
    created_by text NOT NULL CHECK (created_by = btrim(created_by) AND octet_length(created_by) BETWEEN 1 AND 255 AND created_by !~ '[[:cntrl:]]'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (generation_id, project_id, environment)
);

CREATE TABLE IF NOT EXISTS serving_state.asset (
    generation_id uuid NOT NULL REFERENCES serving_state.bundle(generation_id) ON DELETE RESTRICT,
    snapshot_id text NOT NULL CHECK (snapshot_id = btrim(snapshot_id) AND octet_length(snapshot_id) BETWEEN 1 AND 255),
    logical_asset_id text NOT NULL CHECK (logical_asset_id = btrim(logical_asset_id) AND octet_length(logical_asset_id) BETWEEN 1 AND 255),
    asset_type text NOT NULL CHECK (octet_length(asset_type) BETWEEN 1 AND 64),
    asset_key text NOT NULL CHECK (octet_length(asset_key) BETWEEN 1 AND 255),
    parent_logical_asset_id text NOT NULL DEFAULT '' CHECK (octet_length(parent_logical_asset_id) <= 255),
    title text NOT NULL DEFAULT '' CHECK (octet_length(title) <= 512),
    description text NOT NULL DEFAULT '' CHECK (octet_length(description) <= 4096),
    source_file text NOT NULL DEFAULT '' CHECK (octet_length(source_file) <= 2048),
    payload_schema text NOT NULL CHECK (octet_length(payload_schema) BETWEEN 1 AND 128),
    payload_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload_json) = 'object' AND octet_length(payload_json::text) <= 1048576),
    content_hash text NOT NULL CHECK (octet_length(content_hash) BETWEEN 1 AND 255),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (generation_id, logical_asset_id),
    UNIQUE (snapshot_id)
);

CREATE TABLE IF NOT EXISTS serving_state.asset_edge (
    generation_id uuid NOT NULL REFERENCES serving_state.bundle(generation_id) ON DELETE RESTRICT,
    id text NOT NULL CHECK (id = btrim(id) AND octet_length(id) BETWEEN 1 AND 255),
    from_logical_asset_id text NOT NULL CHECK (octet_length(from_logical_asset_id) BETWEEN 1 AND 255),
    to_logical_asset_id text NOT NULL CHECK (octet_length(to_logical_asset_id) BETWEEN 1 AND 255),
    edge_type text NOT NULL CHECK (octet_length(edge_type) BETWEEN 1 AND 128),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (generation_id, id),
    UNIQUE (generation_id, from_logical_asset_id, to_logical_asset_id, edge_type),
    FOREIGN KEY (generation_id, from_logical_asset_id) REFERENCES serving_state.asset(generation_id, logical_asset_id),
    FOREIGN KEY (generation_id, to_logical_asset_id) REFERENCES serving_state.asset(generation_id, logical_asset_id)
);

CREATE TABLE IF NOT EXISTS serving_state.reader_lease (
    lease_id text PRIMARY KEY CHECK (lease_id = btrim(lease_id) AND octet_length(lease_id) BETWEEN 1 AND 255),
    generation_id uuid NOT NULL REFERENCES delivery.delivery_generation(generation_id) ON DELETE RESTRICT,
    ducklake_snapshot_id bigint NOT NULL CHECK (ducklake_snapshot_id > 0),
    owner_id text NOT NULL CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255),
    acquired_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    released_at timestamptz,
    CHECK (expires_at > acquired_at),
    CHECK (released_at IS NULL OR released_at >= acquired_at)
);
CREATE INDEX IF NOT EXISTS reader_lease_live_idx ON serving_state.reader_lease (generation_id, ducklake_snapshot_id, expires_at) WHERE released_at IS NULL;
CREATE INDEX IF NOT EXISTS bundle_scope_idx ON serving_state.bundle (project_id, environment, created_at DESC);
CREATE INDEX IF NOT EXISTS asset_generation_idx ON serving_state.asset (generation_id, asset_type, asset_key);

-- Row locking is performed by a fixed-search-path, read-only definer so the
-- runtime role needs no UPDATE privilege on delivery retention evidence.
--
-- A candidate preview is allowed to lease the exact snapshot sealed on its
-- delivery generation while that candidate root remains live.  Activation
-- still creates the generation root; candidate roots are intentionally bound
-- through the generation's candidate_id, generation_id and snapshot seal
-- rather than by a mutable serving pointer. Candidate roots are always
-- bounded by an explicit expiry; generation roots may remain unbounded.
CREATE OR REPLACE FUNCTION serving_state.guard_reader_snapshot_retention(p_generation uuid, p_snapshot bigint)
-- +goose StatementBegin
RETURNS boolean SECURITY DEFINER LANGUAGE plpgsql SET search_path = serving_state, delivery, pg_catalog AS $$
BEGIN
    RETURN EXISTS (
        SELECT 1
        FROM delivery.delivery_generation g
        JOIN delivery.delivery_snapshot_seal s ON s.seal_id = g.snapshot_seal_id
        JOIN delivery.delivery_retention_root r
          ON r.target_id = g.target_id
         AND r.snapshot_seal_id = s.seal_id
         AND (
             (r.root_kind = 'generation' AND r.generation_id = g.generation_id)
             OR (r.root_kind = 'candidate' AND r.candidate_id = g.candidate_id AND r.generation_id = g.generation_id)
         )
        WHERE g.generation_id = p_generation AND s.ducklake_snapshot_id = p_snapshot
          AND r.state = 'live'
          AND (
              (r.root_kind = 'generation' AND (r.expires_at IS NULL OR r.expires_at > clock_timestamp()))
              OR (r.root_kind = 'candidate' AND r.expires_at IS NOT NULL AND r.expires_at > clock_timestamp())
          )
        FOR SHARE OF r
    );
END; $$;
-- +goose StatementEnd

-- Bundle scope and digest identity must agree with the delivery generation and
-- its target.  The target/generation rows are immutable delivery evidence.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION serving_state.validate_bundle_generation() RETURNS trigger LANGUAGE plpgsql SET search_path = serving_state, delivery, pg_catalog AS $$
DECLARE target_project text; target_environment text; generation_digest text; generation_graph_digest text;
BEGIN
    SELECT t.project_id, t.environment, g.serving_artifact_digest, g.compiled_graph_digest
      INTO target_project, target_environment, generation_digest, generation_graph_digest
      FROM delivery.delivery_generation g JOIN delivery.delivery_target t ON t.target_id = g.target_id
     WHERE g.generation_id = NEW.generation_id;
    IF target_project IS NULL OR NEW.project_id <> target_project OR NEW.environment <> target_environment THEN
        RAISE EXCEPTION 'serving bundle scope does not match delivery generation';
    END IF;
    IF NEW.artifact_digest <> generation_digest THEN
        RAISE EXCEPTION 'serving bundle artifact digest does not match delivery generation';
    END IF;
    IF NEW.compiled_graph_digest <> generation_graph_digest THEN
        RAISE EXCEPTION 'serving bundle graph digest does not match delivery generation';
    END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS bundle_generation_consistency ON serving_state.bundle;
CREATE TRIGGER bundle_generation_consistency BEFORE INSERT ON serving_state.bundle FOR EACH ROW EXECUTE FUNCTION serving_state.validate_bundle_generation();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION serving_state.reject_bundle_mutation() RETURNS trigger LANGUAGE plpgsql SET search_path = serving_state, delivery, pg_catalog AS $$
BEGIN RAISE EXCEPTION 'serving generation bundle evidence is immutable'; END; $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS bundle_immutable ON serving_state.bundle;
CREATE TRIGGER bundle_immutable BEFORE UPDATE OR DELETE ON serving_state.bundle FOR EACH ROW EXECUTE FUNCTION serving_state.reject_bundle_mutation();
DROP TRIGGER IF EXISTS asset_immutable ON serving_state.asset;
CREATE TRIGGER asset_immutable BEFORE UPDATE OR DELETE ON serving_state.asset FOR EACH ROW EXECUTE FUNCTION serving_state.reject_bundle_mutation();
DROP TRIGGER IF EXISTS asset_edge_immutable ON serving_state.asset_edge;
CREATE TRIGGER asset_edge_immutable BEFORE UPDATE OR DELETE ON serving_state.asset_edge FOR EACH ROW EXECUTE FUNCTION serving_state.reject_bundle_mutation();

-- Reader leases are the sole mutable serving rows. They are query leases, not
-- an independent retention authority: every lease must reference a live
-- delivery generation retention root and may only move once from live to
-- released (or extend forward while that root remains live).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION serving_state.validate_reader_lease_mutation() RETURNS trigger SECURITY DEFINER LANGUAGE plpgsql SET search_path = serving_state, delivery, pg_catalog AS $$
DECLARE expected_snapshot bigint;
BEGIN
    IF TG_OP = 'INSERT' THEN
        SELECT s.ducklake_snapshot_id INTO expected_snapshot
          FROM delivery.delivery_generation g
          JOIN delivery.delivery_snapshot_seal s ON s.seal_id = g.snapshot_seal_id
         WHERE g.generation_id = NEW.generation_id;
        IF expected_snapshot IS NULL OR expected_snapshot <> NEW.ducklake_snapshot_id THEN
            RAISE EXCEPTION 'reader lease snapshot does not match delivery snapshot seal';
        END IF;
        IF NOT serving_state.guard_reader_snapshot_retention(NEW.generation_id, NEW.ducklake_snapshot_id) THEN
            RAISE EXCEPTION 'reader lease requires a live delivery retention root';
        END IF;
        IF NEW.expires_at <= clock_timestamp() OR NEW.expires_at > clock_timestamp() + interval '24 hours' THEN
            RAISE EXCEPTION 'reader lease expiry must be within the 24-hour DB-clock window';
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.lease_id <> OLD.lease_id OR NEW.generation_id <> OLD.generation_id OR NEW.ducklake_snapshot_id <> OLD.ducklake_snapshot_id OR NEW.owner_id <> OLD.owner_id OR NEW.acquired_at <> OLD.acquired_at THEN
        RAISE EXCEPTION 'reader lease identity is immutable';
    END IF;
    IF OLD.released_at IS NOT NULL AND (NEW.released_at IS DISTINCT FROM OLD.released_at OR NEW.expires_at IS DISTINCT FROM OLD.expires_at) THEN
        RAISE EXCEPTION 'released reader lease is immutable';
    END IF;
    IF NEW.released_at IS NULL AND NEW.expires_at < OLD.expires_at THEN
        RAISE EXCEPTION 'reader lease expiry cannot move backwards';
    END IF;
    -- Direct UPDATE callers receive the same retention fence as the
    -- repository's renewal query.  In particular, a renewal must take a
    -- share lock on the exact live delivery root before it can move the
    -- expiry forward; this serializes with delivery root retirement/expiry
    -- and prevents extending a lease after its root has expired.
    IF NEW.released_at IS NULL AND NEW.expires_at > OLD.expires_at
       AND NOT serving_state.guard_reader_snapshot_retention(NEW.generation_id, NEW.ducklake_snapshot_id) THEN
        RAISE EXCEPTION 'reader lease renewal requires a live delivery retention root';
    END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS reader_lease_mutation ON serving_state.reader_lease;
CREATE TRIGGER reader_lease_mutation BEFORE INSERT OR UPDATE ON serving_state.reader_lease FOR EACH ROW EXECUTE FUNCTION serving_state.validate_reader_lease_mutation();

-- Expired query-lease release is an operational capability, not
-- request-serving authority. Delivery owns retention-root lifecycle and this
-- capability deliberately does not mutate those roots or immutable delivery
-- evidence. A maintenance batch only advances reader-lease release markers
-- in a bounded, deterministic order.
CREATE OR REPLACE FUNCTION serving_state.release_expired_query_snapshot_leases(
    p_environment text,
    p_limit integer
)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, serving_state, delivery
-- +goose StatementBegin
AS $$
DECLARE
    removed bigint := 0;
BEGIN
    IF p_environment IS NULL OR p_environment <> btrim(p_environment)
       OR octet_length(p_environment) < 1 OR octet_length(p_environment) > 128
       OR p_environment !~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$' THEN
        RAISE EXCEPTION 'serving-state retention environment is invalid';
    END IF;
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 1000 THEN
        RAISE EXCEPTION 'serving-state retention limit must be between 1 and 1000';
    END IF;
    WITH doomed AS (
        SELECT l.lease_id
        FROM serving_state.reader_lease l
        JOIN delivery.delivery_generation g ON g.generation_id = l.generation_id
        JOIN delivery.delivery_target t ON t.target_id = g.target_id
        WHERE t.environment = p_environment
          AND l.released_at IS NULL
          AND l.expires_at <= clock_timestamp()
        ORDER BY l.expires_at, l.lease_id
        LIMIT p_limit
        FOR UPDATE OF l SKIP LOCKED
    )
    UPDATE serving_state.reader_lease l
       SET released_at = clock_timestamp()
      FROM doomed d
     WHERE l.lease_id = d.lease_id;
    GET DIAGNOSTICS removed = ROW_COUNT;
    RETURN removed;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON SCHEMA serving_state FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA serving_state FROM PUBLIC;
REVOKE ALL ON FUNCTION serving_state.guard_reader_snapshot_retention(uuid,bigint) FROM PUBLIC;
REVOKE ALL ON FUNCTION serving_state.validate_reader_lease_mutation() FROM PUBLIC;
REVOKE ALL ON FUNCTION serving_state.release_expired_query_snapshot_leases(text,integer) FROM PUBLIC;
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_owner') THEN
        GRANT ALL ON FUNCTION serving_state.release_expired_query_snapshot_leases(text,integer) TO leapview_control_owner;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_migrator') THEN
        GRANT ALL ON FUNCTION serving_state.release_expired_query_snapshot_leases(text,integer) TO leapview_control_migrator;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA serving_state TO leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION serving_state.guard_reader_snapshot_retention(uuid,bigint) TO leapview_control_runtime;
        REVOKE EXECUTE ON FUNCTION serving_state.release_expired_query_snapshot_leases(text,integer) FROM leapview_control_runtime;
        GRANT SELECT, INSERT ON serving_state.bundle, serving_state.asset, serving_state.asset_edge TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON serving_state.reader_lease TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA serving_state TO leapview_control_maintenance;
        GRANT EXECUTE ON FUNCTION serving_state.release_expired_query_snapshot_leases(text,integer) TO leapview_control_maintenance;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA serving_state TO leapview_control_readonly;
        GRANT SELECT ON ALL TABLES IN SCHEMA serving_state TO leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA serving_state TO leapview_control_backup;
        GRANT SELECT ON ALL TABLES IN SCHEMA serving_state TO leapview_control_backup;
    END IF;
END $$;
-- +goose StatementEnd

-- capability source: internal/release/postgres/schema.sql
-- Clean-slate release control authority (ADR-0020).
--
-- Release owns the API-facing immutable release identity, artifact evidence,
-- candidate provenance, and deployment linkage. Canonical delivery selection
-- remains owned by the delivery capability; this schema intentionally does
-- not recreate legacy candidate/deployment/serving-pointer projections.

CREATE SCHEMA IF NOT EXISTS release;

CREATE TABLE IF NOT EXISTS release.release_record (
    release_id text PRIMARY KEY,
    project_id text NOT NULL,
    environment text NOT NULL,
    generation_id text NOT NULL,
    project_digest text NOT NULL CHECK (project_digest ~ '^sha256:[0-9a-f]{64}$'),
    artifact_digest text NOT NULL CHECK (artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    artifact_actual_digest text,
    artifact_size_bytes bigint NOT NULL DEFAULT 0 CHECK (artifact_size_bytes >= 0),
    artifact_uploaded_at timestamptz,
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    idempotency_key text NOT NULL,
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','validating','ready','failed')),
    provenance jsonb NOT NULL CHECK (jsonb_typeof(provenance) = 'object' AND octet_length(provenance::text) <= 65536),
    created_by text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    finalized_at timestamptz,
    error text NOT NULL DEFAULT '',
    CHECK (release_id = btrim(release_id) AND octet_length(release_id) BETWEEN 1 AND 255),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255),
    CHECK (environment = btrim(environment) AND octet_length(environment) BETWEEN 1 AND 255),
    CHECK (generation_id = btrim(generation_id) AND octet_length(generation_id) BETWEEN 1 AND 255),
    CHECK (idempotency_key = btrim(idempotency_key) AND octet_length(idempotency_key) BETWEEN 1 AND 512),
    CHECK (created_by = btrim(created_by) AND octet_length(created_by) BETWEEN 1 AND 255),
    CHECK (artifact_actual_digest IS NULL OR artifact_actual_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (
        (artifact_actual_digest IS NULL AND artifact_uploaded_at IS NULL AND artifact_size_bytes = 0)
        OR (artifact_actual_digest IS NOT NULL AND artifact_uploaded_at IS NOT NULL
            AND artifact_actual_digest = artifact_digest AND artifact_size_bytes >= 0)
    ),
    CHECK (
        (status IN ('draft', 'validating') AND finalized_at IS NULL AND error = '')
        OR (status = 'ready' AND finalized_at IS NOT NULL AND error = '')
        OR (status = 'failed' AND finalized_at IS NOT NULL AND octet_length(error) BETWEEN 1 AND 4096)
    ),
    CHECK (octet_length(error) <= 4096),
    UNIQUE (project_id, idempotency_key),
    UNIQUE (project_id, environment, generation_id)
);

CREATE TABLE IF NOT EXISTS release.release_connection (
    release_id text NOT NULL REFERENCES release.release_record(release_id) ON DELETE RESTRICT,
    connection_id text NOT NULL,
    revision_id text NOT NULL,
    PRIMARY KEY (release_id, connection_id),
    CHECK (connection_id = btrim(connection_id) AND octet_length(connection_id) BETWEEN 1 AND 255),
    CHECK (revision_id = btrim(revision_id) AND octet_length(revision_id) BETWEEN 1 AND 255)
);

CREATE OR REPLACE FUNCTION release.guard_release_mutation()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT (NEW.status IS NOT DISTINCT FROM OLD.status)
       AND NOT (OLD.status = 'draft' AND NEW.status = 'validating')
       AND NOT (OLD.status = 'validating' AND NEW.status IN ('ready', 'failed')) THEN
        RAISE EXCEPTION 'illegal release status transition';
    END IF;
    IF NEW.status IN ('validating', 'ready')
       AND (NEW.artifact_uploaded_at IS NULL OR NEW.artifact_actual_digest IS NULL
            OR NEW.artifact_actual_digest <> NEW.artifact_digest) THEN
        RAISE EXCEPTION 'release status requires matching uploaded artifact';
    END IF;
    IF NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'release created_at is immutable';
    END IF;
    IF NEW.release_id IS DISTINCT FROM OLD.release_id
       OR NEW.project_id IS DISTINCT FROM OLD.project_id
       OR NEW.environment IS DISTINCT FROM OLD.environment
       OR NEW.generation_id IS DISTINCT FROM OLD.generation_id
       OR NEW.project_digest IS DISTINCT FROM OLD.project_digest
       OR NEW.artifact_digest IS DISTINCT FROM OLD.artifact_digest
       OR NEW.request_digest IS DISTINCT FROM OLD.request_digest
       OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key
       OR NEW.provenance IS DISTINCT FROM OLD.provenance
       OR NEW.created_by IS DISTINCT FROM OLD.created_by
       OR (NEW.artifact_actual_digest IS DISTINCT FROM OLD.artifact_actual_digest
           OR NEW.artifact_size_bytes IS DISTINCT FROM OLD.artifact_size_bytes
           OR NEW.artifact_uploaded_at IS DISTINCT FROM OLD.artifact_uploaded_at)
          AND NOT (
              OLD.status = 'draft'
              AND OLD.artifact_actual_digest IS NULL
              AND OLD.artifact_uploaded_at IS NULL
              AND NEW.artifact_actual_digest IS NOT NULL
              AND NEW.artifact_uploaded_at IS NOT NULL
              AND NEW.artifact_actual_digest = NEW.artifact_digest
          )
       OR OLD.status IN ('draft','validating') AND NEW.status IS NOT DISTINCT FROM OLD.status AND (
           NEW.finalized_at IS DISTINCT FROM OLD.finalized_at
           OR NEW.error IS DISTINCT FROM OLD.error)
       OR OLD.status IN ('ready','failed') AND (
           NEW.status IS DISTINCT FROM OLD.status
           OR NEW.error IS DISTINCT FROM OLD.error
           OR NEW.finalized_at IS DISTINCT FROM OLD.finalized_at) THEN
        RAISE EXCEPTION 'release immutable identity or evidence cannot be mutated';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS release_record_guard ON release.release_record;
CREATE TRIGGER release_record_guard
    BEFORE UPDATE ON release.release_record
    FOR EACH ROW EXECUTE FUNCTION release.guard_release_mutation();

-- Candidate provenance is immutable admission evidence. A replay with the
-- same candidate revision and a different digest is a conflict in the
-- repository; the database uniqueness constraint prevents a second authority.
CREATE TABLE IF NOT EXISTS release.candidate_provenance (
    project_id text NOT NULL,
    candidate_id text NOT NULL,
    candidate_revision bigint NOT NULL CHECK (candidate_revision > 0),
    provenance_digest text NOT NULL CHECK (provenance_digest ~ '^sha256:[0-9a-f]{64}$'),
    provenance jsonb NOT NULL CHECK (jsonb_typeof(provenance) = 'object' AND octet_length(provenance::text) <= 65536),
    retained_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (project_id, candidate_id, candidate_revision),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255),
    CHECK (candidate_id = btrim(candidate_id) AND octet_length(candidate_id) BETWEEN 1 AND 255)
);

-- Deployment linkage is immutable evidence. The delivery capability owns
-- activation and target pointers; release stores only this exact association.
CREATE TABLE IF NOT EXISTS release.deployment_linkage (
    deployment_id text PRIMARY KEY,
    project_id text NOT NULL,
    release_id text NOT NULL REFERENCES release.release_record(release_id) ON DELETE RESTRICT,
    rollback_of text REFERENCES release.release_record(release_id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (deployment_id = btrim(deployment_id) AND octet_length(deployment_id) BETWEEN 1 AND 255),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255),
    CHECK (rollback_of IS NULL OR rollback_of <> release_id),
    UNIQUE (project_id, deployment_id)
);

CREATE OR REPLACE FUNCTION release.reject_immutable_mutation()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'release immutable evidence cannot be mutated';
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS candidate_provenance_immutable ON release.candidate_provenance;
CREATE TRIGGER candidate_provenance_immutable
    BEFORE UPDATE OR DELETE ON release.candidate_provenance
    FOR EACH ROW EXECUTE FUNCTION release.reject_immutable_mutation();

DROP TRIGGER IF EXISTS deployment_linkage_immutable ON release.deployment_linkage;
CREATE TRIGGER deployment_linkage_immutable
    BEFORE UPDATE OR DELETE ON release.deployment_linkage
    FOR EACH ROW EXECUTE FUNCTION release.reject_immutable_mutation();

DROP TRIGGER IF EXISTS release_connection_immutable ON release.release_connection;
CREATE TRIGGER release_connection_immutable
    BEFORE UPDATE OR DELETE ON release.release_connection
    FOR EACH ROW EXECUTE FUNCTION release.reject_immutable_mutation();

CREATE OR REPLACE FUNCTION release.guard_connection_insert()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    parent_status text;
    parent_uploaded_at timestamptz;
BEGIN
    SELECT status, artifact_uploaded_at
      INTO parent_status, parent_uploaded_at
      FROM release.release_record
     WHERE release_id = NEW.release_id
     FOR UPDATE;
    IF NOT FOUND OR parent_status <> 'draft' OR parent_uploaded_at IS NOT NULL THEN
        RAISE EXCEPTION 'release connections require a draft release before artifact upload';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS release_connection_insert_guard ON release.release_connection;
CREATE TRIGGER release_connection_insert_guard
    BEFORE INSERT ON release.release_connection
    FOR EACH ROW EXECUTE FUNCTION release.guard_connection_insert();

CREATE INDEX IF NOT EXISTS release_record_project_created_idx
    ON release.release_record(project_id, created_at DESC, release_id DESC);
CREATE INDEX IF NOT EXISTS candidate_provenance_generation_idx
    ON release.candidate_provenance(project_id, (provenance -> 'plan' -> 'identity' ->> 'environment'), (provenance -> 'plan' -> 'identity' ->> 'generationId'));

REVOKE ALL ON SCHEMA release FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA release FROM PUBLIC;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA release FROM PUBLIC;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA release TO leapview_control_runtime;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON release.release_record, release.release_connection
            FROM leapview_control_runtime;
        GRANT SELECT ON release.release_record, release.release_connection TO leapview_control_runtime;
        GRANT INSERT (release_id, project_id, environment, generation_id, project_digest,
                      artifact_digest, request_digest, idempotency_key, provenance, created_by)
            ON release.release_record TO leapview_control_runtime;
        GRANT UPDATE (artifact_actual_digest, artifact_size_bytes, artifact_uploaded_at,
                      status, finalized_at, error)
            ON release.release_record TO leapview_control_runtime;
        GRANT INSERT ON release.release_connection TO leapview_control_runtime;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON release.candidate_provenance, release.deployment_linkage
            FROM leapview_control_runtime;
        GRANT SELECT ON release.candidate_provenance, release.deployment_linkage TO leapview_control_runtime;
        GRANT INSERT (project_id, candidate_id, candidate_revision, provenance_digest, provenance)
            ON release.candidate_provenance TO leapview_control_runtime;
        GRANT INSERT (deployment_id, project_id, release_id, rollback_of)
            ON release.deployment_linkage TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA release TO leapview_control_readonly;
        GRANT SELECT ON ALL TABLES IN SCHEMA release TO leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA release TO leapview_control_backup;
        GRANT SELECT ON ALL TABLES IN SCHEMA release TO leapview_control_backup;
    END IF;
END
$$;
-- +goose StatementEnd

-- capability source: internal/analytics/ducklake/postgres/schema.sql
-- DuckLake control capability schema (FAI-563/564).
--
-- This schema is applied to the LeapView control PostgreSQL database.  The
-- DuckLake metadata catalog itself remains in the separately provisioned
-- DuckLake PostgreSQL database and is accessed by local DuckDB connectors.
-- These rows carry only immutable identities and lifecycle evidence; they do
-- not duplicate DuckLake's table/file manifest.
CREATE SCHEMA IF NOT EXISTS ducklake;

CREATE TABLE IF NOT EXISTS ducklake.catalog_identity (
    physical_pool_id       text PRIMARY KEY,
    catalog_database       text NOT NULL
        CHECK (catalog_database = btrim(catalog_database) AND octet_length(catalog_database) BETWEEN 1 AND 255),
    catalog_id             text NOT NULL,
    catalog_uuid           text NOT NULL
        CHECK (catalog_uuid = btrim(catalog_uuid)
            AND catalog_uuid ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'),
    metadata_schema        text NOT NULL,
    created_at             timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (physical_pool_id, catalog_id),
    CHECK (physical_pool_id = btrim(physical_pool_id) AND octet_length(physical_pool_id) BETWEEN 1 AND 255),
    CHECK (catalog_id = btrim(catalog_id) AND octet_length(catalog_id) BETWEEN 1 AND 255),
    CHECK (metadata_schema = btrim(metadata_schema) AND metadata_schema ~ '^[A-Za-z_][A-Za-z0-9_]*$')
);

-- Marker anomalies are durable pool-wide quarantine evidence.  They are
-- deliberately separate from positive attempt termination evidence: an
-- ambiguous or mismatched external marker cannot be represented as an abort
-- and must gate every successor attempt/recovery for this physical pool.
-- +goose StatementBegin
DO $$
BEGIN
    CREATE TYPE ducklake.marker_quarantine_reason AS ENUM
        ('duplicate', 'digest_mismatch', 'identity_mismatch');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS ducklake.marker_quarantine (
    physical_pool_id       text NOT NULL,
    catalog_id             text NOT NULL,
    attempt_id             uuid NOT NULL,
    request_digest         text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    plan_digest            text NOT NULL CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    reason                 ducklake.marker_quarantine_reason NOT NULL,
    evidence               jsonb NOT NULL,
    observed_marker_digest text NOT NULL CHECK (observed_marker_digest ~ '^sha256:[0-9a-f]{64}$'),
    observed_snapshot_ids  bigint[] NOT NULL DEFAULT '{}'::bigint[],
    created_at             timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (physical_pool_id, catalog_id, attempt_id),
    FOREIGN KEY (attempt_id, physical_pool_id, catalog_id)
        REFERENCES delivery.delivery_build_attempt(attempt_id, physical_pool_id, catalog_id),
    FOREIGN KEY (physical_pool_id, catalog_id)
        REFERENCES ducklake.catalog_identity(physical_pool_id, catalog_id),
    CHECK (physical_pool_id = btrim(physical_pool_id) AND octet_length(physical_pool_id) BETWEEN 1 AND 255),
    CHECK (catalog_id = btrim(catalog_id) AND octet_length(catalog_id) BETWEEN 1 AND 255),
    CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 32768),
    CHECK (cardinality(observed_snapshot_ids) <= 128)
);

CREATE INDEX IF NOT EXISTS ducklake_marker_quarantine_pool_idx
    ON ducklake.marker_quarantine (physical_pool_id, created_at);


-- Source observations are captured by the exact native DuckLake writer while
-- its prepared source session is still live.  The attempt key makes the
-- capture replay-safe; marker and envelope bytes are canonical identities,
-- not mutable diagnostic payloads.
CREATE TABLE IF NOT EXISTS ducklake.source_observation_capture (
    attempt_id            uuid PRIMARY KEY,
    commit_marker         jsonb NOT NULL,
    observation_envelope  jsonb NOT NULL,
    content_digest        text NOT NULL,
    captured_at           timestamptz NOT NULL,
    created_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (attempt_id) REFERENCES delivery.delivery_build_attempt(attempt_id) ON DELETE RESTRICT,
    CHECK (jsonb_typeof(commit_marker) = 'object' AND octet_length(commit_marker::text) <= 4096),
    CHECK (jsonb_typeof(observation_envelope) = 'object' AND octet_length(observation_envelope::text) <= 8388608),
    CHECK (content_digest ~ '^sha256:[0-9a-f]{64}$')
);

CREATE OR REPLACE FUNCTION ducklake.guard_source_observation_capture_immutable() RETURNS trigger
-- +goose StatementBegin
LANGUAGE plpgsql SET search_path = pg_catalog, ducklake AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'source observation captures are immutable';
    END IF;
    IF NEW.attempt_id IS DISTINCT FROM OLD.attempt_id
       OR NEW.commit_marker IS DISTINCT FROM OLD.commit_marker
       OR NEW.observation_envelope IS DISTINCT FROM OLD.observation_envelope
       OR NEW.content_digest IS DISTINCT FROM OLD.content_digest
       OR NEW.captured_at IS DISTINCT FROM OLD.captured_at
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'source observation capture identity is immutable';
    END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS source_observation_capture_immutable ON ducklake.source_observation_capture;
CREATE TRIGGER source_observation_capture_immutable
BEFORE UPDATE OR DELETE ON ducklake.source_observation_capture
FOR EACH ROW EXECUTE FUNCTION ducklake.guard_source_observation_capture_immutable();


-- A retention row is the gate for every durable root and active query lease.
-- Retiring/expiring prevents new leases while existing leases drain; expiring
-- additionally records the exact maintenance claim being reconciled.
CREATE TABLE IF NOT EXISTS ducklake.snapshot_retention (
    physical_pool_id text NOT NULL,
    catalog_id       text NOT NULL,
    snapshot_id      bigint NOT NULL CHECK (snapshot_id > 0),
    state            text NOT NULL CHECK (state IN ('live', 'retiring', 'expiring', 'expired', 'quarantined', 'cleanup-complete')),
    protected_until  timestamptz,
    retired_at       timestamptz,
    expired_at       timestamptz,
    cleanup_owner_id text,
    cleanup_fencing_epoch bigint NOT NULL DEFAULT 0 CHECK (cleanup_fencing_epoch >= 0),
    cleanup_lease_expires_at timestamptz,
    quarantined_at  timestamptz,
    cleanup_completed_at timestamptz,
    quarantine_evidence jsonb,
    cleanup_evidence jsonb,
    evidence         jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 32768),
    retention_claim_id uuid,
    retention_claim_owner_id text,
    retention_claim_fencing_epoch bigint NOT NULL DEFAULT 0 CHECK (retention_claim_fencing_epoch >= 0),
    retention_claimed_at timestamptz,
    created_at       timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (physical_pool_id, catalog_id, snapshot_id),
    FOREIGN KEY (physical_pool_id, catalog_id) REFERENCES ducklake.catalog_identity(physical_pool_id, catalog_id),
    CHECK (catalog_id = btrim(catalog_id) AND octet_length(catalog_id) BETWEEN 1 AND 255),
    CHECK ((state = 'live' AND retired_at IS NULL AND expired_at IS NULL) OR state <> 'live'),
    CHECK ((state = 'retiring' AND retired_at IS NOT NULL AND expired_at IS NULL) OR state <> 'retiring'),
    CHECK ((state = 'expiring' AND retired_at IS NOT NULL AND expired_at IS NULL) OR state <> 'expiring'),
    CHECK ((state IN ('expired', 'quarantined', 'cleanup-complete') AND expired_at IS NOT NULL) OR state NOT IN ('expired', 'quarantined', 'cleanup-complete')),
    CHECK (retired_at IS NULL OR retired_at >= created_at),
    CHECK (expired_at IS NULL OR expired_at >= COALESCE(retired_at, created_at)),
    CHECK ((cleanup_fencing_epoch = 0 AND cleanup_owner_id IS NULL AND cleanup_lease_expires_at IS NULL) OR (cleanup_fencing_epoch > 0 AND cleanup_owner_id IS NOT NULL AND cleanup_lease_expires_at IS NOT NULL)),
    CHECK (cleanup_owner_id IS NULL OR (cleanup_owner_id = btrim(cleanup_owner_id) AND octet_length(cleanup_owner_id) BETWEEN 1 AND 255)),
    CHECK (cleanup_lease_expires_at IS NULL OR cleanup_lease_expires_at > created_at),
    CHECK ((state IN ('quarantined', 'cleanup-complete') AND quarantined_at IS NOT NULL) OR state NOT IN ('quarantined', 'cleanup-complete')),
    CHECK ((state = 'cleanup-complete' AND cleanup_completed_at IS NOT NULL) OR state <> 'cleanup-complete'),
    CHECK (quarantine_evidence IS NULL OR (jsonb_typeof(quarantine_evidence) = 'object' AND octet_length(quarantine_evidence::text) <= 32768)),
    CHECK (cleanup_evidence IS NULL OR (jsonb_typeof(cleanup_evidence) = 'object' AND octet_length(cleanup_evidence::text) <= 32768)),
    CHECK (quarantined_at IS NULL OR quarantined_at >= COALESCE(expired_at, created_at)),
    CHECK (cleanup_completed_at IS NULL OR cleanup_completed_at >= COALESCE(quarantined_at, created_at)),
    CHECK ((retention_claim_fencing_epoch = 0 AND retention_claim_id IS NULL AND retention_claim_owner_id IS NULL AND retention_claimed_at IS NULL)
        OR (retention_claim_fencing_epoch > 0 AND retention_claim_id IS NOT NULL AND retention_claim_owner_id IS NOT NULL AND retention_claim_owner_id = btrim(retention_claim_owner_id) AND octet_length(retention_claim_owner_id) BETWEEN 1 AND 255 AND retention_claimed_at IS NOT NULL))
);

-- Add claim columns to installations created before the resumable retention
-- coordinator existed. The checks above cover fresh databases; these guarded
-- constraints preserve the same invariant during in-place upgrades.
ALTER TABLE ducklake.snapshot_retention
    ADD COLUMN IF NOT EXISTS retention_claim_id uuid,
    ADD COLUMN IF NOT EXISTS retention_claim_owner_id text,
    ADD COLUMN IF NOT EXISTS retention_claim_fencing_epoch bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS retention_claimed_at timestamptz;
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='ducklake.snapshot_retention'::regclass AND conname='snapshot_retention_claim_epoch_nonnegative') THEN
        ALTER TABLE ducklake.snapshot_retention ADD CONSTRAINT snapshot_retention_claim_epoch_nonnegative CHECK (retention_claim_fencing_epoch >= 0);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='ducklake.snapshot_retention'::regclass AND conname='snapshot_retention_claim_shape') THEN
        ALTER TABLE ducklake.snapshot_retention ADD CONSTRAINT snapshot_retention_claim_shape CHECK ((retention_claim_fencing_epoch = 0 AND retention_claim_id IS NULL AND retention_claim_owner_id IS NULL AND retention_claimed_at IS NULL)
            OR (retention_claim_fencing_epoch > 0 AND retention_claim_id IS NOT NULL AND retention_claim_owner_id IS NOT NULL AND retention_claim_owner_id = btrim(retention_claim_owner_id) AND octet_length(retention_claim_owner_id) BETWEEN 1 AND 255 AND retention_claimed_at IS NOT NULL));
    END IF;
END $$;
-- +goose StatementEnd

-- Older installations predate the resumable expiry claim state and retain the
-- original inline state check (which does not admit `expiring`).  Replace that
-- generated constraint in place so claim_retention_snapshots can advance rows
-- without requiring a destructive table rebuild.  Fresh databases already
-- carry the current definition and therefore take the no-op path.
-- +goose StatementBegin
DO $$
DECLARE
    v_definition text;
BEGIN
    SELECT pg_get_constraintdef(oid)
      INTO v_definition
      FROM pg_constraint
     WHERE conrelid='ducklake.snapshot_retention'::regclass
       AND conname='snapshot_retention_state_check';
    IF v_definition IS NOT NULL AND position('expiring' IN v_definition) = 0 THEN
        ALTER TABLE ducklake.snapshot_retention DROP CONSTRAINT snapshot_retention_state_check;
        v_definition := NULL;
    END IF;
    IF v_definition IS NULL THEN
        ALTER TABLE ducklake.snapshot_retention
            ADD CONSTRAINT snapshot_retention_state_check
            CHECK (state IN ('live', 'retiring', 'expiring', 'expired', 'quarantined', 'cleanup-complete'));
    END IF;
END $$;
-- +goose StatementEnd

-- Runtime admission is a narrow capability: callers may name only the
-- canonical delivery seal, while this owner-executed function derives the
-- exact physical pool/catalog/snapshot identity and idempotently creates the
-- live retention gate. Keep the search path fixed and qualify every object
-- so an untrusted caller cannot redirect SECURITY DEFINER resolution.
CREATE OR REPLACE FUNCTION ducklake.admit_snapshot_retention_from_seal(p_seal_id uuid)
RETURNS TABLE (physical_pool_id text, catalog_id text, snapshot_id bigint, retention_state text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
-- +goose StatementBegin
AS $$
DECLARE
    v_physical_pool_id text;
    v_catalog_id text;
    v_snapshot_id bigint;
BEGIN
    SELECT s.physical_pool_id, s.catalog_id, s.ducklake_snapshot_id
      INTO v_physical_pool_id, v_catalog_id, v_snapshot_id
      FROM delivery.delivery_snapshot_seal AS s
     WHERE s.seal_id = p_seal_id;
    IF NOT FOUND THEN
        RETURN;
    END IF;

    -- Enforce the same migration/maintenance serialization at the database
    -- capability boundary. A caller with EXECUTE privilege must not be able
    -- to bypass the repository's admission check by invoking this function
    -- directly.
    PERFORM ducklake.assert_attempt_admission_fence(v_physical_pool_id, v_catalog_id);

    INSERT INTO ducklake.snapshot_retention AS r
        (physical_pool_id, catalog_id, snapshot_id, state)
    VALUES (v_physical_pool_id, v_catalog_id, v_snapshot_id, 'live')
    ON CONFLICT ON CONSTRAINT snapshot_retention_pkey DO NOTHING;

    SELECT r.state
      INTO retention_state
      FROM ducklake.snapshot_retention AS r
     WHERE r.physical_pool_id = v_physical_pool_id
       AND r.catalog_id = v_catalog_id
       AND r.snapshot_id = v_snapshot_id
     FOR UPDATE;
    IF NOT FOUND THEN
        RETURN;
    END IF;
    physical_pool_id := v_physical_pool_id;
    catalog_id := v_catalog_id;
    snapshot_id := v_snapshot_id;
    RETURN NEXT;
END;
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION ducklake.admit_snapshot_retention_from_seal(uuid) FROM PUBLIC;

-- Orphan observations are immutable, bounded evidence for snapshots found in
-- DuckLake metadata before a control retention row was established.  They are
-- deliberately separate from retention: an orphan is not executable until a
-- qualified root is created, and cleanup workers must reconcile it first.
CREATE TABLE IF NOT EXISTS ducklake.snapshot_orphan (
    orphan_id        uuid PRIMARY KEY,
    physical_pool_id text NOT NULL,
    catalog_id       text NOT NULL,
    snapshot_id      bigint NOT NULL CHECK (snapshot_id > 0),
    state            text NOT NULL CHECK (state IN ('quarantined', 'cleanup-complete')),
    cleanup_owner_id text,
    cleanup_fencing_epoch bigint NOT NULL DEFAULT 0 CHECK (cleanup_fencing_epoch >= 0),
    cleanup_lease_expires_at timestamptz,
    evidence         jsonb NOT NULL CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 32768),
    discovered_at    timestamptz NOT NULL DEFAULT clock_timestamp(),
    cleanup_not_before timestamptz NOT NULL DEFAULT clock_timestamp(),
    resolved_at      timestamptz,
    FOREIGN KEY (physical_pool_id, catalog_id) REFERENCES ducklake.catalog_identity(physical_pool_id, catalog_id),
    UNIQUE (physical_pool_id, catalog_id, snapshot_id),
    CHECK ((state = 'quarantined' AND resolved_at IS NULL) OR (state = 'cleanup-complete' AND resolved_at IS NOT NULL)),
    CHECK ((cleanup_fencing_epoch = 0 AND cleanup_owner_id IS NULL AND cleanup_lease_expires_at IS NULL) OR (cleanup_fencing_epoch > 0 AND cleanup_owner_id IS NOT NULL AND cleanup_lease_expires_at IS NOT NULL)),
    CHECK (cleanup_owner_id IS NULL OR (cleanup_owner_id = btrim(cleanup_owner_id) AND octet_length(cleanup_owner_id) BETWEEN 1 AND 255)),
    CHECK (cleanup_lease_expires_at IS NULL OR cleanup_lease_expires_at > discovered_at),
    CHECK (cleanup_not_before >= discovered_at)
);

-- Older installations predate orphan cleanup grace.  Add the column in place,
-- backfill existing observations from their discovery timestamp, and restore
-- the same default, nullability, and ordering invariant as fresh databases.
ALTER TABLE ducklake.snapshot_orphan
    ADD COLUMN IF NOT EXISTS cleanup_not_before timestamptz;
-- The current immutable-row trigger also protects cleanup_not_before.  Drop it
-- for the transactional backfill; the schema's trigger definition is recreated
-- below after this migration block has completed.
DROP TRIGGER IF EXISTS snapshot_orphan_identity_immutable ON ducklake.snapshot_orphan;
UPDATE ducklake.snapshot_orphan
   SET cleanup_not_before = CASE
       WHEN cleanup_not_before IS NULL OR cleanup_not_before < discovered_at THEN discovered_at
       ELSE cleanup_not_before
   END
 WHERE cleanup_not_before IS NULL OR cleanup_not_before < discovered_at;
ALTER TABLE ducklake.snapshot_orphan
    ALTER COLUMN cleanup_not_before SET DEFAULT clock_timestamp(),
    ALTER COLUMN cleanup_not_before SET NOT NULL;
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
          FROM pg_constraint
         WHERE conrelid = 'ducklake.snapshot_orphan'::regclass
           AND conname = 'snapshot_orphan_cleanup_not_before_check'
    ) THEN
        ALTER TABLE ducklake.snapshot_orphan
            ADD CONSTRAINT snapshot_orphan_cleanup_not_before_check
            CHECK (cleanup_not_before >= discovered_at);
    END IF;
END $$;
-- +goose StatementEnd

CREATE INDEX IF NOT EXISTS ducklake_snapshot_orphan_backlog_idx
    ON ducklake.snapshot_orphan (physical_pool_id, catalog_id, state, discovered_at);

-- A snapshot-orphan scan is a durable, resumable walk over one exact
-- physical pool/catalog.  The catalog metadata lives in the separately
-- provisioned DuckLake database; this control-side ledger stores only the
-- bounded page identities and evidence supplied by that catalog adapter.
CREATE TABLE IF NOT EXISTS ducklake.snapshot_orphan_scan (
    scan_id             uuid PRIMARY KEY,
    physical_pool_id    text NOT NULL,
    catalog_id          text NOT NULL,
    owner_id            text NOT NULL,
    fencing_epoch       bigint NOT NULL CHECK (fencing_epoch > 0),
    page_size           integer NOT NULL CHECK (page_size BETWEEN 1 AND 256),
    grace_micros        bigint NOT NULL CHECK (grace_micros BETWEEN 1 AND 2592000000000),
    cursor_snapshot_id  bigint NOT NULL DEFAULT 0 CHECK (cursor_snapshot_id >= 0),
    pages_scanned       integer NOT NULL DEFAULT 0 CHECK (pages_scanned >= 0),
    snapshots_scanned   bigint NOT NULL DEFAULT 0 CHECK (snapshots_scanned >= 0),
    orphans_recorded    bigint NOT NULL DEFAULT 0 CHECK (orphans_recorded >= 0),
    state               text NOT NULL CHECK (state IN ('running','completed')),
    request_evidence    jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(request_evidence) = 'object' AND octet_length(request_evidence::text) <= 32768),
    completion_evidence jsonb,
    cleanup_not_before  timestamptz NOT NULL,
    pruned_at           timestamptz,
    pruned_page_count   integer NOT NULL DEFAULT 0 CHECK (pruned_page_count >= 0),
    pruned_page_digest  text NOT NULL DEFAULT '' CHECK (pruned_page_digest = '' OR pruned_page_digest ~ '^sha256:[0-9a-f]{64}$'),
    started_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    completed_at        timestamptz,
    FOREIGN KEY (physical_pool_id, catalog_id)
        REFERENCES ducklake.catalog_identity(physical_pool_id, catalog_id),
    UNIQUE (scan_id, physical_pool_id, catalog_id),
    CHECK (physical_pool_id = btrim(physical_pool_id) AND octet_length(physical_pool_id) BETWEEN 1 AND 255),
    CHECK (catalog_id = btrim(catalog_id) AND octet_length(catalog_id) BETWEEN 1 AND 255),
    CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255),
    CHECK (completed_at IS NULL OR (state = 'completed' AND completed_at >= started_at)),
    CHECK (cleanup_not_before >= started_at),
    CHECK (completion_evidence IS NULL OR (jsonb_typeof(completion_evidence) = 'object' AND octet_length(completion_evidence::text) <= 32768)),
    CHECK ((pruned_at IS NULL AND pruned_page_count = 0 AND pruned_page_digest = '') OR (state = 'completed' AND pruned_at IS NOT NULL AND pruned_page_count >= 0 AND pruned_page_digest ~ '^sha256:[0-9a-f]{64}$'))
);

CREATE TABLE IF NOT EXISTS ducklake.snapshot_orphan_scan_page (
    scan_id             uuid NOT NULL REFERENCES ducklake.snapshot_orphan_scan(scan_id) ON DELETE RESTRICT,
    physical_pool_id    text NOT NULL,
    catalog_id          text NOT NULL,
    page_number         integer NOT NULL CHECK (page_number > 0),
    cursor_before       bigint NOT NULL CHECK (cursor_before >= 0),
    cursor_after        bigint NOT NULL CHECK (cursor_after >= cursor_before),
    snapshot_ids        bigint[] NOT NULL,
    orphan_count        integer NOT NULL DEFAULT 0 CHECK (orphan_count >= 0 AND orphan_count <= 256),
    terminal            boolean NOT NULL DEFAULT false,
    page_digest         text NOT NULL CHECK (page_digest ~ '^sha256:[0-9a-f]{64}$'),
    evidence            jsonb NOT NULL
        CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 32768),
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (scan_id, page_number),
    FOREIGN KEY (scan_id, physical_pool_id, catalog_id)
        REFERENCES ducklake.snapshot_orphan_scan(scan_id, physical_pool_id, catalog_id),
    CHECK (cardinality(snapshot_ids) <= 256),
    CHECK (snapshot_ids = '{}'::bigint[] OR snapshot_ids[1] > cursor_before),
    CHECK (snapshot_ids = '{}'::bigint[] OR snapshot_ids[array_length(snapshot_ids,1)] <= cursor_after)
);

CREATE UNIQUE INDEX IF NOT EXISTS ducklake_snapshot_orphan_scan_page_cursor_idx
    ON ducklake.snapshot_orphan_scan_page (scan_id, cursor_before, cursor_after);

CREATE INDEX IF NOT EXISTS ducklake_snapshot_orphan_scan_backlog_idx
    ON ducklake.snapshot_orphan_scan (physical_pool_id, catalog_id, state, updated_at);

-- The clean-slate upgrade authority is deliberately separate from the
-- catalog identity and serving evidence above.  Runtime attachments only
-- read these tables; only the dedicated migrator role receives write access.
-- The tuple is stored in bounded typed columns rather than hidden in JSON so
-- an attach can compare every component exactly.
CREATE TABLE IF NOT EXISTS ducklake.catalog_runtime_compatibility (
    physical_pool_id       text PRIMARY KEY,
    catalog_id             text NOT NULL,
    duckdb_runtime         text NOT NULL,
    ducklake_extension     text NOT NULL,
    catalog_format         text NOT NULL,
    compatibility_digest   text NOT NULL CHECK (compatibility_digest ~ '^sha256:[0-9a-f]{64}$'),
    catalog_schema_version text NOT NULL,
    current_migration_id  uuid,
    updated_at             timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (physical_pool_id, catalog_id)
        REFERENCES ducklake.catalog_identity(physical_pool_id, catalog_id),
    CHECK (catalog_id = btrim(catalog_id) AND octet_length(catalog_id) BETWEEN 1 AND 255),
    CHECK (duckdb_runtime = btrim(duckdb_runtime) AND octet_length(duckdb_runtime) BETWEEN 1 AND 255),
    CHECK (ducklake_extension = btrim(ducklake_extension) AND octet_length(ducklake_extension) BETWEEN 1 AND 255),
    CHECK (catalog_format = btrim(catalog_format) AND octet_length(catalog_format) BETWEEN 1 AND 255),
    CHECK (catalog_schema_version = btrim(catalog_schema_version) AND octet_length(catalog_schema_version) BETWEEN 1 AND 128)
);

-- A row exists for the global fence (physical_pool_id = '') and one row per
-- pool.  Epochs never move backwards; expiry only permits a successor claim
-- after the bounded lease has elapsed.  Global acquisition serializes with
-- pool acquisition in the repository by locking the global row first.
CREATE TABLE IF NOT EXISTS ducklake.migration_fence (
    scope                 text NOT NULL CHECK (scope IN ('global', 'pool')),
    physical_pool_id      text NOT NULL DEFAULT '',
    owner_id              text,
    fencing_epoch         bigint NOT NULL DEFAULT 0 CHECK (fencing_epoch >= 0),
    lease_expires_at      timestamptz,
    updated_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (scope, physical_pool_id),
    CHECK ((scope = 'global' AND physical_pool_id = '') OR
           (scope = 'pool' AND physical_pool_id = btrim(physical_pool_id) AND octet_length(physical_pool_id) BETWEEN 1 AND 255)),
    CHECK ((owner_id IS NULL AND lease_expires_at IS NULL) OR
           (owner_id IS NOT NULL AND owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255 AND lease_expires_at IS NOT NULL))
);

INSERT INTO ducklake.migration_fence(scope, physical_pool_id)
VALUES ('global', '') ON CONFLICT (scope, physical_pool_id) DO NOTHING;

-- Catalog-wide retention has its own authority.  It deliberately does not
-- reuse migration_fence: snapshot expiry and physical-file cleanup are a
-- resumable maintenance operation, not a catalog schema migration.  The
-- composite key prevents two workers from expiring the same pool/catalog
-- concurrently while the monotonically increasing epoch fences stale
-- workers after lease takeover.
CREATE TABLE IF NOT EXISTS ducklake.pool_maintenance_fence (
    physical_pool_id       text NOT NULL,
    catalog_id             text NOT NULL,
    owner_id               text,
    fencing_epoch          bigint NOT NULL DEFAULT 0 CHECK (fencing_epoch >= 0),
    lease_expires_at       timestamptz,
    updated_at             timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (physical_pool_id, catalog_id),
    FOREIGN KEY (physical_pool_id, catalog_id)
        REFERENCES ducklake.catalog_identity(physical_pool_id, catalog_id),
    CHECK (physical_pool_id = btrim(physical_pool_id) AND octet_length(physical_pool_id) BETWEEN 1 AND 255),
    CHECK (catalog_id = btrim(catalog_id) AND octet_length(catalog_id) BETWEEN 1 AND 255),
    CHECK ((owner_id IS NULL AND lease_expires_at IS NULL) OR
           (owner_id IS NOT NULL AND owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255 AND lease_expires_at IS NOT NULL))
);

-- A maintenance run and its exact snapshot set survive worker crashes.  The
-- operation row records catalog-wide phase progress; child rows carry the
-- per-snapshot evidence needed for exact replay and audit.
CREATE TABLE IF NOT EXISTS ducklake.retention_maintenance (
    maintenance_id    uuid PRIMARY KEY,
    physical_pool_id  text NOT NULL,
    catalog_id        text NOT NULL,
    owner_id           text NOT NULL,
    fencing_epoch      bigint NOT NULL CHECK (fencing_epoch > 0),
    state              text NOT NULL CHECK (state IN ('running','completed','failed')),
    phase              text NOT NULL CHECK (phase IN ('expiry','old-files','orphans','completed')),
    dry_run            boolean NOT NULL,
    file_grace_micros  bigint NOT NULL CHECK (file_grace_micros > 0),
    snapshot_set_digest text NOT NULL DEFAULT '' CHECK (snapshot_set_digest = '' OR snapshot_set_digest ~ '^sha256:[0-9a-f]{64}$'),
    phase_evidence     jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(phase_evidence) = 'object' AND octet_length(phase_evidence::text) <= 32768),
    started_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    completed_at       timestamptz,
    FOREIGN KEY (physical_pool_id, catalog_id)
        REFERENCES ducklake.catalog_identity(physical_pool_id, catalog_id),
    UNIQUE (maintenance_id, physical_pool_id, catalog_id),
    CHECK (completed_at IS NULL OR state IN ('completed','failed'))
);

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='ducklake.snapshot_retention'::regclass AND conname='snapshot_retention_maintenance_claim_fk') THEN
        ALTER TABLE ducklake.snapshot_retention
            ADD CONSTRAINT snapshot_retention_maintenance_claim_fk
            FOREIGN KEY (retention_claim_id, physical_pool_id, catalog_id)
            REFERENCES ducklake.retention_maintenance(maintenance_id, physical_pool_id, catalog_id);
    END IF;
END $$;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS ducklake.retention_maintenance_snapshot (
    maintenance_id      uuid NOT NULL REFERENCES ducklake.retention_maintenance(maintenance_id) ON DELETE RESTRICT,
    physical_pool_id    text NOT NULL,
    catalog_id          text NOT NULL,
    snapshot_id         bigint NOT NULL CHECK (snapshot_id > 0),
    phase                text NOT NULL CHECK (phase IN ('eligible','expired','quarantined','cleanup-complete')),
    expiry_evidence     jsonb,
    quarantine_evidence jsonb,
    cleanup_evidence    jsonb,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (maintenance_id, physical_pool_id, catalog_id, snapshot_id),
    FOREIGN KEY (physical_pool_id, catalog_id, snapshot_id)
        REFERENCES ducklake.snapshot_retention(physical_pool_id, catalog_id, snapshot_id),
    FOREIGN KEY (maintenance_id, physical_pool_id, catalog_id)
        REFERENCES ducklake.retention_maintenance(maintenance_id, physical_pool_id, catalog_id),
    CHECK (expiry_evidence IS NULL OR (jsonb_typeof(expiry_evidence) = 'object' AND octet_length(expiry_evidence::text) <= 32768)),
    CHECK (quarantine_evidence IS NULL OR (jsonb_typeof(quarantine_evidence) = 'object' AND octet_length(quarantine_evidence::text) <= 32768)),
    CHECK (cleanup_evidence IS NULL OR (jsonb_typeof(cleanup_evidence) = 'object' AND octet_length(cleanup_evidence::text) <= 32768)),
    CHECK ((phase = 'eligible' AND expiry_evidence IS NULL AND quarantine_evidence IS NULL AND cleanup_evidence IS NULL)
        OR phase <> 'eligible'),
    CHECK ((phase IN ('expired','quarantined','cleanup-complete') AND expiry_evidence IS NOT NULL) OR phase IN ('eligible')),
    CHECK ((phase IN ('quarantined','cleanup-complete') AND quarantine_evidence IS NOT NULL) OR phase IN ('eligible','expired')),
    CHECK ((phase = 'cleanup-complete' AND cleanup_evidence IS NOT NULL) OR phase <> 'cleanup-complete')
);

CREATE INDEX IF NOT EXISTS ducklake_retention_maintenance_snapshot_idx
    ON ducklake.retention_maintenance_snapshot (physical_pool_id, catalog_id, phase, snapshot_id);

-- One immutable row records the exact before/after tuple and every lifecycle
-- outcome.  Completion/failure evidence is append-only through the guarded
-- repository transitions; a decision is mandatory for failed migrations so
-- operators can distinguish rollback from forward recovery.
CREATE TABLE IF NOT EXISTS ducklake.catalog_migration (
    migration_id          uuid PRIMARY KEY,
    physical_pool_id      text NOT NULL,
    catalog_id            text NOT NULL,
    owner_id              text NOT NULL,
    fencing_epoch         bigint NOT NULL CHECK (fencing_epoch > 0),
    global_fencing_epoch  bigint NOT NULL CHECK (global_fencing_epoch > 0),
    current_duckdb_runtime text NOT NULL,
    current_ducklake_extension text NOT NULL,
    current_catalog_format text NOT NULL,
    current_compatibility_digest text NOT NULL CHECK (current_compatibility_digest ~ '^sha256:[0-9a-f]{64}$'),
    current_catalog_schema_version text NOT NULL,
    target_duckdb_runtime text NOT NULL,
    target_ducklake_extension text NOT NULL,
    target_catalog_format text NOT NULL,
    target_compatibility_digest text NOT NULL CHECK (target_compatibility_digest ~ '^sha256:[0-9a-f]{64}$'),
    target_catalog_schema_version text NOT NULL,
    state                 text NOT NULL CHECK (state IN ('running', 'completed', 'failed')),
    started_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    terminal_at           timestamptz,
    begin_evidence        jsonb NOT NULL,
    completion_evidence   jsonb,
    failure_evidence      jsonb,
    recovery_decision     text CHECK (recovery_decision IS NULL OR recovery_decision IN ('rollback', 'forward_recovery')),
    decision_evidence     jsonb,
    FOREIGN KEY (physical_pool_id, catalog_id)
        REFERENCES ducklake.catalog_identity(physical_pool_id, catalog_id),
    CHECK (physical_pool_id = btrim(physical_pool_id) AND octet_length(physical_pool_id) BETWEEN 1 AND 255),
    CHECK (catalog_id = btrim(catalog_id) AND octet_length(catalog_id) BETWEEN 1 AND 255),
    CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255),
    CHECK (current_catalog_schema_version = btrim(current_catalog_schema_version) AND octet_length(current_catalog_schema_version) BETWEEN 1 AND 128),
    CHECK (target_catalog_schema_version = btrim(target_catalog_schema_version) AND octet_length(target_catalog_schema_version) BETWEEN 1 AND 128),
    CHECK (octet_length(current_duckdb_runtime) BETWEEN 1 AND 255 AND current_duckdb_runtime = btrim(current_duckdb_runtime)),
    CHECK (octet_length(current_ducklake_extension) BETWEEN 1 AND 255 AND current_ducklake_extension = btrim(current_ducklake_extension)),
    CHECK (octet_length(current_catalog_format) BETWEEN 1 AND 255 AND current_catalog_format = btrim(current_catalog_format)),
    CHECK (octet_length(target_duckdb_runtime) BETWEEN 1 AND 255 AND target_duckdb_runtime = btrim(target_duckdb_runtime)),
    CHECK (octet_length(target_ducklake_extension) BETWEEN 1 AND 255 AND target_ducklake_extension = btrim(target_ducklake_extension)),
    CHECK (octet_length(target_catalog_format) BETWEEN 1 AND 255 AND target_catalog_format = btrim(target_catalog_format)),
    CHECK ((state = 'running' AND terminal_at IS NULL AND completion_evidence IS NULL AND failure_evidence IS NULL AND recovery_decision IS NULL AND decision_evidence IS NULL) OR
           (state <> 'running' AND terminal_at IS NOT NULL)),
    CHECK (jsonb_typeof(begin_evidence) = 'object' AND begin_evidence <> '{}'::jsonb AND octet_length(begin_evidence::text) BETWEEN 2 AND 32768),
    CHECK (completion_evidence IS NULL OR (jsonb_typeof(completion_evidence) = 'object' AND completion_evidence <> '{}'::jsonb AND octet_length(completion_evidence::text) BETWEEN 2 AND 32768)),
    CHECK (failure_evidence IS NULL OR (jsonb_typeof(failure_evidence) = 'object' AND failure_evidence <> '{}'::jsonb AND octet_length(failure_evidence::text) BETWEEN 2 AND 32768)),
    CHECK (decision_evidence IS NULL OR (jsonb_typeof(decision_evidence) = 'object' AND decision_evidence <> '{}'::jsonb AND octet_length(decision_evidence::text) BETWEEN 2 AND 32768)),
    CHECK ((state <> 'completed' OR (completion_evidence IS NOT NULL AND recovery_decision IS NULL AND decision_evidence IS NULL)) AND
           (state <> 'failed' OR (failure_evidence IS NOT NULL AND recovery_decision IS NOT NULL AND decision_evidence IS NOT NULL)))
);

CREATE UNIQUE INDEX IF NOT EXISTS ducklake_catalog_migration_identity_idx
    ON ducklake.catalog_migration (migration_id, physical_pool_id, catalog_id);

CREATE UNIQUE INDEX IF NOT EXISTS ducklake_catalog_migration_running_idx
    ON ducklake.catalog_migration (physical_pool_id, catalog_id) WHERE state = 'running';

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conname = 'catalog_runtime_compatibility_migration_fk'
           AND conrelid = 'ducklake.catalog_runtime_compatibility'::regclass
    ) THEN
        ALTER TABLE ducklake.catalog_runtime_compatibility
            ADD CONSTRAINT catalog_runtime_compatibility_migration_fk
            FOREIGN KEY (current_migration_id, physical_pool_id, catalog_id)
            REFERENCES ducklake.catalog_migration (migration_id, physical_pool_id, catalog_id);
    END IF;
END
$$;
-- +goose StatementEnd

-- Each retained/active snapshot gets immutable evidence under the target
-- tuple.  Runtime attach checks every live/retiring retention row against the
-- current compatibility row; a missing or rejected qualification fails closed.
CREATE TABLE IF NOT EXISTS ducklake.snapshot_requalification (
    qualification_id       uuid PRIMARY KEY,
    physical_pool_id       text NOT NULL,
    catalog_id             text NOT NULL,
    snapshot_id            bigint NOT NULL CHECK (snapshot_id > 0),
    migration_id           uuid NOT NULL,
    duckdb_runtime         text NOT NULL,
    ducklake_extension     text NOT NULL,
    catalog_format         text NOT NULL,
    compatibility_digest   text NOT NULL CHECK (compatibility_digest ~ '^sha256:[0-9a-f]{64}$'),
    catalog_schema_version text NOT NULL,
    status                 text NOT NULL CHECK (status IN ('qualified', 'rejected')),
    evidence               jsonb NOT NULL CHECK (jsonb_typeof(evidence) = 'object' AND evidence <> '{}'::jsonb AND octet_length(evidence::text) BETWEEN 2 AND 32768),
    qualified_at           timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (physical_pool_id, catalog_id, snapshot_id)
        REFERENCES ducklake.snapshot_retention(physical_pool_id, catalog_id, snapshot_id),
    CHECK (physical_pool_id = btrim(physical_pool_id) AND octet_length(physical_pool_id) BETWEEN 1 AND 255),
    CHECK (catalog_id = btrim(catalog_id) AND octet_length(catalog_id) BETWEEN 1 AND 255),
    CHECK (octet_length(duckdb_runtime) BETWEEN 1 AND 255 AND duckdb_runtime = btrim(duckdb_runtime)),
    CHECK (octet_length(ducklake_extension) BETWEEN 1 AND 255 AND ducklake_extension = btrim(ducklake_extension)),
    CHECK (octet_length(catalog_format) BETWEEN 1 AND 255 AND catalog_format = btrim(catalog_format)),
    CHECK (catalog_schema_version = btrim(catalog_schema_version) AND octet_length(catalog_schema_version) BETWEEN 1 AND 128),
    UNIQUE (physical_pool_id, catalog_id, snapshot_id, migration_id)
);

-- Enforce that qualification evidence can only refer to the migration's exact
-- pool/catalog identity.  The migration epoch also lets runtime reject stale
-- evidence after a tuple cycle (A -> B -> A).
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conname = 'snapshot_requalification_migration_identity_fk'
           AND conrelid = 'ducklake.snapshot_requalification'::regclass
    ) THEN
        ALTER TABLE ducklake.snapshot_requalification
            ADD CONSTRAINT snapshot_requalification_migration_identity_fk
            FOREIGN KEY (migration_id, physical_pool_id, catalog_id)
            REFERENCES ducklake.catalog_migration (migration_id, physical_pool_id, catalog_id);
    END IF;
END
$$;
-- +goose StatementEnd

CREATE INDEX IF NOT EXISTS ducklake_snapshot_requalification_lookup_idx
    ON ducklake.snapshot_requalification (physical_pool_id, catalog_id, snapshot_id, status, compatibility_digest, catalog_schema_version);

CREATE OR REPLACE FUNCTION ducklake.reject_immutable_change()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'DuckLake identity evidence is immutable';
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.reject_snapshot_retention_identity_change()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE'
       OR NEW.physical_pool_id <> OLD.physical_pool_id
       OR NEW.catalog_id <> OLD.catalog_id
       OR NEW.snapshot_id <> OLD.snapshot_id THEN
        RAISE EXCEPTION 'DuckLake snapshot retention identity is immutable';
    END IF;
    IF NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'DuckLake snapshot retention created_at is immutable';
    END IF;
    IF OLD.state = 'cleanup-complete' AND NEW.state <> OLD.state THEN
        RAISE EXCEPTION 'DuckLake cleanup-complete snapshot retention is immutable';
    END IF;
    IF OLD.state = 'quarantined' AND NEW.state NOT IN ('quarantined', 'cleanup-complete') THEN
        RAISE EXCEPTION 'DuckLake snapshot retention lifecycle is monotonic';
    END IF;
    IF OLD.state = 'expired' AND NEW.state NOT IN ('expired', 'quarantined', 'cleanup-complete') THEN
        RAISE EXCEPTION 'DuckLake snapshot retention lifecycle is monotonic';
    END IF;
    IF OLD.state = 'expiring' AND NEW.state NOT IN ('expiring', 'expired') THEN
        RAISE EXCEPTION 'DuckLake snapshot retention lifecycle is monotonic';
    END IF;
    IF OLD.state IN ('expired', 'quarantined', 'cleanup-complete') AND NEW.state = OLD.state AND (
           NEW.protected_until IS DISTINCT FROM OLD.protected_until
        OR NEW.retired_at IS DISTINCT FROM OLD.retired_at
        OR NEW.expired_at IS DISTINCT FROM OLD.expired_at
        OR NEW.evidence IS DISTINCT FROM OLD.evidence
        OR NEW.quarantined_at IS DISTINCT FROM OLD.quarantined_at
        OR NEW.cleanup_completed_at IS DISTINCT FROM OLD.cleanup_completed_at
        OR NEW.quarantine_evidence IS DISTINCT FROM OLD.quarantine_evidence
        OR NEW.cleanup_evidence IS DISTINCT FROM OLD.cleanup_evidence
        OR (NEW.cleanup_owner_id IS DISTINCT FROM OLD.cleanup_owner_id
            OR NEW.cleanup_fencing_epoch IS DISTINCT FROM OLD.cleanup_fencing_epoch
            OR NEW.cleanup_lease_expires_at IS DISTINCT FROM OLD.cleanup_lease_expires_at)
           AND NOT (NEW.state IN ('expired','quarantined')
                    AND NEW.cleanup_fencing_epoch > OLD.cleanup_fencing_epoch)) THEN
        RAISE EXCEPTION 'DuckLake expired snapshot retention is immutable';
    END IF;
    IF NEW.cleanup_fencing_epoch < OLD.cleanup_fencing_epoch THEN
        RAISE EXCEPTION 'DuckLake cleanup fencing epoch cannot move backwards';
    END IF;
    IF NEW.cleanup_fencing_epoch > OLD.cleanup_fencing_epoch
       AND (NEW.state NOT IN ('expired','quarantined') OR NEW.cleanup_owner_id IS NULL OR NEW.cleanup_lease_expires_at IS NULL) THEN
        RAISE EXCEPTION 'DuckLake cleanup claim requires an expiring snapshot';
    END IF;
    IF NEW.state = 'expiring'
       AND (NEW.retention_claim_id IS NULL OR NEW.retention_claim_owner_id IS NULL
            OR NEW.retention_claim_fencing_epoch <= 0 OR NEW.retention_claimed_at IS NULL) THEN
        RAISE EXCEPTION 'DuckLake expiring snapshot requires a maintenance claim';
    END IF;
    IF NEW.protected_until IS NOT NULL AND OLD.protected_until IS NOT NULL
       AND NEW.protected_until < OLD.protected_until THEN
        RAISE EXCEPTION 'DuckLake snapshot protection cannot move backwards';
    END IF;
    IF OLD.state IN ('retiring', 'expiring', 'expired', 'quarantined', 'cleanup-complete') AND NEW.state = 'live' THEN
        RAISE EXCEPTION 'DuckLake snapshot retention lifecycle is monotonic';
    END IF;
    IF OLD.state = 'retiring' AND NEW.state NOT IN ('retiring', 'expiring') THEN
        RAISE EXCEPTION 'DuckLake snapshot retention lifecycle is monotonic';
    END IF;
    IF OLD.state = 'live' AND NEW.state NOT IN ('live', 'retiring') THEN
        RAISE EXCEPTION 'DuckLake snapshot retention lifecycle is monotonic';
    END IF;
    IF OLD.state = 'live' AND NEW.state = 'retiring'
       AND EXISTS (
           -- A delivery root is physically attributable only through its
           -- immutable snapshot seal. Roots with a NULL seal are not mapped
           -- to a pool/catalog/snapshot by inference.
           SELECT 1
             FROM delivery.delivery_retention_root root
             JOIN delivery.delivery_snapshot_seal seal ON seal.seal_id = root.snapshot_seal_id
            WHERE seal.physical_pool_id=OLD.physical_pool_id
              AND seal.catalog_id=OLD.catalog_id
              AND seal.ducklake_snapshot_id=OLD.snapshot_id
              AND root.state IN ('live','retiring')) THEN
        RAISE EXCEPTION 'DuckLake snapshot durable roots must be released before retirement';
    END IF;
    IF NEW.evidence IS DISTINCT FROM OLD.evidence
       AND NOT (OLD.state IN ('retiring', 'expiring') AND NEW.state = 'expired') THEN
        RAISE EXCEPTION 'DuckLake snapshot retention evidence is immutable';
    END IF;
    IF NEW.state = OLD.state AND (
           NEW.retired_at IS DISTINCT FROM OLD.retired_at
        OR NEW.expired_at IS DISTINCT FROM OLD.expired_at) THEN
        RAISE EXCEPTION 'DuckLake snapshot retention lifecycle timestamps are immutable';
    END IF;
    IF OLD.state = 'live' AND NEW.state IN ('expiring', 'expired', 'quarantined', 'cleanup-complete') THEN
        RAISE EXCEPTION 'DuckLake snapshot must retire before expiration';
    END IF;
    IF OLD.state IN ('retiring', 'expiring') AND NEW.state = 'expired'
       AND (EXISTS (
               SELECT 1
                 FROM serving_state.reader_lease l
                 JOIN delivery.delivery_generation g ON g.generation_id = l.generation_id
                 JOIN delivery.delivery_snapshot_seal seal ON seal.seal_id = g.snapshot_seal_id
                WHERE seal.physical_pool_id=OLD.physical_pool_id
                  AND seal.catalog_id=OLD.catalog_id
                  AND seal.ducklake_snapshot_id=OLD.snapshot_id
                  AND l.released_at IS NULL)
            OR EXISTS (
               SELECT 1
                 FROM delivery.delivery_retention_root root
                 JOIN delivery.delivery_snapshot_seal seal ON seal.seal_id = root.snapshot_seal_id
                WHERE seal.physical_pool_id=OLD.physical_pool_id
                  AND seal.catalog_id=OLD.catalog_id
                  AND seal.ducklake_snapshot_id=OLD.snapshot_id
                  AND root.state IN ('live','retiring'))
            ) THEN
        RAISE EXCEPTION 'canonical snapshot protections remain';
    END IF;
    IF NEW.state = 'cleanup-complete' AND (OLD.state <> 'quarantined' OR NEW.cleanup_completed_at IS NULL OR NEW.cleanup_evidence IS NULL) THEN
        RAISE EXCEPTION 'DuckLake snapshot must be quarantined before cleanup-complete';
    END IF;
    IF NEW.state = 'quarantined' AND (NEW.expired_at IS NULL OR NEW.quarantined_at IS NULL OR NEW.quarantine_evidence IS NULL) THEN
        RAISE EXCEPTION 'DuckLake quarantined snapshot requires expired_at';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.reject_snapshot_orphan_identity_change()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE'
       OR NEW.orphan_id <> OLD.orphan_id
       OR NEW.physical_pool_id <> OLD.physical_pool_id
       OR NEW.catalog_id <> OLD.catalog_id
       OR NEW.snapshot_id <> OLD.snapshot_id
       OR NEW.discovered_at IS DISTINCT FROM OLD.discovered_at
       OR NEW.cleanup_not_before IS DISTINCT FROM OLD.cleanup_not_before THEN
        RAISE EXCEPTION 'DuckLake snapshot orphan identity is immutable';
    END IF;
    IF OLD.state = 'cleanup-complete' THEN
        IF NEW.state <> OLD.state OR NEW.evidence IS DISTINCT FROM OLD.evidence
           OR NEW.resolved_at IS DISTINCT FROM OLD.resolved_at
           OR NEW.cleanup_owner_id IS DISTINCT FROM OLD.cleanup_owner_id
           OR NEW.cleanup_fencing_epoch IS DISTINCT FROM OLD.cleanup_fencing_epoch
           OR NEW.cleanup_lease_expires_at IS DISTINCT FROM OLD.cleanup_lease_expires_at THEN
            RAISE EXCEPTION 'DuckLake cleanup-complete snapshot orphan is immutable';
        END IF;
    ELSIF OLD.state = 'quarantined' AND NEW.state NOT IN ('quarantined', 'cleanup-complete') THEN
        RAISE EXCEPTION 'DuckLake snapshot orphan lifecycle is monotonic';
    ELSIF OLD.state = 'quarantined' AND NEW.state = 'quarantined'
          AND (NEW.evidence IS DISTINCT FROM OLD.evidence OR NEW.resolved_at IS DISTINCT FROM OLD.resolved_at
               OR ((NEW.cleanup_owner_id IS DISTINCT FROM OLD.cleanup_owner_id
                    OR NEW.cleanup_lease_expires_at IS DISTINCT FROM OLD.cleanup_lease_expires_at)
                   AND NOT (NEW.cleanup_fencing_epoch > OLD.cleanup_fencing_epoch))) THEN
        RAISE EXCEPTION 'DuckLake snapshot orphan evidence is immutable';
    ELSIF NEW.state = 'cleanup-complete' AND NEW.resolved_at IS NULL THEN
        RAISE EXCEPTION 'DuckLake cleanup-complete snapshot orphan requires resolved_at';
    END IF;
    IF NEW.cleanup_fencing_epoch < OLD.cleanup_fencing_epoch THEN
        RAISE EXCEPTION 'DuckLake orphan cleanup fencing epoch cannot move backwards';
    END IF;
    IF NEW.cleanup_fencing_epoch > OLD.cleanup_fencing_epoch
       AND (NEW.state <> 'quarantined' OR NEW.cleanup_owner_id IS NULL OR NEW.cleanup_lease_expires_at IS NULL) THEN
        RAISE EXCEPTION 'DuckLake orphan cleanup claim requires a quarantined orphan';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.reject_snapshot_orphan_scan_change()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, ducklake AS $$
BEGIN
    IF TG_OP = 'DELETE'
       OR NEW.scan_id <> OLD.scan_id
       OR NEW.physical_pool_id <> OLD.physical_pool_id
       OR NEW.catalog_id <> OLD.catalog_id
       OR NEW.page_size <> OLD.page_size
       OR NEW.grace_micros <> OLD.grace_micros
       OR NEW.cleanup_not_before <> OLD.cleanup_not_before
       OR NEW.started_at <> OLD.started_at
       OR NEW.request_evidence IS DISTINCT FROM OLD.request_evidence THEN
        RAISE EXCEPTION 'DuckLake snapshot orphan scan identity is immutable';
    END IF;
    IF OLD.state = 'completed' AND
       (NEW.state <> OLD.state OR NEW.cursor_snapshot_id <> OLD.cursor_snapshot_id
        OR NEW.pages_scanned <> OLD.pages_scanned OR NEW.snapshots_scanned <> OLD.snapshots_scanned
        OR NEW.orphans_recorded <> OLD.orphans_recorded OR NEW.updated_at IS DISTINCT FROM OLD.updated_at
        OR NEW.completed_at IS DISTINCT FROM OLD.completed_at
        OR NEW.completion_evidence IS DISTINCT FROM OLD.completion_evidence
        OR (NEW.pruned_at IS DISTINCT FROM OLD.pruned_at
            OR NEW.pruned_page_count <> OLD.pruned_page_count
            OR NEW.pruned_page_digest <> OLD.pruned_page_digest)
           AND NOT (OLD.pruned_at IS NULL AND NEW.pruned_at IS NOT NULL
                    AND NEW.pruned_page_count >= 0
                    AND NEW.pruned_page_digest ~ '^sha256:[0-9a-f]{64}$')) THEN
        RAISE EXCEPTION 'DuckLake terminal snapshot orphan scan is immutable';
    END IF;
    IF NEW.fencing_epoch < OLD.fencing_epoch
       OR (NEW.fencing_epoch = OLD.fencing_epoch AND NEW.owner_id IS DISTINCT FROM OLD.owner_id) THEN
        RAISE EXCEPTION 'DuckLake snapshot orphan scan fence epoch cannot move backwards';
    END IF;
    IF NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'DuckLake snapshot orphan scan updated_at cannot move backwards';
    END IF;
    IF NEW.cursor_snapshot_id < OLD.cursor_snapshot_id
       OR NEW.pages_scanned < OLD.pages_scanned
       OR NEW.snapshots_scanned < OLD.snapshots_scanned
       OR NEW.orphans_recorded < OLD.orphans_recorded THEN
        RAISE EXCEPTION 'DuckLake snapshot orphan scan progress cannot move backwards';
    END IF;
    IF (NEW.state = 'running' AND (NEW.completed_at IS NOT NULL OR NEW.completion_evidence IS NOT NULL))
       OR (NEW.state = 'completed' AND (NEW.completed_at IS NULL OR NEW.completion_evidence IS NULL)) THEN
        RAISE EXCEPTION 'DuckLake snapshot orphan scan terminal evidence is inconsistent';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.reject_snapshot_orphan_scan_page_change()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, ducklake AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF current_setting('ducklake.scan_prune', true) = 'on' THEN RETURN OLD; END IF;
        RAISE EXCEPTION 'DuckLake snapshot orphan scan page evidence is immutable';
    END IF;
    IF TG_OP <> 'UPDATE'
       OR NEW.scan_id <> OLD.scan_id
       OR NEW.physical_pool_id <> OLD.physical_pool_id
       OR NEW.catalog_id <> OLD.catalog_id
       OR NEW.page_number <> OLD.page_number
       OR NEW.cursor_before <> OLD.cursor_before
       OR NEW.cursor_after <> OLD.cursor_after
       OR NEW.snapshot_ids IS DISTINCT FROM OLD.snapshot_ids
       OR NEW.orphan_count <> OLD.orphan_count
       OR NEW.terminal <> OLD.terminal
       OR NEW.page_digest <> OLD.page_digest
       OR NEW.evidence IS DISTINCT FROM OLD.evidence
       OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'DuckLake snapshot orphan scan page evidence is immutable';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS catalog_identity_immutable ON ducklake.catalog_identity;
CREATE TRIGGER catalog_identity_immutable
    BEFORE UPDATE OR DELETE ON ducklake.catalog_identity
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_immutable_change();

DROP TRIGGER IF EXISTS marker_quarantine_immutable ON ducklake.marker_quarantine;
CREATE TRIGGER marker_quarantine_immutable
    BEFORE UPDATE OR DELETE ON ducklake.marker_quarantine
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_immutable_change();

DROP TRIGGER IF EXISTS snapshot_retention_identity_immutable ON ducklake.snapshot_retention;
CREATE TRIGGER snapshot_retention_identity_immutable
    BEFORE UPDATE OR DELETE ON ducklake.snapshot_retention
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_snapshot_retention_identity_change();

DROP TRIGGER IF EXISTS snapshot_orphan_identity_immutable ON ducklake.snapshot_orphan;
CREATE TRIGGER snapshot_orphan_identity_immutable
    BEFORE UPDATE OR DELETE ON ducklake.snapshot_orphan
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_snapshot_orphan_identity_change();

DROP TRIGGER IF EXISTS snapshot_orphan_scan_immutable ON ducklake.snapshot_orphan_scan;
CREATE TRIGGER snapshot_orphan_scan_immutable
    BEFORE UPDATE OR DELETE ON ducklake.snapshot_orphan_scan
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_snapshot_orphan_scan_change();

DROP TRIGGER IF EXISTS snapshot_orphan_scan_page_immutable ON ducklake.snapshot_orphan_scan_page;
CREATE TRIGGER snapshot_orphan_scan_page_immutable
    BEFORE UPDATE OR DELETE ON ducklake.snapshot_orphan_scan_page
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_snapshot_orphan_scan_page_change();

CREATE OR REPLACE FUNCTION ducklake.reject_catalog_migration_change()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, ducklake AS $$
BEGIN
    IF TG_OP = 'DELETE'
       OR NEW.migration_id <> OLD.migration_id
       OR NEW.physical_pool_id <> OLD.physical_pool_id
       OR NEW.catalog_id <> OLD.catalog_id
       OR NEW.owner_id <> OLD.owner_id
       OR NEW.fencing_epoch <> OLD.fencing_epoch
       OR NEW.global_fencing_epoch <> OLD.global_fencing_epoch
       OR NEW.current_duckdb_runtime <> OLD.current_duckdb_runtime
       OR NEW.current_ducklake_extension <> OLD.current_ducklake_extension
       OR NEW.current_catalog_format <> OLD.current_catalog_format
       OR NEW.current_compatibility_digest <> OLD.current_compatibility_digest
       OR NEW.current_catalog_schema_version <> OLD.current_catalog_schema_version
       OR NEW.target_duckdb_runtime <> OLD.target_duckdb_runtime
       OR NEW.target_ducklake_extension <> OLD.target_ducklake_extension
       OR NEW.target_catalog_format <> OLD.target_catalog_format
       OR NEW.target_compatibility_digest <> OLD.target_compatibility_digest
       OR NEW.target_catalog_schema_version <> OLD.target_catalog_schema_version
       OR NEW.started_at <> OLD.started_at
       OR NEW.begin_evidence IS DISTINCT FROM OLD.begin_evidence THEN
        RAISE EXCEPTION 'DuckLake catalog migration identity is immutable';
    END IF;
    IF OLD.state <> 'running' THEN
        IF NEW.state <> OLD.state
           OR NEW.terminal_at IS DISTINCT FROM OLD.terminal_at
           OR NEW.completion_evidence IS DISTINCT FROM OLD.completion_evidence
           OR NEW.failure_evidence IS DISTINCT FROM OLD.failure_evidence
           OR NEW.recovery_decision IS DISTINCT FROM OLD.recovery_decision
           OR NEW.decision_evidence IS DISTINCT FROM OLD.decision_evidence THEN
            RAISE EXCEPTION 'DuckLake terminal catalog migration is immutable';
        END IF;
    ELSIF NEW.state = 'running' AND (NEW.terminal_at IS NOT NULL OR NEW.completion_evidence IS NOT NULL OR NEW.failure_evidence IS NOT NULL OR NEW.recovery_decision IS NOT NULL OR NEW.decision_evidence IS NOT NULL) THEN
        RAISE EXCEPTION 'running DuckLake catalog migration cannot carry terminal evidence';
    END IF;
    IF OLD.state = 'running' AND NEW.state NOT IN ('running', 'completed', 'failed') THEN
        RAISE EXCEPTION 'DuckLake catalog migration lifecycle is monotonic';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.reject_snapshot_requalification_change()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, ducklake AS $$
BEGIN
    IF TG_OP = 'DELETE'
       OR NEW.qualification_id <> OLD.qualification_id
       OR NEW.physical_pool_id <> OLD.physical_pool_id
       OR NEW.catalog_id <> OLD.catalog_id
       OR NEW.snapshot_id <> OLD.snapshot_id
       OR NEW.migration_id <> OLD.migration_id
       OR NEW.duckdb_runtime <> OLD.duckdb_runtime
       OR NEW.ducklake_extension <> OLD.ducklake_extension
       OR NEW.catalog_format <> OLD.catalog_format
       OR NEW.compatibility_digest <> OLD.compatibility_digest
       OR NEW.catalog_schema_version <> OLD.catalog_schema_version
       OR NEW.qualified_at <> OLD.qualified_at
       OR NEW.evidence IS DISTINCT FROM OLD.evidence
       OR NEW.status <> OLD.status THEN
        RAISE EXCEPTION 'DuckLake snapshot requalification evidence is immutable';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.reject_migration_fence_change()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, ducklake AS $$
BEGIN
    IF TG_OP = 'DELETE'
       OR NEW.scope <> OLD.scope
       OR NEW.physical_pool_id <> OLD.physical_pool_id THEN
        RAISE EXCEPTION 'DuckLake migration fence identity is immutable';
    END IF;
    IF NEW.fencing_epoch < OLD.fencing_epoch THEN
        RAISE EXCEPTION 'DuckLake migration fencing epoch cannot move backwards';
    END IF;
    IF NEW.fencing_epoch = OLD.fencing_epoch
       AND NEW.owner_id IS DISTINCT FROM OLD.owner_id
       AND NOT (NEW.owner_id IS NULL AND NEW.lease_expires_at IS NULL) THEN
        RAISE EXCEPTION 'DuckLake migration fence owner change requires a new epoch';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS catalog_migration_immutable ON ducklake.catalog_migration;
CREATE TRIGGER catalog_migration_immutable
    BEFORE UPDATE OR DELETE ON ducklake.catalog_migration
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_catalog_migration_change();

DROP TRIGGER IF EXISTS snapshot_requalification_immutable ON ducklake.snapshot_requalification;
CREATE TRIGGER snapshot_requalification_immutable
    BEFORE UPDATE OR DELETE ON ducklake.snapshot_requalification
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_snapshot_requalification_change();

DROP TRIGGER IF EXISTS migration_fence_monotonic ON ducklake.migration_fence;
CREATE TRIGGER migration_fence_monotonic
    BEFORE UPDATE OR DELETE ON ducklake.migration_fence
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_migration_fence_change();

CREATE OR REPLACE FUNCTION ducklake.reject_pool_maintenance_fence_change()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, ducklake AS $$
BEGIN
    IF TG_OP = 'DELETE'
       OR NEW.physical_pool_id <> OLD.physical_pool_id
       OR NEW.catalog_id <> OLD.catalog_id THEN
        RAISE EXCEPTION 'DuckLake pool maintenance fence identity is immutable';
    END IF;
    IF NEW.fencing_epoch < OLD.fencing_epoch THEN
        RAISE EXCEPTION 'DuckLake pool maintenance fencing epoch cannot move backwards';
    END IF;
    IF NEW.fencing_epoch = OLD.fencing_epoch
       AND NEW.owner_id IS DISTINCT FROM OLD.owner_id
       AND NOT (NEW.owner_id IS NULL AND NEW.lease_expires_at IS NULL) THEN
        RAISE EXCEPTION 'DuckLake pool maintenance fence owner change requires a new epoch';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.reject_retention_maintenance_change()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, ducklake AS $$
BEGIN
    IF TG_OP = 'DELETE'
       OR NEW.maintenance_id <> OLD.maintenance_id
       OR NEW.physical_pool_id <> OLD.physical_pool_id
       OR NEW.catalog_id <> OLD.catalog_id
       OR NEW.started_at <> OLD.started_at THEN
        RAISE EXCEPTION 'DuckLake retention maintenance identity is immutable';
    END IF;
    IF OLD.state IN ('completed','failed') AND
       (NEW.state <> OLD.state OR NEW.phase <> OLD.phase
        OR NEW.owner_id <> OLD.owner_id OR NEW.fencing_epoch <> OLD.fencing_epoch
        OR NEW.dry_run <> OLD.dry_run OR NEW.file_grace_micros <> OLD.file_grace_micros
        OR NEW.snapshot_set_digest <> OLD.snapshot_set_digest
        OR NEW.phase_evidence IS DISTINCT FROM OLD.phase_evidence
        OR NEW.completed_at IS DISTINCT FROM OLD.completed_at) THEN
        RAISE EXCEPTION 'DuckLake terminal retention maintenance is immutable';
    END IF;
    IF OLD.state = 'running'
       AND (NEW.dry_run <> OLD.dry_run OR NEW.file_grace_micros <> OLD.file_grace_micros
            OR (OLD.snapshot_set_digest <> '' AND NEW.snapshot_set_digest <> OLD.snapshot_set_digest)) THEN
        RAISE EXCEPTION 'DuckLake retention maintenance request identity is immutable';
    END IF;
    IF NEW.fencing_epoch < OLD.fencing_epoch THEN
        RAISE EXCEPTION 'DuckLake retention maintenance fencing epoch cannot move backwards';
    END IF;
    IF NEW.state = 'completed' AND (NEW.phase <> 'completed' OR NEW.completed_at IS NULL) THEN
        RAISE EXCEPTION 'DuckLake completed retention maintenance requires terminal phase';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.reject_retention_maintenance_snapshot_change()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, ducklake AS $$
BEGIN
    IF TG_OP = 'DELETE'
       OR NEW.maintenance_id <> OLD.maintenance_id
       OR NEW.physical_pool_id <> OLD.physical_pool_id
       OR NEW.catalog_id <> OLD.catalog_id
       OR NEW.snapshot_id <> OLD.snapshot_id
       OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'DuckLake retention maintenance snapshot identity is immutable';
    END IF;
    IF OLD.phase = 'cleanup-complete' AND (NEW.phase <> OLD.phase OR NEW.expiry_evidence IS DISTINCT FROM OLD.expiry_evidence OR NEW.quarantine_evidence IS DISTINCT FROM OLD.quarantine_evidence OR NEW.cleanup_evidence IS DISTINCT FROM OLD.cleanup_evidence) THEN
        RAISE EXCEPTION 'DuckLake completed retention maintenance snapshot is immutable';
    END IF;
    IF OLD.phase = 'quarantined' AND NEW.phase NOT IN ('quarantined','cleanup-complete') THEN
        RAISE EXCEPTION 'DuckLake retention maintenance snapshot lifecycle is monotonic';
    END IF;
    IF OLD.phase = 'expired' AND NEW.phase NOT IN ('expired','quarantined','cleanup-complete') THEN
        RAISE EXCEPTION 'DuckLake retention maintenance snapshot lifecycle is monotonic';
    END IF;
    IF OLD.phase = 'eligible' AND NEW.phase NOT IN ('eligible','expired','quarantined') THEN
        RAISE EXCEPTION 'DuckLake retention maintenance snapshot lifecycle is monotonic';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS pool_maintenance_fence_monotonic ON ducklake.pool_maintenance_fence;
CREATE TRIGGER pool_maintenance_fence_monotonic
    BEFORE UPDATE OR DELETE ON ducklake.pool_maintenance_fence
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_pool_maintenance_fence_change();
DROP TRIGGER IF EXISTS retention_maintenance_immutable ON ducklake.retention_maintenance;
CREATE TRIGGER retention_maintenance_immutable
    BEFORE UPDATE OR DELETE ON ducklake.retention_maintenance
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_retention_maintenance_change();
DROP TRIGGER IF EXISTS retention_maintenance_snapshot_immutable ON ducklake.retention_maintenance_snapshot;
CREATE TRIGGER retention_maintenance_snapshot_immutable
    BEFORE UPDATE OR DELETE ON ducklake.retention_maintenance_snapshot
    FOR EACH ROW EXECUTE FUNCTION ducklake.reject_retention_maintenance_snapshot_change();

-- The control upgrade coordinator receives no direct DML on authority rows. These narrowly
-- scoped SECURITY DEFINER functions are the database capability boundary: all
-- fence claims, compatibility registration, lifecycle transitions, and
-- requalification writes re-check the active owner/epoch under PostgreSQL's
-- clock while holding the relevant rows.
CREATE OR REPLACE FUNCTION ducklake.acquire_migration_fence(
    p_scope text,
    p_physical_pool_id text,
    p_owner_id text,
    p_lease_expires_at timestamptz
) RETURNS TABLE(scope text, physical_pool_id text, owner_id text, fencing_epoch bigint, lease_expires_at timestamptz)
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE
    v_now timestamptz := clock_timestamp();
    v_lease timestamptz := COALESCE(p_lease_expires_at, v_now + interval '24 hours');
    v_owner text;
    v_epoch bigint;
    v_expiry timestamptz;
    v_global_owner text;
    v_global_epoch bigint;
    v_global_expiry timestamptz;
BEGIN
    IF p_scope NOT IN ('global','pool')
       OR (p_scope = 'global' AND p_physical_pool_id <> '')
       OR (p_scope = 'pool' AND (p_physical_pool_id = '' OR p_physical_pool_id <> btrim(p_physical_pool_id) OR octet_length(p_physical_pool_id) > 255))
       OR p_owner_id = '' OR p_owner_id <> btrim(p_owner_id) OR octet_length(p_owner_id) > 255
       OR v_lease <= v_now OR v_lease > v_now + interval '24 hours' THEN
        RAISE EXCEPTION 'invalid migration fence claim';
    END IF;
    INSERT INTO ducklake.migration_fence(scope,physical_pool_id)
    VALUES ('global','') ON CONFLICT DO NOTHING;
    SELECT f.owner_id,f.fencing_epoch,f.lease_expires_at
      INTO v_global_owner,v_global_epoch,v_global_expiry
      FROM ducklake.migration_fence f
     WHERE f.scope='global' AND f.physical_pool_id=''
     FOR UPDATE;
    IF v_global_owner IS NOT NULL AND v_global_expiry > v_now
       AND p_scope='pool' AND v_global_owner <> p_owner_id THEN
        RAISE EXCEPTION 'migration fence busy';
    END IF;
    IF p_scope='global' AND EXISTS (
        SELECT 1 FROM ducklake.migration_fence f
         WHERE f.scope='pool' AND f.owner_id IS NOT NULL AND f.lease_expires_at > v_now
    ) THEN
        RAISE EXCEPTION 'migration fence busy';
    END IF;
    IF p_scope='pool' THEN
        INSERT INTO ducklake.migration_fence(scope,physical_pool_id)
        VALUES ('pool',p_physical_pool_id) ON CONFLICT DO NOTHING;
    END IF;
    SELECT f.owner_id,f.fencing_epoch,f.lease_expires_at
      INTO v_owner,v_epoch,v_expiry
      FROM ducklake.migration_fence f
     WHERE f.scope=p_scope AND f.physical_pool_id=p_physical_pool_id
     FOR UPDATE;
    -- Retention acquisition locks global migration → pool migration →
    -- maintenance.  Use that same order to close the cross-authority race.
    IF p_scope = 'global' THEN
        PERFORM 1 FROM ducklake.pool_maintenance_fence f
         WHERE f.owner_id IS NOT NULL AND f.lease_expires_at > v_now
         FOR UPDATE;
    ELSE
        PERFORM 1 FROM ducklake.pool_maintenance_fence f
         WHERE f.physical_pool_id=p_physical_pool_id
           AND f.owner_id IS NOT NULL AND f.lease_expires_at > v_now
         FOR UPDATE;
    END IF;
    IF FOUND THEN
        RAISE EXCEPTION 'migration fence busy';
    END IF;
    IF v_owner IS NOT NULL AND v_expiry > v_now THEN
        IF v_owner = p_owner_id THEN
            RETURN QUERY SELECT p_scope,p_physical_pool_id,v_owner,v_epoch,v_expiry;
            RETURN;
        END IF;
        RAISE EXCEPTION 'migration fence busy';
    END IF;
    UPDATE ducklake.migration_fence f
       SET owner_id=p_owner_id, fencing_epoch=f.fencing_epoch+1,
           lease_expires_at=v_lease, updated_at=v_now
     WHERE f.scope=p_scope AND f.physical_pool_id=p_physical_pool_id
     RETURNING f.owner_id,f.fencing_epoch,f.lease_expires_at
      INTO v_owner,v_epoch,v_expiry;
    RETURN QUERY SELECT p_scope,p_physical_pool_id,v_owner,v_epoch,v_expiry;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.release_migration_fence(
    p_scope text, p_physical_pool_id text, p_owner_id text, p_fencing_epoch bigint
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE v_owner text; v_epoch bigint;
BEGIN
    UPDATE ducklake.migration_fence
       SET owner_id=NULL, lease_expires_at=NULL, updated_at=clock_timestamp()
     WHERE scope=p_scope AND physical_pool_id=p_physical_pool_id
       AND owner_id=p_owner_id AND fencing_epoch=p_fencing_epoch;
    IF FOUND THEN RETURN; END IF;
    SELECT owner_id,fencing_epoch INTO v_owner,v_epoch
      FROM ducklake.migration_fence
     WHERE scope=p_scope AND physical_pool_id=p_physical_pool_id;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence not found'; END IF;
    IF v_owner IS NULL AND v_epoch=p_fencing_epoch THEN RETURN; END IF;
    RAISE EXCEPTION 'migration fence stale';
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.renew_migration_fence(
    p_scope text, p_physical_pool_id text, p_owner_id text,
    p_fencing_epoch bigint, p_lease_expires_at timestamptz
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE
    v_now timestamptz := clock_timestamp();
    v_lease timestamptz := COALESCE(p_lease_expires_at, v_now + interval '24 hours');
    v_owner text; v_epoch bigint; v_expiry timestamptz;
BEGIN
    IF v_lease <= v_now OR v_lease > v_now + interval '24 hours' THEN
        RAISE EXCEPTION 'invalid migration fence renewal';
    END IF;
    UPDATE ducklake.migration_fence
       SET lease_expires_at=v_lease, updated_at=v_now
     WHERE scope=p_scope AND physical_pool_id=p_physical_pool_id
       AND owner_id=p_owner_id AND fencing_epoch=p_fencing_epoch
       AND lease_expires_at > v_now;
    IF FOUND THEN RETURN; END IF;
    SELECT owner_id,fencing_epoch,lease_expires_at INTO v_owner,v_epoch,v_expiry
      FROM ducklake.migration_fence
     WHERE scope=p_scope AND physical_pool_id=p_physical_pool_id;
    IF NOT FOUND OR v_owner IS DISTINCT FROM p_owner_id OR v_epoch <> p_fencing_epoch THEN
        RAISE EXCEPTION 'migration fence stale';
    END IF;
    IF v_expiry IS NULL OR v_expiry <= v_now THEN
        RAISE EXCEPTION 'migration fence expired';
    END IF;
    RAISE EXCEPTION 'migration fence stale';
END;
$$;
-- +goose StatementEnd

-- Retention authority is catalog-wide for one physical pool.  Claims are
-- serialized by the row lock and fenced by a monotonically increasing epoch.
-- The function intentionally has no age-based defaults: callers enumerate
-- exact control-plane eligible snapshots before invoking DuckLake expiry.
CREATE OR REPLACE FUNCTION ducklake.acquire_pool_maintenance_fence(
    p_physical_pool_id text,
    p_catalog_id text,
    p_owner_id text,
    p_lease_expires_at timestamptz
) RETURNS TABLE(physical_pool_id text, catalog_id text, owner_id text, fencing_epoch bigint, lease_expires_at timestamptz)
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE
    v_now timestamptz := clock_timestamp();
    v_lease timestamptz := COALESCE(p_lease_expires_at, v_now + interval '24 hours');
    v_owner text;
    v_epoch bigint;
    v_expiry timestamptz;
BEGIN
    IF p_physical_pool_id = '' OR p_physical_pool_id <> btrim(p_physical_pool_id) OR octet_length(p_physical_pool_id) > 255
       OR p_catalog_id = '' OR p_catalog_id <> btrim(p_catalog_id) OR octet_length(p_catalog_id) > 255
       OR p_owner_id = '' OR p_owner_id <> btrim(p_owner_id) OR octet_length(p_owner_id) > 255
       OR v_lease <= v_now OR v_lease > v_now + interval '24 hours' THEN
        RAISE EXCEPTION 'invalid pool maintenance fence claim';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM ducklake.catalog_identity ci WHERE ci.physical_pool_id=p_physical_pool_id AND ci.catalog_id=p_catalog_id) THEN
        RAISE EXCEPTION 'catalog identity not found';
    END IF;
    -- Lock the migration rows first.  Migration acquisition uses the same
    -- global→pool→maintenance order, preventing a check-then-act race.
    INSERT INTO ducklake.migration_fence(scope,physical_pool_id)
    VALUES ('global','') ON CONFLICT DO NOTHING;
    PERFORM 1 FROM ducklake.migration_fence mf
     WHERE mf.scope='global' AND mf.physical_pool_id='' FOR UPDATE;
    INSERT INTO ducklake.migration_fence(scope,physical_pool_id)
    VALUES ('pool',p_physical_pool_id) ON CONFLICT DO NOTHING;
    PERFORM 1 FROM ducklake.migration_fence mf
     WHERE mf.scope='pool' AND mf.physical_pool_id=p_physical_pool_id FOR UPDATE;
    INSERT INTO ducklake.pool_maintenance_fence(physical_pool_id,catalog_id)
    VALUES (p_physical_pool_id,p_catalog_id) ON CONFLICT DO NOTHING;
    SELECT f.owner_id,f.fencing_epoch,f.lease_expires_at
      INTO v_owner,v_epoch,v_expiry
      FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
     FOR UPDATE;
    IF v_owner IS NOT NULL AND v_expiry > v_now THEN
        IF v_owner = p_owner_id THEN
            RETURN QUERY SELECT p_physical_pool_id,p_catalog_id,v_owner,v_epoch,v_expiry;
            RETURN;
        END IF;
        RAISE EXCEPTION 'pool maintenance fence busy';
    END IF;
    -- Maintenance and catalog migration are separate authorities, but may not
    -- mutate the same pool concurrently.
    IF EXISTS (SELECT 1 FROM ducklake.migration_fence f
              WHERE ((f.scope='global' AND f.physical_pool_id='') OR
                     (f.scope='pool' AND f.physical_pool_id=p_physical_pool_id))
                AND f.owner_id IS NOT NULL AND f.lease_expires_at > v_now) THEN
        RAISE EXCEPTION 'pool maintenance fence busy';
    END IF;
    -- Admission serializes on the same global→pool→maintenance lock order;
    -- once this maintenance row is locked, no admitted running writer can
    -- appear after this check. A running attempt must drain before a
    -- maintenance fence can be acquired.
    IF EXISTS (SELECT 1 FROM delivery.delivery_build_attempt a
               WHERE a.physical_pool_id=p_physical_pool_id
                 AND a.catalog_id=p_catalog_id
                 AND a.state='running') THEN
        RAISE EXCEPTION 'pool maintenance fence busy: running attempt';
    END IF;
    UPDATE ducklake.pool_maintenance_fence f
       SET owner_id=p_owner_id, fencing_epoch=f.fencing_epoch+1,
           lease_expires_at=v_lease, updated_at=v_now
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
     RETURNING f.owner_id,f.fencing_epoch,f.lease_expires_at
      INTO v_owner,v_epoch,v_expiry;
    RETURN QUERY SELECT p_physical_pool_id,p_catalog_id,v_owner,v_epoch,v_expiry;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.release_pool_maintenance_fence(
    p_physical_pool_id text, p_catalog_id text, p_owner_id text, p_fencing_epoch bigint
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE v_owner text; v_epoch bigint;
BEGIN
    UPDATE ducklake.pool_maintenance_fence
       SET owner_id=NULL, lease_expires_at=NULL, updated_at=clock_timestamp()
     WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id
       AND owner_id=p_owner_id AND fencing_epoch=p_fencing_epoch;
    IF FOUND THEN RETURN; END IF;
    SELECT owner_id,fencing_epoch INTO v_owner,v_epoch
      FROM ducklake.pool_maintenance_fence
     WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id;
    IF NOT FOUND THEN RAISE EXCEPTION 'pool maintenance fence not found'; END IF;
    IF v_owner IS NULL AND v_epoch=p_fencing_epoch THEN RETURN; END IF;
    RAISE EXCEPTION 'pool maintenance fence stale';
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.renew_pool_maintenance_fence(
    p_physical_pool_id text, p_catalog_id text, p_owner_id text,
    p_fencing_epoch bigint, p_lease_expires_at timestamptz
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE
    v_now timestamptz := clock_timestamp();
    v_lease timestamptz := COALESCE(p_lease_expires_at, v_now + interval '24 hours');
    v_owner text; v_epoch bigint; v_expiry timestamptz;
BEGIN
    IF v_lease <= v_now OR v_lease > v_now + interval '24 hours' THEN
        RAISE EXCEPTION 'invalid pool maintenance fence renewal';
    END IF;
    UPDATE ducklake.pool_maintenance_fence
       SET lease_expires_at=v_lease, updated_at=v_now
     WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id
       AND owner_id=p_owner_id AND fencing_epoch=p_fencing_epoch
       AND lease_expires_at > v_now;
    IF FOUND THEN RETURN; END IF;
    SELECT owner_id,fencing_epoch,lease_expires_at INTO v_owner,v_epoch,v_expiry
      FROM ducklake.pool_maintenance_fence
     WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id;
    IF NOT FOUND OR v_owner IS DISTINCT FROM p_owner_id OR v_epoch <> p_fencing_epoch THEN
        RAISE EXCEPTION 'pool maintenance fence stale';
    END IF;
    IF v_expiry IS NULL OR v_expiry <= v_now THEN
        RAISE EXCEPTION 'pool maintenance fence expired';
    END IF;
    RAISE EXCEPTION 'pool maintenance fence stale';
END;
$$;
-- +goose StatementEnd

-- Build admission is the one runtime operation that must observe the
-- migration/retention authorities.  Keep the row locks inside a narrowly
-- scoped SECURITY DEFINER function: the runtime role may not UPDATE fence
-- tables directly, yet the locks remain held by the caller's transaction
-- through the subsequent attempt insert.  The order is global migration ->
-- pool migration -> pool maintenance, matching every fence claim path.
CREATE OR REPLACE FUNCTION ducklake.assert_attempt_admission_fence(
    p_physical_pool_id text,
    p_catalog_id text
) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE
    v_now timestamptz := clock_timestamp();
    v_owner text;
    v_expiry timestamptz;
BEGIN
    IF p_physical_pool_id = '' OR p_physical_pool_id <> btrim(p_physical_pool_id) OR octet_length(p_physical_pool_id) > 255
       OR p_catalog_id = '' OR p_catalog_id <> btrim(p_catalog_id) OR octet_length(p_catalog_id) > 255 THEN
        RAISE EXCEPTION 'invalid attempt admission fence scope';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM ducklake.catalog_identity ci
                   WHERE ci.physical_pool_id=p_physical_pool_id AND ci.catalog_id=p_catalog_id) THEN
        RAISE EXCEPTION 'catalog identity not found';
    END IF;

    INSERT INTO ducklake.migration_fence(scope,physical_pool_id)
    VALUES ('global','') ON CONFLICT DO NOTHING;
    SELECT owner_id,lease_expires_at INTO v_owner,v_expiry
      FROM ducklake.migration_fence
     WHERE scope='global' AND physical_pool_id=''
     FOR UPDATE;
    IF v_owner IS NOT NULL AND v_expiry > v_now THEN
        RAISE EXCEPTION 'migration fence busy';
    END IF;

    INSERT INTO ducklake.migration_fence(scope,physical_pool_id)
    VALUES ('pool',p_physical_pool_id) ON CONFLICT DO NOTHING;
    SELECT owner_id,lease_expires_at INTO v_owner,v_expiry
      FROM ducklake.migration_fence
     WHERE scope='pool' AND physical_pool_id=p_physical_pool_id
     FOR UPDATE;
    IF v_owner IS NOT NULL AND v_expiry > v_now THEN
        RAISE EXCEPTION 'migration fence busy';
    END IF;

    -- Materialize and lock the exact pool/catalog maintenance row before
    -- checking its owner. Without this first-use insert, admission could
    -- observe no row while a concurrent retention claim creates and activates
    -- the fence, then proceed to insert a running attempt after the check.
    INSERT INTO ducklake.pool_maintenance_fence(physical_pool_id,catalog_id)
    VALUES (p_physical_pool_id,p_catalog_id) ON CONFLICT DO NOTHING;
    SELECT owner_id,lease_expires_at INTO v_owner,v_expiry
      FROM ducklake.pool_maintenance_fence
     WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id
     FOR UPDATE;
    IF v_owner IS NOT NULL AND v_expiry > v_now THEN
        RAISE EXCEPTION 'pool maintenance fence busy';
    END IF;
END;
$$;
-- +goose StatementEnd

-- PostgreSQL grants EXECUTE on new functions to PUBLIC by default.  This
-- runtime-only admission capability must be explicitly private before role
-- grants are applied below.
REVOKE EXECUTE ON FUNCTION ducklake.assert_attempt_admission_fence(text,text) FROM PUBLIC;

-- Retention lifecycle writes are capability-gated.  The maintenance role has
-- no INSERT/UPDATE privilege on these tables; each function validates the
-- exact pool fence and operation identity before changing state.  Snapshot
-- retention and its durable per-operation evidence are advanced together in
-- the same function transaction, so a crash cannot expose one without the
-- other.
CREATE OR REPLACE FUNCTION ducklake.begin_retention_maintenance(
    p_maintenance_id uuid, p_physical_pool_id text, p_catalog_id text,
    p_owner_id text, p_fencing_epoch bigint, p_dry_run boolean,
    p_file_grace_micros bigint, p_snapshot_set_digest text,
    p_phase_evidence jsonb
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE v_now timestamptz := clock_timestamp(); v_existing record;
BEGIN
    IF p_physical_pool_id = '' OR p_physical_pool_id <> btrim(p_physical_pool_id) OR octet_length(p_physical_pool_id) > 255
       OR p_catalog_id = '' OR p_catalog_id <> btrim(p_catalog_id) OR octet_length(p_catalog_id) > 255
       OR p_owner_id = '' OR p_owner_id <> btrim(p_owner_id) OR octet_length(p_owner_id) > 255
       OR p_fencing_epoch <= 0 OR p_file_grace_micros <= 0
       OR p_snapshot_set_digest <> '' AND p_snapshot_set_digest !~ '^sha256:[0-9a-f]{64}$'
       OR jsonb_typeof(COALESCE(p_phase_evidence, '{}'::jsonb)) <> 'object' THEN
        RAISE EXCEPTION 'invalid retention maintenance request';
    END IF;
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_owner_id AND f.fencing_epoch=p_fencing_epoch
       AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    SELECT * INTO v_existing FROM ducklake.retention_maintenance
     WHERE maintenance_id=p_maintenance_id FOR UPDATE;
    IF FOUND THEN
        IF v_existing.physical_pool_id IS DISTINCT FROM p_physical_pool_id
           OR v_existing.catalog_id IS DISTINCT FROM p_catalog_id
           OR v_existing.dry_run IS DISTINCT FROM p_dry_run
           OR v_existing.file_grace_micros IS DISTINCT FROM p_file_grace_micros
           OR (v_existing.snapshot_set_digest <> '' AND p_snapshot_set_digest <> '' AND v_existing.snapshot_set_digest IS DISTINCT FROM p_snapshot_set_digest)
           OR v_existing.state NOT IN ('running','completed') THEN
            RAISE EXCEPTION 'retention maintenance conflict';
        END IF;
        IF v_existing.state='completed' THEN RETURN; END IF;
        UPDATE ducklake.retention_maintenance
           SET owner_id=p_owner_id, fencing_epoch=p_fencing_epoch, updated_at=v_now
         WHERE maintenance_id=p_maintenance_id;
        RETURN;
    END IF;
    INSERT INTO ducklake.retention_maintenance
      (maintenance_id,physical_pool_id,catalog_id,owner_id,fencing_epoch,state,phase,
       dry_run,file_grace_micros,snapshot_set_digest,phase_evidence,started_at,updated_at)
    VALUES
      (p_maintenance_id,p_physical_pool_id,p_catalog_id,p_owner_id,p_fencing_epoch,
       'running','expiry',p_dry_run,p_file_grace_micros,COALESCE(p_snapshot_set_digest,''),
       COALESCE(p_phase_evidence,'{}'::jsonb),v_now,v_now);
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.update_retention_maintenance(
    p_maintenance_id uuid, p_physical_pool_id text, p_catalog_id text,
    p_owner_id text, p_fencing_epoch bigint, p_state text, p_phase text,
    p_dry_run boolean, p_file_grace_micros bigint, p_snapshot_set_digest text,
    p_phase_evidence jsonb, p_completed_at timestamptz
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE v_now timestamptz := clock_timestamp(); v_existing record; v_missing bigint;
BEGIN
    IF p_state NOT IN ('running','completed','failed') OR p_phase NOT IN ('expiry','old-files','orphans','completed')
       OR p_owner_id = '' OR p_owner_id <> btrim(p_owner_id) OR octet_length(p_owner_id) > 255
       OR p_fencing_epoch <= 0 OR p_file_grace_micros <= 0
       OR p_snapshot_set_digest <> '' AND p_snapshot_set_digest !~ '^sha256:[0-9a-f]{64}$'
       OR jsonb_typeof(COALESCE(p_phase_evidence, '{}'::jsonb)) <> 'object' THEN
        RAISE EXCEPTION 'invalid retention maintenance update';
    END IF;
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_owner_id AND f.fencing_epoch=p_fencing_epoch
       AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    SELECT * INTO v_existing FROM ducklake.retention_maintenance
     WHERE maintenance_id=p_maintenance_id FOR UPDATE;
    IF NOT FOUND OR v_existing.physical_pool_id IS DISTINCT FROM p_physical_pool_id
       OR v_existing.catalog_id IS DISTINCT FROM p_catalog_id
       OR v_existing.owner_id IS DISTINCT FROM p_owner_id
       OR v_existing.fencing_epoch IS DISTINCT FROM p_fencing_epoch THEN
        RAISE EXCEPTION 'maintenance fence stale';
    END IF;
    IF v_existing.state <> 'running' THEN
        IF v_existing.state=p_state AND v_existing.phase=p_phase
           AND v_existing.phase_evidence IS NOT DISTINCT FROM COALESCE(p_phase_evidence,'{}'::jsonb) THEN RETURN; END IF;
        RAISE EXCEPTION 'terminal retention maintenance is immutable';
    END IF;
    IF p_state='completed' AND p_snapshot_set_digest = '' THEN
        RAISE EXCEPTION 'retention maintenance snapshot set is not frozen';
    END IF;
    IF p_state='completed' AND NOT p_dry_run THEN
        SELECT count(*) INTO v_missing
          FROM ducklake.retention_maintenance_snapshot s
          JOIN ducklake.snapshot_retention r
            ON r.physical_pool_id=s.physical_pool_id AND r.catalog_id=s.catalog_id AND r.snapshot_id=s.snapshot_id
         WHERE s.maintenance_id=p_maintenance_id
           AND (s.phase <> 'cleanup-complete' OR r.state <> 'cleanup-complete' OR r.retention_claim_id IS DISTINCT FROM p_maintenance_id);
        IF v_missing <> 0 THEN RAISE EXCEPTION 'retention cleanup evidence incomplete'; END IF;
    END IF;
    UPDATE ducklake.retention_maintenance
       SET state=p_state,phase=p_phase,dry_run=p_dry_run,
           file_grace_micros=p_file_grace_micros,
           snapshot_set_digest=p_snapshot_set_digest,
           phase_evidence=COALESCE(p_phase_evidence,'{}'::jsonb),updated_at=v_now,
           completed_at=CASE WHEN p_state='completed' THEN v_now ELSE NULL END
     WHERE maintenance_id=p_maintenance_id AND state='running';
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.insert_retention_maintenance_snapshot(
    p_maintenance_id uuid, p_physical_pool_id text, p_catalog_id text,
    p_snapshot_id bigint, p_owner_id text, p_fencing_epoch bigint
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE v_now timestamptz := clock_timestamp(); v_operation record; v_retention record;
BEGIN
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_owner_id AND f.fencing_epoch=p_fencing_epoch
       AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    SELECT * INTO v_operation FROM ducklake.retention_maintenance m
     WHERE m.maintenance_id=p_maintenance_id FOR SHARE;
    IF NOT FOUND OR v_operation.physical_pool_id IS DISTINCT FROM p_physical_pool_id
       OR v_operation.catalog_id IS DISTINCT FROM p_catalog_id
       OR v_operation.owner_id IS DISTINCT FROM p_owner_id
       OR v_operation.fencing_epoch IS DISTINCT FROM p_fencing_epoch
       OR v_operation.state <> 'running' OR v_operation.snapshot_set_digest <> '' THEN
        RAISE EXCEPTION 'maintenance fence stale';
    END IF;
    SELECT retention_claim_id,state INTO v_retention FROM ducklake.snapshot_retention
     WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id;
    IF NOT FOUND OR (v_operation.dry_run AND (v_retention.state NOT IN ('retiring','expired') OR v_retention.retention_claim_id IS NOT NULL))
       OR (NOT v_operation.dry_run AND (v_retention.retention_claim_id IS DISTINCT FROM p_maintenance_id OR v_retention.state NOT IN ('expiring','expired'))) THEN
        RAISE EXCEPTION 'retention snapshot claim mismatch';
    END IF;
    INSERT INTO ducklake.retention_maintenance_snapshot
      (maintenance_id,physical_pool_id,catalog_id,snapshot_id,phase)
    VALUES (p_maintenance_id,p_physical_pool_id,p_catalog_id,p_snapshot_id,'eligible')
    ON CONFLICT (maintenance_id,physical_pool_id,catalog_id,snapshot_id) DO NOTHING;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.insert_retention_maintenance_snapshots(
    p_maintenance_id uuid, p_physical_pool_id text, p_catalog_id text,
    p_snapshot_ids bigint[], p_owner_id text, p_fencing_epoch bigint
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE v_now timestamptz := clock_timestamp(); v_operation record; v_matches integer; v_count integer;
BEGIN
    v_count := COALESCE(cardinality(p_snapshot_ids), 0);
    IF v_count < 1 OR v_count > 256
       OR (SELECT count(DISTINCT ids.snapshot_id) FROM unnest(p_snapshot_ids) AS ids(snapshot_id)) <> v_count
       OR EXISTS (SELECT 1 FROM unnest(p_snapshot_ids) AS ids(snapshot_id) WHERE ids.snapshot_id <= 0) THEN
        RAISE EXCEPTION 'invalid retention snapshot batch';
    END IF;
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_owner_id AND f.fencing_epoch=p_fencing_epoch
       AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    SELECT * INTO v_operation FROM ducklake.retention_maintenance m
     WHERE m.maintenance_id=p_maintenance_id FOR SHARE;
    IF NOT FOUND OR v_operation.physical_pool_id IS DISTINCT FROM p_physical_pool_id
       OR v_operation.catalog_id IS DISTINCT FROM p_catalog_id
       OR v_operation.owner_id IS DISTINCT FROM p_owner_id
       OR v_operation.fencing_epoch IS DISTINCT FROM p_fencing_epoch
       OR v_operation.state <> 'running' OR v_operation.snapshot_set_digest <> '' THEN
        RAISE EXCEPTION 'maintenance fence stale';
    END IF;
    SELECT count(*) INTO v_matches
      FROM unnest(p_snapshot_ids) AS ids(snapshot_id)
      JOIN ducklake.snapshot_retention r
        ON r.physical_pool_id=p_physical_pool_id AND r.catalog_id=p_catalog_id
       AND r.snapshot_id=ids.snapshot_id
     WHERE (v_operation.dry_run AND r.state IN ('retiring','expired') AND r.retention_claim_id IS NULL)
        OR (NOT v_operation.dry_run AND r.retention_claim_id=p_maintenance_id AND r.state IN ('expiring','expired'));
    IF v_matches <> v_count THEN RAISE EXCEPTION 'retention snapshot claim mismatch'; END IF;
    INSERT INTO ducklake.retention_maintenance_snapshot
      (maintenance_id,physical_pool_id,catalog_id,snapshot_id,phase)
    SELECT p_maintenance_id,p_physical_pool_id,p_catalog_id,ids.snapshot_id,'eligible'
      FROM unnest(p_snapshot_ids) AS ids(snapshot_id)
    ON CONFLICT (maintenance_id,physical_pool_id,catalog_id,snapshot_id) DO NOTHING;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.update_retention_maintenance_snapshot(
    p_maintenance_id uuid, p_physical_pool_id text, p_catalog_id text,
    p_snapshot_id bigint, p_owner_id text, p_fencing_epoch bigint,
    p_phase text, p_expiry_evidence jsonb, p_quarantine_evidence jsonb,
    p_cleanup_evidence jsonb
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE v_now timestamptz := clock_timestamp(); v_old record; v_retention record;
BEGIN
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_owner_id AND f.fencing_epoch=p_fencing_epoch
       AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    PERFORM 1 FROM ducklake.retention_maintenance m
     WHERE m.maintenance_id=p_maintenance_id AND m.physical_pool_id=p_physical_pool_id
       AND m.catalog_id=p_catalog_id AND m.owner_id=p_owner_id AND m.fencing_epoch=p_fencing_epoch;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence stale'; END IF;
    SELECT * INTO v_old FROM ducklake.retention_maintenance_snapshot s
     WHERE s.maintenance_id=p_maintenance_id AND s.physical_pool_id=p_physical_pool_id
       AND s.catalog_id=p_catalog_id AND s.snapshot_id=p_snapshot_id FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'retention maintenance snapshot not found'; END IF;
    SELECT state,evidence,quarantine_evidence,cleanup_evidence INTO v_retention
      FROM ducklake.snapshot_retention
     WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id;
    IF NOT FOUND THEN RAISE EXCEPTION 'snapshot retention not found'; END IF;
    IF p_phase NOT IN ('eligible','expired','quarantined','cleanup-complete') THEN
        RAISE EXCEPTION 'invalid retention maintenance snapshot phase';
    END IF;
    IF v_old.phase='cleanup-complete' THEN
        IF v_old.phase IS DISTINCT FROM p_phase OR v_old.expiry_evidence IS DISTINCT FROM p_expiry_evidence
           OR v_old.quarantine_evidence IS DISTINCT FROM p_quarantine_evidence OR v_old.cleanup_evidence IS DISTINCT FROM p_cleanup_evidence THEN
            RAISE EXCEPTION 'completed retention maintenance snapshot is immutable';
        END IF;
        RETURN;
    END IF;
    IF (v_old.phase='quarantined' AND p_phase NOT IN ('quarantined','cleanup-complete'))
       OR (v_old.phase='expired' AND p_phase NOT IN ('expired','quarantined','cleanup-complete'))
       OR (v_old.phase='eligible' AND p_phase NOT IN ('eligible','expired','quarantined','cleanup-complete')) THEN
        RAISE EXCEPTION 'retention maintenance snapshot lifecycle is monotonic';
    END IF;
    IF p_phase='expired' AND (v_retention.state NOT IN ('expired','quarantined','cleanup-complete') OR v_retention.evidence IS DISTINCT FROM p_expiry_evidence) THEN
        RAISE EXCEPTION 'snapshot expiry evidence is not durable';
    ELSIF p_phase='quarantined' AND (v_retention.state NOT IN ('quarantined','cleanup-complete') OR v_retention.quarantine_evidence IS DISTINCT FROM p_quarantine_evidence) THEN
        RAISE EXCEPTION 'snapshot quarantine evidence is not durable';
    ELSIF p_phase='cleanup-complete' AND (v_retention.state <> 'cleanup-complete' OR v_retention.cleanup_evidence IS DISTINCT FROM p_cleanup_evidence) THEN
        RAISE EXCEPTION 'snapshot cleanup evidence is not durable';
    END IF;
    UPDATE ducklake.retention_maintenance_snapshot
       SET phase=p_phase,expiry_evidence=p_expiry_evidence,
           quarantine_evidence=p_quarantine_evidence,cleanup_evidence=p_cleanup_evidence,
           updated_at=v_now
     WHERE maintenance_id=p_maintenance_id AND physical_pool_id=p_physical_pool_id
       AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.claim_retention_snapshots(
    p_maintenance_id uuid, p_physical_pool_id text, p_catalog_id text,
    p_owner_id text, p_fencing_epoch bigint, p_limit integer
) RETURNS bigint[]
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE v_now timestamptz := clock_timestamp(); v_operation record; v_ids bigint[];
BEGIN
    IF p_limit < 1 OR p_limit > 256 THEN RAISE EXCEPTION 'invalid retention claim limit'; END IF;
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_owner_id AND f.fencing_epoch=p_fencing_epoch
       AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    SELECT * INTO v_operation FROM ducklake.retention_maintenance m
     WHERE m.maintenance_id=p_maintenance_id FOR SHARE;
    IF NOT FOUND OR v_operation.physical_pool_id IS DISTINCT FROM p_physical_pool_id
       OR v_operation.catalog_id IS DISTINCT FROM p_catalog_id
       OR v_operation.owner_id IS DISTINCT FROM p_owner_id
       OR v_operation.fencing_epoch IS DISTINCT FROM p_fencing_epoch
       OR v_operation.state <> 'running' OR v_operation.snapshot_set_digest <> '' THEN
        RAISE EXCEPTION 'maintenance fence stale';
    END IF;
    WITH candidates AS (
        SELECT r.snapshot_id
          FROM ducklake.snapshot_retention AS r
         WHERE r.physical_pool_id=p_physical_pool_id AND r.catalog_id=p_catalog_id
           AND r.state IN ('retiring','expired') AND r.retention_claim_id IS NULL
           AND NOT EXISTS (
               SELECT 1
                 FROM delivery.delivery_retention_root root
                 JOIN delivery.delivery_snapshot_seal seal ON seal.seal_id = root.snapshot_seal_id
                WHERE seal.physical_pool_id=r.physical_pool_id
                  AND seal.catalog_id=r.catalog_id
                  AND seal.ducklake_snapshot_id=r.snapshot_id
                  AND root.state IN ('live','retiring'))
           AND NOT EXISTS (
               SELECT 1
                 FROM serving_state.reader_lease lease
                 JOIN delivery.delivery_generation g ON g.generation_id = lease.generation_id
                 JOIN delivery.delivery_snapshot_seal seal ON seal.seal_id = g.snapshot_seal_id
                WHERE seal.physical_pool_id=r.physical_pool_id
                  AND seal.catalog_id=r.catalog_id
                  AND seal.ducklake_snapshot_id=r.snapshot_id
                  AND lease.released_at IS NULL)
         ORDER BY r.snapshot_id
         LIMIT p_limit
         FOR UPDATE OF r
    ), changed AS (
        UPDATE ducklake.snapshot_retention AS r
       SET state=CASE WHEN r.state='retiring' THEN 'expiring' ELSE r.state END,
           retention_claim_id=p_maintenance_id,retention_claim_owner_id=p_owner_id,
           retention_claim_fencing_epoch=p_fencing_epoch,retention_claimed_at=v_now
      FROM candidates
     WHERE r.physical_pool_id=p_physical_pool_id AND r.catalog_id=p_catalog_id
       AND r.snapshot_id=candidates.snapshot_id
        RETURNING r.snapshot_id
    )
    SELECT COALESCE(array_agg(snapshot_id ORDER BY snapshot_id),'{}'::bigint[]) INTO v_ids FROM changed;
    RETURN v_ids;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.expire_snapshot_under_maintenance_fence(
    p_expired_at timestamptz, p_evidence jsonb, p_physical_pool_id text,
    p_catalog_id text, p_snapshot_id bigint, p_maintenance_id uuid,
    p_maintenance_owner_id text, p_maintenance_fencing_epoch bigint
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE v_now timestamptz := clock_timestamp(); v_operation record; v_retention record; v_child record; v_expired timestamptz := COALESCE(p_expired_at, v_now);
BEGIN
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_maintenance_owner_id AND f.fencing_epoch=p_maintenance_fencing_epoch
       AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    SELECT * INTO v_operation FROM ducklake.retention_maintenance m
     WHERE m.maintenance_id=p_maintenance_id FOR SHARE;
    IF NOT FOUND OR v_operation.physical_pool_id IS DISTINCT FROM p_physical_pool_id
       OR v_operation.catalog_id IS DISTINCT FROM p_catalog_id
       OR v_operation.owner_id IS DISTINCT FROM p_maintenance_owner_id
       OR v_operation.fencing_epoch IS DISTINCT FROM p_maintenance_fencing_epoch
       OR v_operation.dry_run THEN RAISE EXCEPTION 'maintenance fence stale'; END IF;
    SELECT * INTO v_retention FROM ducklake.snapshot_retention r
     WHERE r.physical_pool_id=p_physical_pool_id AND r.catalog_id=p_catalog_id AND r.snapshot_id=p_snapshot_id FOR UPDATE;
    IF NOT FOUND OR v_retention.retention_claim_id IS DISTINCT FROM p_maintenance_id THEN RAISE EXCEPTION 'maintenance fence stale'; END IF;
    IF v_retention.state='expiring' THEN
        IF EXISTS (
               SELECT 1
                 FROM serving_state.reader_lease l
                 JOIN delivery.delivery_generation g ON g.generation_id = l.generation_id
                 JOIN delivery.delivery_snapshot_seal seal ON seal.seal_id = g.snapshot_seal_id
                WHERE seal.physical_pool_id=p_physical_pool_id
                  AND seal.catalog_id=p_catalog_id
                  AND seal.ducklake_snapshot_id=p_snapshot_id
                  AND l.released_at IS NULL)
           OR EXISTS (
               SELECT 1
                 FROM delivery.delivery_retention_root root
                 JOIN delivery.delivery_snapshot_seal seal ON seal.seal_id = root.snapshot_seal_id
                WHERE seal.physical_pool_id=p_physical_pool_id
                  AND seal.catalog_id=p_catalog_id
                  AND seal.ducklake_snapshot_id=p_snapshot_id
                  AND root.state IN ('live','retiring'))
           THEN
            RAISE EXCEPTION 'canonical snapshot protections remain';
        END IF;
        UPDATE ducklake.snapshot_retention SET state='expired',expired_at=v_expired,evidence=p_evidence
         WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id AND state='expiring';
    ELSIF v_retention.state='expired' THEN
        IF v_retention.evidence IS DISTINCT FROM p_evidence THEN RAISE EXCEPTION 'expiration evidence differs'; END IF;
    ELSE
        RAISE EXCEPTION 'snapshot must be expiring before expiry';
    END IF;
    SELECT * INTO v_child FROM ducklake.retention_maintenance_snapshot s
     WHERE s.maintenance_id=p_maintenance_id AND s.physical_pool_id=p_physical_pool_id AND s.catalog_id=p_catalog_id AND s.snapshot_id=p_snapshot_id FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'retention maintenance snapshot not found'; END IF;
    IF v_child.phase='eligible' THEN
        UPDATE ducklake.retention_maintenance_snapshot
           SET phase='expired',expiry_evidence=p_evidence,updated_at=v_now
         WHERE maintenance_id=p_maintenance_id AND physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id;
    ELSIF v_child.phase='expired' AND v_child.expiry_evidence IS DISTINCT FROM p_evidence THEN
        RAISE EXCEPTION 'expiration evidence differs';
    ELSIF v_child.phase NOT IN ('expired','quarantined','cleanup-complete') THEN
        RAISE EXCEPTION 'retention maintenance snapshot lifecycle is invalid';
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.claim_snapshot_cleanup_under_maintenance_fence(
    p_physical_pool_id text, p_catalog_id text, p_snapshot_id bigint,
    p_cleanup_owner_id text, p_cleanup_lease_expires_at timestamptz,
    p_maintenance_id uuid, p_maintenance_owner_id text,
    p_maintenance_fencing_epoch bigint
) RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE v_now timestamptz := clock_timestamp(); v_operation record; v_retention record; v_lease timestamptz := p_cleanup_lease_expires_at;
BEGIN
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_maintenance_owner_id AND f.fencing_epoch=p_maintenance_fencing_epoch
       AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    SELECT * INTO v_operation FROM ducklake.retention_maintenance m
     WHERE m.maintenance_id=p_maintenance_id FOR SHARE;
    IF NOT FOUND OR v_operation.physical_pool_id IS DISTINCT FROM p_physical_pool_id
       OR v_operation.catalog_id IS DISTINCT FROM p_catalog_id
       OR v_operation.owner_id IS DISTINCT FROM p_maintenance_owner_id
       OR v_operation.fencing_epoch IS DISTINCT FROM p_maintenance_fencing_epoch
       OR v_operation.dry_run THEN RAISE EXCEPTION 'maintenance fence stale'; END IF;
    SELECT * INTO v_retention FROM ducklake.snapshot_retention r
     WHERE r.physical_pool_id=p_physical_pool_id AND r.catalog_id=p_catalog_id AND r.snapshot_id=p_snapshot_id FOR UPDATE;
    IF NOT FOUND OR v_retention.retention_claim_id IS DISTINCT FROM p_maintenance_id THEN RAISE EXCEPTION 'maintenance fence stale'; END IF;
    IF v_retention.state NOT IN ('expired','quarantined') THEN RAISE EXCEPTION 'snapshot cleanup pending'; END IF;
    IF v_retention.cleanup_owner_id IS NOT NULL AND v_retention.cleanup_lease_expires_at > v_now THEN
        IF v_retention.cleanup_owner_id=p_cleanup_owner_id THEN RETURN v_retention.cleanup_fencing_epoch; END IF;
        RAISE EXCEPTION 'snapshot cleanup busy';
    END IF;
    IF v_lease IS NULL THEN v_lease := v_now + interval '24 hours'; END IF;
    IF v_lease <= v_now OR v_lease > v_now + interval '24 hours' OR p_cleanup_owner_id = '' OR p_cleanup_owner_id <> btrim(p_cleanup_owner_id) OR octet_length(p_cleanup_owner_id) > 255 THEN
        RAISE EXCEPTION 'invalid snapshot cleanup claim';
    END IF;
    UPDATE ducklake.snapshot_retention
       SET cleanup_owner_id=p_cleanup_owner_id,cleanup_fencing_epoch=cleanup_fencing_epoch+1,cleanup_lease_expires_at=v_lease
     WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id;
    SELECT cleanup_fencing_epoch INTO v_retention FROM ducklake.snapshot_retention
     WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id;
    RETURN v_retention.cleanup_fencing_epoch;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.quarantine_snapshot_under_maintenance_fence(
    p_quarantine_evidence jsonb, p_quarantined_at timestamptz,
    p_physical_pool_id text, p_catalog_id text, p_snapshot_id bigint,
    p_cleanup_owner_id text, p_cleanup_fencing_epoch bigint,
    p_maintenance_id uuid, p_maintenance_owner_id text,
    p_maintenance_fencing_epoch bigint
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE v_now timestamptz := clock_timestamp(); v_operation record; v_retention record; v_child record; v_at timestamptz := v_now;
BEGIN
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_maintenance_owner_id AND f.fencing_epoch=p_maintenance_fencing_epoch
       AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    SELECT * INTO v_operation FROM ducklake.retention_maintenance m
     WHERE m.maintenance_id=p_maintenance_id FOR SHARE;
    IF NOT FOUND OR v_operation.physical_pool_id IS DISTINCT FROM p_physical_pool_id
       OR v_operation.catalog_id IS DISTINCT FROM p_catalog_id
       OR v_operation.owner_id IS DISTINCT FROM p_maintenance_owner_id
       OR v_operation.fencing_epoch IS DISTINCT FROM p_maintenance_fencing_epoch
       OR v_operation.dry_run THEN RAISE EXCEPTION 'maintenance fence stale'; END IF;
    SELECT * INTO v_retention FROM ducklake.snapshot_retention r
     WHERE r.physical_pool_id=p_physical_pool_id AND r.catalog_id=p_catalog_id AND r.snapshot_id=p_snapshot_id FOR UPDATE;
    IF NOT FOUND OR v_retention.retention_claim_id IS DISTINCT FROM p_maintenance_id
       OR v_retention.cleanup_owner_id IS DISTINCT FROM p_cleanup_owner_id
       OR v_retention.cleanup_fencing_epoch IS DISTINCT FROM p_cleanup_fencing_epoch
       OR v_retention.cleanup_lease_expires_at <= v_now THEN RAISE EXCEPTION 'maintenance fence stale'; END IF;
    IF v_retention.state='expired' THEN
        UPDATE ducklake.snapshot_retention SET state='quarantined',quarantine_evidence=p_quarantine_evidence,quarantined_at=v_at
         WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id AND state='expired';
    ELSIF v_retention.state='quarantined' THEN
        IF v_retention.quarantine_evidence IS DISTINCT FROM p_quarantine_evidence THEN RAISE EXCEPTION 'quarantine evidence differs'; END IF;
    ELSE
        RAISE EXCEPTION 'snapshot must be expired before quarantine';
    END IF;
    SELECT * INTO v_child FROM ducklake.retention_maintenance_snapshot s
     WHERE s.maintenance_id=p_maintenance_id AND s.physical_pool_id=p_physical_pool_id AND s.catalog_id=p_catalog_id AND s.snapshot_id=p_snapshot_id FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'retention maintenance snapshot not found'; END IF;
    IF v_child.phase IN ('expired','quarantined') THEN
        IF v_child.phase='quarantined' AND v_child.quarantine_evidence IS DISTINCT FROM p_quarantine_evidence THEN RAISE EXCEPTION 'quarantine evidence differs'; END IF;
        IF v_child.phase='expired' THEN
            UPDATE ducklake.retention_maintenance_snapshot SET phase='quarantined',quarantine_evidence=p_quarantine_evidence,updated_at=v_now
             WHERE maintenance_id=p_maintenance_id AND physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id;
        END IF;
    ELSIF v_child.phase='cleanup-complete' THEN RETURN;
    ELSE RAISE EXCEPTION 'retention maintenance snapshot expiry evidence missing';
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.complete_snapshot_cleanup_under_maintenance_fence(
    p_cleanup_evidence jsonb, p_cleanup_completed_at timestamptz,
    p_physical_pool_id text, p_catalog_id text, p_snapshot_id bigint,
    p_cleanup_owner_id text, p_cleanup_fencing_epoch bigint,
    p_maintenance_id uuid, p_maintenance_owner_id text,
    p_maintenance_fencing_epoch bigint
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE v_now timestamptz := clock_timestamp(); v_operation record; v_retention record; v_child record; v_at timestamptz := v_now;
BEGIN
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_maintenance_owner_id AND f.fencing_epoch=p_maintenance_fencing_epoch
       AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    SELECT * INTO v_operation FROM ducklake.retention_maintenance m
     WHERE m.maintenance_id=p_maintenance_id FOR SHARE;
    IF NOT FOUND OR v_operation.physical_pool_id IS DISTINCT FROM p_physical_pool_id
       OR v_operation.catalog_id IS DISTINCT FROM p_catalog_id
       OR v_operation.owner_id IS DISTINCT FROM p_maintenance_owner_id
       OR v_operation.fencing_epoch IS DISTINCT FROM p_maintenance_fencing_epoch
       OR v_operation.dry_run THEN RAISE EXCEPTION 'maintenance fence stale'; END IF;
    SELECT * INTO v_retention FROM ducklake.snapshot_retention r
     WHERE r.physical_pool_id=p_physical_pool_id AND r.catalog_id=p_catalog_id AND r.snapshot_id=p_snapshot_id FOR UPDATE;
    IF NOT FOUND OR v_retention.retention_claim_id IS DISTINCT FROM p_maintenance_id
       OR v_retention.cleanup_owner_id IS DISTINCT FROM p_cleanup_owner_id
       OR v_retention.cleanup_fencing_epoch IS DISTINCT FROM p_cleanup_fencing_epoch THEN RAISE EXCEPTION 'maintenance fence stale'; END IF;
    IF v_retention.state='quarantined' THEN
        IF v_retention.cleanup_lease_expires_at <= v_now THEN RAISE EXCEPTION 'snapshot cleanup lease expired'; END IF;
        UPDATE ducklake.snapshot_retention SET state='cleanup-complete',cleanup_evidence=p_cleanup_evidence,cleanup_completed_at=v_at
         WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id AND state='quarantined';
    ELSIF v_retention.state='cleanup-complete' THEN
        IF v_retention.cleanup_evidence IS DISTINCT FROM p_cleanup_evidence THEN RAISE EXCEPTION 'cleanup evidence differs'; END IF;
    ELSE
        RAISE EXCEPTION 'snapshot must be quarantined before cleanup-complete';
    END IF;
    SELECT * INTO v_child FROM ducklake.retention_maintenance_snapshot s
     WHERE s.maintenance_id=p_maintenance_id AND s.physical_pool_id=p_physical_pool_id AND s.catalog_id=p_catalog_id AND s.snapshot_id=p_snapshot_id FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'retention maintenance snapshot not found'; END IF;
    IF v_child.phase='cleanup-complete' THEN
        IF v_child.cleanup_evidence IS DISTINCT FROM p_cleanup_evidence THEN RAISE EXCEPTION 'cleanup evidence differs'; END IF;
    ELSIF v_child.phase='quarantined' THEN
        UPDATE ducklake.retention_maintenance_snapshot SET phase='cleanup-complete',cleanup_evidence=p_cleanup_evidence,updated_at=v_now
         WHERE maintenance_id=p_maintenance_id AND physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id;
    ELSE
        RAISE EXCEPTION 'retention maintenance snapshot quarantine evidence missing';
    END IF;
END;
$$;
-- +goose StatementEnd

DROP FUNCTION IF EXISTS ducklake.register_catalog_runtime_compatibility(text,text,text,text,text,text,text);
CREATE OR REPLACE FUNCTION ducklake.register_catalog_runtime_compatibility(
    p_physical_pool_id text, p_catalog_id text, p_duckdb_runtime text,
    p_ducklake_extension text, p_catalog_format text,
    p_compatibility_digest text, p_catalog_schema_version text,
    p_owner_id text, p_pool_fencing_epoch bigint, p_global_fencing_epoch bigint
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE v_exists boolean; v_now timestamptz := clock_timestamp(); v_existing record;
BEGIN
    PERFORM 1 FROM ducklake.migration_fence
     WHERE scope='global' AND physical_pool_id='' AND owner_id=p_owner_id
       AND fencing_epoch=p_global_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    PERFORM 1 FROM ducklake.migration_fence
     WHERE scope='pool' AND physical_pool_id=p_physical_pool_id AND owner_id=p_owner_id
       AND fencing_epoch=p_pool_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    SELECT EXISTS (SELECT 1 FROM ducklake.catalog_runtime_compatibility WHERE physical_pool_id=p_physical_pool_id) INTO v_exists;
    IF v_exists THEN
        SELECT * INTO v_existing FROM ducklake.catalog_runtime_compatibility WHERE physical_pool_id=p_physical_pool_id;
        IF v_existing.catalog_id IS DISTINCT FROM p_catalog_id
           OR v_existing.duckdb_runtime IS DISTINCT FROM p_duckdb_runtime
           OR v_existing.ducklake_extension IS DISTINCT FROM p_ducklake_extension
           OR v_existing.catalog_format IS DISTINCT FROM p_catalog_format
           OR v_existing.compatibility_digest IS DISTINCT FROM p_compatibility_digest
           OR v_existing.catalog_schema_version IS DISTINCT FROM p_catalog_schema_version THEN
            RAISE EXCEPTION 'runtime compatibility mismatch';
        END IF;
        RETURN;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM ducklake.catalog_identity
         WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id
    ) THEN
        RAISE EXCEPTION 'runtime compatibility mismatch';
    END IF;
    INSERT INTO ducklake.catalog_runtime_compatibility
      (physical_pool_id,catalog_id,duckdb_runtime,ducklake_extension,catalog_format,
       compatibility_digest,catalog_schema_version,updated_at)
    VALUES (p_physical_pool_id,p_catalog_id,p_duckdb_runtime,p_ducklake_extension,
            p_catalog_format,p_compatibility_digest,p_catalog_schema_version,clock_timestamp());
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.begin_catalog_migration(
    p_migration_id uuid, p_physical_pool_id text, p_catalog_id text,
    p_owner_id text, p_pool_fencing_epoch bigint, p_global_fencing_epoch bigint,
    p_current_duckdb_runtime text, p_current_ducklake_extension text,
    p_current_catalog_format text, p_current_compatibility_digest text,
    p_current_catalog_schema_version text, p_target_duckdb_runtime text,
    p_target_ducklake_extension text, p_target_catalog_format text,
    p_target_compatibility_digest text, p_target_catalog_schema_version text,
    p_begin_evidence jsonb
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE v_now timestamptz := clock_timestamp(); v_catalog record; v_existing record;
BEGIN
    PERFORM 1 FROM ducklake.migration_fence
     WHERE scope='global' AND physical_pool_id='' AND owner_id=p_owner_id
       AND fencing_epoch=p_global_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    PERFORM 1 FROM ducklake.migration_fence
     WHERE scope='pool' AND physical_pool_id=p_physical_pool_id AND owner_id=p_owner_id
       AND fencing_epoch=p_pool_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    IF jsonb_typeof(p_begin_evidence) <> 'object' OR p_begin_evidence = '{}'::jsonb
       OR NOT ((p_begin_evidence->>'drain_verified')='true' OR (p_begin_evidence->>'drained')='true' OR (p_begin_evidence->>'readers_drained')='true')
       OR NOT ((p_begin_evidence->>'backup_verified')='true' OR (p_begin_evidence->>'backup_verification')='true' OR (p_begin_evidence->>'backup')='true') THEN
        RAISE EXCEPTION 'migration evidence required';
    END IF;
    SELECT * INTO v_existing FROM ducklake.catalog_migration WHERE migration_id=p_migration_id FOR UPDATE;
    IF FOUND THEN
        IF v_existing.physical_pool_id IS DISTINCT FROM p_physical_pool_id
           OR v_existing.catalog_id IS DISTINCT FROM p_catalog_id
           OR v_existing.owner_id IS DISTINCT FROM p_owner_id
           OR v_existing.fencing_epoch IS DISTINCT FROM p_pool_fencing_epoch
           OR v_existing.global_fencing_epoch IS DISTINCT FROM p_global_fencing_epoch
           OR v_existing.current_duckdb_runtime IS DISTINCT FROM p_current_duckdb_runtime
           OR v_existing.current_ducklake_extension IS DISTINCT FROM p_current_ducklake_extension
           OR v_existing.current_catalog_format IS DISTINCT FROM p_current_catalog_format
           OR v_existing.current_compatibility_digest IS DISTINCT FROM p_current_compatibility_digest
           OR v_existing.current_catalog_schema_version IS DISTINCT FROM p_current_catalog_schema_version
           OR v_existing.target_duckdb_runtime IS DISTINCT FROM p_target_duckdb_runtime
           OR v_existing.target_ducklake_extension IS DISTINCT FROM p_target_ducklake_extension
           OR v_existing.target_catalog_format IS DISTINCT FROM p_target_catalog_format
           OR v_existing.target_compatibility_digest IS DISTINCT FROM p_target_compatibility_digest
           OR v_existing.target_catalog_schema_version IS DISTINCT FROM p_target_catalog_schema_version
           OR v_existing.state IS DISTINCT FROM 'running'
           OR v_existing.begin_evidence IS DISTINCT FROM p_begin_evidence THEN
            RAISE EXCEPTION 'migration conflict';
        END IF;
        RETURN;
    END IF;
    SELECT * INTO v_catalog FROM ducklake.catalog_runtime_compatibility
     WHERE physical_pool_id=p_physical_pool_id FOR SHARE;
    IF NOT FOUND OR v_catalog.catalog_id <> p_catalog_id
       OR v_catalog.duckdb_runtime <> p_current_duckdb_runtime
       OR v_catalog.ducklake_extension <> p_current_ducklake_extension
       OR v_catalog.catalog_format <> p_current_catalog_format
       OR v_catalog.compatibility_digest <> p_current_compatibility_digest
       OR v_catalog.catalog_schema_version <> p_current_catalog_schema_version THEN
        RAISE EXCEPTION 'runtime compatibility mismatch';
    END IF;
    INSERT INTO ducklake.catalog_migration
      (migration_id,physical_pool_id,catalog_id,owner_id,fencing_epoch,global_fencing_epoch,
       current_duckdb_runtime,current_ducklake_extension,current_catalog_format,
       current_compatibility_digest,current_catalog_schema_version,target_duckdb_runtime,
       target_ducklake_extension,target_catalog_format,target_compatibility_digest,
       target_catalog_schema_version,state,started_at,begin_evidence)
    VALUES (p_migration_id,p_physical_pool_id,p_catalog_id,p_owner_id,p_pool_fencing_epoch,
            p_global_fencing_epoch,p_current_duckdb_runtime,p_current_ducklake_extension,
            p_current_catalog_format,p_current_compatibility_digest,p_current_catalog_schema_version,
            p_target_duckdb_runtime,p_target_ducklake_extension,p_target_catalog_format,
            p_target_compatibility_digest,p_target_catalog_schema_version,'running',v_now,p_begin_evidence)
    ON CONFLICT (migration_id) DO NOTHING;
    SELECT * INTO v_existing FROM ducklake.catalog_migration WHERE migration_id=p_migration_id;
    IF NOT FOUND OR v_existing.physical_pool_id IS DISTINCT FROM p_physical_pool_id
       OR v_existing.catalog_id IS DISTINCT FROM p_catalog_id OR v_existing.owner_id IS DISTINCT FROM p_owner_id
       OR v_existing.fencing_epoch IS DISTINCT FROM p_pool_fencing_epoch OR v_existing.global_fencing_epoch IS DISTINCT FROM p_global_fencing_epoch
       OR v_existing.current_duckdb_runtime IS DISTINCT FROM p_current_duckdb_runtime OR v_existing.current_ducklake_extension IS DISTINCT FROM p_current_ducklake_extension
       OR v_existing.current_catalog_format IS DISTINCT FROM p_current_catalog_format OR v_existing.current_compatibility_digest IS DISTINCT FROM p_current_compatibility_digest
       OR v_existing.current_catalog_schema_version IS DISTINCT FROM p_current_catalog_schema_version OR v_existing.target_duckdb_runtime IS DISTINCT FROM p_target_duckdb_runtime
       OR v_existing.target_ducklake_extension IS DISTINCT FROM p_target_ducklake_extension OR v_existing.target_catalog_format IS DISTINCT FROM p_target_catalog_format
       OR v_existing.target_compatibility_digest IS DISTINCT FROM p_target_compatibility_digest OR v_existing.target_catalog_schema_version IS DISTINCT FROM p_target_catalog_schema_version
       OR v_existing.state IS DISTINCT FROM 'running' OR v_existing.begin_evidence IS DISTINCT FROM p_begin_evidence THEN
        RAISE EXCEPTION 'migration conflict';
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.complete_catalog_migration(
    p_migration_id uuid, p_owner_id text, p_pool_fencing_epoch bigint,
    p_global_fencing_epoch bigint, p_completion_evidence jsonb
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE m record; v_now timestamptz := clock_timestamp(); v_terminal timestamptz; v_missing bigint; v_rows bigint;
BEGIN
    PERFORM 1 FROM ducklake.migration_fence WHERE scope='global' AND physical_pool_id=''
      AND owner_id=p_owner_id AND fencing_epoch=p_global_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    PERFORM 1 FROM ducklake.migration_fence WHERE scope='pool' AND physical_pool_id=(SELECT physical_pool_id FROM ducklake.catalog_migration WHERE migration_id=p_migration_id)
      AND owner_id=p_owner_id AND fencing_epoch=p_pool_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    SELECT * INTO m FROM ducklake.catalog_migration WHERE migration_id=p_migration_id FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'catalog migration not found'; END IF;
    IF m.state='completed' THEN
        IF m.completion_evidence = p_completion_evidence THEN RETURN; END IF;
        RAISE EXCEPTION 'migration conflict';
    END IF;
    IF m.state <> 'running' THEN RAISE EXCEPTION 'catalog migration terminal'; END IF;
    IF m.owner_id <> p_owner_id OR m.fencing_epoch <> p_pool_fencing_epoch OR m.global_fencing_epoch <> p_global_fencing_epoch THEN
        RAISE EXCEPTION 'migration fence stale';
    END IF;
    -- Serialize the retained-snapshot boundary with commit attempts. Writers
    -- that started before completion become part of this migration epoch;
    -- writers admitted after the transaction commits receive a later
    -- created_at and rely on their native qualification evidence instead.
    LOCK TABLE ducklake.snapshot_retention IN SHARE MODE;
    v_terminal := clock_timestamp();
    SELECT count(*) INTO v_missing FROM ducklake.snapshot_retention r
     WHERE r.physical_pool_id=m.physical_pool_id AND r.catalog_id=m.catalog_id AND r.state IN ('live','retiring')
       AND NOT EXISTS (SELECT 1 FROM ducklake.snapshot_requalification q WHERE q.physical_pool_id=r.physical_pool_id AND q.catalog_id=r.catalog_id AND q.snapshot_id=r.snapshot_id AND q.migration_id=p_migration_id AND q.status='qualified' AND q.duckdb_runtime=m.target_duckdb_runtime AND q.ducklake_extension=m.target_ducklake_extension AND q.catalog_format=m.target_catalog_format AND q.compatibility_digest=m.target_compatibility_digest AND q.catalog_schema_version=m.target_catalog_schema_version);
    IF v_missing <> 0 THEN RAISE EXCEPTION 'snapshot qualification missing'; END IF;
    UPDATE ducklake.catalog_runtime_compatibility
       SET duckdb_runtime=m.target_duckdb_runtime,ducklake_extension=m.target_ducklake_extension,
           catalog_format=m.target_catalog_format,compatibility_digest=m.target_compatibility_digest,
           catalog_schema_version=m.target_catalog_schema_version,current_migration_id=p_migration_id,
           updated_at=v_terminal
     WHERE physical_pool_id=m.physical_pool_id AND catalog_id=m.catalog_id
       AND duckdb_runtime=m.current_duckdb_runtime AND ducklake_extension=m.current_ducklake_extension
       AND catalog_format=m.current_catalog_format AND compatibility_digest=m.current_compatibility_digest
       AND catalog_schema_version=m.current_catalog_schema_version;
    GET DIAGNOSTICS v_rows = ROW_COUNT;
    IF v_rows <> 1 THEN RAISE EXCEPTION 'runtime compatibility mismatch'; END IF;
    UPDATE ducklake.catalog_migration
       SET state='completed',terminal_at=v_terminal,completion_evidence=p_completion_evidence
     WHERE migration_id=p_migration_id AND state='running' AND owner_id=p_owner_id
       AND fencing_epoch=p_pool_fencing_epoch AND global_fencing_epoch=p_global_fencing_epoch;
    GET DIAGNOSTICS v_rows = ROW_COUNT;
    IF v_rows <> 1 THEN RAISE EXCEPTION 'migration fence stale'; END IF;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.fail_catalog_migration(
    p_migration_id uuid, p_owner_id text, p_pool_fencing_epoch bigint,
    p_global_fencing_epoch bigint, p_failure_evidence jsonb,
    p_recovery_decision text, p_decision_evidence jsonb
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE m record; v_now timestamptz := clock_timestamp(); v_rows bigint;
BEGIN
    PERFORM 1 FROM ducklake.migration_fence WHERE scope='global' AND physical_pool_id=''
      AND owner_id=p_owner_id AND fencing_epoch=p_global_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    PERFORM 1 FROM ducklake.migration_fence WHERE scope='pool' AND physical_pool_id=(SELECT physical_pool_id FROM ducklake.catalog_migration WHERE migration_id=p_migration_id)
      AND owner_id=p_owner_id AND fencing_epoch=p_pool_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    SELECT * INTO m FROM ducklake.catalog_migration WHERE migration_id=p_migration_id FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'catalog migration not found'; END IF;
    IF m.state='failed' THEN
        IF m.failure_evidence = p_failure_evidence AND m.recovery_decision = p_recovery_decision AND m.decision_evidence = p_decision_evidence THEN RETURN; END IF;
        RAISE EXCEPTION 'migration conflict';
    END IF;
    IF m.state <> 'running' THEN RAISE EXCEPTION 'catalog migration terminal'; END IF;
    UPDATE ducklake.catalog_migration
       SET state='failed',terminal_at=v_now,failure_evidence=p_failure_evidence,
           recovery_decision=p_recovery_decision,decision_evidence=p_decision_evidence
     WHERE migration_id=p_migration_id AND state='running';
    GET DIAGNOSTICS v_rows = ROW_COUNT;
    IF v_rows <> 1 THEN RAISE EXCEPTION 'migration fence stale'; END IF;
END;
$$;
-- +goose StatementEnd

-- Requalification must retain its composite foreign-key protection without
-- granting the migrator role REFERENCES/UPDATE on retention rows. This
-- narrowly scoped definer function performs validation and insertion as the
-- schema owner; runtime roles do not receive EXECUTE.
DROP FUNCTION IF EXISTS ducklake.record_snapshot_requalification(uuid,text,text,bigint,uuid,text,text,text,text,text,text,jsonb,timestamptz);
DROP FUNCTION IF EXISTS ducklake.lock_snapshot_retention(text,text,bigint);
CREATE OR REPLACE FUNCTION ducklake.record_snapshot_requalification(
    p_qualification_id uuid,
    p_physical_pool_id text,
    p_catalog_id text,
    p_snapshot_id bigint,
    p_migration_id uuid,
    p_duckdb_runtime text,
    p_ducklake_extension text,
    p_catalog_format text,
    p_compatibility_digest text,
    p_catalog_schema_version text,
    p_status text,
    p_evidence jsonb,
    p_qualified_at timestamptz,
    p_owner_id text,
    p_pool_fencing_epoch bigint,
    p_global_fencing_epoch bigint
) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE
    v_now timestamptz := clock_timestamp();
    v_migration record;
    v_state text;
    v_existing record;
BEGIN
    PERFORM 1 FROM ducklake.migration_fence WHERE scope='global' AND physical_pool_id=''
      AND owner_id=p_owner_id AND fencing_epoch=p_global_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    PERFORM 1 FROM ducklake.migration_fence WHERE scope='pool' AND physical_pool_id=p_physical_pool_id
      AND owner_id=p_owner_id AND fencing_epoch=p_pool_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    SELECT q.* INTO v_existing FROM ducklake.snapshot_requalification q
     WHERE q.qualification_id=p_qualification_id FOR UPDATE;
    IF FOUND THEN
        IF v_existing.physical_pool_id IS DISTINCT FROM p_physical_pool_id
           OR v_existing.catalog_id IS DISTINCT FROM p_catalog_id
           OR v_existing.snapshot_id IS DISTINCT FROM p_snapshot_id
           OR v_existing.migration_id IS DISTINCT FROM p_migration_id
           OR v_existing.duckdb_runtime IS DISTINCT FROM p_duckdb_runtime
           OR v_existing.ducklake_extension IS DISTINCT FROM p_ducklake_extension
           OR v_existing.catalog_format IS DISTINCT FROM p_catalog_format
           OR v_existing.compatibility_digest IS DISTINCT FROM p_compatibility_digest
           OR v_existing.catalog_schema_version IS DISTINCT FROM p_catalog_schema_version
           OR v_existing.status IS DISTINCT FROM p_status
           OR v_existing.evidence IS DISTINCT FROM p_evidence THEN
            RAISE EXCEPTION 'qualification conflict';
        END IF;
        RETURN;
    END IF;
    SELECT * INTO v_migration FROM ducklake.catalog_migration WHERE migration_id=p_migration_id FOR SHARE;
    IF NOT FOUND OR v_migration.physical_pool_id <> p_physical_pool_id OR v_migration.catalog_id <> p_catalog_id
       OR v_migration.owner_id <> p_owner_id OR v_migration.fencing_epoch <> p_pool_fencing_epoch
       OR v_migration.global_fencing_epoch <> p_global_fencing_epoch OR v_migration.state <> 'running'
       OR v_migration.target_duckdb_runtime <> p_duckdb_runtime OR v_migration.target_ducklake_extension <> p_ducklake_extension
       OR v_migration.target_catalog_format <> p_catalog_format OR v_migration.target_compatibility_digest <> p_compatibility_digest
       OR v_migration.target_catalog_schema_version <> p_catalog_schema_version THEN
        RAISE EXCEPTION 'qualification conflict';
    END IF;
    SELECT state INTO v_state FROM ducklake.snapshot_retention
     WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id
       AND state IN ('live','retiring') FOR SHARE;
    IF NOT FOUND THEN RAISE EXCEPTION 'snapshot not found'; END IF;
    INSERT INTO ducklake.snapshot_requalification
      (qualification_id,physical_pool_id,catalog_id,snapshot_id,migration_id,
       duckdb_runtime,ducklake_extension,catalog_format,compatibility_digest,
       catalog_schema_version,status,evidence,qualified_at)
    VALUES
      (p_qualification_id,p_physical_pool_id,p_catalog_id,p_snapshot_id,p_migration_id,
       p_duckdb_runtime,p_ducklake_extension,p_catalog_format,p_compatibility_digest,
       p_catalog_schema_version,p_status,p_evidence,v_now);
END;
$$;
-- +goose StatementEnd

-- The orphan UUID is derived from the exact physical identity tuple.  The
-- unique relational key remains authoritative; this deterministic value makes
-- replay independent of caller-generated UUIDs.
CREATE OR REPLACE FUNCTION ducklake.snapshot_orphan_uuid(
    p_physical_pool_id text, p_catalog_id text, p_snapshot_id bigint
) RETURNS uuid
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
    SELECT md5(length(p_physical_pool_id)::text || ':' || p_physical_pool_id
             || length(p_catalog_id)::text || ':' || p_catalog_id
             || p_snapshot_id::text)::uuid
$$;
-- +goose StatementEnd

-- RetentionCoordinator submits one bounded batch per control phase.  These
-- wrappers deliberately invoke the existing fenced lifecycle capabilities in
-- one SQL statement: every child still receives the same row locks, fence,
-- operation identity, monotonicity, and evidence checks, while the client no
-- longer performs a network round trip for each child.  A failure rolls back
-- the whole statement, so a successor can replay the exact frozen set.
CREATE OR REPLACE FUNCTION ducklake.expire_snapshots_under_maintenance_fence(
    p_snapshot_ids bigint[], p_expired_at timestamptz, p_items jsonb,
    p_physical_pool_id text, p_catalog_id text, p_maintenance_id uuid,
    p_maintenance_owner_id text, p_maintenance_fencing_epoch bigint
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE v_item jsonb; v_id bigint; v_evidence jsonb; v_count integer;
BEGIN
    v_count := COALESCE(cardinality(p_snapshot_ids), 0);
    IF v_count < 1 OR v_count > 256 OR (SELECT count(DISTINCT id) FROM unnest(p_snapshot_ids) AS u(id)) <> v_count
       OR jsonb_typeof(COALESCE(p_items, 'null'::jsonb)) <> 'array'
       OR jsonb_array_length(p_items) <> v_count THEN
        RAISE EXCEPTION 'invalid retention expiry batch';
    END IF;
    FOR v_item IN SELECT value FROM jsonb_array_elements(p_items) LOOP
        IF jsonb_typeof(v_item) <> 'object' OR (v_item->>'snapshot_id') IS NULL
           OR (v_item->'evidence') IS NULL OR jsonb_typeof(v_item->'evidence') <> 'object' THEN
            RAISE EXCEPTION 'invalid retention expiry evidence';
        END IF;
        v_id := (v_item->>'snapshot_id')::bigint;
        IF NOT (v_id = ANY(p_snapshot_ids)) THEN
            RAISE EXCEPTION 'retention expiry snapshot is outside the frozen set';
        END IF;
        v_evidence := v_item->'evidence';
        PERFORM ducklake.expire_snapshot_under_maintenance_fence(
            p_expired_at, v_evidence, p_physical_pool_id, p_catalog_id, v_id,
            p_maintenance_id, p_maintenance_owner_id, p_maintenance_fencing_epoch);
    END LOOP;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.reconcile_retention_maintenance_snapshots(
    p_items jsonb, p_maintenance_id uuid, p_physical_pool_id text,
    p_catalog_id text, p_owner_id text, p_fencing_epoch bigint
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE v_item jsonb; v_id bigint; v_phase text;
        v_old_phase text; v_expiry jsonb; v_quarantine jsonb; v_cleanup jsonb;
BEGIN
    IF jsonb_typeof(COALESCE(p_items, 'null'::jsonb)) <> 'array' OR jsonb_array_length(p_items) > 256 THEN
        RAISE EXCEPTION 'invalid retention reconciliation batch';
    END IF;
    FOR v_item IN SELECT value FROM jsonb_array_elements(p_items) LOOP
        IF jsonb_typeof(v_item) <> 'object' OR (v_item->>'snapshot_id') IS NULL
           OR (v_item->>'phase') IS NULL THEN
            RAISE EXCEPTION 'invalid retention reconciliation item';
        END IF;
        v_id := (v_item->>'snapshot_id')::bigint;
        v_phase := v_item->>'phase';
        v_expiry := CASE WHEN v_item ? 'expiry_evidence' AND jsonb_typeof(v_item->'expiry_evidence') <> 'null' THEN v_item->'expiry_evidence' ELSE NULL END;
        v_quarantine := CASE WHEN v_item ? 'quarantine_evidence' AND jsonb_typeof(v_item->'quarantine_evidence') <> 'null' THEN v_item->'quarantine_evidence' ELSE NULL END;
        v_cleanup := CASE WHEN v_item ? 'cleanup_evidence' AND jsonb_typeof(v_item->'cleanup_evidence') <> 'null' THEN v_item->'cleanup_evidence' ELSE NULL END;
        SELECT s.phase INTO v_old_phase
          FROM ducklake.retention_maintenance_snapshot s
         WHERE s.maintenance_id=p_maintenance_id AND s.physical_pool_id=p_physical_pool_id
           AND s.catalog_id=p_catalog_id AND s.snapshot_id=v_id FOR UPDATE;
        IF NOT FOUND THEN RAISE EXCEPTION 'retention maintenance snapshot not found'; END IF;
        -- The immutable trigger intentionally permits only one lifecycle edge
        -- from eligible (eligible→expired→quarantined). Replay may still
        -- collapse those edges into this single network round trip.
        IF v_old_phase = 'eligible' AND v_phase IN ('quarantined','cleanup-complete') THEN
            PERFORM ducklake.update_retention_maintenance_snapshot(
                p_maintenance_id, p_physical_pool_id, p_catalog_id, v_id,
                p_owner_id, p_fencing_epoch, 'expired', v_expiry, NULL, NULL);
            v_old_phase := 'expired';
        END IF;
        IF v_old_phase IN ('eligible','expired') AND v_phase = 'cleanup-complete' THEN
            IF v_old_phase = 'eligible' THEN
                PERFORM ducklake.update_retention_maintenance_snapshot(
                    p_maintenance_id, p_physical_pool_id, p_catalog_id, v_id,
                    p_owner_id, p_fencing_epoch, 'expired', v_expiry, NULL, NULL);
            END IF;
            PERFORM ducklake.update_retention_maintenance_snapshot(
                p_maintenance_id, p_physical_pool_id, p_catalog_id, v_id,
                p_owner_id, p_fencing_epoch, 'quarantined', v_expiry, v_quarantine, NULL);
            v_old_phase := 'quarantined';
        ELSIF v_old_phase = 'eligible' AND v_phase = 'quarantined' THEN
            PERFORM ducklake.update_retention_maintenance_snapshot(
                p_maintenance_id, p_physical_pool_id, p_catalog_id, v_id,
                p_owner_id, p_fencing_epoch, 'expired', v_expiry, NULL, NULL);
            v_old_phase := 'expired';
            PERFORM ducklake.update_retention_maintenance_snapshot(
                p_maintenance_id, p_physical_pool_id, p_catalog_id, v_id,
                p_owner_id, p_fencing_epoch, 'quarantined', v_expiry, v_quarantine, NULL);
            v_old_phase := 'quarantined';
        END IF;
        IF v_old_phase <> v_phase THEN
            PERFORM ducklake.update_retention_maintenance_snapshot(
                p_maintenance_id, p_physical_pool_id, p_catalog_id, v_id,
                p_owner_id, p_fencing_epoch, v_phase, v_expiry, v_quarantine, v_cleanup);
        END IF;
    END LOOP;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.quarantine_snapshots_under_maintenance_fence(
    p_snapshot_ids bigint[], p_items jsonb, p_cleanup_lease_expires_at timestamptz,
    p_quarantined_at timestamptz,
    p_physical_pool_id text, p_catalog_id text, p_maintenance_id uuid,
    p_maintenance_owner_id text, p_maintenance_fencing_epoch bigint
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE v_item jsonb; v_id bigint; v_evidence jsonb; v_cleanup_epoch bigint; v_count integer;
BEGIN
    v_count := COALESCE(cardinality(p_snapshot_ids), 0);
    IF v_count < 1 OR v_count > 256 OR (SELECT count(DISTINCT id) FROM unnest(p_snapshot_ids) AS u(id)) <> v_count
       OR jsonb_typeof(COALESCE(p_items, 'null'::jsonb)) <> 'array'
       OR jsonb_array_length(p_items) <> v_count THEN
        RAISE EXCEPTION 'invalid retention quarantine batch';
    END IF;
    FOR v_item IN SELECT value FROM jsonb_array_elements(p_items) LOOP
        IF jsonb_typeof(v_item) <> 'object' OR (v_item->>'snapshot_id') IS NULL
           OR (v_item->'evidence') IS NULL OR jsonb_typeof(v_item->'evidence') <> 'object' THEN
            RAISE EXCEPTION 'invalid retention quarantine evidence';
        END IF;
        v_id := (v_item->>'snapshot_id')::bigint;
        IF NOT (v_id = ANY(p_snapshot_ids)) THEN
            RAISE EXCEPTION 'retention quarantine snapshot is outside the frozen set';
        END IF;
        v_evidence := v_item->'evidence';
        v_cleanup_epoch := ducklake.claim_snapshot_cleanup_under_maintenance_fence(
            p_physical_pool_id, p_catalog_id, v_id, p_maintenance_owner_id,
            p_cleanup_lease_expires_at, p_maintenance_id, p_maintenance_owner_id,
            p_maintenance_fencing_epoch);
        PERFORM ducklake.quarantine_snapshot_under_maintenance_fence(
            v_evidence, p_quarantined_at, p_physical_pool_id, p_catalog_id, v_id,
            p_maintenance_owner_id, v_cleanup_epoch, p_maintenance_id,
            p_maintenance_owner_id, p_maintenance_fencing_epoch);
    END LOOP;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.complete_snapshots_cleanup_under_maintenance_fence(
    p_snapshot_ids bigint[], p_items jsonb, p_cleanup_lease_expires_at timestamptz,
    p_cleanup_completed_at timestamptz,
    p_physical_pool_id text, p_catalog_id text, p_maintenance_id uuid,
    p_maintenance_owner_id text, p_maintenance_fencing_epoch bigint
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE v_item jsonb; v_id bigint; v_evidence jsonb; v_cleanup_epoch bigint;
        v_state text; v_owner text; v_count integer;
BEGIN
    v_count := COALESCE(cardinality(p_snapshot_ids), 0);
    IF v_count < 1 OR v_count > 256 OR (SELECT count(DISTINCT id) FROM unnest(p_snapshot_ids) AS u(id)) <> v_count
       OR jsonb_typeof(COALESCE(p_items, 'null'::jsonb)) <> 'array'
       OR jsonb_array_length(p_items) <> v_count THEN
        RAISE EXCEPTION 'invalid retention cleanup batch';
    END IF;
    FOR v_item IN SELECT value FROM jsonb_array_elements(p_items) LOOP
        IF jsonb_typeof(v_item) <> 'object' OR (v_item->>'snapshot_id') IS NULL
           OR (v_item->'evidence') IS NULL OR jsonb_typeof(v_item->'evidence') <> 'object' THEN
            RAISE EXCEPTION 'invalid retention cleanup evidence';
        END IF;
        v_id := (v_item->>'snapshot_id')::bigint;
        IF NOT (v_id = ANY(p_snapshot_ids)) THEN
            RAISE EXCEPTION 'retention cleanup snapshot is outside the frozen set';
        END IF;
        v_evidence := v_item->'evidence';
        SELECT r.state, r.cleanup_owner_id, r.cleanup_fencing_epoch
          INTO v_state, v_owner, v_cleanup_epoch
          FROM ducklake.snapshot_retention r
         WHERE r.physical_pool_id=p_physical_pool_id AND r.catalog_id=p_catalog_id
           AND r.snapshot_id=v_id FOR UPDATE;
        IF NOT FOUND THEN RAISE EXCEPTION 'snapshot retention not found'; END IF;
        IF v_state <> 'cleanup-complete' THEN
            v_cleanup_epoch := ducklake.claim_snapshot_cleanup_under_maintenance_fence(
                p_physical_pool_id, p_catalog_id, v_id, p_maintenance_owner_id,
                p_cleanup_lease_expires_at, p_maintenance_id, p_maintenance_owner_id,
                p_maintenance_fencing_epoch);
        ELSIF v_owner IS DISTINCT FROM p_maintenance_owner_id OR v_cleanup_epoch <= 0 THEN
            RAISE EXCEPTION 'maintenance fence stale';
        END IF;
        PERFORM ducklake.complete_snapshot_cleanup_under_maintenance_fence(
            v_evidence, p_cleanup_completed_at, p_physical_pool_id, p_catalog_id, v_id,
            p_maintenance_owner_id, v_cleanup_epoch, p_maintenance_id,
            p_maintenance_owner_id, p_maintenance_fencing_epoch);
    END LOOP;
END;
$$;
-- +goose StatementEnd

DROP FUNCTION IF EXISTS ducklake.register_catalog_runtime_compatibility(text,text,text,text,text,text,text);
CREATE OR REPLACE FUNCTION ducklake.register_catalog_runtime_compatibility(
    p_physical_pool_id text, p_catalog_id text, p_duckdb_runtime text,
    p_ducklake_extension text, p_catalog_format text,
    p_compatibility_digest text, p_catalog_schema_version text,
    p_owner_id text, p_pool_fencing_epoch bigint, p_global_fencing_epoch bigint
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE v_exists boolean; v_now timestamptz := clock_timestamp(); v_existing record;
BEGIN
    PERFORM 1 FROM ducklake.migration_fence
     WHERE scope='global' AND physical_pool_id='' AND owner_id=p_owner_id
       AND fencing_epoch=p_global_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    PERFORM 1 FROM ducklake.migration_fence
     WHERE scope='pool' AND physical_pool_id=p_physical_pool_id AND owner_id=p_owner_id
       AND fencing_epoch=p_pool_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    SELECT EXISTS (SELECT 1 FROM ducklake.catalog_runtime_compatibility WHERE physical_pool_id=p_physical_pool_id) INTO v_exists;
    IF v_exists THEN
        SELECT * INTO v_existing FROM ducklake.catalog_runtime_compatibility WHERE physical_pool_id=p_physical_pool_id;
        IF v_existing.catalog_id IS DISTINCT FROM p_catalog_id
           OR v_existing.duckdb_runtime IS DISTINCT FROM p_duckdb_runtime
           OR v_existing.ducklake_extension IS DISTINCT FROM p_ducklake_extension
           OR v_existing.catalog_format IS DISTINCT FROM p_catalog_format
           OR v_existing.compatibility_digest IS DISTINCT FROM p_compatibility_digest
           OR v_existing.catalog_schema_version IS DISTINCT FROM p_catalog_schema_version THEN
            RAISE EXCEPTION 'runtime compatibility mismatch';
        END IF;
        RETURN;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM ducklake.catalog_identity
         WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id
    ) THEN
        RAISE EXCEPTION 'runtime compatibility mismatch';
    END IF;
    INSERT INTO ducklake.catalog_runtime_compatibility
      (physical_pool_id,catalog_id,duckdb_runtime,ducklake_extension,catalog_format,
       compatibility_digest,catalog_schema_version,updated_at)
    VALUES (p_physical_pool_id,p_catalog_id,p_duckdb_runtime,p_ducklake_extension,
            p_catalog_format,p_compatibility_digest,p_catalog_schema_version,clock_timestamp());
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.begin_catalog_migration(
    p_migration_id uuid, p_physical_pool_id text, p_catalog_id text,
    p_owner_id text, p_pool_fencing_epoch bigint, p_global_fencing_epoch bigint,
    p_current_duckdb_runtime text, p_current_ducklake_extension text,
    p_current_catalog_format text, p_current_compatibility_digest text,
    p_current_catalog_schema_version text, p_target_duckdb_runtime text,
    p_target_ducklake_extension text, p_target_catalog_format text,
    p_target_compatibility_digest text, p_target_catalog_schema_version text,
    p_begin_evidence jsonb
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE v_now timestamptz := clock_timestamp(); v_catalog record; v_existing record;
BEGIN
    PERFORM 1 FROM ducklake.migration_fence
     WHERE scope='global' AND physical_pool_id='' AND owner_id=p_owner_id
       AND fencing_epoch=p_global_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    PERFORM 1 FROM ducklake.migration_fence
     WHERE scope='pool' AND physical_pool_id=p_physical_pool_id AND owner_id=p_owner_id
       AND fencing_epoch=p_pool_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    IF jsonb_typeof(p_begin_evidence) <> 'object' OR p_begin_evidence = '{}'::jsonb
       OR NOT ((p_begin_evidence->>'drain_verified')='true' OR (p_begin_evidence->>'drained')='true' OR (p_begin_evidence->>'readers_drained')='true')
       OR NOT ((p_begin_evidence->>'backup_verified')='true' OR (p_begin_evidence->>'backup_verification')='true' OR (p_begin_evidence->>'backup')='true') THEN
        RAISE EXCEPTION 'migration evidence required';
    END IF;
    SELECT * INTO v_existing FROM ducklake.catalog_migration WHERE migration_id=p_migration_id FOR UPDATE;
    IF FOUND THEN
        IF v_existing.physical_pool_id IS DISTINCT FROM p_physical_pool_id
           OR v_existing.catalog_id IS DISTINCT FROM p_catalog_id
           OR v_existing.owner_id IS DISTINCT FROM p_owner_id
           OR v_existing.fencing_epoch IS DISTINCT FROM p_pool_fencing_epoch
           OR v_existing.global_fencing_epoch IS DISTINCT FROM p_global_fencing_epoch
           OR v_existing.current_duckdb_runtime IS DISTINCT FROM p_current_duckdb_runtime
           OR v_existing.current_ducklake_extension IS DISTINCT FROM p_current_ducklake_extension
           OR v_existing.current_catalog_format IS DISTINCT FROM p_current_catalog_format
           OR v_existing.current_compatibility_digest IS DISTINCT FROM p_current_compatibility_digest
           OR v_existing.current_catalog_schema_version IS DISTINCT FROM p_current_catalog_schema_version
           OR v_existing.target_duckdb_runtime IS DISTINCT FROM p_target_duckdb_runtime
           OR v_existing.target_ducklake_extension IS DISTINCT FROM p_target_ducklake_extension
           OR v_existing.target_catalog_format IS DISTINCT FROM p_target_catalog_format
           OR v_existing.target_compatibility_digest IS DISTINCT FROM p_target_compatibility_digest
           OR v_existing.target_catalog_schema_version IS DISTINCT FROM p_target_catalog_schema_version
           OR v_existing.state IS DISTINCT FROM 'running'
           OR v_existing.begin_evidence IS DISTINCT FROM p_begin_evidence THEN
            RAISE EXCEPTION 'migration conflict';
        END IF;
        RETURN;
    END IF;
    SELECT * INTO v_catalog FROM ducklake.catalog_runtime_compatibility
     WHERE physical_pool_id=p_physical_pool_id FOR SHARE;
    IF NOT FOUND OR v_catalog.catalog_id <> p_catalog_id
       OR v_catalog.duckdb_runtime <> p_current_duckdb_runtime
       OR v_catalog.ducklake_extension <> p_current_ducklake_extension
       OR v_catalog.catalog_format <> p_current_catalog_format
       OR v_catalog.compatibility_digest <> p_current_compatibility_digest
       OR v_catalog.catalog_schema_version <> p_current_catalog_schema_version THEN
        RAISE EXCEPTION 'runtime compatibility mismatch';
    END IF;
    INSERT INTO ducklake.catalog_migration
      (migration_id,physical_pool_id,catalog_id,owner_id,fencing_epoch,global_fencing_epoch,
       current_duckdb_runtime,current_ducklake_extension,current_catalog_format,
       current_compatibility_digest,current_catalog_schema_version,target_duckdb_runtime,
       target_ducklake_extension,target_catalog_format,target_compatibility_digest,
       target_catalog_schema_version,state,started_at,begin_evidence)
    VALUES (p_migration_id,p_physical_pool_id,p_catalog_id,p_owner_id,p_pool_fencing_epoch,
            p_global_fencing_epoch,p_current_duckdb_runtime,p_current_ducklake_extension,
            p_current_catalog_format,p_current_compatibility_digest,p_current_catalog_schema_version,
            p_target_duckdb_runtime,p_target_ducklake_extension,p_target_catalog_format,
            p_target_compatibility_digest,p_target_catalog_schema_version,'running',v_now,p_begin_evidence)
    ON CONFLICT (migration_id) DO NOTHING;
    SELECT * INTO v_existing FROM ducklake.catalog_migration WHERE migration_id=p_migration_id;
    IF NOT FOUND OR v_existing.physical_pool_id IS DISTINCT FROM p_physical_pool_id
       OR v_existing.catalog_id IS DISTINCT FROM p_catalog_id OR v_existing.owner_id IS DISTINCT FROM p_owner_id
       OR v_existing.fencing_epoch IS DISTINCT FROM p_pool_fencing_epoch OR v_existing.global_fencing_epoch IS DISTINCT FROM p_global_fencing_epoch
       OR v_existing.current_duckdb_runtime IS DISTINCT FROM p_current_duckdb_runtime OR v_existing.current_ducklake_extension IS DISTINCT FROM p_current_ducklake_extension
       OR v_existing.current_catalog_format IS DISTINCT FROM p_current_catalog_format OR v_existing.current_compatibility_digest IS DISTINCT FROM p_current_compatibility_digest
       OR v_existing.current_catalog_schema_version IS DISTINCT FROM p_current_catalog_schema_version OR v_existing.target_duckdb_runtime IS DISTINCT FROM p_target_duckdb_runtime
       OR v_existing.target_ducklake_extension IS DISTINCT FROM p_target_ducklake_extension OR v_existing.target_catalog_format IS DISTINCT FROM p_target_catalog_format
       OR v_existing.target_compatibility_digest IS DISTINCT FROM p_target_compatibility_digest OR v_existing.target_catalog_schema_version IS DISTINCT FROM p_target_catalog_schema_version
       OR v_existing.state IS DISTINCT FROM 'running' OR v_existing.begin_evidence IS DISTINCT FROM p_begin_evidence THEN
        RAISE EXCEPTION 'migration conflict';
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.complete_catalog_migration(
    p_migration_id uuid, p_owner_id text, p_pool_fencing_epoch bigint,
    p_global_fencing_epoch bigint, p_completion_evidence jsonb
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE m record; v_now timestamptz := clock_timestamp(); v_terminal timestamptz; v_missing bigint; v_rows bigint;
BEGIN
    PERFORM 1 FROM ducklake.migration_fence WHERE scope='global' AND physical_pool_id=''
      AND owner_id=p_owner_id AND fencing_epoch=p_global_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    PERFORM 1 FROM ducklake.migration_fence WHERE scope='pool' AND physical_pool_id=(SELECT physical_pool_id FROM ducklake.catalog_migration WHERE migration_id=p_migration_id)
      AND owner_id=p_owner_id AND fencing_epoch=p_pool_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    SELECT * INTO m FROM ducklake.catalog_migration WHERE migration_id=p_migration_id FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'catalog migration not found'; END IF;
    IF m.state='completed' THEN
        IF m.completion_evidence = p_completion_evidence THEN RETURN; END IF;
        RAISE EXCEPTION 'migration conflict';
    END IF;
    IF m.state <> 'running' THEN RAISE EXCEPTION 'catalog migration terminal'; END IF;
    IF m.owner_id <> p_owner_id OR m.fencing_epoch <> p_pool_fencing_epoch OR m.global_fencing_epoch <> p_global_fencing_epoch THEN
        RAISE EXCEPTION 'migration fence stale';
    END IF;
    -- Serialize the retained-snapshot boundary with commit attempts. Writers
    -- that started before completion become part of this migration epoch;
    -- writers admitted after the transaction commits receive a later
    -- created_at and rely on their native qualification evidence instead.
    LOCK TABLE ducklake.snapshot_retention IN SHARE MODE;
    v_terminal := clock_timestamp();
    SELECT count(*) INTO v_missing FROM ducklake.snapshot_retention r
     WHERE r.physical_pool_id=m.physical_pool_id AND r.catalog_id=m.catalog_id AND r.state IN ('live','retiring')
       AND NOT EXISTS (SELECT 1 FROM ducklake.snapshot_requalification q WHERE q.physical_pool_id=r.physical_pool_id AND q.catalog_id=r.catalog_id AND q.snapshot_id=r.snapshot_id AND q.migration_id=p_migration_id AND q.status='qualified' AND q.duckdb_runtime=m.target_duckdb_runtime AND q.ducklake_extension=m.target_ducklake_extension AND q.catalog_format=m.target_catalog_format AND q.compatibility_digest=m.target_compatibility_digest AND q.catalog_schema_version=m.target_catalog_schema_version);
    IF v_missing <> 0 THEN RAISE EXCEPTION 'snapshot qualification missing'; END IF;
    UPDATE ducklake.catalog_runtime_compatibility
       SET duckdb_runtime=m.target_duckdb_runtime,ducklake_extension=m.target_ducklake_extension,
           catalog_format=m.target_catalog_format,compatibility_digest=m.target_compatibility_digest,
           catalog_schema_version=m.target_catalog_schema_version,current_migration_id=p_migration_id,
           updated_at=v_terminal
     WHERE physical_pool_id=m.physical_pool_id AND catalog_id=m.catalog_id
       AND duckdb_runtime=m.current_duckdb_runtime AND ducklake_extension=m.current_ducklake_extension
       AND catalog_format=m.current_catalog_format AND compatibility_digest=m.current_compatibility_digest
       AND catalog_schema_version=m.current_catalog_schema_version;
    GET DIAGNOSTICS v_rows = ROW_COUNT;
    IF v_rows <> 1 THEN RAISE EXCEPTION 'runtime compatibility mismatch'; END IF;
    UPDATE ducklake.catalog_migration
       SET state='completed',terminal_at=v_terminal,completion_evidence=p_completion_evidence
     WHERE migration_id=p_migration_id AND state='running' AND owner_id=p_owner_id
       AND fencing_epoch=p_pool_fencing_epoch AND global_fencing_epoch=p_global_fencing_epoch;
    GET DIAGNOSTICS v_rows = ROW_COUNT;
    IF v_rows <> 1 THEN RAISE EXCEPTION 'migration fence stale'; END IF;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.fail_catalog_migration(
    p_migration_id uuid, p_owner_id text, p_pool_fencing_epoch bigint,
    p_global_fencing_epoch bigint, p_failure_evidence jsonb,
    p_recovery_decision text, p_decision_evidence jsonb
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE m record; v_now timestamptz := clock_timestamp(); v_rows bigint;
BEGIN
    PERFORM 1 FROM ducklake.migration_fence WHERE scope='global' AND physical_pool_id=''
      AND owner_id=p_owner_id AND fencing_epoch=p_global_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    PERFORM 1 FROM ducklake.migration_fence WHERE scope='pool' AND physical_pool_id=(SELECT physical_pool_id FROM ducklake.catalog_migration WHERE migration_id=p_migration_id)
      AND owner_id=p_owner_id AND fencing_epoch=p_pool_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    SELECT * INTO m FROM ducklake.catalog_migration WHERE migration_id=p_migration_id FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'catalog migration not found'; END IF;
    IF m.state='failed' THEN
        IF m.failure_evidence = p_failure_evidence AND m.recovery_decision = p_recovery_decision AND m.decision_evidence = p_decision_evidence THEN RETURN; END IF;
        RAISE EXCEPTION 'migration conflict';
    END IF;
    IF m.state <> 'running' THEN RAISE EXCEPTION 'catalog migration terminal'; END IF;
    UPDATE ducklake.catalog_migration
       SET state='failed',terminal_at=v_now,failure_evidence=p_failure_evidence,
           recovery_decision=p_recovery_decision,decision_evidence=p_decision_evidence
     WHERE migration_id=p_migration_id AND state='running';
    GET DIAGNOSTICS v_rows = ROW_COUNT;
    IF v_rows <> 1 THEN RAISE EXCEPTION 'migration fence stale'; END IF;
END;
$$;
-- +goose StatementEnd

-- Requalification must retain its composite foreign-key protection without
-- granting the migrator role REFERENCES/UPDATE on retention rows. This
-- narrowly scoped definer function performs validation and insertion as the
-- schema owner; runtime roles do not receive EXECUTE.
DROP FUNCTION IF EXISTS ducklake.record_snapshot_requalification(uuid,text,text,bigint,uuid,text,text,text,text,text,text,jsonb,timestamptz);
DROP FUNCTION IF EXISTS ducklake.lock_snapshot_retention(text,text,bigint);
CREATE OR REPLACE FUNCTION ducklake.record_snapshot_requalification(
    p_qualification_id uuid,
    p_physical_pool_id text,
    p_catalog_id text,
    p_snapshot_id bigint,
    p_migration_id uuid,
    p_duckdb_runtime text,
    p_ducklake_extension text,
    p_catalog_format text,
    p_compatibility_digest text,
    p_catalog_schema_version text,
    p_status text,
    p_evidence jsonb,
    p_qualified_at timestamptz,
    p_owner_id text,
    p_pool_fencing_epoch bigint,
    p_global_fencing_epoch bigint
) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE
    v_now timestamptz := clock_timestamp();
    v_migration record;
    v_state text;
    v_existing record;
BEGIN
    PERFORM 1 FROM ducklake.migration_fence WHERE scope='global' AND physical_pool_id=''
      AND owner_id=p_owner_id AND fencing_epoch=p_global_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    PERFORM 1 FROM ducklake.migration_fence WHERE scope='pool' AND physical_pool_id=p_physical_pool_id
      AND owner_id=p_owner_id AND fencing_epoch=p_pool_fencing_epoch AND lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'migration fence expired'; END IF;
    SELECT q.* INTO v_existing FROM ducklake.snapshot_requalification q
     WHERE q.qualification_id=p_qualification_id FOR UPDATE;
    IF FOUND THEN
        IF v_existing.physical_pool_id IS DISTINCT FROM p_physical_pool_id
           OR v_existing.catalog_id IS DISTINCT FROM p_catalog_id
           OR v_existing.snapshot_id IS DISTINCT FROM p_snapshot_id
           OR v_existing.migration_id IS DISTINCT FROM p_migration_id
           OR v_existing.duckdb_runtime IS DISTINCT FROM p_duckdb_runtime
           OR v_existing.ducklake_extension IS DISTINCT FROM p_ducklake_extension
           OR v_existing.catalog_format IS DISTINCT FROM p_catalog_format
           OR v_existing.compatibility_digest IS DISTINCT FROM p_compatibility_digest
           OR v_existing.catalog_schema_version IS DISTINCT FROM p_catalog_schema_version
           OR v_existing.status IS DISTINCT FROM p_status
           OR v_existing.evidence IS DISTINCT FROM p_evidence THEN
            RAISE EXCEPTION 'qualification conflict';
        END IF;
        RETURN;
    END IF;
    SELECT * INTO v_migration FROM ducklake.catalog_migration WHERE migration_id=p_migration_id FOR SHARE;
    IF NOT FOUND OR v_migration.physical_pool_id <> p_physical_pool_id OR v_migration.catalog_id <> p_catalog_id
       OR v_migration.owner_id <> p_owner_id OR v_migration.fencing_epoch <> p_pool_fencing_epoch
       OR v_migration.global_fencing_epoch <> p_global_fencing_epoch OR v_migration.state <> 'running'
       OR v_migration.target_duckdb_runtime <> p_duckdb_runtime OR v_migration.target_ducklake_extension <> p_ducklake_extension
       OR v_migration.target_catalog_format <> p_catalog_format OR v_migration.target_compatibility_digest <> p_compatibility_digest
       OR v_migration.target_catalog_schema_version <> p_catalog_schema_version THEN
        RAISE EXCEPTION 'qualification conflict';
    END IF;
    SELECT state INTO v_state FROM ducklake.snapshot_retention
     WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id
       AND state IN ('live','retiring') FOR SHARE;
    IF NOT FOUND THEN RAISE EXCEPTION 'snapshot not found'; END IF;
    INSERT INTO ducklake.snapshot_requalification
      (qualification_id,physical_pool_id,catalog_id,snapshot_id,migration_id,
       duckdb_runtime,ducklake_extension,catalog_format,compatibility_digest,
       catalog_schema_version,status,evidence,qualified_at)
    VALUES
      (p_qualification_id,p_physical_pool_id,p_catalog_id,p_snapshot_id,p_migration_id,
       p_duckdb_runtime,p_ducklake_extension,p_catalog_format,p_compatibility_digest,
       p_catalog_schema_version,p_status,p_evidence,v_now);
END;
$$;
-- +goose StatementEnd


-- Begin a bounded scanner under the exact catalog-wide maintenance fence.
-- The control role receives EXECUTE only; all scan state and cursor evidence
-- remain in the immutable ledger tables below.
CREATE OR REPLACE FUNCTION ducklake.begin_snapshot_orphan_scan(
    p_scan_id uuid, p_physical_pool_id text, p_catalog_id text,
    p_owner_id text, p_fencing_epoch bigint, p_page_size integer,
    p_grace_micros bigint, p_request_evidence jsonb
) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE
    v_now timestamptz := clock_timestamp();
    v_existing record;
BEGIN
    IF p_physical_pool_id = '' OR p_physical_pool_id <> btrim(p_physical_pool_id) OR octet_length(p_physical_pool_id) > 255
       OR p_catalog_id = '' OR p_catalog_id <> btrim(p_catalog_id) OR octet_length(p_catalog_id) > 255
       OR p_owner_id = '' OR p_owner_id <> btrim(p_owner_id) OR octet_length(p_owner_id) > 255
       OR p_fencing_epoch <= 0 OR p_page_size < 1 OR p_page_size > 256
       OR p_grace_micros < 1 OR p_grace_micros > 2592000000000
       OR jsonb_typeof(COALESCE(p_request_evidence, '{}'::jsonb)) <> 'object'
       OR octet_length(COALESCE(p_request_evidence, '{}'::jsonb)::text) > 32768 THEN
        RAISE EXCEPTION 'invalid snapshot orphan scan request';
    END IF;
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_owner_id AND f.fencing_epoch=p_fencing_epoch
       AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    SELECT * INTO v_existing FROM ducklake.snapshot_orphan_scan
     WHERE scan_id=p_scan_id FOR UPDATE;
    IF FOUND THEN
        IF v_existing.physical_pool_id IS DISTINCT FROM p_physical_pool_id
           OR v_existing.catalog_id IS DISTINCT FROM p_catalog_id
           OR v_existing.page_size IS DISTINCT FROM p_page_size
           OR v_existing.grace_micros IS DISTINCT FROM p_grace_micros
           OR v_existing.request_evidence IS DISTINCT FROM COALESCE(p_request_evidence, '{}'::jsonb) THEN
            RAISE EXCEPTION 'snapshot orphan scan conflict';
        END IF;
        IF v_existing.state <> 'running' THEN RETURN; END IF;
        IF v_existing.owner_id IS DISTINCT FROM p_owner_id
           OR v_existing.fencing_epoch IS DISTINCT FROM p_fencing_epoch THEN
            IF p_fencing_epoch <= v_existing.fencing_epoch THEN
                RAISE EXCEPTION 'snapshot orphan scan owned by another fence';
            END IF;
            -- The active exact pool fence has already advanced, so a
            -- successor may take over the durable cursor without resetting
            -- page evidence or counters.
            UPDATE ducklake.snapshot_orphan_scan
               SET owner_id=p_owner_id,fencing_epoch=p_fencing_epoch,updated_at=v_now
             WHERE scan_id=p_scan_id;
        END IF;
        RETURN;
    END IF;
    INSERT INTO ducklake.snapshot_orphan_scan
      (scan_id,physical_pool_id,catalog_id,owner_id,fencing_epoch,page_size,grace_micros,cleanup_not_before,state,request_evidence,started_at,updated_at)
    VALUES
      (p_scan_id,p_physical_pool_id,p_catalog_id,p_owner_id,p_fencing_epoch,p_page_size,p_grace_micros,v_now + p_grace_micros * interval '1 microsecond','running',COALESCE(p_request_evidence, '{}'::jsonb),v_now,v_now);
END;
$$;
-- +goose StatementEnd

-- Record exactly one catalog page. The adapter supplies sorted snapshot IDs
-- and an object keyed by snapshot ID containing the catalog's bounded evidence.
-- Every candidate is rechecked against all control-plane authorities before a
-- deterministic orphan row is inserted. Replaying an existing page requires
-- byte-equivalent identities/evidence and never advances the cursor twice.
CREATE OR REPLACE FUNCTION ducklake.record_snapshot_orphan_scan_page(
    p_scan_id uuid, p_physical_pool_id text, p_catalog_id text,
    p_owner_id text, p_fencing_epoch bigint, p_page_number integer,
    p_cursor_before bigint, p_cursor_after bigint, p_snapshot_ids bigint[],
    p_page_digest text, p_evidence jsonb, p_terminal boolean
) RETURNS TABLE(next_cursor bigint, orphan_count integer)
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE
    v_now timestamptz := clock_timestamp();
    v_scan record;
    v_page record;
    v_len integer := COALESCE(cardinality(p_snapshot_ids), 0);
    v_prev bigint := p_cursor_before;
    v_id bigint;
    v_i integer;
    v_orphans integer := 0;
    v_protected boolean;
    v_item jsonb;
    v_expected_digest text;
BEGIN
    p_snapshot_ids := COALESCE(p_snapshot_ids, '{}'::bigint[]);
    p_evidence := COALESCE(p_evidence, '{}'::jsonb);
    IF p_fencing_epoch <= 0 OR p_page_number <= 0 OR p_cursor_before < 0
       OR p_cursor_after < p_cursor_before OR v_len > 256
       OR p_page_digest IS NULL OR p_page_digest !~ '^sha256:[0-9a-f]{64}$'
       OR jsonb_typeof(p_evidence) <> 'object'
       OR octet_length(p_evidence::text) > 32768 THEN
        RAISE EXCEPTION 'invalid snapshot orphan scan page';
    END IF;
    IF v_len = 0 AND p_cursor_after <> p_cursor_before THEN
        RAISE EXCEPTION 'empty terminal snapshot orphan page must preserve cursor';
    END IF;
    v_expected_digest := 'sha256:' || pg_catalog.encode(pg_catalog.sha256(pg_catalog.convert_to(p_evidence::text, 'UTF8')), 'hex');
    IF p_page_digest <> v_expected_digest THEN
        RAISE EXCEPTION 'snapshot orphan scan page digest mismatch';
    END IF;
    IF v_len = 0 AND NOT COALESCE(p_terminal, false) THEN
        RAISE EXCEPTION 'empty non-terminal snapshot orphan scan page';
    END IF;
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_owner_id AND f.fencing_epoch=p_fencing_epoch
       AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    SELECT * INTO v_scan FROM ducklake.snapshot_orphan_scan s
     WHERE s.scan_id=p_scan_id FOR UPDATE;
    IF NOT FOUND OR v_scan.physical_pool_id <> p_physical_pool_id OR v_scan.catalog_id <> p_catalog_id THEN
        RAISE EXCEPTION 'snapshot orphan scan not found';
    END IF;
    IF v_scan.owner_id <> p_owner_id OR v_scan.fencing_epoch <> p_fencing_epoch THEN
        RAISE EXCEPTION 'snapshot orphan scan fence stale';
    END IF;
    SELECT * INTO v_page FROM ducklake.snapshot_orphan_scan_page
     WHERE scan_id=p_scan_id AND page_number=p_page_number FOR UPDATE;
    IF FOUND THEN
        IF v_page.physical_pool_id <> p_physical_pool_id OR v_page.catalog_id <> p_catalog_id
           OR v_page.cursor_before <> p_cursor_before OR v_page.cursor_after <> p_cursor_after
           OR v_page.snapshot_ids IS DISTINCT FROM p_snapshot_ids
           OR v_page.page_digest <> p_page_digest OR v_page.evidence IS DISTINCT FROM COALESCE(p_evidence, '{}'::jsonb) THEN
            RAISE EXCEPTION 'snapshot orphan scan page conflict';
        END IF;
        IF v_page.terminal IS DISTINCT FROM COALESCE(p_terminal, false) THEN
            RAISE EXCEPTION 'snapshot orphan scan page conflict';
        END IF;
        RETURN QUERY SELECT v_page.cursor_after, v_page.orphan_count;
        RETURN;
    END IF;
    IF v_scan.state <> 'running' THEN RAISE EXCEPTION 'snapshot orphan scan terminal'; END IF;
    IF v_scan.pages_scanned + 1 <> p_page_number OR v_scan.cursor_snapshot_id <> p_cursor_before THEN
        RAISE EXCEPTION 'snapshot orphan scan cursor mismatch';
    END IF;
    IF v_len > v_scan.page_size THEN RAISE EXCEPTION 'snapshot orphan scan page exceeds bound'; END IF;
    IF v_len > 0 AND p_cursor_after <> p_snapshot_ids[v_len] THEN
        RAISE EXCEPTION 'snapshot orphan scan cursor must equal final snapshot';
    END IF;
    FOR v_i IN 1..v_len LOOP
        v_id := p_snapshot_ids[v_i];
        IF v_id IS NULL OR v_id <= v_prev OR NOT (p_evidence ? v_id::text) THEN
            RAISE EXCEPTION 'snapshot orphan scan page is not strictly ordered or lacks evidence';
        END IF;
        v_prev := v_id;
    END LOOP;
    FOR v_i IN 1..v_len LOOP
        v_id := p_snapshot_ids[v_i];
        -- A snapshot is protected if any authoritative control row knows it.
        -- Retention rows (including terminal rows), delivery attempts,
        -- generation/seal evidence, active reader leases, maintenance
        -- children, or live durable roots all suppress orphan classification.
        -- A delivery root with no snapshot_seal_id remains un-attributable;
        -- never guess its physical identity from target/generation metadata.
        SELECT EXISTS (
            SELECT 1 FROM ducklake.snapshot_retention r
             WHERE r.physical_pool_id=p_physical_pool_id AND r.catalog_id=p_catalog_id AND r.snapshot_id=v_id
            UNION ALL
            SELECT 1
              FROM delivery.delivery_retention_root root
              JOIN delivery.delivery_snapshot_seal seal ON seal.seal_id = root.snapshot_seal_id
             WHERE seal.physical_pool_id=p_physical_pool_id
               AND seal.catalog_id=p_catalog_id
               AND seal.ducklake_snapshot_id=v_id
               AND root.state IN ('live','retiring')
            UNION ALL
            SELECT 1
              FROM serving_state.reader_lease l
              JOIN delivery.delivery_generation g ON g.generation_id = l.generation_id
              JOIN delivery.delivery_snapshot_seal seal ON seal.seal_id = g.snapshot_seal_id
             WHERE seal.physical_pool_id=p_physical_pool_id
               AND seal.catalog_id=p_catalog_id
               AND seal.ducklake_snapshot_id=v_id
               AND l.released_at IS NULL
            UNION ALL
            SELECT 1
              FROM delivery.delivery_generation g
              JOIN delivery.delivery_snapshot_seal seal ON seal.seal_id = g.snapshot_seal_id
             WHERE seal.physical_pool_id=p_physical_pool_id
               AND seal.catalog_id=p_catalog_id
               AND seal.ducklake_snapshot_id=v_id
            UNION ALL
            SELECT 1 FROM delivery.delivery_build_attempt a
             WHERE a.physical_pool_id=p_physical_pool_id AND a.catalog_id=p_catalog_id AND a.snapshot_id=v_id
            UNION ALL
            SELECT 1 FROM ducklake.retention_maintenance_snapshot m
             WHERE m.physical_pool_id=p_physical_pool_id AND m.catalog_id=p_catalog_id AND m.snapshot_id=v_id
        ) INTO v_protected;
        IF NOT v_protected THEN
            v_item := p_evidence -> (v_id::text);
            IF jsonb_typeof(v_item) <> 'object' THEN RAISE EXCEPTION 'snapshot orphan evidence must be an object'; END IF;
            INSERT INTO ducklake.snapshot_orphan
              (orphan_id,physical_pool_id,catalog_id,snapshot_id,state,evidence,discovered_at,cleanup_not_before)
            VALUES
              (ducklake.snapshot_orphan_uuid(p_physical_pool_id,p_catalog_id,v_id),p_physical_pool_id,p_catalog_id,v_id,'quarantined',
               jsonb_build_object('catalog',v_item,'snapshot_id',v_id),v_now,v_scan.cleanup_not_before)
            ON CONFLICT (physical_pool_id,catalog_id,snapshot_id) DO NOTHING;
            IF FOUND THEN
                v_orphans := v_orphans + 1;
            ELSE
                SELECT evidence INTO v_item FROM ducklake.snapshot_orphan
                 WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=v_id FOR UPDATE;
                IF (v_item -> 'catalog') IS DISTINCT FROM (COALESCE(p_evidence, '{}'::jsonb) -> (v_id::text)) THEN
                    RAISE EXCEPTION 'snapshot orphan evidence conflict';
                END IF;
            END IF;
        END IF;
    END LOOP;
    INSERT INTO ducklake.snapshot_orphan_scan_page
      (scan_id,physical_pool_id,catalog_id,page_number,cursor_before,cursor_after,snapshot_ids,orphan_count,terminal,page_digest,evidence,created_at)
    VALUES
      (p_scan_id,p_physical_pool_id,p_catalog_id,p_page_number,p_cursor_before,p_cursor_after,p_snapshot_ids,v_orphans,COALESCE(p_terminal,false),p_page_digest,p_evidence,v_now);
    UPDATE ducklake.snapshot_orphan_scan
       SET cursor_snapshot_id=p_cursor_after,pages_scanned=pages_scanned+1,
           snapshots_scanned=snapshots_scanned+v_len,orphans_recorded=orphans_recorded+v_orphans,
           updated_at=v_now
     WHERE scan_id=p_scan_id;
    RETURN QUERY SELECT p_cursor_after, v_orphans;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.complete_snapshot_orphan_scan(
    p_scan_id uuid, p_physical_pool_id text, p_catalog_id text,
    p_owner_id text, p_fencing_epoch bigint, p_completion_evidence jsonb
) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE
    v_now timestamptz := clock_timestamp();
    v_scan record;
    v_terminal boolean;
BEGIN
    IF p_fencing_epoch <= 0 OR jsonb_typeof(COALESCE(p_completion_evidence, '{}'::jsonb)) <> 'object' THEN
        RAISE EXCEPTION 'invalid snapshot orphan scan completion';
    END IF;
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_owner_id AND f.fencing_epoch=p_fencing_epoch AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    SELECT * INTO v_scan FROM ducklake.snapshot_orphan_scan WHERE scan_id=p_scan_id FOR UPDATE;
    IF NOT FOUND OR v_scan.physical_pool_id <> p_physical_pool_id OR v_scan.catalog_id <> p_catalog_id THEN RAISE EXCEPTION 'snapshot orphan scan not found'; END IF;
    IF v_scan.owner_id <> p_owner_id OR v_scan.fencing_epoch <> p_fencing_epoch THEN RAISE EXCEPTION 'snapshot orphan scan fence stale'; END IF;
    IF v_scan.state = 'completed' THEN
        IF v_scan.completion_evidence IS DISTINCT FROM COALESCE(p_completion_evidence, '{}'::jsonb) THEN RAISE EXCEPTION 'snapshot orphan scan completion conflict'; END IF;
        RETURN;
    END IF;
    IF v_scan.state <> 'running' THEN RAISE EXCEPTION 'snapshot orphan scan terminal'; END IF;
    SELECT p.terminal INTO v_terminal FROM ducklake.snapshot_orphan_scan_page p
     WHERE p.scan_id=p_scan_id ORDER BY p.page_number DESC LIMIT 1;
    IF NOT FOUND OR NOT v_terminal THEN RAISE EXCEPTION 'snapshot orphan scan requires a terminal page'; END IF;
    UPDATE ducklake.snapshot_orphan_scan
       SET state='completed',updated_at=v_now,completed_at=v_now,
           completion_evidence=COALESCE(p_completion_evidence, '{}'::jsonb)
     WHERE scan_id=p_scan_id;
END;
$$;
-- +goose StatementEnd

-- Prune page payloads only after a completed scan has aged past the bounded
-- policy window. The scan summary, counters, completion evidence, and a
-- server-computed digest of the removed page sequence remain as audit proof.
CREATE OR REPLACE FUNCTION ducklake.prune_snapshot_orphan_scan_pages(
    p_physical_pool_id text, p_catalog_id text, p_owner_id text,
    p_fencing_epoch bigint, p_min_age_micros bigint, p_max_scans integer
) RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE
    v_now timestamptz := clock_timestamp();
    v_cutoff timestamptz;
    v_scan record;
    v_count integer := 0;
    v_pages integer;
    v_digest text;
BEGIN
    IF p_physical_pool_id = '' OR p_catalog_id = '' OR p_owner_id = ''
       OR p_fencing_epoch <= 0 OR p_min_age_micros < 86400000000
       OR p_min_age_micros > 2592000000000 OR p_max_scans < 1 OR p_max_scans > 64 THEN
        RAISE EXCEPTION 'invalid snapshot orphan scan prune request';
    END IF;
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_owner_id AND f.fencing_epoch=p_fencing_epoch
       AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    v_cutoff := v_now - p_min_age_micros * interval '1 microsecond';
    FOR v_scan IN
        SELECT s.scan_id FROM ducklake.snapshot_orphan_scan s
         WHERE s.physical_pool_id=p_physical_pool_id AND s.catalog_id=p_catalog_id
           AND s.state='completed' AND s.completed_at IS NOT NULL
           AND s.completed_at <= v_cutoff AND s.pruned_at IS NULL
         ORDER BY s.completed_at,s.scan_id
         LIMIT p_max_scans
         FOR UPDATE
    LOOP
        SELECT count(*)::integer,
               COALESCE('sha256:' || pg_catalog.encode(pg_catalog.sha256(pg_catalog.convert_to(string_agg(p.page_digest, ',' ORDER BY p.page_number), 'UTF8')), 'hex'), 'sha256:' || pg_catalog.encode(pg_catalog.sha256(pg_catalog.convert_to('', 'UTF8')), 'hex'))
          INTO v_pages,v_digest
          FROM ducklake.snapshot_orphan_scan_page p WHERE p.scan_id=v_scan.scan_id;
        PERFORM set_config('ducklake.scan_prune', 'on', true);
        DELETE FROM ducklake.snapshot_orphan_scan_page WHERE scan_id=v_scan.scan_id;
        PERFORM set_config('ducklake.scan_prune', 'off', true);
        UPDATE ducklake.snapshot_orphan_scan
           SET pruned_at=v_now,pruned_page_count=v_pages,pruned_page_digest=v_digest
         WHERE scan_id=v_scan.scan_id;
        v_count := v_count + 1;
    END LOOP;
    RETURN v_count;
END;
$$;
-- +goose StatementEnd

-- Fenced orphan cleanup capabilities. Existing direct DML remains denied to
-- the maintenance role; these functions validate the exact pool fence and
-- enforce monotonic lifecycle transitions under row locks.
CREATE OR REPLACE FUNCTION ducklake.claim_snapshot_orphan_cleanup_under_pool_fence(
    p_physical_pool_id text, p_catalog_id text, p_snapshot_id bigint,
    p_owner_id text, p_cleanup_lease_expires_at timestamptz,
    p_fence_owner_id text, p_fencing_epoch bigint
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE v_now timestamptz := clock_timestamp(); v_epoch bigint; v_owner text; v_expiry timestamptz; v_state text;
BEGIN
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_fence_owner_id AND f.fencing_epoch=p_fencing_epoch AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    SELECT state,cleanup_owner_id,cleanup_fencing_epoch,cleanup_lease_expires_at INTO v_state,v_owner,v_epoch,v_expiry
      FROM ducklake.snapshot_orphan WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'snapshot orphan not found'; END IF;
    IF v_state <> 'quarantined' THEN RAISE EXCEPTION 'snapshot orphan is terminal'; END IF;
    IF v_owner IS NOT NULL AND v_expiry > v_now THEN
        IF v_owner = p_owner_id THEN RETURN v_epoch; END IF;
        RAISE EXCEPTION 'snapshot orphan cleanup busy';
    END IF;
    IF p_cleanup_lease_expires_at IS NULL OR p_cleanup_lease_expires_at <= v_now OR p_cleanup_lease_expires_at > v_now + interval '24 hours' THEN RAISE EXCEPTION 'invalid orphan cleanup lease'; END IF;
    IF (SELECT cleanup_not_before FROM ducklake.snapshot_orphan
         WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id) > v_now THEN
        RAISE EXCEPTION 'snapshot orphan cleanup grace is active';
    END IF;
    -- Recheck every protected authority while the exact fence and orphan row
    -- remain locked. The admission path takes the same pool-fence row first,
    -- so no new running writer can slip in after this check. Roots lacking a
    -- snapshot seal are deliberately not assigned a physical identity.
    IF EXISTS (SELECT 1 FROM ducklake.snapshot_retention r
               WHERE r.physical_pool_id=p_physical_pool_id AND r.catalog_id=p_catalog_id AND r.snapshot_id=p_snapshot_id)
       OR EXISTS (
              SELECT 1
                FROM delivery.delivery_retention_root root
                JOIN delivery.delivery_snapshot_seal seal ON seal.seal_id = root.snapshot_seal_id
               WHERE seal.physical_pool_id=p_physical_pool_id
                 AND seal.catalog_id=p_catalog_id
                 AND seal.ducklake_snapshot_id=p_snapshot_id
                 AND root.state IN ('live','retiring'))
       OR EXISTS (
              SELECT 1
                FROM serving_state.reader_lease l
                JOIN delivery.delivery_generation g ON g.generation_id = l.generation_id
                JOIN delivery.delivery_snapshot_seal seal ON seal.seal_id = g.snapshot_seal_id
               WHERE seal.physical_pool_id=p_physical_pool_id
                 AND seal.catalog_id=p_catalog_id
                 AND seal.ducklake_snapshot_id=p_snapshot_id
                 AND l.released_at IS NULL)
       OR EXISTS (
              SELECT 1
                FROM delivery.delivery_generation g
                JOIN delivery.delivery_snapshot_seal seal ON seal.seal_id = g.snapshot_seal_id
               WHERE seal.physical_pool_id=p_physical_pool_id
                 AND seal.catalog_id=p_catalog_id
                 AND seal.ducklake_snapshot_id=p_snapshot_id)
       OR EXISTS (SELECT 1 FROM delivery.delivery_build_attempt a
                  WHERE a.physical_pool_id=p_physical_pool_id AND a.catalog_id=p_catalog_id AND a.snapshot_id=p_snapshot_id)
       OR EXISTS (SELECT 1 FROM ducklake.retention_maintenance_snapshot m
                  WHERE m.physical_pool_id=p_physical_pool_id AND m.catalog_id=p_catalog_id AND m.snapshot_id=p_snapshot_id) THEN
        RAISE EXCEPTION 'snapshot orphan became protected';
    END IF;
    UPDATE ducklake.snapshot_orphan
       SET cleanup_owner_id=p_owner_id,cleanup_fencing_epoch=v_epoch+1,cleanup_lease_expires_at=p_cleanup_lease_expires_at
     WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id;
    RETURN v_epoch+1;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION ducklake.complete_snapshot_orphan_cleanup_under_pool_fence(
    p_physical_pool_id text, p_catalog_id text, p_snapshot_id bigint,
    p_owner_id text, p_fencing_epoch bigint, p_evidence jsonb,
    p_fence_owner_id text, p_pool_fencing_epoch bigint
) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, ducklake
-- +goose StatementBegin
AS $$
DECLARE v_now timestamptz := clock_timestamp(); v_state text; v_owner text; v_epoch bigint; v_expiry timestamptz; v_existing jsonb;
BEGIN
    IF jsonb_typeof(COALESCE(p_evidence, '{}'::jsonb)) <> 'object' OR p_evidence = '{}'::jsonb THEN RAISE EXCEPTION 'cleanup evidence is required'; END IF;
    PERFORM 1 FROM ducklake.pool_maintenance_fence f
     WHERE f.physical_pool_id=p_physical_pool_id AND f.catalog_id=p_catalog_id
       AND f.owner_id=p_fence_owner_id AND f.fencing_epoch=p_pool_fencing_epoch AND f.lease_expires_at > v_now FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'maintenance fence expired'; END IF;
    SELECT state,cleanup_owner_id,cleanup_fencing_epoch,cleanup_lease_expires_at,evidence INTO v_state,v_owner,v_epoch,v_expiry,v_existing
      FROM ducklake.snapshot_orphan WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'snapshot orphan not found'; END IF;
    IF v_state='cleanup-complete' THEN
        IF (v_existing -> 'cleanup') IS DISTINCT FROM p_evidence THEN
            RAISE EXCEPTION 'snapshot orphan cleanup evidence conflict';
        END IF;
        RETURN;
    END IF;
    IF v_state <> 'quarantined' OR v_owner IS DISTINCT FROM p_owner_id OR v_epoch <> p_fencing_epoch OR v_expiry IS NULL OR v_expiry <= v_now THEN RAISE EXCEPTION 'snapshot orphan cleanup fence stale'; END IF;
    -- Preserve the immutable discovery/catalog proof and append cleanup
    -- evidence under a bounded namespaced object. Replays compare only this
    -- subobject, never replacing the original observation.
    UPDATE ducklake.snapshot_orphan
       SET state='cleanup-complete',
           evidence=jsonb_set(v_existing, '{cleanup}', p_evidence, true),
           resolved_at=v_now
     WHERE physical_pool_id=p_physical_pool_id AND catalog_id=p_catalog_id AND snapshot_id=p_snapshot_id;
END;
$$;
-- +goose StatementEnd

-- DuckLake control state is capability-gated.  PUBLIC receives no schema,
-- relation, sequence, or trigger-function privileges; application roles are
-- granted only the exact lifecycle operations exposed by the repository.
REVOKE ALL ON SCHEMA ducklake FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA ducklake FROM PUBLIC;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA ducklake FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA ducklake FROM PUBLIC;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        EXECUTE 'GRANT USAGE ON SCHEMA ducklake TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT, INSERT ON TABLE ducklake.catalog_identity TO leapview_control_runtime';
        EXECUTE 'REVOKE UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE ducklake.catalog_identity FROM leapview_control_runtime';
        EXECUTE 'GRANT SELECT ON TABLE ducklake.snapshot_retention TO leapview_control_runtime';
        EXECUTE 'REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLE ducklake.snapshot_retention FROM leapview_control_runtime';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.admit_snapshot_retention_from_seal(uuid) TO leapview_control_runtime';
        -- Runtime admission does not discover or reconcile physical orphans.
        -- Keep the row visible for bounded diagnostics, but remove every
        -- direct lifecycle mutation capability (including grants left by an
        -- earlier schema revision).
        EXECUTE 'REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES ON TABLE ducklake.snapshot_orphan FROM leapview_control_runtime';
        EXECUTE 'GRANT SELECT ON TABLE ducklake.snapshot_orphan TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT ON TABLE ducklake.snapshot_orphan_scan, ducklake.snapshot_orphan_scan_page TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT, INSERT ON TABLE ducklake.marker_quarantine TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT, INSERT ON TABLE ducklake.source_observation_capture TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT ON TABLE ducklake.catalog_runtime_compatibility, ducklake.migration_fence, ducklake.pool_maintenance_fence, ducklake.retention_maintenance, ducklake.retention_maintenance_snapshot, ducklake.catalog_migration, ducklake.snapshot_requalification TO leapview_control_runtime';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.assert_attempt_admission_fence(text,text) TO leapview_control_runtime';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        EXECUTE 'GRANT USAGE ON SCHEMA ducklake TO leapview_control_readonly';
        EXECUTE 'REVOKE EXECUTE ON FUNCTION ducklake.assert_attempt_admission_fence(text,text) FROM leapview_control_readonly';
        EXECUTE 'REVOKE ALL ON FUNCTION ducklake.admit_snapshot_retention_from_seal(uuid) FROM leapview_control_readonly';
        EXECUTE 'REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES ON TABLE ducklake.snapshot_orphan FROM leapview_control_readonly';
        EXECUTE 'GRANT SELECT ON TABLE '
            || 'ducklake.catalog_identity, ducklake.snapshot_retention, '
            || 'ducklake.snapshot_orphan, ducklake.marker_quarantine, '
            || 'ducklake.snapshot_orphan_scan, ducklake.snapshot_orphan_scan_page, '
            || 'ducklake.catalog_runtime_compatibility, ducklake.migration_fence, ducklake.pool_maintenance_fence, '
            || 'ducklake.retention_maintenance, ducklake.retention_maintenance_snapshot, '
            || 'ducklake.catalog_migration, ducklake.snapshot_requalification, '
            || 'ducklake.source_observation_capture '
            || 'TO leapview_control_readonly';
    END IF;
    -- The upgrade coordinator is a control-database capability.  It is
    -- deliberately distinct from the DuckLake catalog migrator role (which
    -- is owner-capable in the separate leapview_ducklake database and has no
    -- control-database CONNECT privilege).
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_upgrade_coordinator') THEN
        EXECUTE 'GRANT USAGE ON SCHEMA ducklake TO leapview_control_upgrade_coordinator';
        EXECUTE 'REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES ON TABLE ducklake.catalog_runtime_compatibility, ducklake.catalog_migration, ducklake.snapshot_requalification, ducklake.migration_fence FROM leapview_control_upgrade_coordinator';
        EXECUTE 'GRANT SELECT ON TABLE ducklake.catalog_identity, ducklake.snapshot_retention, ducklake.catalog_runtime_compatibility, ducklake.migration_fence, ducklake.catalog_migration, ducklake.snapshot_requalification TO leapview_control_upgrade_coordinator';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.acquire_migration_fence(text,text,text,timestamptz) TO leapview_control_upgrade_coordinator';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.release_migration_fence(text,text,text,bigint) TO leapview_control_upgrade_coordinator';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.renew_migration_fence(text,text,text,bigint,timestamptz) TO leapview_control_upgrade_coordinator';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.register_catalog_runtime_compatibility(text,text,text,text,text,text,text,text,bigint,bigint) TO leapview_control_upgrade_coordinator';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.begin_catalog_migration(uuid,text,text,text,bigint,bigint,text,text,text,text,text,text,text,text,text,text,jsonb) TO leapview_control_upgrade_coordinator';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.complete_catalog_migration(uuid,text,bigint,bigint,jsonb) TO leapview_control_upgrade_coordinator';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.fail_catalog_migration(uuid,text,bigint,bigint,jsonb,text,jsonb) TO leapview_control_upgrade_coordinator';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.record_snapshot_requalification(uuid,text,text,bigint,uuid,text,text,text,text,text,text,jsonb,timestamptz,text,bigint,bigint) TO leapview_control_upgrade_coordinator';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        EXECUTE 'GRANT USAGE ON SCHEMA ducklake TO leapview_control_maintenance';
        EXECUTE 'REVOKE EXECUTE ON FUNCTION ducklake.assert_attempt_admission_fence(text,text) FROM leapview_control_maintenance';
        EXECUTE 'REVOKE ALL ON FUNCTION ducklake.admit_snapshot_retention_from_seal(uuid) FROM leapview_control_maintenance';
        EXECUTE 'REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES ON TABLE ducklake.snapshot_orphan FROM leapview_control_maintenance';
        EXECUTE 'REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES ON TABLE ducklake.snapshot_orphan_scan, ducklake.snapshot_orphan_scan_page FROM leapview_control_maintenance';
        EXECUTE 'REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES ON TABLE ducklake.retention_maintenance, ducklake.retention_maintenance_snapshot, ducklake.snapshot_retention FROM leapview_control_maintenance';
        EXECUTE 'GRANT SELECT ON TABLE ducklake.pool_maintenance_fence TO leapview_control_maintenance';
        EXECUTE 'GRANT SELECT ON TABLE ducklake.retention_maintenance, ducklake.retention_maintenance_snapshot, ducklake.snapshot_retention TO leapview_control_maintenance';
        EXECUTE 'GRANT SELECT ON TABLE ducklake.catalog_identity, ducklake.snapshot_orphan, ducklake.snapshot_orphan_scan, ducklake.snapshot_orphan_scan_page TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.acquire_pool_maintenance_fence(text,text,text,timestamptz) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.release_pool_maintenance_fence(text,text,text,bigint) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.renew_pool_maintenance_fence(text,text,text,bigint,timestamptz) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.begin_retention_maintenance(uuid,text,text,text,bigint,boolean,bigint,text,jsonb) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.update_retention_maintenance(uuid,text,text,text,bigint,text,text,boolean,bigint,text,jsonb,timestamptz) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.insert_retention_maintenance_snapshot(uuid,text,text,bigint,text,bigint) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.insert_retention_maintenance_snapshots(uuid,text,text,bigint[],text,bigint) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.claim_retention_snapshots(uuid,text,text,text,bigint,integer) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.expire_snapshot_under_maintenance_fence(timestamptz,jsonb,text,text,bigint,uuid,text,bigint) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.claim_snapshot_cleanup_under_maintenance_fence(text,text,bigint,text,timestamptz,uuid,text,bigint) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.quarantine_snapshot_under_maintenance_fence(jsonb,timestamptz,text,text,bigint,text,bigint,uuid,text,bigint) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.complete_snapshot_cleanup_under_maintenance_fence(jsonb,timestamptz,text,text,bigint,text,bigint,uuid,text,bigint) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.begin_snapshot_orphan_scan(uuid,text,text,text,bigint,integer,bigint,jsonb) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.record_snapshot_orphan_scan_page(uuid,text,text,text,bigint,integer,bigint,bigint,bigint[],text,jsonb,boolean) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.complete_snapshot_orphan_scan(uuid,text,text,text,bigint,jsonb) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.prune_snapshot_orphan_scan_pages(text,text,text,bigint,bigint,integer) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.claim_snapshot_orphan_cleanup_under_pool_fence(text,text,bigint,text,timestamptz,text,bigint) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION ducklake.complete_snapshot_orphan_cleanup_under_pool_fence(text,text,bigint,text,bigint,jsonb,text,bigint) TO leapview_control_maintenance';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        EXECUTE 'REVOKE ALL ON FUNCTION ducklake.admit_snapshot_retention_from_seal(uuid) FROM leapview_control_backup';
    END IF;
END
$$;
-- +goose StatementEnd

-- capability source: internal/platform/jobs/postgres/schema.sql
-- LeapView-owned product history for asynchronous operations.
-- River owns operational queueing, claims, retries, leases, scheduling, and
-- worker lifecycle in its public.river_* tables. These rows remain after
-- River cleanup so public IDs, authorization, evidence, and event history do
-- not depend on the executor's retention policy.
CREATE SCHEMA IF NOT EXISTS jobs;

CREATE OR REPLACE FUNCTION jobs.guard_river_result_fence()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, jobs
-- +goose StatementBegin
AS $$
DECLARE
    result_fence jsonb;
    expected_attempt text;
    expected_owner text;
BEGIN
    result_fence := NEW.metadata -> 'leapview:river_result_fence';
    IF result_fence IS NULL THEN
        RETURN NEW;
    END IF;

    -- The fence is transport-only evidence supplied by the finishing worker.
    -- Never retain it in River metadata, even for an accepted current result.
    NEW.metadata := NEW.metadata - 'leapview:river_result_fence';
    IF OLD.state <> 'running' THEN
        -- River may retry a stale completion after the successor has already
        -- left running. Return the untouched terminal row so the completer can
        -- drain without merging stale output or other worker metadata.
        RETURN OLD;
    END IF;
    IF jsonb_typeof(result_fence) IS DISTINCT FROM 'object' THEN
        RAISE EXCEPTION 'invalid River result fence';
    END IF;
    expected_attempt := result_fence ->> 'attempt';
    expected_owner := result_fence ->> 'owner';
    IF expected_attempt IS NULL
       OR expected_attempt !~ '^[1-9][0-9]*$'
       OR expected_attempt IS DISTINCT FROM OLD.attempt::text
       OR expected_owner IS NULL
       OR expected_owner IS DISTINCT FROM btrim(expected_owner)
       OR expected_owner = ''
       OR coalesce(array_length(OLD.attempted_by, 1), 0) = 0
       OR expected_owner IS DISTINCT FROM OLD.attempted_by[array_upper(OLD.attempted_by, 1)] THEN
        -- Abort River's ID-only UPDATE while another attempt is running. The
        -- LeapView River executor isolates this SQLSTATE to the stale row, so
        -- unrelated results in the same completion batch still commit.
        RAISE EXCEPTION 'stale River result fence' USING ERRCODE = 'LV001';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('public.river_job') IS NOT NULL THEN
        EXECUTE 'CREATE OR REPLACE TRIGGER river_result_fence_guard
            BEFORE UPDATE ON public.river_job
            FOR EACH ROW EXECUTE FUNCTION jobs.guard_river_result_fence()';
    END IF;
END
$$;
-- +goose StatementEnd

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
REVOKE ALL ON FUNCTION jobs.guard_river_result_fence(), jobs.guard_job_history_update(), jobs.reject_event_mutation(),
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
-- capability source: internal/agent/postgres/schema.sql
-- Clean-slate PostgreSQL agent persistence. Agent state is deliberately
-- separate from the platform jobs authority: jobs and workflow events are
-- linked through caller-owned transactions, never through cross-schema FKs.
CREATE SCHEMA IF NOT EXISTS agent;

CREATE TABLE IF NOT EXISTS agent.conversations (
    id              text PRIMARY KEY,
    principal_id    text NOT NULL CHECK (principal_id = btrim(principal_id) AND length(principal_id) BETWEEN 1 AND 256),
    title           text NOT NULL CHECK (title = btrim(title) AND length(title) BETWEEN 1 AND 512),
    status          text NOT NULL CHECK (status IN ('active', 'archived')),
    metadata_json   jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata_json) = 'object' AND octet_length(metadata_json::text) <= 1048576),
    transcript_json jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(transcript_json) = 'array' AND octet_length(transcript_json::text) <= 1048576),
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    archived_at     timestamptz,
    CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 256),
    CHECK ((status = 'archived') = (archived_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS conversations_principal_updated_idx
    ON agent.conversations(principal_id, updated_at DESC, created_at DESC);

CREATE TABLE IF NOT EXISTS agent.runs (
    id              text PRIMARY KEY,
    conversation_id text NOT NULL REFERENCES agent.conversations(id) ON DELETE CASCADE,
    status          text NOT NULL CHECK (status IN ('preparing', 'running', 'completed', 'failed', 'canceled')),
    model           text NOT NULL DEFAULT '',
    stop_reason     text NOT NULL DEFAULT '',
    input_tokens    bigint NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    output_tokens   bigint NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
    total_tokens    bigint NOT NULL DEFAULT 0 CHECK (total_tokens >= 0),
    error           text NOT NULL DEFAULT '',
    metadata_json   jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata_json) = 'object' AND octet_length(metadata_json::text) <= 1048576),
    next_event_sequence bigint NOT NULL DEFAULT 1 CHECK (next_event_sequence > 0),
    started_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at     timestamptz,
    CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 256),
    CHECK ((status IN ('completed', 'failed', 'canceled')) = (finished_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS runs_conversation_started_idx
    ON agent.runs(conversation_id, started_at DESC, id);

CREATE TABLE IF NOT EXISTS agent.messages (
    id              text PRIMARY KEY,
    conversation_id text NOT NULL REFERENCES agent.conversations(id) ON DELETE CASCADE,
    run_id          text REFERENCES agent.runs(id) ON DELETE SET NULL,
    sequence        bigint NOT NULL CHECK (sequence > 0),
    role            text NOT NULL CHECK (role IN ('user', 'assistant', 'tool', 'summary')),
    content_text    text NOT NULL DEFAULT '',
    content_json    jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(content_json) = 'object' AND octet_length(content_json::text) <= 1048576),
    tool_call_id    text NOT NULL DEFAULT '',
    tool_name       text NOT NULL DEFAULT '',
    is_error        boolean NOT NULL DEFAULT false,
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (conversation_id, sequence)
);

CREATE INDEX IF NOT EXISTS messages_conversation_sequence_idx
    ON agent.messages(conversation_id, sequence);

CREATE TABLE IF NOT EXISTS agent.events (
    event_id        bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    run_id          text NOT NULL REFERENCES agent.runs(id) ON DELETE CASCADE,
    aggregate_version bigint NOT NULL CHECK (aggregate_version > 0),
    stream_sequence bigint NOT NULL DEFAULT 0 CHECK (stream_sequence >= 0),
    event_type      text NOT NULL CHECK (event_type = btrim(event_type) AND length(event_type) BETWEEN 1 AND 128),
    severity        text NOT NULL DEFAULT 'info' CHECK (severity = btrim(severity) AND length(severity) BETWEEN 1 AND 32),
    payload_json    jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload_json) = 'object' AND octet_length(payload_json::text) <= 1048576),
    event_key       text NOT NULL DEFAULT '' CHECK (length(event_key) <= 256),
    created_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (run_id, aggregate_version)
);

CREATE UNIQUE INDEX IF NOT EXISTS events_run_key_idx
    ON agent.events(run_id, event_key) WHERE event_key <> '';
CREATE INDEX IF NOT EXISTS events_run_sequence_idx ON agent.events(run_id, aggregate_version, event_id);

-- Retention floors are durable evidence of the latest fully drained policy
-- boundary for each agent evidence class.  A floor is a cursor, never an
-- authorization shortcut: runtime writes continue to use the normal
-- repository leaves, while only the bounded maintenance function below can
-- remove history or advance these rows.
CREATE TABLE IF NOT EXISTS agent.retention_floor (
    retention_class text PRIMARY KEY CHECK (retention_class IN ('conversations', 'run_events')),
    floor_at        timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00'::timestamptz,
    updated_at      timestamptz NOT NULL DEFAULT clock_timestamp()
);
INSERT INTO agent.retention_floor (retention_class)
VALUES ('conversations'), ('run_events')
ON CONFLICT (retention_class) DO NOTHING;

-- Agent evidence is immutable to request-serving roles.  The retention
-- function sets a transaction-local marker and requires the separately
-- authenticated maintenance session; merely forging the marker cannot bypass
-- this trigger.  Child rows are guarded as well because conversation removal
-- cascades runs, messages and events through their foreign keys.
CREATE OR REPLACE FUNCTION agent.reject_history_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
-- +goose StatementBegin
AS $$
BEGIN
    IF TG_OP = 'DELETE'
       AND current_setting('agent.retention', true) = 'on'
       AND session_user = 'leapview_control_maintenance' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'agent history is immutable';
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS conversations_no_delete ON agent.conversations;
CREATE TRIGGER conversations_no_delete
    BEFORE DELETE ON agent.conversations
    FOR EACH ROW EXECUTE FUNCTION agent.reject_history_mutation();
DROP TRIGGER IF EXISTS runs_no_delete ON agent.runs;
CREATE TRIGGER runs_no_delete
    BEFORE DELETE ON agent.runs
    FOR EACH ROW EXECUTE FUNCTION agent.reject_history_mutation();
DROP TRIGGER IF EXISTS messages_no_delete ON agent.messages;
CREATE TRIGGER messages_no_delete
    BEFORE DELETE ON agent.messages
    FOR EACH ROW EXECUTE FUNCTION agent.reject_history_mutation();
DROP TRIGGER IF EXISTS events_append_only ON agent.events;
CREATE TRIGGER events_append_only
    BEFORE UPDATE OR DELETE ON agent.events
    FOR EACH ROW EXECUTE FUNCTION agent.reject_history_mutation();

-- Remove one bounded batch of run-stream events.  The target is capped by the
-- database clock and cannot move backwards from a previously drained floor.
-- The floor advances only when no eligible row remains: a smaller batch or a
-- row held by another maintenance transaction therefore leaves the floor
-- unchanged, preserving a truthful durable boundary.
CREATE OR REPLACE FUNCTION agent.prune_archived_run_events(
    p_requested_cutoff timestamptz,
    p_batch_limit integer
)
RETURNS TABLE (
    requested_cutoff timestamptz,
    cutoff timestamptz,
    requested_limit integer,
    removed_count bigint,
    retained_floor timestamptz
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, agent
-- +goose StatementBegin
AS $$
DECLARE
    v_floor timestamptz;
    v_target timestamptz;
    v_removed bigint := 0;
    v_remaining boolean;
BEGIN
    IF session_user <> 'leapview_control_maintenance' THEN
        RAISE EXCEPTION 'agent retention requires the maintenance capability';
    END IF;
    IF p_requested_cutoff IS NULL THEN
        RAISE EXCEPTION 'agent retention cutoff is required';
    END IF;
    IF p_batch_limit IS NULL OR p_batch_limit < 1 OR p_batch_limit > 1000 THEN
        RAISE EXCEPTION 'agent retention batch limit must be between 1 and 1000';
    END IF;
    SELECT f.floor_at INTO STRICT v_floor
      FROM agent.retention_floor f
     WHERE f.retention_class = 'run_events'
     FOR UPDATE;
    requested_cutoff := p_requested_cutoff;
    requested_limit := p_batch_limit;
    v_target := GREATEST(v_floor, LEAST(p_requested_cutoff, clock_timestamp()));
    cutoff := v_target;

    PERFORM set_config('agent.retention', 'on', true);
    WITH candidates AS (
        SELECT e.event_id
          FROM agent.events e
          JOIN agent.runs r ON r.id = e.run_id
          JOIN agent.conversations c ON c.id = r.conversation_id
         WHERE c.status = 'archived'
           AND c.archived_at IS NOT NULL
           AND c.archived_at <= v_target
           AND r.status IN ('completed', 'failed', 'canceled')
         ORDER BY c.archived_at, e.event_id
         FOR UPDATE OF e SKIP LOCKED
         LIMIT p_batch_limit
    ), deleted AS (
        DELETE FROM agent.events e
         USING candidates d
         WHERE e.event_id = d.event_id
         RETURNING e.event_id
    )
    SELECT count(*) INTO v_removed FROM deleted;

    SELECT EXISTS (
        SELECT 1
          FROM agent.events e
          JOIN agent.runs r ON r.id = e.run_id
          JOIN agent.conversations c ON c.id = r.conversation_id
         WHERE c.status = 'archived'
           AND c.archived_at IS NOT NULL
           AND c.archived_at <= v_target
           AND r.status IN ('completed', 'failed', 'canceled')
    ) INTO v_remaining;
    IF NOT v_remaining AND v_target > v_floor THEN
        UPDATE agent.retention_floor
           SET floor_at = v_target, updated_at = clock_timestamp()
         WHERE retention_class = 'run_events';
        v_floor := v_target;
    END IF;
    cutoff := v_target;
    removed_count := v_removed;
    retained_floor := v_floor;
    RETURN NEXT;
END;
$$;
-- +goose StatementEnd

-- Drain archived conversation children in a bounded order: messages, terminal
-- runs with no remaining children, then empty conversations. Foreign-key
-- cascades are therefore no-ops and active/nonterminal evidence is preserved.
CREATE OR REPLACE FUNCTION agent.prune_archived_conversations(
    p_requested_cutoff timestamptz,
    p_batch_limit integer
)
RETURNS TABLE (
    requested_cutoff timestamptz,
    cutoff timestamptz,
    requested_limit integer,
    conversations_removed bigint,
    messages_removed bigint,
    runs_removed bigint,
    retained_floor timestamptz
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, agent
-- +goose StatementBegin
AS $$
DECLARE
    v_floor timestamptz;
    v_target timestamptz;
    v_conversations bigint := 0;
    v_messages bigint := 0;
    v_runs bigint := 0;
    v_remaining_limit integer;
    v_remaining boolean;
BEGIN
    IF session_user <> 'leapview_control_maintenance' THEN
        RAISE EXCEPTION 'agent retention requires the maintenance capability';
    END IF;
    IF p_requested_cutoff IS NULL THEN
        RAISE EXCEPTION 'agent retention cutoff is required';
    END IF;
    IF p_batch_limit IS NULL OR p_batch_limit < 1 OR p_batch_limit > 1000 THEN
        RAISE EXCEPTION 'agent retention batch limit must be between 1 and 1000';
    END IF;
    SELECT f.floor_at INTO STRICT v_floor
      FROM agent.retention_floor f
     WHERE f.retention_class = 'conversations'
     FOR UPDATE;
    requested_cutoff := p_requested_cutoff;
    requested_limit := p_batch_limit;
    v_target := GREATEST(v_floor, LEAST(p_requested_cutoff, clock_timestamp()));
    cutoff := v_target;

    PERFORM set_config('agent.retention', 'on', true);
    v_remaining_limit := p_batch_limit;

    -- Drain messages first. They are archived evidence and have no child
    -- rows, so deleting them cannot cascade outside this bounded batch.
    WITH candidates AS (
        SELECT m.id
          FROM agent.messages m
          JOIN agent.conversations c ON c.id = m.conversation_id
         WHERE c.status = 'archived'
           AND c.archived_at IS NOT NULL
           AND c.archived_at <= v_target
           AND NOT EXISTS (
               SELECT 1
                 FROM agent.runs r
                WHERE r.conversation_id = c.id
                  AND r.status IN ('preparing', 'running')
           )
         ORDER BY c.archived_at, m.id
         FOR UPDATE OF m SKIP LOCKED
         LIMIT v_remaining_limit
    ), deleted AS (
        DELETE FROM agent.messages m USING candidates d
         WHERE m.id = d.id
         RETURNING m.id
    )
    SELECT count(*) INTO v_messages FROM deleted;
    v_remaining_limit := v_remaining_limit - v_messages::integer;

    -- Runs are removed only once all events and messages for that run are
    -- gone. This makes the FK cascade a no-op and keeps physical deletion
    -- bounded and observable.
    IF v_remaining_limit > 0 THEN
        WITH candidates AS (
            SELECT r.id
              FROM agent.runs r
              JOIN agent.conversations c ON c.id = r.conversation_id
             WHERE c.status = 'archived'
               AND c.archived_at IS NOT NULL
               AND c.archived_at <= v_target
               AND r.status IN ('completed', 'failed', 'canceled')
               AND NOT EXISTS (SELECT 1 FROM agent.events e WHERE e.run_id = r.id)
               AND NOT EXISTS (SELECT 1 FROM agent.messages m WHERE m.run_id = r.id)
             ORDER BY c.archived_at, r.id
             FOR UPDATE OF r SKIP LOCKED
             LIMIT v_remaining_limit
        ), deleted AS (
            DELETE FROM agent.runs r USING candidates d
             WHERE r.id = d.id
             RETURNING r.id
        )
        SELECT count(*) INTO v_runs FROM deleted;
        v_remaining_limit := v_remaining_limit - v_runs::integer;
    END IF;

    -- Finally remove only empty archived conversations. There are no child
    -- rows left, so ON DELETE CASCADE cannot erase uncounted physical rows.
    IF v_remaining_limit > 0 THEN
        WITH candidates AS (
            SELECT c.id
              FROM agent.conversations c
             WHERE c.status = 'archived'
               AND c.archived_at IS NOT NULL
               AND c.archived_at <= v_target
               AND NOT EXISTS (SELECT 1 FROM agent.runs r WHERE r.conversation_id = c.id)
               AND NOT EXISTS (SELECT 1 FROM agent.messages m WHERE m.conversation_id = c.id)
             ORDER BY c.archived_at, c.id
             FOR UPDATE OF c SKIP LOCKED
             LIMIT v_remaining_limit
        ), deleted AS (
            DELETE FROM agent.conversations c USING candidates d
             WHERE c.id = d.id
             RETURNING c.id
        )
        SELECT count(*) INTO v_conversations FROM deleted;
    END IF;

    SELECT EXISTS (
        SELECT 1
          FROM agent.conversations c
         WHERE c.status = 'archived'
           AND c.archived_at IS NOT NULL
           AND c.archived_at <= v_target
    ) INTO v_remaining;
    IF NOT v_remaining AND v_target > v_floor THEN
        UPDATE agent.retention_floor
           SET floor_at = v_target, updated_at = clock_timestamp()
         WHERE retention_class = 'conversations';
        v_floor := v_target;
    END IF;
    cutoff := v_target;
    conversations_removed := v_conversations;
    messages_removed := v_messages;
    runs_removed := v_runs;
    retained_floor := v_floor;
    RETURN NEXT;
END;
$$;
-- +goose StatementEnd

-- One maintenance facade executes both class-specific bounded leaves.  The
-- returned floors let operators prove each policy boundary independently.
CREATE OR REPLACE FUNCTION agent.prune_archived_agent_history(
    p_requested_cutoff timestamptz,
    p_batch_limit integer
)
RETURNS TABLE (
    requested_cutoff timestamptz,
    cutoff timestamptz,
    requested_limit integer,
    conversations_removed bigint,
    messages_removed bigint,
    runs_removed bigint,
    run_events_removed bigint,
    conversations_floor timestamptz,
    run_events_floor timestamptz
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, agent
-- +goose StatementBegin
AS $$
DECLARE
    v_conversation_removed bigint := 0;
    v_message_removed bigint := 0;
    v_run_removed bigint := 0;
    v_event_removed bigint := 0;
    v_conversation_floor timestamptz;
    v_event_floor timestamptz;
    v_remaining integer;
BEGIN
    IF session_user <> 'leapview_control_maintenance' THEN
        RAISE EXCEPTION 'agent retention requires the maintenance capability';
    END IF;
    IF p_requested_cutoff IS NULL OR p_batch_limit IS NULL OR p_batch_limit < 1 OR p_batch_limit > 1000 THEN
        RAISE EXCEPTION 'agent retention cutoff and batch limit are required (1..1000)';
    END IF;
    -- One global batch budget is shared across classes. Run events drain
    -- first; conversations consume only the remainder, so one invocation can
    -- never delete more than p_batch_limit candidate rows in total.
    SELECT p.removed_count, p.retained_floor
      INTO v_event_removed, v_event_floor
      FROM agent.prune_archived_run_events(p_requested_cutoff, p_batch_limit) p;
    v_remaining := p_batch_limit - v_event_removed::integer;
    IF v_remaining > 0 THEN
        SELECT p.conversations_removed, p.messages_removed, p.runs_removed, p.retained_floor
          INTO v_conversation_removed, v_message_removed, v_run_removed, v_conversation_floor
          FROM agent.prune_archived_conversations(p_requested_cutoff, v_remaining) p;
    ELSE
        SELECT f.floor_at INTO v_conversation_floor
          FROM agent.retention_floor f
         WHERE f.retention_class = 'conversations'
         FOR SHARE;
    END IF;
    requested_cutoff := p_requested_cutoff;
    cutoff := LEAST(p_requested_cutoff, clock_timestamp());
    requested_limit := p_batch_limit;
    conversations_removed := v_conversation_removed;
    messages_removed := v_message_removed;
    runs_removed := v_run_removed;
    run_events_removed := v_event_removed;
    conversations_floor := v_conversation_floor;
    run_events_floor := v_event_floor;
    RETURN NEXT;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON SCHEMA agent FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA agent FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA agent FROM PUBLIC;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA agent TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON agent.conversations, agent.runs TO leapview_control_runtime;
        GRANT SELECT, INSERT ON agent.messages, agent.events TO leapview_control_runtime;
        GRANT USAGE ON ALL SEQUENCES IN SCHEMA agent TO leapview_control_runtime;
        REVOKE DELETE ON agent.conversations, agent.runs, agent.messages, agent.events, agent.retention_floor FROM leapview_control_runtime;
        REVOKE EXECUTE ON FUNCTION agent.prune_archived_run_events(timestamptz, integer), agent.prune_archived_conversations(timestamptz, integer), agent.prune_archived_agent_history(timestamptz, integer) FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_owner') THEN
        GRANT ALL ON ALL TABLES IN SCHEMA agent TO leapview_control_owner;
        GRANT ALL ON ALL FUNCTIONS IN SCHEMA agent TO leapview_control_owner;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_migrator') THEN
        GRANT ALL ON ALL TABLES IN SCHEMA agent TO leapview_control_migrator;
        GRANT ALL ON ALL FUNCTIONS IN SCHEMA agent TO leapview_control_migrator;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA agent TO leapview_control_maintenance;
        GRANT EXECUTE ON FUNCTION agent.prune_archived_agent_history(timestamptz, integer) TO leapview_control_maintenance;
        REVOKE EXECUTE ON FUNCTION agent.prune_archived_run_events(timestamptz, integer), agent.prune_archived_conversations(timestamptz, integer) FROM leapview_control_maintenance;
        REVOKE ALL ON agent.conversations, agent.runs, agent.messages, agent.events, agent.retention_floor FROM leapview_control_maintenance;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA agent TO leapview_control_readonly;
        GRANT SELECT ON agent.conversations, agent.runs, agent.messages, agent.events, agent.retention_floor TO leapview_control_readonly;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON agent.conversations, agent.runs, agent.messages, agent.events, agent.retention_floor FROM leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA agent TO leapview_control_backup;
        GRANT SELECT ON agent.conversations, agent.runs, agent.messages, agent.events, agent.retention_floor TO leapview_control_backup;
    END IF;
END
$$;
-- +goose StatementEnd

-- capability source: internal/refresh/postgres/schema.sql
-- Clean-slate refresh authority (ADR-0014/0016).
--
-- This capability deliberately has no refresh queue.  The canonical
-- platform jobs schema owns worker admission and leases; refresh.run.job_id
-- links a run to that job.  No analytical rows, Arrow payloads or result
-- bytes are stored here.
CREATE SCHEMA IF NOT EXISTS refresh;
REVOKE ALL ON SCHEMA refresh FROM PUBLIC;

CREATE TABLE IF NOT EXISTS refresh.schedule_revision (
    schedule_revision_id text PRIMARY KEY,
    project_id text NOT NULL,
    environment text NOT NULL,
    pipeline_id text NOT NULL,
    schedule_id text NOT NULL,
    semantic_model_id text NOT NULL,
    generation_id text NOT NULL,
    artifact_digest text NOT NULL,
    cron text NOT NULL,
    timezone text NOT NULL,
    starting_deadline interval NOT NULL DEFAULT interval '0 seconds',
    concurrency_policy text NOT NULL,
    schedule_digest text NOT NULL,
    next_run_at timestamptz NOT NULL,
    valid_from timestamptz NOT NULL DEFAULT clock_timestamp(),
    closed_at timestamptz,
    enabled boolean NOT NULL DEFAULT true,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (schedule_revision_id = btrim(schedule_revision_id) AND length(schedule_revision_id) BETWEEN 1 AND 256),
    CHECK (project_id = btrim(project_id) AND length(project_id) BETWEEN 1 AND 255),
    CHECK (environment = btrim(environment) AND length(environment) BETWEEN 1 AND 128),
    CHECK (pipeline_id = btrim(pipeline_id) AND length(pipeline_id) BETWEEN 1 AND 255),
    CHECK (schedule_id = btrim(schedule_id) AND length(schedule_id) BETWEEN 1 AND 255),
    CHECK (semantic_model_id = btrim(semantic_model_id) AND length(semantic_model_id) BETWEEN 1 AND 255),
    CHECK (generation_id = btrim(generation_id) AND length(generation_id) BETWEEN 1 AND 255),
    CHECK (artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (schedule_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (cron = btrim(cron) AND length(cron) BETWEEN 1 AND 255),
    CHECK (timezone = btrim(timezone) AND length(timezone) BETWEEN 1 AND 128),
    CHECK (starting_deadline >= interval '0 seconds' AND starting_deadline <= interval '366 days'),
    CHECK (concurrency_policy IN ('Forbid','Replace')),
    CHECK (closed_at IS NULL OR closed_at >= valid_from)
);
CREATE UNIQUE INDEX IF NOT EXISTS schedule_active_key
    ON refresh.schedule_revision(project_id, environment, pipeline_id, generation_id, schedule_id)
    WHERE closed_at IS NULL AND enabled;
CREATE INDEX IF NOT EXISTS schedule_due_idx
    ON refresh.schedule_revision(next_run_at, project_id, environment, pipeline_id)
    WHERE closed_at IS NULL AND enabled;

CREATE OR REPLACE FUNCTION refresh.guard_schedule_insert() RETURNS trigger
-- +goose StatementBegin
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
BEGIN
    IF NEW.closed_at IS NOT NULL OR NOT NEW.enabled THEN RAISE EXCEPTION 'schedule revisions must begin active'; END IF;
    NEW.valid_from := clock_timestamp(); NEW.updated_at := NEW.valid_from;
    RETURN NEW;
END; $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS schedule_insert_guard ON refresh.schedule_revision;
CREATE TRIGGER schedule_insert_guard BEFORE INSERT ON refresh.schedule_revision FOR EACH ROW EXECUTE FUNCTION refresh.guard_schedule_insert();

CREATE OR REPLACE FUNCTION refresh.guard_schedule_update() RETURNS trigger
-- +goose StatementBegin
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
BEGIN
    IF NEW.schedule_revision_id IS DISTINCT FROM OLD.schedule_revision_id OR NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.environment IS DISTINCT FROM OLD.environment OR NEW.pipeline_id IS DISTINCT FROM OLD.pipeline_id OR NEW.schedule_id IS DISTINCT FROM OLD.schedule_id OR NEW.semantic_model_id IS DISTINCT FROM OLD.semantic_model_id OR NEW.generation_id IS DISTINCT FROM OLD.generation_id OR NEW.artifact_digest IS DISTINCT FROM OLD.artifact_digest OR NEW.cron IS DISTINCT FROM OLD.cron OR NEW.timezone IS DISTINCT FROM OLD.timezone OR NEW.starting_deadline IS DISTINCT FROM OLD.starting_deadline OR NEW.concurrency_policy IS DISTINCT FROM OLD.concurrency_policy OR NEW.schedule_digest IS DISTINCT FROM OLD.schedule_digest OR NEW.valid_from IS DISTINCT FROM OLD.valid_from THEN RAISE EXCEPTION 'schedule revision identity is immutable'; END IF;
    IF OLD.closed_at IS NOT NULL AND (NEW.closed_at IS DISTINCT FROM OLD.closed_at OR NEW.enabled IS DISTINCT FROM OLD.enabled) THEN RAISE EXCEPTION 'closed schedule revision is immutable'; END IF;
    IF OLD.closed_at IS NOT NULL AND NEW.next_run_at IS DISTINCT FROM OLD.next_run_at THEN RAISE EXCEPTION 'closed schedule revision is immutable'; END IF;
    IF NEW.closed_at IS NOT NULL AND OLD.closed_at IS NULL AND NEW.enabled THEN RAISE EXCEPTION 'closed schedule revision must be disabled'; END IF;
    IF NEW.closed_at IS NOT NULL AND OLD.closed_at IS NULL AND NEW.next_run_at IS DISTINCT FROM OLD.next_run_at THEN RAISE EXCEPTION 'schedule close cannot mutate next run time'; END IF;
    NEW.updated_at := clock_timestamp();
    RETURN NEW;
END; $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS schedule_guard ON refresh.schedule_revision;
CREATE TRIGGER schedule_guard BEFORE UPDATE ON refresh.schedule_revision FOR EACH ROW EXECUTE FUNCTION refresh.guard_schedule_update();

CREATE OR REPLACE FUNCTION refresh.close_omitted_schedules(
    p_project_id text,
    p_environment text,
    p_generation_id text,
    p_pipelines text[],
    p_schedule_ids text[]
) RETURNS bigint
LANGUAGE plpgsql
-- +goose StatementBegin
SET search_path = pg_catalog, refresh AS $$
DECLARE affected bigint;
BEGIN
    UPDATE refresh.schedule_revision s
       SET closed_at=clock_timestamp(), enabled=false, updated_at=clock_timestamp()
     WHERE s.project_id=p_project_id
       AND s.environment=p_environment
       AND s.generation_id=p_generation_id
       AND s.closed_at IS NULL
       AND s.enabled
       AND NOT EXISTS (
           SELECT 1
             FROM unnest(p_pipelines, p_schedule_ids) AS omitted(pipeline_id, schedule_id)
            WHERE omitted.pipeline_id=s.pipeline_id
              AND omitted.schedule_id=s.schedule_id
       );
    GET DIAGNOSTICS affected = ROW_COUNT;
    RETURN affected;
END; $$;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS refresh.run (
    run_id text PRIMARY KEY,
    -- Immutable provenance pointer into platform.operation. It is intentionally
    -- text and has no cross-schema FK so operation retention can prune terminal
    -- rows without deleting historical refresh evidence.
    operation_id text,
    project_id text NOT NULL,
    environment text NOT NULL,
    generation_id text NOT NULL,
    parent_run_id text REFERENCES refresh.run(run_id) ON DELETE RESTRICT,
    pipeline_id text NOT NULL,
    semantic_model_id text NOT NULL,
    target_type text NOT NULL,
    target_id text NOT NULL,
    target_revision bigint NOT NULL DEFAULT 0,
    trigger_type text NOT NULL,
    invocation_source text NOT NULL,
    trigger_id text NOT NULL DEFAULT '',
    concurrency_policy text NOT NULL DEFAULT 'Forbid',
    schedule_revision_id text NOT NULL DEFAULT '',
    occurrence_id text NOT NULL DEFAULT '',
    nominal_time timestamptz,
    plan_digest text NOT NULL,
    artifact_digest text NOT NULL,
    matching_schedule_ids jsonb NOT NULL DEFAULT '[]'::jsonb,
    materialization_scope jsonb NOT NULL DEFAULT '[]'::jsonb,
    principal_id text NOT NULL DEFAULT '',
    job_id text REFERENCES jobs.job_history(id) ON DELETE RESTRICT,
    status text NOT NULL DEFAULT 'queued',
    attempt_count bigint NOT NULL DEFAULT 0,
    fence_generation bigint NOT NULL DEFAULT 0,
    lease_owner text NOT NULL DEFAULT '',
    lease_expires_at timestamptz,
    error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    started_at timestamptz,
    finished_at timestamptz,
    CHECK (run_id = btrim(run_id) AND length(run_id) BETWEEN 1 AND 256),
    CHECK (operation_id IS NULL OR operation_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
    CHECK (project_id = btrim(project_id) AND length(project_id) BETWEEN 1 AND 255),
    CHECK (environment = btrim(environment) AND length(environment) BETWEEN 1 AND 128),
    CHECK (generation_id = btrim(generation_id) AND length(generation_id) BETWEEN 1 AND 255),
    CHECK (parent_run_id IS NULL OR (parent_run_id = btrim(parent_run_id) AND length(parent_run_id) BETWEEN 1 AND 256 AND parent_run_id <> run_id)),
    CHECK (pipeline_id = btrim(pipeline_id) AND length(pipeline_id) BETWEEN 1 AND 255),
    CHECK (semantic_model_id = btrim(semantic_model_id) AND length(semantic_model_id) BETWEEN 1 AND 255),
    CHECK (target_type IN ('refresh_pipeline','model')),
    CHECK (target_id = btrim(target_id) AND length(target_id) BETWEEN 1 AND 255),
    CHECK (target_revision >= 0),
    CHECK (trigger_type IN ('manual','schedule','dependency')),
    CHECK (invocation_source IN ('manual','schedule','external','backfill','dependency')),
    CHECK (trigger_id = btrim(trigger_id) AND length(trigger_id) <= 256),
    CHECK (concurrency_policy IN ('Forbid','Replace')),
    CHECK (schedule_revision_id = btrim(schedule_revision_id) AND length(schedule_revision_id) <= 256),
    CHECK (occurrence_id = btrim(occurrence_id) AND length(occurrence_id) <= 256),
    CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (jsonb_typeof(matching_schedule_ids) = 'array' AND octet_length(matching_schedule_ids::text) <= 16384),
    CHECK (jsonb_typeof(materialization_scope) = 'array' AND octet_length(materialization_scope::text) <= 16384),
    CHECK (status IN ('queued','running','prepared','succeeded','failed','cancelled','superseded','skipped')),
    CHECK (attempt_count >= 0 AND fence_generation >= 0),
    CHECK ((status IN ('running','prepared') AND lease_owner <> '' AND lease_expires_at IS NOT NULL) OR (status NOT IN ('running','prepared') AND lease_owner = '' AND lease_expires_at IS NULL)),
    CHECK ((status IN ('succeeded','failed','cancelled','superseded','skipped') AND finished_at IS NOT NULL) OR (status IN ('queued','running','prepared') AND finished_at IS NULL))
);
CREATE INDEX IF NOT EXISTS run_scope_idx ON refresh.run(project_id, environment, created_at DESC, run_id DESC);
CREATE INDEX IF NOT EXISTS run_target_idx ON refresh.run(project_id, environment, target_type, target_id, created_at DESC, run_id DESC);
CREATE INDEX IF NOT EXISTS run_recovery_idx ON refresh.run(environment, created_at, run_id) WHERE job_id IS NOT NULL AND status IN ('queued','running','prepared');

CREATE OR REPLACE FUNCTION refresh.guard_run_insert() RETURNS trigger
-- +goose StatementBegin
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
DECLARE parent_project text; parent_environment text; parent_generation text; parent_parent text;
BEGIN
    IF NEW.status <> 'queued' OR NEW.attempt_count <> 0 OR NEW.fence_generation <> 0 OR NEW.lease_owner <> '' OR NEW.lease_expires_at IS NOT NULL OR NEW.started_at IS NOT NULL OR NEW.finished_at IS NOT NULL THEN RAISE EXCEPTION 'run inserts must begin as empty queued records'; END IF;
    IF NEW.parent_run_id IS NOT NULL THEN
        IF NEW.parent_run_id = NEW.run_id THEN RAISE EXCEPTION 'run cannot parent itself'; END IF;
        SELECT project_id,environment,generation_id,parent_run_id INTO parent_project,parent_environment,parent_generation,parent_parent FROM refresh.run WHERE run_id=NEW.parent_run_id;
        IF parent_project IS NULL OR parent_project IS DISTINCT FROM NEW.project_id OR parent_environment IS DISTINCT FROM NEW.environment OR parent_generation IS DISTINCT FROM NEW.generation_id OR parent_parent IS NOT NULL THEN
            RAISE EXCEPTION 'run parent must be an existing root in the same serving scope';
        END IF;
    END IF;
    NEW.created_at := clock_timestamp(); NEW.updated_at := NEW.created_at;
    RETURN NEW;
END; $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS run_insert_guard ON refresh.run;
CREATE TRIGGER run_insert_guard BEFORE INSERT ON refresh.run FOR EACH ROW EXECUTE FUNCTION refresh.guard_run_insert();

-- Every committed root run must have one canonical platform job.  Dependency
-- children intentionally remain jobless: the root job owns their tree.  A
-- deferred constraint trigger permits the atomic insert-then-attach sequence
-- used by the jobs adapter while rejecting standalone/root rows that would be
-- invisible to queue recovery.
CREATE OR REPLACE FUNCTION refresh.guard_root_job_attachment() RETURNS trigger
-- +goose StatementBegin
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
DECLARE current_parent text; current_job text; job_kind text; job_workload text; job_resource_kind text; job_resource_id text; job_partition text; job_principal text; job_status text;
BEGIN
    SELECT parent_run_id, job_id INTO current_parent, current_job
      FROM refresh.run WHERE run_id = NEW.run_id;
    IF current_parent IS NULL AND current_job IS NULL THEN
        RAISE EXCEPTION 'root refresh run requires canonical platform job';
    END IF;
    IF current_parent IS NULL THEN
        SELECT kind,workload_class,resource_kind,resource_id,partition_key,principal_id,status
          INTO job_kind,job_workload,job_resource_kind,job_resource_id,job_partition,job_principal,job_status
          FROM jobs.job_history WHERE id=current_job;
        IF job_kind IS DISTINCT FROM 'refresh_pipeline' OR job_workload IS DISTINCT FROM 'background' OR job_resource_kind IS DISTINCT FROM 'refresh_run' OR job_resource_id IS DISTINCT FROM NEW.run_id OR job_partition IS DISTINCT FROM ('refresh:'||NEW.project_id||':'||NEW.environment) OR job_principal IS DISTINCT FROM NEW.principal_id OR job_status IS DISTINCT FROM 'queued' THEN
            RAISE EXCEPTION 'root refresh job does not match canonical queue identity';
        END IF;
    END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS run_root_job_guard ON refresh.run;
CREATE CONSTRAINT TRIGGER run_root_job_guard
    AFTER INSERT OR UPDATE OF parent_run_id,job_id ON refresh.run
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION refresh.guard_root_job_attachment();

CREATE TABLE IF NOT EXISTS refresh.schedule_occurrence (
    occurrence_id text PRIMARY KEY,
    project_id text NOT NULL,
    environment text NOT NULL,
    pipeline_id text NOT NULL,
    nominal_time timestamptz NOT NULL,
    schedule_revision_id text NOT NULL REFERENCES refresh.schedule_revision(schedule_revision_id),
    matching_schedule_ids jsonb NOT NULL,
    semantic_model_id text NOT NULL,
    generation_id text NOT NULL,
    artifact_digest text NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    run_id text REFERENCES refresh.run(run_id),
    fence_generation bigint NOT NULL DEFAULT 0,
    lease_owner text NOT NULL DEFAULT '',
    lease_expires_at timestamptz,
    claimed_at timestamptz,
    finished_at timestamptz,
    outcome jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (occurrence_id = btrim(occurrence_id) AND length(occurrence_id) BETWEEN 1 AND 256),
    CHECK (project_id = btrim(project_id) AND length(project_id) BETWEEN 1 AND 255),
    CHECK (environment = btrim(environment) AND length(environment) BETWEEN 1 AND 128),
    CHECK (pipeline_id = btrim(pipeline_id) AND length(pipeline_id) BETWEEN 1 AND 255),
    CHECK (jsonb_typeof(matching_schedule_ids) = 'array' AND octet_length(matching_schedule_ids::text) <= 16384),
    CHECK (artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (status IN ('pending','claimed','queued','running','succeeded','failed','cancelled','skipped','superseded')),
    CHECK (fence_generation >= 0),
    CHECK ((status = 'claimed' AND lease_owner <> '' AND lease_expires_at IS NOT NULL) OR (status <> 'claimed' AND lease_owner = '' AND lease_expires_at IS NULL)),
    CHECK (jsonb_typeof(outcome) = 'object' AND octet_length(outcome::text) <= 32768),
    UNIQUE (project_id, environment, pipeline_id, nominal_time)
);
CREATE INDEX IF NOT EXISTS occurrence_due_idx ON refresh.schedule_occurrence(project_id, environment, status, nominal_time);

CREATE OR REPLACE FUNCTION refresh.guard_occurrence_insert() RETURNS trigger
-- +goose StatementBegin
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
BEGIN
    IF NEW.status <> 'pending' OR NEW.fence_generation <> 0 OR NEW.lease_owner <> '' OR NEW.lease_expires_at IS NOT NULL OR NEW.finished_at IS NOT NULL THEN RAISE EXCEPTION 'occurrence inserts must begin pending and unfenced'; END IF;
    NEW.created_at := clock_timestamp();
    RETURN NEW;
END; $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS occurrence_insert_guard ON refresh.schedule_occurrence;
CREATE TRIGGER occurrence_insert_guard BEFORE INSERT ON refresh.schedule_occurrence FOR EACH ROW EXECUTE FUNCTION refresh.guard_occurrence_insert();

CREATE TABLE IF NOT EXISTS refresh.attempt (
    run_id text NOT NULL REFERENCES refresh.run(run_id),
    attempt_number bigint NOT NULL,
    fence_generation bigint NOT NULL,
    owner_id text NOT NULL,
    lease_expires_at timestamptz NOT NULL,
    status text NOT NULL DEFAULT 'running',
    evidence jsonb NOT NULL DEFAULT '{}'::jsonb,
    error text NOT NULL DEFAULT '',
    claimed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at timestamptz,
    PRIMARY KEY (run_id, attempt_number),
    UNIQUE (run_id, fence_generation),
    CHECK (attempt_number > 0 AND fence_generation > 0),
    CHECK (owner_id = btrim(owner_id) AND length(owner_id) BETWEEN 1 AND 256),
    CHECK (status IN ('running','succeeded','failed','cancelled','expired','indeterminate')),
    CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 65536),
    CHECK ((status = 'running') = (finished_at IS NULL))
);
CREATE INDEX IF NOT EXISTS attempt_lease_idx ON refresh.attempt(status, lease_expires_at);

CREATE OR REPLACE FUNCTION refresh.guard_attempt_insert() RETURNS trigger
-- +goose StatementBegin
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
DECLARE run_owner text; run_fence bigint; run_status text; run_expiry timestamptz;
BEGIN
    IF NEW.status <> 'running' OR NEW.finished_at IS NOT NULL OR NEW.fence_generation <= 0
       OR NEW.owner_id = '' OR NEW.lease_expires_at <= clock_timestamp()
       OR NEW.lease_expires_at > clock_timestamp() + interval '24 hours'
       OR NEW.evidence IS DISTINCT FROM '{}'::jsonb THEN
        RAISE EXCEPTION 'attempt inserts must begin as live empty evidence';
    END IF;
    SELECT lease_owner,fence_generation,status,lease_expires_at
      INTO run_owner,run_fence,run_status,run_expiry
      FROM refresh.run WHERE run_id=NEW.run_id;
    IF run_owner IS DISTINCT FROM NEW.owner_id OR run_fence IS DISTINCT FROM NEW.fence_generation
       OR run_status <> 'running' OR run_expiry IS NULL OR run_expiry <= clock_timestamp() THEN
        RAISE EXCEPTION 'attempt insert is not fenced by current live run';
    END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS attempt_insert_guard ON refresh.attempt;
CREATE TRIGGER attempt_insert_guard BEFORE INSERT ON refresh.attempt FOR EACH ROW EXECUTE FUNCTION refresh.guard_attempt_insert();

-- Tree terminalization is kept in capability-owned functions so the recursive
-- transition remains one atomic PostgreSQL statement while sqlc exposes a
-- typed scalar result (the exact number of rows changed).
CREATE OR REPLACE FUNCTION refresh.fail_child_runs(p_run_id text, p_error text)
RETURNS bigint
LANGUAGE plpgsql
-- +goose StatementBegin
SET search_path = pg_catalog, refresh AS $$
DECLARE affected bigint;
BEGIN
    WITH RECURSIVE tree(run_id) AS (
        SELECT r.run_id FROM refresh.run r WHERE r.run_id = p_run_id
        UNION ALL
        SELECT child.run_id FROM refresh.run child JOIN tree parent ON child.parent_run_id = parent.run_id
    )
    UPDATE refresh.run
       SET status='failed', error=p_error, finished_at=clock_timestamp(), lease_owner='', lease_expires_at=NULL
     WHERE run_id IN (SELECT tree.run_id FROM tree WHERE tree.run_id <> p_run_id)
       AND status IN ('queued','running','prepared');
    GET DIAGNOSTICS affected = ROW_COUNT;
    RETURN affected;
END; $$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION refresh.complete_child_runs(p_run_id text)
RETURNS bigint
LANGUAGE plpgsql
-- +goose StatementBegin
SET search_path = pg_catalog, refresh AS $$
DECLARE affected bigint;
BEGIN
    WITH RECURSIVE tree(run_id) AS (
        SELECT r.run_id FROM refresh.run r WHERE r.run_id = p_run_id
        UNION ALL
        SELECT child.run_id FROM refresh.run child JOIN tree parent ON child.parent_run_id = parent.run_id
    )
    UPDATE refresh.run
       SET status='succeeded', finished_at=clock_timestamp(), lease_owner='', lease_expires_at=NULL
     WHERE run_id IN (SELECT tree.run_id FROM tree WHERE tree.run_id <> p_run_id)
       AND status IN ('queued','running','prepared');
    GET DIAGNOSTICS affected = ROW_COUNT;
    RETURN affected;
END; $$;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS refresh.publication_link (
    publication_id text PRIMARY KEY,
    run_id text NOT NULL REFERENCES refresh.run(run_id),
    base_generation_id text NOT NULL,
    result_generation_id text NOT NULL,
    plan_digest text NOT NULL,
    artifact_digest text NOT NULL,
    physical_pool_id text NOT NULL,
    catalog_id text NOT NULL,
    expected_target_revision bigint NOT NULL CHECK (expected_target_revision > 0),
    result_target_revision bigint NOT NULL CHECK (result_target_revision > expected_target_revision),
    snapshot_id bigint,
    state text NOT NULL DEFAULT 'pending',
    fence_generation bigint NOT NULL,
    owner_id text NOT NULL,
    evidence jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    committed_at timestamptz,
    CHECK (publication_id = btrim(publication_id) AND length(publication_id) BETWEEN 1 AND 256),
    CHECK (base_generation_id = btrim(base_generation_id) AND length(base_generation_id) BETWEEN 1 AND 255),
    CHECK (result_generation_id = btrim(result_generation_id) AND length(result_generation_id) BETWEEN 1 AND 255),
    CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (physical_pool_id = btrim(physical_pool_id) AND length(physical_pool_id) BETWEEN 1 AND 255),
    CHECK (catalog_id = btrim(catalog_id) AND length(catalog_id) BETWEEN 1 AND 255),
    CHECK (snapshot_id IS NULL OR snapshot_id > 0),
    CHECK (state IN ('pending','committed','failed','fenced')),
    CHECK (fence_generation > 0),
    CHECK (owner_id = btrim(owner_id) AND length(owner_id) BETWEEN 1 AND 256),
    CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 65536),
    CHECK ((state = 'committed') = (committed_at IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS publication_run_idx ON refresh.publication_link(run_id) WHERE state IN ('pending','committed');

CREATE OR REPLACE FUNCTION refresh.guard_publication_insert() RETURNS trigger
-- +goose StatementBegin
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
DECLARE run_generation text; run_plan text; run_artifact text; run_owner text; run_fence bigint; run_status text; run_expiry timestamptz;
BEGIN
    IF NEW.state <> 'pending' OR NEW.snapshot_id IS NOT NULL OR NEW.committed_at IS NOT NULL
       OR NEW.evidence = '{}'::jsonb THEN
        RAISE EXCEPTION 'publication links must begin pending without commit evidence';
    END IF;
    SELECT generation_id,plan_digest,artifact_digest,lease_owner,fence_generation,status,lease_expires_at
      INTO run_generation,run_plan,run_artifact,run_owner,run_fence,run_status,run_expiry
      FROM refresh.run WHERE run_id=NEW.run_id;
    IF run_generation IS DISTINCT FROM NEW.base_generation_id OR NEW.result_generation_id = '' OR run_plan IS DISTINCT FROM NEW.plan_digest
       OR run_artifact IS DISTINCT FROM NEW.artifact_digest OR run_owner IS DISTINCT FROM NEW.owner_id
       OR run_fence IS DISTINCT FROM NEW.fence_generation OR run_status NOT IN ('running','prepared')
       OR run_expiry IS NULL OR run_expiry <= clock_timestamp() THEN
        RAISE EXCEPTION 'publication link is not fenced by current live run';
    END IF;
    NEW.created_at := clock_timestamp();
    RETURN NEW;
END; $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS publication_insert_guard ON refresh.publication_link;
CREATE TRIGGER publication_insert_guard BEFORE INSERT ON refresh.publication_link FOR EACH ROW EXECUTE FUNCTION refresh.guard_publication_insert();

CREATE TABLE IF NOT EXISTS refresh.recovery_state (
    run_id text PRIMARY KEY REFERENCES refresh.run(run_id),
    state text NOT NULL DEFAULT 'unreconciled',
    reconciliation_fence bigint NOT NULL DEFAULT 0,
    owner_id text NOT NULL DEFAULT '',
    lease_expires_at timestamptz,
    exact_external_identity text NOT NULL DEFAULT '',
    evidence jsonb NOT NULL DEFAULT '{}'::jsonb,
    last_error text NOT NULL DEFAULT '',
    next_reconcile_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (state IN ('unreconciled','pending','reconciled','indeterminate','quarantined')),
    CHECK (reconciliation_fence >= 0),
    CHECK ((reconciliation_fence > 0 AND owner_id <> '' AND lease_expires_at IS NOT NULL) OR (reconciliation_fence = 0 AND owner_id = '' AND lease_expires_at IS NULL)),
    CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 65536)
);

CREATE OR REPLACE FUNCTION refresh.guard_recovery_insert() RETURNS trigger
-- +goose StatementBegin
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
DECLARE run_status text;
BEGIN
    IF NEW.state NOT IN ('unreconciled','pending','reconciled','indeterminate','quarantined')
       OR NEW.reconciliation_fence <> 1
       OR NEW.lease_expires_at IS NULL OR NEW.lease_expires_at <= clock_timestamp()
       OR NEW.lease_expires_at > clock_timestamp() + interval '24 hours'
       OR NEW.owner_id <> btrim(NEW.owner_id) OR length(NEW.owner_id) > 256
       OR NEW.exact_external_identity <> btrim(NEW.exact_external_identity) OR length(NEW.exact_external_identity) > 256
       OR length(NEW.last_error) > 4096
       OR (NEW.reconciliation_fence > 0 AND NEW.evidence = '{}'::jsonb)
       OR (NEW.state IN ('reconciled','indeterminate') AND NEW.exact_external_identity = '')
       OR NEW.evidence IS DISTINCT FROM '{}'::jsonb AND jsonb_typeof(NEW.evidence) <> 'object' THEN
        RAISE EXCEPTION 'recovery inserts must begin with canonical state, fence and evidence';
    END IF;
    SELECT status INTO run_status FROM refresh.run WHERE run_id=NEW.run_id;
    IF run_status NOT IN ('failed','indeterminate') THEN RAISE EXCEPTION 'recovery requires failed or indeterminate run'; END IF;
    NEW.updated_at := clock_timestamp();
    RETURN NEW;
END; $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS recovery_insert_guard ON refresh.recovery_state;
CREATE TRIGGER recovery_insert_guard BEFORE INSERT ON refresh.recovery_state FOR EACH ROW EXECUTE FUNCTION refresh.guard_recovery_insert();

-- A compact serving watermark, not analytical data.  Snapshot identity points
-- into the DuckLake authority and is never a container for result bytes.
CREATE TABLE IF NOT EXISTS refresh.data_version (
    project_id text NOT NULL,
    environment text NOT NULL,
    semantic_model_id text NOT NULL,
    generation_id text NOT NULL,
    snapshot_id bigint NOT NULL CHECK (snapshot_id > 0),
    refreshed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    source text NOT NULL CHECK (source IN ('publish','refresh')),
    physical_pool_id text NOT NULL,
    catalog_id text NOT NULL,
    pipeline_id text NOT NULL DEFAULT '',
    run_id text NOT NULL DEFAULT '',
    target_revision bigint NOT NULL DEFAULT 0 CHECK (target_revision >= 0),
    lease_owner text NOT NULL DEFAULT '',
    lease_revision bigint NOT NULL DEFAULT 0 CHECK (lease_revision >= 0),
    PRIMARY KEY (project_id, environment, semantic_model_id, generation_id),
    CHECK (project_id = btrim(project_id) AND length(project_id) BETWEEN 1 AND 255),
    CHECK (environment = btrim(environment) AND length(environment) BETWEEN 1 AND 128),
    CHECK (semantic_model_id = btrim(semantic_model_id) AND length(semantic_model_id) BETWEEN 1 AND 255),
    CHECK (generation_id = btrim(generation_id) AND length(generation_id) BETWEEN 1 AND 255),
    CHECK (physical_pool_id = btrim(physical_pool_id) AND length(physical_pool_id) BETWEEN 1 AND 255),
    CHECK (catalog_id = btrim(catalog_id) AND length(catalog_id) BETWEEN 1 AND 255),
    CHECK (pipeline_id = btrim(pipeline_id) AND length(pipeline_id) <= 255),
    CHECK (run_id = btrim(run_id) AND length(run_id) <= 256),
    CHECK ((lease_owner = '') = (lease_revision = 0))
);

CREATE OR REPLACE FUNCTION refresh.guard_data_version_insert() RETURNS trigger
-- +goose StatementBegin
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
DECLARE pub_generation text; pub_pool text; pub_catalog text; pub_snapshot bigint; pub_run text; pub_project text; pub_environment text; pub_model text;
BEGIN
    IF NEW.refreshed_at IS NULL OR NEW.snapshot_id <= 0 OR NEW.source NOT IN ('publish','refresh')
       OR (NEW.lease_revision = 0 AND NEW.lease_owner <> '')
       OR (NEW.lease_revision > 0 AND NEW.lease_owner = '') THEN
        RAISE EXCEPTION 'data version insert is not canonical';
    END IF;
    SELECT p.result_generation_id,p.physical_pool_id,p.catalog_id,p.snapshot_id,p.run_id,r.project_id,r.environment,r.semantic_model_id
      INTO pub_generation,pub_pool,pub_catalog,pub_snapshot,pub_run,pub_project,pub_environment,pub_model
      FROM refresh.publication_link p JOIN refresh.run r ON r.run_id=p.run_id
     WHERE p.run_id=NEW.run_id AND p.state='committed' AND p.result_generation_id=NEW.generation_id
       AND p.physical_pool_id=NEW.physical_pool_id AND p.catalog_id=NEW.catalog_id
       AND p.snapshot_id=NEW.snapshot_id AND (NEW.source='publish' OR (p.fence_generation=NEW.lease_revision AND p.owner_id=NEW.lease_owner));
    IF pub_run IS NULL OR pub_project IS DISTINCT FROM NEW.project_id OR pub_environment IS DISTINCT FROM NEW.environment
       OR pub_model IS DISTINCT FROM NEW.semantic_model_id OR pub_generation IS DISTINCT FROM NEW.generation_id THEN
        RAISE EXCEPTION 'data version must reference exact committed publication';
    END IF;
    IF NEW.source='publish' AND NEW.lease_revision <> 0 THEN
        RAISE EXCEPTION 'published data versions cannot carry a refresh lease';
    END IF;
    IF NEW.source='refresh' AND NEW.lease_revision > 0 THEN
        IF NOT EXISTS (SELECT 1 FROM refresh.run r JOIN refresh.publication_link p ON p.run_id=r.run_id WHERE r.run_id=NEW.run_id AND r.project_id=NEW.project_id AND r.environment=NEW.environment AND p.base_generation_id=r.generation_id AND p.result_generation_id=NEW.generation_id AND p.state='committed' AND p.fence_generation=NEW.lease_revision AND p.owner_id=NEW.lease_owner AND r.status IN ('running','prepared','succeeded')) THEN
            RAISE EXCEPTION 'refresh data version lease is not tied to current run';
        END IF;
    END IF;
    NEW.refreshed_at := clock_timestamp();
    RETURN NEW;
END; $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS data_version_insert_guard ON refresh.data_version;
CREATE TRIGGER data_version_insert_guard BEFORE INSERT ON refresh.data_version FOR EACH ROW EXECUTE FUNCTION refresh.guard_data_version_insert();

CREATE OR REPLACE FUNCTION refresh.guard_data_version_update() RETURNS trigger
-- +goose StatementBegin
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
BEGIN
    IF NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.environment IS DISTINCT FROM OLD.environment OR NEW.semantic_model_id IS DISTINCT FROM OLD.semantic_model_id OR NEW.generation_id IS DISTINCT FROM OLD.generation_id THEN RAISE EXCEPTION 'data version identity is immutable'; END IF;
    IF NEW.lease_revision < OLD.lease_revision THEN RAISE EXCEPTION 'data version fence cannot decrease'; END IF;
    IF NEW.lease_revision = OLD.lease_revision AND (NEW.snapshot_id IS DISTINCT FROM OLD.snapshot_id OR NEW.source IS DISTINCT FROM OLD.source OR NEW.physical_pool_id IS DISTINCT FROM OLD.physical_pool_id OR NEW.catalog_id IS DISTINCT FROM OLD.catalog_id OR NEW.pipeline_id IS DISTINCT FROM OLD.pipeline_id OR NEW.run_id IS DISTINCT FROM OLD.run_id OR NEW.target_revision IS DISTINCT FROM OLD.target_revision OR NEW.lease_owner IS DISTINCT FROM OLD.lease_owner) THEN RAISE EXCEPTION 'equal-fence data version replay conflicts'; END IF;
    IF NEW.lease_revision > OLD.lease_revision THEN
		IF NEW.source='publish' AND NEW.lease_revision <> 0 THEN RAISE EXCEPTION 'published data versions cannot carry a refresh lease'; END IF;
        IF NOT EXISTS (SELECT 1 FROM refresh.publication_link p JOIN refresh.run r ON r.run_id=p.run_id WHERE p.run_id=NEW.run_id AND p.state='committed' AND p.result_generation_id=NEW.generation_id AND p.physical_pool_id=NEW.physical_pool_id AND p.catalog_id=NEW.catalog_id AND p.snapshot_id=NEW.snapshot_id AND p.fence_generation=NEW.lease_revision AND r.project_id=NEW.project_id AND r.environment=NEW.environment AND r.semantic_model_id=NEW.semantic_model_id) THEN
            RAISE EXCEPTION 'higher-fence data version must reference exact committed publication';
        END IF;
        IF NEW.source='refresh' AND NOT EXISTS (SELECT 1 FROM refresh.run r JOIN refresh.publication_link p ON p.run_id=r.run_id WHERE r.run_id=NEW.run_id AND r.project_id=NEW.project_id AND r.environment=NEW.environment AND p.base_generation_id=r.generation_id AND p.result_generation_id=NEW.generation_id AND p.state='committed' AND p.fence_generation=NEW.lease_revision AND p.owner_id=NEW.lease_owner AND r.status IN ('running','prepared','succeeded')) THEN
            RAISE EXCEPTION 'higher-fence data version lease is not tied to current run';
        END IF;
    END IF;
    NEW.refreshed_at := clock_timestamp();
    RETURN NEW;
END; $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS data_version_guard ON refresh.data_version;
CREATE TRIGGER data_version_guard BEFORE UPDATE ON refresh.data_version FOR EACH ROW EXECUTE FUNCTION refresh.guard_data_version_update();

-- Database-owned timestamps and monotonic fencing are defence in depth for
-- roles that accidentally receive a wider UPDATE privilege.
CREATE OR REPLACE FUNCTION refresh.guard_updated_at() RETURNS trigger
-- +goose StatementBegin
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
DECLARE recovery_run_status text;
BEGIN
    NEW.updated_at := clock_timestamp();
    IF TG_TABLE_NAME = 'run' THEN
        IF NEW.run_id IS DISTINCT FROM OLD.run_id OR NEW.operation_id IS DISTINCT FROM OLD.operation_id OR NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.environment IS DISTINCT FROM OLD.environment OR NEW.generation_id IS DISTINCT FROM OLD.generation_id OR NEW.parent_run_id IS DISTINCT FROM OLD.parent_run_id OR NEW.pipeline_id IS DISTINCT FROM OLD.pipeline_id OR NEW.semantic_model_id IS DISTINCT FROM OLD.semantic_model_id OR NEW.target_type IS DISTINCT FROM OLD.target_type OR NEW.target_id IS DISTINCT FROM OLD.target_id OR NEW.target_revision IS DISTINCT FROM OLD.target_revision OR NEW.trigger_type IS DISTINCT FROM OLD.trigger_type OR NEW.invocation_source IS DISTINCT FROM OLD.invocation_source OR NEW.trigger_id IS DISTINCT FROM OLD.trigger_id OR NEW.concurrency_policy IS DISTINCT FROM OLD.concurrency_policy OR NEW.schedule_revision_id IS DISTINCT FROM OLD.schedule_revision_id OR NEW.occurrence_id IS DISTINCT FROM OLD.occurrence_id OR NEW.nominal_time IS DISTINCT FROM OLD.nominal_time OR NEW.plan_digest IS DISTINCT FROM OLD.plan_digest OR NEW.artifact_digest IS DISTINCT FROM OLD.artifact_digest OR NEW.matching_schedule_ids IS DISTINCT FROM OLD.matching_schedule_ids OR NEW.materialization_scope IS DISTINCT FROM OLD.materialization_scope OR NEW.principal_id IS DISTINCT FROM OLD.principal_id OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN RAISE EXCEPTION 'run identity is immutable'; END IF;
        IF NEW.job_id IS DISTINCT FROM OLD.job_id AND (OLD.job_id <> '' OR NEW.job_id = '') THEN RAISE EXCEPTION 'run job attachment is immutable after first bind'; END IF;
        IF OLD.status IN ('succeeded','failed','cancelled','superseded','skipped') AND NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION 'terminal run is immutable'; END IF;
        IF OLD.status='queued' AND NEW.status NOT IN ('queued','running','succeeded','cancelled','failed','superseded','skipped') THEN RAISE EXCEPTION 'illegal queued run transition'; END IF;
		IF OLD.status='running' AND NEW.status NOT IN ('running','prepared','succeeded','failed','cancelled','superseded') THEN RAISE EXCEPTION 'illegal running run transition'; END IF;
		IF OLD.status='prepared' AND NEW.status NOT IN ('running','prepared','succeeded','failed','cancelled','superseded') THEN RAISE EXCEPTION 'illegal prepared run transition'; END IF;
		IF NEW.status='running' THEN
			IF NEW.lease_owner = '' OR NEW.lease_expires_at IS NULL OR NEW.lease_expires_at <= clock_timestamp() OR NEW.lease_expires_at > clock_timestamp() + interval '24 hours' THEN RAISE EXCEPTION 'running run requires a live bounded lease'; END IF;
			IF OLD.status='queued' AND (NEW.fence_generation <= OLD.fence_generation OR NEW.attempt_count <> OLD.attempt_count + 1 OR NEW.started_at IS NULL) THEN RAISE EXCEPTION 'queued run claim must advance fence and attempt'; END IF;
			IF OLD.status='running' AND NEW.fence_generation > OLD.fence_generation AND (OLD.lease_expires_at IS NULL OR OLD.lease_expires_at > clock_timestamp() OR NEW.attempt_count <> OLD.attempt_count + 1) THEN RAISE EXCEPTION 'run takeover requires expired lease and next attempt'; END IF;
			IF OLD.status='prepared' AND (NEW.fence_generation <> OLD.fence_generation + 1 OR NEW.attempt_count <> OLD.attempt_count + 1 OR OLD.lease_expires_at IS NULL OR OLD.lease_expires_at > clock_timestamp()) THEN RAISE EXCEPTION 'prepared run takeover requires expired lease and next attempt'; END IF;
			IF OLD.status='running' AND NEW.fence_generation = OLD.fence_generation AND NEW.lease_owner IS DISTINCT FROM OLD.lease_owner THEN RAISE EXCEPTION 'run owner change requires a new fence'; END IF;
		END IF;
		IF NEW.status='prepared' AND (NEW.lease_owner = '' OR NEW.lease_expires_at IS NULL OR NEW.lease_expires_at <= clock_timestamp() OR NEW.lease_expires_at > clock_timestamp() + interval '24 hours') THEN RAISE EXCEPTION 'prepared run requires a live bounded lease'; END IF;
		IF OLD.status IN ('running','prepared') AND NEW.status='prepared' AND NEW.fence_generation > OLD.fence_generation AND (OLD.lease_expires_at IS NULL OR OLD.lease_expires_at > clock_timestamp() OR NEW.attempt_count <> OLD.attempt_count + 1) THEN RAISE EXCEPTION 'run takeover requires expired lease and next attempt'; END IF;
		IF OLD.status IN ('running','prepared') AND NEW.status='prepared' AND NEW.fence_generation = OLD.fence_generation AND NEW.lease_owner IS DISTINCT FROM OLD.lease_owner THEN RAISE EXCEPTION 'run owner change requires a new fence'; END IF;
		IF OLD.status='running' AND NEW.status IN ('succeeded','failed','cancelled','superseded') AND (NEW.finished_at IS NULL OR NEW.lease_owner <> '' OR NEW.lease_expires_at IS NOT NULL) THEN RAISE EXCEPTION 'terminal run requires closed lease and finish time'; END IF;
		IF OLD.status='queued' AND NEW.status IN ('succeeded','cancelled','failed','superseded','skipped') AND (NEW.finished_at IS NULL OR NEW.lease_owner <> '' OR NEW.lease_expires_at IS NOT NULL) THEN RAISE EXCEPTION 'queued terminal run requires finish time'; END IF;
		IF OLD.status='prepared' AND NEW.status IN ('succeeded','failed','cancelled','superseded') AND (NEW.finished_at IS NULL OR NEW.lease_owner <> '' OR NEW.lease_expires_at IS NOT NULL) THEN RAISE EXCEPTION 'terminal prepared run requires closed lease and finish time'; END IF;
		IF NEW.status='superseded' AND NEW.trigger_type <> 'schedule' THEN RAISE EXCEPTION 'only scheduled runs may be superseded by overlap replacement'; END IF;
	ELSIF TG_TABLE_NAME = 'recovery_state' THEN
		SELECT status INTO recovery_run_status FROM refresh.run WHERE run_id=NEW.run_id;
		IF recovery_run_status NOT IN ('failed','indeterminate') THEN RAISE EXCEPTION 'recovery requires failed or indeterminate run'; END IF;
		IF NEW.run_id IS DISTINCT FROM OLD.run_id THEN RAISE EXCEPTION 'recovery run identity is immutable'; END IF;
		IF NEW.reconciliation_fence = OLD.reconciliation_fence AND (NEW.state IS DISTINCT FROM OLD.state OR NEW.owner_id IS DISTINCT FROM OLD.owner_id OR NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at OR NEW.exact_external_identity IS DISTINCT FROM OLD.exact_external_identity OR NEW.last_error IS DISTINCT FROM OLD.last_error OR NEW.evidence IS DISTINCT FROM OLD.evidence OR NEW.next_reconcile_at IS DISTINCT FROM OLD.next_reconcile_at) THEN RAISE EXCEPTION 'equal-fence recovery replay conflicts'; END IF;
		IF NEW.reconciliation_fence > OLD.reconciliation_fence AND (NEW.reconciliation_fence <> OLD.reconciliation_fence + 1 OR NEW.owner_id = '' OR NEW.lease_expires_at IS NULL OR NEW.lease_expires_at <= clock_timestamp() OR NEW.lease_expires_at > clock_timestamp() + interval '24 hours') THEN RAISE EXCEPTION 'recovery fence must advance one live lease at a time'; END IF;
		IF NEW.reconciliation_fence > OLD.reconciliation_fence AND (OLD.lease_expires_at IS NULL OR OLD.lease_expires_at > clock_timestamp()) THEN RAISE EXCEPTION 'recovery takeover requires expired authority lease'; END IF;
		IF NEW.state IN ('reconciled','indeterminate') AND NEW.exact_external_identity = '' THEN RAISE EXCEPTION 'terminal recovery requires exact external identity'; END IF;
    END IF;
    IF TG_TABLE_NAME = 'run' THEN
        IF NEW.fence_generation < OLD.fence_generation THEN RAISE EXCEPTION 'run fence cannot decrease'; END IF;
    ELSIF TG_TABLE_NAME = 'recovery_state' THEN
        IF NEW.reconciliation_fence < OLD.reconciliation_fence THEN RAISE EXCEPTION 'reconciliation fence cannot decrease'; END IF;
    END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS run_updated_at ON refresh.run;
CREATE TRIGGER run_updated_at BEFORE UPDATE ON refresh.run FOR EACH ROW EXECUTE FUNCTION refresh.guard_updated_at();

CREATE OR REPLACE FUNCTION refresh.guard_run_claim_evidence() RETURNS trigger
-- +goose StatementBegin
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM refresh.attempt a WHERE a.run_id=NEW.run_id AND a.attempt_number=NEW.attempt_count AND a.fence_generation=NEW.fence_generation) THEN
        RAISE EXCEPTION 'running run must have matching durable attempt evidence';
    END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS run_claim_evidence_guard ON refresh.run;
CREATE CONSTRAINT TRIGGER run_claim_evidence_guard
    AFTER UPDATE OF status ON refresh.run
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW WHEN (NEW.status='running' AND OLD.status IN ('queued','prepared'))
    EXECUTE FUNCTION refresh.guard_run_claim_evidence();

DROP TRIGGER IF EXISTS recovery_updated_at ON refresh.recovery_state;
CREATE TRIGGER recovery_updated_at BEFORE UPDATE ON refresh.recovery_state FOR EACH ROW EXECUTE FUNCTION refresh.guard_updated_at();

CREATE OR REPLACE FUNCTION refresh.guard_attempt_update() RETURNS trigger
-- +goose StatementBegin
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
DECLARE run_owner text; run_fence bigint; run_status text; run_expiry timestamptz;
BEGIN
    IF NEW.run_id IS DISTINCT FROM OLD.run_id OR NEW.attempt_number IS DISTINCT FROM OLD.attempt_number OR NEW.fence_generation < OLD.fence_generation OR NEW.owner_id IS DISTINCT FROM OLD.owner_id OR NEW.claimed_at IS DISTINCT FROM OLD.claimed_at OR NEW.started_at IS DISTINCT FROM OLD.started_at THEN RAISE EXCEPTION 'attempt identity is immutable'; END IF;
    IF OLD.status <> 'running' AND NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION 'terminal attempt is immutable'; END IF;
    SELECT lease_owner,fence_generation,status,lease_expires_at INTO run_owner,run_fence,run_status,run_expiry FROM refresh.run WHERE run_id=NEW.run_id;
    IF NEW.status IN ('succeeded','failed','cancelled','indeterminate') AND NEW.evidence = '{}'::jsonb THEN RAISE EXCEPTION 'terminal attempt requires evidence'; END IF;
    IF NEW.status IN ('succeeded','failed','cancelled','indeterminate') AND (run_owner IS DISTINCT FROM NEW.owner_id OR run_fence IS DISTINCT FROM NEW.fence_generation OR run_status NOT IN ('running','prepared') OR run_expiry IS NULL OR run_expiry <= clock_timestamp()) THEN
        RAISE EXCEPTION 'attempt terminal transition is not fenced by current live run';
    END IF;
    IF NEW.status='expired' AND (run_fence IS DISTINCT FROM NEW.fence_generation OR run_status NOT IN ('running','prepared','failed','superseded') OR run_expiry IS NULL OR run_expiry > clock_timestamp()) THEN
        RAISE EXCEPTION 'attempt expiry is not fenced by expired run';
    END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS attempt_guard ON refresh.attempt;
CREATE TRIGGER attempt_guard BEFORE UPDATE ON refresh.attempt FOR EACH ROW EXECUTE FUNCTION refresh.guard_attempt_update();

CREATE OR REPLACE FUNCTION refresh.guard_publication_update() RETURNS trigger
-- +goose StatementBegin
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
DECLARE run_owner text; run_fence bigint; run_status text; run_expiry timestamptz;
BEGIN
    IF NEW.publication_id IS DISTINCT FROM OLD.publication_id OR NEW.run_id IS DISTINCT FROM OLD.run_id OR NEW.base_generation_id IS DISTINCT FROM OLD.base_generation_id OR NEW.result_generation_id IS DISTINCT FROM OLD.result_generation_id OR NEW.plan_digest IS DISTINCT FROM OLD.plan_digest OR NEW.artifact_digest IS DISTINCT FROM OLD.artifact_digest OR NEW.physical_pool_id IS DISTINCT FROM OLD.physical_pool_id OR NEW.catalog_id IS DISTINCT FROM OLD.catalog_id OR NEW.expected_target_revision IS DISTINCT FROM OLD.expected_target_revision OR NEW.result_target_revision IS DISTINCT FROM OLD.result_target_revision OR NEW.fence_generation IS DISTINCT FROM OLD.fence_generation OR NEW.owner_id IS DISTINCT FROM OLD.owner_id OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN RAISE EXCEPTION 'publication identity is immutable'; END IF;
    IF OLD.state <> 'pending' AND NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION 'terminal publication is immutable'; END IF;
    IF NEW.state <> 'committed' OR NEW.snapshot_id IS NULL OR NEW.snapshot_id <= 0 OR NEW.committed_at IS NULL OR NEW.evidence = '{}'::jsonb THEN RAISE EXCEPTION 'publication transition requires committed physical evidence'; END IF;
    SELECT lease_owner,fence_generation,status,lease_expires_at INTO run_owner,run_fence,run_status,run_expiry FROM refresh.run WHERE run_id=NEW.run_id;
    IF run_owner IS DISTINCT FROM NEW.owner_id OR run_fence IS DISTINCT FROM NEW.fence_generation OR run_status NOT IN ('running','prepared') OR run_expiry IS NULL OR run_expiry <= clock_timestamp() THEN RAISE EXCEPTION 'publication transition is not fenced by current live run'; END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS publication_guard ON refresh.publication_link;
CREATE TRIGGER publication_guard BEFORE UPDATE ON refresh.publication_link FOR EACH ROW EXECUTE FUNCTION refresh.guard_publication_update();

CREATE OR REPLACE FUNCTION refresh.guard_occurrence_update() RETURNS trigger
-- +goose StatementBegin
LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
BEGIN
    IF NEW.occurrence_id IS DISTINCT FROM OLD.occurrence_id OR NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.environment IS DISTINCT FROM OLD.environment OR NEW.pipeline_id IS DISTINCT FROM OLD.pipeline_id OR NEW.nominal_time IS DISTINCT FROM OLD.nominal_time OR NEW.schedule_revision_id IS DISTINCT FROM OLD.schedule_revision_id OR NEW.matching_schedule_ids IS DISTINCT FROM OLD.matching_schedule_ids OR NEW.generation_id IS DISTINCT FROM OLD.generation_id OR NEW.artifact_digest IS DISTINCT FROM OLD.artifact_digest OR NEW.created_at IS DISTINCT FROM OLD.created_at OR NEW.fence_generation < OLD.fence_generation THEN RAISE EXCEPTION 'occurrence identity is immutable'; END IF;
	IF OLD.run_id IS NOT NULL AND NEW.run_id IS DISTINCT FROM OLD.run_id THEN RAISE EXCEPTION 'occurrence run binding is immutable'; END IF;
	IF OLD.run_id IS NULL AND NEW.run_id IS NOT NULL AND NOT (OLD.status='claimed' AND NEW.status='queued') THEN RAISE EXCEPTION 'occurrence run binding requires claimed to queued transition'; END IF;
    IF NEW.status IN ('queued','running','succeeded','failed','cancelled','superseded') AND NEW.run_id IS NULL THEN RAISE EXCEPTION 'bound occurrence status requires run id'; END IF;
    IF OLD.status IN ('succeeded','failed','cancelled','skipped','superseded') AND NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION 'terminal occurrence is immutable'; END IF;
    IF OLD.status='pending' AND NEW.status NOT IN ('pending','claimed','skipped','superseded') THEN RAISE EXCEPTION 'illegal pending occurrence transition'; END IF;
    IF OLD.status='claimed' AND NEW.status NOT IN ('claimed','pending','queued') THEN RAISE EXCEPTION 'illegal claimed occurrence transition'; END IF;
    IF NEW.status='claimed' AND (NEW.lease_owner = '' OR NEW.lease_expires_at IS NULL OR NEW.lease_expires_at <= clock_timestamp() OR NEW.lease_expires_at > clock_timestamp() + interval '24 hours' OR NEW.claimed_at IS NULL) THEN RAISE EXCEPTION 'claimed occurrence requires a live bounded lease'; END IF;
    IF OLD.status='pending' AND NEW.status='claimed' AND NEW.fence_generation <> OLD.fence_generation + 1 THEN RAISE EXCEPTION 'occurrence claim must advance fence'; END IF;
    IF OLD.status='claimed' AND NEW.status='claimed' AND NEW.fence_generation <> OLD.fence_generation THEN RAISE EXCEPTION 'occurrence heartbeat cannot change fence'; END IF;
    IF NEW.status IN ('queued','running','succeeded','failed','cancelled','skipped','superseded') AND (NEW.lease_owner <> '' OR NEW.lease_expires_at IS NOT NULL) THEN RAISE EXCEPTION 'non-claimed occurrence cannot hold a lease'; END IF;
	IF OLD.status='queued' AND NEW.status NOT IN ('queued','running','succeeded','failed','cancelled','superseded') THEN RAISE EXCEPTION 'illegal queued occurrence transition'; END IF;
	IF OLD.status='running' AND NEW.status NOT IN ('running','succeeded','failed','cancelled','superseded') THEN RAISE EXCEPTION 'illegal running occurrence transition'; END IF;
	IF NEW.status IN ('pending','claimed','queued','running') AND NEW.finished_at IS NOT NULL THEN RAISE EXCEPTION 'active occurrence cannot have finished time'; END IF;
	IF NEW.status IN ('succeeded','failed','cancelled','skipped','superseded') AND (NEW.finished_at IS NULL OR NEW.outcome = '{}'::jsonb) THEN RAISE EXCEPTION 'terminal occurrence requires finish time and outcome'; END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS occurrence_guard ON refresh.schedule_occurrence;
CREATE TRIGGER occurrence_guard BEFORE UPDATE ON refresh.schedule_occurrence FOR EACH ROW EXECUTE FUNCTION refresh.guard_occurrence_update();

-- Runtime roles never receive direct DELETE.  This bounded maintenance
-- function only expires abandoned leases and leaves all evidence queryable.
CREATE OR REPLACE FUNCTION refresh.maintenance(p_limit integer)
RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER
-- +goose StatementBegin
SET search_path = pg_catalog, refresh AS $$
DECLARE v_count bigint := 0; v_affected bigint := 0; v_limit integer;
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 1000 THEN RAISE EXCEPTION 'refresh maintenance limit must be between 1 and 1000'; END IF;
    v_limit := p_limit;
    WITH stale AS (
        SELECT run_id, attempt_number FROM refresh.attempt
        WHERE status = 'running' AND lease_expires_at <= clock_timestamp()
        ORDER BY lease_expires_at, run_id, attempt_number LIMIT v_limit
        FOR UPDATE SKIP LOCKED
    )
    UPDATE refresh.attempt a SET status='expired', finished_at=clock_timestamp(), error='lease expired'
    FROM stale s WHERE a.run_id=s.run_id AND a.attempt_number=s.attempt_number;
    GET DIAGNOSTICS v_affected = ROW_COUNT;
    v_count := v_affected;
    v_limit := v_limit - v_affected::integer;
    IF v_limit <= 0 THEN
        RETURN v_count;
    END IF;
    WITH stale_runs AS (
        SELECT r.run_id FROM refresh.run r
        WHERE r.status IN ('running','prepared') AND r.lease_expires_at <= clock_timestamp()
        ORDER BY r.lease_expires_at, r.run_id LIMIT v_limit FOR UPDATE SKIP LOCKED
    )
    UPDATE refresh.run r SET status='failed', error='lease expired', finished_at=clock_timestamp(), lease_owner='', lease_expires_at=NULL
    FROM stale_runs s WHERE r.run_id=s.run_id AND r.status IN ('running','prepared');
    GET DIAGNOSTICS v_affected = ROW_COUNT;
    RETURN v_count + v_affected;
END; $$;
-- +goose StatementEnd

-- Capability grants are conditional so SchemaSQL remains independently
-- applicable to an empty conformance database.  Production provisioning
-- creates these roles before applying the control baseline.
-- +goose StatementBegin
DO $$
BEGIN
	REVOKE ALL ON ALL TABLES IN SCHEMA refresh FROM PUBLIC;
	REVOKE ALL ON ALL SEQUENCES IN SCHEMA refresh FROM PUBLIC;
	REVOKE ALL ON FUNCTION refresh.maintenance(integer) FROM PUBLIC;
	REVOKE ALL ON FUNCTION refresh.fail_child_runs(text,text) FROM PUBLIC;
	REVOKE ALL ON FUNCTION refresh.complete_child_runs(text) FROM PUBLIC;
	REVOKE ALL ON FUNCTION refresh.close_omitted_schedules(text,text,text,text[],text[]) FROM PUBLIC;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_owner') THEN
        GRANT ALL ON ALL TABLES IN SCHEMA refresh TO leapview_control_owner;
        GRANT ALL ON ALL SEQUENCES IN SCHEMA refresh TO leapview_control_owner;
        GRANT ALL ON ALL FUNCTIONS IN SCHEMA refresh TO leapview_control_owner;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_migrator') THEN
        GRANT ALL ON ALL TABLES IN SCHEMA refresh TO leapview_control_migrator;
        GRANT ALL ON ALL SEQUENCES IN SCHEMA refresh TO leapview_control_migrator;
        GRANT ALL ON ALL FUNCTIONS IN SCHEMA refresh TO leapview_control_migrator;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA refresh TO leapview_control_maintenance;
        GRANT EXECUTE ON FUNCTION refresh.maintenance(integer) TO leapview_control_maintenance;
        REVOKE ALL ON FUNCTION refresh.fail_child_runs(text,text), refresh.complete_child_runs(text), refresh.close_omitted_schedules(text,text,text,text[],text[]) FROM leapview_control_maintenance;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA refresh TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON refresh.schedule_revision, refresh.run, refresh.schedule_occurrence, refresh.attempt, refresh.publication_link, refresh.recovery_state, refresh.data_version TO leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION refresh.fail_child_runs(text,text), refresh.complete_child_runs(text), refresh.close_omitted_schedules(text,text,text,text[],text[]) TO leapview_control_runtime;
        REVOKE DELETE ON refresh.schedule_revision, refresh.run, refresh.schedule_occurrence, refresh.attempt, refresh.publication_link, refresh.recovery_state, refresh.data_version FROM leapview_control_runtime;
        REVOKE EXECUTE ON FUNCTION refresh.maintenance(integer) FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA refresh TO leapview_control_readonly;
        GRANT SELECT ON ALL TABLES IN SCHEMA refresh TO leapview_control_readonly;
        REVOKE ALL ON FUNCTION refresh.fail_child_runs(text,text), refresh.complete_child_runs(text), refresh.close_omitted_schedules(text,text,text,text[],text[]) FROM leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA refresh TO leapview_control_backup;
        GRANT SELECT ON ALL TABLES IN SCHEMA refresh TO leapview_control_backup;
        REVOKE ALL ON FUNCTION refresh.fail_child_runs(text,text), refresh.complete_child_runs(text), refresh.close_omitted_schedules(text,text,text,text[],text[]) FROM leapview_control_backup;
    END IF;
END $$;
-- +goose StatementEnd

-- capability source: internal/recoveryset/postgres/schema.sql
-- Durable PostgreSQL recovery-set frontier (FAI-573).
-- The frontier is normalized so recovery validation can read exact typed
-- identities without inferring a latest/current row.

CREATE SCHEMA IF NOT EXISTS recovery;

CREATE TABLE IF NOT EXISTS recovery.recovery_set (
    set_id                         uuid PRIMARY KEY,
    schema_version                 integer NOT NULL DEFAULT 1 CHECK (schema_version = 1),
    expected_cluster_points        integer NOT NULL DEFAULT 2 CHECK (expected_cluster_points = 2),
    expected_object_roots           integer NOT NULL CHECK (expected_object_roots BETWEEN 1 AND 128),
    target_id                      text NOT NULL,
    generation_id                  uuid NOT NULL,
    publication_id                 uuid NOT NULL,
    target_revision                bigint NOT NULL CHECK (target_revision > 0),
    snapshot_seal_id               uuid NOT NULL,
    physical_pool_id               text NOT NULL,
    tenant_domain                  text NOT NULL,
    region                         text NOT NULL,
    encryption_domain              text NOT NULL,
    object_namespace               text NOT NULL,
    catalog_database               text NOT NULL,
    catalog_id                     text NOT NULL,
    catalog_uuid                   text NOT NULL,
    catalog_version                bigint NOT NULL CHECK (catalog_version > 0),
    ducklake_snapshot_id           bigint NOT NULL CHECK (ducklake_snapshot_id > 0),
    relation_namespace             text NOT NULL,
    relation_manifest_digest       text NOT NULL,
    closure_digest                 text NOT NULL,
    object_root                    text NOT NULL,
    object_root_digest             text NOT NULL,
    artifact_root                  text NOT NULL,
    artifact_root_digest           text NOT NULL,
    serving_artifact_id            text NOT NULL,
    serving_artifact_digest        text NOT NULL,
    compiled_graph_digest          text NOT NULL,
    compiled_config_digest         text NOT NULL,
    security_domain_fingerprint    text NOT NULL,
    request_digest                 text NOT NULL,
    plan_digest                    text NOT NULL,
    compatibility_digest           text NOT NULL,
    duckdb_version                 text NOT NULL,
    runtime_version                text NOT NULL,
    ducklake_extension_version     text NOT NULL,
    ducklake_spec_version          text NOT NULL,
    catalog_schema_version         text NOT NULL,
    duckdb_runtime                 text NOT NULL,
    ducklake_extension             text NOT NULL,
    catalog_format                 text NOT NULL,
    storage_implementation         text NOT NULL,
    object_naming_contract         text NOT NULL,
    fence_epoch                    bigint NOT NULL CHECK (fence_epoch > 0),
    audit_identity                 text NOT NULL,
    frontier_digest                text NOT NULL,
    status                         text NOT NULL DEFAULT 'prepared'
        CHECK (status IN ('prepared', 'published', 'superseded', 'invalid')),
    created_by                     text NOT NULL,
    created_at                     timestamptz NOT NULL DEFAULT clock_timestamp(),
    published_by                   text,
    published_at                   timestamptz,
    -- The attempt row is created after this frontier row, so the explicit
    -- publication binding is verified transactionally by PublishRecoverySet
    -- rather than expressed as a circular foreign key.
    published_validation_attempt_id uuid,
    CHECK (target_id = btrim(target_id) AND octet_length(target_id) BETWEEN 1 AND 255),
    CHECK (physical_pool_id = btrim(physical_pool_id) AND octet_length(physical_pool_id) BETWEEN 1 AND 255),
    CHECK (tenant_domain = btrim(tenant_domain) AND octet_length(tenant_domain) BETWEEN 1 AND 255),
    CHECK (region = btrim(region) AND octet_length(region) BETWEEN 1 AND 128),
    CHECK (encryption_domain = btrim(encryption_domain) AND octet_length(encryption_domain) BETWEEN 1 AND 255),
    CHECK (object_namespace = btrim(object_namespace) AND octet_length(object_namespace) BETWEEN 1 AND 512),
    CHECK (catalog_database = btrim(catalog_database) AND octet_length(catalog_database) BETWEEN 1 AND 255),
    CHECK (catalog_id = btrim(catalog_id) AND octet_length(catalog_id) BETWEEN 1 AND 255),
    CHECK (catalog_uuid = btrim(catalog_uuid) AND octet_length(catalog_uuid) BETWEEN 1 AND 255),
    CHECK (relation_namespace = btrim(relation_namespace) AND octet_length(relation_namespace) BETWEEN 1 AND 512),
    CHECK (serving_artifact_id = btrim(serving_artifact_id) AND octet_length(serving_artifact_id) BETWEEN 1 AND 255),
    CHECK (duckdb_runtime = btrim(duckdb_runtime) AND octet_length(duckdb_runtime) BETWEEN 1 AND 255),
    CHECK (ducklake_extension = btrim(ducklake_extension) AND octet_length(ducklake_extension) BETWEEN 1 AND 255),
    CHECK (catalog_format = btrim(catalog_format) AND octet_length(catalog_format) BETWEEN 1 AND 255),
    CHECK (storage_implementation = btrim(storage_implementation) AND octet_length(storage_implementation) BETWEEN 1 AND 255),
    CHECK (object_naming_contract = btrim(object_naming_contract) AND octet_length(object_naming_contract) BETWEEN 1 AND 255),
    CHECK (audit_identity = btrim(audit_identity) AND octet_length(audit_identity) BETWEEN 1 AND 255),
    CHECK (created_by = btrim(created_by) AND octet_length(created_by) BETWEEN 1 AND 255),
    CHECK (published_by IS NULL OR (published_by = btrim(published_by) AND octet_length(published_by) BETWEEN 1 AND 255)),
    CHECK (generation_id IS NOT NULL AND publication_id IS NOT NULL AND snapshot_seal_id IS NOT NULL),
    CHECK (relation_manifest_digest ~ '^sha256:[0-9a-f]{64}$' AND closure_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (object_root_digest ~ '^sha256:[0-9a-f]{64}$' AND artifact_root_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (serving_artifact_digest ~ '^sha256:[0-9a-f]{64}$' AND compiled_graph_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (compiled_config_digest ~ '^sha256:[0-9a-f]{64}$' AND security_domain_fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$' AND plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (compatibility_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (frontier_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK ((status = 'prepared' AND published_by IS NULL AND published_at IS NULL AND published_validation_attempt_id IS NULL)
        OR (status IN ('published', 'superseded') AND published_by IS NOT NULL AND published_at IS NOT NULL AND published_validation_attempt_id IS NOT NULL)
        OR status = 'invalid')
);

CREATE TABLE IF NOT EXISTS recovery.recovery_cluster_point (
    set_id               uuid NOT NULL REFERENCES recovery.recovery_set(set_id) ON DELETE RESTRICT,
    database_role        text NOT NULL CHECK (database_role IN ('control', 'ducklake')),
    cluster_identity     text NOT NULL,
    database_identity    text NOT NULL,
    recovery_identity    text NOT NULL,
    PRIMARY KEY (set_id, database_role),
    CHECK (cluster_identity = btrim(cluster_identity) AND octet_length(cluster_identity) BETWEEN 1 AND 255),
    CHECK (database_identity = btrim(database_identity) AND octet_length(database_identity) BETWEEN 1 AND 255),
    CHECK (recovery_identity = btrim(recovery_identity) AND octet_length(recovery_identity) BETWEEN 1 AND 512)
);

CREATE TABLE IF NOT EXISTS recovery.recovery_object_root (
    set_id                      uuid NOT NULL REFERENCES recovery.recovery_set(set_id) ON DELETE RESTRICT,
    root_kind                   text NOT NULL,
    root_uri                    text NOT NULL,
    version_id                  text NOT NULL,
    digest                      text NOT NULL,
    provider_recovery_frontier text NOT NULL DEFAULT '',
    PRIMARY KEY (set_id, root_kind, root_uri, version_id),
    CHECK (root_kind = btrim(root_kind) AND octet_length(root_kind) BETWEEN 1 AND 128),
    CHECK (root_uri = btrim(root_uri) AND octet_length(root_uri) BETWEEN 1 AND 2048),
    CHECK (version_id = btrim(version_id) AND octet_length(version_id) BETWEEN 1 AND 512),
    CHECK (provider_recovery_frontier = btrim(provider_recovery_frontier)
        AND octet_length(provider_recovery_frontier) <= 512
        AND (root_uri !~* '^(s3|gs|az)://' OR provider_recovery_frontier <> '')),
    CHECK (digest ~ '^sha256:[0-9a-f]{64}$')
);

CREATE TABLE IF NOT EXISTS recovery.validation_attempt (
    attempt_id       uuid PRIMARY KEY,
    set_id           uuid NOT NULL REFERENCES recovery.recovery_set(set_id) ON DELETE RESTRICT,
    owner_id         text NOT NULL,
    fence_epoch      bigint NOT NULL CHECK (fence_epoch > 0),
    audit_identity   text NOT NULL,
    status           text NOT NULL CHECK (status IN ('running', 'passed', 'failed')),
    result_digest    text,
    error            text,
    started_at       timestamptz NOT NULL DEFAULT clock_timestamp(),
    completed_at     timestamptz,
    CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255),
    CHECK (audit_identity = btrim(audit_identity) AND octet_length(audit_identity) BETWEEN 1 AND 255),
    CHECK (result_digest IS NULL OR result_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (error IS NULL OR octet_length(error) <= 16384),
    CHECK ((status = 'running' AND completed_at IS NULL AND result_digest IS NULL AND error IS NULL)
        OR (status = 'passed' AND completed_at IS NOT NULL AND result_digest IS NOT NULL AND error IS NULL)
        OR (status = 'failed' AND completed_at IS NOT NULL AND error IS NOT NULL))
);

CREATE TABLE IF NOT EXISTS recovery.validation_result (
    attempt_id       uuid PRIMARY KEY REFERENCES recovery.validation_attempt(attempt_id) ON DELETE RESTRICT,
    result_digest    text NOT NULL CHECK (result_digest ~ '^sha256:[0-9a-f]{64}$'),
    evidence         jsonb NOT NULL CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) BETWEEN 2 AND 65536),
    recorded_at      timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE OR REPLACE FUNCTION recovery.reject_frontier_mutation()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, recovery AS $$
BEGIN
    IF TG_OP = 'UPDATE'
       AND NEW.set_id = OLD.set_id
       AND NEW.schema_version = OLD.schema_version
       AND NEW.expected_cluster_points = OLD.expected_cluster_points AND NEW.expected_object_roots = OLD.expected_object_roots
       AND NEW.target_id = OLD.target_id AND NEW.generation_id = OLD.generation_id
       AND NEW.publication_id = OLD.publication_id AND NEW.target_revision = OLD.target_revision
       AND NEW.snapshot_seal_id = OLD.snapshot_seal_id AND NEW.physical_pool_id = OLD.physical_pool_id
       AND NEW.tenant_domain = OLD.tenant_domain AND NEW.region = OLD.region
       AND NEW.encryption_domain = OLD.encryption_domain AND NEW.object_namespace = OLD.object_namespace
       AND NEW.catalog_database = OLD.catalog_database AND NEW.catalog_id = OLD.catalog_id
       AND NEW.catalog_uuid = OLD.catalog_uuid AND NEW.catalog_version = OLD.catalog_version
       AND NEW.ducklake_snapshot_id = OLD.ducklake_snapshot_id AND NEW.relation_namespace = OLD.relation_namespace
       AND NEW.relation_manifest_digest = OLD.relation_manifest_digest AND NEW.closure_digest = OLD.closure_digest
       AND NEW.object_root = OLD.object_root AND NEW.object_root_digest = OLD.object_root_digest
       AND NEW.artifact_root = OLD.artifact_root AND NEW.artifact_root_digest = OLD.artifact_root_digest
       AND NEW.serving_artifact_id = OLD.serving_artifact_id AND NEW.serving_artifact_digest = OLD.serving_artifact_digest
       AND NEW.compiled_graph_digest = OLD.compiled_graph_digest AND NEW.compiled_config_digest = OLD.compiled_config_digest
       AND NEW.security_domain_fingerprint = OLD.security_domain_fingerprint AND NEW.request_digest = OLD.request_digest
       AND NEW.plan_digest = OLD.plan_digest AND NEW.compatibility_digest = OLD.compatibility_digest
       AND NEW.frontier_digest = OLD.frontier_digest
       AND NEW.duckdb_version = OLD.duckdb_version AND NEW.runtime_version = OLD.runtime_version
       AND NEW.ducklake_extension_version = OLD.ducklake_extension_version AND NEW.ducklake_spec_version = OLD.ducklake_spec_version
       AND NEW.catalog_schema_version = OLD.catalog_schema_version AND NEW.duckdb_runtime = OLD.duckdb_runtime
       AND NEW.ducklake_extension = OLD.ducklake_extension AND NEW.catalog_format = OLD.catalog_format
       AND NEW.storage_implementation = OLD.storage_implementation AND NEW.object_naming_contract = OLD.object_naming_contract
       AND NEW.fence_epoch = OLD.fence_epoch AND NEW.audit_identity = OLD.audit_identity
       AND NEW.created_by = OLD.created_by AND NEW.created_at = OLD.created_at
       AND ((OLD.status = 'prepared' AND NEW.published_validation_attempt_id IS NOT NULL)
            OR NEW.published_validation_attempt_id IS NOT DISTINCT FROM OLD.published_validation_attempt_id)
       AND ((OLD.status = 'prepared' AND NEW.status = 'published' AND NEW.published_by IS NOT NULL AND NEW.published_at IS NOT NULL
             AND NEW.published_validation_attempt_id IS NOT NULL
             AND EXISTS (
                 SELECT 1
                 FROM recovery.validation_attempt AS validation
                 JOIN recovery.validation_result AS result ON result.attempt_id = validation.attempt_id
                 WHERE validation.attempt_id = NEW.published_validation_attempt_id
                   AND validation.set_id = NEW.set_id
                   AND validation.fence_epoch = NEW.fence_epoch
                   AND validation.status = 'passed'
                   AND validation.result_digest = result.result_digest
             )
             AND (SELECT count(*) FROM recovery.recovery_cluster_point WHERE set_id = NEW.set_id) = 2
             AND (SELECT count(*) FROM recovery.recovery_object_root WHERE set_id = NEW.set_id) = 2
             AND EXISTS (
                 SELECT 1 FROM recovery.recovery_object_root AS root
                  WHERE root.set_id = NEW.set_id AND root.root_kind = 'ducklake'
                    AND root.root_uri = NEW.object_root AND root.digest = NEW.object_root_digest
             )
             AND EXISTS (
                 SELECT 1 FROM recovery.recovery_object_root AS root
                  WHERE root.set_id = NEW.set_id AND root.root_kind = 'serving-artifact'
                    AND root.root_uri = NEW.artifact_root AND root.digest = NEW.artifact_root_digest
             ))
         OR (OLD.status = 'published' AND NEW.status IN ('published', 'superseded') AND NEW.published_by = OLD.published_by AND NEW.published_at = OLD.published_at)) THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'recovery-set frontier identity is immutable';
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION recovery.reject_frontier_insert()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, recovery AS $$
BEGIN
    -- A frontier is created prepared and can become published only through
    -- the fenced transition below, which proves one exact passed validation.
    IF NEW.status = 'prepared' AND NEW.published_validation_attempt_id IS NULL THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'recovery-set frontier must be created prepared';
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS recovery_set_immutable ON recovery.recovery_set;
CREATE TRIGGER recovery_set_immutable BEFORE UPDATE OR DELETE ON recovery.recovery_set
FOR EACH ROW EXECUTE FUNCTION recovery.reject_frontier_mutation();

DROP TRIGGER IF EXISTS recovery_set_insert_guard ON recovery.recovery_set;
CREATE TRIGGER recovery_set_insert_guard BEFORE INSERT ON recovery.recovery_set
FOR EACH ROW EXECUTE FUNCTION recovery.reject_frontier_insert();

CREATE OR REPLACE FUNCTION recovery.reject_child_mutation()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, recovery AS $$
DECLARE expected_count integer; current_count integer; frontier_status text;
BEGIN
    IF TG_OP = 'INSERT' THEN
        SELECT CASE WHEN TG_TABLE_NAME = 'recovery_cluster_point' THEN expected_cluster_points ELSE expected_object_roots END,
               status
          INTO STRICT expected_count, frontier_status
          FROM recovery.recovery_set WHERE set_id = NEW.set_id FOR UPDATE;
        IF frontier_status <> 'prepared' THEN
            RAISE EXCEPTION 'recovery-set evidence cannot be appended after publication';
        END IF;
        IF TG_TABLE_NAME = 'recovery_cluster_point' THEN
            SELECT expected_cluster_points INTO expected_count FROM recovery.recovery_set WHERE set_id = NEW.set_id;
            SELECT count(*) INTO current_count FROM recovery.recovery_cluster_point WHERE set_id = NEW.set_id;
        ELSE
            SELECT expected_object_roots INTO expected_count FROM recovery.recovery_set WHERE set_id = NEW.set_id;
            SELECT count(*) INTO current_count FROM recovery.recovery_object_root WHERE set_id = NEW.set_id;
        END IF;
        IF current_count >= expected_count THEN
            RAISE EXCEPTION 'recovery-set evidence is complete and cannot be extended';
        END IF;
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'recovery-set evidence is append-only';
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION recovery.guard_validation_attempt_transition()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, recovery AS $$
BEGIN
    IF TG_OP = 'INSERT'
       AND NEW.status = 'running' AND NEW.result_digest IS NULL
       AND NEW.error IS NULL AND NEW.completed_at IS NULL
       AND EXISTS (
           SELECT 1 FROM recovery.recovery_set AS frontier
           WHERE frontier.set_id = NEW.set_id
             AND frontier.fence_epoch = NEW.fence_epoch
             AND frontier.status = 'prepared'
       ) THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'UPDATE'
       AND OLD.status = 'running' AND NEW.status IN ('passed', 'failed')
       AND NEW.attempt_id = OLD.attempt_id AND NEW.set_id = OLD.set_id
       AND NEW.owner_id = OLD.owner_id AND NEW.fence_epoch = OLD.fence_epoch
       AND NEW.audit_identity = OLD.audit_identity AND NEW.started_at = OLD.started_at
       AND NEW.completed_at IS NOT NULL
       AND ((NEW.status = 'passed' AND EXISTS (
                SELECT 1 FROM recovery.validation_result AS result
                WHERE result.attempt_id = NEW.attempt_id AND result.result_digest = NEW.result_digest
            ))
         OR (NEW.status = 'failed' AND (NEW.result_digest IS NULL OR EXISTS (
                SELECT 1 FROM recovery.validation_result AS result
                WHERE result.attempt_id = NEW.attempt_id AND result.result_digest = NEW.result_digest
            )))) THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'recovery validation attempt identity or terminal result is immutable';
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS recovery_validation_attempt_guard ON recovery.validation_attempt;
CREATE TRIGGER recovery_validation_attempt_guard BEFORE INSERT OR UPDATE OR DELETE ON recovery.validation_attempt
FOR EACH ROW EXECUTE FUNCTION recovery.guard_validation_attempt_transition();

CREATE OR REPLACE FUNCTION recovery.reject_validation_result_mutation()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, recovery AS $$
BEGIN
    RAISE EXCEPTION 'recovery validation evidence is immutable';
END;
$$;
-- +goose StatementEnd

-- Keep the SQL-side digest check byte-for-byte compatible with Go's
-- encoding/json output. PostgreSQL jsonb compares object structure (which is
-- useful for rejecting reordered/unknown evidence keys), but its text form
-- reorders keys and inserts spaces. Build the envelope in the Go struct field
-- order from individually encoded scalar values so a maintenance-role SQL
-- insert cannot mint an arbitrary result digest.
CREATE OR REPLACE FUNCTION recovery.canonical_json_string(value text)
-- +goose StatementBegin
RETURNS text LANGUAGE sql IMMUTABLE STRICT SET search_path = pg_catalog, recovery AS $$
    SELECT replace(replace(replace(replace(replace(to_json(value)::text, '<', E'\\u003c'), '>', E'\\u003e'), '&', E'\\u0026'), U&'\2028', E'\\u2028'), U&'\2029', E'\\u2029')
$$;
-- +goose StatementEnd

-- Validation results are capability evidence, not an arbitrary JSON side
-- channel.  Reconstruct the exact v1 envelope from the immutable frontier
-- and its append-only child rows before accepting a direct SQL INSERT.  The
-- Go repository performs the same ValidateFor comparison; this trigger keeps
-- maintenance-role SQL from bypassing that contract before a passed attempt
-- can be published.
CREATE OR REPLACE FUNCTION recovery.guard_validation_result_insert()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, recovery AS $$
DECLARE
    frontier recovery.recovery_set;
    expected_evidence jsonb;
    expected_evidence_json text;
    expected_digest text;
BEGIN
    SELECT selected.*
      INTO frontier
      FROM recovery.validation_attempt AS attempt
      JOIN recovery.recovery_set AS selected ON selected.set_id = attempt.set_id
     WHERE attempt.attempt_id = NEW.attempt_id
     FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'validation result requires an exact recovery frontier';
    END IF;
    IF (SELECT count(*) FROM recovery.recovery_cluster_point WHERE set_id = frontier.set_id) <> frontier.expected_cluster_points
       OR (SELECT count(*) FROM recovery.recovery_object_root WHERE set_id = frontier.set_id) <> frontier.expected_object_roots THEN
        RAISE EXCEPTION 'validation result requires complete recovery frontier evidence';
    END IF;
    IF frontier.expected_object_roots <> 2
       OR (SELECT count(*) FROM recovery.recovery_object_root WHERE set_id = frontier.set_id) <> 2
       OR NOT EXISTS (
           SELECT 1 FROM recovery.recovery_object_root AS root
            WHERE root.set_id = frontier.set_id AND root.root_kind = 'ducklake'
              AND root.root_uri = frontier.object_root AND root.digest = frontier.object_root_digest
       )
       OR NOT EXISTS (
           SELECT 1 FROM recovery.recovery_object_root AS root
            WHERE root.set_id = frontier.set_id AND root.root_kind = 'serving-artifact'
              AND root.root_uri = frontier.artifact_root AND root.digest = frontier.artifact_root_digest
       ) THEN
        RAISE EXCEPTION 'validation result requires canonical ducklake and serving-artifact roots';
    END IF;
    expected_evidence_json :=
        '{"schema_version":1,"set_id":' || recovery.canonical_json_string(frontier.set_id::text) ||
        ',"attempt_id":' || recovery.canonical_json_string(NEW.attempt_id::text) ||
        ',"frontier_digest":' || recovery.canonical_json_string(frontier.frontier_digest) ||
        ',"cluster_points":' || COALESCE((
            SELECT '[' || string_agg(
                '{"database_role":' || recovery.canonical_json_string(point.database_role) ||
                ',"cluster_identity":' || recovery.canonical_json_string(point.cluster_identity) ||
                ',"database_identity":' || recovery.canonical_json_string(point.database_identity) ||
                ',"recovery_identity":' || recovery.canonical_json_string(point.recovery_identity) || '}',
                ',' ORDER BY point.database_role
            ) || ']'
              FROM recovery.recovery_cluster_point AS point
             WHERE point.set_id = frontier.set_id
        ), '[]') ||
        ',"object_roots":' || COALESCE((
            SELECT '[' || string_agg(
                '{"kind":' || recovery.canonical_json_string(root.root_kind) ||
                ',"uri":' || recovery.canonical_json_string(root.root_uri) ||
                ',"version_id":' || recovery.canonical_json_string(root.version_id) ||
                ',"digest":' || recovery.canonical_json_string(root.digest) ||
                ',"provider_recovery_frontier":' || recovery.canonical_json_string(root.provider_recovery_frontier) || '}',
                ',' ORDER BY root.root_kind, root.root_uri, root.version_id
            ) || ']'
              FROM recovery.recovery_object_root AS root
             WHERE root.set_id = frontier.set_id
        ), '[]') ||
        ',"relation_namespace":' || recovery.canonical_json_string(frontier.relation_namespace) ||
        ',"relation_manifest_digest":' || recovery.canonical_json_string(frontier.relation_manifest_digest) ||
        ',"closure_digest":' || recovery.canonical_json_string(frontier.closure_digest) || '}';
    expected_evidence := expected_evidence_json::jsonb;
    expected_digest := 'sha256:' || encode(pg_catalog.sha256(convert_to(expected_evidence_json, 'UTF8')), 'hex');
    IF NEW.evidence IS DISTINCT FROM expected_evidence OR NEW.result_digest IS DISTINCT FROM expected_digest THEN
        RAISE EXCEPTION 'validation result evidence does not match exact recovery frontier';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS recovery_validation_result_guard ON recovery.validation_result;
CREATE TRIGGER recovery_validation_result_guard BEFORE INSERT ON recovery.validation_result
FOR EACH ROW EXECUTE FUNCTION recovery.guard_validation_result_insert();

DROP TRIGGER IF EXISTS recovery_validation_result_immutable ON recovery.validation_result;
CREATE TRIGGER recovery_validation_result_immutable BEFORE UPDATE OR DELETE ON recovery.validation_result
FOR EACH ROW EXECUTE FUNCTION recovery.reject_validation_result_mutation();

DROP TRIGGER IF EXISTS recovery_cluster_point_immutable ON recovery.recovery_cluster_point;
CREATE TRIGGER recovery_cluster_point_immutable BEFORE INSERT OR UPDATE OR DELETE ON recovery.recovery_cluster_point
FOR EACH ROW EXECUTE FUNCTION recovery.reject_child_mutation();
DROP TRIGGER IF EXISTS recovery_object_root_immutable ON recovery.recovery_object_root;
CREATE TRIGGER recovery_object_root_immutable BEFORE INSERT OR UPDATE OR DELETE ON recovery.recovery_object_root
FOR EACH ROW EXECUTE FUNCTION recovery.reject_child_mutation();

CREATE INDEX IF NOT EXISTS recovery_set_target_status_idx ON recovery.recovery_set (target_id, status, created_at DESC, set_id DESC);
CREATE INDEX IF NOT EXISTS recovery_validation_set_idx ON recovery.validation_attempt (set_id, started_at DESC, attempt_id DESC);

REVOKE ALL ON SCHEMA recovery FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA recovery FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA recovery FROM PUBLIC;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_migrator') THEN
        GRANT USAGE ON SCHEMA recovery TO leapview_control_migrator;
        GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA recovery TO leapview_control_migrator;
        GRANT EXECUTE ON FUNCTION recovery.canonical_json_string(text) TO leapview_control_migrator;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA recovery TO leapview_control_runtime;
        GRANT SELECT ON ALL TABLES IN SCHEMA recovery TO leapview_control_runtime;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON ALL TABLES IN SCHEMA recovery FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA recovery TO leapview_control_maintenance;
        GRANT SELECT, INSERT, UPDATE ON recovery.recovery_set, recovery.validation_attempt TO leapview_control_maintenance;
        GRANT SELECT, INSERT ON recovery.recovery_cluster_point, recovery.recovery_object_root, recovery.validation_result TO leapview_control_maintenance;
        GRANT EXECUTE ON FUNCTION recovery.canonical_json_string(text) TO leapview_control_maintenance;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA recovery TO leapview_control_backup;
        GRANT SELECT ON ALL TABLES IN SCHEMA recovery TO leapview_control_backup;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA recovery TO leapview_control_readonly;
        GRANT SELECT ON ALL TABLES IN SCHEMA recovery TO leapview_control_readonly;
    END IF;
END
$$;
-- +goose StatementEnd

-- capability source: internal/lineage/postgres/schema.sql
-- Durable, immutable lineage graph authority (ADR-0020 / FAI-568).
--
-- This is a capability-owned schema.  It is deliberately independent from
-- the control-plane baseline so conformance tests (and a future deployment
-- migration) can apply it in isolation.
CREATE SCHEMA IF NOT EXISTS lineage;

CREATE TABLE IF NOT EXISTS lineage.graphs (
    graph_digest TEXT PRIMARY KEY,
    graph_version INTEGER NOT NULL,
    project_id TEXT NOT NULL,
    node_count INTEGER NOT NULL CHECK (node_count BETWEEN 1 AND 100000),
    edge_count INTEGER NOT NULL CHECK (edge_count BETWEEN 0 AND 500000),
    compiler_version INTEGER NOT NULL DEFAULT 1 CHECK (compiler_version > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    CHECK (graph_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (graph_version > 0),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 256)
);

-- A graph digest is immutable, while a scope advances through revisions.  A
-- scope is deliberately explicit (for example a target or serving lane); it
-- is never inferred from a delivery or environment name.  valid_from and
-- valid_to form a non-overlapping half-open validity interval.  PostgreSQL
-- owns all timestamps so callers cannot back-date or forge publication
-- evidence.
CREATE UNIQUE INDEX IF NOT EXISTS lineage_graphs_project_digest_uq
    ON lineage.graphs (project_id, graph_digest);

CREATE TABLE IF NOT EXISTS lineage.revisions (
    project_id   TEXT NOT NULL,
    scope_id     TEXT NOT NULL,
    revision_id  BIGINT NOT NULL,
    graph_digest TEXT NOT NULL,
    valid_from   TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    valid_to     TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (project_id, scope_id, revision_id),
    FOREIGN KEY (graph_digest, project_id)
        REFERENCES lineage.graphs(graph_digest, project_id) ON DELETE RESTRICT,
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 256),
    CHECK (scope_id = btrim(scope_id) AND octet_length(scope_id) BETWEEN 1 AND 256),
    CHECK (revision_id > 0),
    CHECK (valid_to IS NULL OR valid_to > valid_from),
    CHECK (created_at >= valid_from)
);

CREATE UNIQUE INDEX IF NOT EXISTS lineage_revisions_current_uq
    ON lineage.revisions (project_id, scope_id)
    WHERE valid_to IS NULL;
CREATE INDEX IF NOT EXISTS lineage_revisions_scope_validity_idx
    ON lineage.revisions (project_id, scope_id, valid_from DESC, revision_id DESC);
CREATE INDEX IF NOT EXISTS lineage_revisions_graph_idx
    ON lineage.revisions (graph_digest, project_id);

CREATE OR REPLACE FUNCTION lineage.enforce_revision_validity()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, lineage
-- +goose StatementBegin
AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF NEW.project_id IS DISTINCT FROM OLD.project_id
           OR NEW.scope_id IS DISTINCT FROM OLD.scope_id
           OR NEW.revision_id IS DISTINCT FROM OLD.revision_id
           OR NEW.graph_digest IS DISTINCT FROM OLD.graph_digest
           OR NEW.valid_from IS DISTINCT FROM OLD.valid_from
           OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
            RAISE EXCEPTION 'lineage revision identity and valid_from are immutable';
        END IF;
        IF OLD.valid_to IS NOT NULL AND NEW.valid_to IS DISTINCT FROM OLD.valid_to THEN
            RAISE EXCEPTION 'lineage revision validity is immutable after closure';
        END IF;
        IF NEW.valid_to IS NOT NULL AND NEW.valid_to <= NEW.valid_from THEN
            RAISE EXCEPTION 'lineage revision valid_to must be after valid_from';
        END IF;
        IF EXISTS (
            SELECT 1 FROM lineage.revisions r
             WHERE r.project_id = NEW.project_id AND r.scope_id = NEW.scope_id
               AND (r.project_id, r.scope_id, r.revision_id) <> (NEW.project_id, NEW.scope_id, NEW.revision_id)
               AND tstzrange(r.valid_from, COALESCE(r.valid_to, 'infinity'::timestamptz), '[)')
                   && tstzrange(NEW.valid_from, COALESCE(NEW.valid_to, 'infinity'::timestamptz), '[)')
        ) THEN
            RAISE EXCEPTION 'lineage revision validity overlaps existing scope revision';
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'lineage revisions are immutable';
    END IF;
    IF EXISTS (
        SELECT 1 FROM lineage.revisions r
         WHERE r.project_id = NEW.project_id AND r.scope_id = NEW.scope_id
           AND tstzrange(r.valid_from, COALESCE(r.valid_to, 'infinity'::timestamptz), '[)')
               && tstzrange(NEW.valid_from, COALESCE(NEW.valid_to, 'infinity'::timestamptz), '[)')
    ) THEN
        RAISE EXCEPTION 'lineage revision validity overlaps existing scope revision';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS lineage_revisions_validity ON lineage.revisions;
CREATE TRIGGER lineage_revisions_validity
    BEFORE INSERT OR UPDATE OR DELETE ON lineage.revisions
    FOR EACH ROW EXECUTE FUNCTION lineage.enforce_revision_validity();

-- The application runtime is not granted UPDATE on revisions.  Publication
-- executes through this narrowly scoped SECURITY DEFINER function owned by
-- the migration/owner role, preserving atomic replacement without broadening
-- table privileges.  The caller must already have admitted the referenced
-- graph through the normal graph/node/edge inserts in the same transaction.
CREATE OR REPLACE FUNCTION lineage.publish_revision(p_project_id TEXT, p_scope_id TEXT, p_graph_digest TEXT)
RETURNS TABLE(project_id TEXT, scope_id TEXT, revision_id BIGINT, graph_digest TEXT, valid_from TIMESTAMPTZ, valid_to TIMESTAMPTZ, created_at TIMESTAMPTZ)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, lineage
-- +goose StatementBegin
AS $$
DECLARE
    current_row lineage.revisions%ROWTYPE;
    next_revision BIGINT;
BEGIN
    IF p_project_id IS NULL OR p_scope_id IS NULL OR p_graph_digest IS NULL THEN
        RAISE EXCEPTION 'lineage publication identity is required';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended(p_project_id || '|' || p_scope_id, 0));
    SELECT * INTO current_row
      FROM lineage.revisions
     WHERE lineage.revisions.project_id = p_project_id
       AND lineage.revisions.scope_id = p_scope_id
       AND lineage.revisions.valid_to IS NULL;
    IF FOUND AND current_row.graph_digest = p_graph_digest THEN
        project_id := current_row.project_id;
        scope_id := current_row.scope_id;
        revision_id := current_row.revision_id;
        graph_digest := current_row.graph_digest;
        valid_from := current_row.valid_from;
        valid_to := current_row.valid_to;
        created_at := current_row.created_at;
        RETURN NEXT;
        RETURN;
    END IF;
    SELECT COALESCE(MAX(r.revision_id), 0) + 1 INTO next_revision
      FROM lineage.revisions r
     WHERE r.project_id = p_project_id AND r.scope_id = p_scope_id;
    UPDATE lineage.revisions r
       SET valid_to = GREATEST(clock_timestamp(), r.valid_from + interval '1 microsecond')
     WHERE r.project_id = p_project_id AND r.scope_id = p_scope_id AND r.valid_to IS NULL;
    INSERT INTO lineage.revisions (project_id, scope_id, revision_id, graph_digest)
    VALUES (p_project_id, p_scope_id, next_revision, p_graph_digest)
    RETURNING lineage.revisions.project_id, lineage.revisions.scope_id,
              lineage.revisions.revision_id, lineage.revisions.graph_digest,
              lineage.revisions.valid_from, lineage.revisions.valid_to,
              lineage.revisions.created_at
         INTO project_id, scope_id, revision_id, graph_digest,
              valid_from, valid_to, created_at;
    RETURN NEXT;
END;
$$;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS lineage.nodes (
    graph_digest TEXT NOT NULL,
    project_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    resource_kind TEXT NOT NULL,
    identity_digest TEXT NOT NULL,
    properties JSONB NOT NULL,
    PRIMARY KEY (graph_digest, node_id),
    FOREIGN KEY (graph_digest, project_id)
        REFERENCES lineage.graphs(graph_digest, project_id) ON DELETE CASCADE,
    CHECK (node_id = btrim(node_id) AND octet_length(node_id) BETWEEN 1 AND 256),
    CHECK (resource_kind = btrim(resource_kind) AND octet_length(resource_kind) BETWEEN 1 AND 128),
    CHECK (identity_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (jsonb_typeof(properties) = 'object' AND octet_length(properties::text) <= 65536),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 256)
);

CREATE TABLE IF NOT EXISTS lineage.edges (
    graph_digest TEXT NOT NULL,
    project_id TEXT NOT NULL,
    from_node_id TEXT NOT NULL,
    to_node_id TEXT NOT NULL,
    relation TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (graph_digest, from_node_id, to_node_id),
    FOREIGN KEY (graph_digest, project_id)
        REFERENCES lineage.graphs(graph_digest, project_id) ON DELETE CASCADE,
    FOREIGN KEY (graph_digest, from_node_id)
        REFERENCES lineage.nodes(graph_digest, node_id) ON DELETE CASCADE,
    FOREIGN KEY (graph_digest, to_node_id)
        REFERENCES lineage.nodes(graph_digest, node_id) ON DELETE CASCADE,
    CHECK (from_node_id <> to_node_id),
    CHECK (relation = btrim(relation) AND octet_length(relation) <= 128),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 256)
);

-- Traversal functions keep the two fixed recursive result shapes in the
-- capability schema so sqlc can expose typed methods.  The caller still
-- supplies the generation digest and access-authority allow-set; every
-- recursive step joins that allow-set, preventing denied transit nodes.
CREATE OR REPLACE FUNCTION lineage.traverse_upstream(
    p_graph_digest TEXT,
    p_project_id TEXT,
    p_root_id TEXT,
    p_allowed TEXT[],
    p_max_depth INTEGER,
    p_row_limit INTEGER
)
RETURNS TABLE(node_id TEXT, resource_kind TEXT, identity_digest TEXT, properties JSONB, depth INTEGER)
LANGUAGE SQL STABLE
SET search_path = pg_catalog, lineage
AS $function$
WITH RECURSIVE allowed(node_id) AS (SELECT unnest(p_allowed)), walk(node_id, depth) AS (
    SELECT p_root_id, 0
    UNION
    SELECT e.to_node_id, w.depth + 1
    FROM walk w
    JOIN lineage.edges e ON e.graph_digest = p_graph_digest
        AND e.project_id = p_project_id
        AND e.from_node_id = w.node_id
    JOIN allowed a ON a.node_id = e.to_node_id
    WHERE w.depth < p_max_depth
)
SELECT node_id, resource_kind, identity_digest, properties, depth
FROM (
    SELECT DISTINCT ON (n.node_id)
        n.node_id, n.resource_kind, n.identity_digest, n.properties, w.depth
    FROM walk w
    JOIN allowed a ON a.node_id = w.node_id
    JOIN lineage.nodes n ON n.graph_digest = p_graph_digest
        AND n.project_id = p_project_id
        AND n.node_id = w.node_id
    ORDER BY n.node_id, w.depth
) unique_nodes
ORDER BY depth, node_id
LIMIT p_row_limit
$function$;

CREATE OR REPLACE FUNCTION lineage.traverse_downstream(
    p_graph_digest TEXT,
    p_project_id TEXT,
    p_root_id TEXT,
    p_allowed TEXT[],
    p_max_depth INTEGER,
    p_row_limit INTEGER
)
RETURNS TABLE(node_id TEXT, resource_kind TEXT, identity_digest TEXT, properties JSONB, depth INTEGER)
LANGUAGE SQL STABLE
SET search_path = pg_catalog, lineage
AS $function$
WITH RECURSIVE allowed(node_id) AS (SELECT unnest(p_allowed)), walk(node_id, depth) AS (
    SELECT p_root_id, 0
    UNION
    SELECT e.from_node_id, w.depth + 1
    FROM walk w
    JOIN lineage.edges e ON e.graph_digest = p_graph_digest
        AND e.project_id = p_project_id
        AND e.to_node_id = w.node_id
    JOIN allowed a ON a.node_id = e.from_node_id
    WHERE w.depth < p_max_depth
)
SELECT node_id, resource_kind, identity_digest, properties, depth
FROM (
    SELECT DISTINCT ON (n.node_id)
        n.node_id, n.resource_kind, n.identity_digest, n.properties, w.depth
    FROM walk w
    JOIN allowed a ON a.node_id = w.node_id
    JOIN lineage.nodes n ON n.graph_digest = p_graph_digest
        AND n.project_id = p_project_id
        AND n.node_id = w.node_id
    ORDER BY n.node_id, w.depth
) unique_nodes
ORDER BY depth, node_id
LIMIT p_row_limit
$function$;

CREATE OR REPLACE FUNCTION lineage.count_upstream_edges(
    p_graph_digest TEXT,
    p_project_id TEXT,
    p_root_id TEXT,
    p_allowed TEXT[],
    p_max_depth INTEGER
)
RETURNS BIGINT
LANGUAGE SQL STABLE
SET search_path = pg_catalog, lineage
AS $function$
WITH RECURSIVE allowed(node_id) AS (SELECT unnest(p_allowed)), walk(node_id, depth) AS (
    SELECT p_root_id, 0
    UNION
    SELECT e.to_node_id, w.depth + 1
    FROM walk w
    JOIN lineage.edges e ON e.graph_digest = p_graph_digest
        AND e.project_id = p_project_id
        AND e.from_node_id = w.node_id
    JOIN allowed a ON a.node_id = e.to_node_id
    WHERE w.depth < p_max_depth
)
SELECT count(*)
FROM walk w
JOIN lineage.edges e ON e.graph_digest = p_graph_digest
    AND e.project_id = p_project_id
    AND e.from_node_id = w.node_id
JOIN allowed a ON a.node_id = e.to_node_id
WHERE w.depth < p_max_depth
$function$;

CREATE OR REPLACE FUNCTION lineage.count_downstream_edges(
    p_graph_digest TEXT,
    p_project_id TEXT,
    p_root_id TEXT,
    p_allowed TEXT[],
    p_max_depth INTEGER
)
RETURNS BIGINT
LANGUAGE SQL STABLE
SET search_path = pg_catalog, lineage
AS $function$
WITH RECURSIVE allowed(node_id) AS (SELECT unnest(p_allowed)), walk(node_id, depth) AS (
    SELECT p_root_id, 0
    UNION
    SELECT e.from_node_id, w.depth + 1
    FROM walk w
    JOIN lineage.edges e ON e.graph_digest = p_graph_digest
        AND e.project_id = p_project_id
        AND e.to_node_id = w.node_id
    JOIN allowed a ON a.node_id = e.from_node_id
    WHERE w.depth < p_max_depth
)
SELECT count(*)
FROM walk w
JOIN lineage.edges e ON e.graph_digest = p_graph_digest
    AND e.project_id = p_project_id
    AND e.to_node_id = w.node_id
JOIN allowed a ON a.node_id = e.from_node_id
WHERE w.depth < p_max_depth
$function$;

-- A binding is the only serving-facing selector.  Delivery and generation
-- are explicit, immutable identities; no environment or graph metadata is
-- inferred by this capability.
CREATE TABLE IF NOT EXISTS lineage.bindings (
    delivery_id TEXT NOT NULL,
    generation_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    graph_digest TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (delivery_id, generation_id),
    FOREIGN KEY (graph_digest, project_id)
        REFERENCES lineage.graphs(graph_digest, project_id) ON DELETE RESTRICT,
    CHECK (delivery_id = btrim(delivery_id) AND octet_length(delivery_id) BETWEEN 1 AND 256),
    CHECK (generation_id = btrim(generation_id) AND octet_length(generation_id) BETWEEN 1 AND 256),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 256)
);

CREATE INDEX IF NOT EXISTS lineage_edges_from_idx
    ON lineage.edges (graph_digest, from_node_id, to_node_id);
CREATE INDEX IF NOT EXISTS lineage_edges_to_idx
    ON lineage.edges (graph_digest, to_node_id, from_node_id);
CREATE INDEX IF NOT EXISTS lineage_nodes_project_idx
    ON lineage.nodes (project_id, graph_digest, node_id);
CREATE INDEX IF NOT EXISTS lineage_edges_project_from_idx
    ON lineage.edges (project_id, graph_digest, from_node_id, to_node_id);
CREATE INDEX IF NOT EXISTS lineage_edges_project_to_idx
    ON lineage.edges (project_id, graph_digest, to_node_id, from_node_id);
CREATE INDEX IF NOT EXISTS lineage_bindings_graph_idx
    ON lineage.bindings (project_id, graph_digest, delivery_id, generation_id);

CREATE OR REPLACE FUNCTION lineage.reject_immutable_change()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, lineage
-- +goose StatementBegin
AS $$
BEGIN
    RAISE EXCEPTION 'lineage projections and bindings are immutable';
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS lineage_graphs_immutable ON lineage.graphs;
CREATE TRIGGER lineage_graphs_immutable
    BEFORE UPDATE OR DELETE ON lineage.graphs
    FOR EACH ROW EXECUTE FUNCTION lineage.reject_immutable_change();
DROP TRIGGER IF EXISTS lineage_nodes_immutable ON lineage.nodes;
CREATE TRIGGER lineage_nodes_immutable
    BEFORE UPDATE OR DELETE ON lineage.nodes
    FOR EACH ROW EXECUTE FUNCTION lineage.reject_immutable_change();
DROP TRIGGER IF EXISTS lineage_edges_immutable ON lineage.edges;
CREATE TRIGGER lineage_edges_immutable
    BEFORE UPDATE OR DELETE ON lineage.edges
    FOR EACH ROW EXECUTE FUNCTION lineage.reject_immutable_change();
DROP TRIGGER IF EXISTS lineage_bindings_immutable ON lineage.bindings;
CREATE TRIGGER lineage_bindings_immutable
    BEFORE UPDATE OR DELETE ON lineage.bindings
    FOR EACH ROW EXECUTE FUNCTION lineage.reject_immutable_change();

-- PUBLIC must not discover or mutate lineage state. Runtime may admit
-- immutable graph/binding rows and invoke the narrow publication function, but
-- cannot directly mutate revision rows. Conditional grants keep isolated
-- tests usable when deployment roles have not yet been provisioned.
REVOKE ALL ON SCHEMA lineage FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA lineage FROM PUBLIC;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA lineage FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA lineage FROM PUBLIC;
-- +goose StatementBegin
DO $$
DECLARE role_name TEXT;
BEGIN
    FOREACH role_name IN ARRAY ARRAY['leapview_control_owner','leapview_control_migrator','leapview_control_runtime','leapview_control_readonly','leapview_control_backup'] LOOP
        IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = role_name) THEN
            EXECUTE format('GRANT USAGE ON SCHEMA lineage TO %I', role_name);
            EXECUTE format('GRANT EXECUTE ON FUNCTION lineage.traverse_upstream(text,text,text,text[],integer,integer), lineage.traverse_downstream(text,text,text,text[],integer,integer), lineage.count_upstream_edges(text,text,text,text[],integer), lineage.count_downstream_edges(text,text,text,text[],integer) TO %I', role_name);
            IF role_name IN ('leapview_control_owner','leapview_control_migrator') THEN
                EXECUTE format('GRANT ALL ON ALL TABLES IN SCHEMA lineage TO %I', role_name);
                EXECUTE format('GRANT ALL ON ALL SEQUENCES IN SCHEMA lineage TO %I', role_name);
                EXECUTE format('GRANT EXECUTE ON FUNCTION lineage.publish_revision(text,text,text) TO %I', role_name);
            ELSE
                EXECUTE format('GRANT SELECT ON ALL TABLES IN SCHEMA lineage TO %I', role_name);
                IF role_name = 'leapview_control_runtime' THEN
                    EXECUTE format('GRANT INSERT ON lineage.graphs, lineage.nodes, lineage.edges, lineage.bindings TO %I', role_name);
                    EXECUTE format('REVOKE INSERT, UPDATE, DELETE ON lineage.revisions FROM %I', role_name);
                    EXECUTE format('GRANT EXECUTE ON FUNCTION lineage.publish_revision(text,text,text) TO %I', role_name);
                END IF;
            END IF;
        END IF;
    END LOOP;
END
$$;
-- +goose StatementEnd

-- capability source: internal/analytics/queryaudit/postgres/schema.sql
-- Query-audit capability schema (ADR-0020).
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
-- +goose StatementBegin
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
-- +goose StatementEnd

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
-- +goose StatementBegin
AS $$
BEGIN
    IF p_before IS NULL THEN
        RAISE EXCEPTION 'query event prune cutoff is required';
    END IF;
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 1000 THEN
        RAISE EXCEPTION 'query event prune limit must be between 1 and 1000';
    END IF;
	-- Set the append-only exception inside the SECURITY DEFINER body. A
	-- function-level custom GUC requires schema-applier privileges on
	-- PostgreSQL 18 and prevents the clean baseline from being installed by
	-- the bounded migrator role.
	PERFORM set_config('audit.capability', 'maintenance', true);
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
-- +goose StatementEnd

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
-- +goose StatementBegin
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
-- +goose StatementEnd

-- folded forward source: access typed attribute registry
-- FAI-636: typed semantic-access attribute registry.
--
-- This is a forward-only access-capability migration. It must not be folded
-- into schema.sql because revision 1 is already an immutable, recorded
-- baseline. Principal assignments and trusted claim mappings deliberately
-- remain outside this registry migration.

CREATE TABLE access.semantic_attribute_registry (
    singleton          boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    profile            text NOT NULL CHECK (profile = 'leapview.semantic-access/v1'),
    registry_revision  bigint NOT NULL DEFAULT 0 CHECK (registry_revision >= 0),
    registry_digest    text NOT NULL CHECK (registry_digest ~ '^sha256:[0-9a-f]{64}$'),
    updated_at         timestamptz NOT NULL DEFAULT clock_timestamp()
);

INSERT INTO access.semantic_attribute_registry
    (singleton, profile, registry_revision, registry_digest)
VALUES
    (true, 'leapview.semantic-access/v1', 0,
     'sha256:9362dbdb62923a10f67bc1da04b02e2bbad74dce5b5442aaa3fb5e0cc5851b9d')
ON CONFLICT (singleton) DO NOTHING;

CREATE TABLE access.semantic_attribute_definition (
    definition_id      uuid PRIMARY KEY DEFAULT uuidv7(),
    name               text NOT NULL UNIQUE
                       CHECK (name ~ '^[A-Za-z_][A-Za-z0-9_]*$'),
    value_type         text NOT NULL
                       CHECK (value_type IN ('String','Boolean','Integer','Decimal','Date','Timestamp')),
    value_shape        text NOT NULL CHECK (value_shape IN ('scalar','list')),
    profile            text NOT NULL CHECK (profile = 'leapview.semantic-access/v1'),
    definition_version bigint NOT NULL DEFAULT 1 CHECK (definition_version > 0),
    owner_kind         text NOT NULL DEFAULT 'instance'
                       CHECK (owner_kind IN ('instance','principal','group')),
    owner_id           uuid,
    display_name       text NOT NULL DEFAULT '' CHECK (length(display_name) <= 255),
    description        text NOT NULL DEFAULT '' CHECK (length(description) <= 4096),
    documentation_url  text NOT NULL DEFAULT '' CHECK (length(documentation_url) <= 2048),
    enabled            boolean NOT NULL DEFAULT true,
    disabled_at        timestamptz,
    created_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at         timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK ((owner_kind = 'instance' AND owner_id IS NULL) OR
           (owner_kind <> 'instance' AND owner_id IS NOT NULL)),
    CHECK ((enabled AND disabled_at IS NULL) OR (NOT enabled AND disabled_at IS NOT NULL))
);
CREATE INDEX semantic_attribute_definition_owner_idx
    ON access.semantic_attribute_definition(owner_kind, owner_id, name);

CREATE OR REPLACE FUNCTION access.reject_semantic_attribute_registry_rewrite()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, access
-- +goose StatementBegin
AS $$
BEGIN
    IF OLD.singleton <> NEW.singleton OR OLD.profile <> NEW.profile THEN
        RAISE EXCEPTION 'semantic attribute registry identity is immutable';
    END IF;
    IF NEW.registry_revision <> OLD.registry_revision + 1 OR
       NEW.registry_digest = OLD.registry_digest THEN
        RAISE EXCEPTION 'semantic attribute registry revision must advance with a new digest';
    END IF;
    NEW.updated_at := GREATEST(clock_timestamp(), OLD.updated_at + interval '1 microsecond');
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION access.reject_semantic_attribute_definition_rewrite()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, access
-- +goose StatementBegin
AS $$
BEGIN
    IF OLD.definition_id <> NEW.definition_id OR
       OLD.name <> NEW.name OR
       OLD.value_type <> NEW.value_type OR
       OLD.value_shape <> NEW.value_shape OR
       OLD.profile <> NEW.profile OR
       OLD.created_at <> NEW.created_at THEN
        RAISE EXCEPTION 'semantic attribute identity and type are immutable';
    END IF;
    IF NEW.definition_version <> OLD.definition_version + 1 THEN
        RAISE EXCEPTION 'semantic attribute definition version must advance exactly once';
    END IF;
    IF OLD.owner_kind = NEW.owner_kind AND
       OLD.owner_id IS NOT DISTINCT FROM NEW.owner_id AND
       OLD.display_name = NEW.display_name AND
       OLD.description = NEW.description AND
       OLD.documentation_url = NEW.documentation_url AND
       OLD.enabled = NEW.enabled AND
       OLD.disabled_at IS NOT DISTINCT FROM NEW.disabled_at THEN
        RAISE EXCEPTION 'semantic attribute update did not change mutable state';
    END IF;
    IF OLD.enabled = NEW.enabled AND OLD.disabled_at IS DISTINCT FROM NEW.disabled_at THEN
        RAISE EXCEPTION 'semantic attribute disable timestamp is database-owned';
    END IF;
    IF OLD.enabled <> NEW.enabled THEN
        NEW.disabled_at := CASE WHEN NEW.enabled THEN NULL ELSE clock_timestamp() END;
    END IF;
    NEW.updated_at := GREATEST(clock_timestamp(), OLD.updated_at + interval '1 microsecond');
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER semantic_attribute_registry_no_delete
    BEFORE DELETE ON access.semantic_attribute_registry
    FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER semantic_attribute_registry_immutable
    BEFORE UPDATE ON access.semantic_attribute_registry
    FOR EACH ROW EXECUTE FUNCTION access.reject_semantic_attribute_registry_rewrite();
CREATE TRIGGER semantic_attribute_definition_no_delete
    BEFORE DELETE ON access.semantic_attribute_definition
    FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER semantic_attribute_definition_immutable
    BEFORE UPDATE ON access.semantic_attribute_definition
    FOR EACH ROW EXECUTE FUNCTION access.reject_semantic_attribute_definition_rewrite();

REVOKE ALL ON TABLE access.semantic_attribute_registry,
    access.semantic_attribute_definition FROM PUBLIC;
REVOKE ALL ON FUNCTION access.reject_semantic_attribute_registry_rewrite(),
    access.reject_semantic_attribute_definition_rewrite() FROM PUBLIC;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT SELECT, INSERT, UPDATE ON access.semantic_attribute_registry,
            access.semantic_attribute_definition TO leapview_control_runtime;
        REVOKE DELETE, TRUNCATE, REFERENCES, TRIGGER ON access.semantic_attribute_registry,
            access.semantic_attribute_definition FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT SELECT ON access.semantic_attribute_registry,
            access.semantic_attribute_definition TO leapview_control_readonly;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON access.semantic_attribute_registry,
               access.semantic_attribute_definition FROM leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT SELECT ON access.semantic_attribute_registry,
            access.semantic_attribute_definition TO leapview_control_backup;
    END IF;
END
$$;
-- +goose StatementEnd

-- folded forward source: access semantic attribute control
-- FAI-637: durable semantic-access assignments and trusted claim mappings.
--
-- Revision 002 is intentionally not edited.  This migration owns only the
-- control-plane rows which reference the immutable definition registry.

CREATE TABLE access.semantic_attribute_control_state (
    singleton         boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    profile           text NOT NULL CHECK (profile = 'leapview.semantic-access/v1'),
    control_revision  bigint NOT NULL DEFAULT 0 CHECK (control_revision >= 0),
    control_digest    text NOT NULL CHECK (control_digest ~ '^sha256:[0-9a-f]{64}$'),
    updated_at        timestamptz NOT NULL DEFAULT clock_timestamp()
);

INSERT INTO access.semantic_attribute_control_state
    (singleton, profile, control_revision, control_digest)
VALUES
    (true, 'leapview.semantic-access/v1', 0,
     'sha256:e05005cdeee20cc98d9e8de8f32ed4b8da34a95f82872dc3b65a451ce7de4e37')
ON CONFLICT (singleton) DO NOTHING;

-- A row is one assignment incarnation.  Tombstones remain in place forever;
-- restoring a subject/definition pair creates a new immutable assignment id.
CREATE TABLE access.semantic_attribute_assignment (
    assignment_id       uuid PRIMARY KEY DEFAULT uuidv7(),
    definition_id       uuid NOT NULL REFERENCES access.semantic_attribute_definition(definition_id),
    subject_kind        text NOT NULL CHECK (subject_kind IN ('principal','group')),
    subject_id          uuid NOT NULL,
    definition_version  bigint NOT NULL CHECK (definition_version > 0),
    value_type          text NOT NULL CHECK (value_type IN ('String','Boolean','Integer','Decimal','Date','Timestamp')),
    value_shape         text NOT NULL CHECK (value_shape IN ('scalar','list')),
    canonical_values    text[] NOT NULL,
    value_digest        text NOT NULL CHECK (value_digest ~ '^sha256:[0-9a-f]{64}$'),
    assignment_version  bigint NOT NULL DEFAULT 1 CHECK (assignment_version > 0),
    tombstoned_at       timestamptz,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (array_ndims(canonical_values) = 1),
    CHECK (cardinality(canonical_values) BETWEEN 1 AND 1024),
    CHECK ((value_shape = 'scalar' AND cardinality(canonical_values) = 1) OR value_shape = 'list')
);
CREATE UNIQUE INDEX semantic_attribute_assignment_active_key
    ON access.semantic_attribute_assignment(definition_id, subject_kind, subject_id)
    WHERE tombstoned_at IS NULL;
CREATE INDEX semantic_attribute_assignment_subject_idx
    ON access.semantic_attribute_assignment(subject_kind, subject_id, definition_id, assignment_id);

-- A mapping has no value payload: it names the trusted provider claim which
-- will be canonicalized at authentication/evaluation time.
CREATE TABLE access.semantic_attribute_claim_mapping (
    mapping_id          uuid PRIMARY KEY DEFAULT uuidv7(),
    source_kind        text NOT NULL CHECK (source_kind IN ('saml','oidc','embed','service_token')),
    provider            text NOT NULL CHECK (provider = btrim(provider) AND octet_length(provider) BETWEEN 1 AND 128 AND provider !~ '[[:cntrl:]]'),
    issuer              text NOT NULL CHECK (issuer = btrim(issuer) AND octet_length(issuer) BETWEEN 1 AND 1024 AND issuer !~ '[[:cntrl:]]'),
    audience            text NOT NULL CHECK (audience = btrim(audience) AND octet_length(audience) BETWEEN 1 AND 512 AND audience !~ '[[:cntrl:]]'),
    claim               text NOT NULL CHECK (claim = btrim(claim) AND octet_length(claim) BETWEEN 1 AND 1024 AND claim !~ '[[:cntrl:]]'),
    definition_id       uuid NOT NULL REFERENCES access.semantic_attribute_definition(definition_id),
    definition_version  bigint NOT NULL CHECK (definition_version > 0),
    value_type          text NOT NULL CHECK (value_type IN ('String','Boolean','Integer','Decimal','Date','Timestamp')),
    value_shape         text NOT NULL CHECK (value_shape IN ('scalar','list')),
    mapping_version     bigint NOT NULL DEFAULT 1 CHECK (mapping_version > 0),
    tombstoned_at       timestamptz,
    created_at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at          timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE UNIQUE INDEX semantic_attribute_claim_mapping_active_key
    ON access.semantic_attribute_claim_mapping(source_kind, provider, issuer, audience, claim, definition_id)
    WHERE tombstoned_at IS NULL;
CREATE INDEX semantic_attribute_claim_mapping_lookup_idx
    ON access.semantic_attribute_claim_mapping(source_kind, provider, issuer, audience, claim, mapping_id);

CREATE OR REPLACE FUNCTION access.validate_semantic_attribute_owner_exists()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, access
-- +goose StatementBegin
AS $$
BEGIN
    -- An already-owned definition may still be edited after its owner is
    -- revoked. Requiring the owner to remain active on every metadata update
    -- would strand the definition and prevent its lifecycle from completing.
    IF TG_OP = 'UPDATE' AND OLD.owner_kind = NEW.owner_kind AND OLD.owner_id IS NOT DISTINCT FROM NEW.owner_id THEN
        RETURN NEW;
    END IF;
    IF NEW.owner_kind = 'principal' AND NOT EXISTS (
        SELECT 1 FROM access.principal WHERE id = NEW.owner_id AND revoked_at IS NULL
    ) THEN
        RAISE EXCEPTION 'semantic attribute owner principal does not exist';
    ELSIF NEW.owner_kind = 'group' AND NOT EXISTS (
        SELECT 1 FROM access.access_group WHERE id = NEW.owner_id AND revoked_at IS NULL
    ) THEN
        RAISE EXCEPTION 'semantic attribute owner group does not exist';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER semantic_attribute_definition_owner_exists
    BEFORE INSERT OR UPDATE ON access.semantic_attribute_definition
    FOR EACH ROW EXECUTE FUNCTION access.validate_semantic_attribute_owner_exists();

CREATE OR REPLACE FUNCTION access.validate_semantic_attribute_assignment()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, access
-- +goose StatementBegin
AS $$
DECLARE definition_type text; definition_shape text; definition_enabled boolean; tombstone_transition boolean := false;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        tombstone_transition := OLD.tombstoned_at IS NULL AND NEW.tombstoned_at IS NOT NULL;
    END IF;
    SELECT value_type, value_shape, enabled
      INTO definition_type, definition_shape, definition_enabled
      FROM access.semantic_attribute_definition
     WHERE definition_id = NEW.definition_id;
    IF NOT FOUND OR (NOT definition_enabled AND NOT tombstone_transition) THEN
        RAISE EXCEPTION 'semantic attribute definition is missing or disabled';
    END IF;
    IF definition_type <> NEW.value_type OR definition_shape <> NEW.value_shape THEN
        RAISE EXCEPTION 'semantic attribute assignment type is not the definition type';
    END IF;
    IF TG_OP = 'INSERT' OR NOT tombstone_transition THEN
        IF NEW.subject_kind = 'principal' AND NOT EXISTS (
            SELECT 1 FROM access.principal WHERE id = NEW.subject_id AND revoked_at IS NULL
        ) THEN
            RAISE EXCEPTION 'semantic attribute assignment principal does not exist';
        ELSIF NEW.subject_kind = 'group' AND NOT EXISTS (
            SELECT 1 FROM access.access_group WHERE id = NEW.subject_id AND revoked_at IS NULL
        ) THEN
            RAISE EXCEPTION 'semantic attribute assignment group does not exist';
        END IF;
    END IF;
    IF TG_OP = 'UPDATE' THEN
        IF OLD.assignment_id <> NEW.assignment_id OR OLD.definition_id <> NEW.definition_id OR
           OLD.subject_kind <> NEW.subject_kind OR OLD.subject_id <> NEW.subject_id OR
           OLD.definition_version <> NEW.definition_version OR OLD.value_type <> NEW.value_type OR
           OLD.value_shape <> NEW.value_shape OR OLD.created_at <> NEW.created_at THEN
            RAISE EXCEPTION 'semantic attribute assignment identity and type are immutable';
        END IF;
        IF OLD.tombstoned_at IS NOT NULL AND NEW.tombstoned_at IS NULL THEN
            RAISE EXCEPTION 'semantic attribute assignment tombstone is immutable';
        END IF;
        IF OLD.tombstoned_at IS NOT NULL THEN
            RAISE EXCEPTION 'semantic attribute assignment tombstone is immutable';
        END IF;
        IF OLD.tombstoned_at IS NULL AND NEW.tombstoned_at IS NOT NULL AND
           (OLD.canonical_values IS DISTINCT FROM NEW.canonical_values OR OLD.value_digest <> NEW.value_digest) THEN
            RAISE EXCEPTION 'semantic attribute assignment tombstone cannot rewrite its value';
        END IF;
        IF NEW.assignment_version <> OLD.assignment_version + 1 THEN
            RAISE EXCEPTION 'semantic attribute assignment version must advance exactly once';
        END IF;
        IF OLD.canonical_values = NEW.canonical_values AND OLD.value_digest = NEW.value_digest AND
           OLD.tombstoned_at IS NOT DISTINCT FROM NEW.tombstoned_at THEN
            RAISE EXCEPTION 'semantic attribute assignment update did not change mutable state';
        END IF;
        IF OLD.tombstoned_at IS DISTINCT FROM NEW.tombstoned_at AND NEW.tombstoned_at IS NULL THEN
            RAISE EXCEPTION 'semantic attribute assignment tombstone is database-owned';
        END IF;
    END IF;
    NEW.updated_at := CASE WHEN TG_OP = 'UPDATE'
        THEN GREATEST(clock_timestamp(), OLD.updated_at + interval '1 microsecond')
        ELSE clock_timestamp() END;
    IF TG_OP = 'UPDATE' AND OLD.tombstoned_at IS NULL AND NEW.tombstoned_at IS NOT NULL THEN
        NEW.tombstoned_at := clock_timestamp();
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION access.validate_semantic_attribute_claim_mapping()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, access
-- +goose StatementBegin
AS $$
DECLARE definition_type text; definition_shape text; definition_enabled boolean; tombstone_transition boolean := false;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        tombstone_transition := OLD.tombstoned_at IS NULL AND NEW.tombstoned_at IS NOT NULL;
    END IF;
    SELECT value_type, value_shape, enabled
      INTO definition_type, definition_shape, definition_enabled
      FROM access.semantic_attribute_definition
     WHERE definition_id = NEW.definition_id;
    IF NOT FOUND OR (NOT definition_enabled AND NOT tombstone_transition) THEN
        RAISE EXCEPTION 'semantic attribute mapping definition is missing or disabled';
    END IF;
    IF definition_type <> NEW.value_type OR definition_shape <> NEW.value_shape THEN
        RAISE EXCEPTION 'semantic attribute mapping type is not the definition type';
    END IF;
    IF TG_OP = 'UPDATE' THEN
        IF OLD.mapping_id <> NEW.mapping_id OR OLD.source_kind <> NEW.source_kind OR
           OLD.provider <> NEW.provider OR OLD.issuer <> NEW.issuer OR OLD.audience <> NEW.audience OR
           OLD.claim <> NEW.claim OR OLD.definition_id <> NEW.definition_id OR
           OLD.definition_version <> NEW.definition_version OR
           OLD.value_type <> NEW.value_type OR OLD.value_shape <> NEW.value_shape OR OLD.created_at <> NEW.created_at THEN
            RAISE EXCEPTION 'semantic attribute mapping identity and type are immutable';
        END IF;
        IF OLD.tombstoned_at IS NOT NULL AND NEW.tombstoned_at IS NULL THEN
            RAISE EXCEPTION 'semantic attribute mapping tombstone is immutable';
        END IF;
        IF OLD.tombstoned_at IS NOT NULL THEN
            RAISE EXCEPTION 'semantic attribute mapping tombstone is immutable';
        END IF;
        IF NEW.mapping_version <> OLD.mapping_version + 1 THEN
            RAISE EXCEPTION 'semantic attribute mapping version must advance exactly once';
        END IF;
        IF OLD.tombstoned_at IS NOT DISTINCT FROM NEW.tombstoned_at THEN
            RAISE EXCEPTION 'semantic attribute mapping update did not change mutable state';
        END IF;
    END IF;
    NEW.updated_at := CASE WHEN TG_OP = 'UPDATE'
        THEN GREATEST(clock_timestamp(), OLD.updated_at + interval '1 microsecond')
        ELSE clock_timestamp() END;
    IF TG_OP = 'UPDATE' AND OLD.tombstoned_at IS NULL AND NEW.tombstoned_at IS NOT NULL THEN
        NEW.tombstoned_at := clock_timestamp();
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION access.reject_semantic_attribute_control_state_rewrite()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, access
-- +goose StatementBegin
AS $$
BEGIN
    IF OLD.singleton <> NEW.singleton OR OLD.profile <> NEW.profile OR
       NEW.control_revision <> OLD.control_revision + 1 OR NEW.control_digest = OLD.control_digest THEN
        RAISE EXCEPTION 'semantic attribute control revision must advance with a new digest';
    END IF;
    NEW.updated_at := GREATEST(clock_timestamp(), OLD.updated_at + interval '1 microsecond');
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER semantic_attribute_control_state_no_delete
    BEFORE DELETE ON access.semantic_attribute_control_state
    FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER semantic_attribute_control_state_immutable
    BEFORE UPDATE ON access.semantic_attribute_control_state
    FOR EACH ROW EXECUTE FUNCTION access.reject_semantic_attribute_control_state_rewrite();
CREATE TRIGGER semantic_attribute_assignment_no_delete
    BEFORE DELETE ON access.semantic_attribute_assignment
    FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER semantic_attribute_assignment_immutable
    BEFORE INSERT OR UPDATE ON access.semantic_attribute_assignment
    FOR EACH ROW EXECUTE FUNCTION access.validate_semantic_attribute_assignment();
CREATE TRIGGER semantic_attribute_claim_mapping_no_delete
    BEFORE DELETE ON access.semantic_attribute_claim_mapping
    FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER semantic_attribute_claim_mapping_immutable
    BEFORE INSERT OR UPDATE ON access.semantic_attribute_claim_mapping
    FOR EACH ROW EXECUTE FUNCTION access.validate_semantic_attribute_claim_mapping();

REVOKE ALL ON TABLE access.semantic_attribute_control_state,
    access.semantic_attribute_assignment,
    access.semantic_attribute_claim_mapping FROM PUBLIC;
REVOKE ALL ON FUNCTION access.validate_semantic_attribute_owner_exists(),
    access.validate_semantic_attribute_assignment(),
    access.validate_semantic_attribute_claim_mapping(),
    access.reject_semantic_attribute_control_state_rewrite() FROM PUBLIC;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT SELECT, INSERT, UPDATE ON access.semantic_attribute_control_state,
            access.semantic_attribute_assignment,
            access.semantic_attribute_claim_mapping TO leapview_control_runtime;
        REVOKE DELETE, TRUNCATE, REFERENCES, TRIGGER ON access.semantic_attribute_control_state,
            access.semantic_attribute_assignment,
            access.semantic_attribute_claim_mapping FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT SELECT ON access.semantic_attribute_control_state,
            access.semantic_attribute_assignment,
            access.semantic_attribute_claim_mapping TO leapview_control_readonly;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON access.semantic_attribute_control_state,
               access.semantic_attribute_assignment,
               access.semantic_attribute_claim_mapping FROM leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT SELECT ON access.semantic_attribute_control_state,
            access.semantic_attribute_assignment,
            access.semantic_attribute_claim_mapping TO leapview_control_backup;
    END IF;
END
$$;
-- +goose StatementEnd

-- Return to the migrator login before Goose records version 1 in its standard
-- table, which is intentionally owned by that login rather than the durable
-- product-object owner role.
RESET ROLE;
