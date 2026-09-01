-- Clean PostgreSQL access authority baseline. This file is applied to an
-- empty control database by the access capability migration; it deliberately
-- contains no compatibility ALTERs or SQLite-era projections.
CREATE SCHEMA IF NOT EXISTS access;
CREATE SCHEMA IF NOT EXISTS audit;
CREATE TABLE IF NOT EXISTS access.platform_setting (
    key text PRIMARY KEY CHECK (key = btrim(key) AND length(key) BETWEEN 1 AND 255),
    value text NOT NULL CHECK (length(value)<=2048),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE IF NOT EXISTS audit.audit_event (
    audit_id uuid PRIMARY KEY,
    -- Delivery activation uses the same immutable identity for its durable
    -- domain event and audit record.  Keep that relationship on the
    -- access-owned audit authority rather than introducing a second audit
    -- table in the deployment capability.
    event_id uuid UNIQUE,
    actor_id text CHECK (actor_id IS NULL OR (actor_id = btrim(actor_id) AND length(actor_id) BETWEEN 1 AND 255)),
    scope_id text CHECK (scope_id IS NULL OR (scope_id = btrim(scope_id) AND length(scope_id) BETWEEN 1 AND 255)),
    principal_id uuid,
    source text NOT NULL CHECK (length(source)<=128),
    operation text NOT NULL CHECK (length(operation)<=255),
    action text NOT NULL CHECK (length(action)<=255),
    resource_kind text,
    resource_id text,
    project_id text CHECK (project_id IS NULL OR (project_id = btrim(project_id) AND length(project_id) BETWEEN 1 AND 255)),
    environment text CHECK (environment IS NULL OR (environment = btrim(environment) AND length(environment) BETWEEN 1 AND 128)),
    generation_id text CHECK (generation_id IS NULL OR (generation_id = btrim(generation_id) AND length(generation_id) BETWEEN 1 AND 255)),
    capability text NOT NULL DEFAULT '',
    outcome text NOT NULL DEFAULT 'success',
    request_digest text CHECK (request_digest IS NULL OR request_digest ~ '^sha256:[0-9a-f]{64}$'),
    request_id text CHECK (request_id IS NULL OR (request_id = btrim(request_id) AND length(request_id) BETWEEN 1 AND 256)),
    correlation_id text CHECK (correlation_id IS NULL OR (correlation_id = btrim(correlation_id) AND length(correlation_id) BETWEEN 1 AND 256)),
    aggregate_key text NOT NULL DEFAULT '',
    aggregate_sequence bigint NOT NULL DEFAULT 0,
    intent_digest text NOT NULL DEFAULT '',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata)='object' AND octet_length(metadata::text)<=32768),
    occurred_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX IF NOT EXISTS audit_event_retention_order_idx
    ON audit.audit_event (occurred_at, audit_id);

