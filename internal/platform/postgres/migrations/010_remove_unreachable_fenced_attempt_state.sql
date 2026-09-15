-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- AttemptFenced was never a persisted transition in the deployment
-- authority. Remove the legacy allowance from databases that predate the
-- source-contract cleanup without rewriting any immutable migration.
-- +goose StatementBegin
DO $$
DECLARE
    constraint_row record;
BEGIN
    FOR constraint_row IN
        SELECT c.conname
          FROM pg_constraint AS c
         WHERE c.conrelid = 'delivery.delivery_build_attempt'::regclass
           AND c.contype = 'c'
           AND pg_get_constraintdef(c.oid) ILIKE '%fenced%'
    LOOP
        EXECUTE format('ALTER TABLE delivery.delivery_build_attempt DROP CONSTRAINT %I', constraint_row.conname);
    END LOOP;
END;
$$;
-- +goose StatementEnd

ALTER TABLE delivery.delivery_build_attempt
    ADD CONSTRAINT delivery_build_attempt_state_allowed_check
    CHECK (state = btrim(state) AND octet_length(state) BETWEEN 1 AND 32 AND state IN ('running','committed','aborted','indeterminate'));

ALTER TABLE delivery.delivery_build_attempt
    ADD CONSTRAINT delivery_build_attempt_evidence_state_check
    CHECK ((state = 'running' AND snapshot_id IS NULL AND commit_marker IS NULL AND termination_evidence IS NULL)
        OR (state = 'committed' AND snapshot_id IS NOT NULL AND commit_marker IS NOT NULL AND termination_evidence IS NULL)
        OR (state IN ('aborted','indeterminate') AND snapshot_id IS NULL AND commit_marker IS NULL AND termination_evidence IS NOT NULL));

RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
-- This migration narrows a live state contract. Reintroducing an unreachable
-- state would make old workers able to write ambiguous evidence.
-- +goose StatementBegin
DO $$ BEGIN
  RAISE EXCEPTION 'unreachable fenced attempt state removal is immutable; destructive down is forbidden';
END $$;
-- +goose StatementEnd
RESET ROLE;
