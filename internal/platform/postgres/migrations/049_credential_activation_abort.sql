-- +goose Up
SET LOCAL ROLE leapview_control_owner;

ALTER TABLE credential.activation_preparation
    DROP CONSTRAINT activation_preparation_deployment_id_key,
    ADD COLUMN aborted_at timestamptz,
    ADD COLUMN aborted_by text,
    ADD CONSTRAINT activation_preparation_abort_state_check CHECK (
        (aborted_at IS NULL AND aborted_by IS NULL) OR (
            aborted_at IS NOT NULL AND aborted_by IS NOT NULL
            AND aborted_at > '-infinity'::timestamptz AND isfinite(aborted_at) AND aborted_at >= created_at
            AND aborted_by = btrim(aborted_by) AND octet_length(aborted_by) BETWEEN 1 AND 255
            AND aborted_by !~ '[[:cntrl:]]'
        )
    );

CREATE UNIQUE INDEX activation_preparation_one_pending_deployment_idx
    ON credential.activation_preparation (deployment_id) WHERE aborted_at IS NULL;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION credential.guard_activation_preparation_abort() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.aborted_at IS NOT NULL OR NEW.aborted_by IS NOT NULL
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
    IF OLD.aborted_at IS NOT NULL OR OLD.aborted_by IS NOT NULL
       OR NEW.aborted_at IS NULL OR NEW.aborted_by IS NULL
       OR NEW.aborted_at < OLD.created_at OR NOT isfinite(NEW.aborted_at)
       OR NEW.aborted_by <> btrim(NEW.aborted_by)
       OR octet_length(NEW.aborted_by) NOT BETWEEN 1 AND 255
       OR NEW.aborted_by ~ '[[:cntrl:]]' THEN
        RAISE EXCEPTION 'credential activation preparation may be aborted only once';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER activation_preparation_immutable ON credential.activation_preparation;
CREATE TRIGGER activation_preparation_abort_guard
    BEFORE INSERT OR UPDATE OR DELETE ON credential.activation_preparation
    FOR EACH ROW EXECUTE FUNCTION credential.guard_activation_preparation_abort();

REVOKE ALL ON FUNCTION credential.guard_activation_preparation_abort() FROM PUBLIC;
GRANT UPDATE (aborted_at, aborted_by) ON credential.activation_preparation TO leapview_control_runtime;

RESET ROLE;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'credential abort records are forward-only; restore a coordinated backup to downgrade';
END $$;
-- +goose StatementEnd