-- The floor is durable evidence of the policy boundary used by the last
-- bounded retention batch.  It is a cursor, not an authorization shortcut:
-- append-only producers continue to write audit_event directly.
CREATE TABLE IF NOT EXISTS audit.audit_retention_floor (
    retention_class text PRIMARY KEY CHECK (retention_class IN ('short', 'standard', 'security')),
    floor_at timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00'::timestamptz,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
INSERT INTO audit.audit_retention_floor (retention_class)
VALUES ('short'), ('standard'), ('security')
ON CONFLICT (retention_class) DO NOTHING;

-- Operational auth state has one monotonic floor. Audit events are final
-- immutable inserts on PostgreSQL (there is no same-database outbox).
CREATE TABLE IF NOT EXISTS access.access_retention_floor (
    retention_class text PRIMARY KEY CHECK (retention_class = 'auth_state'),
    floor_at timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00'::timestamptz,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
INSERT INTO access.access_retention_floor (retention_class)
VALUES ('auth_state')
ON CONFLICT (retention_class) DO NOTHING;

-- Audit history is immutable to runtime callers.  A deletion can only be
-- reached through the bounded SECURITY DEFINER function below, which sets a
-- transaction-local marker and is itself executable only by maintenance.
CREATE OR REPLACE FUNCTION audit.reject_audit_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        -- The database owns evidence time.  Even an owner or runtime caller
        -- supplying an explicit age cannot make a fresh event look old.
        NEW.occurred_at := statement_timestamp();
        RETURN NEW;
    END IF;
    IF TG_OP = 'DELETE'
       AND current_setting('audit.maintenance', true) = 'on'
       AND session_user = 'leapview_control_maintenance' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'audit history is immutable';
END;
$$;
DROP TRIGGER IF EXISTS audit_event_immutable ON audit.audit_event;
CREATE TRIGGER audit_event_immutable BEFORE INSERT OR UPDATE OR DELETE ON audit.audit_event FOR EACH ROW EXECUTE FUNCTION audit.reject_audit_mutation();

-- Retention is a maintenance capability, never a runtime table privilege.
-- Every invocation is capped by the database clock and one bounded candidate
-- batch.  Candidate rows are inspected and locked before eligibility is
-- decided; malformed retention envelopes are retained for operator review
-- rather than silently discarded.  Valid envelopes (short, standard, or
-- security) follow the explicitly supplied policy cutoff.
CREATE OR REPLACE FUNCTION audit.prune_audit_events(
    p_retention_class text,
    p_requested_cutoff timestamptz,
    p_batch_limit integer
)
RETURNS TABLE (
    retention_class text,
    requested_cutoff timestamptz,
    cutoff timestamptz,
    requested_limit integer,
    removed_count bigint,
    retained_floor timestamptz
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, audit
AS $$
DECLARE
    v_floor timestamptz;
    v_target timestamptz;
    v_remaining timestamptz;
    v_removed bigint := 0;
BEGIN
    IF session_user <> 'leapview_control_maintenance' THEN
        RAISE EXCEPTION 'audit retention requires the maintenance capability';
    END IF;
    IF p_retention_class IS NULL OR p_retention_class NOT IN ('short', 'standard', 'security') THEN
        RAISE EXCEPTION 'audit retention class must be short, standard, or security';
    END IF;
    IF p_requested_cutoff IS NULL THEN
        RAISE EXCEPTION 'audit retention cutoff is required';
    END IF;
    IF p_batch_limit IS NULL OR p_batch_limit < 1 OR p_batch_limit > 1000 THEN
        RAISE EXCEPTION 'audit retention batch limit must be between 1 and 1000';
    END IF;

    SELECT f.floor_at INTO STRICT v_floor
      FROM audit.audit_retention_floor f
     WHERE f.retention_class = p_retention_class
     FOR UPDATE;

    retention_class := p_retention_class;
    requested_cutoff := p_requested_cutoff;
    requested_limit := p_batch_limit;
    -- Never let an operator-provided future cutoff delete newly-written rows.
    v_target := GREATEST(v_floor, LEAST(p_requested_cutoff, clock_timestamp()));

    cutoff := v_target;

    -- The CTE first locks and inspects the exact rows to be removed.  The
    -- trigger marker is transaction-local and cannot be used by runtime SQL,
    -- which has neither DELETE nor EXECUTE privilege.
    PERFORM set_config('audit.maintenance', 'on', true);
    WITH candidates AS (
        SELECT e.audit_id, e.occurred_at, e.source, e.operation,
               e.action, e.outcome, e.metadata
         FROM audit.audit_event e
         WHERE e.occurred_at < v_target
           AND CASE
                 WHEN e.metadata ? 'retention' THEN e.metadata->>'retention' = p_retention_class
                 ELSE p_retention_class = 'standard'
               END
         ORDER BY e.occurred_at, e.audit_id
         FOR UPDATE SKIP LOCKED
         LIMIT p_batch_limit
    ), deleted AS (
        DELETE FROM audit.audit_event e
         USING candidates c
         WHERE e.audit_id = c.audit_id
         RETURNING e.audit_id
    )
    SELECT count(*) INTO v_removed FROM deleted;

    -- Derive the floor from the rows still visible after the bounded delete.
    -- This keeps a full backlog, or a row skipped by a concurrent lock, from
    -- being represented as already retained past the actual evidence.
    SELECT min(e.occurred_at) INTO v_remaining
      FROM audit.audit_event e
     WHERE e.occurred_at < v_target
       AND CASE
             WHEN e.metadata ? 'retention' THEN e.metadata->>'retention' = p_retention_class
             ELSE p_retention_class = 'standard'
           END;
    IF v_remaining IS NULL THEN
        v_remaining := v_target;
    END IF;
    IF v_remaining > v_floor THEN
        UPDATE audit.audit_retention_floor
           SET floor_at = v_remaining, updated_at = clock_timestamp()
         WHERE audit_retention_floor.retention_class = p_retention_class;
        v_floor := v_remaining;
    END IF;
    cutoff := v_target;
    removed_count := v_removed;
    retained_floor := v_floor;
    RETURN NEXT;
END;
$$;
REVOKE ALL ON FUNCTION audit.prune_audit_events(text, timestamptz, integer) FROM PUBLIC;

-- Remove expired or explicitly revoked access credentials in one bounded
-- batch. Every candidate is locked before deletion, so concurrent maintenance
-- workers do not double-delete and a locked backlog keeps the durable floor
-- below the requested boundary. Final audit events use their independent
-- class-based retention function above; PostgreSQL has no audit outbox.
CREATE OR REPLACE FUNCTION access.prune_auth_state(
    p_requested_cutoff timestamptz,
    p_batch_limit integer
)
RETURNS TABLE (
    requested_cutoff timestamptz,
    cutoff timestamptz,
    requested_limit integer,
    sessions_removed bigint,
    oauth_sessions_removed bigint,
    oauth_assertions_removed bigint,
    desktop_codes_removed bigint,
    device_authorizations_removed bigint,
    api_tokens_removed bigint,
    service_secrets_removed bigint,
    authoring_sessions_removed bigint,
    authoring_credentials_removed bigint,
    auth_state_floor timestamptz
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, access, audit
AS $$
DECLARE
    v_auth_floor timestamptz;
    v_target timestamptz;
    v_total bigint := 0;
    v_removed bigint := 0;
    v_remaining integer;
    v_auth_remaining boolean;
BEGIN
    IF session_user <> 'leapview_control_maintenance' THEN
        RAISE EXCEPTION 'access retention requires the maintenance capability';
    END IF;
    IF p_requested_cutoff IS NULL THEN
        RAISE EXCEPTION 'access retention cutoff is required';
    END IF;
    IF p_batch_limit IS NULL OR p_batch_limit < 1 OR p_batch_limit > 1000 THEN
        RAISE EXCEPTION 'access retention batch limit must be between 1 and 1000';
    END IF;

    SELECT floor_at INTO STRICT v_auth_floor
      FROM access.access_retention_floor
     WHERE retention_class = 'auth_state'
     FOR UPDATE;
    requested_cutoff := p_requested_cutoff;
    requested_limit := p_batch_limit;
    -- Database time is authoritative. A replay with an older requested
    -- cutoff must not widen the deletion predicate to the already-advanced
    -- floor; the floor itself remains monotonic as durable evidence.
    v_target := LEAST(p_requested_cutoff, clock_timestamp());
    cutoff := v_target;
    PERFORM set_config('access.maintenance', 'on', true);

    -- Inactive OAuth request state is opaque but no longer usable. Active
    -- sessions are retained even when old so token replay evidence remains
    -- available to the runtime verifier.
    v_remaining := p_batch_limit;
    WITH candidates AS (
        SELECT s.kind, s.signature
          FROM access.oauth_session s
         WHERE s.created_at < v_target AND s.active = false
         ORDER BY s.created_at, s.kind, s.signature
         FOR UPDATE SKIP LOCKED
         LIMIT v_remaining
    ), deleted AS (
        DELETE FROM access.oauth_session s USING candidates c
         WHERE s.kind = c.kind AND s.signature = c.signature
         RETURNING s.signature
    )
    SELECT count(*) INTO v_removed FROM deleted;
    oauth_sessions_removed := v_removed;
    v_total := v_total + v_removed;

    IF v_total < p_batch_limit THEN
        v_remaining := p_batch_limit - v_total::integer;
        WITH candidates AS (
            SELECT a.jti
              FROM access.oauth_client_assertion a
             WHERE a.expires_at < v_target
             ORDER BY a.expires_at, a.jti
             FOR UPDATE SKIP LOCKED
             LIMIT v_remaining
        ), deleted AS (
            DELETE FROM access.oauth_client_assertion a USING candidates c
             WHERE a.jti = c.jti
             RETURNING a.jti
        )
        SELECT count(*) INTO v_removed FROM deleted;
    ELSE
        v_removed := 0;
    END IF;
    oauth_assertions_removed := v_removed;
    v_total := v_total + v_removed;

    IF v_total < p_batch_limit THEN
        v_remaining := p_batch_limit - v_total::integer;
        WITH candidates AS (
            SELECT c.code_hash
              FROM access.desktop_authorization_code c
             WHERE c.expires_at < v_target
                OR (c.consumed_at IS NOT NULL AND c.consumed_at < v_target)
             ORDER BY c.expires_at, c.code_hash
             FOR UPDATE SKIP LOCKED
             LIMIT v_remaining
        ), deleted AS (
            DELETE FROM access.desktop_authorization_code c USING candidates d
             WHERE c.code_hash = d.code_hash
             RETURNING c.code_hash
        )
        SELECT count(*) INTO v_removed FROM deleted;
    ELSE
        v_removed := 0;
    END IF;
    desktop_codes_removed := v_removed;
    v_total := v_total + v_removed;

    IF v_total < p_batch_limit THEN
        v_remaining := p_batch_limit - v_total::integer;
        WITH candidates AS (
            SELECT d.id
              FROM access.device_authorization d
             WHERE d.expires_at < v_target
                OR (d.status = 'denied' AND d.denied_at IS NOT NULL AND d.denied_at < v_target)
                OR (d.status = 'consumed' AND d.consumed_at IS NOT NULL AND d.consumed_at < v_target)
             ORDER BY d.expires_at, d.id
             FOR UPDATE SKIP LOCKED
             LIMIT v_remaining
        ), deleted AS (
            DELETE FROM access.device_authorization d USING candidates c
             WHERE d.id = c.id
             RETURNING d.id
        )
        SELECT count(*) INTO v_removed FROM deleted;
    ELSE
        v_removed := 0;
    END IF;
    device_authorizations_removed := v_removed;
    v_total := v_total + v_removed;

    IF v_total < p_batch_limit THEN
        v_remaining := p_batch_limit - v_total::integer;
        WITH candidates AS (
            SELECT t.id
              FROM access.api_token t
             WHERE (t.expires_at < v_target)
                OR (t.revoked_at IS NOT NULL AND t.revoked_at < v_target)
             ORDER BY LEAST(t.expires_at, COALESCE(t.revoked_at, t.expires_at)), t.id
             FOR UPDATE SKIP LOCKED
             LIMIT v_remaining
        ), deleted AS (
            DELETE FROM access.api_token t USING candidates c
             WHERE t.id = c.id
             RETURNING t.id
        )
        SELECT count(*) INTO v_removed FROM deleted;
    ELSE
        v_removed := 0;
    END IF;
    api_tokens_removed := v_removed;
    v_total := v_total + v_removed;

    IF v_total < p_batch_limit THEN
        v_remaining := p_batch_limit - v_total::integer;
        WITH candidates AS (
            SELECT s.id
              FROM access.service_principal_secret s
             WHERE (s.expires_at < v_target)
                OR (s.revoked_at IS NOT NULL AND s.revoked_at < v_target)
             ORDER BY LEAST(s.expires_at, COALESCE(s.revoked_at, s.expires_at)), s.id
             FOR UPDATE SKIP LOCKED
             LIMIT v_remaining
        ), deleted AS (
            DELETE FROM access.service_principal_secret s USING candidates c
             WHERE s.id = c.id
             RETURNING s.id
        )
        SELECT count(*) INTO v_removed FROM deleted;
    ELSE
        v_removed := 0;
    END IF;
    service_secrets_removed := v_removed;
    v_total := v_total + v_removed;

    IF v_total < p_batch_limit THEN
        v_remaining := p_batch_limit - v_total::integer;
        WITH candidates AS (
            SELECT s.id
              FROM access.session s
             WHERE (s.expires_at < v_target)
                OR (s.revoked_at IS NOT NULL AND s.revoked_at < v_target)
             ORDER BY LEAST(s.expires_at, COALESCE(s.revoked_at, s.expires_at)), s.id
             FOR UPDATE SKIP LOCKED
             LIMIT v_remaining
        ), deleted AS (
            DELETE FROM access.session s USING candidates c
             WHERE s.id = c.id
             RETURNING s.id
        )
        SELECT count(*) INTO v_removed FROM deleted;
    ELSE
        v_removed := 0;
    END IF;
    sessions_removed := v_removed;
    v_total := v_total + v_removed;

    -- Credentials are children of authoring sessions and must drain first;
    -- a revoked parent invalidates its credentials even when their refresh
    -- expiry is still in the future.
    IF v_total < p_batch_limit THEN
        v_remaining := p_batch_limit - v_total::integer;
        WITH candidates AS (
            SELECT c.id
              FROM access.authoring_credential c
              JOIN access.authoring_session s ON s.id = c.session_id
             WHERE (s.revoked_at IS NOT NULL AND s.revoked_at < v_target)
                OR (c.replaced_at IS NOT NULL AND c.replaced_at < v_target)
                OR (c.refresh_expires_at IS NOT NULL AND c.refresh_expires_at < v_target)
                OR (c.refresh_expires_at IS NULL AND c.access_expires_at < v_target)
             ORDER BY c.created_at, c.id
             FOR UPDATE OF c SKIP LOCKED
             LIMIT v_remaining
        ), deleted AS (
            DELETE FROM access.authoring_credential c USING candidates d
             WHERE c.id = d.id
             RETURNING c.id
        )
        SELECT count(*) INTO v_removed FROM deleted;
    ELSE
        v_removed := 0;
    END IF;
    authoring_credentials_removed := v_removed;
    v_total := v_total + v_removed;

    IF v_total < p_batch_limit THEN
        v_remaining := p_batch_limit - v_total::integer;
        WITH candidates AS (
            SELECT s.id
              FROM access.authoring_session s
             WHERE (s.expires_at < v_target)
                OR (s.revoked_at IS NOT NULL AND s.revoked_at < v_target)
             ORDER BY LEAST(s.expires_at, COALESCE(s.revoked_at, s.expires_at)), s.id
             FOR UPDATE SKIP LOCKED
             LIMIT v_remaining
        ), deleted AS (
            DELETE FROM access.authoring_session s USING candidates c
             WHERE s.id = c.id
               AND NOT EXISTS (SELECT 1 FROM access.authoring_credential x WHERE x.session_id = s.id)
             RETURNING s.id
        )
        SELECT count(*) INTO v_removed FROM deleted;
    ELSE
        v_removed := 0;
    END IF;
    authoring_sessions_removed := v_removed;
    v_total := v_total + v_removed;

    -- Floors only advance when no eligible row remains. This is deliberately
    -- checked after the batch so a smaller limit or SKIP LOCKED row remains
    -- visible as backlog evidence to the next invocation.
    SELECT EXISTS (
        SELECT 1 FROM access.session s
         WHERE (s.expires_at < v_target) OR (s.revoked_at IS NOT NULL AND s.revoked_at < v_target)
        UNION ALL SELECT 1 FROM access.oauth_session s WHERE s.created_at < v_target AND s.active = false
        UNION ALL SELECT 1 FROM access.oauth_client_assertion a WHERE a.expires_at < v_target
        UNION ALL SELECT 1 FROM access.desktop_authorization_code c WHERE c.expires_at < v_target OR (c.consumed_at IS NOT NULL AND c.consumed_at < v_target)
        UNION ALL SELECT 1 FROM access.device_authorization d WHERE d.expires_at < v_target OR (d.status='denied' AND d.denied_at IS NOT NULL AND d.denied_at < v_target) OR (d.status='consumed' AND d.consumed_at IS NOT NULL AND d.consumed_at < v_target)
        UNION ALL SELECT 1 FROM access.api_token t WHERE t.expires_at < v_target OR (t.revoked_at IS NOT NULL AND t.revoked_at < v_target)
        UNION ALL SELECT 1 FROM access.service_principal_secret s WHERE s.expires_at < v_target OR (s.revoked_at IS NOT NULL AND s.revoked_at < v_target)
        UNION ALL SELECT 1 FROM access.authoring_credential c JOIN access.authoring_session s ON s.id=c.session_id WHERE (s.revoked_at IS NOT NULL AND s.revoked_at < v_target) OR (c.replaced_at IS NOT NULL AND c.replaced_at < v_target) OR (c.refresh_expires_at IS NOT NULL AND c.refresh_expires_at < v_target) OR (c.refresh_expires_at IS NULL AND c.access_expires_at < v_target)
        UNION ALL SELECT 1 FROM access.authoring_session s WHERE (s.expires_at < v_target OR (s.revoked_at IS NOT NULL AND s.revoked_at < v_target)) AND NOT EXISTS (SELECT 1 FROM access.authoring_credential c WHERE c.session_id=s.id)
    ) INTO v_auth_remaining;
    IF NOT v_auth_remaining AND v_target > v_auth_floor THEN
        UPDATE access.access_retention_floor
           SET floor_at = v_target, updated_at = clock_timestamp()
         WHERE retention_class = 'auth_state';
        v_auth_floor := v_target;
    END IF;

    cutoff := v_target;
    sessions_removed := COALESCE(sessions_removed, 0);
    oauth_sessions_removed := COALESCE(oauth_sessions_removed, 0);
    oauth_assertions_removed := COALESCE(oauth_assertions_removed, 0);
    desktop_codes_removed := COALESCE(desktop_codes_removed, 0);
    device_authorizations_removed := COALESCE(device_authorizations_removed, 0);
    api_tokens_removed := COALESCE(api_tokens_removed, 0);
    service_secrets_removed := COALESCE(service_secrets_removed, 0);
    authoring_sessions_removed := COALESCE(authoring_sessions_removed, 0);
    authoring_credentials_removed := COALESCE(authoring_credentials_removed, 0);
    auth_state_floor := v_auth_floor;
    RETURN NEXT;
END;
$$;
REVOKE ALL ON FUNCTION access.prune_auth_state(timestamptz, integer) FROM PUBLIC;

CREATE TABLE access.principal (
    id uuid PRIMARY KEY,
    principal_type text NOT NULL CHECK (principal_type IN ('user','service','system','dashboard_publication')),
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
CREATE OR REPLACE FUNCTION access.allow_maintenance_delete() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    IF current_setting('access.maintenance', true) = 'on'
       AND session_user = 'leapview_control_maintenance' THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'access state deletion requires bounded maintenance';
END;
$$;
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
CREATE OR REPLACE FUNCTION access.reject_preference_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.preference_id<>NEW.preference_id OR OLD.principal_id<>NEW.principal_id OR OLD.theme<>NEW.theme OR OLD.created_at IS DISTINCT FROM NEW.created_at THEN
        RAISE EXCEPTION 'preference identity is immutable';
    END IF;
    RETURN NEW;
END; $$;
CREATE OR REPLACE FUNCTION access.reject_avatar_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.avatar_id<>NEW.avatar_id OR OLD.principal_id<>NEW.principal_id OR OLD.sha256<>NEW.sha256 OR OLD.media_type<>NEW.media_type OR OLD.size_bytes<>NEW.size_bytes OR OLD.width<>NEW.width OR OLD.height<>NEW.height OR OLD.updated_at<>NEW.updated_at THEN
        RAISE EXCEPTION 'avatar identity is immutable';
    END IF;
    RETURN NEW;
END; $$;
CREATE OR REPLACE FUNCTION access.reject_object_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'avatar object identity is immutable';
END; $$;
CREATE OR REPLACE FUNCTION access.reject_consumption_rewind() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.consumed_at IS NOT NULL AND (NEW.consumed_at IS NULL OR NEW.consumed_at < OLD.consumed_at) THEN
        RAISE EXCEPTION 'consumption is monotonic';
    END IF;
    RETURN NEW;
END; $$;
CREATE OR REPLACE FUNCTION access.reject_device_authorization_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.id<>NEW.id OR OLD.client_id<>NEW.client_id OR OLD.device_code_hash<>NEW.device_code_hash OR OLD.user_code_hash<>NEW.user_code_hash OR OLD.target_id<>NEW.target_id OR OLD.project_id<>NEW.project_id OR OLD.capabilities IS DISTINCT FROM NEW.capabilities OR OLD.created_at<>NEW.created_at OR OLD.expires_at<>NEW.expires_at THEN
        RAISE EXCEPTION 'device authorization identity is immutable';
    END IF;
    IF OLD.status='pending' AND NEW.status NOT IN ('pending','approved','denied') THEN
        RAISE EXCEPTION 'invalid device authorization transition';
    ELSIF OLD.status='approved' AND NEW.status NOT IN ('approved','consumed') THEN
        RAISE EXCEPTION 'invalid device authorization transition';
    ELSIF OLD.status IN ('denied','consumed') AND NEW.status<>OLD.status THEN
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
END; $$;
CREATE OR REPLACE FUNCTION access.reject_authoring_credential_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.active = FALSE AND NEW.active <> FALSE THEN
        RAISE EXCEPTION 'authoring credential activation is not reversible';
    END IF;
    IF OLD.replaced_at IS NOT NULL AND (NEW.replaced_at IS NULL OR NEW.replaced_at < OLD.replaced_at) THEN
        RAISE EXCEPTION 'credential replacement timestamp is monotonic';
    END IF;
    RETURN NEW;
END; $$;

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
CREATE TRIGGER session_no_delete BEFORE DELETE ON access.session FOR EACH ROW EXECUTE FUNCTION access.allow_maintenance_delete();
CREATE TRIGGER session_identity_immutable BEFORE UPDATE ON access.session FOR EACH ROW EXECUTE FUNCTION access.reject_session_identity_rewrite();
CREATE TRIGGER session_revocation_monotonic BEFORE UPDATE ON access.session FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER api_token_no_delete BEFORE DELETE ON access.api_token FOR EACH ROW EXECUTE FUNCTION access.allow_maintenance_delete();
CREATE TRIGGER api_token_identity_immutable BEFORE UPDATE ON access.api_token FOR EACH ROW EXECUTE FUNCTION access.reject_token_identity_rewrite();
CREATE TRIGGER api_token_revocation_monotonic BEFORE UPDATE ON access.api_token FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER service_secret_no_delete BEFORE DELETE ON access.service_principal_secret FOR EACH ROW EXECUTE FUNCTION access.allow_maintenance_delete();
CREATE TRIGGER service_secret_identity_immutable BEFORE UPDATE ON access.service_principal_secret FOR EACH ROW EXECUTE FUNCTION access.reject_service_secret_identity_rewrite();
CREATE TRIGGER service_secret_revocation_monotonic BEFORE UPDATE ON access.service_principal_secret FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER local_credential_no_delete BEFORE DELETE ON access.local_credential FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER local_credential_identity_immutable BEFORE UPDATE ON access.local_credential FOR EACH ROW EXECUTE FUNCTION access.reject_credential_identity_rewrite();
CREATE TRIGGER local_credential_revocation_monotonic BEFORE UPDATE ON access.local_credential FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER external_identity_no_delete BEFORE DELETE ON access.external_identity FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER external_identity_identity_immutable BEFORE UPDATE ON access.external_identity FOR EACH ROW EXECUTE FUNCTION access.reject_external_identity_rewrite();
CREATE TRIGGER external_identity_revocation_monotonic BEFORE UPDATE ON access.external_identity FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER platform_setting_no_mutation BEFORE UPDATE OR DELETE ON access.platform_setting FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();

-- Project-generation authorization is owned by access.  Snapshot rows and
-- their children are immutable evidence; mutable administrative bindings use
-- revocation tombstones so history can be reconciled without hard deletes.
CREATE TABLE access.authorization_snapshot (
    project_id text NOT NULL CHECK (project_id = btrim(project_id) AND length(project_id) BETWEEN 1 AND 255),
    environment text NOT NULL CHECK (environment = btrim(environment) AND length(environment) BETWEEN 1 AND 128),
    generation_id text NOT NULL CHECK (generation_id = btrim(generation_id) AND length(generation_id) BETWEEN 1 AND 255),
    digest text NOT NULL CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (project_id, environment, generation_id)
);

CREATE TABLE access.authorization_role_binding (
    id text NOT NULL CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 255),
    project_id text NOT NULL,
    environment text NOT NULL,
    generation_id text NOT NULL,
    subject_kind text NOT NULL CHECK (subject_kind IN ('principal','group')),
    subject_id text NOT NULL CHECK (subject_id = btrim(subject_id) AND length(subject_id) BETWEEN 1 AND 255),
    role text NOT NULL CHECK (role IN ('owner','admin','deployer','data_deployer','contributor','editor','member','viewer')),
    capabilities jsonb NOT NULL CHECK (access.valid_capabilities(capabilities) AND jsonb_typeof(capabilities)='array' AND octet_length(capabilities::text)<=2048),
    name text NOT NULL DEFAULT '' CHECK (length(name)<=255),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz,
    PRIMARY KEY (project_id, environment, generation_id, id),
    FOREIGN KEY (project_id, environment, generation_id) REFERENCES access.authorization_snapshot(project_id, environment, generation_id)
);
CREATE UNIQUE INDEX authorization_role_binding_active_key ON access.authorization_role_binding(project_id, environment, generation_id, subject_kind, subject_id, role) WHERE revoked_at IS NULL;
CREATE INDEX authorization_role_binding_subject_idx ON access.authorization_role_binding(project_id, environment, generation_id, subject_kind, subject_id);

CREATE TABLE access.authorization_grant (
    id text NOT NULL CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 255),
    project_id text NOT NULL,
    environment text NOT NULL,
    generation_id text NOT NULL,
    subject_kind text NOT NULL CHECK (subject_kind IN ('principal','group')),
    subject_id text NOT NULL CHECK (subject_id = btrim(subject_id) AND length(subject_id) BETWEEN 1 AND 255),
    resource_id text NOT NULL CHECK (resource_id = btrim(resource_id) AND length(resource_id) BETWEEN 1 AND 255),
    resource_kind text NOT NULL CHECK (resource_kind = btrim(resource_kind) AND length(resource_kind) BETWEEN 1 AND 128),
    capability text NOT NULL CHECK (capability IN ('PROJECT_ADMIN','RESOURCE_USE','RESOURCE_READ','RESOURCE_EDIT','RESOURCE_MANAGE','RESOURCE_SHARE','RESOURCE_PUBLISH')),
    name text NOT NULL DEFAULT '' CHECK (length(name)<=255),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz,
    PRIMARY KEY (project_id, environment, generation_id, id),
    FOREIGN KEY (project_id, environment, generation_id) REFERENCES access.authorization_snapshot(project_id, environment, generation_id)
);
CREATE UNIQUE INDEX authorization_grant_active_key ON access.authorization_grant(project_id, environment, generation_id, subject_kind, subject_id, resource_id, resource_kind, capability) WHERE revoked_at IS NULL;
CREATE INDEX authorization_grant_subject_idx ON access.authorization_grant(project_id, environment, generation_id, subject_kind, subject_id);
CREATE INDEX authorization_grant_resource_idx ON access.authorization_grant(project_id, environment, generation_id, resource_id, capability);

CREATE TABLE access.authorization_data_policy (
    id text NOT NULL CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 255),
    project_id text NOT NULL,
    environment text NOT NULL,
    generation_id text NOT NULL,
    resource_id text NOT NULL CHECK (resource_id = btrim(resource_id) AND length(resource_id) BETWEEN 1 AND 255),
    resource_kind text NOT NULL CHECK (resource_kind = btrim(resource_kind) AND length(resource_kind) BETWEEN 1 AND 128),
    subject_kind text CHECK (subject_kind IS NULL OR subject_kind IN ('principal','group')),
    subject_id text,
    policy_type text NOT NULL CHECK (policy_type IN ('row_filter','column_mask')),
    expression jsonb NOT NULL CHECK (jsonb_typeof(expression)='object' AND octet_length(expression::text)<=32768),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz,
    PRIMARY KEY (project_id, environment, generation_id, id),
    CHECK ((subject_kind IS NULL AND subject_id IS NULL) OR (subject_kind IS NOT NULL AND subject_id IS NOT NULL)),
    FOREIGN KEY (project_id, environment, generation_id) REFERENCES access.authorization_snapshot(project_id, environment, generation_id)
);
CREATE INDEX authorization_data_policy_resource_idx ON access.authorization_data_policy(project_id, environment, generation_id, resource_id);

CREATE TABLE access.authorization_revocation (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id text NOT NULL,
    environment text NOT NULL,
    generation_id text,
    subject_kind text CHECK (subject_kind IS NULL OR subject_kind IN ('principal','group')),
    subject_id text,
    resource_id text,
    capability text,
    reason text NOT NULL DEFAULT '' CHECK (length(reason)<=1024),
    revoked_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata)='object' AND octet_length(metadata::text)<=8192)
);

