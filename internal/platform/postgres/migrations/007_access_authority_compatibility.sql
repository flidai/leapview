-- FAI-617 access composition: upgrade the minimal control-plane access
-- projection in place.  This migration deliberately does not execute the
-- standalone access schema: revisions 001-006 may already contain rows which
-- are part of the control-plane history and must remain addressable.

SET ROLE leapview_control_owner;

-- Canonical access audit events carry the exact project-generation identity.
-- Keep these columns nullable so all existing baseline rows remain valid while
-- making the native RecordCanonicalAuditEvent contract available in place.
ALTER TABLE audit.audit_event
    ADD COLUMN IF NOT EXISTS project_id text,
    ADD COLUMN IF NOT EXISTS environment text,
    ADD COLUMN IF NOT EXISTS generation_id text;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'audit.audit_event'::regclass
          AND conname = 'audit_event_project_id_compatibility_check'
    ) THEN
        ALTER TABLE audit.audit_event
            ADD CONSTRAINT audit_event_project_id_compatibility_check
            CHECK (project_id IS NULL OR (project_id = btrim(project_id) AND length(project_id) BETWEEN 1 AND 255)) NOT VALID;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'audit.audit_event'::regclass
          AND conname = 'audit_event_environment_compatibility_check'
    ) THEN
        ALTER TABLE audit.audit_event
            ADD CONSTRAINT audit_event_environment_compatibility_check
            CHECK (environment IS NULL OR (environment = btrim(environment) AND length(environment) BETWEEN 1 AND 128)) NOT VALID;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'audit.audit_event'::regclass
          AND conname = 'audit_event_generation_id_compatibility_check'
    ) THEN
        ALTER TABLE audit.audit_event
            ADD CONSTRAINT audit_event_generation_id_compatibility_check
            CHECK (generation_id IS NULL OR (generation_id = btrim(generation_id) AND length(generation_id) BETWEEN 1 AND 255)) NOT VALID;
    END IF;
END;
$$;

-- The first control-plane revision has the identity tables, but not the
-- fields used by the access repository.  Additive columns retain every
-- existing principal, group, membership, and session row.
ALTER TABLE access.principal
    ADD COLUMN IF NOT EXISTS email text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS disabled_at timestamptz,
    ADD COLUMN IF NOT EXISTS blocked_at timestamptz,
    ADD COLUMN IF NOT EXISTS revoked_at timestamptz,
    ADD COLUMN IF NOT EXISTS last_seen_at timestamptz;

ALTER TABLE access.principal
    DROP CONSTRAINT IF EXISTS principal_principal_type_check;

-- Baseline disabled principals had no lifecycle timestamp.  Backfill a
-- durable tombstone marker before enforcing the native disabled invariant.
UPDATE access.principal
SET disabled_at = COALESCE(disabled_at, clock_timestamp())
WHERE status = 'disabled' AND disabled_at IS NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'access.principal'::regclass
          AND conname = 'principal_email_length_compatibility_check'
    ) THEN
        ALTER TABLE access.principal
            ADD CONSTRAINT principal_email_length_compatibility_check
            CHECK (length(email) <= 320) NOT VALID;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'access.principal'::regclass
          AND conname = 'principal_principal_type_compatibility_check'
    ) THEN
        ALTER TABLE access.principal
            ADD CONSTRAINT principal_principal_type_compatibility_check
            CHECK (principal_type IN ('user', 'service', 'system', 'dashboard_publication')) NOT VALID;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'access.principal'::regclass
          AND conname = 'principal_status_compatibility_check'
    ) THEN
        ALTER TABLE access.principal
            ADD CONSTRAINT principal_status_compatibility_check
            CHECK (status IN ('active', 'disabled', 'pending')) NOT VALID;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'access.principal'::regclass
          AND conname = 'principal_active_disabled_at_check'
    ) THEN
        ALTER TABLE access.principal
            ADD CONSTRAINT principal_active_disabled_at_check
            CHECK ((status = 'active' AND disabled_at IS NULL) OR status <> 'active') NOT VALID;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'access.principal'::regclass
          AND conname = 'principal_disabled_at_check'
    ) THEN
        ALTER TABLE access.principal
            ADD CONSTRAINT principal_disabled_at_check
            CHECK (status <> 'disabled' OR disabled_at IS NOT NULL OR revoked_at IS NOT NULL) NOT VALID;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'access.principal'::regclass
          AND conname = 'principal_pending_disabled_at_check'
    ) THEN
        ALTER TABLE access.principal
            ADD CONSTRAINT principal_pending_disabled_at_check
            CHECK (status <> 'pending' OR disabled_at IS NULL) NOT VALID;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'access.principal'::regclass
          AND conname = 'principal_revoked_status_check'
    ) THEN
        ALTER TABLE access.principal
            ADD CONSTRAINT principal_revoked_status_check
            CHECK (revoked_at IS NULL OR status = 'disabled') NOT VALID;
    END IF;
END;
$$;

CREATE UNIQUE INDEX IF NOT EXISTS principal_email_active_key
    ON access.principal (lower(email))
    WHERE email <> '' AND revoked_at IS NULL;

ALTER TABLE access.access_group
    ADD COLUMN IF NOT EXISTS revoked_at timestamptz;
ALTER TABLE access.access_group
    DROP CONSTRAINT IF EXISTS access_group_provider_external_id_key;
CREATE UNIQUE INDEX IF NOT EXISTS access_group_active_key
    ON access.access_group (provider, NULLIF(external_id, ''))
    WHERE revoked_at IS NULL;

ALTER TABLE access.principal_group
    ADD COLUMN IF NOT EXISTS membership_id uuid DEFAULT uuidv7(),
    ADD COLUMN IF NOT EXISTS revoked_at timestamptz;
ALTER TABLE access.principal_group
    ALTER COLUMN membership_id SET DEFAULT uuidv7(),
    ALTER COLUMN membership_id SET NOT NULL;

-- The legacy composite key identifies the relationship, not the membership
-- event.  Replace it with a generated event identity so revoke/re-add keeps
-- both rows as append-only history.  Existing relationship identities are
-- preserved in their principal_id/group_id columns.
ALTER TABLE access.principal_group
    DROP CONSTRAINT IF EXISTS principal_group_pkey;
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'access.principal_group'::regclass
          AND conname = 'principal_group_pkey'
    ) THEN
        ALTER TABLE access.principal_group
            ADD CONSTRAINT principal_group_pkey PRIMARY KEY (membership_id);
    END IF;
END;
$$;
CREATE UNIQUE INDEX IF NOT EXISTS principal_group_active_key
    ON access.principal_group (principal_id, group_id)
    WHERE revoked_at IS NULL;

-- Revision one had no password verifier.  Seal all pre-007 sessions before
-- making the new verifier mandatory.  The zero verifier is intentionally not
-- usable by the Argon2 verifier, and revoked_at is the authoritative fence;
-- no legacy bearer session remains active after this migration.
ALTER TABLE access.session
    ADD COLUMN IF NOT EXISTS verifier bytea,
    ADD COLUMN IF NOT EXISTS revoked_at timestamptz,
    ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'browser',
    ADD COLUMN IF NOT EXISTS instance_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS profile_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS client_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS absolute_expires_at timestamptz;

