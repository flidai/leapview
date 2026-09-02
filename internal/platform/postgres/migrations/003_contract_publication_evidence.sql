-- FAI-622: immutable leapview.contract/v1 publication evidence.
-- The existing composite resource identity remains the only identity authority.

SET ROLE leapview_control_owner;

CREATE TABLE IF NOT EXISTS project.contract_publication (
    instance_id             platform.resource_id NOT NULL,
    authored_id             platform.resource_id NOT NULL,
    resource_kind           text NOT NULL CHECK (resource_kind IN ('source', 'model', 'semantic_model')),
    version                 text NOT NULL CHECK (length(version) BETWEEN 5 AND 255),
    version_baseline        text NOT NULL CHECK (length(version_baseline) BETWEEN 5 AND 255 AND position('+' IN version_baseline) = 0),
    projection_profile      text NOT NULL CHECK (projection_profile = 'leapview.contract/v1'),
    canonical_bytes         bytea NOT NULL CHECK (octet_length(canonical_bytes) BETWEEN 1 AND 16777216),
    canonical_digest        text NOT NULL CHECK (
        length(canonical_digest) = 71
        AND substr(canonical_digest, 1, 7) = 'sha256:'
        AND substr(canonical_digest, 8) !~ '[^0-9a-f]'
    ),
    published_at            timestamptz NOT NULL DEFAULT clock_timestamp(),
    validation_evidence_json text NOT NULL CHECK (
        octet_length(validation_evidence_json) BETWEEN 1 AND 65536
        AND jsonb_typeof(validation_evidence_json::jsonb) = 'object'
        AND validation_evidence_json::jsonb ->> 'version' = '1'
        AND jsonb_typeof(validation_evidence_json::jsonb -> 'checks') = 'array'
        AND jsonb_array_length(validation_evidence_json::jsonb -> 'checks') > 0
    ),
    PRIMARY KEY (instance_id, authored_id, resource_kind, version_baseline),
    FOREIGN KEY (instance_id, authored_id, resource_kind)
        REFERENCES project.resource_identity(instance_id, authored_id, resource_kind) ON DELETE RESTRICT
);

CREATE OR REPLACE FUNCTION project.reject_contract_publication_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'published contract evidence is immutable';
END;
$$;

DROP TRIGGER IF EXISTS contract_publication_immutable ON project.contract_publication;
CREATE TRIGGER contract_publication_immutable
    BEFORE UPDATE OR DELETE ON project.contract_publication
    FOR EACH ROW EXECUTE FUNCTION project.reject_contract_publication_mutation();

DROP TRIGGER IF EXISTS contract_publication_no_truncate ON project.contract_publication;
CREATE TRIGGER contract_publication_no_truncate
    BEFORE TRUNCATE ON project.contract_publication
    FOR EACH STATEMENT EXECUTE FUNCTION project.reject_contract_publication_mutation();

GRANT SELECT, INSERT ON project.contract_publication TO leapview_control_runtime;
GRANT SELECT ON project.contract_publication TO leapview_control_readonly;
REVOKE UPDATE, DELETE, TRUNCATE ON project.contract_publication FROM leapview_control_runtime, leapview_control_readonly;
REVOKE ALL ON FUNCTION project.reject_contract_publication_mutation() FROM PUBLIC;

RESET ROLE;
