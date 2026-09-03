-- FAI-617: add the immutable durable-reference evidence to the activation
-- transition journal without rewriting the already-applied revision four.

SET ROLE leapview_control_owner;

-- Revision four predates durable reference evidence.  Add the column in a
-- nullable state so existing rows can be backfilled before enforcing the
-- immutable persistence contract for all future writes.
ALTER TABLE project.identity_activation_transition
    ADD COLUMN IF NOT EXISTS durable_references_json text;

UPDATE project.identity_activation_transition
SET durable_references_json = '[]'
WHERE durable_references_json IS NULL;

ALTER TABLE project.identity_activation_transition
    ALTER COLUMN durable_references_json SET NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'identity_activation_transition_durable_references_json_check'
          AND conrelid = 'project.identity_activation_transition'::regclass
    ) THEN
        ALTER TABLE project.identity_activation_transition
            ADD CONSTRAINT identity_activation_transition_durable_references_json_check
            CHECK (
                octet_length(durable_references_json) BETWEEN 2 AND 16777216
                AND jsonb_typeof(durable_references_json::jsonb) = 'array'
            );
    END IF;
END;
$$;

-- Keep immutable evidence fenced with the existing journal guard.  Inserts
-- are legal only in the prepared state; all progress thereafter must use the
-- compare-and-set repository path.
CREATE OR REPLACE FUNCTION project.identity_activation_transition_guard()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.phase <> 'prepared' OR NEW.phase_error <> '' OR NEW.completed_at IS NOT NULL THEN
            RAISE EXCEPTION 'activation transition must be inserted in prepared state';
        END IF;
        RETURN NEW;
    END IF;

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
        OR NEW.durable_references_json IS DISTINCT FROM OLD.durable_references_json
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
    BEFORE INSERT OR UPDATE ON project.identity_activation_transition
    FOR EACH ROW EXECUTE FUNCTION project.identity_activation_transition_guard();

RESET ROLE;
