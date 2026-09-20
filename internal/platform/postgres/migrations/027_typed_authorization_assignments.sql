-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- Durable grants and assignment revisions now carry the profile-pinned PairSet
-- as their authority. Legacy capability columns remain nullable evidence for
-- rows written before this migration; they are never interpreted as typed
-- authority. The migration is additive and intentionally does not rewrite
-- historical rows because generic capabilities cannot be expanded without
-- widening or changing their meaning.
ALTER TABLE access.authorization_snapshot
    ADD COLUMN permission_profile text,
    ADD CONSTRAINT authorization_snapshot_permission_profile_check
        CHECK (permission_profile IS NULL OR permission_profile = 'leapview.permissions/v1');

ALTER TABLE access.authorization_role_binding
    ALTER COLUMN role DROP NOT NULL,
    ALTER COLUMN capabilities DROP NOT NULL,
    ADD COLUMN permission_profile text,
    ADD COLUMN permissions jsonb,
    ADD COLUMN permission_role text,
    ADD CONSTRAINT authorization_role_binding_typed_permissions_check
        CHECK (
            access.valid_permission_pairs(permission_profile, permissions)
            AND (
                (permission_profile IS NULL AND permissions IS NULL AND permission_role IS NULL AND role IS NOT NULL AND capabilities IS NOT NULL)
                OR (permission_profile = 'leapview.permissions/v1' AND permissions IS NOT NULL AND permission_role IN ('viewer','explorer','editor','project_admin','publisher','release_approver','release_operator','auditor') AND role IS NULL AND capabilities IS NULL)
            )
        );

ALTER TABLE access.authorization_grant
    ALTER COLUMN resource_id DROP NOT NULL,
    ALTER COLUMN resource_kind DROP NOT NULL,
    ALTER COLUMN capability DROP NOT NULL,
    ADD COLUMN permission_profile text,
    ADD COLUMN permissions jsonb,
    ADD CONSTRAINT authorization_grant_typed_permissions_check
        CHECK (
            access.valid_permission_pairs(permission_profile, permissions)
            AND (
                (permission_profile IS NULL AND permissions IS NULL
                 AND resource_id IS NOT NULL AND resource_kind IS NOT NULL AND capability IS NOT NULL)
                OR
                (permission_profile = 'leapview.permissions/v1'
                 AND permissions IS NOT NULL
                 AND jsonb_array_length(permissions) = 1
                 AND resource_id IS NULL AND resource_kind IS NULL AND capability IS NULL)
            )
        );

ALTER TABLE access.authorization_policy_role_binding
    ALTER COLUMN role DROP NOT NULL,
    ALTER COLUMN capabilities DROP NOT NULL,
    ADD COLUMN permission_profile text,
    ADD COLUMN permissions jsonb,
    ADD COLUMN permission_role text,
    ADD CONSTRAINT authorization_policy_role_binding_typed_permissions_check
        CHECK (
            access.valid_permission_pairs(permission_profile, permissions)
            AND (
                (permission_profile IS NULL AND permissions IS NULL AND permission_role IS NULL AND role IS NOT NULL AND capabilities IS NOT NULL)
                OR (permission_profile = 'leapview.permissions/v1' AND permissions IS NOT NULL AND permission_role IN ('viewer','explorer','editor','project_admin','publisher','release_approver','release_operator','auditor') AND role IS NULL AND capabilities IS NULL)
            )
        );

CREATE UNIQUE INDEX authorization_grant_typed_pair_key
    ON access.authorization_grant (
        project_id, environment, generation_id, subject_kind, subject_id,
        (permissions->0->>'action'),
        (permissions->0->'target'->>'scope'),
        (permissions->0->'target'->>'instanceId'),
        (permissions->0->'target'->>'projectId'),
        (permissions->0->'target'->>'resourceKind'),
        (permissions->0->'target'->>'resourceId'),
        (permissions->0->'target'->>'includeFuture')
    ) NULLS NOT DISTINCT WHERE revoked_at IS NULL AND permission_profile IS NOT NULL;

CREATE UNIQUE INDEX authorization_role_binding_typed_role_key
    ON access.authorization_role_binding
        (project_id, environment, generation_id, subject_kind, subject_id, permission_role)
    WHERE revoked_at IS NULL AND permission_profile IS NOT NULL;

