-- PostgreSQL connection-binding capability schema (ADR-0020).
--
-- This schema stores target-scoped, non-secret connection state. Credential
-- values are resolved by the credential authority at runtime and are never
-- persisted here.
CREATE SCHEMA IF NOT EXISTS connection_binding;

CREATE OR REPLACE FUNCTION connection_binding.endpoint_is_valid(value jsonb)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE
STRICT
SET search_path = pg_catalog
AS $$
DECLARE
    key text;
    item jsonb;
    option_key text;
    option_value jsonb;
BEGIN
    IF jsonb_typeof(value) <> 'object'
       OR octet_length(value::text) NOT BETWEEN 2 AND 16384
       OR value - 'host' - 'port' - 'database' - 'objectScope' - 'sourceIdentity' - 'tlsMode' - 'options' <> '{}'::jsonb THEN
        RETURN false;
    END IF;
    FOR key, item IN SELECT * FROM jsonb_each(value) LOOP
        IF key IN ('host', 'database', 'objectScope', 'sourceIdentity', 'tlsMode') THEN
            IF jsonb_typeof(item) <> 'string' OR item #>> '{}' <> btrim(item #>> '{}') THEN
                RETURN false;
            END IF;
        ELSIF key = 'port' THEN
            IF jsonb_typeof(item) <> 'number' OR item #>> '{}' !~ '^[0-9]+$'
               OR (item #>> '{}')::numeric > 65535 THEN
                RETURN false;
            END IF;
        ELSIF key = 'options' THEN
            IF jsonb_typeof(item) <> 'object' THEN
                RETURN false;
            END IF;
            FOR option_key, option_value IN SELECT * FROM jsonb_each(item) LOOP
                IF option_key !~ '^[A-Za-z_][A-Za-z0-9_.-]{0,127}$'
                   OR jsonb_typeof(option_value) <> 'string'
                   OR lower(option_key) ~ '(password|secret|token|credential|private_key|access_key)' THEN
                    RETURN false;
                END IF;
            END LOOP;
        ELSE
            RETURN false;
        END IF;
    END LOOP;
    RETURN true;
END;
$$;

CREATE TABLE IF NOT EXISTS connection_binding.target_connection_binding (
    id                       text PRIMARY KEY,
    target_id                text NOT NULL,
    connection_id            text NOT NULL,
    connector_kind           text NOT NULL,
    authentication_mode      text NOT NULL,
    project_id               text NOT NULL,
    environment              text NOT NULL,
    endpoint_json            jsonb NOT NULL,
    credential_project_id    text NOT NULL DEFAULT '',
    credential_environment   text NOT NULL DEFAULT '',
    credential_secret_path   text NOT NULL DEFAULT '',
    credential_secret_key    text NOT NULL DEFAULT '',
    enabled                  boolean NOT NULL,
    validated_version        text NOT NULL DEFAULT '',
    health                   text NOT NULL,
    health_reason            text NOT NULL DEFAULT '',
    last_validated_at        timestamptz,
    created_at               timestamptz NOT NULL,
    updated_at               timestamptz NOT NULL,
    revision                 bigint NOT NULL,
    CHECK (id = btrim(id) AND id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]{0,159}$'),
    CHECK (target_id = btrim(target_id) AND target_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]{0,159}$'),
    CHECK (connection_id = btrim(connection_id) AND octet_length(connection_id) BETWEEN 1 AND 255
        AND connection_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (connector_kind ~ '^[A-Za-z_][A-Za-z0-9_.:-]{0,127}$'
        AND connector_kind IN ('managed', 's3', 'r2', 'gcs', 'http', 'azure_blob', 'postgres', 'mysql', 'sqlite', 'ducklake', 'quack')),
    CHECK (authentication_mode IN ('none', 'external_bundle', 'workload_identity')),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255
        AND project_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (environment ~ '^[A-Za-z_][A-Za-z0-9_.:-]{0,127}$'),
    CHECK (connection_binding.endpoint_is_valid(endpoint_json)),
    CHECK (credential_project_id = btrim(credential_project_id) AND octet_length(credential_project_id) <= 255),
    CHECK (credential_environment = btrim(credential_environment) AND octet_length(credential_environment) <= 255),
    CHECK (credential_secret_path = btrim(credential_secret_path) AND octet_length(credential_secret_path) <= 1024),
    CHECK (credential_secret_key = btrim(credential_secret_key) AND octet_length(credential_secret_key) <= 255),
    CHECK (octet_length(validated_version) <= 255),
    CHECK (health IN ('pending', 'healthy', 'degraded', 'disabled')),
    CHECK ((health <> 'healthy') OR (btrim(validated_version) <> '' AND last_validated_at IS NOT NULL)),
    CHECK ((health <> 'degraded') OR health_reason ~ '^[A-Z0-9_]{1,64}$'),
    CHECK ((health = 'degraded') OR health_reason = ''),
    CHECK (octet_length(health_reason) <= 255),
    CHECK (revision > 0),
    CHECK (updated_at >= created_at),
    CHECK (last_validated_at IS NULL OR (last_validated_at >= created_at AND last_validated_at <= updated_at)),
    CHECK ((enabled AND health <> 'disabled') OR (NOT enabled AND health = 'disabled')),
    CHECK ((authentication_mode = 'external_bundle'
            AND credential_project_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'
            AND credential_environment <> ''
            AND credential_secret_path LIKE '/%'
            AND credential_secret_key <> '')
        OR (authentication_mode <> 'external_bundle'
            AND credential_project_id = '' AND credential_environment = ''
            AND credential_secret_path = '' AND credential_secret_key = ''))
);

CREATE UNIQUE INDEX IF NOT EXISTS target_connection_binding_scope_idx
    ON connection_binding.target_connection_binding (target_id, project_id, environment, connection_id);
CREATE INDEX IF NOT EXISTS target_connection_binding_health_idx
    ON connection_binding.target_connection_binding (target_id, environment, health, updated_at DESC);

-- The capability owns mutable revisions, but no caller can delete history or
-- replace identity columns. A stale Save is rejected by its optimistic
-- predicate in the generated query below.
CREATE OR REPLACE FUNCTION connection_binding.reject_identity_change()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, connection_binding
AS $$
BEGIN
    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.target_id IS DISTINCT FROM OLD.target_id
       OR NEW.connection_id IS DISTINCT FROM OLD.connection_id
       OR NEW.project_id IS DISTINCT FROM OLD.project_id
       OR NEW.environment IS DISTINCT FROM OLD.environment
       OR NEW.created_at IS DISTINCT FROM OLD.created_at
       OR NEW.revision <> OLD.revision + 1 THEN
        RAISE EXCEPTION 'connection binding identity or revision is immutable';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS target_connection_binding_identity_guard ON connection_binding.target_connection_binding;
CREATE TRIGGER target_connection_binding_identity_guard
    BEFORE UPDATE ON connection_binding.target_connection_binding
    FOR EACH ROW EXECUTE FUNCTION connection_binding.reject_identity_change();

-- No delete operation is part of the domain repository. Keep this invariant
-- true even for owner-level maintenance sessions.
CREATE OR REPLACE FUNCTION connection_binding.reject_delete()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, connection_binding
AS $$
BEGIN
    RAISE EXCEPTION 'connection binding history is not deletable';
END;
$$;
DROP TRIGGER IF EXISTS target_connection_binding_no_delete ON connection_binding.target_connection_binding;
CREATE TRIGGER target_connection_binding_no_delete
    BEFORE DELETE ON connection_binding.target_connection_binding
    FOR EACH ROW EXECUTE FUNCTION connection_binding.reject_delete();

REVOKE ALL ON SCHEMA connection_binding FROM PUBLIC;
REVOKE ALL ON TABLE connection_binding.target_connection_binding FROM PUBLIC;
REVOKE ALL ON FUNCTION connection_binding.endpoint_is_valid(jsonb) FROM PUBLIC;
REVOKE ALL ON FUNCTION connection_binding.reject_identity_change() FROM PUBLIC;
REVOKE ALL ON FUNCTION connection_binding.reject_delete() FROM PUBLIC;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_owner') THEN
        GRANT ALL ON SCHEMA connection_binding TO leapview_control_owner;
        GRANT ALL ON connection_binding.target_connection_binding TO leapview_control_owner;
        GRANT EXECUTE ON FUNCTION connection_binding.endpoint_is_valid(jsonb) TO leapview_control_owner;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_migrator') THEN
        GRANT USAGE ON SCHEMA connection_binding TO leapview_control_migrator;
        GRANT ALL ON connection_binding.target_connection_binding TO leapview_control_migrator;
        GRANT EXECUTE ON FUNCTION connection_binding.endpoint_is_valid(jsonb) TO leapview_control_migrator;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA connection_binding TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON connection_binding.target_connection_binding TO leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION connection_binding.endpoint_is_valid(jsonb) TO leapview_control_runtime;
        REVOKE DELETE, TRUNCATE, REFERENCES, TRIGGER ON connection_binding.target_connection_binding FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA connection_binding TO leapview_control_readonly;
        GRANT SELECT ON connection_binding.target_connection_binding TO leapview_control_readonly;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON connection_binding.target_connection_binding FROM leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA connection_binding TO leapview_control_backup;
        GRANT SELECT ON connection_binding.target_connection_binding TO leapview_control_backup;
    END IF;
END
$$;

-- A profile application is an exact local checkout/runtime/target admission
-- checkpoint. Required graph intent and applied binding evidence are kept in
-- separate child tables so an applying record can be durable before any
-- provider observation exists. No credential bundle or raw credential hash is
-- represented by this schema.
CREATE TABLE IF NOT EXISTS connection_binding.profile_application (
    id             text PRIMARY KEY,
    checkout_id    text NOT NULL,
    runtime_id     text NOT NULL,
    target_id      text NOT NULL,
    project_id     text NOT NULL,
    environment    text NOT NULL,
    profile_name   text NOT NULL,
    graph_digest   text NOT NULL,
    source_digest  text NOT NULL,
    profile_digest text NOT NULL,
    last_completed_application_id text NOT NULL DEFAULT '',
    last_completed_at timestamptz,
    retired_connections jsonb NOT NULL DEFAULT '[]'::jsonb,
    status         text NOT NULL,
    revision       bigint NOT NULL,
    created_at     timestamptz NOT NULL,
    updated_at     timestamptz NOT NULL,
    CHECK (id = btrim(id) AND octet_length(id) BETWEEN 1 AND 256 AND id !~ '[[:space:][:cntrl:]]'),
    CHECK (checkout_id = btrim(checkout_id) AND octet_length(checkout_id) BETWEEN 1 AND 256 AND checkout_id !~ '[[:space:][:cntrl:]]'),
    CHECK (runtime_id = btrim(runtime_id) AND octet_length(runtime_id) BETWEEN 1 AND 256 AND runtime_id !~ '[[:space:][:cntrl:]]'),
    CHECK (target_id = btrim(target_id) AND target_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]{0,159}$'),
    CHECK (project_id = btrim(project_id) AND project_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (environment = btrim(environment) AND environment ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (profile_name = btrim(profile_name) AND octet_length(profile_name) BETWEEN 1 AND 256 AND profile_name !~ '[[:space:][:cntrl:]]'),
    CHECK (graph_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (source_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (profile_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK ((last_completed_application_id = '' AND last_completed_at IS NULL) OR
           (last_completed_application_id = btrim(last_completed_application_id) AND
            octet_length(last_completed_application_id) BETWEEN 1 AND 256 AND
            last_completed_application_id !~ '[[:space:][:cntrl:]]' AND last_completed_at IS NOT NULL)),
    CHECK (jsonb_typeof(retired_connections) = 'array' AND octet_length(retired_connections::text) <= 65536),
    CHECK (status IN ('applying', 'incomplete', 'applied')),
    CHECK (revision > 0),
    CHECK (updated_at >= created_at),
    UNIQUE (checkout_id, runtime_id, target_id, project_id, environment)
);

CREATE TABLE IF NOT EXISTS connection_binding.profile_application_required_connection (
    application_id text NOT NULL REFERENCES connection_binding.profile_application(id) ON DELETE CASCADE,
    connection_id  text NOT NULL,
    connector_kind text NOT NULL,
    PRIMARY KEY (application_id, connection_id),
    CHECK (connection_id = btrim(connection_id) AND connection_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (connector_kind = btrim(connector_kind) AND octet_length(connector_kind) BETWEEN 1 AND 256 AND connector_kind !~ '[[:space:][:cntrl:]]')
);

CREATE TABLE IF NOT EXISTS connection_binding.profile_application_expected_connection (
    application_id   text NOT NULL REFERENCES connection_binding.profile_application(id) ON DELETE CASCADE,
    binding_id       text NOT NULL,
    connection_id    text NOT NULL,
    connector_kind   text NOT NULL,
    authentication_mode text NOT NULL,
    endpoint_json    jsonb NOT NULL,
    credential_project_id text NOT NULL DEFAULT '',
    credential_environment text NOT NULL DEFAULT '',
    credential_secret_path text NOT NULL DEFAULT '',
    credential_secret_key text NOT NULL DEFAULT '',
    binding_revision bigint NOT NULL,
    provider_version text NOT NULL,
    PRIMARY KEY (application_id, connection_id),
    CHECK (binding_id = btrim(binding_id) AND octet_length(binding_id) BETWEEN 1 AND 256 AND binding_id !~ '[[:space:][:cntrl:]]'),
    CHECK (connection_id = btrim(connection_id) AND connection_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (connector_kind = btrim(connector_kind) AND octet_length(connector_kind) BETWEEN 1 AND 256 AND connector_kind !~ '[[:space:][:cntrl:]]'),
    CHECK (authentication_mode IN ('none', 'external_bundle')),
    CHECK (connection_binding.endpoint_is_valid(endpoint_json)),
    CHECK (credential_project_id = btrim(credential_project_id) AND octet_length(credential_project_id) <= 255),
    CHECK (credential_environment = btrim(credential_environment) AND octet_length(credential_environment) <= 255),
    CHECK (credential_secret_path = btrim(credential_secret_path) AND octet_length(credential_secret_path) <= 1024),
    CHECK (credential_secret_key = btrim(credential_secret_key) AND octet_length(credential_secret_key) <= 255),
    CHECK ((authentication_mode = 'external_bundle' AND credential_project_id <> '' AND credential_environment <> '' AND credential_secret_path LIKE '/%' AND credential_secret_key <> '') OR (authentication_mode = 'none' AND credential_project_id = '' AND credential_environment = '' AND credential_secret_path = '' AND credential_secret_key = '')),
    CHECK (binding_revision >= 0),
    CHECK (provider_version = btrim(provider_version) AND octet_length(provider_version) BETWEEN 1 AND 256 AND provider_version !~ '[[:space:][:cntrl:]]')
);

CREATE TABLE IF NOT EXISTS connection_binding.profile_application_applied_connection (
    application_id   text NOT NULL REFERENCES connection_binding.profile_application(id) ON DELETE CASCADE,
    binding_id       text NOT NULL,
    connection_id    text NOT NULL,
    connector_kind   text NOT NULL,
    authentication_mode text NOT NULL,
    endpoint_json    jsonb NOT NULL,
    credential_project_id text NOT NULL DEFAULT '',
    credential_environment text NOT NULL DEFAULT '',
    credential_secret_path text NOT NULL DEFAULT '',
    credential_secret_key text NOT NULL DEFAULT '',
    binding_revision bigint NOT NULL,
    provider_version text NOT NULL,
    PRIMARY KEY (application_id, connection_id),
    CHECK (binding_id = btrim(binding_id) AND octet_length(binding_id) BETWEEN 1 AND 256 AND binding_id !~ '[[:space:][:cntrl:]]'),
    CHECK (connection_id = btrim(connection_id) AND connection_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (connector_kind = btrim(connector_kind) AND octet_length(connector_kind) BETWEEN 1 AND 256 AND connector_kind !~ '[[:space:][:cntrl:]]'),
    CHECK (authentication_mode IN ('none', 'external_bundle')),
    CHECK (connection_binding.endpoint_is_valid(endpoint_json)),
    CHECK (credential_project_id = btrim(credential_project_id) AND octet_length(credential_project_id) <= 255),
    CHECK (credential_environment = btrim(credential_environment) AND octet_length(credential_environment) <= 255),
    CHECK (credential_secret_path = btrim(credential_secret_path) AND octet_length(credential_secret_path) <= 1024),
    CHECK (credential_secret_key = btrim(credential_secret_key) AND octet_length(credential_secret_key) <= 255),
    CHECK ((authentication_mode = 'external_bundle' AND credential_project_id <> '' AND credential_environment <> '' AND credential_secret_path LIKE '/%' AND credential_secret_key <> '') OR (authentication_mode = 'none' AND credential_project_id = '' AND credential_environment = '' AND credential_secret_path = '' AND credential_secret_key = '')),
    CHECK (binding_revision > 0),
    CHECK (provider_version = btrim(provider_version) AND octet_length(provider_version) BETWEEN 1 AND 256 AND provider_version !~ '[[:space:][:cntrl:]]')
);

CREATE INDEX IF NOT EXISTS profile_application_lookup_idx
    ON connection_binding.profile_application (checkout_id, runtime_id, target_id, project_id, environment);
CREATE INDEX IF NOT EXISTS profile_application_required_order_idx
    ON connection_binding.profile_application_required_connection (application_id, connection_id);
CREATE INDEX IF NOT EXISTS profile_application_expected_order_idx
    ON connection_binding.profile_application_expected_connection (application_id, connection_id);
CREATE INDEX IF NOT EXISTS profile_application_applied_order_idx
    ON connection_binding.profile_application_applied_connection (application_id, connection_id);

-- Applied evidence is not a client progress counter. It may be inserted only
-- when it exactly describes the current healthy binding owned by the existing
-- binding authority in the same target and serving scope.
CREATE OR REPLACE FUNCTION connection_binding.require_current_profile_binding_evidence()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, connection_binding
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
          FROM connection_binding.profile_application AS application
          JOIN connection_binding.profile_application_required_connection AS required
            ON required.application_id = application.id
           AND required.connection_id = NEW.connection_id
           AND required.connector_kind = NEW.connector_kind
          JOIN connection_binding.profile_application_expected_connection AS expected
            ON expected.application_id = application.id
           AND expected.binding_id = NEW.binding_id
           AND expected.connection_id = NEW.connection_id
           AND expected.connector_kind = NEW.connector_kind
           AND expected.authentication_mode = NEW.authentication_mode
           AND expected.endpoint_json = NEW.endpoint_json
           AND expected.credential_project_id = NEW.credential_project_id
           AND expected.credential_environment = NEW.credential_environment
           AND expected.credential_secret_path = NEW.credential_secret_path
           AND expected.credential_secret_key = NEW.credential_secret_key
           AND expected.provider_version = NEW.provider_version
           AND NEW.binding_revision >= GREATEST(1, expected.binding_revision)
          JOIN connection_binding.target_connection_binding AS binding
            ON binding.id = NEW.binding_id
           AND binding.target_id = application.target_id
           AND binding.project_id = application.project_id
           AND binding.environment = application.environment
           AND binding.connection_id = NEW.connection_id
         WHERE application.id = NEW.application_id
           AND application.status IN ('applying', 'incomplete')
           AND binding.enabled
           AND binding.health = 'healthy'
           AND binding.connector_kind = NEW.connector_kind
           AND binding.authentication_mode = NEW.authentication_mode
           AND binding.endpoint_json = NEW.endpoint_json
           AND binding.credential_project_id = NEW.credential_project_id
           AND binding.credential_environment = NEW.credential_environment
           AND binding.credential_secret_path = NEW.credential_secret_path
           AND binding.credential_secret_key = NEW.credential_secret_key
           AND binding.revision = NEW.binding_revision
           AND binding.validated_version = NEW.provider_version
    ) THEN
        RAISE EXCEPTION 'profile application evidence does not match a current healthy binding';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS profile_application_applied_binding_guard ON connection_binding.profile_application_applied_connection;
CREATE TRIGGER profile_application_applied_binding_guard
    BEFORE INSERT OR UPDATE ON connection_binding.profile_application_applied_connection
    FOR EACH ROW EXECUTE FUNCTION connection_binding.require_current_profile_binding_evidence();

-- The parent identity and revision are guarded at the database boundary even
-- when an owner-level session bypasses the Go repository. Required intent is
-- immutable; applied evidence can change only before the parent is applied.
CREATE OR REPLACE FUNCTION connection_binding.reject_profile_application_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, connection_binding
AS $$
DECLARE
    old_application_id text;
    new_application_id text;
BEGIN
    -- Replace is the one bounded path that may supersede an immutable
    -- checkpoint. The marker is transaction-local and is set only after the
    -- repository has locked and CAS-checked the current row.
    IF current_setting('connection_binding.profile_application_replacement', true) = 'on'
       AND current_user = (
           SELECT pg_catalog.pg_get_userbyid(procedure.proowner)
             FROM pg_catalog.pg_proc AS procedure
             JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = procedure.pronamespace
            WHERE namespace.nspname = 'connection_binding'
              AND procedure.proname = 'delete_profile_application_for_replacement'
              AND procedure.pronargs = 8
       ) THEN
        IF TG_OP = 'DELETE' THEN
            RETURN OLD;
        END IF;
        RETURN NEW;
    END IF;
    IF TG_TABLE_NAME = 'profile_application' THEN
        IF NEW.id IS DISTINCT FROM OLD.id
           OR NEW.checkout_id IS DISTINCT FROM OLD.checkout_id
           OR NEW.runtime_id IS DISTINCT FROM OLD.runtime_id
           OR NEW.target_id IS DISTINCT FROM OLD.target_id
           OR NEW.project_id IS DISTINCT FROM OLD.project_id
           OR NEW.environment IS DISTINCT FROM OLD.environment
           OR NEW.profile_name IS DISTINCT FROM OLD.profile_name
           OR NEW.graph_digest IS DISTINCT FROM OLD.graph_digest
           OR NEW.source_digest IS DISTINCT FROM OLD.source_digest
           OR NEW.profile_digest IS DISTINCT FROM OLD.profile_digest
           OR NEW.retired_connections IS DISTINCT FROM OLD.retired_connections
           OR NEW.created_at IS DISTINCT FROM OLD.created_at
           OR NEW.revision <> OLD.revision + 1 THEN
            RAISE EXCEPTION 'profile application identity or revision is immutable';
        END IF;
        IF OLD.status = 'applied' THEN
            RAISE EXCEPTION 'applied profile application is terminal';
        END IF;
        IF OLD.status <> 'applied' AND NEW.status = 'applied' AND NOT (
            current_setting('connection_binding.profile_application_completion', true) = 'on'
            AND current_user = (
                SELECT pg_catalog.pg_get_userbyid(procedure.proowner)
                  FROM pg_catalog.pg_proc AS procedure
                  JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = procedure.pronamespace
                 WHERE namespace.nspname = 'connection_binding'
                   AND procedure.proname = 'complete_profile_application'
                   AND procedure.pronargs = 8
            )
        ) THEN
            RAISE EXCEPTION 'profile application completion requires the bounded completion function';
        END IF;
        IF (NEW.last_completed_application_id IS DISTINCT FROM OLD.last_completed_application_id
            OR NEW.last_completed_at IS DISTINCT FROM OLD.last_completed_at) AND NOT (
            current_setting('connection_binding.profile_application_completion', true) = 'on'
            AND current_user = (
                SELECT pg_catalog.pg_get_userbyid(procedure.proowner)
                  FROM pg_catalog.pg_proc AS procedure
                  JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = procedure.pronamespace
                 WHERE namespace.nspname = 'connection_binding'
                   AND procedure.proname = 'complete_profile_application'
                   AND procedure.pronargs = 8
            )
        ) THEN
            RAISE EXCEPTION 'last completed profile application metadata is owner-managed';
        END IF;
        IF OLD.status = 'applying' AND NEW.status NOT IN ('applying', 'incomplete', 'applied') THEN
            RAISE EXCEPTION 'invalid profile application status transition';
        ELSIF OLD.status = 'incomplete' AND NEW.status NOT IN ('applying', 'incomplete', 'applied') THEN
            RAISE EXCEPTION 'invalid profile application status transition';
        END IF;
        IF NEW.status = 'applied' AND (
            (SELECT count(*) FROM connection_binding.profile_application_required_connection WHERE application_id = NEW.id)
            <> (SELECT count(*) FROM connection_binding.profile_application_expected_connection WHERE application_id = NEW.id)
            OR (SELECT count(*) FROM connection_binding.profile_application_expected_connection WHERE application_id = NEW.id)
            <> (SELECT count(*) FROM connection_binding.profile_application_applied_connection WHERE application_id = NEW.id)
            OR EXISTS (
                SELECT 1
                  FROM connection_binding.profile_application_applied_connection AS applied
                  LEFT JOIN connection_binding.profile_application_required_connection AS required
                    ON required.application_id = applied.application_id
                   AND required.connection_id = applied.connection_id
                 WHERE applied.application_id = NEW.id
                   AND (required.connection_id IS NULL OR required.connector_kind IS DISTINCT FROM applied.connector_kind)
            )
            OR EXISTS (
                SELECT 1
                  FROM connection_binding.profile_application_applied_connection AS applied
                  LEFT JOIN connection_binding.profile_application_expected_connection AS expected
                    ON expected.application_id = applied.application_id
                   AND expected.connection_id = applied.connection_id
                 WHERE applied.application_id = NEW.id
                   AND (expected.connection_id IS NULL
                        OR expected.binding_id IS DISTINCT FROM applied.binding_id
                        OR expected.connector_kind IS DISTINCT FROM applied.connector_kind
                        OR expected.authentication_mode IS DISTINCT FROM applied.authentication_mode
                        OR expected.endpoint_json IS DISTINCT FROM applied.endpoint_json
                        OR expected.credential_project_id IS DISTINCT FROM applied.credential_project_id
                        OR expected.credential_environment IS DISTINCT FROM applied.credential_environment
                        OR expected.credential_secret_path IS DISTINCT FROM applied.credential_secret_path
                        OR expected.credential_secret_key IS DISTINCT FROM applied.credential_secret_key
                        OR expected.provider_version IS DISTINCT FROM applied.provider_version
                        OR applied.binding_revision < GREATEST(1, expected.binding_revision))
            )
            OR EXISTS (
                SELECT 1
                  FROM connection_binding.profile_application_applied_connection AS applied
                  LEFT JOIN connection_binding.target_connection_binding AS binding
                    ON binding.id = applied.binding_id
                   AND binding.target_id = NEW.target_id
                   AND binding.project_id = NEW.project_id
                   AND binding.environment = NEW.environment
                   AND binding.connection_id = applied.connection_id
                 WHERE applied.application_id = NEW.id
                   AND (binding.id IS NULL
                        OR NOT binding.enabled
                        OR binding.health <> 'healthy'
                        OR binding.connector_kind IS DISTINCT FROM applied.connector_kind
                        OR binding.authentication_mode IS DISTINCT FROM applied.authentication_mode
                        OR binding.endpoint_json IS DISTINCT FROM applied.endpoint_json
                        OR binding.credential_project_id IS DISTINCT FROM applied.credential_project_id
                        OR binding.credential_environment IS DISTINCT FROM applied.credential_environment
                        OR binding.credential_secret_path IS DISTINCT FROM applied.credential_secret_path
                        OR binding.credential_secret_key IS DISTINCT FROM applied.credential_secret_key
                        OR binding.revision IS DISTINCT FROM applied.binding_revision
                        OR binding.validated_version IS DISTINCT FROM applied.provider_version)
            )
        ) THEN
            RAISE EXCEPTION 'applied profile application requires complete binding evidence';
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        old_application_id := OLD.application_id;
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        new_application_id := NEW.application_id;
    END IF;
    -- Serialize child mutation with completion. Locking both possible parents
    -- in canonical order closes move/delete races across terminalization.
    PERFORM application.id
      FROM connection_binding.profile_application AS application
     WHERE application.id = old_application_id OR application.id = new_application_id
     ORDER BY application.id
     FOR UPDATE NOWAIT;
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        IF EXISTS (
            SELECT 1 FROM connection_binding.profile_application
             WHERE id = OLD.application_id AND status = 'applied'
        ) THEN
            RAISE EXCEPTION 'applied profile application evidence is immutable';
        END IF;
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        IF EXISTS (
            SELECT 1 FROM connection_binding.profile_application
             WHERE id = NEW.application_id AND status = 'applied'
        ) THEN
            RAISE EXCEPTION 'applied profile application evidence is immutable';
        END IF;
    END IF;
    IF TG_OP <> 'INSERT'
       AND TG_TABLE_NAME IN ('profile_application_required_connection', 'profile_application_expected_connection') THEN
        RAISE EXCEPTION 'profile application required intent is immutable';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;

-- A new checkpoint must begin non-terminal. Completion is a separate,
-- evidence-checking CAS boundary below.
CREATE OR REPLACE FUNCTION connection_binding.require_initial_profile_application_state()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, connection_binding
AS $$
BEGIN
    IF NEW.status <> 'applying' OR (
        NEW.revision <> 1
        AND current_user <> (
            SELECT pg_catalog.pg_get_userbyid(procedure.proowner)
              FROM pg_catalog.pg_proc AS procedure
              JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = procedure.pronamespace
             WHERE namespace.nspname = 'connection_binding'
               AND procedure.proname = 'delete_profile_application_for_replacement'
               AND procedure.pronargs = 8
        )
    ) THEN
        RAISE EXCEPTION 'profile application must begin in applying revision 1 or an owner replacement revision';
    END IF;
    IF NEW.revision = 1 AND (NEW.last_completed_application_id <> '' OR NEW.last_completed_at IS NOT NULL) THEN
        RAISE EXCEPTION 'new profile application cannot claim prior completion';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION connection_binding.complete_profile_application(
    application_id text,
    expected_revision bigint,
    expected_checkout_id text,
    expected_runtime_id text,
    expected_target_id text,
    expected_project_id text,
    expected_environment text,
    completion_updated_at timestamptz
) RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, connection_binding
AS $$
DECLARE
    affected bigint;
BEGIN
    -- Lock the parent in a separate statement so a waiter receives a fresh
    -- READ COMMITTED snapshot after any child mutation commits.
    PERFORM profile_application.id
     FROM connection_binding.profile_application AS profile_application
     WHERE profile_application.id = complete_profile_application.application_id
     FOR UPDATE NOWAIT;
    -- Lock the exact binding rows next so none can rotate or disable after
    -- validation but before terminalization.
    PERFORM binding.id
      FROM connection_binding.target_connection_binding AS binding
      JOIN connection_binding.profile_application_applied_connection AS applied
        ON applied.binding_id = binding.id
     WHERE applied.application_id = complete_profile_application.application_id
     ORDER BY binding.id
     FOR UPDATE OF binding NOWAIT;
    PERFORM set_config('connection_binding.profile_application_completion', 'on', true);
    UPDATE connection_binding.profile_application
       SET status = 'applied',
           last_completed_application_id = application_id,
           last_completed_at = completion_updated_at,
           updated_at = completion_updated_at,
           revision = revision + 1
     WHERE id = application_id
       AND revision = expected_revision
       AND status IN ('applying', 'incomplete')
       AND checkout_id = expected_checkout_id
       AND runtime_id = expected_runtime_id
       AND target_id = expected_target_id
       AND project_id = expected_project_id
       AND environment = expected_environment
       AND completion_updated_at >= updated_at;
    GET DIAGNOSTICS affected = ROW_COUNT;
    PERFORM set_config('connection_binding.profile_application_completion', 'off', true);
    RETURN affected;
END;
$$;

-- Delete the old exact checkpoint as one owner-executed CAS operation. The
-- Only owner/migrator administration can execute this function; SECURITY
-- DEFINER changes current_user only for its bounded CAS-checked body.
DROP FUNCTION IF EXISTS connection_binding.delete_profile_application_for_replacement(text, bigint, text, text, text, text, text);
CREATE OR REPLACE FUNCTION connection_binding.delete_profile_application_for_replacement(
    application_id text,
    expected_revision bigint,
    expected_checkout_id text,
    expected_runtime_id text,
    expected_target_id text,
    expected_project_id text,
    expected_environment text,
    replacement_application_id text
) RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, connection_binding
AS $$
DECLARE
    affected bigint;
BEGIN
    IF application_id = replacement_application_id THEN
        RETURN 0;
    END IF;
    PERFORM set_config('connection_binding.profile_application_replacement', 'on', true);
    DELETE FROM connection_binding.profile_application
     WHERE id = application_id
       AND revision = expected_revision
       AND checkout_id = expected_checkout_id
       AND runtime_id = expected_runtime_id
       AND target_id = expected_target_id
       AND project_id = expected_project_id
       AND environment = expected_environment;
    GET DIAGNOSTICS affected = ROW_COUNT;
    PERFORM set_config('connection_binding.profile_application_replacement', 'off', true);
    RETURN affected;
END;
$$;

DROP TRIGGER IF EXISTS profile_application_identity_guard ON connection_binding.profile_application;
CREATE TRIGGER profile_application_identity_guard
    BEFORE UPDATE ON connection_binding.profile_application
    FOR EACH ROW EXECUTE FUNCTION connection_binding.reject_profile_application_mutation();
DROP TRIGGER IF EXISTS profile_application_initial_state_guard ON connection_binding.profile_application;
CREATE TRIGGER profile_application_initial_state_guard
    BEFORE INSERT ON connection_binding.profile_application
    FOR EACH ROW EXECUTE FUNCTION connection_binding.require_initial_profile_application_state();
DROP TRIGGER IF EXISTS profile_application_no_delete ON connection_binding.profile_application;
CREATE TRIGGER profile_application_no_delete
    BEFORE DELETE ON connection_binding.profile_application
    FOR EACH ROW EXECUTE FUNCTION connection_binding.reject_profile_application_mutation();
DROP TRIGGER IF EXISTS profile_application_required_no_mutation ON connection_binding.profile_application_required_connection;
CREATE TRIGGER profile_application_required_no_mutation
    BEFORE INSERT OR UPDATE OR DELETE ON connection_binding.profile_application_required_connection
    FOR EACH ROW EXECUTE FUNCTION connection_binding.reject_profile_application_mutation();
DROP TRIGGER IF EXISTS profile_application_expected_no_mutation ON connection_binding.profile_application_expected_connection;
CREATE TRIGGER profile_application_expected_no_mutation
    BEFORE INSERT OR UPDATE OR DELETE ON connection_binding.profile_application_expected_connection
    FOR EACH ROW EXECUTE FUNCTION connection_binding.reject_profile_application_mutation();
DROP TRIGGER IF EXISTS profile_application_applied_guard ON connection_binding.profile_application_applied_connection;
CREATE TRIGGER profile_application_applied_guard
    BEFORE INSERT OR UPDATE OR DELETE ON connection_binding.profile_application_applied_connection
    FOR EACH ROW EXECUTE FUNCTION connection_binding.reject_profile_application_mutation();

REVOKE ALL ON TABLE connection_binding.profile_application,
    connection_binding.profile_application_required_connection,
    connection_binding.profile_application_expected_connection,
    connection_binding.profile_application_applied_connection FROM PUBLIC;
REVOKE ALL ON FUNCTION connection_binding.reject_profile_application_mutation() FROM PUBLIC;
REVOKE ALL ON FUNCTION connection_binding.require_current_profile_binding_evidence() FROM PUBLIC;
REVOKE ALL ON FUNCTION connection_binding.require_initial_profile_application_state() FROM PUBLIC;
REVOKE ALL ON FUNCTION connection_binding.complete_profile_application(text, bigint, text, text, text, text, text, timestamptz) FROM PUBLIC;
REVOKE ALL ON FUNCTION connection_binding.delete_profile_application_for_replacement(text, bigint, text, text, text, text, text, text) FROM PUBLIC;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_owner') THEN
        GRANT ALL ON TABLE connection_binding.profile_application,
            connection_binding.profile_application_required_connection,
            connection_binding.profile_application_expected_connection,
            connection_binding.profile_application_applied_connection TO leapview_control_owner;
        GRANT EXECUTE ON FUNCTION connection_binding.reject_profile_application_mutation() TO leapview_control_owner;
        GRANT EXECUTE ON FUNCTION connection_binding.require_current_profile_binding_evidence() TO leapview_control_owner;
        GRANT EXECUTE ON FUNCTION connection_binding.require_initial_profile_application_state() TO leapview_control_owner;
        GRANT EXECUTE ON FUNCTION connection_binding.complete_profile_application(text, bigint, text, text, text, text, text, timestamptz) TO leapview_control_owner;
        GRANT EXECUTE ON FUNCTION connection_binding.delete_profile_application_for_replacement(text, bigint, text, text, text, text, text, text) TO leapview_control_owner;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_migrator') THEN
        GRANT USAGE ON SCHEMA connection_binding TO leapview_control_migrator;
        GRANT ALL ON TABLE connection_binding.profile_application,
            connection_binding.profile_application_required_connection,
            connection_binding.profile_application_expected_connection,
            connection_binding.profile_application_applied_connection TO leapview_control_migrator;
        GRANT EXECUTE ON FUNCTION connection_binding.reject_profile_application_mutation() TO leapview_control_migrator;
        GRANT EXECUTE ON FUNCTION connection_binding.require_current_profile_binding_evidence() TO leapview_control_migrator;
        GRANT EXECUTE ON FUNCTION connection_binding.require_initial_profile_application_state() TO leapview_control_migrator;
        GRANT EXECUTE ON FUNCTION connection_binding.complete_profile_application(text, bigint, text, text, text, text, text, timestamptz) TO leapview_control_migrator;
        GRANT EXECUTE ON FUNCTION connection_binding.delete_profile_application_for_replacement(text, bigint, text, text, text, text, text, text) TO leapview_control_migrator;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA connection_binding TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON connection_binding.profile_application,
            connection_binding.profile_application_required_connection,
            connection_binding.profile_application_expected_connection,
            connection_binding.profile_application_applied_connection TO leapview_control_runtime;
        -- Runtime Save replaces only mutable, non-terminal applied evidence;
        -- the applied-child trigger rejects deletion after terminalization.
        GRANT DELETE ON connection_binding.profile_application_applied_connection TO leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION connection_binding.require_current_profile_binding_evidence() TO leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION connection_binding.complete_profile_application(text, bigint, text, text, text, text, text, timestamptz) TO leapview_control_runtime;
        REVOKE EXECUTE ON FUNCTION connection_binding.delete_profile_application_for_replacement(text, bigint, text, text, text, text, text, text) FROM leapview_control_runtime;
        REVOKE DELETE ON connection_binding.profile_application,
            connection_binding.profile_application_required_connection,
            connection_binding.profile_application_expected_connection FROM leapview_control_runtime;
        REVOKE TRUNCATE, REFERENCES, TRIGGER ON connection_binding.profile_application,
            connection_binding.profile_application_required_connection,
            connection_binding.profile_application_expected_connection,
            connection_binding.profile_application_applied_connection FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA connection_binding TO leapview_control_readonly;
        GRANT SELECT ON connection_binding.profile_application,
            connection_binding.profile_application_required_connection,
            connection_binding.profile_application_expected_connection,
            connection_binding.profile_application_applied_connection TO leapview_control_readonly;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON connection_binding.profile_application,
            connection_binding.profile_application_required_connection,
            connection_binding.profile_application_expected_connection,
            connection_binding.profile_application_applied_connection FROM leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA connection_binding TO leapview_control_backup;
        GRANT SELECT ON connection_binding.profile_application,
            connection_binding.profile_application_required_connection,
            connection_binding.profile_application_expected_connection,
            connection_binding.profile_application_applied_connection TO leapview_control_backup;
    END IF;
END
$$;
