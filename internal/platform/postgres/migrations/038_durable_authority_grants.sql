-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- Independent authority is intentionally separate from
-- access.authorization_grant, whose identity is bound to one serving
-- generation. These records survive generation changes, but resource grants
-- retain the instance-local ResourceUID and therefore cannot revive after an
-- authored resource is tombstoned and recreated.
CREATE TABLE access.resource_share_grant (
    id text PRIMARY KEY CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 255),
    profile text NOT NULL CHECK (profile = 'leapview.durable-grants/v1'),
    instance_id text NOT NULL,
    project_id text NOT NULL,
    resource_uid uuid NOT NULL,
    resource_id text NOT NULL,
    resource_kind text NOT NULL CHECK (resource_kind IN ('connection','source','model','semantic_model','pipeline','dashboard')),
    issuer_principal_id uuid NOT NULL REFERENCES access.principal(id),
    issuer_credential_class text NOT NULL CHECK (issuer_credential_class IN ('session','api_token')),
    issuer_credential_id text NOT NULL CHECK (issuer_credential_id = btrim(issuer_credential_id) AND length(issuer_credential_id) BETWEEN 1 AND 512),
    issuer_credential_fingerprint text NOT NULL CHECK (issuer_credential_fingerprint = btrim(issuer_credential_fingerprint) AND length(issuer_credential_fingerprint) BETWEEN 16 AND 512),
    recipient_kind text NOT NULL CHECK (recipient_kind IN ('principal','group')),
    recipient_id uuid NOT NULL,
    permission_profile text NOT NULL CHECK (permission_profile = 'leapview.permissions/v1'),
    permissions jsonb NOT NULL CHECK (access.valid_permission_pairs(permission_profile, permissions) AND jsonb_array_length(permissions) BETWEEN 1 AND 64),
    issued_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz,
    fingerprint text NOT NULL CHECK (fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    idempotency_key text NOT NULL CHECK (idempotency_key = btrim(idempotency_key) AND length(idempotency_key) BETWEEN 1 AND 256),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    allow_onward_delegation boolean NOT NULL DEFAULT false,
    revoked_at timestamptz,
    revoked_by_principal_id uuid REFERENCES access.principal(id),
    revocation_reason text NOT NULL DEFAULT '' CHECK (length(revocation_reason) <= 1024),
    UNIQUE (fingerprint),
    UNIQUE (issuer_principal_id, issuer_credential_id, idempotency_key),
    FOREIGN KEY (instance_id, project_id, resource_uid)
        REFERENCES project.resource_uid_registry(instance_id, project_id, resource_uid) ON DELETE RESTRICT,
    CHECK (instance_id = btrim(instance_id) AND length(instance_id) BETWEEN 1 AND 255),
    CHECK (project_id = btrim(project_id) AND length(project_id) BETWEEN 1 AND 255),
    CHECK (resource_id = btrim(resource_id) AND length(resource_id) BETWEEN 1 AND 255),
    CHECK (expires_at IS NULL OR (expires_at > issued_at AND expires_at <= issued_at + interval '365 days')),
    CHECK (revoked_at IS NULL OR (revoked_by_principal_id IS NOT NULL AND revoked_at >= issued_at))
);
CREATE INDEX resource_share_grant_recipient_idx ON access.resource_share_grant(recipient_kind, recipient_id, project_id, resource_uid) WHERE revoked_at IS NULL;
CREATE INDEX resource_share_grant_target_idx ON access.resource_share_grant(instance_id, project_id, resource_uid) WHERE revoked_at IS NULL;

