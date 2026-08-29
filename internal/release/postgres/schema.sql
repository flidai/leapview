-- Clean-slate release control authority (ADR-0016).
--
-- Release owns the API-facing immutable release identity, artifact evidence,
-- candidate provenance, and deployment linkage. Canonical delivery selection
-- remains owned by the delivery capability; this schema intentionally does
-- not recreate legacy candidate/deployment/serving-pointer projections.

CREATE SCHEMA IF NOT EXISTS release;

CREATE TABLE IF NOT EXISTS release.release_record (
    release_id text PRIMARY KEY,
    project_id text NOT NULL,
    environment text NOT NULL,
    generation_id text NOT NULL,
    project_digest text NOT NULL CHECK (project_digest ~ '^sha256:[0-9a-f]{64}$'),
    artifact_digest text NOT NULL CHECK (artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    artifact_actual_digest text,
    artifact_size_bytes bigint NOT NULL DEFAULT 0 CHECK (artifact_size_bytes >= 0),
    artifact_uploaded_at timestamptz,
    request_digest text NOT NULL CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    idempotency_key text NOT NULL,
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','validating','ready','failed')),
    provenance jsonb NOT NULL CHECK (jsonb_typeof(provenance) = 'object' AND octet_length(provenance::text) <= 65536),
    created_by text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    finalized_at timestamptz,
    error text NOT NULL DEFAULT '',
    CHECK (release_id = btrim(release_id) AND octet_length(release_id) BETWEEN 1 AND 255),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255),
    CHECK (environment = btrim(environment) AND octet_length(environment) BETWEEN 1 AND 255),
    CHECK (generation_id = btrim(generation_id) AND octet_length(generation_id) BETWEEN 1 AND 255),
    CHECK (idempotency_key = btrim(idempotency_key) AND octet_length(idempotency_key) BETWEEN 1 AND 512),
    CHECK (created_by = btrim(created_by) AND octet_length(created_by) BETWEEN 1 AND 255),
    CHECK (artifact_actual_digest IS NULL OR artifact_actual_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (
        (artifact_actual_digest IS NULL AND artifact_uploaded_at IS NULL AND artifact_size_bytes = 0)
        OR (artifact_actual_digest IS NOT NULL AND artifact_uploaded_at IS NOT NULL
            AND artifact_actual_digest = artifact_digest AND artifact_size_bytes >= 0)
    ),
    CHECK (
        (status IN ('draft', 'validating') AND finalized_at IS NULL AND error = '')
        OR (status = 'ready' AND finalized_at IS NOT NULL AND error = '')
        OR (status = 'failed' AND finalized_at IS NOT NULL AND octet_length(error) BETWEEN 1 AND 4096)
    ),
    CHECK (octet_length(error) <= 4096),
    UNIQUE (project_id, idempotency_key),
    UNIQUE (project_id, environment, generation_id)
);

CREATE TABLE IF NOT EXISTS release.release_connection (
    release_id text NOT NULL REFERENCES release.release_record(release_id) ON DELETE RESTRICT,
    connection_id text NOT NULL,
    revision_id text NOT NULL,
    PRIMARY KEY (release_id, connection_id),
    CHECK (connection_id = btrim(connection_id) AND octet_length(connection_id) BETWEEN 1 AND 255),
    CHECK (revision_id = btrim(revision_id) AND octet_length(revision_id) BETWEEN 1 AND 255)
);

CREATE OR REPLACE FUNCTION release.guard_release_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT (NEW.status IS NOT DISTINCT FROM OLD.status)
       AND NOT (OLD.status = 'draft' AND NEW.status = 'validating')
       AND NOT (OLD.status = 'validating' AND NEW.status IN ('ready', 'failed')) THEN
        RAISE EXCEPTION 'illegal release status transition';
    END IF;
    IF NEW.status IN ('validating', 'ready')
       AND (NEW.artifact_uploaded_at IS NULL OR NEW.artifact_actual_digest IS NULL
            OR NEW.artifact_actual_digest <> NEW.artifact_digest) THEN
        RAISE EXCEPTION 'release status requires matching uploaded artifact';
    END IF;
    IF NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'release created_at is immutable';
    END IF;
    IF NEW.release_id IS DISTINCT FROM OLD.release_id
       OR NEW.project_id IS DISTINCT FROM OLD.project_id
       OR NEW.environment IS DISTINCT FROM OLD.environment
       OR NEW.generation_id IS DISTINCT FROM OLD.generation_id
       OR NEW.project_digest IS DISTINCT FROM OLD.project_digest
       OR NEW.artifact_digest IS DISTINCT FROM OLD.artifact_digest
       OR NEW.request_digest IS DISTINCT FROM OLD.request_digest
       OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key
       OR NEW.provenance IS DISTINCT FROM OLD.provenance
       OR NEW.created_by IS DISTINCT FROM OLD.created_by
       OR (NEW.artifact_actual_digest IS DISTINCT FROM OLD.artifact_actual_digest
           OR NEW.artifact_size_bytes IS DISTINCT FROM OLD.artifact_size_bytes
           OR NEW.artifact_uploaded_at IS DISTINCT FROM OLD.artifact_uploaded_at)
          AND NOT (
              OLD.status = 'draft'
              AND OLD.artifact_actual_digest IS NULL
              AND OLD.artifact_uploaded_at IS NULL
              AND NEW.artifact_actual_digest IS NOT NULL
              AND NEW.artifact_uploaded_at IS NOT NULL
              AND NEW.artifact_actual_digest = NEW.artifact_digest
          )
       OR OLD.status IN ('draft','validating') AND NEW.status IS NOT DISTINCT FROM OLD.status AND (
           NEW.finalized_at IS DISTINCT FROM OLD.finalized_at
           OR NEW.error IS DISTINCT FROM OLD.error)
       OR OLD.status IN ('ready','failed') AND (
           NEW.status IS DISTINCT FROM OLD.status
           OR NEW.error IS DISTINCT FROM OLD.error
           OR NEW.finalized_at IS DISTINCT FROM OLD.finalized_at) THEN
        RAISE EXCEPTION 'release immutable identity or evidence cannot be mutated';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS release_record_guard ON release.release_record;
CREATE TRIGGER release_record_guard
    BEFORE UPDATE ON release.release_record
    FOR EACH ROW EXECUTE FUNCTION release.guard_release_mutation();

-- Candidate provenance is immutable admission evidence. A replay with the
-- same candidate revision and a different digest is a conflict in the
-- repository; the database uniqueness constraint prevents a second authority.
CREATE TABLE IF NOT EXISTS release.candidate_provenance (
    project_id text NOT NULL,
    candidate_id text NOT NULL,
    candidate_revision bigint NOT NULL CHECK (candidate_revision > 0),
    provenance_digest text NOT NULL CHECK (provenance_digest ~ '^sha256:[0-9a-f]{64}$'),
    provenance jsonb NOT NULL CHECK (jsonb_typeof(provenance) = 'object' AND octet_length(provenance::text) <= 65536),
    retained_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (project_id, candidate_id, candidate_revision),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255),
    CHECK (candidate_id = btrim(candidate_id) AND octet_length(candidate_id) BETWEEN 1 AND 255)
);

