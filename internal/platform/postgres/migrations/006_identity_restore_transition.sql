-- FAI-663: make explicit identity restore a distinct, durably journaled
-- transition operation.  Earlier migrations remain immutable.

SET ROLE leapview_control_owner;

ALTER TABLE project.identity_activation_transition
    ADD COLUMN IF NOT EXISTS approved_authored_ids_json text;

UPDATE project.identity_activation_transition
SET approved_authored_ids_json = '[]'
WHERE approved_authored_ids_json IS NULL;

ALTER TABLE project.identity_activation_transition
    ALTER COLUMN approved_authored_ids_json SET NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'identity_activation_transition_approved_authored_ids_json_check'
          AND conrelid = 'project.identity_activation_transition'::regclass
    ) THEN
        ALTER TABLE project.identity_activation_transition
            ADD CONSTRAINT identity_activation_transition_approved_authored_ids_json_check
            CHECK (
                octet_length(approved_authored_ids_json) BETWEEN 2 AND 16777216
                AND jsonb_typeof(approved_authored_ids_json::jsonb) = 'array'
            );
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION project.identity_restore_authored_ids_canonical(value text)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE
AS $$
DECLARE
    document    jsonb;
    item        jsonb;
    authored_id text;
    previous_id text := '';
    first_id    boolean := true;
BEGIN
    BEGIN
        document := value::jsonb;
    EXCEPTION WHEN others THEN
        RETURN false;
    END;
    IF jsonb_typeof(document) <> 'array' THEN
        RETURN false;
    END IF;
    FOR item IN SELECT element FROM jsonb_array_elements(document) AS elements(element) LOOP
        IF jsonb_typeof(item) <> 'string' THEN
            RETURN false;
        END IF;
        authored_id := item #>> '{}';
        IF authored_id !~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$' OR length(authored_id) > 255 THEN
            RETURN false;
        END IF;
        -- Match Go's bytewise string ordering regardless of the database's
        -- configured locale. Authored IDs are constrained to ASCII above.
        IF NOT first_id AND authored_id COLLATE "C" <= previous_id COLLATE "C" THEN
            RETURN false;
        END IF;
        previous_id := authored_id;
        first_id := false;
    END LOOP;
    RETURN true;
END;
$$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'identity_activation_transition_approved_authored_ids_canonical_check'
          AND conrelid = 'project.identity_activation_transition'::regclass
    ) THEN
        ALTER TABLE project.identity_activation_transition
            ADD CONSTRAINT identity_activation_transition_approved_authored_ids_canonical_check
            CHECK (project.identity_restore_authored_ids_canonical(approved_authored_ids_json));
    END IF;
END;
$$;

-- Existing revision four used a column-level operation check. Replace it with
-- the expanded operation set while retaining a stable constraint name for
-- future migration checks and retries.
ALTER TABLE project.identity_activation_transition
    DROP CONSTRAINT IF EXISTS identity_activation_transition_operation_check;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'identity_activation_transition_operation_check'
          AND conrelid = 'project.identity_activation_transition'::regclass
    ) THEN
        ALTER TABLE project.identity_activation_transition
            ADD CONSTRAINT identity_activation_transition_operation_check
            CHECK (operation IN ('publish', 'rollback', 'restore'));
    END IF;
END;
$$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'identity_activation_transition_restore_ids_check'
          AND conrelid = 'project.identity_activation_transition'::regclass
    ) THEN
        ALTER TABLE project.identity_activation_transition
            ADD CONSTRAINT identity_activation_transition_restore_ids_check
            CHECK (
                (operation = 'restore' AND jsonb_array_length(approved_authored_ids_json::jsonb) > 0)
                OR (operation IN ('publish', 'rollback') AND jsonb_array_length(approved_authored_ids_json::jsonb) = 0)
            );
    END IF;
END;
$$;

-- A restore is a publication of a new bundle and must have the same
-- one-transition-per-bundle fence as an ordinary publish. Rollbacks remain
-- reusable historical operations and are intentionally excluded.
DROP INDEX IF EXISTS project.identity_activation_transition_publish_bundle_idx;
CREATE UNIQUE INDEX IF NOT EXISTS identity_activation_transition_publish_bundle_idx
    ON project.identity_activation_transition (instance_id, bundle_id)
    WHERE operation IN ('publish', 'restore');

-- Preserve the revision-five prepared-only insert fence and extend immutable
-- evidence protection to the approved restore IDs.
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
        OR NEW.approved_authored_ids_json IS DISTINCT FROM OLD.approved_authored_ids_json
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

GRANT EXECUTE ON FUNCTION project.identity_restore_authored_ids_canonical(text) TO leapview_control_runtime;
REVOKE ALL ON FUNCTION project.identity_restore_authored_ids_canonical(text) FROM PUBLIC;

RESET ROLE;
