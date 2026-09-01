-- Clean-slate project identity authority (ADR-0016).
--
-- The compiler remains authoritative for the project graph.  This schema
-- stores only the durable project identity and the bounded authored metadata
-- needed by control-plane projections.  It deliberately does not recreate
-- the historical SQLite `projects` table or any serving-state projection.

CREATE SCHEMA IF NOT EXISTS project;

CREATE TABLE IF NOT EXISTS project.project_identity (
    project_id   text PRIMARY KEY,
    title        text NOT NULL,
    description  text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at   timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (
        project_id = btrim(project_id)
        AND octet_length(project_id) BETWEEN 1 AND 255
        AND project_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'
    ),
    CHECK (
        title = btrim(title)
        AND octet_length(title) BETWEEN 1 AND 255
    ),
    CHECK (octet_length(description) <= 4096),
    CHECK (updated_at >= created_at)
);

-- Project identity and its authored metadata are an immutable authority. A
-- replay with different metadata is rejected by the repository as a hard
-- conflict; direct UPDATE/DELETE attempts are rejected by the database too.
CREATE OR REPLACE FUNCTION project.reject_project_identity_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'project identity and authored metadata are immutable';
END;
$$;

DROP TRIGGER IF EXISTS project_identity_immutable ON project.project_identity;
CREATE TRIGGER project_identity_immutable
    BEFORE UPDATE OR DELETE ON project.project_identity
    FOR EACH ROW EXECUTE FUNCTION project.reject_project_identity_mutation();

-- Capability schemas are never reachable through PUBLIC defaults. The
-- explicit runtime grant is deliberately limited to the operations needed by
-- identity ensure and reads; no UPDATE or DELETE privilege is granted.
REVOKE ALL ON SCHEMA project FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA project FROM PUBLIC;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA project FROM PUBLIC;

DO $$
DECLARE
    role_name text;
BEGIN
    FOREACH role_name IN ARRAY ARRAY[
        'leapview_control_runtime',
        'leapview_control_readonly'
    ] LOOP
        IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = role_name) THEN
            EXECUTE format('GRANT USAGE ON SCHEMA project TO %I', role_name);
            IF role_name = 'leapview_control_runtime' THEN
                EXECUTE format('GRANT SELECT, INSERT ON project.project_identity TO %I', role_name);
            ELSE
                EXECUTE format('GRANT SELECT ON project.project_identity TO %I', role_name);
            END IF;
        END IF;
    END LOOP;
END
$$;

-- Immutable source delivery authority.  Source bytes are deliberately not
-- stored here: object_key is a caller-verified reference into the object
-- storage capability and this schema stores only its typed identity.
CREATE TABLE IF NOT EXISTS project.source_blob (
    project_id                text NOT NULL,
    storage_security_domain   text NOT NULL,
    digest                    text NOT NULL,
    size_bytes                bigint NOT NULL,
    object_key                text NOT NULL,
    content_type              text NOT NULL,
    metadata_digest           text NOT NULL,
    created_at                timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (project_id, storage_security_domain, digest),
    UNIQUE (project_id, storage_security_domain, object_key),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255),
    CHECK (storage_security_domain = btrim(storage_security_domain) AND octet_length(storage_security_domain) BETWEEN 1 AND 255),
    CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (size_bytes BETWEEN 0 AND 16777216),
    CHECK (object_key = btrim(object_key) AND octet_length(object_key) BETWEEN 1 AND 2048),
    CHECK (content_type = btrim(content_type) AND octet_length(content_type) BETWEEN 1 AND 255),
    CHECK (metadata_digest ~ '^sha256:[0-9a-f]{64}$')
);

