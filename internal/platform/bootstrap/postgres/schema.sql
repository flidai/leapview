-- Clean-slate platform bootstrap authority (ADR-0020).
--
-- This capability owns only instance bootstrap state: settings required by
-- startup, one immutable instance identity/environment binding, and the
-- singleton project claim. It deliberately does not inspect delivery,
-- serving-state, managed-data, or product tables owned by other capabilities.

CREATE SCHEMA IF NOT EXISTS platform;

CREATE TABLE IF NOT EXISTS platform.setting (
    key        text PRIMARY KEY,
    value      text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (key = btrim(key) AND octet_length(key) BETWEEN 1 AND 255),
    CHECK (octet_length(value) <= 65536),
    CHECK (updated_at >= created_at)
);

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

CREATE TABLE IF NOT EXISTS platform.instance_project_claim (
    singleton_id smallint PRIMARY KEY CHECK (singleton_id = 1),
    project_id   text NOT NULL,
    environment  text NOT NULL,
    claimed_by   text NOT NULL,
    claimed_at   timestamptz NOT NULL,
    CHECK (project_id = btrim(project_id)
           AND octet_length(project_id) BETWEEN 1 AND 255
           AND project_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (environment = btrim(environment)
           AND octet_length(environment) BETWEEN 1 AND 255
           AND environment ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (claimed_by = btrim(claimed_by)
           AND octet_length(claimed_by) BETWEEN 1 AND 256
           AND claimed_by !~ '[[:cntrl:]]'),
    CHECK (claimed_at IS NOT NULL)
);

-- Settings are the only mutable bootstrap values. The identity, environment,
-- and project claim are immutable authorities; direct UPDATE/DELETE attempts
-- fail even for roles which accidentally receive table-level write grants.
CREATE OR REPLACE FUNCTION platform.reject_bootstrap_immutable_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'platform bootstrap identity and claims are immutable';
END;
$$;

DROP TRIGGER IF EXISTS instance_identity_immutable ON platform.instance_identity;
CREATE TRIGGER instance_identity_immutable
    BEFORE UPDATE OR DELETE ON platform.instance_identity
    FOR EACH ROW EXECUTE FUNCTION platform.reject_bootstrap_immutable_mutation();

DROP TRIGGER IF EXISTS instance_environment_immutable ON platform.instance_environment;
CREATE TRIGGER instance_environment_immutable
    BEFORE UPDATE OR DELETE ON platform.instance_environment
    FOR EACH ROW EXECUTE FUNCTION platform.reject_bootstrap_immutable_mutation();

DROP TRIGGER IF EXISTS instance_project_claim_immutable ON platform.instance_project_claim;
CREATE TRIGGER instance_project_claim_immutable
    BEFORE UPDATE OR DELETE ON platform.instance_project_claim
    FOR EACH ROW EXECUTE FUNCTION platform.reject_bootstrap_immutable_mutation();

CREATE OR REPLACE FUNCTION platform.touch_setting_updated_at()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    NEW.updated_at := clock_timestamp();
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS setting_updated_at ON platform.setting;
CREATE TRIGGER setting_updated_at
    BEFORE UPDATE ON platform.setting
    FOR EACH ROW EXECUTE FUNCTION platform.touch_setting_updated_at();

REVOKE ALL ON SCHEMA platform FROM PUBLIC;
REVOKE ALL ON TABLE platform.setting, platform.instance_identity,
    platform.instance_environment, platform.instance_project_claim FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.reject_bootstrap_immutable_mutation() FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.touch_setting_updated_at() FROM PUBLIC;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA platform TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON platform.setting TO leapview_control_runtime;
        GRANT SELECT, INSERT ON platform.instance_identity,
            platform.instance_environment, platform.instance_project_claim
            TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA platform TO leapview_control_readonly;
        GRANT SELECT ON platform.setting, platform.instance_identity,
            platform.instance_environment, platform.instance_project_claim
            TO leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA platform TO leapview_control_backup;
        GRANT SELECT ON platform.setting, platform.instance_identity,
            platform.instance_environment, platform.instance_project_claim
            TO leapview_control_backup;
    END IF;
END
$$;
