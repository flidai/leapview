-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- Install the exact generated v1 validator before issuing the new
-- instance.project.claim credential. Migration 027 is immutable; this
-- forward replacement adds that one action without weakening any pair shape.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.valid_permission_pairs(profile text, value jsonb)
RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
    item jsonb;
    target jsonb;
    action_name text;
    target_scope text;
    expected_scope text;
    resource_kind text;
    include_future boolean;
    allowed_resource_kinds text[];
    seen_items jsonb := '[]'::jsonb;
BEGIN
    IF profile IS NULL OR value IS NULL THEN
        RETURN profile IS NULL AND value IS NULL;
    END IF;
    IF profile <> 'leapview.permissions/v1' OR jsonb_typeof(value) <> 'array' THEN RETURN FALSE; END IF;
    IF jsonb_array_length(value) > 256 OR octet_length(value::text) > 65536 THEN RETURN FALSE; END IF;
    FOR item IN SELECT jsonb_array_elements(value) LOOP
        IF jsonb_typeof(item) <> 'object'
           OR (SELECT count(*) FROM jsonb_object_keys(item)) <> 3
           OR EXISTS (SELECT 1 FROM jsonb_object_keys(item) AS key WHERE key NOT IN ('action', 'target', 'profile'))
           OR jsonb_typeof(item->'action') <> 'string'
           OR jsonb_typeof(item->'profile') <> 'string'
           OR jsonb_typeof(item->'target') <> 'object'
           OR item->>'profile' <> profile THEN
            RETURN FALSE;
        END IF;
        IF EXISTS (SELECT 1 FROM jsonb_array_elements(seen_items) AS seen(seen_item) WHERE seen.seen_item = item) THEN
            RETURN FALSE;
        END IF;
        seen_items := seen_items || jsonb_build_array(item);
        target := item->'target';
        IF (SELECT count(*) FROM jsonb_object_keys(target)) < 1
           OR EXISTS (SELECT 1 FROM jsonb_object_keys(target) AS key WHERE key NOT IN ('scope', 'instanceId', 'projectId', 'resourceKind', 'resourceId', 'includeFuture'))
           OR NOT (target ? 'scope') OR jsonb_typeof(target->'scope') <> 'string' THEN
            RETURN FALSE;
        END IF;
        IF (target ? 'instanceId' AND jsonb_typeof(target->'instanceId') <> 'string')
           OR (target ? 'projectId' AND jsonb_typeof(target->'projectId') <> 'string')
           OR (target ? 'resourceKind' AND jsonb_typeof(target->'resourceKind') <> 'string')
           OR (target ? 'resourceId' AND jsonb_typeof(target->'resourceId') <> 'string')
           OR (target ? 'includeFuture' AND jsonb_typeof(target->'includeFuture') <> 'boolean') THEN
            RETURN FALSE;
        END IF;
        action_name := item->>'action';
        target_scope := target->>'scope';
        expected_scope := NULL;
        allowed_resource_kinds := ARRAY[]::text[];
        IF action_name = 'dashboard.read' THEN
            expected_scope := 'resource';
            allowed_resource_kinds := ARRAY['dashboard']::text[];
        ELSIF action_name = 'dashboard.create' THEN
            expected_scope := 'project';
            allowed_resource_kinds := ARRAY['dashboard']::text[];
        ELSIF action_name = 'dashboard.update' THEN
            expected_scope := 'resource';
            allowed_resource_kinds := ARRAY['dashboard']::text[];
        ELSIF action_name = 'dashboard.delete' THEN
            expected_scope := 'resource';
            allowed_resource_kinds := ARRAY['dashboard']::text[];
        ELSIF action_name = 'dashboard.publish' THEN
            expected_scope := 'resource';
            allowed_resource_kinds := ARRAY['dashboard']::text[];
        ELSIF action_name = 'semantic.read' THEN
            expected_scope := 'resource';
            allowed_resource_kinds := ARRAY['semantic_model']::text[];
        ELSIF action_name = 'semantic.query' THEN
            expected_scope := 'resource';
            allowed_resource_kinds := ARRAY['semantic_model']::text[];
        ELSIF action_name = 'semantic.consume' THEN
            expected_scope := 'resource';
            allowed_resource_kinds := ARRAY['semantic_model']::text[];
        ELSIF action_name = 'semantic.create' THEN
            expected_scope := 'project';
            allowed_resource_kinds := ARRAY['semantic_model']::text[];
        ELSIF action_name = 'semantic.update' THEN
            expected_scope := 'resource';
            allowed_resource_kinds := ARRAY['semantic_model']::text[];
        ELSIF action_name = 'semantic.delete' THEN
            expected_scope := 'resource';
            allowed_resource_kinds := ARRAY['semantic_model']::text[];
        ELSIF action_name = 'source.read' THEN
            expected_scope := 'resource';
            allowed_resource_kinds := ARRAY['source']::text[];
        ELSIF action_name = 'source.create' THEN
            expected_scope := 'project';
            allowed_resource_kinds := ARRAY['source']::text[];
        ELSIF action_name = 'source.update' THEN
            expected_scope := 'resource';
            allowed_resource_kinds := ARRAY['source']::text[];
        ELSIF action_name = 'source.delete' THEN
            expected_scope := 'resource';
            allowed_resource_kinds := ARRAY['source']::text[];
        ELSIF action_name = 'model.read' THEN
            expected_scope := 'resource';
            allowed_resource_kinds := ARRAY['model']::text[];
        ELSIF action_name = 'model.create' THEN
            expected_scope := 'project';
            allowed_resource_kinds := ARRAY['model']::text[];
        ELSIF action_name = 'model.update' THEN
            expected_scope := 'resource';
            allowed_resource_kinds := ARRAY['model']::text[];
        ELSIF action_name = 'model.delete' THEN
            expected_scope := 'resource';
            allowed_resource_kinds := ARRAY['model']::text[];
        ELSIF action_name = 'pipeline.read' THEN
            expected_scope := 'resource';
            allowed_resource_kinds := ARRAY['pipeline']::text[];
        ELSIF action_name = 'pipeline.create' THEN
            expected_scope := 'project';
            allowed_resource_kinds := ARRAY['pipeline']::text[];
        ELSIF action_name = 'pipeline.run' THEN
            expected_scope := 'resource';
            allowed_resource_kinds := ARRAY['pipeline']::text[];
        ELSIF action_name = 'pipeline.update' THEN
            expected_scope := 'resource';
            allowed_resource_kinds := ARRAY['pipeline']::text[];
        ELSIF action_name = 'pipeline.delete' THEN
            expected_scope := 'resource';
            allowed_resource_kinds := ARRAY['pipeline']::text[];
        ELSIF action_name = 'connection.read' THEN
            expected_scope := 'resource';
            allowed_resource_kinds := ARRAY['connection']::text[];
        ELSIF action_name = 'connection.create' THEN
            expected_scope := 'project';
            allowed_resource_kinds := ARRAY['connection']::text[];
        ELSIF action_name = 'connection.use' THEN
            expected_scope := 'resource';
            allowed_resource_kinds := ARRAY['connection']::text[];
        ELSIF action_name = 'connection.manage' THEN
            expected_scope := 'resource';
            allowed_resource_kinds := ARRAY['connection']::text[];
        ELSIF action_name = 'resource.share' THEN
            expected_scope := 'resource';
            allowed_resource_kinds := ARRAY['connection', 'source', 'model', 'semantic_model', 'pipeline', 'dashboard']::text[];
        ELSIF action_name = 'delivery.read' THEN
            expected_scope := 'project';
            allowed_resource_kinds := ARRAY['project']::text[];
        ELSIF action_name = 'delivery.plan' THEN
            expected_scope := 'project';
            allowed_resource_kinds := ARRAY['project']::text[];
        ELSIF action_name = 'delivery.build' THEN
            expected_scope := 'project';
            allowed_resource_kinds := ARRAY['project']::text[];
        ELSIF action_name = 'delivery.publish' THEN
            expected_scope := 'project';
            allowed_resource_kinds := ARRAY['project']::text[];
        ELSIF action_name = 'delivery.approve' THEN
            expected_scope := 'project';
            allowed_resource_kinds := ARRAY['project']::text[];
        ELSIF action_name = 'delivery.activate' THEN
            expected_scope := 'project';
            allowed_resource_kinds := ARRAY['project']::text[];
        ELSIF action_name = 'delivery.rollback' THEN
            expected_scope := 'project';
            allowed_resource_kinds := ARRAY['project']::text[];
        ELSIF action_name = 'project.settings.read' THEN
            expected_scope := 'project';
            allowed_resource_kinds := ARRAY['project']::text[];
        ELSIF action_name = 'project.settings.update' THEN
            expected_scope := 'project';
            allowed_resource_kinds := ARRAY['project']::text[];
        ELSIF action_name = 'project.access.read' THEN
            expected_scope := 'project';
            allowed_resource_kinds := ARRAY['project']::text[];
        ELSIF action_name = 'project.access.manage' THEN
            expected_scope := 'project';
            allowed_resource_kinds := ARRAY['project']::text[];
        ELSIF action_name = 'project.access.delegate' THEN
            expected_scope := 'project';
            allowed_resource_kinds := ARRAY['project']::text[];
        ELSIF action_name = 'audit.read' THEN
            expected_scope := 'project';
            allowed_resource_kinds := ARRAY['project']::text[];
        ELSIF action_name = 'workload.delegate' THEN
            expected_scope := 'resource';
            allowed_resource_kinds := ARRAY['pipeline']::text[];
        ELSIF action_name = 'platform.settings.read' THEN
            expected_scope := 'instance';
            allowed_resource_kinds := ARRAY[]::text[];
        ELSIF action_name = 'platform.settings.update' THEN
            expected_scope := 'instance';
            allowed_resource_kinds := ARRAY[]::text[];
        ELSIF action_name = 'platform.access.read' THEN
            expected_scope := 'instance';
            allowed_resource_kinds := ARRAY[]::text[];
        ELSIF action_name = 'platform.access.manage' THEN
            expected_scope := 'instance';
            allowed_resource_kinds := ARRAY[]::text[];
        ELSIF action_name = 'platform.audit.read' THEN
            expected_scope := 'instance';
            allowed_resource_kinds := ARRAY[]::text[];
        ELSIF action_name = 'instance.project.claim' THEN
            expected_scope := 'instance';
            allowed_resource_kinds := ARRAY[]::text[];
        ELSE
            RETURN FALSE;
        END IF;
        IF target_scope = 'instance' THEN
            IF expected_scope <> 'instance' OR NOT (target ? 'instanceId')
               OR target->>'instanceId' = '' OR target->>'instanceId' <> btrim(target->>'instanceId')
               OR octet_length(target->>'instanceId') > 255
               OR position(chr(9) in target->>'instanceId') > 0
               OR position(chr(10) in target->>'instanceId') > 0
               OR position(chr(13) in target->>'instanceId') > 0
               OR target ? 'projectId' OR target ? 'resourceKind' OR target ? 'resourceId'
               OR COALESCE((target->>'includeFuture')::boolean, FALSE) THEN RETURN FALSE; END IF;
        ELSIF target_scope = 'project' THEN
            IF expected_scope = 'instance' OR NOT (target ? 'projectId')
               OR target->>'projectId' !~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'
               OR target ? 'instanceId' OR target ? 'resourceId' THEN RETURN FALSE; END IF;
            include_future := COALESCE((target->>'includeFuture')::boolean, FALSE);
            IF include_future THEN
                IF expected_scope <> 'resource' OR NOT (target ? 'resourceKind') THEN RETURN FALSE; END IF;
                resource_kind := target->>'resourceKind';
                IF NOT (resource_kind = ANY(allowed_resource_kinds)) THEN RETURN FALSE; END IF;
            ELSE
                IF expected_scope <> 'project' OR target ? 'resourceKind' THEN RETURN FALSE; END IF;
            END IF;
        ELSIF target_scope = 'resource' THEN
            IF expected_scope <> 'resource' OR NOT (target ? 'projectId')
               OR target->>'projectId' !~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'
               OR NOT (target ? 'resourceKind') OR NOT (target ? 'resourceId')
               OR target->>'resourceKind' <> btrim(target->>'resourceKind')
               OR NOT (target->>'resourceKind' = ANY(allowed_resource_kinds))
               OR target->>'resourceId' !~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'
               OR target ? 'instanceId'
               OR COALESCE((target->>'includeFuture')::boolean, FALSE) THEN RETURN FALSE; END IF;
        ELSE
            RETURN FALSE;
        END IF;
    END LOOP;
    RETURN TRUE;
