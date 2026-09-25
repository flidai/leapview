-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- Persist only the coarse browser/OS label derived by the authentication
-- boundary. Raw user-agent values and versions are deliberately excluded.
ALTER TABLE access.session
ADD COLUMN client_label text NOT NULL DEFAULT ''
CHECK (client_label = btrim(client_label) AND length(client_label) <= 255);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_session_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF OLD.id<>NEW.id OR OLD.principal_id<>NEW.principal_id OR OLD.token_fingerprint<>NEW.token_fingerprint OR OLD.verifier<>NEW.verifier OR OLD.kind<>NEW.kind OR OLD.instance_id<>NEW.instance_id OR OLD.profile_id<>NEW.profile_id OR OLD.client_id<>NEW.client_id OR OLD.client_label<>NEW.client_label OR OLD.created_at<>NEW.created_at OR OLD.absolute_expires_at IS DISTINCT FROM NEW.absolute_expires_at THEN RAISE EXCEPTION 'session identity is immutable'; END IF; RETURN NEW; END; $$;
-- +goose StatementEnd

RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_session_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF OLD.id<>NEW.id OR OLD.principal_id<>NEW.principal_id OR OLD.token_fingerprint<>NEW.token_fingerprint OR OLD.verifier<>NEW.verifier OR OLD.kind<>NEW.kind OR OLD.instance_id<>NEW.instance_id OR OLD.profile_id<>NEW.profile_id OR OLD.client_id<>NEW.client_id OR OLD.created_at<>NEW.created_at OR OLD.absolute_expires_at IS DISTINCT FROM NEW.absolute_expires_at THEN RAISE EXCEPTION 'session identity is immutable'; END IF; RETURN NEW; END; $$;
-- +goose StatementEnd

ALTER TABLE access.session DROP COLUMN client_label;

RESET ROLE;
