-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- Dashboard authoring commands use one UUIDv7 for the command, domain event,
-- and audit record. Enforce the capability required by the actual lifecycle
-- action instead of treating publish and archive as ordinary edits.
CREATE OR REPLACE FUNCTION dashboard.guard_authoring_dashboard_evidence()
RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, dashboard, event, audit
-- +goose StatementBegin
AS $$
DECLARE
    v_ok boolean;
    v_required_capability text := 'RESOURCE_EDIT';
BEGIN
    IF NEW.last_event_id IS NULL THEN
        RAISE EXCEPTION 'authoring dashboard mutation requires canonical event identity';
    END IF;
    IF TG_OP = 'UPDATE' THEN
        SELECT CASE c.action
                 WHEN 'publish' THEN 'RESOURCE_PUBLISH'
                 WHEN 'archive' THEN 'RESOURCE_MANAGE'
                 ELSE 'RESOURCE_EDIT'
               END
          INTO v_required_capability
          FROM dashboard.authoring_commands c
         WHERE c.project_id = NEW.project_id
           AND c.dashboard_id = NEW.dashboard_id
           AND c.command_id = NEW.last_event_id;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'authoring dashboard mutation requires linked command evidence';
        END IF;
    END IF;
    SELECT EXISTS (
        SELECT 1
          FROM event.event_log e
          JOIN audit.audit_event a ON a.event_id = e.event_id
         WHERE e.event_id = NEW.last_event_id
           AND e.scope_id = NEW.project_id
           AND e.aggregate_type = 'dashboard_authoring'
           AND e.aggregate_id = NEW.dashboard_id
           AND e.aggregate_version > 0
           AND a.scope_id = e.scope_id
           AND a.source = 'dashboard.authoring'
           AND a.capability = v_required_capability
           AND a.outcome = 'success'
           AND a.actor_id IS NOT NULL
           AND a.resource_kind = 'dashboard'
           AND a.resource_id = e.aggregate_id
           AND a.aggregate_key = ('dashboard_authoring:' || e.scope_id || ':' || e.aggregate_id)
           AND a.aggregate_sequence = e.aggregate_version
           AND (a.correlation_id IS NOT DISTINCT FROM e.correlation_id::text
                OR (e.correlation_id IS NULL AND a.correlation_id IS NOT NULL
                    AND NOT (a.correlation_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$')))
           AND a.action = e.event_type
           AND a.metadata = e.payload)
       INTO v_ok;
    IF NOT v_ok THEN
        RAISE EXCEPTION 'authoring dashboard mutation requires linked canonical event and audit evidence';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION dashboard.guard_authoring_dashboard_evidence() FROM PUBLIC;

RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;

-- Roll back only the capability discrimination. The original baseline guard
-- continues to require linked edit evidence for every authoring mutation.
CREATE OR REPLACE FUNCTION dashboard.guard_authoring_dashboard_evidence()
RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, dashboard, event, audit
-- +goose StatementBegin
AS $$
DECLARE v_ok boolean;
BEGIN
    IF NEW.last_event_id IS NULL THEN
        RAISE EXCEPTION 'authoring dashboard mutation requires canonical event identity';
    END IF;
    SELECT EXISTS (
        SELECT 1
          FROM event.event_log e
          JOIN audit.audit_event a ON a.event_id = e.event_id
         WHERE e.event_id = NEW.last_event_id
           AND e.scope_id = NEW.project_id
           AND e.aggregate_type = 'dashboard_authoring'
           AND e.aggregate_id = NEW.dashboard_id
           AND e.aggregate_version > 0
           AND a.scope_id = e.scope_id
           AND a.source = 'dashboard.authoring'
           AND a.capability = 'RESOURCE_EDIT'
           AND a.outcome = 'success'
           AND a.actor_id IS NOT NULL
           AND a.resource_kind = 'dashboard'
           AND a.resource_id = e.aggregate_id
           AND a.aggregate_key = ('dashboard_authoring:' || e.scope_id || ':' || e.aggregate_id)
           AND a.aggregate_sequence = e.aggregate_version
           AND (a.correlation_id IS NOT DISTINCT FROM e.correlation_id::text
                OR (e.correlation_id IS NULL AND a.correlation_id IS NOT NULL
                    AND NOT (a.correlation_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$')))
           AND a.action = e.event_type
           AND a.metadata = e.payload)
       INTO v_ok;
    IF NOT v_ok THEN
        RAISE EXCEPTION 'authoring dashboard mutation requires linked canonical event and audit evidence';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION dashboard.guard_authoring_dashboard_evidence() FROM PUBLIC;

RESET ROLE;