UPDATE access.session
SET revoked_at = COALESCE(revoked_at, clock_timestamp()),
    verifier = COALESCE(verifier, decode(repeat('00', 32), 'hex'))
WHERE verifier IS NULL;

ALTER TABLE access.session
    ALTER COLUMN verifier SET NOT NULL,
    ALTER COLUMN kind SET DEFAULT 'browser',
    ALTER COLUMN instance_id SET DEFAULT '',
    ALTER COLUMN profile_id SET DEFAULT '',
    ALTER COLUMN client_id SET DEFAULT '';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'access.session'::regclass
          AND conname = 'session_expiry_compatibility_check'
    ) THEN
        ALTER TABLE access.session
            ADD CONSTRAINT session_expiry_compatibility_check
            CHECK (expires_at > created_at) NOT VALID;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'access.session'::regclass
          AND conname = 'session_absolute_expiry_compatibility_check'
    ) THEN
        ALTER TABLE access.session
            ADD CONSTRAINT session_absolute_expiry_compatibility_check
            CHECK (absolute_expires_at IS NULL OR absolute_expires_at >= expires_at) NOT VALID;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'access.session'::regclass
          AND conname = 'session_token_fingerprint_compatibility_check'
    ) THEN
        ALTER TABLE access.session
            ADD CONSTRAINT session_token_fingerprint_compatibility_check
            CHECK (octet_length(token_fingerprint) = 32) NOT VALID;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'access.session'::regclass
          AND conname = 'session_verifier_compatibility_check'
    ) THEN
        ALTER TABLE access.session
            ADD CONSTRAINT session_verifier_compatibility_check
            CHECK (octet_length(verifier) BETWEEN 32 AND 512) NOT VALID;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'access.session'::regclass
          AND conname = 'session_kind_scope_compatibility_check'
    ) THEN
        ALTER TABLE access.session
            ADD CONSTRAINT session_kind_scope_compatibility_check
            CHECK (
                (kind = 'browser' AND instance_id = '' AND profile_id = '' AND client_id = '' AND absolute_expires_at IS NULL)
                OR (kind = 'desktop' AND instance_id <> '' AND profile_id <> '' AND client_id <> '' AND absolute_expires_at IS NOT NULL)
            ) NOT VALID;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'access.session'::regclass
          AND conname = 'session_instance_id_length_compatibility_check'
    ) THEN
        ALTER TABLE access.session
            ADD CONSTRAINT session_instance_id_length_compatibility_check
            CHECK (length(instance_id) <= 128) NOT VALID;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'access.session'::regclass
          AND conname = 'session_profile_id_length_compatibility_check'
    ) THEN
        ALTER TABLE access.session
            ADD CONSTRAINT session_profile_id_length_compatibility_check
            CHECK (length(profile_id) <= 128) NOT VALID;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'access.session'::regclass
          AND conname = 'session_client_id_length_compatibility_check'
    ) THEN
        ALTER TABLE access.session
            ADD CONSTRAINT session_client_id_length_compatibility_check
            CHECK (length(client_id) <= 255) NOT VALID;
    END IF;
END;
$$;

CREATE INDEX IF NOT EXISTS access_session_active_fp_idx
    ON access.session (token_fingerprint) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS access_session_principal_idx
    ON access.session (principal_id, created_at DESC);

