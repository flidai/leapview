-- +goose Up
SET LOCAL ROLE leapview_control_owner;

ALTER TABLE access.authorization_policy_grant
    ALTER COLUMN capability DROP NOT NULL,
    ADD COLUMN permission_profile text,
    ADD COLUMN permissions jsonb,
    ADD CONSTRAINT authorization_policy_grant_exact_representation CHECK (
        (permission_profile IS NULL AND permissions IS NULL AND capability IS NOT NULL)
        OR
        (permission_profile IS NOT NULL AND permission_profile = 'leapview.permissions/v1' AND permissions IS NOT NULL
         AND jsonb_typeof(permissions) = 'array' AND permissions <> '[]'::jsonb AND capability IS NULL)
    );

RESET ROLE;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'typed authorization policy grants are forward-only; restore a coordinated backup to downgrade';
END $$;
-- +goose StatementEnd
