-- Clean PostgreSQL access authority baseline. This file is applied to an
-- empty control database by the access capability migration; it deliberately
-- contains no compatibility ALTERs or SQLite-era projections.
CREATE SCHEMA IF NOT EXISTS access;

CREATE TABLE access.principal (
    id uuid PRIMARY KEY,
    principal_type text NOT NULL CHECK (principal_type IN ('user','service','system')),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled','pending')),
    email text NOT NULL DEFAULT '' CHECK (length(email) <= 320),
    display_name text NOT NULL DEFAULT '' CHECK (length(display_name) <= 512),
    disabled_at timestamptz,
    blocked_at timestamptz,
    revoked_at timestamptz,
    last_seen_at timestamptz,
    attributes jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(attributes)='object' AND octet_length(attributes::text)<=16384),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
    ,CHECK ((status='active' AND disabled_at IS NULL) OR status<>'active')
    ,CHECK (status<>'disabled' OR disabled_at IS NOT NULL OR revoked_at IS NOT NULL)
    ,CHECK (status<>'pending' OR disabled_at IS NULL)
    ,CHECK (revoked_at IS NULL OR status='disabled')
);
CREATE UNIQUE INDEX principal_email_active_key ON access.principal (lower(email)) WHERE email <> '' AND revoked_at IS NULL;

CREATE TABLE access.external_identity (
    id uuid PRIMARY KEY,
    principal_id uuid NOT NULL REFERENCES access.principal(id),
    provider text NOT NULL CHECK (provider = btrim(provider) AND length(provider) BETWEEN 1 AND 128),
    tenant_id text NOT NULL DEFAULT '' CHECK (tenant_id = btrim(tenant_id) AND length(tenant_id) <= 255),
    subject text NOT NULL CHECK (subject = btrim(subject) AND length(subject) BETWEEN 1 AND 512),
    user_name text NOT NULL DEFAULT '' CHECK (length(user_name) <= 320),
    external_id text NOT NULL DEFAULT '' CHECK (length(external_id) <= 512),
    email text NOT NULL DEFAULT '' CHECK (length(email) <= 320),
    display_name text NOT NULL DEFAULT '' CHECK (length(display_name) <= 512),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz
);
CREATE UNIQUE INDEX external_identity_active_key ON access.external_identity(provider, tenant_id, subject) WHERE revoked_at IS NULL;
CREATE UNIQUE INDEX external_identity_active_external_id ON access.external_identity(provider, tenant_id, external_id) WHERE external_id <> '' AND revoked_at IS NULL;

CREATE TABLE access.platform_role_binding (
    id uuid PRIMARY KEY,
    principal_id uuid NOT NULL REFERENCES access.principal(id),
    role text NOT NULL CHECK (role = 'platform_admin'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz
);
CREATE UNIQUE INDEX platform_role_binding_active_key ON access.platform_role_binding(principal_id, role) WHERE revoked_at IS NULL;

CREATE TABLE access.access_group (
    id uuid PRIMARY KEY,
    name text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 255),
    provider text NOT NULL DEFAULT '' CHECK (length(provider) <= 255),
    external_id text NOT NULL DEFAULT '' CHECK (length(external_id) <= 512),
    attributes jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(attributes)='object' AND octet_length(attributes::text)<=16384),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz
);
-- NULLIF intentionally permits multiple local ('','') groups while keeping
-- provider/external identities unique while active.
CREATE UNIQUE INDEX access_group_active_key ON access.access_group(provider, NULLIF(external_id,'')) WHERE revoked_at IS NULL;

CREATE TABLE access.principal_group (
    membership_id uuid PRIMARY KEY DEFAULT uuidv7(),
    principal_id uuid NOT NULL REFERENCES access.principal(id),
    group_id uuid NOT NULL REFERENCES access.access_group(id),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz
);
CREATE UNIQUE INDEX principal_group_active_key ON access.principal_group(principal_id, group_id) WHERE revoked_at IS NULL;

