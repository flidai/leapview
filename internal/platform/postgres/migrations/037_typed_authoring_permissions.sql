-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- Typed authoring code persists profile-pinned action/target pairs. Existing
-- device requests cannot be safely expanded from independent legacy
-- capabilities, so they receive an explicit empty authority and are expired.
ALTER TABLE access.device_authorization
    ALTER COLUMN capabilities DROP NOT NULL,
    ADD COLUMN permission_profile text NOT NULL DEFAULT 'leapview.permissions/v1'
        CHECK (permission_profile = 'leapview.permissions/v1'),
    ADD COLUMN permissions jsonb NOT NULL DEFAULT '[]'::jsonb
        CHECK (access.valid_permission_pairs(permission_profile, permissions)
               AND octet_length(permissions::text) <= 16384);

-- Record and retire outstanding device challenges before changing their
-- expiration identity. Their legacy scope is retained only as historical
-- evidence; no pending or approved challenge remains usable after upgrade.
INSERT INTO audit.audit_event (
    audit_id, principal_id, source, operation, action, resource_kind,
    resource_id, capability, outcome, metadata
)
SELECT md5('retire-legacy-device-authorization:' || id)::uuid, principal_id,
       'access.migration', 'retire_legacy_authoring_credentials',
       'device_authorization.retired', 'device_authorization', id, '', 'success',
       jsonb_build_object('reason', 'capability_only_authoring_scope_retired',
                          'permissionProfile', 'leapview.permissions/v1')
FROM access.device_authorization
WHERE status IN ('pending', 'approved') AND expires_at > clock_timestamp()
ON CONFLICT (audit_id) DO NOTHING;

DROP TRIGGER device_authorization_immutable ON access.device_authorization;
UPDATE access.device_authorization
SET expires_at = LEAST(expires_at, clock_timestamp())
WHERE status IN ('pending', 'approved') AND expires_at > clock_timestamp();

ALTER TABLE access.authoring_session
    ALTER COLUMN capabilities DROP NOT NULL,
    ADD COLUMN permission_profile text NOT NULL DEFAULT 'leapview.permissions/v1'
        CHECK (permission_profile = 'leapview.permissions/v1'),
    ADD COLUMN permissions jsonb NOT NULL DEFAULT '[]'::jsonb
        CHECK (access.valid_permission_pairs(permission_profile, permissions)
               AND octet_length(permissions::text) <= 16384);

-- Existing authoring sessions are revoked rather than translating a
-- capability list into a potentially wider set of typed permissions. An
-- explicit empty PairSet keeps the historical row valid while preventing
-- legacy authority from being projected into current authorization.
INSERT INTO audit.audit_event (
    audit_id, principal_id, source, operation, action, resource_kind,
    resource_id, capability, outcome, metadata
)
SELECT md5('retire-legacy-authoring-session:' || id)::uuid, principal_id,
       'access.migration', 'retire_legacy_authoring_credentials',
       'authoring_session.revoked', 'authoring_session', id, '', 'success',
       jsonb_build_object('reason', 'capability_only_authoring_scope_retired',
                          'permissionProfile', 'leapview.permissions/v1')
FROM access.authoring_session
WHERE revoked_at IS NULL
ON CONFLICT (audit_id) DO NOTHING;

UPDATE access.authoring_session
SET revoked_at = COALESCE(revoked_at, clock_timestamp());

UPDATE access.authoring_credential
SET active = FALSE,
    replaced_at = COALESCE(replaced_at, clock_timestamp())
WHERE active;

-- New rows must supply typed scopes explicitly. The defaults above exist only
-- to backfill historical rows to a no-authority PairSet.
ALTER TABLE access.device_authorization
    ALTER COLUMN permission_profile DROP DEFAULT,
    ALTER COLUMN permissions DROP DEFAULT;
ALTER TABLE access.authoring_session
    ALTER COLUMN permission_profile DROP DEFAULT,
    ALTER COLUMN permissions DROP DEFAULT;

