-- +goose Up
SET LOCAL ROLE leapview_control_owner;

ALTER TABLE credential.validation_receipt
    ADD UNIQUE (deployment_id, receipt_id);

-- A preparation reserves one immutable receipt for one intended activation.
-- The unique deployment key deliberately permits only one outstanding
-- preparation in this bounded primitive; lifecycle completion can relax it.
CREATE TABLE IF NOT EXISTS credential.activation_preparation (
    operation_id             text PRIMARY KEY,
    deployment_id            text NOT NULL,
    receipt_id               text NOT NULL,
    expected_target_revision bigint NOT NULL,
    predecessor_generation_id text,
    candidate_id             text NOT NULL,
    generation_id            text NOT NULL,
    publication_id           text NOT NULL,
    created_at               timestamptz NOT NULL,
    UNIQUE (deployment_id),
    UNIQUE (receipt_id),
    FOREIGN KEY (deployment_id, receipt_id)
        REFERENCES credential.validation_receipt (deployment_id, receipt_id),
    CHECK (operation_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
        AND operation_id <> '00000000-0000-0000-0000-000000000000'),
    CHECK (deployment_id = btrim(deployment_id) AND octet_length(deployment_id) BETWEEN 1 AND 255),
    CHECK (receipt_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
        AND receipt_id <> '00000000-0000-0000-0000-000000000000'),
    CHECK (expected_target_revision > 0),
    CHECK (predecessor_generation_id IS NULL OR (
        predecessor_generation_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
        AND predecessor_generation_id <> '00000000-0000-0000-0000-000000000000')),
    CHECK (candidate_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
        AND candidate_id <> '00000000-0000-0000-0000-000000000000'),
    CHECK (generation_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
        AND generation_id <> '00000000-0000-0000-0000-000000000000'),
    CHECK (publication_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
        AND publication_id <> '00000000-0000-0000-0000-000000000000'),
    CHECK (created_at > '-infinity'::timestamptz AND isfinite(created_at))
);

DROP TRIGGER IF EXISTS activation_preparation_immutable ON credential.activation_preparation;
CREATE TRIGGER activation_preparation_immutable
    BEFORE UPDATE OR DELETE ON credential.activation_preparation
    FOR EACH ROW EXECUTE FUNCTION credential.reject_immutable_row_change();

REVOKE ALL ON credential.activation_preparation FROM PUBLIC;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT SELECT ON credential.validation_receipt TO leapview_control_runtime;
        GRANT SELECT, INSERT ON credential.activation_preparation TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT SELECT ON credential.activation_preparation TO leapview_control_backup;
    END IF;
END;
$$;
-- +goose StatementEnd

RESET ROLE;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'credential preparation records are forward-only; restore a coordinated backup to downgrade';
END $$;
-- +goose StatementEnd
