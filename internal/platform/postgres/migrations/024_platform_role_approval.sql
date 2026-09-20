-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- Durable, optional two-person approval for instance-wide platform-role
-- changes. Request identity is immutable; status transitions are guarded by a
-- monotonic revision and all lifecycle evidence remains queryable after a
-- restart.
CREATE TABLE IF NOT EXISTS access.platform_role_approval (
    id uuid PRIMARY KEY,
    action text NOT NULL CHECK (action IN ('grant','revoke')),
    principal_id uuid NOT NULL REFERENCES access.principal(id),
    requester_id uuid NOT NULL REFERENCES access.principal(id),
    approver_id uuid REFERENCES access.principal(id),
    canceled_by uuid REFERENCES access.principal(id),
    expired_by uuid REFERENCES access.principal(id),
    status text NOT NULL CHECK (status IN ('pending','approved','canceled','expired','executed')),
    expected_revision text NOT NULL CHECK (expected_revision = btrim(expected_revision) AND length(expected_revision) BETWEEN 1 AND 255),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    idempotency_key text NOT NULL UNIQUE CHECK (idempotency_key = btrim(idempotency_key) AND length(idempotency_key) BETWEEN 1 AND 256),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    approved_at timestamptz,
    canceled_at timestamptz,
    expired_at timestamptz,
    executed_at timestamptz,
    binding_id uuid REFERENCES access.platform_role_binding(id),
    result_revision text CHECK (result_revision IS NULL OR result_revision ~ '^sha256:[0-9a-f]{64}$'),
    CHECK ((status = 'pending' AND approver_id IS NULL AND approved_at IS NULL AND canceled_by IS NULL AND canceled_at IS NULL AND expired_by IS NULL AND expired_at IS NULL AND executed_at IS NULL AND binding_id IS NULL AND result_revision IS NULL)
        OR (status = 'approved' AND approver_id IS NOT NULL AND approved_at IS NOT NULL AND canceled_by IS NULL AND canceled_at IS NULL AND expired_by IS NULL AND expired_at IS NULL AND executed_at IS NULL AND binding_id IS NULL AND result_revision IS NULL)
        OR (status = 'canceled' AND canceled_by IS NOT NULL AND canceled_at IS NOT NULL AND executed_at IS NULL AND binding_id IS NULL AND result_revision IS NULL)
        OR (status = 'expired' AND expired_at IS NOT NULL AND executed_at IS NULL AND binding_id IS NULL AND result_revision IS NULL)
        OR (status = 'executed' AND approver_id IS NOT NULL AND approved_at IS NOT NULL AND executed_at IS NOT NULL AND binding_id IS NOT NULL AND result_revision IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS platform_role_approval_lifecycle_idx ON access.platform_role_approval(status, expires_at, created_at DESC);

CREATE TABLE IF NOT EXISTS access.platform_role_approval_operation (
    idempotency_key text PRIMARY KEY CHECK (idempotency_key = btrim(idempotency_key) AND length(idempotency_key) BETWEEN 1 AND 256),
    approval_id uuid NOT NULL REFERENCES access.platform_role_approval(id),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    action text NOT NULL CHECK (action IN ('request','approve','cancel','expire','execute')),
    result_revision bigint NOT NULL CHECK (result_revision > 0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

DROP TRIGGER IF EXISTS platform_role_approval_no_delete ON access.platform_role_approval;
CREATE TRIGGER platform_role_approval_no_delete
    BEFORE DELETE ON access.platform_role_approval
    FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
DROP TRIGGER IF EXISTS platform_role_approval_operation_no_delete ON access.platform_role_approval_operation;
CREATE TRIGGER platform_role_approval_operation_no_delete
    BEFORE DELETE ON access.platform_role_approval_operation
    FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
DROP TRIGGER IF EXISTS platform_role_approval_operation_immutable ON access.platform_role_approval_operation;
CREATE TRIGGER platform_role_approval_operation_immutable
    BEFORE UPDATE ON access.platform_role_approval_operation
    FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.guard_platform_role_approval_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.id <> NEW.id OR OLD.action <> NEW.action OR OLD.principal_id <> NEW.principal_id OR OLD.requester_id <> NEW.requester_id
       OR OLD.expected_revision <> NEW.expected_revision OR OLD.request_digest <> NEW.request_digest
       OR OLD.idempotency_key <> NEW.idempotency_key OR OLD.created_at <> NEW.created_at THEN
        RAISE EXCEPTION 'platform role approval identity is immutable';
    END IF;
    IF OLD.status = 'pending' AND NEW.status NOT IN ('pending','approved','canceled','expired') THEN
        RAISE EXCEPTION 'invalid platform role approval transition';
    ELSIF OLD.status = 'approved' AND NEW.status NOT IN ('approved','executed') THEN
        RAISE EXCEPTION 'invalid platform role approval transition';
    ELSIF OLD.status IN ('canceled','expired','executed') AND NEW.status <> OLD.status THEN
        RAISE EXCEPTION 'invalid platform role approval transition';
    END IF;
    IF NEW.revision <> OLD.revision + (CASE WHEN NEW.status = OLD.status THEN 0 ELSE 1 END) THEN
        RAISE EXCEPTION 'platform role approval revision is not monotonic';
    END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS platform_role_approval_transition ON access.platform_role_approval;
CREATE TRIGGER platform_role_approval_transition
    BEFORE UPDATE ON access.platform_role_approval
    FOR EACH ROW EXECUTE FUNCTION access.guard_platform_role_approval_transition();

REVOKE ALL ON TABLE access.platform_role_approval, access.platform_role_approval_operation FROM PUBLIC;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA access TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON access.platform_role_approval TO leapview_control_runtime;
        GRANT SELECT, INSERT ON access.platform_role_approval_operation TO leapview_control_runtime;
        REVOKE DELETE, TRUNCATE, REFERENCES, TRIGGER ON access.platform_role_approval FROM leapview_control_runtime;
        REVOKE UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON access.platform_role_approval_operation FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA access TO leapview_control_maintenance;
        GRANT SELECT ON access.platform_role_approval, access.platform_role_approval_operation TO leapview_control_maintenance;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON access.platform_role_approval, access.platform_role_approval_operation FROM leapview_control_maintenance;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA access TO leapview_control_readonly;
        GRANT SELECT ON access.platform_role_approval, access.platform_role_approval_operation TO leapview_control_readonly;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON access.platform_role_approval, access.platform_role_approval_operation FROM leapview_control_readonly;
    END IF;
END $$;
-- +goose StatementEnd

RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'platform role approval history is immutable; destructive down is forbidden';
END $$;
-- +goose StatementEnd
RESET ROLE;