CREATE TABLE access.principal_preferences (
    preference_id uuid PRIMARY KEY DEFAULT uuidv7(),
    principal_id uuid NOT NULL REFERENCES access.principal(id),
    theme text NOT NULL DEFAULT 'system' CHECK (theme IN ('system','light','dark','dark_dimmed','light_colorblind','dark_colorblind','light_tritanopia','dark_tritanopia')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz
);
CREATE UNIQUE INDEX principal_preferences_active_key ON access.principal_preferences(principal_id) WHERE revoked_at IS NULL;

CREATE TABLE access.avatar_object (
    sha256 text PRIMARY KEY CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    object_key text NOT NULL CHECK (object_key = btrim(object_key) AND length(object_key) BETWEEN 1 AND 2048),
    media_type text NOT NULL CHECK (media_type='image/png'),
    size_bytes bigint NOT NULL CHECK (size_bytes > 0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE access.principal_avatar (
    avatar_id uuid PRIMARY KEY DEFAULT uuidv7(),
    principal_id uuid NOT NULL REFERENCES access.principal(id),
    sha256 text NOT NULL REFERENCES access.avatar_object(sha256),
    media_type text NOT NULL CHECK (media_type='image/png'),
    size_bytes bigint NOT NULL CHECK (size_bytes > 0),
    width integer NOT NULL CHECK (width=256),
    height integer NOT NULL CHECK (height=256),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz
);
CREATE UNIQUE INDEX principal_avatar_active_key ON access.principal_avatar(principal_id) WHERE revoked_at IS NULL;
CREATE TRIGGER avatar_object_no_delete BEFORE DELETE ON access.avatar_object FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER avatar_object_immutable BEFORE UPDATE ON access.avatar_object FOR EACH ROW EXECUTE FUNCTION access.reject_object_identity_rewrite();
CREATE TRIGGER principal_avatar_immutable BEFORE UPDATE ON access.principal_avatar FOR EACH ROW EXECUTE FUNCTION access.reject_avatar_identity_rewrite();
CREATE TRIGGER principal_preferences_immutable BEFORE UPDATE ON access.principal_preferences FOR EACH ROW EXECUTE FUNCTION access.reject_preference_identity_rewrite();

-- Desktop authorization codes are short-lived bearer artifacts.  The hash
-- is the identity; consumption is a monotonic tombstone and therefore safe
-- under competing redemption transactions.
CREATE TABLE access.desktop_authorization_code (
    code_hash bytea PRIMARY KEY CHECK (octet_length(code_hash)=32),
    principal_id uuid NOT NULL REFERENCES access.principal(id),
    client_id text NOT NULL CHECK (client_id='leapview-desktop'),
    instance_id text NOT NULL CHECK (length(instance_id) BETWEEN 1 AND 128),
    profile_id text NOT NULL CHECK (profile_id = btrim(profile_id) AND length(profile_id) BETWEEN 1 AND 128),
    redirect_uri text NOT NULL CHECK (length(redirect_uri)<=2048),
    code_challenge text NOT NULL CHECK (length(code_challenge) BETWEEN 43 AND 128),
    return_path text NOT NULL CHECK (length(return_path)<=2048),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (expires_at > created_at AND expires_at <= created_at + interval '10 minutes')
);
CREATE INDEX desktop_authorization_code_expiry_idx ON access.desktop_authorization_code(expires_at);

-- First-party CLI/device authorization is separate from desktop browser
-- codes because it carries an authoring scope and refresh-token family.
CREATE TABLE access.device_authorization (
    id text PRIMARY KEY CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 128),
    client_id text NOT NULL CHECK (client_id='leapview-cli'),
    device_code_hash text NOT NULL UNIQUE CHECK (device_code_hash ~ '^[0-9a-f]{64}$'),
    user_code_hash text NOT NULL UNIQUE CHECK (user_code_hash ~ '^[0-9a-f]{64}$'),
    target_id text NOT NULL CHECK (target_id = btrim(target_id) AND length(target_id)<=255),
    project_id text NOT NULL CHECK (project_id = btrim(project_id) AND length(project_id)<=255),
    capabilities jsonb NOT NULL CHECK (access.valid_capabilities(capabilities) AND jsonb_typeof(capabilities)='array' AND octet_length(capabilities::text)<=2048),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','denied','consumed')),
    principal_id uuid REFERENCES access.principal(id),
    expires_at timestamptz NOT NULL,
    poll_interval_seconds integer NOT NULL CHECK (poll_interval_seconds > 0),
    last_polled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    approved_at timestamptz,
    denied_at timestamptz,
    consumed_at timestamptz,
    CHECK (expires_at > created_at AND expires_at <= created_at + interval '24 hours'),
    CHECK ((status='pending' AND principal_id IS NULL AND approved_at IS NULL AND denied_at IS NULL AND consumed_at IS NULL)
        OR (status='approved' AND principal_id IS NOT NULL AND approved_at IS NOT NULL AND denied_at IS NULL AND consumed_at IS NULL)
        OR (status='denied' AND principal_id IS NOT NULL AND denied_at IS NOT NULL AND consumed_at IS NULL)
        OR (status='consumed' AND principal_id IS NOT NULL AND approved_at IS NOT NULL AND consumed_at IS NOT NULL))
);
CREATE INDEX device_authorization_expiry_idx ON access.device_authorization(expires_at);
CREATE TABLE access.authoring_session (
    id text PRIMARY KEY CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 128),
    kind text NOT NULL CHECK (kind IN ('human_cli','workload')),
    client_id text NOT NULL CHECK (length(client_id)<=255),
    principal_id uuid NOT NULL REFERENCES access.principal(id),
    target_id text NOT NULL CHECK (length(target_id)<=255),
    project_id text NOT NULL CHECK (length(project_id)<=255),
    capabilities jsonb NOT NULL CHECK (access.valid_capabilities(capabilities) AND jsonb_typeof(capabilities)='array' AND octet_length(capabilities::text)<=2048),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    last_used_at timestamptz,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    CHECK (expires_at > created_at AND expires_at <= created_at + interval '365 days')
);
CREATE INDEX authoring_session_principal_idx ON access.authoring_session(principal_id, created_at DESC);
CREATE TABLE access.authoring_credential (
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
    CHECK ((refresh_token_hash IS NULL AND refresh_expires_at IS NULL) OR (refresh_token_hash IS NOT NULL AND refresh_expires_at IS NOT NULL AND refresh_expires_at > access_expires_at))
);
CREATE UNIQUE INDEX authoring_credential_active_session_idx ON access.authoring_credential(session_id) WHERE active;
CREATE INDEX authoring_credential_access_expiry_idx ON access.authoring_credential(access_expires_at);
CREATE INDEX authoring_credential_refresh_expiry_idx ON access.authoring_credential(refresh_expires_at) WHERE refresh_expires_at IS NOT NULL;

-- MCP OAuth state is owned by the access capability.  It is deliberately
-- separate from the browser/session credential tables: fosite request state
-- is opaque JSON, while client identity and token signatures remain typed and
-- uniquely indexed.  The runtime role may mutate these rows; no SQLite
-- compatibility projection exists on the PostgreSQL path.
CREATE TABLE access.oauth_client (
    id text PRIMARY KEY CHECK (id = btrim(id) AND length(id) BETWEEN 1 AND 255),
    name text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 255),
    redirect_uris jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(redirect_uris)='array' AND octet_length(redirect_uris::text)<=16384),
    grant_types jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(grant_types)='array' AND octet_length(grant_types::text)<=4096),
    response_types jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(response_types)='array' AND octet_length(response_types::text)<=4096),
    scopes jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(scopes)='array' AND octet_length(scopes::text)<=4096),
    audience jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(audience)='array' AND octet_length(audience::text)<=4096),
    public_client boolean NOT NULL DEFAULT false,
    secret_hash bytea,
    token_endpoint_auth_method text NOT NULL DEFAULT 'none' CHECK (token_endpoint_auth_method = btrim(token_endpoint_auth_method) AND length(token_endpoint_auth_method)<=64),
    principal_id uuid REFERENCES access.principal(id),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE access.oauth_session (
    kind text NOT NULL CHECK (kind IN ('authorize_code','access_token','refresh_token','pkce')),
    signature text NOT NULL CHECK (signature = btrim(signature) AND length(signature) BETWEEN 1 AND 512),
    request_id text NOT NULL CHECK (request_id = btrim(request_id) AND length(request_id) BETWEEN 1 AND 512),
    request_json jsonb NOT NULL CHECK (jsonb_typeof(request_json)='object' AND octet_length(request_json::text)<=131072),
    access_signature text NOT NULL DEFAULT '' CHECK (access_signature = btrim(access_signature) AND length(access_signature)<=512),
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (kind, signature)
);
CREATE INDEX oauth_session_request_idx ON access.oauth_session(kind, request_id);
CREATE TABLE access.oauth_client_assertion (
    jti text PRIMARY KEY CHECK (jti = btrim(jti) AND length(jti) BETWEEN 1 AND 512),
    expires_at timestamptz NOT NULL
);
CREATE INDEX oauth_client_assertion_expiry_idx ON access.oauth_client_assertion(expires_at);

