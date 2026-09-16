-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- Next-run and schedule activation are visible on pipeline pages even when no
-- run starts. Notify the same scoped invalidation stream on committed changes.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION refresh.notify_schedule_change()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, refresh AS $$
BEGIN
    IF TG_OP = 'INSERT' OR NEW.next_run_at IS DISTINCT FROM OLD.next_run_at
       OR NEW.closed_at IS DISTINCT FROM OLD.closed_at
       OR NEW.enabled IS DISTINCT FROM OLD.enabled THEN
        PERFORM pg_notify('leapview_refresh_changed', json_build_object('projectId', NEW.project_id, 'environment', NEW.environment)::text);
    END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd

CREATE TRIGGER schedule_notify_change
AFTER INSERT OR UPDATE OF next_run_at, closed_at, enabled ON refresh.schedule_revision
FOR EACH ROW EXECUTE FUNCTION refresh.notify_schedule_change();

REVOKE ALL ON FUNCTION refresh.notify_schedule_change() FROM PUBLIC;
RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
DROP TRIGGER IF EXISTS schedule_notify_change ON refresh.schedule_revision;
DROP FUNCTION IF EXISTS refresh.notify_schedule_change();
RESET ROLE;