CREATE UNIQUE INDEX authorization_policy_role_binding_typed_role_key
    ON access.authorization_policy_role_binding
        (target_id, project_id, environment, revision, subject_kind, subject_id, permission_role)
    WHERE permission_profile IS NOT NULL;

-- Snapshot and assignment identities include the typed authority. Revocation
-- remains the only mutable state; the captured profile and PairSet cannot be
-- changed in place after installation.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_role_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.id<>NEW.id OR OLD.principal_id<>NEW.principal_id OR OLD.role<>NEW.role
       OR OLD.created_at<>NEW.created_at THEN
        RAISE EXCEPTION 'role identity is immutable';
    END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_authorization_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME = 'authorization_snapshot' THEN
        IF OLD.project_id<>NEW.project_id OR OLD.environment<>NEW.environment
           OR OLD.generation_id<>NEW.generation_id OR OLD.digest<>NEW.digest
           OR OLD.permission_profile IS DISTINCT FROM NEW.permission_profile
           OR OLD.created_at<>NEW.created_at THEN
            RAISE EXCEPTION 'authorization snapshot identity is immutable';
        END IF;
    ELSIF TG_TABLE_NAME = 'authorization_role_binding' THEN
        IF OLD.id<>NEW.id OR OLD.project_id<>NEW.project_id OR OLD.environment<>NEW.environment
           OR OLD.generation_id<>NEW.generation_id OR OLD.subject_kind<>NEW.subject_kind
           OR OLD.subject_id<>NEW.subject_id OR OLD.role<>NEW.role
           OR OLD.capabilities IS DISTINCT FROM NEW.capabilities
           OR OLD.permission_profile IS DISTINCT FROM NEW.permission_profile
           OR OLD.permissions IS DISTINCT FROM NEW.permissions
           OR OLD.permission_role IS DISTINCT FROM NEW.permission_role
           OR OLD.name<>NEW.name OR OLD.created_at<>NEW.created_at THEN
            RAISE EXCEPTION 'authorization role binding identity is immutable';
        END IF;
    ELSIF TG_TABLE_NAME = 'authorization_grant' THEN
        IF OLD.id<>NEW.id OR OLD.project_id<>NEW.project_id OR OLD.environment<>NEW.environment
           OR OLD.generation_id<>NEW.generation_id OR OLD.subject_kind<>NEW.subject_kind
           OR OLD.subject_id<>NEW.subject_id OR OLD.resource_id IS DISTINCT FROM NEW.resource_id
           OR OLD.resource_kind IS DISTINCT FROM NEW.resource_kind OR OLD.capability IS DISTINCT FROM NEW.capability
           OR OLD.permission_profile IS DISTINCT FROM NEW.permission_profile
           OR OLD.permissions IS DISTINCT FROM NEW.permissions
           OR OLD.name<>NEW.name OR OLD.created_at<>NEW.created_at THEN
            RAISE EXCEPTION 'authorization grant identity is immutable';
        END IF;
    ELSIF TG_TABLE_NAME = 'authorization_data_policy' THEN
        IF OLD.id<>NEW.id OR OLD.project_id<>NEW.project_id OR OLD.environment<>NEW.environment
           OR OLD.generation_id<>NEW.generation_id OR OLD.resource_id<>NEW.resource_id
           OR OLD.resource_kind<>NEW.resource_kind OR OLD.subject_kind IS DISTINCT FROM NEW.subject_kind
           OR OLD.subject_id IS DISTINCT FROM NEW.subject_id OR OLD.policy_type<>NEW.policy_type
           OR OLD.expression IS DISTINCT FROM NEW.expression OR OLD.created_at<>NEW.created_at THEN
            RAISE EXCEPTION 'authorization policy identity is immutable';
        END IF;
    END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd

REVOKE ALL ON TABLE access.authorization_snapshot, access.authorization_role_binding,
    access.authorization_grant, access.authorization_policy_role_binding
    FROM PUBLIC;

RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
-- Typed assignment evidence is forward-only. Removing columns would erase
-- authority history and is intentionally forbidden.
-- +goose StatementBegin
DO $$
BEGIN
    RAISE EXCEPTION 'typed authorization assignments migration is immutable';
END $$;
-- +goose StatementEnd
RESET ROLE;