CREATE TABLE IF NOT EXISTS project.source_snapshot (
    snapshot_id                    uuid PRIMARY KEY,
    project_id                     text NOT NULL,
    storage_security_domain        text NOT NULL,
    source_digest                  text NOT NULL,
    project_file                   text NOT NULL,
    project_digest                 text NOT NULL,
    project_artifact_object_key    text NOT NULL,
    project_artifact_digest        text NOT NULL,
    project_artifact_size_bytes    bigint NOT NULL,
    manifest_object_key            text NOT NULL,
    manifest_object_digest         text NOT NULL,
    manifest_object_size_bytes     bigint NOT NULL,
    compiler_version               text NOT NULL,
    schema_version                 bigint NOT NULL,
    state                          text NOT NULL DEFAULT 'building',
    sealed_at                      timestamptz,
    created_at                     timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (project_id, storage_security_domain, source_digest),
    UNIQUE (snapshot_id, project_id, storage_security_domain),
    UNIQUE (snapshot_id, source_digest),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255),
    CHECK (storage_security_domain = btrim(storage_security_domain) AND octet_length(storage_security_domain) BETWEEN 1 AND 255),
    CHECK (source_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (project_file = btrim(project_file) AND octet_length(project_file) BETWEEN 1 AND 1024),
    CHECK (project_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (project_artifact_object_key = btrim(project_artifact_object_key) AND octet_length(project_artifact_object_key) BETWEEN 1 AND 2048),
    CHECK (project_artifact_digest ~ '^sha256:[0-9a-f]{64}$' AND project_artifact_size_bytes BETWEEN 0 AND 67108864),
    CHECK (manifest_object_key = btrim(manifest_object_key) AND octet_length(manifest_object_key) BETWEEN 1 AND 2048),
    CHECK (manifest_object_digest ~ '^sha256:[0-9a-f]{64}$' AND manifest_object_size_bytes BETWEEN 0 AND 67108864),
    CHECK (compiler_version = btrim(compiler_version) AND octet_length(compiler_version) BETWEEN 1 AND 255),
    CHECK (schema_version > 0),
    CHECK (state IN ('building', 'sealed')),
    CHECK ((state = 'sealed') = (sealed_at IS NOT NULL))
);

CREATE TABLE IF NOT EXISTS project.source_snapshot_entry (
    snapshot_id                 uuid NOT NULL,
    project_id                  text NOT NULL,
    storage_security_domain     text NOT NULL,
    path                        text NOT NULL,
    digest                      text NOT NULL,
    size_bytes                  bigint NOT NULL,
    ordinal                     integer NOT NULL,
    PRIMARY KEY (snapshot_id, path),
    UNIQUE (snapshot_id, ordinal),
    FOREIGN KEY (snapshot_id, project_id, storage_security_domain)
        REFERENCES project.source_snapshot(snapshot_id, project_id, storage_security_domain) ON DELETE RESTRICT,
    FOREIGN KEY (project_id, storage_security_domain, digest)
        REFERENCES project.source_blob(project_id, storage_security_domain, digest) ON DELETE RESTRICT,
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255),
    CHECK (storage_security_domain = btrim(storage_security_domain) AND octet_length(storage_security_domain) BETWEEN 1 AND 255),
    CHECK (path = btrim(path) AND octet_length(path) BETWEEN 1 AND 1024),
    CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (size_bytes BETWEEN 0 AND 16777216),
    CHECK (ordinal >= 0)
);