CREATE TABLE access.session (
    id uuid PRIMARY KEY,
    principal_id uuid NOT NULL REFERENCES access.principal(id),
    token_fingerprint bytea NOT NULL UNIQUE,
    verifier bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    last_seen_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz,
    kind text NOT NULL DEFAULT 'browser' CHECK (kind IN ('browser','desktop')),
    instance_id text NOT NULL DEFAULT '' CHECK (length(instance_id) <= 128),
    profile_id text NOT NULL DEFAULT '' CHECK (length(profile_id) <= 128),
    client_id text NOT NULL DEFAULT '' CHECK (length(client_id) <= 255),
    absolute_expires_at timestamptz,
    CHECK (expires_at > created_at),
    CHECK (absolute_expires_at IS NULL OR absolute_expires_at >= expires_at),
    CHECK (octet_length(token_fingerprint)=32),
    CHECK (octet_length(verifier) BETWEEN 32 AND 512),
    CHECK ((kind='browser' AND instance_id='' AND profile_id='' AND client_id='' AND absolute_expires_at IS NULL) OR (kind='desktop' AND instance_id<>'' AND profile_id<>'' AND client_id<>'' AND absolute_expires_at IS NOT NULL))
);
CREATE INDEX access_session_active_fp_idx ON access.session(token_fingerprint) WHERE revoked_at IS NULL;
CREATE INDEX access_session_principal_idx ON access.session(principal_id, created_at DESC);

CREATE TABLE access.local_credential (
    principal_id uuid PRIMARY KEY REFERENCES access.principal(id),
    verifier bytea NOT NULL,
    must_change boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    password_changed_at timestamptz,
    revoked_at timestamptz,
    CHECK (octet_length(verifier) BETWEEN 32 AND 512)
);

CREATE FUNCTION access.valid_capabilities(value jsonb) RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE item text; seen_items jsonb := '[]'::jsonb;
BEGIN
    IF value IS NULL THEN RETURN TRUE; END IF;
    IF jsonb_typeof(value) <> 'array' THEN RETURN FALSE; END IF;
    FOR item IN SELECT jsonb_array_elements_text(value) LOOP
        IF item NOT IN ('PROJECT_ADMIN','RESOURCE_USE','RESOURCE_READ','RESOURCE_EDIT','RESOURCE_MANAGE','RESOURCE_SHARE','RESOURCE_PUBLISH') THEN RETURN FALSE; END IF;
        IF seen_items ? item THEN RETURN FALSE; END IF;
        seen_items := seen_items || to_jsonb(item);
    END LOOP;
    RETURN TRUE;
END $$;

CREATE TABLE access.api_token (
    id uuid PRIMARY KEY,
    principal_id uuid NOT NULL REFERENCES access.principal(id),
    name text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 255),
    token_fingerprint bytea NOT NULL UNIQUE,
    verifier bytea NOT NULL,
    capabilities jsonb CHECK (access.valid_capabilities(capabilities)),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    last_used_at timestamptz,
    revoked_at timestamptz,
    CHECK (octet_length(token_fingerprint)=32),
    CHECK (octet_length(verifier) BETWEEN 32 AND 512),
    CHECK (expires_at > created_at AND expires_at <= created_at + interval '365 days')
);
CREATE INDEX access_api_token_principal_idx ON access.api_token(principal_id, created_at DESC);
CREATE INDEX access_api_token_active_fp_idx ON access.api_token(token_fingerprint) WHERE revoked_at IS NULL;

CREATE TABLE access.service_principal_secret (
    id uuid PRIMARY KEY,
    service_principal_id uuid NOT NULL REFERENCES access.principal(id),
    name text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 255),
    secret_fingerprint bytea NOT NULL UNIQUE,
    verifier bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz,
    CHECK (octet_length(secret_fingerprint)=32),
    CHECK (octet_length(verifier) BETWEEN 32 AND 512),
    CHECK (expires_at > created_at AND expires_at <= created_at + interval '365 days')
);
CREATE INDEX service_secret_principal_idx ON access.service_principal_secret(service_principal_id, created_at DESC);

