-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- Provenance is private access authority, never inferred from token names.
CREATE TABLE access.initial_password_setup (
    -- The spent claim can be pruned while its publisher remains active.
    -- This initialization record permanently retains the original binding.
    claim_credential_id uuid PRIMARY KEY,
    principal_id uuid NOT NULL REFERENCES access.principal(id),
    instance_id text NOT NULL CHECK (instance_id = btrim(instance_id) AND length(instance_id) BETWEEN 1 AND 255),
    closed_at timestamptz,
    UNIQUE (principal_id, instance_id)
);
CREATE TABLE access.initial_publisher_origin (
    publisher_credential_id uuid PRIMARY KEY REFERENCES access.api_token(id) ON DELETE CASCADE,
    claim_credential_id uuid NOT NULL REFERENCES access.initial_password_setup(claim_credential_id),
    project_id text NOT NULL CHECK (project_id = btrim(project_id) AND length(project_id) BETWEEN 1 AND 255)
);

-- +goose StatementBegin
CREATE FUNCTION access.reject_initial_setup_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.claim_credential_id <> NEW.claim_credential_id OR OLD.principal_id <> NEW.principal_id
       OR OLD.instance_id <> NEW.instance_id
       OR (OLD.closed_at IS NOT NULL AND NEW.closed_at IS DISTINCT FROM OLD.closed_at) THEN
        RAISE EXCEPTION 'initial password setup closure is permanent';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION access.reject_initial_setup_rewrite() FROM PUBLIC;
CREATE TRIGGER initial_password_setup_monotonic BEFORE UPDATE ON access.initial_password_setup
FOR EACH ROW EXECUTE FUNCTION access.reject_initial_setup_rewrite();
CREATE TRIGGER initial_password_setup_no_delete BEFORE DELETE ON access.initial_password_setup
FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER initial_publisher_origin_immutable BEFORE UPDATE ON access.initial_publisher_origin
FOR EACH ROW EXECUTE FUNCTION access.reject_authorization_policy_history_mutation();
CREATE TRIGGER initial_publisher_origin_no_delete BEFORE DELETE ON access.initial_publisher_origin
FOR EACH ROW EXECUTE FUNCTION access.allow_maintenance_delete();
REVOKE ALL ON access.initial_password_setup, access.initial_publisher_origin FROM PUBLIC;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_runtime') THEN
  GRANT SELECT, INSERT ON access.initial_password_setup, access.initial_publisher_origin TO leapview_control_runtime;
  GRANT UPDATE (closed_at) ON access.initial_password_setup TO leapview_control_runtime;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_backup') THEN
  GRANT SELECT ON access.initial_password_setup, access.initial_publisher_origin TO leapview_control_backup;
 END IF;
END $$;
-- +goose StatementEnd

RESET ROLE;

-- +goose Down
DO $$ BEGIN
 RAISE EXCEPTION 'initial publisher password setup migration is forward-only';
END $$;
