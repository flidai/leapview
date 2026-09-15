-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- A NOTIFY is delivered only when the containing transaction commits. It is
-- an invalidation hint, not an event log: listeners reread refresh.run.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION refresh.notify_root_run_change()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
BEGIN
    IF NEW.parent_run_id IS NULL AND (TG_OP = 'INSERT' OR NEW.status IS DISTINCT FROM OLD.status) THEN
        PERFORM pg_notify('leapview_refresh_changed', json_build_object('projectId', NEW.project_id, 'environment', NEW.environment)::text);
    END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd

CREATE TRIGGER run_notify_change
AFTER INSERT OR UPDATE OF status ON refresh.run
FOR EACH ROW EXECUTE FUNCTION refresh.notify_root_run_change();

REVOKE ALL ON FUNCTION refresh.notify_root_run_change() FROM PUBLIC;
RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
DROP TRIGGER IF EXISTS run_notify_change ON refresh.run;
DROP FUNCTION IF EXISTS refresh.notify_root_run_change();
RESET ROLE;