CREATE TABLE access.execution_grant (
    id text PRIMARY KEY CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 255),
    profile text NOT NULL CHECK (profile = 'leapview.durable-grants/v1'),
    instance_id text NOT NULL,
    project_id text NOT NULL,
    resource_uid uuid NOT NULL,
    resource_id text NOT NULL,
    resource_kind text NOT NULL CHECK (resource_kind = 'pipeline'),
    issuer_principal_id uuid NOT NULL REFERENCES access.principal(id),
    issuer_credential_class text NOT NULL CHECK (issuer_credential_class IN ('session','api_token')),
    issuer_credential_id text NOT NULL CHECK (issuer_credential_id = btrim(issuer_credential_id) AND length(issuer_credential_id) BETWEEN 1 AND 512),
    issuer_credential_fingerprint text NOT NULL CHECK (issuer_credential_fingerprint = btrim(issuer_credential_fingerprint) AND length(issuer_credential_fingerprint) BETWEEN 16 AND 512),
    execution_principal_id uuid NOT NULL REFERENCES access.principal(id),
    permission_profile text NOT NULL CHECK (permission_profile = 'leapview.permissions/v1'),
    permissions jsonb NOT NULL CHECK (access.valid_permission_pairs(permission_profile, permissions) AND jsonb_array_length(permissions) BETWEEN 1 AND 64),
    workflow_id text NOT NULL CHECK (workflow_id = btrim(workflow_id) AND length(workflow_id) BETWEEN 1 AND 512),
    workflow_revision text NOT NULL CHECK (workflow_revision = btrim(workflow_revision) AND length(workflow_revision) BETWEEN 1 AND 512),
    closure_digest text NOT NULL CHECK (closure_digest ~ '^sha256:[0-9a-f]{64}$'),
    binding_digest text NOT NULL CHECK (binding_digest ~ '^sha256:[0-9a-f]{64}$'),
    destination_digest text NOT NULL CHECK (destination_digest ~ '^sha256:[0-9a-f]{64}$'),
    trigger_digest text NOT NULL CHECK (trigger_digest ~ '^sha256:[0-9a-f]{64}$'),
    issued_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    fingerprint text NOT NULL CHECK (fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    idempotency_key text NOT NULL CHECK (idempotency_key = btrim(idempotency_key) AND length(idempotency_key) BETWEEN 1 AND 256),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    revoked_at timestamptz,
    revoked_by_principal_id uuid REFERENCES access.principal(id),
    revocation_reason text NOT NULL DEFAULT '' CHECK (length(revocation_reason) <= 1024),
    UNIQUE (fingerprint),
    UNIQUE (issuer_principal_id, issuer_credential_id, idempotency_key),
    FOREIGN KEY (instance_id, project_id, resource_uid)
        REFERENCES project.resource_uid_registry(instance_id, project_id, resource_uid) ON DELETE RESTRICT,
    CHECK (instance_id = btrim(instance_id) AND length(instance_id) BETWEEN 1 AND 255),
    CHECK (project_id = btrim(project_id) AND length(project_id) BETWEEN 1 AND 255),
    CHECK (resource_id = btrim(resource_id) AND length(resource_id) BETWEEN 1 AND 255),
    CHECK (expires_at > issued_at AND expires_at <= issued_at + interval '365 days'),
    CHECK (revoked_at IS NULL OR (revoked_by_principal_id IS NOT NULL AND revoked_at >= issued_at))
);
CREATE INDEX execution_grant_principal_idx ON access.execution_grant(execution_principal_id, project_id, resource_uid) WHERE revoked_at IS NULL;
CREATE INDEX execution_grant_target_idx ON access.execution_grant(instance_id, project_id, resource_uid) WHERE revoked_at IS NULL;

CREATE TABLE access.grant_admin_envelope (
    id text PRIMARY KEY CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 255),
    profile text NOT NULL CHECK (profile = 'leapview.durable-grants/v1'),
    issuer_principal_id uuid NOT NULL REFERENCES access.principal(id),
    issuer_credential_class text NOT NULL CHECK (issuer_credential_class IN ('session','api_token')),
    issuer_credential_id text NOT NULL CHECK (issuer_credential_id = btrim(issuer_credential_id) AND length(issuer_credential_id) BETWEEN 1 AND 512),
    issuer_credential_fingerprint text NOT NULL CHECK (issuer_credential_fingerprint = btrim(issuer_credential_fingerprint) AND length(issuer_credential_fingerprint) BETWEEN 16 AND 512),
    bound_principal_id uuid NOT NULL REFERENCES access.principal(id),
    permission_profile text NOT NULL CHECK (permission_profile = 'leapview.permissions/v1'),
    permissions jsonb NOT NULL CHECK (access.valid_permission_pairs(permission_profile, permissions) AND jsonb_array_length(permissions) BETWEEN 1 AND 64),
    target_project_id text NOT NULL CHECK (target_project_id = btrim(target_project_id) AND length(target_project_id) BETWEEN 1 AND 255),
    target_resource_kind text CHECK (target_resource_kind IS NULL OR target_resource_kind IN ('connection','source','model','semantic_model','pipeline','dashboard')),
    target_resource_id text CHECK (target_resource_id IS NULL OR (target_resource_id = btrim(target_resource_id) AND length(target_resource_id) BETWEEN 1 AND 255)),
    recipient_selector text NOT NULL CHECK (recipient_selector = btrim(recipient_selector) AND length(recipient_selector) BETWEEN 1 AND 1024),
    role_version text NOT NULL CHECK (role_version = btrim(role_version) AND length(role_version) BETWEEN 1 AND 255),
    issued_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    fingerprint text NOT NULL CHECK (fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    idempotency_key text NOT NULL CHECK (idempotency_key = btrim(idempotency_key) AND length(idempotency_key) BETWEEN 1 AND 256),
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    allow_onward_delegation boolean NOT NULL DEFAULT false,
    revoked_at timestamptz,
    revoked_by_principal_id uuid REFERENCES access.principal(id),
    revocation_reason text NOT NULL DEFAULT '' CHECK (length(revocation_reason) <= 1024),
    UNIQUE (fingerprint),
    UNIQUE (issuer_principal_id, issuer_credential_id, idempotency_key),
    CHECK ((target_resource_kind IS NULL) = (target_resource_id IS NULL)),
    CHECK (expires_at > issued_at AND expires_at <= issued_at + interval '365 days'),
    CHECK (revoked_at IS NULL OR (revoked_by_principal_id IS NOT NULL AND revoked_at >= issued_at))
);
CREATE INDEX grant_admin_envelope_principal_idx ON access.grant_admin_envelope(bound_principal_id, target_project_id) WHERE revoked_at IS NULL;

