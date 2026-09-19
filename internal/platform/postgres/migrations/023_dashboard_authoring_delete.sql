-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- Keep only the command identity and the deleted revision after the authored
-- lifecycle is removed. This is the durable retry fence for a destructive
-- command and intentionally has no foreign key back to the dashboard.
CREATE TABLE IF NOT EXISTS dashboard.authoring_delete_commands (
    project_id text NOT NULL,
    dashboard_id text NOT NULL,
    command_id uuid NOT NULL,
    request_fingerprint text NOT NULL CHECK (request_fingerprint = btrim(request_fingerprint) AND octet_length(request_fingerprint) BETWEEN 1 AND 255),
    revision_id uuid NOT NULL,
    revision_number bigint NOT NULL CHECK (revision_number > 0),
    content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (project_id, dashboard_id, command_id)
);

CREATE INDEX IF NOT EXISTS authoring_delete_commands_project_idx
    ON dashboard.authoring_delete_commands(project_id, dashboard_id, created_at DESC);

CREATE OR REPLACE FUNCTION dashboard.authoring_delete_dashboard(
    p_project_id text, p_dashboard_id text, p_expected_revision_id uuid,
    p_expected_revision_number bigint, p_expected_content_hash text,
    p_command_id uuid, p_request_fingerprint text, p_action text,
    p_command_provenance_json jsonb, p_occurred_at timestamptz
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, dashboard
AS $$
DECLARE
    v_existing_fingerprint text;
    v_rows bigint;
BEGIN
    PERFORM 1 FROM dashboard.authoring_dashboards
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
     FOR UPDATE;
    IF NOT FOUND THEN
        SELECT request_fingerprint INTO v_existing_fingerprint
          FROM dashboard.authoring_delete_commands
         WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
           AND command_id = p_command_id;
        IF NOT FOUND THEN RAISE EXCEPTION 'authoring dashboard was not found'; END IF;
        IF v_existing_fingerprint IS DISTINCT FROM p_request_fingerprint THEN
            RAISE EXCEPTION 'authoring delete command request fingerprint differs';
        END IF;
        RETURN 0;
    END IF;
    IF p_action <> 'delete' THEN
        RAISE EXCEPTION 'authoring delete requires delete command evidence';
    END IF;
    SELECT request_fingerprint INTO v_existing_fingerprint
      FROM dashboard.authoring_delete_commands
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
       AND command_id = p_command_id;
    IF FOUND THEN
        IF v_existing_fingerprint IS DISTINCT FROM p_request_fingerprint THEN
            RAISE EXCEPTION 'authoring delete command request fingerprint differs';
        END IF;
        RETURN 0;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM dashboard.authoring_drafts
         WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
           AND revision_id = p_expected_revision_id
           AND revision_number = p_expected_revision_number
           AND content_hash = p_expected_content_hash
    ) AND NOT EXISTS (
        SELECT 1 FROM dashboard.authoring_published
         WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
           AND revision_id = p_expected_revision_id
           AND revision_number = p_expected_revision_number
           AND content_hash = p_expected_content_hash
    ) THEN
        RAISE EXCEPTION 'authoring delete compare-and-swap conflict';
    END IF;
    INSERT INTO dashboard.authoring_delete_commands(
        project_id, dashboard_id, command_id, request_fingerprint,
        revision_id, revision_number, content_hash
    ) VALUES (
        p_project_id, p_dashboard_id, p_command_id, p_request_fingerprint,
        p_expected_revision_id, p_expected_revision_number, p_expected_content_hash
    );
    DELETE FROM dashboard.authoring_create_operations
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id;
    DELETE FROM dashboard.authoring_revalidation_attempts
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id;
    DELETE FROM dashboard.authoring_commands
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id;
    DELETE FROM dashboard.authoring_published
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id;
    DELETE FROM dashboard.authoring_drafts
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id;
    DELETE FROM dashboard.authoring_compiled_revisions
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id;
    DELETE FROM dashboard.authoring_revisions
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id;
    DELETE FROM dashboard.authoring_dashboards
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id;
    GET DIAGNOSTICS v_rows = ROW_COUNT;
    IF v_rows <> 1 THEN RAISE EXCEPTION 'authoring dashboard delete lifecycle conflict'; END IF;
    RETURN 1;
END;
$$;

REVOKE ALL ON TABLE dashboard.authoring_delete_commands FROM PUBLIC;
REVOKE ALL ON FUNCTION dashboard.authoring_delete_dashboard(text,text,uuid,bigint,text,uuid,text,text,jsonb,timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION dashboard.authoring_delete_dashboard(text,text,uuid,bigint,text,uuid,text,text,jsonb,timestamptz) TO leapview_control_owner;
GRANT EXECUTE ON FUNCTION dashboard.authoring_delete_dashboard(text,text,uuid,bigint,text,uuid,text,text,jsonb,timestamptz) TO leapview_control_migrator;
GRANT SELECT ON dashboard.authoring_delete_commands TO leapview_control_runtime;
GRANT EXECUTE ON FUNCTION dashboard.authoring_delete_dashboard(text,text,uuid,bigint,text,uuid,text,text,jsonb,timestamptz) TO leapview_control_runtime;
GRANT SELECT ON dashboard.authoring_delete_commands TO leapview_control_readonly;
GRANT SELECT ON dashboard.authoring_delete_commands TO leapview_control_backup;

RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
-- The command fence and deletion audit must remain available for retries and
-- historical inspection after a migration rollback request.
DO $$ BEGIN
    RAISE EXCEPTION 'dashboard authoring delete evidence is immutable; destructive down is forbidden';
END $$;
RESET ROLE;