CREATE OR REPLACE FUNCTION access.reject_authorization_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME = 'authorization_snapshot' THEN
        IF OLD.project_id<>NEW.project_id OR OLD.environment<>NEW.environment OR OLD.generation_id<>NEW.generation_id OR OLD.digest<>NEW.digest OR OLD.created_at<>NEW.created_at THEN RAISE EXCEPTION 'authorization snapshot identity is immutable'; END IF;
    ELSIF TG_TABLE_NAME = 'authorization_role_binding' THEN
        IF OLD.id<>NEW.id OR OLD.project_id<>NEW.project_id OR OLD.environment<>NEW.environment OR OLD.generation_id<>NEW.generation_id OR OLD.subject_kind<>NEW.subject_kind OR OLD.subject_id<>NEW.subject_id OR OLD.role<>NEW.role OR OLD.capabilities IS DISTINCT FROM NEW.capabilities OR OLD.name<>NEW.name OR OLD.created_at<>NEW.created_at THEN RAISE EXCEPTION 'authorization role binding identity is immutable'; END IF;
    ELSIF TG_TABLE_NAME = 'authorization_grant' THEN
        IF OLD.id<>NEW.id OR OLD.project_id<>NEW.project_id OR OLD.environment<>NEW.environment OR OLD.generation_id<>NEW.generation_id OR OLD.subject_kind<>NEW.subject_kind OR OLD.subject_id<>NEW.subject_id OR OLD.resource_id<>NEW.resource_id OR OLD.resource_kind<>NEW.resource_kind OR OLD.capability<>NEW.capability OR OLD.name<>NEW.name OR OLD.created_at<>NEW.created_at THEN RAISE EXCEPTION 'authorization grant identity is immutable'; END IF;
    ELSIF TG_TABLE_NAME = 'authorization_data_policy' THEN
        IF OLD.id<>NEW.id OR OLD.project_id<>NEW.project_id OR OLD.environment<>NEW.environment OR OLD.generation_id<>NEW.generation_id OR OLD.resource_id<>NEW.resource_id OR OLD.resource_kind<>NEW.resource_kind OR OLD.subject_kind IS DISTINCT FROM NEW.subject_kind OR OLD.subject_id IS DISTINCT FROM NEW.subject_id OR OLD.policy_type<>NEW.policy_type OR OLD.expression IS DISTINCT FROM NEW.expression OR OLD.created_at<>NEW.created_at THEN RAISE EXCEPTION 'authorization policy identity is immutable'; END IF;
    END IF;
    RETURN NEW;
