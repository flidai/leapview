-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- A captured authorization grant retains its complete permission set, including
-- action prerequisites (for example connection.read plus connection.upload).
-- Keep the profile/catalog validation and exclusive typed representation; only
-- remove the obsolete singular-pair restriction. Existing rows and snapshot
-- digests are unchanged, and identity-rewrite protection remains in force.
ALTER TABLE access.authorization_grant
    DROP CONSTRAINT authorization_grant_typed_permissions_check,
    ADD CONSTRAINT authorization_grant_typed_permissions_check CHECK (
        access.valid_permission_pairs(permission_profile, permissions)
        AND (
            (permission_profile IS NULL AND permissions IS NULL
             AND resource_id IS NOT NULL AND resource_kind IS NOT NULL AND capability IS NOT NULL)
            OR
            (permission_profile = 'leapview.permissions/v1' AND permissions IS NOT NULL
             AND jsonb_array_length(permissions) >= 1
             AND resource_id IS NULL AND resource_kind IS NULL AND capability IS NULL)
        )
    );

RESET ROLE;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'compound snapshot grants are forward-only; restore a coordinated backup to downgrade';
END $$;
-- +goose StatementEnd
