-- +goose Up
SET LOCAL ROLE leapview_control_owner;

ALTER TABLE credential.activation_preparation
    ADD COLUMN switching_at timestamptz,
    DROP CONSTRAINT activation_preparation_abort_state_check,
    ADD CONSTRAINT activation_preparation_switching_abort_state_check CHECK (
        switching_at IS NULL OR (
            switching_at > '-infinity'::timestamptz AND isfinite(switching_at) AND switching_at >= created_at
        )
    ),
    ADD CONSTRAINT activation_preparation_abort_state_check CHECK (
        (aborted_at IS NULL AND aborted_by IS NULL) OR (
            aborted_at IS NOT NULL AND aborted_by IS NOT NULL
            AND aborted_at > '-infinity'::timestamptz AND isfinite(aborted_at)
            AND aborted_at >= COALESCE(switching_at, created_at)
            AND aborted_by = btrim(aborted_by) AND octet_length(aborted_by) BETWEEN 1 AND 255
            AND aborted_by !~ '[[:cntrl:]]'
        )
    );

DROP TRIGGER activation_preparation_abort_guard ON credential.activation_preparation;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION credential.guard_activation_preparation_transition() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.switching_at IS NOT NULL OR NEW.aborted_at IS NOT NULL OR NEW.aborted_by IS NOT NULL
           OR NOT EXISTS (
                SELECT 1 FROM credential.validation_receipt AS receipt
                WHERE receipt.deployment_id = NEW.deployment_id
                  AND receipt.receipt_id = NEW.receipt_id
                  AND receipt.target_id = NEW.deployment_id
           ) THEN
            RAISE EXCEPTION 'credential activation preparation must be active and target-bound when inserted';
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'credential activation preparations cannot be deleted';
    END IF;
    IF NEW.operation_id IS DISTINCT FROM OLD.operation_id
       OR NEW.deployment_id IS DISTINCT FROM OLD.deployment_id
       OR NEW.receipt_id IS DISTINCT FROM OLD.receipt_id
       OR NEW.expected_target_revision IS DISTINCT FROM OLD.expected_target_revision
       OR NEW.predecessor_generation_id IS DISTINCT FROM OLD.predecessor_generation_id
       OR NEW.candidate_id IS DISTINCT FROM OLD.candidate_id
       OR NEW.generation_id IS DISTINCT FROM OLD.generation_id
       OR NEW.publication_id IS DISTINCT FROM OLD.publication_id
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'credential activation preparation intent is immutable';
    END IF;
    IF OLD.aborted_at IS NOT NULL OR OLD.aborted_by IS NOT NULL THEN
        RAISE EXCEPTION 'credential activation preparation is already complete';
    END IF;
    IF NEW.aborted_at IS NULL AND NEW.aborted_by IS NULL
       AND OLD.switching_at IS NULL AND NEW.switching_at IS NOT NULL
       AND NEW.switching_at >= OLD.created_at AND isfinite(NEW.switching_at) THEN
        RETURN NEW;
    END IF;
    IF NEW.aborted_at IS NOT NULL AND NEW.aborted_by IS NOT NULL
       AND NEW.switching_at IS NOT DISTINCT FROM OLD.switching_at
       AND NEW.aborted_at >= COALESCE(OLD.switching_at, OLD.created_at)
       AND isfinite(NEW.aborted_at)
       AND NEW.aborted_by = btrim(NEW.aborted_by)
       AND octet_length(NEW.aborted_by) BETWEEN 1 AND 255
       AND NEW.aborted_by !~ '[[:cntrl:]]' THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'credential activation preparation transition is invalid';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER activation_preparation_transition_guard
    BEFORE INSERT OR UPDATE OR DELETE ON credential.activation_preparation
    FOR EACH ROW EXECUTE FUNCTION credential.guard_activation_preparation_transition();

REVOKE ALL ON FUNCTION credential.guard_activation_preparation_abort() FROM PUBLIC;
REVOKE ALL ON FUNCTION credential.guard_activation_preparation_transition() FROM PUBLIC;
DROP FUNCTION credential.guard_activation_preparation_abort();
GRANT UPDATE (switching_at) ON credential.activation_preparation TO leapview_control_runtime;

RESET ROLE;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'credential activation switching records are forward-only; restore a coordinated backup to downgrade';
END $$;
-- +goose StatementEnd