END; $$;
CREATE TRIGGER authorization_snapshot_no_delete BEFORE DELETE ON access.authorization_snapshot FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER authorization_snapshot_immutable BEFORE UPDATE ON access.authorization_snapshot FOR EACH ROW EXECUTE FUNCTION access.reject_authorization_identity_rewrite();
CREATE TRIGGER authorization_role_binding_no_delete BEFORE DELETE ON access.authorization_role_binding FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER authorization_role_binding_immutable BEFORE UPDATE ON access.authorization_role_binding FOR EACH ROW EXECUTE FUNCTION access.reject_authorization_identity_rewrite();
CREATE TRIGGER authorization_role_binding_revocation_monotonic BEFORE UPDATE ON access.authorization_role_binding FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER authorization_grant_no_delete BEFORE DELETE ON access.authorization_grant FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER authorization_grant_immutable BEFORE UPDATE ON access.authorization_grant FOR EACH ROW EXECUTE FUNCTION access.reject_authorization_identity_rewrite();
CREATE TRIGGER authorization_grant_revocation_monotonic BEFORE UPDATE ON access.authorization_grant FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER authorization_data_policy_no_delete BEFORE DELETE ON access.authorization_data_policy FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER authorization_data_policy_immutable BEFORE UPDATE ON access.authorization_data_policy FOR EACH ROW EXECUTE FUNCTION access.reject_authorization_identity_rewrite();
CREATE TRIGGER authorization_data_policy_revocation_monotonic BEFORE UPDATE ON access.authorization_data_policy FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER authorization_revocation_append_only BEFORE UPDATE OR DELETE ON access.authorization_revocation FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER principal_preferences_no_delete BEFORE DELETE ON access.principal_preferences FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER principal_preferences_revocation_monotonic BEFORE UPDATE ON access.principal_preferences FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER principal_avatar_no_delete BEFORE DELETE ON access.principal_avatar FOR EACH ROW EXECUTE FUNCTION access.reject_access_delete();
CREATE TRIGGER principal_avatar_revocation_monotonic BEFORE UPDATE ON access.principal_avatar FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER desktop_authorization_code_no_delete BEFORE DELETE ON access.desktop_authorization_code FOR EACH ROW EXECUTE FUNCTION access.allow_maintenance_delete();
CREATE TRIGGER device_authorization_no_delete BEFORE DELETE ON access.device_authorization FOR EACH ROW EXECUTE FUNCTION access.allow_maintenance_delete();
CREATE TRIGGER authoring_session_no_delete BEFORE DELETE ON access.authoring_session FOR EACH ROW EXECUTE FUNCTION access.allow_maintenance_delete();
CREATE OR REPLACE FUNCTION access.reject_authoring_identity_rewrite() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME='desktop_authorization_code' THEN
        IF OLD.code_hash<>NEW.code_hash OR OLD.principal_id<>NEW.principal_id OR OLD.client_id<>NEW.client_id OR OLD.instance_id<>NEW.instance_id OR OLD.profile_id<>NEW.profile_id OR OLD.redirect_uri<>NEW.redirect_uri OR OLD.code_challenge<>NEW.code_challenge OR OLD.return_path<>NEW.return_path OR OLD.expires_at<>NEW.expires_at OR OLD.created_at<>NEW.created_at THEN RAISE EXCEPTION 'desktop authorization identity is immutable'; END IF;
    ELSIF TG_TABLE_NAME='authoring_session' THEN
        IF OLD.id<>NEW.id OR OLD.kind<>NEW.kind OR OLD.client_id<>NEW.client_id OR OLD.principal_id<>NEW.principal_id OR OLD.target_id<>NEW.target_id OR OLD.project_id<>NEW.project_id OR OLD.capabilities IS DISTINCT FROM NEW.capabilities OR OLD.created_at<>NEW.created_at THEN RAISE EXCEPTION 'authoring session identity is immutable'; END IF;
    ELSIF TG_TABLE_NAME='authoring_credential' THEN
        IF OLD.id<>NEW.id OR OLD.session_id<>NEW.session_id OR OLD.access_token_hash<>NEW.access_token_hash OR OLD.refresh_token_hash IS DISTINCT FROM NEW.refresh_token_hash OR OLD.access_expires_at<>NEW.access_expires_at OR OLD.refresh_expires_at IS DISTINCT FROM NEW.refresh_expires_at OR OLD.created_at<>NEW.created_at THEN RAISE EXCEPTION 'authoring credential identity is immutable'; END IF;
    END IF;
    RETURN NEW;
