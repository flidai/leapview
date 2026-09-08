-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- PostgreSQL requires UPDATE privilege for SELECT ... FOR UPDATE. Dashboard
-- projections deliberately deny direct runtime mutation, so expose only the
-- row-lock needed to serialize authoring command replay with guarded writes.
CREATE OR REPLACE FUNCTION dashboard.lock_authoring_dashboard(
    p_project_id text,
    p_dashboard_id text
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, dashboard
-- +goose StatementBegin
AS $$
DECLARE
    locked boolean;
BEGIN
    SELECT true INTO locked
      FROM dashboard.authoring_dashboards
     WHERE project_id = p_project_id
       AND dashboard_id = p_dashboard_id
     FOR UPDATE;
    RETURN COALESCE(locked, false);
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION dashboard.lock_authoring_dashboard(text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION dashboard.lock_authoring_dashboard(text, text) TO leapview_control_runtime;

RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
DROP FUNCTION IF EXISTS dashboard.lock_authoring_dashboard(text, text);
RESET ROLE;
