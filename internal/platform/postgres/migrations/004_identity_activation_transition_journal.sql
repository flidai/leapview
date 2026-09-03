-- FAI-617: PostgreSQL-owned, instance-qualified activation transition journal.
-- Immutable input/evidence is fenced separately from forward progress.

SET ROLE leapview_control_owner;

CREATE TABLE IF NOT EXISTS project.identity_activation_transition (
    instance_id            platform.resource_id NOT NULL,
    transition_id          platform.resource_id NOT NULL,
    operation              text NOT NULL CHECK (operation IN ('publish', 'rollback')),
    candidate_id           platform.resource_id NOT NULL,
    bundle_id              platform.resource_id NOT NULL,
    expected_bundle_id     text NOT NULL CHECK (length(expected_bundle_id) <= 255),
    actor_id               platform.resource_id NOT NULL,
    reason                 text NOT NULL CHECK (length(reason) <= 2048),
    authored_resources_json text NOT NULL CHECK (
        octet_length(authored_resources_json) BETWEEN 2 AND 16777216
        AND jsonb_typeof(authored_resources_json::jsonb) = 'array'
    ),
    graph_digest           text NOT NULL CHECK (
        length(graph_digest) = 71
        AND substr(graph_digest, 1, 7) = 'sha256:'
        AND substr(graph_digest, 8) !~ '[^0-9a-f]'
    ),
    phase                  text NOT NULL DEFAULT 'prepared' CHECK (
        phase IN ('prepared', 'identity_pending', 'identity_active', 'delivery_active', 'completed')
    ),
    phase_error            text NOT NULL DEFAULT '' CHECK (length(phase_error) <= 4096),
    prepared_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    phase_at               timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at             timestamptz NOT NULL DEFAULT clock_timestamp(),
    completed_at           timestamptz,
    PRIMARY KEY (instance_id, transition_id)
);

CREATE OR REPLACE FUNCTION project.identity_activation_transition_guard()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.phase = 'completed' AND NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'completed activation transition is immutable';
    END IF;

    IF NEW.transition_id IS DISTINCT FROM OLD.transition_id
        OR NEW.operation IS DISTINCT FROM OLD.operation
        OR NEW.instance_id IS DISTINCT FROM OLD.instance_id
        OR NEW.candidate_id IS DISTINCT FROM OLD.candidate_id
        OR NEW.bundle_id IS DISTINCT FROM OLD.bundle_id
        OR NEW.expected_bundle_id IS DISTINCT FROM OLD.expected_bundle_id
        OR NEW.actor_id IS DISTINCT FROM OLD.actor_id
        OR NEW.reason IS DISTINCT FROM OLD.reason
        OR NEW.authored_resources_json IS DISTINCT FROM OLD.authored_resources_json
        OR NEW.graph_digest IS DISTINCT FROM OLD.graph_digest
        OR NEW.prepared_at IS DISTINCT FROM OLD.prepared_at THEN
        RAISE EXCEPTION 'activation transition parameters and evidence are immutable';
    END IF;

    IF NEW.completed_at IS DISTINCT FROM OLD.completed_at
        AND NEW.phase <> 'completed' THEN
        RAISE EXCEPTION 'completed_at is only valid for terminal phases';
    END IF;
    IF OLD.completed_at IS NOT NULL AND NEW.completed_at IS NULL THEN
        RAISE EXCEPTION 'completed_at cannot be cleared';
    END IF;
    IF NEW.phase = 'completed' AND NEW.completed_at IS NULL THEN
        RAISE EXCEPTION 'terminal activation transition requires completed_at';
    END IF;

    IF NEW.phase IS DISTINCT FROM OLD.phase AND NOT (
        (OLD.phase = 'prepared' AND NEW.phase = 'identity_pending')
        OR (OLD.phase = 'identity_pending' AND NEW.phase = 'identity_active')
        OR (OLD.phase = 'identity_active' AND NEW.phase = 'delivery_active')
        OR (OLD.phase = 'delivery_active' AND NEW.phase = 'completed')
    ) THEN
        RAISE EXCEPTION 'activation transition phase must move forward';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS identity_activation_transition_guard ON project.identity_activation_transition;
CREATE TRIGGER identity_activation_transition_guard
    BEFORE UPDATE ON project.identity_activation_transition
    FOR EACH ROW EXECUTE FUNCTION project.identity_activation_transition_guard();

CREATE OR REPLACE FUNCTION project.identity_activation_transition_no_delete()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'activation transition journal is append-only';
END;
$$;

DROP TRIGGER IF EXISTS identity_activation_transition_no_delete ON project.identity_activation_transition;
CREATE TRIGGER identity_activation_transition_no_delete
    BEFORE DELETE ON project.identity_activation_transition
    FOR EACH ROW EXECUTE FUNCTION project.identity_activation_transition_no_delete();

DROP TRIGGER IF EXISTS identity_activation_transition_no_truncate ON project.identity_activation_transition;
CREATE TRIGGER identity_activation_transition_no_truncate
    BEFORE TRUNCATE ON project.identity_activation_transition
    FOR EACH STATEMENT EXECUTE FUNCTION project.identity_activation_transition_no_delete();

CREATE INDEX IF NOT EXISTS identity_activation_transition_nonterminal_idx
    ON project.identity_activation_transition (instance_id, updated_at)
    WHERE phase <> 'completed';

-- Multiple ready candidates may be durably prepared, but only one transition
-- may own the identity/delivery cutover for an instance at a time.
CREATE UNIQUE INDEX IF NOT EXISTS identity_activation_transition_cutover_owner_idx
    ON project.identity_activation_transition (instance_id)
    WHERE phase IN ('identity_pending', 'identity_active', 'delivery_active');

-- A serving generation is authored once. Rollback transitions may target the
-- same historical bundle more than once, so only publish evidence is unique.
CREATE UNIQUE INDEX IF NOT EXISTS identity_activation_transition_publish_bundle_idx
    ON project.identity_activation_transition (instance_id, bundle_id)
    WHERE operation = 'publish';

GRANT SELECT, INSERT, UPDATE ON project.identity_activation_transition TO leapview_control_runtime;
GRANT SELECT ON project.identity_activation_transition TO leapview_control_readonly;
REVOKE DELETE, TRUNCATE ON project.identity_activation_transition FROM leapview_control_runtime, leapview_control_readonly;
REVOKE ALL ON FUNCTION project.identity_activation_transition_guard() FROM PUBLIC;
REVOKE ALL ON FUNCTION project.identity_activation_transition_no_delete() FROM PUBLIC;

RESET ROLE;