END; $$;
CREATE TRIGGER desktop_authorization_code_immutable BEFORE UPDATE ON access.desktop_authorization_code FOR EACH ROW EXECUTE FUNCTION access.reject_authoring_identity_rewrite();
CREATE TRIGGER desktop_authorization_code_consumption_monotonic BEFORE UPDATE ON access.desktop_authorization_code FOR EACH ROW EXECUTE FUNCTION access.reject_consumption_rewind();
CREATE TRIGGER device_authorization_immutable BEFORE UPDATE ON access.device_authorization FOR EACH ROW EXECUTE FUNCTION access.reject_device_authorization_rewrite();
CREATE TRIGGER authoring_session_immutable BEFORE UPDATE ON access.authoring_session FOR EACH ROW EXECUTE FUNCTION access.reject_authoring_identity_rewrite();
CREATE TRIGGER authoring_session_revocation_monotonic BEFORE UPDATE ON access.authoring_session FOR EACH ROW EXECUTE FUNCTION access.reject_revocation_clear();
CREATE TRIGGER authoring_credential_no_delete BEFORE DELETE ON access.authoring_credential FOR EACH ROW EXECUTE FUNCTION access.allow_maintenance_delete();
CREATE TRIGGER authoring_credential_immutable BEFORE UPDATE ON access.authoring_credential FOR EACH ROW EXECUTE FUNCTION access.reject_authoring_identity_rewrite();
CREATE TRIGGER authoring_credential_transition BEFORE UPDATE ON access.authoring_credential FOR EACH ROW EXECUTE FUNCTION access.reject_authoring_credential_transition();

