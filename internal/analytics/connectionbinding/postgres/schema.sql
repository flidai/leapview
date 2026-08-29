-- PostgreSQL connection-binding capability schema (ADR-0016).
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
