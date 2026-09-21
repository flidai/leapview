-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- Typed token scopes preserve action/resource pairing and pin the permission
-- catalog profile. Legacy capability arrays remain readable only for bounded
-- bootstrap/migration paths; user-issued legacy credentials are explicitly
-- revoked below because they contain no resource selection that can be
-- converted without widening authority.
-- +goose StatementBegin
CREATE FUNCTION access.valid_permission_pairs(profile text, value jsonb)
RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
    item jsonb;
    target jsonb;
    action_name text;
    target_scope text;
    seen_items jsonb := '[]'::jsonb;
BEGIN
    IF profile IS NULL OR value IS NULL THEN
        RETURN profile IS NULL AND value IS NULL;
    END IF;
    IF profile <> 'leapview.permissions/v1' OR jsonb_typeof(value) <> 'array' THEN RETURN FALSE; END IF;
    IF jsonb_array_length(value) > 256 OR octet_length(value::text) > 65536 THEN RETURN FALSE; END IF;
    FOR item IN SELECT jsonb_array_elements(value) LOOP
        IF jsonb_typeof(item) <> 'object' OR item->>'profile' <> profile
           OR jsonb_typeof(item->'target') <> 'object' THEN
            RETURN FALSE;
        END IF;
        IF seen_items @> jsonb_build_array(item) THEN RETURN FALSE; END IF;
        seen_items := seen_items || jsonb_build_array(item);
        action_name := item->>'action';
        target := item->'target';
        target_scope := target->>'scope';
        IF action_name NOT IN (
            'dashboard.read','dashboard.create','dashboard.update','dashboard.delete','dashboard.publish',
            'semantic.read','semantic.query','semantic.consume','semantic.create','semantic.update','semantic.delete',
            'source.read','source.create','source.update','source.delete',
            'model.read','model.create','model.update','model.delete',
            'pipeline.read','pipeline.create','pipeline.run','pipeline.update','pipeline.delete',
            'connection.read','connection.create','connection.use','connection.manage','resource.share',
            'delivery.read','delivery.plan','delivery.build','delivery.publish','delivery.approve','delivery.activate','delivery.rollback',
            'project.settings.read','project.settings.update','project.access.read','project.access.manage','project.access.delegate','audit.read',
            'workload.delegate','platform.settings.read','platform.settings.update','platform.access.read','platform.access.manage','platform.audit.read'
        ) THEN RETURN FALSE; END IF;
        IF target_scope = 'instance' THEN
            IF action_name NOT LIKE 'platform.%' OR COALESCE(target->>'instanceId','') = ''
               OR target ? 'projectId' OR target ? 'resourceKind' OR target ? 'resourceId'
               OR target ? 'includeFuture' THEN RETURN FALSE; END IF;
        ELSIF target_scope = 'project' THEN
            IF action_name LIKE 'platform.%' OR COALESCE(target->>'projectId','') = ''
               OR target ? 'instanceId' OR target ? 'resourceId' THEN RETURN FALSE; END IF;
            IF COALESCE((target->>'includeFuture')::boolean, FALSE) THEN
                IF COALESCE(target->>'resourceKind','') NOT IN ('connection','source','model','semantic_model','pipeline','dashboard')
                   OR action_name LIKE '%.create' OR action_name LIKE 'project.%'
                   OR action_name LIKE 'delivery.%' OR action_name = 'audit.read' THEN RETURN FALSE; END IF;
            ELSE
                IF target ? 'resourceKind' OR target ? 'includeFuture'
                   OR NOT (action_name LIKE '%.create' OR action_name LIKE 'project.%'
                           OR action_name LIKE 'delivery.%' OR action_name = 'audit.read') THEN
                    RETURN FALSE;
                END IF;
            END IF;
        ELSIF target_scope = 'resource' THEN
            IF action_name LIKE 'platform.%' OR action_name LIKE 'project.%'
               OR action_name LIKE 'delivery.%' OR action_name = 'audit.read'
               OR action_name LIKE '%.create' OR COALESCE(target->>'projectId','') = ''
               OR COALESCE(target->>'resourceKind','') NOT IN ('connection','source','model','semantic_model','pipeline','dashboard')
               OR COALESCE(target->>'resourceId','') = '' OR target ? 'instanceId'
               OR target ? 'includeFuture' THEN RETURN FALSE; END IF;
        ELSE
            RETURN FALSE;
        END IF;
        IF target ? 'resourceKind' AND (
            (action_name LIKE 'dashboard.%' AND target->>'resourceKind' <> 'dashboard')
            OR (action_name LIKE 'semantic.%' AND target->>'resourceKind' <> 'semantic_model')
            OR (action_name LIKE 'source.%' AND target->>'resourceKind' <> 'source')
            OR (action_name LIKE 'model.%' AND target->>'resourceKind' <> 'model')
            OR (action_name LIKE 'pipeline.%' AND target->>'resourceKind' <> 'pipeline')
            OR (action_name LIKE 'connection.%' AND target->>'resourceKind' <> 'connection')
            OR (action_name = 'workload.delegate' AND target->>'resourceKind' <> 'pipeline')
        ) THEN RETURN FALSE; END IF;
    END LOOP;
    RETURN TRUE;
