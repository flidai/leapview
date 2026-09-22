-- +goose Up
SET LOCAL ROLE leapview_control_owner;

CREATE TABLE IF NOT EXISTS access.authorization_policy_grant (
    target_id text NOT NULL,
    project_id text NOT NULL,
    environment text NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    id text NOT NULL CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 255),
    subject_kind text NOT NULL CHECK (subject_kind IN ('principal','group')),
    subject_id text NOT NULL CHECK (subject_id = btrim(subject_id) AND length(subject_id) BETWEEN 1 AND 255),
    resource_id text NOT NULL CHECK (resource_id = btrim(resource_id) AND length(resource_id) BETWEEN 1 AND 255),
    resource_kind text NOT NULL CHECK (resource_kind IN ('project','connection','source','model','semantic_model','pipeline','dashboard')),
    capability text NOT NULL CHECK (capability IN ('RESOURCE_USE','RESOURCE_READ','RESOURCE_EDIT','RESOURCE_MANAGE','RESOURCE_SHARE','RESOURCE_PUBLISH','PROJECT_ADMIN')),
    name text NOT NULL DEFAULT '' CHECK (length(name)<=255),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (target_id, project_id, environment, revision, id),
    UNIQUE (target_id, project_id, environment, revision, subject_kind, subject_id, resource_kind, resource_id, capability),
    FOREIGN KEY (target_id, project_id, environment, revision)
        REFERENCES access.authorization_policy_revision(target_id, project_id, environment, revision)
        ON DELETE RESTRICT
);

CREATE TRIGGER authorization_policy_grant_immutable BEFORE UPDATE ON access.authorization_policy_grant
FOR EACH ROW EXECUTE FUNCTION access.reject_authorization_policy_history_mutation();
CREATE TRIGGER authorization_policy_grant_no_delete BEFORE DELETE ON access.authorization_policy_grant
FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
REVOKE ALL ON access.authorization_policy_grant FROM PUBLIC;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_runtime') THEN
  GRANT SELECT, INSERT ON access.authorization_policy_grant TO leapview_control_runtime;
  REVOKE UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON access.authorization_policy_grant FROM leapview_control_runtime;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_readonly') THEN
  GRANT SELECT ON access.authorization_policy_grant TO leapview_control_readonly;
  REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON access.authorization_policy_grant FROM leapview_control_readonly;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_backup') THEN
  GRANT SELECT ON access.authorization_policy_grant TO leapview_control_backup;
 END IF;
END $$;
-- +goose StatementEnd
RESET ROLE;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 RAISE EXCEPTION 'authorization grant history is forward-only; restore a coordinated backup to downgrade';
END $$;
-- +goose StatementEnd