-- Deployment linkage is immutable evidence. The delivery capability owns
-- activation and target pointers; release stores only this exact association.
CREATE TABLE IF NOT EXISTS release.deployment_linkage (
    deployment_id text PRIMARY KEY,
    project_id text NOT NULL,
    release_id text NOT NULL REFERENCES release.release_record(release_id) ON DELETE RESTRICT,
    rollback_of text REFERENCES release.release_record(release_id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (deployment_id = btrim(deployment_id) AND octet_length(deployment_id) BETWEEN 1 AND 255),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255),
    CHECK (rollback_of IS NULL OR rollback_of <> release_id),
    UNIQUE (project_id, deployment_id)
);

CREATE OR REPLACE FUNCTION release.reject_immutable_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'release immutable evidence cannot be mutated';
END;
$$;

DROP TRIGGER IF EXISTS candidate_provenance_immutable ON release.candidate_provenance;
CREATE TRIGGER candidate_provenance_immutable
    BEFORE UPDATE OR DELETE ON release.candidate_provenance
    FOR EACH ROW EXECUTE FUNCTION release.reject_immutable_mutation();

DROP TRIGGER IF EXISTS deployment_linkage_immutable ON release.deployment_linkage;
CREATE TRIGGER deployment_linkage_immutable
    BEFORE UPDATE OR DELETE ON release.deployment_linkage
    FOR EACH ROW EXECUTE FUNCTION release.reject_immutable_mutation();