CREATE TABLE IF NOT EXISTS project.source_attestation (
    attestation_id       uuid PRIMARY KEY,
    snapshot_id          uuid NOT NULL,
    source_digest        text NOT NULL,
    attestation_digest   text NOT NULL,
    payload              jsonb NOT NULL,
    revision             text NOT NULL DEFAULT '',
    repository           text NOT NULL DEFAULT '',
    ref                  text NOT NULL DEFAULT '',
    change_id            text NOT NULL DEFAULT '',
    created_at           timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (snapshot_id, attestation_digest),
    FOREIGN KEY (snapshot_id, source_digest)
        REFERENCES project.source_snapshot(snapshot_id, source_digest) ON DELETE RESTRICT,
    CHECK (source_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (attestation_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (jsonb_typeof(payload) = 'object' AND octet_length(payload::text) <= 16384),
    CHECK (octet_length(revision) <= 1024 AND octet_length(repository) <= 1024 AND octet_length(ref) <= 1024 AND octet_length(change_id) <= 1024)
);

CREATE TABLE IF NOT EXISTS project.source_sync_plan (
    plan_id                    uuid PRIMARY KEY,
    operation_id               uuid NOT NULL UNIQUE,
    project_id                 text NOT NULL,
    storage_security_domain    text NOT NULL,
    owner_id                   text NOT NULL,
    candidate_key              text NOT NULL,
    source_digest              text NOT NULL,
    project_file               text NOT NULL,
    request_digest             text NOT NULL,
    state                      text NOT NULL DEFAULT 'open',
    expires_at                 timestamptz NOT NULL,
    created_at                 timestamptz NOT NULL DEFAULT clock_timestamp(),
    committed_at               timestamptz,
    UNIQUE (project_id, storage_security_domain, owner_id, candidate_key, request_digest),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255),
    CHECK (storage_security_domain = btrim(storage_security_domain) AND octet_length(storage_security_domain) BETWEEN 1 AND 255),
    CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255),
    CHECK (candidate_key = btrim(candidate_key) AND octet_length(candidate_key) BETWEEN 1 AND 512),
    CHECK (source_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (project_file = btrim(project_file) AND octet_length(project_file) BETWEEN 1 AND 1024),
    CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (state IN ('open', 'committed', 'expired')),
    CHECK (expires_at > created_at AND expires_at <= created_at + interval '5 minutes'),
    CHECK ((state = 'committed') = (committed_at IS NOT NULL))
);

CREATE TABLE IF NOT EXISTS project.source_sync_plan_entry (
    plan_id                  uuid NOT NULL,
    path                     text NOT NULL,
    digest                   text NOT NULL,
    size_bytes               bigint NOT NULL,
    ordinal                  integer NOT NULL,
    PRIMARY KEY (plan_id, path),
    UNIQUE (plan_id, ordinal),
    FOREIGN KEY (plan_id) REFERENCES project.source_sync_plan(plan_id) ON DELETE RESTRICT,
    CHECK (path = btrim(path) AND octet_length(path) BETWEEN 1 AND 1024),
    CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (size_bytes BETWEEN 0 AND 16777216),
    CHECK (ordinal >= 0)
);

CREATE OR REPLACE FUNCTION project.reject_source_immutable_mutation()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, project AS $$
BEGIN
    RAISE EXCEPTION 'project source history is immutable';
END;
$$;

CREATE OR REPLACE FUNCTION project.guard_source_sync_plan_mutation()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, project AS $$
BEGIN
    IF NEW.plan_id IS DISTINCT FROM OLD.plan_id OR NEW.operation_id IS DISTINCT FROM OLD.operation_id
       OR NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.storage_security_domain IS DISTINCT FROM OLD.storage_security_domain
       OR NEW.owner_id IS DISTINCT FROM OLD.owner_id OR NEW.candidate_key IS DISTINCT FROM OLD.candidate_key
       OR NEW.source_digest IS DISTINCT FROM OLD.source_digest OR NEW.project_file IS DISTINCT FROM OLD.project_file
       OR NEW.request_digest IS DISTINCT FROM OLD.request_digest OR NEW.expires_at IS DISTINCT FROM OLD.expires_at
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'source synchronization plan identity is immutable';
    END IF;
    IF OLD.state <> 'open' OR NEW.state NOT IN ('committed', 'expired') OR NEW.state = OLD.state THEN
        RAISE EXCEPTION 'source synchronization plan transition is invalid';
    END IF;
    IF NEW.state = 'committed' AND NEW.committed_at IS NULL THEN
        NEW.committed_at := clock_timestamp();
    END IF;
    IF NEW.state = 'expired' AND NEW.committed_at IS NOT NULL THEN
        RAISE EXCEPTION 'expired source synchronization plan cannot have committed_at';
    END IF;
    IF NEW.state = 'expired' AND clock_timestamp() < OLD.expires_at THEN
        RAISE EXCEPTION 'source synchronization plan has not expired';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION project.guard_source_sync_plan_entry_insert()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, project AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM project.source_sync_plan p
        WHERE p.plan_id = NEW.plan_id AND p.state = 'open'
          AND p.expires_at > clock_timestamp()
    ) THEN
        RAISE EXCEPTION 'source synchronization plan is not open';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION project.guard_source_snapshot_mutation()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, project AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'project source history is immutable';
    END IF;
    IF NEW.snapshot_id IS DISTINCT FROM OLD.snapshot_id OR NEW.project_id IS DISTINCT FROM OLD.project_id
       OR NEW.storage_security_domain IS DISTINCT FROM OLD.storage_security_domain OR NEW.source_digest IS DISTINCT FROM OLD.source_digest
       OR NEW.project_file IS DISTINCT FROM OLD.project_file OR NEW.project_digest IS DISTINCT FROM OLD.project_digest
       OR NEW.project_artifact_object_key IS DISTINCT FROM OLD.project_artifact_object_key
       OR NEW.project_artifact_digest IS DISTINCT FROM OLD.project_artifact_digest
       OR NEW.project_artifact_size_bytes IS DISTINCT FROM OLD.project_artifact_size_bytes
       OR NEW.manifest_object_key IS DISTINCT FROM OLD.manifest_object_key
       OR NEW.manifest_object_digest IS DISTINCT FROM OLD.manifest_object_digest
       OR NEW.manifest_object_size_bytes IS DISTINCT FROM OLD.manifest_object_size_bytes
       OR NEW.compiler_version IS DISTINCT FROM OLD.compiler_version OR NEW.schema_version IS DISTINCT FROM OLD.schema_version
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'project source snapshot identity is immutable';
    END IF;
    IF OLD.state <> 'building' OR NEW.state <> 'sealed' OR NEW.sealed_at IS NOT NULL THEN
        RAISE EXCEPTION 'project source snapshot transition is invalid';
    END IF;
    NEW.sealed_at := clock_timestamp();
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION project.guard_source_snapshot_child_insert()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, project AS $$
DECLARE parent_id uuid;
BEGIN
    parent_id := NEW.snapshot_id;
    IF NOT EXISTS (
        SELECT 1 FROM project.source_snapshot s
        WHERE s.snapshot_id = parent_id AND s.state = 'building'
    ) THEN
        RAISE EXCEPTION 'project source snapshot is not building';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS source_blob_immutable ON project.source_blob;
