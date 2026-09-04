-- FAI-616: mutable live access-control state.
-- Canonical role definitions remain Go-owned.  This migration stores only
-- instance/project assignments and explicit grants; generation-scoped
-- authorization snapshots are immutable evidence and are intentionally not
-- altered here.

SET ROLE leapview_control_owner;

CREATE TABLE IF NOT EXISTS access.control_state (
    instance_id    platform.resource_id PRIMARY KEY,
    project_id     platform.resource_id NOT NULL,
    revision       bigint NOT NULL DEFAULT 0 CHECK (revision >= 0),
    updated_at     timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE UNIQUE INDEX IF NOT EXISTS control_state_instance_project_key
    ON access.control_state (instance_id, project_id);

CREATE TABLE IF NOT EXISTS access.control_role_binding (
    id             platform.resource_id NOT NULL,
    instance_id    platform.resource_id NOT NULL,
    project_id     platform.resource_id NOT NULL,
    subject_kind   text NOT NULL CHECK (subject_kind IN ('principal', 'group')),
    subject_id     uuid NOT NULL,
    role           text NOT NULL CHECK (role IN (
        'owner', 'admin', 'deployer', 'data_deployer', 'contributor',
        'editor', 'member', 'viewer'
    )),
    name           text NOT NULL DEFAULT '' CHECK (length(name) <= 255),
    revision      bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at    timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at    timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at    timestamptz,
    PRIMARY KEY (instance_id, id),
    FOREIGN KEY (instance_id, project_id)
        REFERENCES access.control_state(instance_id, project_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS control_role_binding_active_key
    ON access.control_role_binding
       (instance_id, project_id, subject_kind, subject_id, role)
    WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS control_role_binding_instance_idx
    ON access.control_role_binding (instance_id, project_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS control_role_binding_subject_idx
    ON access.control_role_binding (instance_id, subject_kind, subject_id, revoked_at);

CREATE TABLE IF NOT EXISTS access.control_grant (
    id             platform.resource_id NOT NULL,
    instance_id    platform.resource_id NOT NULL,
    project_id     platform.resource_id NOT NULL,
    subject_kind   text NOT NULL CHECK (subject_kind IN ('principal', 'group')),
    subject_id     uuid NOT NULL,
    resource_id    platform.resource_id NOT NULL,
    resource_kind  text NOT NULL CHECK (resource_kind IN (
        'connection', 'source', 'model', 'semantic_model', 'pipeline', 'dashboard'
    )),
    capability    text NOT NULL CHECK (capability IN (
        'RESOURCE_USE', 'RESOURCE_READ', 'RESOURCE_EDIT',
        'RESOURCE_MANAGE', 'RESOURCE_SHARE', 'RESOURCE_PUBLISH'
    )),
    name           text NOT NULL DEFAULT '' CHECK (length(name) <= 255),
    revision      bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at    timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at    timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at    timestamptz,
    PRIMARY KEY (instance_id, id),
    FOREIGN KEY (instance_id, project_id)
        REFERENCES access.control_state(instance_id, project_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS control_grant_active_key
    ON access.control_grant
       (instance_id, project_id, subject_kind, subject_id, resource_id, resource_kind, capability)
    WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS control_grant_instance_project_idx
    ON access.control_grant (instance_id, project_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS control_grant_subject_idx
    ON access.control_grant (instance_id, subject_kind, subject_id, revoked_at);
CREATE INDEX IF NOT EXISTS control_grant_resource_idx
    ON access.control_grant (instance_id, project_id, resource_id, resource_kind, revoked_at);

-- Identity-bearing fields cannot be rewritten.  Mutable presentation and
-- revision fields are updated only by the access repository with a compare-
-- and-swap predicate.  Revocation is a tombstone, never a hard delete.
CREATE OR REPLACE FUNCTION access.reject_control_state_identity_rewrite()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.instance_id <> OLD.instance_id OR NEW.project_id <> OLD.project_id THEN
        RAISE EXCEPTION 'access control state identity is immutable';
    END IF;
    IF NEW.revision <= OLD.revision THEN
        RAISE EXCEPTION 'access control state revision must increase by one';
    END IF;
    IF NEW.revision <> OLD.revision + 1 THEN
        RAISE EXCEPTION 'access control state revision must increase by one';
    END IF;
    IF NEW.updated_at <= OLD.updated_at THEN
        RAISE EXCEPTION 'access control state timestamp must increase';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS control_state_identity_immutable ON access.control_state;
CREATE TRIGGER control_state_identity_immutable
    BEFORE UPDATE ON access.control_state
    FOR EACH ROW EXECUTE FUNCTION access.reject_control_state_identity_rewrite();

CREATE OR REPLACE FUNCTION access.reject_control_role_binding_identity_rewrite()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.id <> OLD.id
       OR NEW.instance_id <> OLD.instance_id
       OR NEW.project_id <> OLD.project_id
       OR NEW.subject_kind <> OLD.subject_kind
       OR NEW.subject_id <> OLD.subject_id
       OR NEW.role <> OLD.role THEN
        RAISE EXCEPTION 'control role binding identity is immutable';
    END IF;
    IF NEW.revision <> OLD.revision + 1 THEN
        RAISE EXCEPTION 'control role binding revision must increase by one';
    END IF;
    IF NEW.updated_at <= OLD.updated_at THEN
        RAISE EXCEPTION 'control role binding timestamp must increase';
    END IF;
    IF OLD.revoked_at IS NOT NULL AND NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'revoked control role binding is immutable';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS control_role_binding_identity_immutable ON access.control_role_binding;
CREATE TRIGGER control_role_binding_identity_immutable
    BEFORE UPDATE ON access.control_role_binding
    FOR EACH ROW EXECUTE FUNCTION access.reject_control_role_binding_identity_rewrite();

CREATE OR REPLACE FUNCTION access.reject_control_grant_identity_rewrite()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.id <> OLD.id
       OR NEW.instance_id <> OLD.instance_id
       OR NEW.project_id <> OLD.project_id
       OR NEW.resource_id <> OLD.resource_id
       OR NEW.resource_kind <> OLD.resource_kind THEN
        RAISE EXCEPTION 'control grant identity is immutable';
    END IF;
    IF NEW.revision <> OLD.revision + 1 THEN
        RAISE EXCEPTION 'control grant revision must increase by one';
    END IF;
    IF NEW.updated_at <= OLD.updated_at THEN
        RAISE EXCEPTION 'control grant timestamp must increase';
    END IF;
    IF OLD.revoked_at IS NOT NULL AND NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'revoked control grant is immutable';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS control_grant_identity_immutable ON access.control_grant;
CREATE TRIGGER control_grant_identity_immutable
    BEFORE UPDATE ON access.control_grant
    FOR EACH ROW EXECUTE FUNCTION access.reject_control_grant_identity_rewrite();

-- Every row mutation must be paired with the instance-wide revision advance
-- performed by the repository in the same transaction. This preserves the
-- projection invalidation fence even if a future adapter writes these tables.
CREATE OR REPLACE FUNCTION access.require_control_state_advance()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM access.control_state state
        WHERE state.instance_id = NEW.instance_id
          AND state.project_id = NEW.project_id
          AND state.updated_at >= NEW.updated_at
    ) THEN
        RAISE EXCEPTION 'live control row mutation requires control state revision advance';
    END IF;
    RETURN NULL;
END;
$$;

DROP TRIGGER IF EXISTS control_role_binding_state_advanced ON access.control_role_binding;
CREATE CONSTRAINT TRIGGER control_role_binding_state_advanced
    AFTER INSERT OR UPDATE ON access.control_role_binding
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION access.require_control_state_advance();
DROP TRIGGER IF EXISTS control_grant_state_advanced ON access.control_grant;
CREATE CONSTRAINT TRIGGER control_grant_state_advanced
    AFTER INSERT OR UPDATE ON access.control_grant
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION access.require_control_state_advance();

-- Grant/reference identity and terminal revocation lifecycle are checked at
-- commit so the repository can create or suspend the reference later in the
-- same transaction without exposing a transiently valid state.
CREATE OR REPLACE FUNCTION access.require_control_grant_reference()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM project.durable_resource_reference ref
        WHERE ref.instance_id = NEW.instance_id
          AND ref.reference_id = ('grant:' || NEW.id::text)
          AND ref.owner_authored_id = NEW.id
          AND ref.owner_kind = 'grant'
          AND ref.target_authored_id = NEW.resource_id
          AND ref.expected_kind = NEW.resource_kind
          AND (NEW.revoked_at IS NULL OR ref.lifecycle_state = 'suspended')
    ) THEN
        RAISE EXCEPTION 'control grant requires its exact durable reference';
    END IF;
    RETURN NULL;
END;
$$;

DROP TRIGGER IF EXISTS control_grant_reference_consistent ON access.control_grant;
CREATE CONSTRAINT TRIGGER control_grant_reference_consistent
    AFTER INSERT OR UPDATE ON access.control_grant
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION access.require_control_grant_reference();

CREATE OR REPLACE FUNCTION access.reject_live_access_delete()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'live access-control rows must be revoked, not deleted';
END;
$$;

DROP TRIGGER IF EXISTS control_role_binding_no_delete ON access.control_role_binding;
CREATE TRIGGER control_role_binding_no_delete
    BEFORE DELETE ON access.control_role_binding
    FOR EACH ROW EXECUTE FUNCTION access.reject_live_access_delete();
DROP TRIGGER IF EXISTS control_grant_no_delete ON access.control_grant;
CREATE TRIGGER control_grant_no_delete
    BEFORE DELETE ON access.control_grant
    FOR EACH ROW EXECUTE FUNCTION access.reject_live_access_delete();
DROP TRIGGER IF EXISTS control_state_no_delete ON access.control_state;
CREATE TRIGGER control_state_no_delete
    BEFORE DELETE ON access.control_state
    FOR EACH ROW EXECUTE FUNCTION access.reject_live_access_delete();

REVOKE ALL ON TABLE access.control_state, access.control_role_binding, access.control_grant FROM PUBLIC;
REVOKE ALL ON FUNCTION access.reject_control_role_binding_identity_rewrite() FROM PUBLIC;
REVOKE ALL ON FUNCTION access.reject_control_state_identity_rewrite() FROM PUBLIC;
REVOKE ALL ON FUNCTION access.reject_control_grant_identity_rewrite() FROM PUBLIC;
REVOKE ALL ON FUNCTION access.require_control_state_advance() FROM PUBLIC;
REVOKE ALL ON FUNCTION access.require_control_grant_reference() FROM PUBLIC;
REVOKE ALL ON FUNCTION access.reject_live_access_delete() FROM PUBLIC;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA access TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON access.control_state, access.control_role_binding, access.control_grant
            TO leapview_control_runtime;
        REVOKE DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON access.control_state, access.control_role_binding, access.control_grant
            FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA access TO leapview_control_readonly;
        GRANT SELECT ON access.control_state, access.control_role_binding, access.control_grant
            TO leapview_control_readonly;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON access.control_state, access.control_role_binding, access.control_grant
            FROM leapview_control_readonly;
    END IF;
END
$$;

RESET ROLE;
