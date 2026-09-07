-- Native PostgreSQL dashboard-view session authority.
CREATE SCHEMA IF NOT EXISTS dashboard;

CREATE TABLE IF NOT EXISTS dashboard.view_session (
    id text PRIMARY KEY,
    project_id text NOT NULL DEFAULT '',
    publication_id text NOT NULL DEFAULT '',
    principal_or_client text NOT NULL,
    dashboard_id text NOT NULL,
    serving_state_id text NOT NULL,
    stream_instance_id text NOT NULL,
    key_json jsonb NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    state_json jsonb NOT NULL,
    expires_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK ((project_id = '' AND publication_id <> '') OR (project_id <> '' AND publication_id = '')),
    CHECK (project_id = btrim(project_id) AND char_length(project_id) <= 255 AND project_id !~ '[[:cntrl:]]'),
    CHECK (publication_id = btrim(publication_id) AND char_length(publication_id) <= 255 AND publication_id !~ '[[:cntrl:]]'),
    CHECK (principal_or_client = btrim(principal_or_client) AND char_length(principal_or_client) BETWEEN 1 AND 255 AND principal_or_client !~ '[[:cntrl:]]'),
    CHECK (dashboard_id = btrim(dashboard_id) AND char_length(dashboard_id) BETWEEN 1 AND 255 AND dashboard_id !~ '[[:cntrl:]]'),
    CHECK (serving_state_id = btrim(serving_state_id) AND char_length(serving_state_id) BETWEEN 1 AND 255 AND serving_state_id !~ '[[:cntrl:]]'),
    CHECK (stream_instance_id = btrim(stream_instance_id) AND char_length(stream_instance_id) BETWEEN 1 AND 255 AND stream_instance_id !~ '[[:cntrl:]]'),
    CHECK (jsonb_typeof(key_json) = 'object' AND octet_length(key_json::text) <= 16384),
    CHECK (jsonb_typeof(state_json) = 'object' AND octet_length(state_json::text) <= 1048576)
);

CREATE INDEX IF NOT EXISTS view_session_expiry_idx
    ON dashboard.view_session(expires_at);

REVOKE ALL ON SCHEMA dashboard FROM PUBLIC;
REVOKE ALL ON TABLE dashboard.view_session FROM PUBLIC;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA dashboard TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON dashboard.view_session TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA dashboard TO leapview_control_maintenance;
        GRANT SELECT, DELETE ON dashboard.view_session TO leapview_control_maintenance;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA dashboard TO leapview_control_readonly;
        GRANT SELECT ON dashboard.view_session TO leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA dashboard TO leapview_control_backup;
        GRANT SELECT ON dashboard.view_session TO leapview_control_backup;
    END IF;
END
$$;