CREATE TRIGGER source_blob_immutable BEFORE UPDATE OR DELETE ON project.source_blob FOR EACH ROW EXECUTE FUNCTION project.reject_source_immutable_mutation();
DROP TRIGGER IF EXISTS source_snapshot_immutable ON project.source_snapshot;
CREATE TRIGGER source_snapshot_immutable BEFORE UPDATE OR DELETE ON project.source_snapshot FOR EACH ROW EXECUTE FUNCTION project.guard_source_snapshot_mutation();
DROP TRIGGER IF EXISTS source_snapshot_entry_immutable ON project.source_snapshot_entry;
CREATE TRIGGER source_snapshot_entry_immutable BEFORE UPDATE OR DELETE ON project.source_snapshot_entry FOR EACH ROW EXECUTE FUNCTION project.reject_source_immutable_mutation();
DROP TRIGGER IF EXISTS source_attestation_immutable ON project.source_attestation;
CREATE TRIGGER source_attestation_immutable BEFORE UPDATE OR DELETE ON project.source_attestation FOR EACH ROW EXECUTE FUNCTION project.reject_source_immutable_mutation();
DROP TRIGGER IF EXISTS source_sync_plan_entry_immutable ON project.source_sync_plan_entry;
CREATE TRIGGER source_sync_plan_entry_immutable BEFORE UPDATE OR DELETE ON project.source_sync_plan_entry FOR EACH ROW EXECUTE FUNCTION project.reject_source_immutable_mutation();
DROP TRIGGER IF EXISTS source_sync_plan_entry_admission ON project.source_sync_plan_entry;
CREATE TRIGGER source_sync_plan_entry_admission BEFORE INSERT ON project.source_sync_plan_entry FOR EACH ROW EXECUTE FUNCTION project.guard_source_sync_plan_entry_insert();
DROP TRIGGER IF EXISTS source_sync_plan_transition ON project.source_sync_plan;
CREATE TRIGGER source_sync_plan_transition BEFORE UPDATE ON project.source_sync_plan FOR EACH ROW EXECUTE FUNCTION project.guard_source_sync_plan_mutation();
DROP TRIGGER IF EXISTS source_snapshot_entry_admission ON project.source_snapshot_entry;
CREATE TRIGGER source_snapshot_entry_admission BEFORE INSERT ON project.source_snapshot_entry FOR EACH ROW EXECUTE FUNCTION project.guard_source_snapshot_child_insert();
DROP TRIGGER IF EXISTS source_attestation_admission ON project.source_attestation;
CREATE TRIGGER source_attestation_admission BEFORE INSERT ON project.source_attestation FOR EACH ROW EXECUTE FUNCTION project.guard_source_snapshot_child_insert();

