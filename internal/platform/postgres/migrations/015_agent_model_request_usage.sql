-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- Instance-wide daily Agent model request usage. The UTC date is evaluated by
-- the database reservation query; process startup does not reset this row.
CREATE TABLE IF NOT EXISTS agent.model_request_usage (
    singleton_id  smallint PRIMARY KEY CHECK (singleton_id = 1),
    usage_day     date NOT NULL,
    used_requests bigint NOT NULL DEFAULT 0 CHECK (used_requests >= 0)
);

INSERT INTO agent.model_request_usage (singleton_id, usage_day, used_requests)
VALUES (1, (CURRENT_TIMESTAMP AT TIME ZONE 'UTC')::date, 0)
ON CONFLICT (singleton_id) DO NOTHING;

REVOKE ALL ON TABLE agent.model_request_usage FROM PUBLIC;
-- Runtime needs the row-level read/write privileges used by the atomic
-- reservation query; it has no DELETE or schema-wide privilege.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA agent TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON agent.model_request_usage TO leapview_control_runtime;
        REVOKE DELETE, TRUNCATE, REFERENCES, TRIGGER ON agent.model_request_usage FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_owner') THEN
        GRANT ALL ON agent.model_request_usage TO leapview_control_owner;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_migrator') THEN
        GRANT ALL ON agent.model_request_usage TO leapview_control_migrator;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA agent TO leapview_control_maintenance;
        REVOKE ALL ON agent.model_request_usage FROM leapview_control_maintenance;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA agent TO leapview_control_readonly;
        GRANT SELECT ON agent.model_request_usage TO leapview_control_readonly;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON agent.model_request_usage FROM leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA agent TO leapview_control_backup;
        GRANT SELECT ON agent.model_request_usage TO leapview_control_backup;
    END IF;
END
$$;
-- +goose StatementEnd

RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'agent model request usage migration is immutable; destructive down is forbidden';
END $$;
-- +goose StatementEnd
RESET ROLE;
