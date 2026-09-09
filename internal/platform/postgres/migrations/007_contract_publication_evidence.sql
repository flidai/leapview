-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- Immutable FAI-622 publication evidence. This table is intentionally scoped
-- by instance and authored graph identity only; lifecycle authority remains
-- outside this capability.
CREATE TABLE IF NOT EXISTS project.contract_publication (
    instance_id             text NOT NULL,
    authored_id             text NOT NULL,
    resource_kind           text NOT NULL,
    version                 text NOT NULL,
    version_baseline        text NOT NULL,
    projection_profile      text NOT NULL,
    canonical_bytes         bytea NOT NULL,
    canonical_digest        text NOT NULL,
    validation_evidence_json text NOT NULL,
    published_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (instance_id, authored_id, resource_kind, version_baseline),
    CHECK (instance_id = btrim(instance_id) AND octet_length(instance_id) BETWEEN 1 AND 255),
    CHECK (authored_id = btrim(authored_id) AND octet_length(authored_id) BETWEEN 1 AND 255 AND authored_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    CHECK (resource_kind IN ('source', 'model', 'semantic_model')),
    CHECK (version = btrim(version) AND octet_length(version) BETWEEN 5 AND 255),
    CHECK (version_baseline = btrim(version_baseline) AND octet_length(version_baseline) BETWEEN 5 AND 255 AND position('+' IN version_baseline) = 0),
    CHECK (projection_profile = 'leapview.contract/v1'),
    CHECK (octet_length(canonical_bytes) BETWEEN 1 AND 16777216),
    CHECK (canonical_digest = 'sha256:' || pg_catalog.encode(pg_catalog.sha256(canonical_bytes), 'hex')),
    CHECK (convert_from(canonical_bytes, 'UTF8')::jsonb ->> 'apiVersion' = 'leapview.dev/v1'),
    CHECK (projection_profile = convert_from(canonical_bytes, 'UTF8')::jsonb ->> 'profile'),
    CHECK (authored_id = convert_from(canonical_bytes, 'UTF8')::jsonb -> 'metadata' ->> 'id'),
    CHECK (resource_kind = CASE convert_from(canonical_bytes, 'UTF8')::jsonb ->> 'kind'
        WHEN 'Source' THEN 'source'
        WHEN 'Model' THEN 'model'
        WHEN 'SemanticModel' THEN 'semantic_model'
        ELSE '' END),
    CHECK (version = convert_from(canonical_bytes, 'UTF8')::jsonb -> 'metadata' -> 'contract' ->> 'version'),
    CHECK (version_baseline = split_part(version, '+', 1)),
    CHECK (octet_length(validation_evidence_json) BETWEEN 1 AND 65536
        AND jsonb_typeof(validation_evidence_json::jsonb) = 'object'
        AND validation_evidence_json::jsonb ->> 'version' = '1'
        AND jsonb_typeof(validation_evidence_json::jsonb -> 'checks') = 'array'
        AND jsonb_array_length(validation_evidence_json::jsonb -> 'checks') > 0)
);

-- Publication evidence is append-only even for highly privileged SQL users.
-- The statement trigger closes the TRUNCATE escape hatch left by row guards.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION project.reject_contract_publication_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, project
AS $$
BEGIN
    RAISE EXCEPTION 'published contract evidence is immutable';
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS contract_publication_immutable ON project.contract_publication;
CREATE TRIGGER contract_publication_immutable
    BEFORE UPDATE OR DELETE ON project.contract_publication
    FOR EACH ROW EXECUTE FUNCTION project.reject_contract_publication_mutation();
DROP TRIGGER IF EXISTS contract_publication_no_truncate ON project.contract_publication;
CREATE TRIGGER contract_publication_no_truncate
    BEFORE TRUNCATE ON project.contract_publication
    FOR EACH STATEMENT EXECUTE FUNCTION project.reject_contract_publication_mutation();

REVOKE ALL ON TABLE project.contract_publication FROM PUBLIC;
REVOKE ALL ON FUNCTION project.reject_contract_publication_mutation() FROM PUBLIC;
-- Replay is a read operation: runtime and readonly/backup roles receive SELECT,
-- while only runtime receives INSERT. No role receives UPDATE, DELETE, or
-- TRUNCATE, and the trigger remains the database-side final defense.
-- +goose StatementBegin
DO $$
DECLARE role_name text;
BEGIN
    FOREACH role_name IN ARRAY ARRAY[
        'leapview_control_owner', 'leapview_control_migrator',
        'leapview_control_runtime', 'leapview_control_readonly',
        'leapview_control_backup'
    ] LOOP
        IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = role_name) THEN
            EXECUTE format('GRANT USAGE ON SCHEMA project TO %I', role_name);
            IF role_name IN ('leapview_control_owner', 'leapview_control_migrator') THEN
                EXECUTE format('GRANT ALL ON project.contract_publication TO %I', role_name);
                EXECUTE format('GRANT EXECUTE ON FUNCTION project.reject_contract_publication_mutation() TO %I', role_name);
            ELSIF role_name = 'leapview_control_runtime' THEN
                EXECUTE 'GRANT SELECT, INSERT ON project.contract_publication TO leapview_control_runtime';
                EXECUTE 'GRANT EXECUTE ON FUNCTION project.reject_contract_publication_mutation() TO leapview_control_runtime';
            ELSE
                EXECUTE format('GRANT SELECT ON project.contract_publication TO %I', role_name);
            END IF;
            IF role_name NOT IN ('leapview_control_owner', 'leapview_control_migrator') THEN
                EXECUTE format('REVOKE UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON project.contract_publication FROM %I', role_name);
            END IF;
        END IF;
    END LOOP;
END
$$;
-- +goose StatementEnd

RESET ROLE;

-- +goose Down
SET LOCAL ROLE leapview_control_owner;
DROP TABLE IF EXISTS project.contract_publication;
DROP FUNCTION IF EXISTS project.reject_contract_publication_mutation();
RESET ROLE;