REVOKE ALL ON TABLE project.source_blob, project.source_snapshot, project.source_snapshot_entry,
    project.source_attestation, project.source_sync_plan, project.source_sync_plan_entry FROM PUBLIC;
REVOKE ALL ON FUNCTION project.reject_source_immutable_mutation(), project.guard_source_sync_plan_mutation(),
    project.guard_source_sync_plan_entry_insert(), project.guard_source_snapshot_mutation(),
    project.guard_source_snapshot_child_insert() FROM PUBLIC;
DO $$
DECLARE role_name text;
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        EXECUTE 'REVOKE UPDATE, DELETE ON project.source_blob, project.source_snapshot, project.source_snapshot_entry, project.source_attestation, project.source_sync_plan_entry FROM leapview_control_runtime';
    END IF;
    FOREACH role_name IN ARRAY ARRAY['leapview_control_owner','leapview_control_migrator','leapview_control_runtime','leapview_control_readonly','leapview_control_backup'] LOOP
        IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = role_name) THEN
            EXECUTE format('GRANT USAGE ON SCHEMA project TO %I', role_name);
            IF role_name IN ('leapview_control_owner','leapview_control_migrator') THEN
                EXECUTE format('GRANT ALL ON ALL TABLES IN SCHEMA project TO %I', role_name);
            ELSIF role_name = 'leapview_control_runtime' THEN
                EXECUTE 'GRANT SELECT, INSERT ON project.source_blob, project.source_snapshot, project.source_snapshot_entry, project.source_attestation, project.source_sync_plan, project.source_sync_plan_entry TO leapview_control_runtime';
                EXECUTE 'GRANT UPDATE (state, committed_at) ON project.source_sync_plan TO leapview_control_runtime';
                EXECUTE 'GRANT UPDATE (state, sealed_at) ON project.source_snapshot TO leapview_control_runtime';
            ELSE
                EXECUTE format('GRANT SELECT ON ALL TABLES IN SCHEMA project TO %I', role_name);
            END IF;
        END IF;
    END LOOP;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        EXECUTE 'GRANT EXECUTE ON FUNCTION project.guard_source_sync_plan_mutation() TO leapview_control_runtime';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_owner') THEN
        EXECUTE 'GRANT EXECUTE ON FUNCTION project.reject_source_immutable_mutation(), project.guard_source_sync_plan_mutation(), project.guard_source_sync_plan_entry_insert(), project.guard_source_snapshot_mutation(), project.guard_source_snapshot_child_insert() TO leapview_control_owner';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_migrator') THEN
        EXECUTE 'GRANT EXECUTE ON FUNCTION project.reject_source_immutable_mutation(), project.guard_source_sync_plan_mutation(), project.guard_source_sync_plan_entry_insert(), project.guard_source_snapshot_mutation(), project.guard_source_snapshot_child_insert() TO leapview_control_migrator';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        REVOKE ALL ON SCHEMA project FROM leapview_control_maintenance;
        REVOKE ALL ON TABLE project.source_blob, project.source_snapshot, project.source_snapshot_entry,
            project.source_attestation, project.source_sync_plan, project.source_sync_plan_entry FROM leapview_control_maintenance;
    END IF;
END
$$;
