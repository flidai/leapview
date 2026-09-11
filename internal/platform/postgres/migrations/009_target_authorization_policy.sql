-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- FAI-863 target-owned authorization policy head and immutable revisions. The
-- head is only a CAS pointer; role bindings are copied into each revision so
-- historical release/recovery reads cannot resolve current mutable state.
CREATE TABLE IF NOT EXISTS access.authorization_policy (
    target_id text NOT NULL CHECK (target_id = btrim(target_id) AND length(target_id) BETWEEN 1 AND 255),
    project_id text NOT NULL CHECK (project_id = btrim(project_id) AND length(project_id) BETWEEN 1 AND 255),
    environment text NOT NULL CHECK (environment = btrim(environment) AND length(environment) BETWEEN 1 AND 255),
    revision bigint NOT NULL CHECK (revision > 0),
    digest text NOT NULL CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (target_id, project_id, environment)
);

CREATE TABLE IF NOT EXISTS access.authorization_policy_revision (
    target_id text NOT NULL,
    project_id text NOT NULL,
    environment text NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    digest text NOT NULL CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    source_generation_id text CHECK (source_generation_id IS NULL OR (source_generation_id = btrim(source_generation_id) AND length(source_generation_id) BETWEEN 1 AND 255)),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (target_id, project_id, environment, revision),
    FOREIGN KEY (target_id, project_id, environment)
        REFERENCES access.authorization_policy(target_id, project_id, environment)
        ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS access.authorization_policy_role_binding (
    target_id text NOT NULL,
    project_id text NOT NULL,
    environment text NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    id text NOT NULL CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 255),
    subject_kind text NOT NULL CHECK (subject_kind IN ('principal','group')),
    subject_id text NOT NULL CHECK (subject_id = btrim(subject_id) AND length(subject_id) BETWEEN 1 AND 255),
    role text NOT NULL CHECK (role IN ('owner','admin','deployer','data_deployer','contributor','editor','member','viewer')),
    capabilities jsonb NOT NULL CHECK (access.valid_capabilities(capabilities) AND jsonb_typeof(capabilities)='array' AND octet_length(capabilities::text)<=2048),
    name text NOT NULL DEFAULT '' CHECK (length(name)<=255),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (target_id, project_id, environment, revision, id),
    UNIQUE (target_id, project_id, environment, revision, subject_kind, subject_id, role),
    FOREIGN KEY (target_id, project_id, environment, revision)
        REFERENCES access.authorization_policy_revision(target_id, project_id, environment, revision)
        ON DELETE RESTRICT
);
CREATE INDEX IF NOT EXISTS authorization_policy_role_binding_subject_idx
    ON access.authorization_policy_role_binding(target_id, project_id, environment, revision, subject_kind, subject_id);

CREATE TABLE IF NOT EXISTS access.authorization_policy_operation (
    target_id text NOT NULL,
    project_id text NOT NULL,
    environment text NOT NULL,
    idempotency_key text NOT NULL CHECK (idempotency_key = btrim(idempotency_key) AND length(idempotency_key) BETWEEN 1 AND 256),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    revision bigint NOT NULL CHECK (revision > 0),
    policy_digest text NOT NULL CHECK (policy_digest ~ '^sha256:[0-9a-f]{64}$'),
    binding_id text NOT NULL CHECK (binding_id = btrim(binding_id) AND length(binding_id) BETWEEN 1 AND 255),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (target_id, project_id, environment, idempotency_key),
    FOREIGN KEY (target_id, project_id, environment, revision)
        REFERENCES access.authorization_policy_revision(target_id, project_id, environment, revision)
        ON DELETE RESTRICT
);

-- Legacy seals remain readable without invented policy identities. Every seal
-- written by the post-migration application supplies both columns; the pair check
-- prevents partial evidence while permitting pre-governance historical rows.
ALTER TABLE delivery.delivery_snapshot_seal
    ADD COLUMN IF NOT EXISTS authorization_policy_revision bigint CHECK (authorization_policy_revision > 0),
    ADD COLUMN IF NOT EXISTS authorization_policy_digest text CHECK (authorization_policy_digest ~ '^sha256:[0-9a-f]{64}$');
ALTER TABLE delivery.delivery_snapshot_seal
    DROP CONSTRAINT IF EXISTS delivery_snapshot_seal_authorization_policy_pair;