EXCEPTION WHEN invalid_text_representation THEN
    RETURN FALSE;
END $$;
-- +goose StatementEnd

ALTER TABLE access.api_token
    ADD COLUMN permission_profile text,
    ADD COLUMN permissions jsonb,
    ADD CONSTRAINT api_token_typed_permissions_check CHECK (
        access.valid_permission_pairs(permission_profile, permissions)
        AND ((permission_profile IS NULL AND permissions IS NULL)
             OR (permission_profile = 'leapview.permissions/v1' AND capabilities IS NULL))
    );

-- Preserve an explicit, immutable outcome for credentials that cannot be
-- safely converted from a global capability list to resource/action pairs.
INSERT INTO audit.audit_event (
    audit_id, principal_id, source, operation, action, resource_kind,
    resource_id, capability, outcome, metadata
)
SELECT md5('typed-token-migration:' || id::text)::uuid, principal_id,
       'access.migration', 'typed_api_token_permissions', 'api_token.revoked',
       'api_token', id::text, '', 'success',
       jsonb_build_object('reason', 'legacy_scope_cannot_be_safely_converted',
                          'previousCapabilities', COALESCE(capabilities, 'null'::jsonb),
                          'permissionProfile', 'leapview.permissions/v1',
                          'retention', 'security')
FROM access.api_token
WHERE revoked_at IS NULL
ON CONFLICT (audit_id) DO NOTHING;

UPDATE access.api_token
SET revoked_at = clock_timestamp()
WHERE revoked_at IS NULL;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_token_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.id<>NEW.id OR OLD.principal_id<>NEW.principal_id OR OLD.name<>NEW.name
       OR OLD.token_fingerprint<>NEW.token_fingerprint OR OLD.verifier<>NEW.verifier
       OR OLD.capabilities IS DISTINCT FROM NEW.capabilities
       OR OLD.permission_profile IS DISTINCT FROM NEW.permission_profile
       OR OLD.permissions IS DISTINCT FROM NEW.permissions
       OR OLD.expires_at<>NEW.expires_at OR OLD.created_at<>NEW.created_at THEN
        RAISE EXCEPTION 'API token identity is immutable';
    END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION access.valid_permission_pairs(text, jsonb) FROM PUBLIC;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT EXECUTE ON FUNCTION access.valid_permission_pairs(text, jsonb) TO leapview_control_runtime;
    END IF;
END $$;
-- +goose StatementEnd

RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'typed API token permission migration revokes unsafe legacy credentials and is irreversible';
END $$;
-- +goose StatementEnd
RESET ROLE;