CREATE OR REPLACE FUNCTION access.reject_access_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'access history is append-only; revoke instead of delete'; END; $$;
CREATE OR REPLACE FUNCTION access.reject_revocation_clear() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF OLD.revoked_at IS NOT NULL AND (NEW.revoked_at IS NULL OR NEW.revoked_at < OLD.revoked_at) THEN RAISE EXCEPTION 'revocation is monotonic'; END IF; RETURN NEW; END; $$;
CREATE OR REPLACE FUNCTION access.reject_principal_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.id <> NEW.id OR OLD.principal_type <> NEW.principal_type OR OLD.created_at <> NEW.created_at THEN
        RAISE EXCEPTION 'principal identity is immutable';
    END IF;
    IF NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'principal updated_at is monotonic';
    END IF;
    RETURN NEW;
END; $$;
CREATE OR REPLACE FUNCTION access.reject_group_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF OLD.id <> NEW.id OR OLD.provider <> NEW.provider OR OLD.external_id <> NEW.external_id OR OLD.created_at <> NEW.created_at THEN RAISE EXCEPTION 'group identity is immutable'; END IF; RETURN NEW; END; $$;
CREATE OR REPLACE FUNCTION access.reject_role_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF OLD.id<>NEW.id OR OLD.principal_id<>NEW.principal_id OR OLD.role<>NEW.role OR OLD.created_at<>NEW.created_at THEN RAISE EXCEPTION 'role identity is immutable'; END IF; RETURN NEW; END; $$;
CREATE OR REPLACE FUNCTION access.reject_membership_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF OLD.membership_id<>NEW.membership_id OR OLD.principal_id<>NEW.principal_id OR OLD.group_id<>NEW.group_id OR OLD.created_at<>NEW.created_at THEN RAISE EXCEPTION 'membership identity is immutable'; END IF; RETURN NEW; END; $$;
CREATE OR REPLACE FUNCTION access.reject_external_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF OLD.id<>NEW.id OR OLD.principal_id<>NEW.principal_id OR OLD.provider<>NEW.provider OR OLD.tenant_id<>NEW.tenant_id OR OLD.subject<>NEW.subject OR OLD.created_at<>NEW.created_at THEN RAISE EXCEPTION 'external identity is immutable'; END IF; RETURN NEW; END; $$;
CREATE OR REPLACE FUNCTION access.reject_session_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF OLD.id<>NEW.id OR OLD.principal_id<>NEW.principal_id OR OLD.token_fingerprint<>NEW.token_fingerprint OR OLD.verifier<>NEW.verifier OR OLD.kind<>NEW.kind OR OLD.instance_id<>NEW.instance_id OR OLD.profile_id<>NEW.profile_id OR OLD.client_id<>NEW.client_id OR OLD.created_at<>NEW.created_at OR OLD.absolute_expires_at IS DISTINCT FROM NEW.absolute_expires_at THEN RAISE EXCEPTION 'session identity is immutable'; END IF; RETURN NEW; END; $$;
CREATE OR REPLACE FUNCTION access.reject_token_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF OLD.id<>NEW.id OR OLD.principal_id<>NEW.principal_id OR OLD.name<>NEW.name OR OLD.token_fingerprint<>NEW.token_fingerprint OR OLD.verifier<>NEW.verifier OR OLD.capabilities IS DISTINCT FROM NEW.capabilities OR OLD.expires_at<>NEW.expires_at OR OLD.created_at<>NEW.created_at THEN RAISE EXCEPTION 'API token identity is immutable'; END IF; RETURN NEW; END; $$;
CREATE OR REPLACE FUNCTION access.reject_service_secret_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF OLD.id<>NEW.id OR OLD.service_principal_id<>NEW.service_principal_id OR OLD.name<>NEW.name OR OLD.secret_fingerprint<>NEW.secret_fingerprint OR OLD.verifier<>NEW.verifier OR OLD.expires_at<>NEW.expires_at OR OLD.created_at<>NEW.created_at THEN RAISE EXCEPTION 'service secret identity is immutable'; END IF; RETURN NEW; END; $$;
CREATE OR REPLACE FUNCTION access.reject_credential_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF OLD.principal_id<>NEW.principal_id OR OLD.created_at<>NEW.created_at THEN RAISE EXCEPTION 'credential identity is immutable'; END IF; RETURN NEW; END; $$;