-- Keep identity guards aligned with the new authority fields. Legacy
-- capability columns remain immutable historical evidence and are nullable
-- only so typed inserts can omit them.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_device_authorization_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.id<>NEW.id OR OLD.client_id<>NEW.client_id OR OLD.device_code_hash<>NEW.device_code_hash
       OR OLD.user_code_hash<>NEW.user_code_hash OR OLD.target_id<>NEW.target_id OR OLD.project_id<>NEW.project_id
       OR OLD.capabilities IS DISTINCT FROM NEW.capabilities
       OR OLD.permission_profile IS DISTINCT FROM NEW.permission_profile
       OR OLD.permissions IS DISTINCT FROM NEW.permissions
       OR OLD.created_at<>NEW.created_at OR OLD.expires_at<>NEW.expires_at THEN
        RAISE EXCEPTION 'device authorization identity is immutable';
    END IF;
    IF OLD.status='pending' AND NEW.status NOT IN ('pending','approved','denied') THEN
        RAISE EXCEPTION 'invalid device authorization transition';
    ELSIF OLD.status='approved' AND NEW.status NOT IN ('approved','consumed') THEN
        RAISE EXCEPTION 'invalid device authorization transition';
    ELSIF OLD.status IN ('denied','consumed') AND NEW.status<>OLD.status THEN
        RAISE EXCEPTION 'invalid device authorization transition';
    END IF;
    IF OLD.approved_at IS NOT NULL AND (NEW.approved_at IS NULL OR NEW.approved_at < OLD.approved_at) THEN
        RAISE EXCEPTION 'approval timestamp is monotonic';
    END IF;
    IF OLD.denied_at IS NOT NULL AND (NEW.denied_at IS NULL OR NEW.denied_at < OLD.denied_at) THEN
        RAISE EXCEPTION 'denial timestamp is monotonic';
    END IF;
    IF OLD.consumed_at IS NOT NULL AND (NEW.consumed_at IS NULL OR NEW.consumed_at < OLD.consumed_at) THEN
        RAISE EXCEPTION 'consumption timestamp is monotonic';
    END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd
CREATE TRIGGER device_authorization_immutable BEFORE UPDATE ON access.device_authorization
    FOR EACH ROW EXECUTE FUNCTION access.reject_device_authorization_rewrite();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_authoring_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME='desktop_authorization_code' THEN
        IF OLD.code_hash<>NEW.code_hash OR OLD.principal_id<>NEW.principal_id OR OLD.client_id<>NEW.client_id
           OR OLD.instance_id<>NEW.instance_id OR OLD.profile_id<>NEW.profile_id OR OLD.redirect_uri<>NEW.redirect_uri
           OR OLD.code_challenge<>NEW.code_challenge OR OLD.return_path<>NEW.return_path
           OR OLD.expires_at<>NEW.expires_at OR OLD.created_at<>NEW.created_at THEN
            RAISE EXCEPTION 'desktop authorization identity is immutable';
        END IF;
    ELSIF TG_TABLE_NAME='authoring_session' THEN
        IF OLD.id<>NEW.id OR OLD.kind<>NEW.kind OR OLD.client_id<>NEW.client_id OR OLD.principal_id<>NEW.principal_id
           OR OLD.target_id<>NEW.target_id OR OLD.project_id<>NEW.project_id
           OR OLD.capabilities IS DISTINCT FROM NEW.capabilities
           OR OLD.permission_profile<>NEW.permission_profile
           OR OLD.permissions IS DISTINCT FROM NEW.permissions OR OLD.created_at<>NEW.created_at THEN
            RAISE EXCEPTION 'authoring session identity is immutable';
        END IF;
    ELSIF TG_TABLE_NAME='authoring_credential' THEN
        IF OLD.id<>NEW.id OR OLD.session_id<>NEW.session_id OR OLD.access_token_hash<>NEW.access_token_hash
           OR OLD.refresh_token_hash IS DISTINCT FROM NEW.refresh_token_hash
           OR OLD.access_expires_at<>NEW.access_expires_at OR OLD.refresh_expires_at IS DISTINCT FROM NEW.refresh_expires_at
           OR OLD.created_at<>NEW.created_at THEN
            RAISE EXCEPTION 'authoring credential identity is immutable';
        END IF;
    END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd

RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
-- Legacy authoring authority cannot be reconstructed after revocation.
-- +goose StatementBegin
DO $$
BEGIN
    RAISE EXCEPTION 'typed authoring permissions migration is irreversible';
END $$;
-- +goose StatementEnd
RESET ROLE;
