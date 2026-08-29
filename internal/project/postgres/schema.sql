-- Clean-slate project identity authority (ADR-0016).
--
-- The compiler remains authoritative for the project graph.  This schema
-- stores only the durable project identity and the bounded authored metadata
-- needed by control-plane projections.  It deliberately does not recreate
-- the historical SQLite `projects` table or any serving-state projection.

CREATE SCHEMA IF NOT EXISTS project;

CREATE TABLE IF NOT EXISTS project.project_identity (
    project_id   text PRIMARY KEY,
    title        text NOT NULL,
    description  text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (
        project_id = btrim(project_id)
        AND octet_length(project_id) BETWEEN 1 AND 255
        AND project_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'
    ),
    CHECK (
        title = btrim(title)
        AND octet_length(title) BETWEEN 1 AND 255
    ),
    CHECK (octet_length(description) <= 4096),
    CHECK (updated_at >= created_at)
);

-- Project identity and its authored metadata are an immutable authority. A
-- replay with different metadata is rejected by the repository as a hard
-- conflict; direct UPDATE/DELETE attempts are rejected by the database too.
CREATE OR REPLACE FUNCTION project.reject_project_identity_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'project identity and authored metadata are immutable';
END;
$$;

DROP TRIGGER IF EXISTS project_identity_immutable ON project.project_identity;
CREATE TRIGGER project_identity_immutable
    BEFORE UPDATE OR DELETE ON project.project_identity
    FOR EACH ROW EXECUTE FUNCTION project.reject_project_identity_mutation();

-- Capability schemas are never reachable through PUBLIC defaults. The
-- explicit runtime grant is deliberately limited to the operations needed by
-- identity ensure and reads; no UPDATE or DELETE privilege is granted.
REVOKE ALL ON SCHEMA project FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA project FROM PUBLIC;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA project FROM PUBLIC;

DO $$
DECLARE
    role_name text;
BEGIN
    FOREACH role_name IN ARRAY ARRAY[
        'leapview_control_runtime',
        'leapview_control_readonly'
    ] LOOP
        IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = role_name) THEN
            EXECUTE format('GRANT USAGE ON SCHEMA project TO %I', role_name);
            IF role_name = 'leapview_control_runtime' THEN
                EXECUTE format('GRANT SELECT, INSERT ON project.project_identity TO %I', role_name);
            ELSE
                EXECUTE format('GRANT SELECT ON project.project_identity TO %I', role_name);
            END IF;
        END IF;
    END LOOP;
END
$$;