CREATE TRIGGER principal_no_delete BEFORE DELETE ON access.principal FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER principal_identity_immutable BEFORE UPDATE ON access.principal FOR EACH ROW EXECUTE FUNCTION access.reject_principal_identity_rewrite();
CREATE TRIGGER principal_revocation_monotonic BEFORE UPDATE ON access.principal FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER group_no_delete BEFORE DELETE ON access.access_group FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER group_identity_immutable BEFORE UPDATE ON access.access_group FOR EACH ROW EXECUTE FUNCTION access.reject_group_identity_rewrite();
CREATE TRIGGER group_revocation_monotonic BEFORE UPDATE ON access.access_group FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER membership_no_delete BEFORE DELETE ON access.principal_group FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER membership_identity_immutable BEFORE UPDATE ON access.principal_group FOR EACH ROW EXECUTE FUNCTION access.reject_membership_identity_rewrite();
CREATE TRIGGER membership_revocation_monotonic BEFORE UPDATE ON access.principal_group FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER role_no_delete BEFORE DELETE ON access.platform_role_binding FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER role_identity_immutable BEFORE UPDATE ON access.platform_role_binding FOR EACH ROW EXECUTE FUNCTION access.reject_role_identity_rewrite();
CREATE TRIGGER role_revocation_monotonic BEFORE UPDATE ON access.platform_role_binding FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER session_no_delete BEFORE DELETE ON access.session FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER session_identity_immutable BEFORE UPDATE ON access.session FOR EACH ROW EXECUTE FUNCTION access.reject_session_identity_rewrite();
CREATE TRIGGER session_revocation_monotonic BEFORE UPDATE ON access.session FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER api_token_no_delete BEFORE DELETE ON access.api_token FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER api_token_identity_immutable BEFORE UPDATE ON access.api_token FOR EACH ROW EXECUTE FUNCTION access.reject_token_identity_rewrite();
CREATE TRIGGER api_token_revocation_monotonic BEFORE UPDATE ON access.api_token FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER service_secret_no_delete BEFORE DELETE ON access.service_principal_secret FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER service_secret_identity_immutable BEFORE UPDATE ON access.service_principal_secret FOR EACH ROW EXECUTE FUNCTION access.reject_service_secret_identity_rewrite();
CREATE TRIGGER service_secret_revocation_monotonic BEFORE UPDATE ON access.service_principal_secret FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER local_credential_no_delete BEFORE DELETE ON access.local_credential FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER local_credential_identity_immutable BEFORE UPDATE ON access.local_credential FOR EACH ROW EXECUTE FUNCTION access.reject_credential_identity_rewrite();
CREATE TRIGGER local_credential_revocation_monotonic BEFORE UPDATE ON access.local_credential FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER external_identity_no_delete BEFORE DELETE ON access.external_identity FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER external_identity_identity_immutable BEFORE UPDATE ON access.external_identity FOR EACH ROW EXECUTE FUNCTION access.reject_external_identity_rewrite();
CREATE TRIGGER external_identity_revocation_monotonic BEFORE UPDATE ON access.external_identity FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();

DO $$
BEGIN
    REVOKE ALL ON SCHEMA access FROM PUBLIC;
    REVOKE ALL ON ALL TABLES IN SCHEMA access FROM PUBLIC;
    REVOKE ALL ON ALL FUNCTIONS IN SCHEMA access FROM PUBLIC;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_runtime') THEN
        EXECUTE 'GRANT USAGE ON SCHEMA access TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA access TO leapview_control_runtime';
        EXECUTE 'GRANT EXECUTE ON FUNCTION access.valid_capabilities(jsonb) TO leapview_control_runtime';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_readonly') THEN
        EXECUTE 'GRANT USAGE ON SCHEMA access TO leapview_control_readonly';
        EXECUTE 'GRANT SELECT ON ALL TABLES IN SCHEMA access TO leapview_control_readonly';
    END IF;
END $$;
