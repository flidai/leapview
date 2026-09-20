-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- Service-account disablement is reversible identity state. Credential
-- revocation remains monotonic and is performed by the application in the
-- same transaction as the state transition.
ALTER TABLE access.service_principal_secret
    ADD COLUMN IF NOT EXISTS last_used_at timestamptz;

RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
-- Last-used evidence is part of the credential audit contract and cannot be
-- removed by a downgrade without destroying incident-response history.
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'service principal credential evidence is immutable; destructive down is forbidden';
END $$;
-- +goose StatementEnd
RESET ROLE;