DROP TRIGGER IF EXISTS release_connection_immutable ON release.release_connection;
CREATE TRIGGER release_connection_immutable
    BEFORE UPDATE OR DELETE ON release.release_connection
    FOR EACH ROW EXECUTE FUNCTION release.reject_immutable_mutation();

CREATE OR REPLACE FUNCTION release.guard_connection_insert()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    parent_status text;
    parent_uploaded_at timestamptz;
BEGIN
    SELECT status, artifact_uploaded_at
      INTO parent_status, parent_uploaded_at
      FROM release.release_record
     WHERE release_id = NEW.release_id
     FOR UPDATE;
    IF NOT FOUND OR parent_status <> 'draft' OR parent_uploaded_at IS NOT NULL THEN
        RAISE EXCEPTION 'release connections require a draft release before artifact upload';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS release_connection_insert_guard ON release.release_connection;
CREATE TRIGGER release_connection_insert_guard
    BEFORE INSERT ON release.release_connection
    FOR EACH ROW EXECUTE FUNCTION release.guard_connection_insert();

CREATE INDEX IF NOT EXISTS release_record_project_created_idx
    ON release.release_record(project_id, created_at DESC, release_id DESC);
CREATE INDEX IF NOT EXISTS candidate_provenance_generation_idx
    ON release.candidate_provenance(project_id, (provenance -> 'plan' -> 'identity' ->> 'environment'), (provenance -> 'plan' -> 'identity' ->> 'generationId'));

REVOKE ALL ON SCHEMA release FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA release FROM PUBLIC;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA release FROM PUBLIC;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA release TO leapview_control_runtime;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON release.release_record, release.release_connection
            FROM leapview_control_runtime;
        GRANT SELECT ON release.release_record, release.release_connection TO leapview_control_runtime;
        GRANT INSERT (release_id, project_id, environment, generation_id, project_digest,
                      artifact_digest, request_digest, idempotency_key, provenance, created_by)
            ON release.release_record TO leapview_control_runtime;
        GRANT UPDATE (artifact_actual_digest, artifact_size_bytes, artifact_uploaded_at,
                      status, finalized_at, error)
            ON release.release_record TO leapview_control_runtime;
        GRANT INSERT ON release.release_connection TO leapview_control_runtime;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON release.candidate_provenance, release.deployment_linkage
            FROM leapview_control_runtime;
        GRANT SELECT ON release.candidate_provenance, release.deployment_linkage TO leapview_control_runtime;
        GRANT INSERT (project_id, candidate_id, candidate_revision, provenance_digest, provenance)
            ON release.candidate_provenance TO leapview_control_runtime;
        GRANT INSERT (deployment_id, project_id, release_id, rollback_of)
            ON release.deployment_linkage TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA release TO leapview_control_readonly;
        GRANT SELECT ON ALL TABLES IN SCHEMA release TO leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA release TO leapview_control_backup;
        GRANT SELECT ON ALL TABLES IN SCHEMA release TO leapview_control_backup;
    END IF;
END
$$;