-- New access-owned state.  The legacy generic access.credential and
-- access.access_grant tables are intentionally retained as historical rows;
-- current repositories use the typed tables below.
CREATE TABLE IF NOT EXISTS access.platform_setting (
    key text PRIMARY KEY CHECK (key = btrim(key) AND length(key) BETWEEN 1 AND 255),
    value text NOT NULL CHECK (length(value) <= 2048),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE IF NOT EXISTS access.external_identity (
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
CREATE UNIQUE INDEX IF NOT EXISTS external_identity_active_key
    ON access.external_identity (provider, tenant_id, subject)
    WHERE revoked_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS external_identity_active_external_id
    ON access.external_identity (provider, tenant_id, external_id)
    WHERE external_id <> '' AND revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS access.platform_role_binding (
    id uuid PRIMARY KEY,
    principal_id uuid NOT NULL REFERENCES access.principal(id),
    role text NOT NULL CHECK (role = 'platform_admin'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS platform_role_binding_active_key
    ON access.platform_role_binding (principal_id, role) WHERE revoked_at IS NULL;

CREATE OR REPLACE FUNCTION access.valid_capabilities(value jsonb)
RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
    item text;
    seen_items jsonb := '[]'::jsonb;
BEGIN
    IF value IS NULL THEN RETURN TRUE; END IF;
    IF jsonb_typeof(value) <> 'array' THEN RETURN FALSE; END IF;
    FOR item IN SELECT jsonb_array_elements_text(value) LOOP
        IF item NOT IN ('PROJECT_ADMIN', 'RESOURCE_USE', 'RESOURCE_READ', 'RESOURCE_EDIT',
                        'RESOURCE_MANAGE', 'RESOURCE_SHARE', 'RESOURCE_PUBLISH') THEN
            RETURN FALSE;
        END IF;
        IF seen_items ? item THEN RETURN FALSE; END IF;
        seen_items := seen_items || to_jsonb(item);
    END LOOP;
    RETURN TRUE;
END;
$$;

CREATE TABLE IF NOT EXISTS access.local_credential (
    principal_id uuid PRIMARY KEY REFERENCES access.principal(id),
    verifier bytea NOT NULL,
    must_change boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    password_changed_at timestamptz,
    revoked_at timestamptz,
    CHECK (octet_length(verifier) BETWEEN 32 AND 512)
);

CREATE TABLE IF NOT EXISTS access.api_token (
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
    CHECK (octet_length(token_fingerprint) = 32),
    CHECK (octet_length(verifier) BETWEEN 32 AND 512),
    CHECK (expires_at > created_at AND expires_at <= created_at + interval '365 days')
);
CREATE INDEX IF NOT EXISTS access_api_token_principal_idx
    ON access.api_token (principal_id, created_at DESC);
CREATE INDEX IF NOT EXISTS access_api_token_active_fp_idx
    ON access.api_token (token_fingerprint) WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS access.service_principal_secret (
    id uuid PRIMARY KEY,
    service_principal_id uuid NOT NULL REFERENCES access.principal(id),
    name text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 255),
    secret_fingerprint bytea NOT NULL UNIQUE,
    verifier bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz,
    CHECK (octet_length(secret_fingerprint) = 32),
    CHECK (octet_length(verifier) BETWEEN 32 AND 512),
    CHECK (expires_at > created_at AND expires_at <= created_at + interval '365 days')
);
CREATE INDEX IF NOT EXISTS service_secret_principal_idx
    ON access.service_principal_secret (service_principal_id, created_at DESC);

-- Immutable authorization snapshots are persisted for the current repository
-- boundary.  This stores policy evidence; it does not evaluate or rewrite
-- semantic policy.
CREATE TABLE IF NOT EXISTS access.authorization_snapshot (
    project_id text NOT NULL CHECK (project_id = btrim(project_id) AND length(project_id) BETWEEN 1 AND 255),
    environment text NOT NULL CHECK (environment = btrim(environment) AND length(environment) BETWEEN 1 AND 128),
    generation_id text NOT NULL CHECK (generation_id = btrim(generation_id) AND length(generation_id) BETWEEN 1 AND 255),
    digest text NOT NULL CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (project_id, environment, generation_id)
);

CREATE TABLE IF NOT EXISTS access.authorization_role_binding (
    id text NOT NULL CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 255),
    project_id text NOT NULL,
    environment text NOT NULL,
    generation_id text NOT NULL,
    subject_kind text NOT NULL CHECK (subject_kind IN ('principal', 'group')),
    subject_id text NOT NULL CHECK (subject_id = btrim(subject_id) AND length(subject_id) BETWEEN 1 AND 255),
    role text NOT NULL CHECK (role IN ('owner', 'admin', 'deployer', 'data_deployer', 'contributor', 'editor', 'member', 'viewer')),
    capabilities jsonb NOT NULL CHECK (access.valid_capabilities(capabilities) AND jsonb_typeof(capabilities) = 'array' AND octet_length(capabilities::text) <= 2048),
    name text NOT NULL DEFAULT '' CHECK (length(name) <= 255),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz,
    PRIMARY KEY (project_id, environment, generation_id, id),
    FOREIGN KEY (project_id, environment, generation_id)
        REFERENCES access.authorization_snapshot(project_id, environment, generation_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS authorization_role_binding_active_key
    ON access.authorization_role_binding (project_id, environment, generation_id, subject_kind, subject_id, role)
    WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS authorization_role_binding_subject_idx
    ON access.authorization_role_binding (project_id, environment, generation_id, subject_kind, subject_id);

CREATE TABLE IF NOT EXISTS access.authorization_grant (
    id text NOT NULL CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 255),
    project_id text NOT NULL,
    environment text NOT NULL,
    generation_id text NOT NULL,
    subject_kind text NOT NULL CHECK (subject_kind IN ('principal', 'group')),
    subject_id text NOT NULL CHECK (subject_id = btrim(subject_id) AND length(subject_id) BETWEEN 1 AND 255),
    resource_id text NOT NULL CHECK (resource_id = btrim(resource_id) AND length(resource_id) BETWEEN 1 AND 255),
    resource_kind text NOT NULL CHECK (resource_kind = btrim(resource_kind) AND length(resource_kind) BETWEEN 1 AND 128),
    capability text NOT NULL CHECK (capability IN ('PROJECT_ADMIN', 'RESOURCE_USE', 'RESOURCE_READ', 'RESOURCE_EDIT', 'RESOURCE_MANAGE', 'RESOURCE_SHARE', 'RESOURCE_PUBLISH')),
    name text NOT NULL DEFAULT '' CHECK (length(name) <= 255),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz,
    PRIMARY KEY (project_id, environment, generation_id, id),
    FOREIGN KEY (project_id, environment, generation_id)
        REFERENCES access.authorization_snapshot(project_id, environment, generation_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS authorization_grant_active_key
    ON access.authorization_grant (project_id, environment, generation_id, subject_kind, subject_id, resource_id, resource_kind, capability)
    WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS authorization_grant_subject_idx
    ON access.authorization_grant (project_id, environment, generation_id, subject_kind, subject_id);
CREATE INDEX IF NOT EXISTS authorization_grant_resource_idx
    ON access.authorization_grant (project_id, environment, generation_id, resource_id, capability);

CREATE TABLE IF NOT EXISTS access.authorization_data_policy (
    id text NOT NULL CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 255),
    project_id text NOT NULL,
    environment text NOT NULL,
    generation_id text NOT NULL,
    resource_id text NOT NULL CHECK (resource_id = btrim(resource_id) AND length(resource_id) BETWEEN 1 AND 255),
    resource_kind text NOT NULL CHECK (resource_kind = btrim(resource_kind) AND length(resource_kind) BETWEEN 1 AND 128),
    subject_kind text CHECK (subject_kind IS NULL OR subject_kind IN ('principal', 'group')),
    subject_id text,
    policy_type text NOT NULL CHECK (policy_type IN ('row_filter', 'column_mask')),
    expression jsonb NOT NULL CHECK (jsonb_typeof(expression) = 'object' AND octet_length(expression::text) <= 32768),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz,
    PRIMARY KEY (project_id, environment, generation_id, id),
    CHECK ((subject_kind IS NULL AND subject_id IS NULL) OR (subject_kind IS NOT NULL AND subject_id IS NOT NULL)),
    FOREIGN KEY (project_id, environment, generation_id)
        REFERENCES access.authorization_snapshot(project_id, environment, generation_id)
);
CREATE INDEX IF NOT EXISTS authorization_data_policy_resource_idx
    ON access.authorization_data_policy (project_id, environment, generation_id, resource_id);

CREATE TABLE IF NOT EXISTS access.authorization_revocation (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id text NOT NULL,
    environment text NOT NULL,
    generation_id text,
    subject_kind text CHECK (subject_kind IS NULL OR subject_kind IN ('principal', 'group')),
    subject_id text,
    resource_id text,
    capability text,
    reason text NOT NULL DEFAULT '' CHECK (length(reason) <= 1024),
    revoked_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object' AND octet_length(metadata::text) <= 8192)
);

CREATE TABLE IF NOT EXISTS access.principal_preferences (
    preference_id uuid PRIMARY KEY DEFAULT uuidv7(),
    principal_id uuid NOT NULL REFERENCES access.principal(id),
    theme text NOT NULL DEFAULT 'system' CHECK (theme IN ('system', 'light', 'dark', 'dark_dimmed', 'light_colorblind', 'dark_colorblind', 'light_tritanopia', 'dark_tritanopia')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS principal_preferences_active_key
    ON access.principal_preferences (principal_id) WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS access.avatar_object (
    sha256 text PRIMARY KEY CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    object_key text NOT NULL CHECK (object_key = btrim(object_key) AND length(object_key) BETWEEN 1 AND 2048),
    media_type text NOT NULL CHECK (media_type = 'image/png'),
    size_bytes bigint NOT NULL CHECK (size_bytes > 0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE IF NOT EXISTS access.principal_avatar (
    avatar_id uuid PRIMARY KEY DEFAULT uuidv7(),
    principal_id uuid NOT NULL REFERENCES access.principal(id),
    sha256 text NOT NULL REFERENCES access.avatar_object(sha256),
    media_type text NOT NULL CHECK (media_type = 'image/png'),
    size_bytes bigint NOT NULL CHECK (size_bytes > 0),
    width integer NOT NULL CHECK (width = 256),
    height integer NOT NULL CHECK (height = 256),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS principal_avatar_active_key
    ON access.principal_avatar (principal_id) WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS access.desktop_authorization_code (
    code_hash bytea PRIMARY KEY CHECK (octet_length(code_hash) = 32),
    principal_id uuid NOT NULL REFERENCES access.principal(id),
    client_id text NOT NULL CHECK (client_id = 'leapview-desktop'),
    instance_id text NOT NULL CHECK (length(instance_id) BETWEEN 1 AND 128),
    profile_id text NOT NULL CHECK (profile_id = btrim(profile_id) AND length(profile_id) BETWEEN 1 AND 128),
    redirect_uri text NOT NULL CHECK (length(redirect_uri) <= 2048),
    code_challenge text NOT NULL CHECK (length(code_challenge) BETWEEN 43 AND 128),
    return_path text NOT NULL CHECK (length(return_path) <= 2048),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (expires_at > created_at AND expires_at <= created_at + interval '10 minutes')
);
CREATE INDEX IF NOT EXISTS desktop_authorization_code_expiry_idx
    ON access.desktop_authorization_code (expires_at);

CREATE TABLE IF NOT EXISTS access.device_authorization (
    id text PRIMARY KEY CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 128),
    client_id text NOT NULL CHECK (client_id = 'leapview-cli'),
    device_code_hash text NOT NULL UNIQUE CHECK (device_code_hash ~ '^[0-9a-f]{64}$'),
    user_code_hash text NOT NULL UNIQUE CHECK (user_code_hash ~ '^[0-9a-f]{64}$'),
    target_id text NOT NULL CHECK (target_id = btrim(target_id) AND length(target_id) <= 255),
    project_id text NOT NULL CHECK (project_id = btrim(project_id) AND length(project_id) <= 255),
    capabilities jsonb NOT NULL CHECK (access.valid_capabilities(capabilities) AND jsonb_typeof(capabilities) = 'array' AND octet_length(capabilities::text) <= 2048),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'denied', 'consumed')),
    principal_id uuid REFERENCES access.principal(id),
    expires_at timestamptz NOT NULL,
    poll_interval_seconds integer NOT NULL CHECK (poll_interval_seconds > 0),
    last_polled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    approved_at timestamptz,
    denied_at timestamptz,
    consumed_at timestamptz,
    CHECK (expires_at > created_at AND expires_at <= created_at + interval '24 hours'),
    CHECK ((status = 'pending' AND principal_id IS NULL AND approved_at IS NULL AND denied_at IS NULL AND consumed_at IS NULL)
        OR (status = 'approved' AND principal_id IS NOT NULL AND approved_at IS NOT NULL AND denied_at IS NULL AND consumed_at IS NULL)
        OR (status = 'denied' AND principal_id IS NOT NULL AND denied_at IS NOT NULL AND consumed_at IS NULL)
        OR (status = 'consumed' AND principal_id IS NOT NULL AND approved_at IS NOT NULL AND consumed_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS device_authorization_expiry_idx
    ON access.device_authorization (expires_at);

CREATE TABLE IF NOT EXISTS access.authoring_session (
    id text PRIMARY KEY CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 128),
    kind text NOT NULL CHECK (kind IN ('human_cli', 'workload')),
    client_id text NOT NULL CHECK (length(client_id) <= 255),
    principal_id uuid NOT NULL REFERENCES access.principal(id),
    target_id text NOT NULL CHECK (length(target_id) <= 255),
    project_id text NOT NULL CHECK (length(project_id) <= 255),
    capabilities jsonb NOT NULL CHECK (access.valid_capabilities(capabilities) AND jsonb_typeof(capabilities) = 'array' AND octet_length(capabilities::text) <= 2048),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    last_used_at timestamptz,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    CHECK (expires_at > created_at AND expires_at <= created_at + interval '365 days')
);
CREATE INDEX IF NOT EXISTS authoring_session_principal_idx
    ON access.authoring_session (principal_id, created_at DESC);

CREATE TABLE IF NOT EXISTS access.authoring_credential (
    id text PRIMARY KEY CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 128),
    session_id text NOT NULL REFERENCES access.authoring_session(id),
    access_token_hash text NOT NULL UNIQUE CHECK (access_token_hash ~ '^[0-9a-f]{64}$'),
    refresh_token_hash text UNIQUE CHECK (refresh_token_hash IS NULL OR refresh_token_hash ~ '^[0-9a-f]{64}$'),
    access_expires_at timestamptz NOT NULL,
    refresh_expires_at timestamptz,
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    replaced_at timestamptz,
    CHECK (access_expires_at > created_at),
    CHECK ((refresh_token_hash IS NULL AND refresh_expires_at IS NULL)
        OR (refresh_token_hash IS NOT NULL AND refresh_expires_at IS NOT NULL AND refresh_expires_at > access_expires_at))
);
CREATE UNIQUE INDEX IF NOT EXISTS authoring_credential_active_session_idx
    ON access.authoring_credential (session_id) WHERE active;
CREATE INDEX IF NOT EXISTS authoring_credential_access_expiry_idx
    ON access.authoring_credential (access_expires_at);
CREATE INDEX IF NOT EXISTS authoring_credential_refresh_expiry_idx
    ON access.authoring_credential (refresh_expires_at) WHERE refresh_expires_at IS NOT NULL;

-- MCP OAuth state is opaque protocol state and remains separate from browser
-- sessions.  It is the only access state that the runtime may hard-delete.
CREATE TABLE IF NOT EXISTS access.oauth_client (
    id text PRIMARY KEY CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 255),
    name text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 255),
    redirect_uris jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(redirect_uris) = 'array' AND octet_length(redirect_uris::text) <= 16384),
    grant_types jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(grant_types) = 'array' AND octet_length(grant_types::text) <= 4096),
    response_types jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(response_types) = 'array' AND octet_length(response_types::text) <= 4096),
    scopes jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(scopes) = 'array' AND octet_length(scopes::text) <= 4096),
    audience jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(audience) = 'array' AND octet_length(audience::text) <= 4096),
    public_client boolean NOT NULL DEFAULT false,
    secret_hash bytea,
    token_endpoint_auth_method text NOT NULL DEFAULT 'none' CHECK (token_endpoint_auth_method = btrim(token_endpoint_auth_method) AND length(token_endpoint_auth_method) <= 64),
    principal_id uuid REFERENCES access.principal(id),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE IF NOT EXISTS access.oauth_session (
    kind text NOT NULL CHECK (kind IN ('authorize_code', 'access_token', 'refresh_token', 'pkce')),
    signature text NOT NULL CHECK (signature = btrim(signature) AND length(signature) BETWEEN 1 AND 512),
    request_id text NOT NULL CHECK (request_id = btrim(request_id) AND length(request_id) BETWEEN 1 AND 512),
    request_json jsonb NOT NULL CHECK (jsonb_typeof(request_json) = 'object' AND octet_length(request_json::text) <= 131072),
    access_signature text NOT NULL DEFAULT '' CHECK (access_signature = btrim(access_signature) AND length(access_signature) <= 512),
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (kind, signature)
);
CREATE INDEX IF NOT EXISTS oauth_session_request_idx
    ON access.oauth_session (kind, request_id);

CREATE TABLE IF NOT EXISTS access.oauth_client_assertion (
    jti text PRIMARY KEY CHECK (jti = btrim(jti) AND length(jti) BETWEEN 1 AND 512),
    expires_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS oauth_client_assertion_expiry_idx
    ON access.oauth_client_assertion (expires_at);

-- Common history fences.  DROP/CREATE makes trigger installation deterministic
-- when a deployment retries a transaction after a transient failure.
CREATE OR REPLACE FUNCTION access.reject_access_delete()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'access history is append-only; revoke instead of delete';
END;
$$;
CREATE OR REPLACE FUNCTION access.reject_revocation_clear()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.revoked_at IS NOT NULL AND (NEW.revoked_at IS NULL OR NEW.revoked_at < OLD.revoked_at) THEN
        RAISE EXCEPTION 'revocation is monotonic';
    END IF;
    RETURN NEW;
END;
$$;
CREATE OR REPLACE FUNCTION access.reject_principal_identity_rewrite()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.id <> NEW.id OR OLD.principal_type <> NEW.principal_type OR OLD.created_at <> NEW.created_at THEN
        RAISE EXCEPTION 'principal identity is immutable';
    END IF;
    IF NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'principal updated_at is monotonic';
    END IF;
    RETURN NEW;
END;
$$;
CREATE OR REPLACE FUNCTION access.reject_group_identity_rewrite()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.id <> NEW.id OR OLD.provider <> NEW.provider OR OLD.external_id <> NEW.external_id OR OLD.created_at <> NEW.created_at THEN
        RAISE EXCEPTION 'group identity is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE OR REPLACE FUNCTION access.reject_role_identity_rewrite()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.id <> NEW.id OR OLD.principal_id <> NEW.principal_id OR OLD.role <> NEW.role OR OLD.created_at <> NEW.created_at THEN
        RAISE EXCEPTION 'role identity is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE OR REPLACE FUNCTION access.reject_membership_identity_rewrite()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.membership_id <> NEW.membership_id OR OLD.principal_id <> NEW.principal_id OR OLD.group_id <> NEW.group_id OR OLD.created_at <> NEW.created_at THEN
        RAISE EXCEPTION 'membership identity is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE OR REPLACE FUNCTION access.reject_external_identity_rewrite()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.id <> NEW.id OR OLD.principal_id <> NEW.principal_id OR OLD.provider <> NEW.provider OR OLD.tenant_id <> NEW.tenant_id OR OLD.subject <> NEW.subject OR OLD.created_at <> NEW.created_at THEN
        RAISE EXCEPTION 'external identity is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE OR REPLACE FUNCTION access.reject_session_identity_rewrite()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.id <> NEW.id OR OLD.principal_id <> NEW.principal_id OR OLD.token_fingerprint <> NEW.token_fingerprint OR OLD.verifier <> NEW.verifier OR OLD.kind <> NEW.kind OR OLD.instance_id <> NEW.instance_id OR OLD.profile_id <> NEW.profile_id OR OLD.client_id <> NEW.client_id OR OLD.created_at <> NEW.created_at OR OLD.absolute_expires_at IS DISTINCT FROM NEW.absolute_expires_at THEN
        RAISE EXCEPTION 'session identity is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE OR REPLACE FUNCTION access.reject_token_identity_rewrite()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.id <> NEW.id OR OLD.principal_id <> NEW.principal_id OR OLD.name <> NEW.name OR OLD.token_fingerprint <> NEW.token_fingerprint OR OLD.verifier <> NEW.verifier OR OLD.capabilities IS DISTINCT FROM NEW.capabilities OR OLD.expires_at <> NEW.expires_at OR OLD.created_at <> NEW.created_at THEN
        RAISE EXCEPTION 'API token identity is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE OR REPLACE FUNCTION access.reject_service_secret_identity_rewrite()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.id <> NEW.id OR OLD.service_principal_id <> NEW.service_principal_id OR OLD.name <> NEW.name OR OLD.secret_fingerprint <> NEW.secret_fingerprint OR OLD.verifier <> NEW.verifier OR OLD.expires_at <> NEW.expires_at OR OLD.created_at <> NEW.created_at THEN
        RAISE EXCEPTION 'service secret identity is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE OR REPLACE FUNCTION access.reject_credential_identity_rewrite()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.principal_id <> NEW.principal_id OR OLD.created_at <> NEW.created_at THEN
        RAISE EXCEPTION 'credential identity is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE OR REPLACE FUNCTION access.reject_preference_identity_rewrite()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.preference_id <> NEW.preference_id OR OLD.principal_id <> NEW.principal_id OR OLD.theme <> NEW.theme OR OLD.created_at IS DISTINCT FROM NEW.created_at THEN
        RAISE EXCEPTION 'preference identity is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE OR REPLACE FUNCTION access.reject_avatar_identity_rewrite()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.avatar_id <> NEW.avatar_id OR OLD.principal_id <> NEW.principal_id OR OLD.sha256 <> NEW.sha256 OR OLD.media_type <> NEW.media_type OR OLD.size_bytes <> NEW.size_bytes OR OLD.width <> NEW.width OR OLD.height <> NEW.height OR OLD.updated_at <> NEW.updated_at THEN
        RAISE EXCEPTION 'avatar identity is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE OR REPLACE FUNCTION access.reject_object_identity_rewrite()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'avatar object identity is immutable';
END;
$$;
CREATE OR REPLACE FUNCTION access.reject_consumption_rewind()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.consumed_at IS NOT NULL AND (NEW.consumed_at IS NULL OR NEW.consumed_at < OLD.consumed_at) THEN
        RAISE EXCEPTION 'consumption is monotonic';
    END IF;
    RETURN NEW;
END;
$$;
CREATE OR REPLACE FUNCTION access.reject_device_authorization_rewrite()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.id <> NEW.id OR OLD.client_id <> NEW.client_id OR OLD.device_code_hash <> NEW.device_code_hash OR OLD.user_code_hash <> NEW.user_code_hash OR OLD.target_id <> NEW.target_id OR OLD.project_id <> NEW.project_id OR OLD.capabilities IS DISTINCT FROM NEW.capabilities OR OLD.created_at <> NEW.created_at OR OLD.expires_at <> NEW.expires_at THEN
        RAISE EXCEPTION 'device authorization identity is immutable';
    END IF;
    IF OLD.status = 'pending' AND NEW.status NOT IN ('pending', 'approved', 'denied') THEN
        RAISE EXCEPTION 'invalid device authorization transition';
    ELSIF OLD.status = 'approved' AND NEW.status NOT IN ('approved', 'consumed') THEN
        RAISE EXCEPTION 'invalid device authorization transition';
    ELSIF OLD.status IN ('denied', 'consumed') AND NEW.status <> OLD.status THEN
        RAISE EXCEPTION 'invalid device authorization transition';
    END IF;
    IF OLD.approved_at IS NOT NULL AND (NEW.approved_at IS NULL OR NEW.approved_at < OLD.approved_at) THEN
        RAISE EXCEPTION 'approval timestamp is monotonic';
    END IF;
    IF OLD.denied_at IS NOT NULL AND (NEW.denied_at IS NULL OR NEW.denied_at < OLD.denied_at) THEN
        RAISE EXCEPTION 'denial timestamp is monotonic';
    END IF;
    IF OLD.consumed_at IS NOT NULL AND (NEW.consumed_at IS NULL OR NEW.consumed_at < OLD.consumed_at) THEN
        RAISE EXCEPTION 'consumption timestamp is monotonic';
    END IF;
    RETURN NEW;
END;
$$;
CREATE OR REPLACE FUNCTION access.reject_authoring_identity_rewrite()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME = 'desktop_authorization_code' THEN
        IF OLD.code_hash <> NEW.code_hash OR OLD.principal_id <> NEW.principal_id OR OLD.client_id <> NEW.client_id OR OLD.instance_id <> NEW.instance_id OR OLD.profile_id <> NEW.profile_id OR OLD.redirect_uri <> NEW.redirect_uri OR OLD.code_challenge <> NEW.code_challenge OR OLD.return_path <> NEW.return_path OR OLD.expires_at <> NEW.expires_at OR OLD.created_at <> NEW.created_at THEN
            RAISE EXCEPTION 'desktop authorization identity is immutable';
        END IF;
    ELSIF TG_TABLE_NAME = 'authoring_session' THEN
        IF OLD.id <> NEW.id OR OLD.kind <> NEW.kind OR OLD.client_id <> NEW.client_id OR OLD.principal_id <> NEW.principal_id OR OLD.target_id <> NEW.target_id OR OLD.project_id <> NEW.project_id OR OLD.capabilities IS DISTINCT FROM NEW.capabilities OR OLD.created_at <> NEW.created_at THEN
            RAISE EXCEPTION 'authoring session identity is immutable';
        END IF;
    ELSIF TG_TABLE_NAME = 'authoring_credential' THEN
        IF OLD.id <> NEW.id OR OLD.session_id <> NEW.session_id OR OLD.access_token_hash <> NEW.access_token_hash OR OLD.refresh_token_hash IS DISTINCT FROM NEW.refresh_token_hash OR OLD.access_expires_at <> NEW.access_expires_at OR OLD.refresh_expires_at IS DISTINCT FROM NEW.refresh_expires_at OR OLD.created_at <> NEW.created_at THEN
            RAISE EXCEPTION 'authoring credential identity is immutable';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
CREATE OR REPLACE FUNCTION access.reject_authoring_credential_transition()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.active = FALSE AND NEW.active <> FALSE THEN
        RAISE EXCEPTION 'authoring credential activation is not reversible';
    END IF;
    IF OLD.replaced_at IS NOT NULL AND (NEW.replaced_at IS NULL OR NEW.replaced_at < OLD.replaced_at) THEN
        RAISE EXCEPTION 'credential replacement timestamp is monotonic';
    END IF;
    RETURN NEW;
END;
$$;
CREATE OR REPLACE FUNCTION access.reject_authorization_identity_rewrite()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME = 'authorization_snapshot' THEN
        IF OLD.project_id <> NEW.project_id OR OLD.environment <> NEW.environment OR OLD.generation_id <> NEW.generation_id OR OLD.digest <> NEW.digest OR OLD.created_at <> NEW.created_at THEN
            RAISE EXCEPTION 'authorization snapshot identity is immutable';
        END IF;
    ELSIF TG_TABLE_NAME = 'authorization_role_binding' THEN
        IF OLD.id <> NEW.id OR OLD.project_id <> NEW.project_id OR OLD.environment <> NEW.environment OR OLD.generation_id <> NEW.generation_id OR OLD.subject_kind <> NEW.subject_kind OR OLD.subject_id <> NEW.subject_id OR OLD.role <> NEW.role OR OLD.capabilities IS DISTINCT FROM NEW.capabilities OR OLD.name <> NEW.name OR OLD.created_at <> NEW.created_at THEN
            RAISE EXCEPTION 'authorization role binding identity is immutable';
        END IF;
    ELSIF TG_TABLE_NAME = 'authorization_grant' THEN
        IF OLD.id <> NEW.id OR OLD.project_id <> NEW.project_id OR OLD.environment <> NEW.environment OR OLD.generation_id <> NEW.generation_id OR OLD.subject_kind <> NEW.subject_kind OR OLD.subject_id <> NEW.subject_id OR OLD.resource_id <> NEW.resource_id OR OLD.resource_kind <> NEW.resource_kind OR OLD.capability <> NEW.capability OR OLD.name <> NEW.name OR OLD.created_at <> NEW.created_at THEN
            RAISE EXCEPTION 'authorization grant identity is immutable';
        END IF;
    ELSIF TG_TABLE_NAME = 'authorization_data_policy' THEN
        IF OLD.id <> NEW.id OR OLD.project_id <> NEW.project_id OR OLD.environment <> NEW.environment OR OLD.generation_id <> NEW.generation_id OR OLD.resource_id <> NEW.resource_id OR OLD.resource_kind <> NEW.resource_kind OR OLD.subject_kind IS DISTINCT FROM NEW.subject_kind OR OLD.subject_id IS DISTINCT FROM NEW.subject_id OR OLD.policy_type <> NEW.policy_type OR OLD.expression IS DISTINCT FROM NEW.expression OR OLD.created_at <> NEW.created_at THEN
            RAISE EXCEPTION 'authorization policy identity is immutable';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

-- Existing baseline tables did not have these fences; reinstalling the named
-- triggers gives old and new rows one deterministic contract.
DROP TRIGGER IF EXISTS principal_no_delete ON access.principal;
CREATE TRIGGER principal_no_delete BEFORE DELETE ON access.principal FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
DROP TRIGGER IF EXISTS principal_identity_immutable ON access.principal;
CREATE TRIGGER principal_identity_immutable BEFORE UPDATE ON access.principal FOR EACH ROW EXECUTE FUNCTION access.reject_principal_identity_rewrite();
DROP TRIGGER IF EXISTS principal_revocation_monotonic ON access.principal;
CREATE TRIGGER principal_revocation_monotonic BEFORE UPDATE ON access.principal FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
DROP TRIGGER IF EXISTS group_no_delete ON access.access_group;
CREATE TRIGGER group_no_delete BEFORE DELETE ON access.access_group FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
DROP TRIGGER IF EXISTS group_identity_immutable ON access.access_group;
CREATE TRIGGER group_identity_immutable BEFORE UPDATE ON access.access_group FOR EACH ROW EXECUTE FUNCTION access.reject_group_identity_rewrite();
DROP TRIGGER IF EXISTS group_revocation_monotonic ON access.access_group;
CREATE TRIGGER group_revocation_monotonic BEFORE UPDATE ON access.access_group FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
DROP TRIGGER IF EXISTS membership_no_delete ON access.principal_group;
CREATE TRIGGER membership_no_delete BEFORE DELETE ON access.principal_group FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
DROP TRIGGER IF EXISTS membership_identity_immutable ON access.principal_group;
CREATE TRIGGER membership_identity_immutable BEFORE UPDATE ON access.principal_group FOR EACH ROW EXECUTE FUNCTION access.reject_membership_identity_rewrite();
DROP TRIGGER IF EXISTS membership_revocation_monotonic ON access.principal_group;
CREATE TRIGGER membership_revocation_monotonic BEFORE UPDATE ON access.principal_group FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
DROP TRIGGER IF EXISTS role_no_delete ON access.platform_role_binding;
CREATE TRIGGER role_no_delete BEFORE DELETE ON access.platform_role_binding FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
DROP TRIGGER IF EXISTS role_identity_immutable ON access.platform_role_binding;
CREATE TRIGGER role_identity_immutable BEFORE UPDATE ON access.platform_role_binding FOR EACH ROW EXECUTE FUNCTION access.reject_role_identity_rewrite();
DROP TRIGGER IF EXISTS role_revocation_monotonic ON access.platform_role_binding;
CREATE TRIGGER role_revocation_monotonic BEFORE UPDATE ON access.platform_role_binding FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
DROP TRIGGER IF EXISTS session_no_delete ON access.session;
CREATE TRIGGER session_no_delete BEFORE DELETE ON access.session FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
DROP TRIGGER IF EXISTS session_identity_immutable ON access.session;
CREATE TRIGGER session_identity_immutable BEFORE UPDATE ON access.session FOR EACH ROW EXECUTE FUNCTION access.reject_session_identity_rewrite();
DROP TRIGGER IF EXISTS session_revocation_monotonic ON access.session;
CREATE TRIGGER session_revocation_monotonic BEFORE UPDATE ON access.session FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
DROP TRIGGER IF EXISTS external_identity_no_delete ON access.external_identity;
CREATE TRIGGER external_identity_no_delete BEFORE DELETE ON access.external_identity FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
DROP TRIGGER IF EXISTS external_identity_identity_immutable ON access.external_identity;
CREATE TRIGGER external_identity_identity_immutable BEFORE UPDATE ON access.external_identity FOR EACH ROW EXECUTE FUNCTION access.reject_external_identity_rewrite();
DROP TRIGGER IF EXISTS external_identity_revocation_monotonic ON access.external_identity;
CREATE TRIGGER external_identity_revocation_monotonic BEFORE UPDATE ON access.external_identity FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
DROP TRIGGER IF EXISTS platform_setting_no_mutation ON access.platform_setting;
CREATE TRIGGER platform_setting_no_mutation BEFORE UPDATE OR DELETE ON access.platform_setting FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
DROP TRIGGER IF EXISTS local_credential_no_delete ON access.local_credential;
CREATE TRIGGER local_credential_no_delete BEFORE DELETE ON access.local_credential FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
DROP TRIGGER IF EXISTS local_credential_identity_immutable ON access.local_credential;
CREATE TRIGGER local_credential_identity_immutable BEFORE UPDATE ON access.local_credential FOR EACH ROW EXECUTE FUNCTION access.reject_credential_identity_rewrite();
DROP TRIGGER IF EXISTS local_credential_revocation_monotonic ON access.local_credential;
CREATE TRIGGER local_credential_revocation_monotonic BEFORE UPDATE ON access.local_credential FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
DROP TRIGGER IF EXISTS api_token_no_delete ON access.api_token;
CREATE TRIGGER api_token_no_delete BEFORE DELETE ON access.api_token FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
DROP TRIGGER IF EXISTS api_token_identity_immutable ON access.api_token;
CREATE TRIGGER api_token_identity_immutable BEFORE UPDATE ON access.api_token FOR EACH ROW EXECUTE FUNCTION access.reject_token_identity_rewrite();
DROP TRIGGER IF EXISTS api_token_revocation_monotonic ON access.api_token;
CREATE TRIGGER api_token_revocation_monotonic BEFORE UPDATE ON access.api_token FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
DROP TRIGGER IF EXISTS service_secret_no_delete ON access.service_principal_secret;
CREATE TRIGGER service_secret_no_delete BEFORE DELETE ON access.service_principal_secret FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
DROP TRIGGER IF EXISTS service_secret_identity_immutable ON access.service_principal_secret;
CREATE TRIGGER service_secret_identity_immutable BEFORE UPDATE ON access.service_principal_secret FOR EACH ROW EXECUTE FUNCTION access.reject_service_secret_identity_rewrite();
DROP TRIGGER IF EXISTS service_secret_revocation_monotonic ON access.service_principal_secret;
CREATE TRIGGER service_secret_revocation_monotonic BEFORE UPDATE ON access.service_principal_secret FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
DROP TRIGGER IF EXISTS principal_preferences_no_delete ON access.principal_preferences;
CREATE TRIGGER principal_preferences_no_delete BEFORE DELETE ON access.principal_preferences FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
DROP TRIGGER IF EXISTS principal_preferences_revocation_monotonic ON access.principal_preferences;
CREATE TRIGGER principal_preferences_revocation_monotonic BEFORE UPDATE ON access.principal_preferences FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
DROP TRIGGER IF EXISTS principal_preferences_immutable ON access.principal_preferences;
CREATE TRIGGER principal_preferences_immutable BEFORE UPDATE ON access.principal_preferences FOR EACH ROW EXECUTE FUNCTION access.reject_preference_identity_rewrite();
DROP TRIGGER IF EXISTS avatar_object_no_delete ON access.avatar_object;
CREATE TRIGGER avatar_object_no_delete BEFORE DELETE ON access.avatar_object FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
DROP TRIGGER IF EXISTS avatar_object_immutable ON access.avatar_object;
CREATE TRIGGER avatar_object_immutable BEFORE UPDATE ON access.avatar_object FOR EACH ROW EXECUTE FUNCTION access.reject_object_identity_rewrite();
DROP TRIGGER IF EXISTS principal_avatar_no_delete ON access.principal_avatar;
CREATE TRIGGER principal_avatar_no_delete BEFORE DELETE ON access.principal_avatar FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
DROP TRIGGER IF EXISTS principal_avatar_immutable ON access.principal_avatar;
CREATE TRIGGER principal_avatar_immutable BEFORE UPDATE ON access.principal_avatar FOR EACH ROW EXECUTE FUNCTION access.reject_avatar_identity_rewrite();
DROP TRIGGER IF EXISTS principal_avatar_revocation_monotonic ON access.principal_avatar;
CREATE TRIGGER principal_avatar_revocation_monotonic BEFORE UPDATE ON access.principal_avatar FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
DROP TRIGGER IF EXISTS desktop_authorization_code_no_delete ON access.desktop_authorization_code;
CREATE TRIGGER desktop_authorization_code_no_delete BEFORE DELETE ON access.desktop_authorization_code FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
DROP TRIGGER IF EXISTS desktop_authorization_code_immutable ON access.desktop_authorization_code;
CREATE TRIGGER desktop_authorization_code_immutable BEFORE UPDATE ON access.desktop_authorization_code FOR EACH ROW EXECUTE FUNCTION access.reject_authoring_identity_rewrite();
DROP TRIGGER IF EXISTS desktop_authorization_code_consumption_monotonic ON access.desktop_authorization_code;
CREATE TRIGGER desktop_authorization_code_consumption_monotonic BEFORE UPDATE ON access.desktop_authorization_code FOR EACH ROW EXECUTE FUNCTION access.reject_consumption_rewind();
DROP TRIGGER IF EXISTS device_authorization_no_delete ON access.device_authorization;
CREATE TRIGGER device_authorization_no_delete BEFORE DELETE ON access.device_authorization FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
DROP TRIGGER IF EXISTS device_authorization_immutable ON access.device_authorization;
CREATE TRIGGER device_authorization_immutable BEFORE UPDATE ON access.device_authorization FOR EACH ROW EXECUTE FUNCTION access.reject_device_authorization_rewrite();
DROP TRIGGER IF EXISTS authoring_session_no_delete ON access.authoring_session;
CREATE TRIGGER authoring_session_no_delete BEFORE DELETE ON access.authoring_session FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
DROP TRIGGER IF EXISTS authoring_session_immutable ON access.authoring_session;
CREATE TRIGGER authoring_session_immutable BEFORE UPDATE ON access.authoring_session FOR EACH ROW EXECUTE FUNCTION access.reject_authoring_identity_rewrite();
DROP TRIGGER IF EXISTS authoring_session_revocation_monotonic ON access.authoring_session;
CREATE TRIGGER authoring_session_revocation_monotonic BEFORE UPDATE ON access.authoring_session FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
DROP TRIGGER IF EXISTS authoring_credential_no_delete ON access.authoring_credential;
CREATE TRIGGER authoring_credential_no_delete BEFORE DELETE ON access.authoring_credential FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
DROP TRIGGER IF EXISTS authoring_credential_immutable ON access.authoring_credential;
CREATE TRIGGER authoring_credential_immutable BEFORE UPDATE ON access.authoring_credential FOR EACH ROW EXECUTE FUNCTION access.reject_authoring_identity_rewrite();
DROP TRIGGER IF EXISTS authoring_credential_transition ON access.authoring_credential;
CREATE TRIGGER authoring_credential_transition BEFORE UPDATE ON access.authoring_credential FOR EACH ROW EXECUTE FUNCTION access.reject_authoring_credential_transition();
DROP TRIGGER IF EXISTS authorization_snapshot_no_delete ON access.authorization_snapshot;
CREATE TRIGGER authorization_snapshot_no_delete BEFORE DELETE ON access.authorization_snapshot FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
DROP TRIGGER IF EXISTS authorization_snapshot_immutable ON access.authorization_snapshot;
CREATE TRIGGER authorization_snapshot_immutable BEFORE UPDATE ON access.authorization_snapshot FOR EACH ROW EXECUTE FUNCTION access.reject_authorization_identity_rewrite();
DROP TRIGGER IF EXISTS authorization_role_binding_no_delete ON access.authorization_role_binding;
CREATE TRIGGER authorization_role_binding_no_delete BEFORE DELETE ON access.authorization_role_binding FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
DROP TRIGGER IF EXISTS authorization_role_binding_immutable ON access.authorization_role_binding;
CREATE TRIGGER authorization_role_binding_immutable BEFORE UPDATE ON access.authorization_role_binding FOR EACH ROW EXECUTE FUNCTION access.reject_authorization_identity_rewrite();
DROP TRIGGER IF EXISTS authorization_role_binding_revocation_monotonic ON access.authorization_role_binding;
CREATE TRIGGER authorization_role_binding_revocation_monotonic BEFORE UPDATE ON access.authorization_role_binding FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
DROP TRIGGER IF EXISTS authorization_grant_no_delete ON access.authorization_grant;
CREATE TRIGGER authorization_grant_no_delete BEFORE DELETE ON access.authorization_grant FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
DROP TRIGGER IF EXISTS authorization_grant_immutable ON access.authorization_grant;
CREATE TRIGGER authorization_grant_immutable BEFORE UPDATE ON access.authorization_grant FOR EACH ROW EXECUTE FUNCTION access.reject_authorization_identity_rewrite();
DROP TRIGGER IF EXISTS authorization_grant_revocation_monotonic ON access.authorization_grant;
CREATE TRIGGER authorization_grant_revocation_monotonic BEFORE UPDATE ON access.authorization_grant FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
DROP TRIGGER IF EXISTS authorization_data_policy_no_delete ON access.authorization_data_policy;
CREATE TRIGGER authorization_data_policy_no_delete BEFORE DELETE ON access.authorization_data_policy FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
DROP TRIGGER IF EXISTS authorization_data_policy_immutable ON access.authorization_data_policy;
CREATE TRIGGER authorization_data_policy_immutable BEFORE UPDATE ON access.authorization_data_policy FOR EACH ROW EXECUTE FUNCTION access.reject_authorization_identity_rewrite();
DROP TRIGGER IF EXISTS authorization_data_policy_revocation_monotonic ON access.authorization_data_policy;
CREATE TRIGGER authorization_data_policy_revocation_monotonic BEFORE UPDATE ON access.authorization_data_policy FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
DROP TRIGGER IF EXISTS authorization_revocation_append_only ON access.authorization_revocation;
CREATE TRIGGER authorization_revocation_append_only BEFORE UPDATE OR DELETE ON access.authorization_revocation FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();

-- Keep the grants explicit and stable even when this migration is retried.
REVOKE ALL ON SCHEMA access FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA access FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA access FROM PUBLIC;
GRANT USAGE ON SCHEMA access TO leapview_control_migrator, leapview_control_runtime, leapview_control_readonly;
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA access TO leapview_control_migrator;
GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA access TO leapview_control_runtime;
REVOKE DELETE ON ALL TABLES IN SCHEMA access FROM leapview_control_runtime;
GRANT DELETE ON access.oauth_session, access.oauth_client_assertion TO leapview_control_runtime;
GRANT SELECT ON ALL TABLES IN SCHEMA access TO leapview_control_readonly;
REVOKE SELECT ON access.session, access.local_credential, access.api_token,
    access.service_principal_secret, access.desktop_authorization_code,
    access.device_authorization, access.authoring_credential,
    access.oauth_client, access.oauth_session, access.oauth_client_assertion
    FROM leapview_control_readonly;
-- These baseline generic projections are retired by the typed repositories.
-- Quarantine them: runtime has no need to read or mutate them, readonly must
-- never inspect legacy verifier material or stale authorization predicates.
REVOKE SELECT, INSERT, UPDATE ON access.credential, access.access_grant
    FROM leapview_control_runtime;
REVOKE SELECT ON access.credential, access.access_grant
    FROM leapview_control_readonly;

-- Revision 001 grants runtime DELETE and readonly SELECT on every future
-- access table.  Fail closed for future access-owned tables; a later
-- migration must grant either privilege explicitly for an exceptional table.
ALTER DEFAULT PRIVILEGES FOR ROLE leapview_control_owner IN SCHEMA access
    REVOKE DELETE ON TABLES FROM leapview_control_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE leapview_control_owner IN SCHEMA access
    REVOKE SELECT ON TABLES FROM leapview_control_readonly;
GRANT EXECUTE ON FUNCTION access.valid_capabilities(jsonb) TO leapview_control_runtime;
REVOKE ALL ON FUNCTION access.reject_access_delete() FROM PUBLIC;
REVOKE ALL ON FUNCTION access.reject_revocation_clear() FROM PUBLIC;
REVOKE ALL ON FUNCTION access.reject_principal_identity_rewrite() FROM PUBLIC;
REVOKE ALL ON FUNCTION access.reject_group_identity_rewrite() FROM PUBLIC;
REVOKE ALL ON FUNCTION access.reject_role_identity_rewrite() FROM PUBLIC;
REVOKE ALL ON FUNCTION access.reject_membership_identity_rewrite() FROM PUBLIC;
REVOKE ALL ON FUNCTION access.reject_external_identity_rewrite() FROM PUBLIC;
REVOKE ALL ON FUNCTION access.reject_session_identity_rewrite() FROM PUBLIC;
REVOKE ALL ON FUNCTION access.reject_token_identity_rewrite() FROM PUBLIC;
REVOKE ALL ON FUNCTION access.reject_service_secret_identity_rewrite() FROM PUBLIC;
REVOKE ALL ON FUNCTION access.reject_credential_identity_rewrite() FROM PUBLIC;
REVOKE ALL ON FUNCTION access.reject_preference_identity_rewrite() FROM PUBLIC;
REVOKE ALL ON FUNCTION access.reject_avatar_identity_rewrite() FROM PUBLIC;
REVOKE ALL ON FUNCTION access.reject_object_identity_rewrite() FROM PUBLIC;
REVOKE ALL ON FUNCTION access.reject_consumption_rewind() FROM PUBLIC;
REVOKE ALL ON FUNCTION access.reject_device_authorization_rewrite() FROM PUBLIC;
REVOKE ALL ON FUNCTION access.reject_authoring_identity_rewrite() FROM PUBLIC;
REVOKE ALL ON FUNCTION access.reject_authoring_credential_transition() FROM PUBLIC;
REVOKE ALL ON FUNCTION access.reject_authorization_identity_rewrite() FROM PUBLIC;

RESET ROLE;
