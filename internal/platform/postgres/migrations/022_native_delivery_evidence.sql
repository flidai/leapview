-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- The original native seal timestamp qualified_at was assigned by the table
-- default at insertion. Preserve that historical insertion evidence while
-- giving new rows a distinct database-authoritative created_at field.
ALTER TABLE delivery.delivery_snapshot_seal
    ADD COLUMN IF NOT EXISTS created_at timestamptz;
-- The table's history trigger rejects every UPDATE by design. Migration 022
-- is the sole controlled exception: preserve the original database timestamp
-- while the migration transaction holds the control-owner role and lock.
ALTER TABLE delivery.delivery_snapshot_seal
    DISABLE TRIGGER delivery_seal_history_immutable;
UPDATE delivery.delivery_snapshot_seal
   SET created_at = qualified_at
 WHERE created_at IS NULL;
ALTER TABLE delivery.delivery_snapshot_seal
    ENABLE TRIGGER delivery_seal_history_immutable;
ALTER TABLE delivery.delivery_snapshot_seal
    ALTER COLUMN created_at SET DEFAULT clock_timestamp(),
    ALTER COLUMN created_at SET NOT NULL;

-- Resolved inputs are a compact projection; qualification_evidence retains the
-- full native gate envelope, so the two records remain joined by the digest
-- carried in this projection.
ALTER TABLE delivery.delivery_snapshot_seal
    ADD COLUMN IF NOT EXISTS resolved_inputs jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS resolved_inputs_digest text;
ALTER TABLE delivery.delivery_snapshot_seal
    ADD CONSTRAINT delivery_snapshot_seal_resolved_inputs_object
        CHECK (jsonb_typeof(resolved_inputs) = 'object' AND octet_length(resolved_inputs::text) <= 32768),
    ADD CONSTRAINT delivery_snapshot_seal_resolved_inputs_digest
        CHECK (resolved_inputs_digest IS NULL OR resolved_inputs_digest ~ '^sha256:[0-9a-f]{64}$'),
    ADD CONSTRAINT delivery_snapshot_seal_resolved_inputs_pair
        CHECK ((resolved_inputs_digest IS NULL AND resolved_inputs = '{}'::jsonb)
            OR (resolved_inputs_digest IS NOT NULL AND resolved_inputs <> '{}'::jsonb));

ALTER TABLE delivery.delivery_candidate
    ADD COLUMN IF NOT EXISTS resolved_inputs jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS resolved_inputs_digest text;
ALTER TABLE delivery.delivery_candidate
    ADD CONSTRAINT delivery_candidate_resolved_inputs_object
        CHECK (jsonb_typeof(resolved_inputs) = 'object' AND octet_length(resolved_inputs::text) <= 32768),
    ADD CONSTRAINT delivery_candidate_resolved_inputs_digest
        CHECK (resolved_inputs_digest IS NULL OR resolved_inputs_digest ~ '^sha256:[0-9a-f]{64}$'),
    ADD CONSTRAINT delivery_candidate_resolved_inputs_pair
        CHECK ((resolved_inputs_digest IS NULL AND resolved_inputs = '{}'::jsonb)
            OR (resolved_inputs_digest IS NOT NULL AND resolved_inputs <> '{}'::jsonb));

RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
-- Created-at and resolved-input evidence are part of the immutable delivery
-- contract. A downgrade that drops them would erase operator evidence.
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'native delivery evidence is immutable; destructive down is forbidden';
END $$;
-- +goose StatementEnd
RESET ROLE;