DO $$
BEGIN
    REVOKE ALL ON SCHEMA access FROM PUBLIC;
    REVOKE ALL ON ALL TABLES IN SCHEMA access FROM PUBLIC;
    REVOKE ALL ON ALL FUNCTIONS IN SCHEMA access FROM PUBLIC;
    REVOKE ALL ON SCHEMA audit FROM PUBLIC;
    REVOKE ALL ON TABLE audit.audit_event, audit.audit_retention_floor FROM PUBLIC;
    REVOKE ALL ON FUNCTION audit.reject_audit_mutation() FROM PUBLIC;
    REVOKE ALL ON FUNCTION audit.prune_audit_events(text, timestamptz, integer) FROM PUBLIC;
    REVOKE ALL ON FUNCTION access.prune_auth_state(timestamptz, integer) FROM PUBLIC;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_runtime') THEN
        EXECUTE 'GRANT USAGE ON SCHEMA access TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA access TO leapview_control_runtime';
        EXECUTE 'GRANT DELETE ON access.oauth_session, access.oauth_client_assertion TO leapview_control_runtime';
        EXECUTE 'GRANT EXECUTE ON FUNCTION access.valid_capabilities(jsonb) TO leapview_control_runtime';
        EXECUTE 'GRANT USAGE ON SCHEMA audit TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT, INSERT ON audit.audit_event TO leapview_control_runtime';
        EXECUTE 'REVOKE DELETE ON audit.audit_event, audit.audit_retention_floor FROM leapview_control_runtime';
        EXECUTE 'REVOKE EXECUTE ON FUNCTION audit.prune_audit_events(text, timestamptz, integer) FROM leapview_control_runtime';
        EXECUTE 'REVOKE EXECUTE ON FUNCTION access.prune_auth_state(timestamptz, integer) FROM leapview_control_runtime';
        EXECUTE 'REVOKE DELETE ON access.session, access.api_token, access.service_principal_secret, access.desktop_authorization_code, access.device_authorization, access.authoring_session, access.authoring_credential FROM leapview_control_runtime';
        EXECUTE 'REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON access.access_retention_floor FROM leapview_control_runtime';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_maintenance') THEN
        EXECUTE 'GRANT USAGE ON SCHEMA access, audit TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION audit.prune_audit_events(text, timestamptz, integer) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION access.prune_auth_state(timestamptz, integer) TO leapview_control_maintenance';
        EXECUTE 'REVOKE ALL ON audit.audit_event, audit.audit_retention_floor FROM leapview_control_maintenance';
        EXECUTE 'REVOKE ALL ON access.access_retention_floor FROM leapview_control_maintenance';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_readonly') THEN
        EXECUTE 'GRANT USAGE ON SCHEMA access TO leapview_control_readonly';
        EXECUTE 'GRANT SELECT ON ALL TABLES IN SCHEMA access TO leapview_control_readonly';
        EXECUTE 'REVOKE SELECT ON access.session, access.local_credential, access.api_token, access.service_principal_secret, access.desktop_authorization_code, access.device_authorization, access.authoring_credential, access.oauth_client, access.oauth_session, access.oauth_client_assertion FROM leapview_control_readonly';
        EXECUTE 'GRANT USAGE ON SCHEMA audit TO leapview_control_readonly';
        EXECUTE 'GRANT SELECT ON audit.audit_event, audit.audit_retention_floor TO leapview_control_readonly';
        EXECUTE 'REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON access.access_retention_floor FROM leapview_control_readonly';
    END IF;
END $$;