-- Resource identities are checked against the UID registry at issuance. A
-- tombstoned registry row remains for history, but cannot receive a new grant.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.validate_durable_resource_target()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, access, project AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM project.resource_uid_registry r
        WHERE r.instance_id = NEW.instance_id AND r.project_id = NEW.project_id
          AND r.resource_uid = NEW.resource_uid AND r.authored_resource_id = NEW.resource_id
          AND r.resource_kind = NEW.resource_kind AND r.state = 'active'
    ) THEN
        RAISE EXCEPTION 'durable grant exact resource target is not active or does not match its ResourceUID';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER resource_share_grant_resource_target
    BEFORE INSERT ON access.resource_share_grant FOR EACH ROW EXECUTE FUNCTION access.validate_durable_resource_target();
CREATE TRIGGER execution_grant_resource_target
    BEFORE INSERT ON access.execution_grant FOR EACH ROW EXECUTE FUNCTION access.validate_durable_resource_target();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.validate_durable_share_recipient()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, access AS $$
BEGIN
    IF NEW.recipient_kind = 'principal' THEN
        IF NOT EXISTS (SELECT 1 FROM access.principal p WHERE p.id = NEW.recipient_id) THEN
            RAISE EXCEPTION 'durable share recipient principal does not exist';
        END IF;
    ELSIF NOT EXISTS (SELECT 1 FROM access.access_group g WHERE g.id = NEW.recipient_id) THEN
        RAISE EXCEPTION 'durable share recipient group does not exist';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER resource_share_grant_recipient
    BEFORE INSERT ON access.resource_share_grant FOR EACH ROW EXECUTE FUNCTION access.validate_durable_share_recipient();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION access.reject_durable_grant_mutation()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, access AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'durable grant history is append-only; revoke instead of delete';
    END IF;
    IF OLD.id IS DISTINCT FROM NEW.id OR OLD.profile IS DISTINCT FROM NEW.profile
       OR OLD.issuer_principal_id IS DISTINCT FROM NEW.issuer_principal_id
       OR OLD.issuer_credential_class IS DISTINCT FROM NEW.issuer_credential_class
       OR OLD.issuer_credential_id IS DISTINCT FROM NEW.issuer_credential_id
       OR OLD.issuer_credential_fingerprint IS DISTINCT FROM NEW.issuer_credential_fingerprint
       OR OLD.permission_profile IS DISTINCT FROM NEW.permission_profile
       OR OLD.permissions IS DISTINCT FROM NEW.permissions
       OR OLD.issued_at IS DISTINCT FROM NEW.issued_at
       OR OLD.expires_at IS DISTINCT FROM NEW.expires_at
       OR OLD.fingerprint IS DISTINCT FROM NEW.fingerprint
       OR OLD.idempotency_key IS DISTINCT FROM NEW.idempotency_key
       OR OLD.request_digest IS DISTINCT FROM NEW.request_digest THEN
        RAISE EXCEPTION 'durable grant issuance evidence is immutable';
    END IF;
    IF TG_TABLE_NAME = 'resource_share_grant' THEN
        IF OLD.instance_id IS DISTINCT FROM NEW.instance_id OR OLD.project_id IS DISTINCT FROM NEW.project_id
           OR OLD.resource_uid IS DISTINCT FROM NEW.resource_uid OR OLD.resource_id IS DISTINCT FROM NEW.resource_id
           OR OLD.resource_kind IS DISTINCT FROM NEW.resource_kind OR OLD.recipient_kind IS DISTINCT FROM NEW.recipient_kind
           OR OLD.recipient_id IS DISTINCT FROM NEW.recipient_id
           OR OLD.allow_onward_delegation IS DISTINCT FROM NEW.allow_onward_delegation THEN
            RAISE EXCEPTION 'resource share grant identity is immutable';
        END IF;
    ELSIF TG_TABLE_NAME = 'execution_grant' THEN
        IF OLD.instance_id IS DISTINCT FROM NEW.instance_id OR OLD.project_id IS DISTINCT FROM NEW.project_id
           OR OLD.resource_uid IS DISTINCT FROM NEW.resource_uid OR OLD.resource_id IS DISTINCT FROM NEW.resource_id
           OR OLD.resource_kind IS DISTINCT FROM NEW.resource_kind OR OLD.execution_principal_id IS DISTINCT FROM NEW.execution_principal_id
           OR OLD.workflow_id IS DISTINCT FROM NEW.workflow_id OR OLD.workflow_revision IS DISTINCT FROM NEW.workflow_revision
           OR OLD.closure_digest IS DISTINCT FROM NEW.closure_digest OR OLD.binding_digest IS DISTINCT FROM NEW.binding_digest
           OR OLD.destination_digest IS DISTINCT FROM NEW.destination_digest OR OLD.trigger_digest IS DISTINCT FROM NEW.trigger_digest THEN
            RAISE EXCEPTION 'execution grant identity is immutable';
        END IF;
    ELSIF TG_TABLE_NAME = 'grant_admin_envelope' THEN
        IF OLD.bound_principal_id IS DISTINCT FROM NEW.bound_principal_id OR OLD.target_project_id IS DISTINCT FROM NEW.target_project_id
           OR OLD.target_resource_kind IS DISTINCT FROM NEW.target_resource_kind OR OLD.target_resource_id IS DISTINCT FROM NEW.target_resource_id
           OR OLD.recipient_selector IS DISTINCT FROM NEW.recipient_selector OR OLD.role_version IS DISTINCT FROM NEW.role_version
           OR OLD.allow_onward_delegation IS DISTINCT FROM NEW.allow_onward_delegation THEN
            RAISE EXCEPTION 'grant administration envelope identity is immutable';
        END IF;
    END IF;
    IF OLD.revoked_at IS NOT NULL THEN
        IF NEW.revoked_at IS NULL OR NEW.revoked_at < OLD.revoked_at
           OR NEW.revoked_by_principal_id IS DISTINCT FROM OLD.revoked_by_principal_id
           OR NEW.revocation_reason IS DISTINCT FROM OLD.revocation_reason THEN
            RAISE EXCEPTION 'durable grant revocation is monotonic';
        END IF;
    ELSIF NEW.revoked_at IS NOT NULL AND NEW.revoked_by_principal_id IS NULL THEN
        RAISE EXCEPTION 'durable grant revocation principal is required';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER resource_share_grant_immutable
    BEFORE UPDATE OR DELETE ON access.resource_share_grant FOR EACH ROW EXECUTE FUNCTION access.reject_durable_grant_mutation();
CREATE TRIGGER execution_grant_immutable
    BEFORE UPDATE OR DELETE ON access.execution_grant FOR EACH ROW EXECUTE FUNCTION access.reject_durable_grant_mutation();
CREATE TRIGGER grant_admin_envelope_immutable
    BEFORE UPDATE OR DELETE ON access.grant_admin_envelope FOR EACH ROW EXECUTE FUNCTION access.reject_durable_grant_mutation();

REVOKE ALL ON TABLE access.resource_share_grant, access.execution_grant, access.grant_admin_envelope FROM PUBLIC;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT SELECT, INSERT, UPDATE ON access.resource_share_grant, access.execution_grant, access.grant_admin_envelope TO leapview_control_runtime;
        REVOKE DELETE ON access.resource_share_grant, access.execution_grant, access.grant_admin_envelope FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT SELECT ON access.resource_share_grant, access.execution_grant, access.grant_admin_envelope TO leapview_control_readonly;
    END IF;
END $$;
-- +goose StatementEnd

RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
-- +goose StatementBegin
DO $$
BEGIN
    RAISE EXCEPTION 'durable authority grant migration is immutable; destructive down is forbidden';
END;
$$;
-- +goose StatementEnd
RESET ROLE;
