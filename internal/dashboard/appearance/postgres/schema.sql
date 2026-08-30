-- Native PostgreSQL dashboard appearance override authority.
CREATE SCHEMA IF NOT EXISTS dashboard;

CREATE TABLE IF NOT EXISTS dashboard.appearance_override (
    project_id text NOT NULL,
    dashboard_id text NOT NULL,
    icon text,
    color text,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    updated_by text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (project_id, dashboard_id),
    CHECK (project_id = btrim(project_id) AND char_length(project_id) BETWEEN 1 AND 255 AND project_id !~ '[[:cntrl:]]'),
    CHECK (dashboard_id = btrim(dashboard_id) AND char_length(dashboard_id) BETWEEN 1 AND 255 AND dashboard_id !~ '[[:cntrl:]]'),
    CHECK (updated_by = btrim(updated_by) AND char_length(updated_by) <= 255 AND updated_by !~ '[[:cntrl:]]'),
    CHECK (icon IS NULL OR (icon = btrim(icon) AND char_length(icon) BETWEEN 1 AND 255 AND icon !~ '[[:cntrl:]]')),
    CHECK (color IS NULL OR color IN ('gray', 'blue', 'green', 'yellow', 'orange', 'red', 'purple', 'pink', 'coral'))
);

REVOKE ALL ON TABLE dashboard.appearance_override FROM PUBLIC;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA dashboard TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON dashboard.appearance_override TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA dashboard TO leapview_control_readonly;
        GRANT SELECT ON dashboard.appearance_override TO leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA dashboard TO leapview_control_backup;
        GRANT SELECT ON dashboard.appearance_override TO leapview_control_backup;
    END IF;
END
$$;
