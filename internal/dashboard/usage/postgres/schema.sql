-- Native PostgreSQL dashboard viewer-day authority.
CREATE SCHEMA IF NOT EXISTS dashboard;

CREATE TABLE IF NOT EXISTS dashboard.view_day (
    project_id text NOT NULL,
    dashboard_id text NOT NULL,
    principal_id text NOT NULL,
    viewed_on date NOT NULL,
    page_id text NOT NULL,
    first_viewed_at timestamptz NOT NULL,
    last_viewed_at timestamptz NOT NULL,
    PRIMARY KEY (project_id, dashboard_id, principal_id, viewed_on),
    CHECK (project_id = btrim(project_id) AND char_length(project_id) BETWEEN 1 AND 255 AND project_id !~ '[[:cntrl:]]'),
    CHECK (dashboard_id = btrim(dashboard_id) AND char_length(dashboard_id) BETWEEN 1 AND 255 AND dashboard_id !~ '[[:cntrl:]]'),
    CHECK (principal_id = btrim(principal_id) AND char_length(principal_id) BETWEEN 1 AND 255 AND principal_id !~ '[[:cntrl:]]'),
    CHECK (page_id = btrim(page_id) AND char_length(page_id) BETWEEN 1 AND 255 AND page_id !~ '[[:cntrl:]]'),
    CHECK (last_viewed_at >= first_viewed_at)
);

CREATE INDEX IF NOT EXISTS view_day_recent_idx
    ON dashboard.view_day(last_viewed_at DESC, project_id, dashboard_id);

REVOKE ALL ON TABLE dashboard.view_day FROM PUBLIC;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA dashboard TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON dashboard.view_day TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA dashboard TO leapview_control_maintenance;
        GRANT SELECT, DELETE ON dashboard.view_day TO leapview_control_maintenance;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA dashboard TO leapview_control_readonly;
        GRANT SELECT ON dashboard.view_day TO leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA dashboard TO leapview_control_backup;
        GRANT SELECT ON dashboard.view_day TO leapview_control_backup;
    END IF;
END
$$;