ALTER TABLE delivery.delivery_snapshot_seal
    ADD CONSTRAINT delivery_snapshot_seal_authorization_policy_pair
    CHECK ((authorization_policy_revision IS NULL) = (authorization_policy_digest IS NULL));

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_authorization_policy_revision_rewind() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.target_id <> OLD.target_id OR NEW.project_id <> OLD.project_id OR NEW.environment <> OLD.environment
       OR NEW.revision <> OLD.revision + 1 OR NEW.digest = OLD.digest THEN
        RAISE EXCEPTION 'authorization policy identity or revision is not monotonic';
    END IF;
    RETURN NEW;
END; $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS authorization_policy_revision_monotonic ON access.authorization_policy;
CREATE TRIGGER authorization_policy_revision_monotonic
    BEFORE UPDATE ON access.authorization_policy
    FOR EACH ROW EXECUTE FUNCTION access.reject_authorization_policy_revision_rewind();
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_authorization_policy_history_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'authorization policy history is immutable';
END; $$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS authorization_policy_revision_immutable ON access.authorization_policy_revision;
CREATE TRIGGER authorization_policy_revision_immutable
    BEFORE UPDATE ON access.authorization_policy_revision
    FOR EACH ROW EXECUTE FUNCTION access.reject_authorization_policy_history_mutation();
DROP TRIGGER IF EXISTS authorization_policy_role_binding_immutable ON access.authorization_policy_role_binding;
CREATE TRIGGER authorization_policy_role_binding_immutable
    BEFORE UPDATE ON access.authorization_policy_role_binding
    FOR EACH ROW EXECUTE FUNCTION access.reject_authorization_policy_history_mutation();
DROP TRIGGER IF EXISTS authorization_policy_operation_immutable ON access.authorization_policy_operation;
CREATE TRIGGER authorization_policy_operation_immutable
    BEFORE UPDATE ON access.authorization_policy_operation
    FOR EACH ROW EXECUTE FUNCTION access.reject_authorization_policy_history_mutation();

DROP TRIGGER IF EXISTS authorization_policy_no_delete ON access.authorization_policy;
CREATE TRIGGER authorization_policy_no_delete BEFORE DELETE ON access.authorization_policy FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
DROP TRIGGER IF EXISTS authorization_policy_revision_no_delete ON access.authorization_policy_revision;
CREATE TRIGGER authorization_policy_revision_no_delete BEFORE DELETE ON access.authorization_policy_revision FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
DROP TRIGGER IF EXISTS authorization_policy_role_binding_no_delete ON access.authorization_policy_role_binding;
CREATE TRIGGER authorization_policy_role_binding_no_delete BEFORE DELETE ON access.authorization_policy_role_binding FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
DROP TRIGGER IF EXISTS authorization_policy_operation_no_delete ON access.authorization_policy_operation;
CREATE TRIGGER authorization_policy_operation_no_delete BEFORE DELETE ON access.authorization_policy_operation FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();

REVOKE ALL ON TABLE access.authorization_policy, access.authorization_policy_revision,
    access.authorization_policy_role_binding, access.authorization_policy_operation FROM PUBLIC;
REVOKE ALL ON FUNCTION access.reject_authorization_policy_revision_rewind() FROM PUBLIC;
REVOKE ALL ON FUNCTION access.reject_authorization_policy_history_mutation() FROM PUBLIC;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT SELECT, INSERT, UPDATE ON access.authorization_policy,
            access.authorization_policy_revision, access.authorization_policy_role_binding,
            access.authorization_policy_operation TO leapview_control_runtime;
        REVOKE UPDATE ON access.authorization_policy_revision,
            access.authorization_policy_role_binding, access.authorization_policy_operation FROM leapview_control_runtime;
        REVOKE DELETE, TRUNCATE, REFERENCES, TRIGGER ON access.authorization_policy,
            access.authorization_policy_revision, access.authorization_policy_role_binding,
            access.authorization_policy_operation FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT SELECT ON access.authorization_policy, access.authorization_policy_revision,
            access.authorization_policy_role_binding, access.authorization_policy_operation TO leapview_control_readonly;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON access.authorization_policy,
            access.authorization_policy_revision, access.authorization_policy_role_binding,
            access.authorization_policy_operation FROM leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA access TO leapview_control_backup;
        GRANT SELECT ON access.authorization_policy, access.authorization_policy_revision,
            access.authorization_policy_role_binding, access.authorization_policy_operation TO leapview_control_backup;
    END IF;
END $$;
-- +goose StatementEnd
RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
-- Historical authorization evidence is not destructively downgraded.
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'target authorization policy revisions are immutable; destructive down is forbidden';
END $$;
-- +goose StatementEnd
RESET ROLE;
