-- +goose Up
SET LOCAL ROLE leapview_control_owner;

ALTER TABLE access.api_token ADD COLUMN modified_at timestamptz;
UPDATE access.api_token SET modified_at = created_at;
ALTER TABLE access.api_token
    ALTER COLUMN modified_at SET DEFAULT clock_timestamp(),
    ALTER COLUMN modified_at SET NOT NULL;

-- The credential identity remains fixed. Metadata, typed authority, and
-- expiration may change through owner-scoped audited mutations; rotation
-- creates a replacement credential instead of rewriting the bearer secret.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_token_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.id<>NEW.id OR OLD.principal_id<>NEW.principal_id
       OR OLD.token_fingerprint<>NEW.token_fingerprint OR OLD.verifier<>NEW.verifier
       OR OLD.capabilities IS DISTINCT FROM NEW.capabilities
       OR OLD.permission_profile IS DISTINCT FROM NEW.permission_profile
       OR OLD.created_at<>NEW.created_at THEN
        RAISE EXCEPTION 'API token identity is immutable';
    END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd

RESET ROLE;

-- +goose Down
DO $$
BEGIN
    RAISE EXCEPTION 'editable API token migration is forward-only';
END $$;