EXCEPTION WHEN invalid_text_representation THEN
    RETURN FALSE;
END $$;
-- +goose StatementEnd

-- Tokens issued by the retired capability-only path cannot be given a safe
-- action/target scope. Revoke them explicitly before hardening the invariant.
INSERT INTO audit.audit_event (
    audit_id, principal_id, source, operation, action, resource_kind,
    resource_id, capability, outcome, metadata
)
SELECT md5('retire-legacy-token:' || id::text)::uuid, principal_id,
       'access.migration', 'retire_legacy_api_tokens', 'api_token.revoked',
       'api_token', id::text, '', 'success',
       jsonb_build_object('reason', 'capability_only_scope_retired',
                          'permissionProfile', 'leapview.permissions/v1',
                          'retention', 'security')
FROM access.api_token
WHERE revoked_at IS NULL
  AND (permission_profile IS DISTINCT FROM 'leapview.permissions/v1'
       OR permissions IS NULL OR capabilities IS NOT NULL)
ON CONFLICT (audit_id) DO NOTHING;

UPDATE access.api_token
SET revoked_at = clock_timestamp()
WHERE revoked_at IS NULL
  AND (permission_profile IS DISTINCT FROM 'leapview.permissions/v1'
       OR permissions IS NULL OR capabilities IS NOT NULL);

ALTER TABLE access.api_token
    DROP CONSTRAINT api_token_typed_permissions_check,
    ADD CONSTRAINT api_token_typed_permissions_check CHECK (
        access.valid_permission_pairs(permission_profile, permissions)
        AND ((permission_profile = 'leapview.permissions/v1'
              AND permissions IS NOT NULL AND capabilities IS NULL)
             OR (permission_profile IS NULL AND permissions IS NULL
                 AND revoked_at IS NOT NULL))
    );

RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
DO $$
BEGIN
    RAISE EXCEPTION 'active typed API token invariant is immutable';
END $$;
RESET ROLE;
