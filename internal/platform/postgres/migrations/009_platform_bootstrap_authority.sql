-- FAI-609: capability-owned immutable instance identity and environment.
-- Access initialization markers remain owned by the access capability.
SET ROLE leapview_control_owner;

CREATE TABLE IF NOT EXISTS platform.instance_identity (
    singleton_id smallint PRIMARY KEY CHECK (singleton_id = 1),
    instance_id  text NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (instance_id = btrim(instance_id)),
    CHECK (instance_id ~ '^(lvinst_[A-Za-z0-9_-]{32}|instance_[0-9a-f]{32})$')
);

CREATE TABLE IF NOT EXISTS platform.instance_environment (
    singleton_id smallint PRIMARY KEY CHECK (singleton_id = 1),
    environment  text NOT NULL,
    bound_at     timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (environment = btrim(environment)
           AND octet_length(environment) BETWEEN 1 AND 255
           AND environment ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$')
);

CREATE OR REPLACE FUNCTION platform.reject_instance_bootstrap_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'instance identity and environment binding are immutable';
END;
$$;

DROP TRIGGER IF EXISTS instance_identity_immutable ON platform.instance_identity;
CREATE TRIGGER instance_identity_immutable
    BEFORE UPDATE OR DELETE ON platform.instance_identity
    FOR EACH ROW EXECUTE FUNCTION platform.reject_instance_bootstrap_mutation();

DROP TRIGGER IF EXISTS instance_environment_immutable ON platform.instance_environment;
CREATE TRIGGER instance_environment_immutable
    BEFORE UPDATE OR DELETE ON platform.instance_environment
    FOR EACH ROW EXECUTE FUNCTION platform.reject_instance_bootstrap_mutation();

REVOKE ALL ON TABLE platform.instance_identity, platform.instance_environment FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.reject_instance_bootstrap_mutation() FROM PUBLIC;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA platform TO leapview_control_runtime;
        GRANT SELECT, INSERT ON platform.instance_identity, platform.instance_environment
            TO leapview_control_runtime;
        REVOKE UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON platform.instance_identity, platform.instance_environment
            FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA platform TO leapview_control_readonly;
        GRANT SELECT ON platform.instance_identity, platform.instance_environment
            TO leapview_control_readonly;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON platform.instance_identity, platform.instance_environment
            FROM leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA platform TO leapview_control_backup;
        GRANT SELECT ON platform.instance_identity, platform.instance_environment
            TO leapview_control_backup;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON platform.instance_identity, platform.instance_environment
            FROM leapview_control_backup;
    END IF;
END
$$;

RESET ROLE;
