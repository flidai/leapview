-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- Platform administrator mutations are retried over unreliable transports.
-- Keep their replay identity in the access authority so a reused key cannot
-- silently target another principal or action.  This is additive to the
-- immutable 001 control-plane baseline.
CREATE TABLE IF NOT EXISTS access.platform_role_operation (
    idempotency_key text PRIMARY KEY CHECK (idempotency_key = btrim(idempotency_key) AND length(idempotency_key) BETWEEN 1 AND 256),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    action text NOT NULL CHECK (action IN ('grant','revoke')),
    principal_id uuid NOT NULL REFERENCES access.principal(id),
    binding_id uuid NOT NULL REFERENCES access.platform_role_binding(id),
    result_revision text NOT NULL CHECK (result_revision ~ '^sha256:[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

-- Operation rows are final replay evidence.  Keep the table append-only even
-- for privileged SQL callers; the owner/migrator can still inspect it while
-- runtime receives only the SELECT/INSERT surface required by the authority.
DROP TRIGGER IF EXISTS platform_role_operation_no_delete ON access.platform_role_operation;
CREATE TRIGGER platform_role_operation_no_delete
    BEFORE DELETE ON access.platform_role_operation
    FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
DROP TRIGGER IF EXISTS platform_role_operation_immutable ON access.platform_role_operation;
CREATE TRIGGER platform_role_operation_immutable
    BEFORE UPDATE ON access.platform_role_operation
    FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();

REVOKE ALL ON TABLE access.platform_role_operation FROM PUBLIC;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA access TO leapview_control_runtime;
        GRANT SELECT, INSERT ON access.platform_role_operation TO leapview_control_runtime;
        REVOKE UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON access.platform_role_operation FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA access TO leapview_control_maintenance;
        GRANT SELECT ON access.platform_role_operation TO leapview_control_maintenance;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON access.platform_role_operation FROM leapview_control_maintenance;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA access TO leapview_control_readonly;
        GRANT SELECT ON access.platform_role_operation TO leapview_control_readonly;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON access.platform_role_operation FROM leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA access TO leapview_control_backup;
        GRANT SELECT ON access.platform_role_operation TO leapview_control_backup;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON access.platform_role_operation FROM leapview_control_backup;
    END IF;
END $$;
-- +goose StatementEnd

RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
-- Replay history must remain available across upgrades; a downgrade cannot
-- safely remove operation identities that may still be retried by clients.
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'platform role operation history is immutable; destructive down is forbidden';
END $$;
-- +goose StatementEnd
RESET ROLE;
