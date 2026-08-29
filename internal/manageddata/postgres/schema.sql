-- Clean-slate managed-data control authority (ADR-0016).
-- Object storage owns bytes and DuckLake owns analytical metadata.  These
-- tables contain only identity, admission, serving, lease and reconciliation
-- evidence.  The schema intentionally does not recreate legacy SQLite names.

CREATE SCHEMA IF NOT EXISTS managed_data;
CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA managed_data;

CREATE TABLE IF NOT EXISTS managed_data.collection (
    collection_id text PRIMARY KEY,
    project_id text NOT NULL,
    connection_id text NOT NULL,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','archived')),
    created_by text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    archived_at timestamptz,
    request_digest text NOT NULL,
    UNIQUE (project_id, connection_id),
    CHECK (collection_id = btrim(collection_id) AND octet_length(collection_id) BETWEEN 1 AND 255),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255),
    CHECK (connection_id = btrim(connection_id) AND octet_length(connection_id) BETWEEN 1 AND 255),
    CHECK (name = btrim(name) AND octet_length(name) BETWEEN 1 AND 255),
    CHECK (octet_length(description) <= 4096),
    CHECK ((status = 'archived') = (archived_at IS NOT NULL)),
    CHECK (octet_length(request_digest) BETWEEN 1 AND 255)
);

CREATE OR REPLACE FUNCTION managed_data.guard_collection_insert() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
AS $$
BEGIN
  IF NEW.status <> 'active' OR NEW.archived_at IS NOT NULL THEN
    RAISE EXCEPTION 'collection must begin in canonical active state';
  END IF;
  NEW.created_at := clock_timestamp();
  NEW.updated_at := NEW.created_at;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS collection_insert_guard ON managed_data.collection;
CREATE TRIGGER collection_insert_guard BEFORE INSERT ON managed_data.collection FOR EACH ROW EXECUTE FUNCTION managed_data.guard_collection_insert();

CREATE TABLE IF NOT EXISTS managed_data.revision (
    revision_id text PRIMARY KEY,
    collection_id text NOT NULL REFERENCES managed_data.collection(collection_id) ON DELETE RESTRICT,
    sequence bigint NOT NULL CHECK (sequence > 0),
    digest text NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','ready','failed')),
    manifest jsonb NOT NULL,
    file_count bigint NOT NULL CHECK (file_count >= 0),
    size_bytes bigint NOT NULL CHECK (size_bytes >= 0),
    created_by text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    ready_at timestamptz,
    error text NOT NULL DEFAULT '',
    UNIQUE (collection_id, sequence),
    UNIQUE (collection_id, digest),
    UNIQUE (collection_id, revision_id),
    UNIQUE (collection_id, revision_id, digest),
    CHECK (octet_length(revision_id) BETWEEN 1 AND 255),
    CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (jsonb_typeof(manifest) = 'object' AND octet_length(manifest::text) <= 1048576),
    CHECK ((status = 'ready') = (ready_at IS NOT NULL)),
    CHECK (status <> 'failed' OR octet_length(error) > 0),
    CHECK (octet_length(error) <= 4096)
);

CREATE TABLE IF NOT EXISTS managed_data.revision_file (
    revision_id text NOT NULL REFERENCES managed_data.revision(revision_id) ON DELETE RESTRICT,
    logical_path text NOT NULL,
    size_bytes bigint NOT NULL CHECK (size_bytes >= 0),
    sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    storage_key text NOT NULL,
    media_type text NOT NULL DEFAULT '',
    etag text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (revision_id, logical_path),
    CHECK (logical_path = btrim(logical_path) AND octet_length(logical_path) BETWEEN 1 AND 1024),
    CHECK (octet_length(storage_key) BETWEEN 1 AND 2048),
    CHECK (octet_length(media_type) <= 255 AND octet_length(etag) <= 512)
);

CREATE TABLE IF NOT EXISTS managed_data.upload_session (
    upload_id text PRIMARY KEY,
    collection_id text NOT NULL REFERENCES managed_data.collection(collection_id) ON DELETE RESTRICT,
    base_revision_id text,
    revision_id text,
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open','committing','complete','aborted','expired','failed')),
    manifest jsonb NOT NULL,
    expected_file_count bigint NOT NULL CHECK (expected_file_count >= 0),
    expected_size_bytes bigint NOT NULL CHECK (expected_size_bytes >= 0),
    uploaded_file_count bigint NOT NULL DEFAULT 0 CHECK (uploaded_file_count >= 0),
    uploaded_size_bytes bigint NOT NULL DEFAULT 0 CHECK (uploaded_size_bytes >= 0),
    storage_backend text NOT NULL,
    staging_prefix text NOT NULL,
    created_by text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    completed_at timestamptz,
    cleanup_completed_at timestamptz,
    error text NOT NULL DEFAULT '',
    request_digest text NOT NULL,
    manifest_digest text NOT NULL,
    completion_digest text NOT NULL DEFAULT '',
    UNIQUE (collection_id, upload_id),
    FOREIGN KEY (collection_id, base_revision_id) REFERENCES managed_data.revision(collection_id, revision_id) ON DELETE RESTRICT,
    FOREIGN KEY (collection_id, revision_id) REFERENCES managed_data.revision(collection_id, revision_id) ON DELETE RESTRICT,
    CHECK (octet_length(upload_id) BETWEEN 1 AND 255),
    CHECK (jsonb_typeof(manifest) = 'object' AND octet_length(manifest::text) <= 1048576),
    CHECK (octet_length(storage_backend) BETWEEN 1 AND 255 AND storage_backend = btrim(storage_backend)),
    CHECK (octet_length(staging_prefix) BETWEEN 1 AND 2048),
    CHECK (uploaded_file_count <= expected_file_count AND uploaded_size_bytes <= expected_size_bytes),
    CHECK (expires_at > created_at),
    CHECK ((status = 'complete') = (revision_id IS NOT NULL AND completed_at IS NOT NULL)),
    CHECK (status <> 'failed' OR octet_length(error) > 0),
    CHECK (octet_length(error) <= 4096),
    CHECK (octet_length(request_digest) BETWEEN 1 AND 255),
    CHECK (manifest_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (completion_digest = '' OR completion_digest ~ '^sha256:[0-9a-f]{64}$')
);
CREATE INDEX IF NOT EXISTS upload_session_cleanup_idx ON managed_data.upload_session(status, cleanup_completed_at, updated_at, upload_id);
CREATE INDEX IF NOT EXISTS upload_session_expiry_idx ON managed_data.upload_session(status, expires_at);

CREATE OR REPLACE FUNCTION managed_data.guard_upload_insert() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
AS $$
DECLARE cstatus text; bstatus text;
BEGIN
  -- Upload rows have one canonical admission shape.  The trigger overwrites
  -- timestamps so a runtime caller cannot backdate an upload or fabricate
  -- lifecycle evidence while still retaining the table defaults for direct
  -- owner/migrator inserts.
  IF NEW.status <> 'open' OR NEW.uploaded_file_count <> 0 OR NEW.uploaded_size_bytes <> 0
     OR NEW.revision_id IS NOT NULL OR NEW.completed_at IS NOT NULL
     OR NEW.cleanup_completed_at IS NOT NULL OR NEW.error <> '' OR NEW.completion_digest <> '' THEN
    RAISE EXCEPTION 'upload session must begin in canonical open state';
  END IF;
  NEW.created_at := clock_timestamp();
  NEW.updated_at := NEW.created_at;
  SELECT status INTO cstatus FROM managed_data.collection WHERE collection_id=NEW.collection_id;
  IF cstatus IS DISTINCT FROM 'active' THEN RAISE EXCEPTION 'upload session requires an active collection'; END IF;
  IF NEW.base_revision_id IS NOT NULL THEN
    SELECT r.status INTO bstatus FROM managed_data.revision r WHERE r.collection_id=NEW.collection_id AND r.revision_id=NEW.base_revision_id;
    IF bstatus IS DISTINCT FROM 'ready' THEN RAISE EXCEPTION 'base revision must be ready in the same collection'; END IF;
  END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS upload_insert_guard ON managed_data.upload_session;
CREATE TRIGGER upload_insert_guard BEFORE INSERT ON managed_data.upload_session FOR EACH ROW EXECUTE FUNCTION managed_data.guard_upload_insert();

CREATE TABLE IF NOT EXISTS managed_data.multipart_upload (
    multipart_id text PRIMARY KEY,
    upload_id text NOT NULL REFERENCES managed_data.upload_session(upload_id) ON DELETE RESTRICT,
    logical_path text NOT NULL,
    sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    size_bytes bigint NOT NULL CHECK (size_bytes >= 0),
    object_key text NOT NULL DEFAULT '',
    provider_upload_id text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'creating' CHECK (status IN ('creating','open','completing','completed','aborting','aborted','failed')),
    existing boolean NOT NULL DEFAULT false,
    idempotency_identity text NOT NULL,
    completion_identity text NOT NULL DEFAULT '',
    completion_request_hash text NOT NULL DEFAULT '',
    abort_identity text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    completed_at timestamptz,
    aborted_at timestamptz,
    error text NOT NULL DEFAULT '',
    UNIQUE (upload_id, idempotency_identity),
    CHECK (logical_path = btrim(logical_path) AND octet_length(logical_path) BETWEEN 1 AND 1024),
    CHECK (octet_length(idempotency_identity) BETWEEN 1 AND 255),
    CHECK (octet_length(object_key) <= 2048 AND octet_length(provider_upload_id) <= 512),
    CHECK (completion_identity = '' OR octet_length(completion_identity) <= 255),
    CHECK (completion_request_hash = '' OR completion_request_hash ~ '^[0-9a-f]{64}$'),
    CHECK (abort_identity = '' OR octet_length(abort_identity) <= 255),
    CHECK (status = 'creating' OR octet_length(object_key) > 0 OR status IN ('aborting','aborted')),
    CHECK (NOT existing OR status = 'completed'),
    CHECK ((status = 'completed') = (completed_at IS NOT NULL)),
    CHECK ((status = 'aborted') = (aborted_at IS NOT NULL)),
    CHECK (status <> 'failed' OR octet_length(error) > 0),
    CHECK (octet_length(error) <= 4096),
    CHECK (octet_length(completion_identity) <= 255 AND octet_length(abort_identity) <= 255)
);
CREATE TABLE IF NOT EXISTS managed_data.multipart_part (
    multipart_id text NOT NULL REFERENCES managed_data.multipart_upload(multipart_id) ON DELETE RESTRICT,
    part_number integer NOT NULL CHECK (part_number BETWEEN 1 AND 10000),
    size_bytes bigint NOT NULL CHECK (size_bytes > 0),
    sha256 text NOT NULL DEFAULT '' CHECK (sha256 = '' OR sha256 ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (multipart_id, part_number)
);
CREATE TABLE IF NOT EXISTS managed_data.multipart_digest_lease (
    sha256 text PRIMARY KEY CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    owner_id text NOT NULL,
    fencing_epoch bigint NOT NULL CHECK (fencing_epoch > 0),
    state text NOT NULL DEFAULT 'held' CHECK (state IN ('held','released')),
    lease_until timestamptz NOT NULL,
    CHECK (octet_length(owner_id) BETWEEN 1 AND 255)
);

CREATE TABLE IF NOT EXISTS managed_data.environment_pointer (
    collection_id text NOT NULL REFERENCES managed_data.collection(collection_id) ON DELETE RESTRICT,
    environment text NOT NULL,
    revision_id text NOT NULL,
    revision_digest text NOT NULL CHECK (revision_digest ~ '^sha256:[0-9a-f]{64}$'),
    deployment_id text NOT NULL,
    generation bigint NOT NULL CHECK (generation > 0),
    updated_by text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (collection_id, environment),
    FOREIGN KEY (collection_id, revision_id, revision_digest) REFERENCES managed_data.revision(collection_id, revision_id, digest) ON DELETE RESTRICT,
    CHECK (environment = btrim(environment) AND octet_length(environment) BETWEEN 1 AND 128),
    CHECK (octet_length(deployment_id) BETWEEN 1 AND 255 AND octet_length(updated_by) <= 255)
);

CREATE TABLE IF NOT EXISTS managed_data.binding_set (
    project_id text NOT NULL,
    environment text NOT NULL,
    generation_id text NOT NULL,
    binding_digest text NOT NULL CHECK (binding_digest ~ '^sha256:[0-9a-f]{64}$'),
    binding_count bigint NOT NULL CHECK (binding_count >= 0),
    installed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (project_id, environment, generation_id),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255),
    CHECK (environment = btrim(environment) AND octet_length(environment) BETWEEN 1 AND 128),
    CHECK (generation_id = btrim(generation_id) AND octet_length(generation_id) BETWEEN 1 AND 255)
);
CREATE TABLE IF NOT EXISTS managed_data.binding (
    project_id text NOT NULL,
    environment text NOT NULL,
    generation_id text NOT NULL,
    collection_id text NOT NULL REFERENCES managed_data.collection(collection_id) ON DELETE RESTRICT,
    revision_id text NOT NULL,
    bound_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (project_id, environment, generation_id, collection_id),
    FOREIGN KEY (project_id, environment, generation_id) REFERENCES managed_data.binding_set(project_id, environment, generation_id) ON DELETE RESTRICT,
    FOREIGN KEY (collection_id, revision_id) REFERENCES managed_data.revision(collection_id, revision_id) ON DELETE RESTRICT
);

CREATE OR REPLACE FUNCTION managed_data.guard_binding_insert() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
AS $$
DECLARE p text; st text;
BEGIN
  SELECT c.project_id, r.status INTO p, st
    FROM managed_data.collection c JOIN managed_data.revision r ON r.collection_id = c.collection_id
   WHERE c.collection_id = NEW.collection_id AND r.revision_id = NEW.revision_id;
  IF p IS NULL OR p <> NEW.project_id OR st <> 'ready' THEN RAISE EXCEPTION 'binding requires a ready revision in the same project and collection'; END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS binding_insert_guard ON managed_data.binding;
CREATE TRIGGER binding_insert_guard BEFORE INSERT ON managed_data.binding FOR EACH ROW EXECUTE FUNCTION managed_data.guard_binding_insert();

CREATE OR REPLACE FUNCTION managed_data.publish_binding_set(p_project text, p_environment text, p_generation text, p_digest text, p_count bigint, p_bindings jsonb)
RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, managed_data AS $$
DECLARE payload text; computed text; existing_digest text; existing_count bigint; total_count bigint; distinct_count bigint; cid text; rid text; st text; cp text;
BEGIN
  IF jsonb_typeof(p_bindings) <> 'array' OR p_count < 0 OR jsonb_array_length(p_bindings) <> p_count THEN RAISE EXCEPTION 'binding set count is invalid'; END IF;
  SELECT string_agg(x.cid || chr(31) || x.rid || chr(31), '' ORDER BY x.cid), count(*), count(DISTINCT x.cid) INTO payload, total_count, distinct_count
    FROM (SELECT value->>'collection_id' cid, value->>'revision_id' rid FROM jsonb_array_elements(p_bindings)) x;
  IF total_count <> p_count OR distinct_count <> p_count THEN RAISE EXCEPTION 'binding set contains duplicate or invalid collections'; END IF;
  computed := 'sha256:' || encode(digest(convert_to(coalesce(payload,''),'UTF8'),'sha256'),'hex');
  IF computed <> p_digest THEN RAISE EXCEPTION 'binding digest does not match rows'; END IF;
  IF EXISTS (SELECT 1 FROM managed_data.binding_set WHERE project_id=p_project AND environment=p_environment AND generation_id=p_generation) THEN
    SELECT binding_digest,binding_count INTO existing_digest,existing_count FROM managed_data.binding_set WHERE project_id=p_project AND environment=p_environment AND generation_id=p_generation;
    IF existing_digest <> p_digest OR existing_count <> p_count THEN RAISE EXCEPTION 'binding set conflicts with immutable generation evidence'; END IF;
    RETURN;
  END IF;
  FOR cid, rid IN SELECT value->>'collection_id', value->>'revision_id' FROM jsonb_array_elements(p_bindings) LOOP
    SELECT c.project_id, r.status INTO cp, st FROM managed_data.collection c JOIN managed_data.revision r ON r.collection_id=c.collection_id WHERE c.collection_id=cid AND r.revision_id=rid;
    IF cp IS DISTINCT FROM p_project OR st IS DISTINCT FROM 'ready' THEN RAISE EXCEPTION 'binding requires ready revision in same project'; END IF;
  END LOOP;
  INSERT INTO managed_data.binding_set(project_id,environment,generation_id,binding_digest,binding_count) VALUES(p_project,p_environment,p_generation,p_digest,p_count);
  INSERT INTO managed_data.binding(project_id,environment,generation_id,collection_id,revision_id)
    SELECT p_project,p_environment,p_generation,value->>'collection_id',value->>'revision_id' FROM jsonb_array_elements(p_bindings);
END $$;

CREATE TABLE IF NOT EXISTS managed_data.lease (
    lease_key text PRIMARY KEY,
    owner_id text NOT NULL,
    fencing_epoch bigint NOT NULL CHECK (fencing_epoch > 0),
    state text NOT NULL DEFAULT 'held' CHECK (state IN ('held','released')),
    expires_at timestamptz NOT NULL,
    acquired_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    released_at timestamptz,
    CHECK (octet_length(lease_key) BETWEEN 1 AND 255 AND octet_length(owner_id) BETWEEN 1 AND 255),
    CHECK ((state='released') = (released_at IS NOT NULL))
);
CREATE TABLE IF NOT EXISTS managed_data.retention_root (
    root_id text PRIMARY KEY,
    project_id text NOT NULL,
    environment text NOT NULL,
    revision_id text,
    state text NOT NULL CHECK (state IN ('live','retiring','expired')),
    evidence jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    -- DuckLake snapshot retention/root ownership lives in the DuckLake
    -- capability.  This table intentionally admits only managed-data
    -- revision roots, avoiding a duplicate cross-database snapshot authority.
    CHECK (revision_id IS NOT NULL),
    CHECK (octet_length(root_id) BETWEEN 1 AND 255 AND octet_length(project_id) BETWEEN 1 AND 255 AND octet_length(environment) BETWEEN 1 AND 128),
    CHECK (octet_length(revision_id) BETWEEN 1 AND 255),
    CHECK (jsonb_typeof(evidence) = 'object' AND evidence <> '{}'::jsonb AND octet_length(evidence::text) <= 65536)
);

CREATE OR REPLACE FUNCTION managed_data.guard_lease_insert() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
AS $$
BEGIN
  IF NEW.state <> 'held' OR NEW.fencing_epoch <> 1 OR NEW.released_at IS NOT NULL
     OR NEW.expires_at <= clock_timestamp() OR NEW.expires_at > clock_timestamp()+interval '24 hours' THEN
    RAISE EXCEPTION 'lease must begin as a held epoch-one lease within the DB-clock bound';
  END IF;
  NEW.acquired_at := clock_timestamp();
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS lease_insert_guard ON managed_data.lease;
CREATE TRIGGER lease_insert_guard BEFORE INSERT ON managed_data.lease FOR EACH ROW EXECUTE FUNCTION managed_data.guard_lease_insert();

CREATE OR REPLACE FUNCTION managed_data.guard_digest_lease_insert() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
AS $$
BEGIN
  IF NEW.state <> 'held' OR NEW.fencing_epoch <> 1
     OR NEW.lease_until <= clock_timestamp() OR NEW.lease_until > clock_timestamp()+interval '24 hours' THEN
    RAISE EXCEPTION 'digest lease must begin as a held epoch-one lease within the DB-clock bound';
  END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS multipart_digest_insert_guard ON managed_data.multipart_digest_lease;
CREATE TRIGGER multipart_digest_insert_guard BEFORE INSERT ON managed_data.multipart_digest_lease FOR EACH ROW EXECUTE FUNCTION managed_data.guard_digest_lease_insert();

CREATE TABLE IF NOT EXISTS managed_data.reconciliation_evidence (
    evidence_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id text NOT NULL,
    environment text NOT NULL,
    object_key text NOT NULL,
    observed_state text NOT NULL,
    action text NOT NULL,
    evidence jsonb NOT NULL DEFAULT '{}'::jsonb,
    observed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (octet_length(object_key) BETWEEN 1 AND 2048),
    CHECK (jsonb_typeof(evidence) = 'object' AND evidence <> '{}'::jsonb AND octet_length(evidence::text) <= 65536)
);

CREATE OR REPLACE FUNCTION managed_data.guard_retention_root_insert() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
AS $$
DECLARE revision_project text;
BEGIN
  IF NEW.revision_id IS NOT NULL THEN
    SELECT c.project_id INTO revision_project
      FROM managed_data.revision r JOIN managed_data.collection c ON c.collection_id=r.collection_id
     WHERE r.revision_id=NEW.revision_id;
    IF revision_project IS NULL OR revision_project <> NEW.project_id THEN
      RAISE EXCEPTION 'retention revision must exist in the declared project';
    END IF;
  END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS retention_root_insert_guard ON managed_data.retention_root;
CREATE TRIGGER retention_root_insert_guard BEFORE INSERT ON managed_data.retention_root FOR EACH ROW EXECUTE FUNCTION managed_data.guard_retention_root_insert();

-- State transitions are enforced in the database as well as in the Go port,
-- so a compromised runtime client cannot resurrect terminal protocol rows or
-- decrease upload progress.
CREATE OR REPLACE FUNCTION managed_data.guard_upload_transition() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
AS $$
BEGIN
  IF NEW.upload_id <> OLD.upload_id OR NEW.collection_id <> OLD.collection_id
     OR NEW.base_revision_id IS DISTINCT FROM OLD.base_revision_id OR NEW.manifest IS DISTINCT FROM OLD.manifest OR NEW.expected_file_count <> OLD.expected_file_count
     OR NEW.expected_size_bytes <> OLD.expected_size_bytes OR NEW.storage_backend <> OLD.storage_backend
     OR NEW.staging_prefix <> OLD.staging_prefix OR NEW.created_by <> OLD.created_by
     OR NEW.expires_at <> OLD.expires_at OR NEW.request_digest <> OLD.request_digest OR NEW.created_at <> OLD.created_at THEN
    RAISE EXCEPTION 'upload identity and manifest are immutable';
  END IF;
  IF NEW.uploaded_file_count < OLD.uploaded_file_count OR NEW.uploaded_size_bytes < OLD.uploaded_size_bytes THEN
    RAISE EXCEPTION 'upload progress cannot decrease';
  END IF;
  IF OLD.status IN ('complete','aborted','expired','failed') THEN
    -- Terminal protocol rows are immutable.  The sole exception is the
    -- one-way cleanup marker, and even that is accepted only through the
    -- SECURITY DEFINER maintenance capability below.
    IF NEW.status <> OLD.status OR NEW.uploaded_file_count <> OLD.uploaded_file_count
       OR NEW.uploaded_size_bytes <> OLD.uploaded_size_bytes OR NEW.revision_id IS DISTINCT FROM OLD.revision_id
       OR NEW.completed_at IS DISTINCT FROM OLD.completed_at OR NEW.error IS DISTINCT FROM OLD.error
       OR NEW.completion_digest <> OLD.completion_digest THEN
      RAISE EXCEPTION 'terminal upload is immutable';
    END IF;
    IF NEW.cleanup_completed_at IS DISTINCT FROM OLD.cleanup_completed_at THEN
      IF OLD.cleanup_completed_at IS NOT NULL OR NEW.cleanup_completed_at IS NULL
         OR current_setting('managed_data.maintenance', true) <> 'on' OR current_user = session_user THEN
        RAISE EXCEPTION 'cleanup evidence requires bounded maintenance';
      END IF;
    END IF;
    NEW.updated_at := clock_timestamp();
    RETURN NEW;
  END IF;
  IF OLD.status = 'open' AND NEW.status NOT IN ('open','committing','aborted','expired') THEN RAISE EXCEPTION 'invalid upload transition'; END IF;
  IF OLD.status = 'committing' AND NEW.status NOT IN ('committing','complete','failed') THEN RAISE EXCEPTION 'invalid upload transition'; END IF;
  IF OLD.status IN ('complete','aborted','expired','failed') AND NEW.status <> OLD.status THEN RAISE EXCEPTION 'terminal upload cannot transition'; END IF;
  IF OLD.status = 'complete' AND NEW.completion_digest <> OLD.completion_digest THEN RAISE EXCEPTION 'completion identity is immutable'; END IF;
  IF NEW.cleanup_completed_at IS DISTINCT FROM OLD.cleanup_completed_at AND (current_setting('managed_data.maintenance', true) <> 'on' OR current_user = session_user) THEN RAISE EXCEPTION 'cleanup evidence requires bounded maintenance'; END IF;
  IF NEW.status <> 'failed' AND NEW.error IS DISTINCT FROM OLD.error THEN RAISE EXCEPTION 'upload error is only set by failed transition'; END IF;
  IF NEW.status <> 'complete' AND NEW.revision_id IS DISTINCT FROM OLD.revision_id THEN RAISE EXCEPTION 'revision binding requires completion'; END IF;
  IF NEW.status <> 'complete' AND NEW.completed_at IS DISTINCT FROM OLD.completed_at THEN RAISE EXCEPTION 'completion timestamp requires completion'; END IF;
  IF (NEW.status = 'committing' OR NEW.status = 'complete') AND EXISTS (
       SELECT 1 FROM managed_data.multipart_upload m
        WHERE m.upload_id=NEW.upload_id AND m.status NOT IN ('completed','aborted','failed')) THEN
    RAISE EXCEPTION 'upload cannot finalize while multipart children are nonterminal';
  END IF;
  IF NEW.status = 'complete' THEN
    IF NEW.revision_id IS NULL OR NEW.completion_digest = '' OR NEW.uploaded_file_count <> NEW.expected_file_count OR NEW.uploaded_size_bytes <> NEW.expected_size_bytes OR NEW.completed_at IS NULL THEN
      RAISE EXCEPTION 'completed upload requires ready revision, completion identity and exact progress';
    END IF;
    IF NOT EXISTS (
      SELECT 1 FROM managed_data.revision r
       WHERE r.revision_id=NEW.revision_id AND r.collection_id=NEW.collection_id AND r.status='ready'
         AND r.manifest IS NOT DISTINCT FROM NEW.manifest
         AND r.file_count=NEW.expected_file_count AND r.size_bytes=NEW.expected_size_bytes
         AND r.digest=NEW.manifest_digest
    ) THEN
      RAISE EXCEPTION 'completed upload requires matching ready revision manifest';
    END IF;
  END IF;
  NEW.updated_at := clock_timestamp();
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS upload_transition_guard ON managed_data.upload_session;
CREATE TRIGGER upload_transition_guard BEFORE UPDATE ON managed_data.upload_session FOR EACH ROW EXECUTE FUNCTION managed_data.guard_upload_transition();

CREATE OR REPLACE FUNCTION managed_data.guard_collection_transition() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
AS $$
BEGIN
  IF NEW.collection_id <> OLD.collection_id OR NEW.project_id <> OLD.project_id OR NEW.connection_id <> OLD.connection_id
     OR NEW.name <> OLD.name OR NEW.description <> OLD.description OR NEW.created_by <> OLD.created_by
     OR NEW.request_digest <> OLD.request_digest OR NEW.created_at <> OLD.created_at THEN
    RAISE EXCEPTION 'collection identity and authored metadata are immutable';
  END IF;
  IF OLD.status = 'archived' THEN
    IF NEW.status <> 'archived' OR NEW.archived_at IS DISTINCT FROM OLD.archived_at THEN RAISE EXCEPTION 'archived collection is immutable'; END IF;
  ELSIF NEW.status = 'active' THEN
    IF NEW.archived_at IS DISTINCT FROM OLD.archived_at THEN RAISE EXCEPTION 'active collection archive timestamp is immutable'; END IF;
  ELSIF NEW.status = 'archived' THEN
    IF OLD.archived_at IS NOT NULL THEN RAISE EXCEPTION 'collection archive timestamp is already set'; END IF;
    NEW.archived_at := clock_timestamp();
  ELSE
    RAISE EXCEPTION 'invalid collection transition';
  END IF;
  NEW.updated_at := clock_timestamp(); RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS collection_transition_guard ON managed_data.collection;
CREATE TRIGGER collection_transition_guard BEFORE UPDATE ON managed_data.collection FOR EACH ROW EXECUTE FUNCTION managed_data.guard_collection_transition();

CREATE OR REPLACE FUNCTION managed_data.guard_pointer_generation() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
AS $$
BEGIN
  IF NEW.collection_id <> OLD.collection_id OR NEW.environment <> OLD.environment THEN RAISE EXCEPTION 'pointer scope is immutable'; END IF;
  IF NEW.generation < OLD.generation THEN RAISE EXCEPTION 'pointer generation cannot decrease'; END IF;
  IF NEW.generation = OLD.generation AND (NEW.revision_id, NEW.revision_digest, NEW.deployment_id, NEW.updated_by) IS DISTINCT FROM (OLD.revision_id, OLD.revision_digest, OLD.deployment_id, OLD.updated_by) THEN
    RAISE EXCEPTION 'pointer evidence cannot change at the same generation';
  END IF;
  NEW.updated_at := clock_timestamp(); RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS pointer_generation_guard ON managed_data.environment_pointer;
CREATE TRIGGER pointer_generation_guard BEFORE UPDATE ON managed_data.environment_pointer FOR EACH ROW EXECUTE FUNCTION managed_data.guard_pointer_generation();
CREATE OR REPLACE FUNCTION managed_data.guard_pointer_revision() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
AS $$
DECLARE st text;
BEGIN
  SELECT status INTO st FROM managed_data.revision WHERE collection_id=NEW.collection_id AND revision_id=NEW.revision_id AND digest=NEW.revision_digest;
  IF st IS DISTINCT FROM 'ready' THEN RAISE EXCEPTION 'environment pointer requires matching ready revision'; END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS pointer_revision_guard ON managed_data.environment_pointer;
CREATE TRIGGER pointer_revision_guard BEFORE INSERT OR UPDATE ON managed_data.environment_pointer FOR EACH ROW EXECUTE FUNCTION managed_data.guard_pointer_revision();

-- Revisions are admitted in two phases. Identity and manifest fields are
-- immutable; only pending -> ready/failed is allowed. Files may be inserted
-- while pending, but never updated or deleted.
CREATE OR REPLACE FUNCTION managed_data.guard_revision_lifecycle() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
AS $$
DECLARE file_count bigint; total_size bigint;
BEGIN
  IF NEW.revision_id <> OLD.revision_id OR NEW.collection_id <> OLD.collection_id OR NEW.sequence <> OLD.sequence
     OR NEW.digest <> OLD.digest OR NEW.manifest IS DISTINCT FROM OLD.manifest OR NEW.file_count <> OLD.file_count
     OR NEW.size_bytes <> OLD.size_bytes OR NEW.created_by <> OLD.created_by OR NEW.created_at <> OLD.created_at THEN
    RAISE EXCEPTION 'revision identity and admitted metadata are immutable';
  END IF;
  IF OLD.status IN ('ready','failed') THEN RAISE EXCEPTION 'terminal revision is immutable'; END IF;
  IF OLD.status = 'pending' AND NEW.status NOT IN ('pending','ready','failed') THEN RAISE EXCEPTION 'invalid revision transition'; END IF;
  IF OLD.status IN ('ready','failed') AND NEW.status <> OLD.status THEN RAISE EXCEPTION 'terminal revision cannot transition'; END IF;
  IF NEW.status = 'ready' AND NEW.ready_at IS NULL THEN RAISE EXCEPTION 'ready revision requires admission timestamp'; END IF;
  IF NEW.status = 'failed' AND octet_length(NEW.error) = 0 THEN RAISE EXCEPTION 'failed revision requires error'; END IF;
  IF NEW.status = 'ready' THEN
    IF jsonb_typeof(NEW.manifest->'files') <> 'array' THEN
      RAISE EXCEPTION 'ready revision manifest must contain a files array';
    END IF;
    SELECT count(*), COALESCE(sum(size_bytes),0) INTO file_count,total_size FROM managed_data.revision_file WHERE revision_id=NEW.revision_id;
    IF file_count <> NEW.file_count OR total_size <> NEW.size_bytes THEN RAISE EXCEPTION 'ready revision file count or size does not match manifest'; END IF;
    IF file_count <> COALESCE(jsonb_array_length(NEW.manifest->'files'),0) THEN
      RAISE EXCEPTION 'ready revision file count does not match manifest files';
    END IF;
    IF EXISTS (
      SELECT 1
        FROM jsonb_to_recordset(COALESCE(NEW.manifest->'files','[]'::jsonb)) AS mf(path text, size bigint, sha256 text)
        LEFT JOIN managed_data.revision_file rf
          ON rf.revision_id=NEW.revision_id AND rf.logical_path=mf.path
       WHERE mf.path IS NULL OR mf.size IS NULL OR mf.sha256 IS NULL
          OR rf.revision_id IS NULL OR rf.size_bytes <> mf.size OR rf.sha256 <> mf.sha256
    ) THEN
      RAISE EXCEPTION 'ready revision file identity does not match manifest';
    END IF;
  END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS revision_lifecycle_guard ON managed_data.revision;
CREATE TRIGGER revision_lifecycle_guard BEFORE UPDATE ON managed_data.revision FOR EACH ROW EXECUTE FUNCTION managed_data.guard_revision_lifecycle();

CREATE OR REPLACE FUNCTION managed_data.guard_revision_insert() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
AS $$
BEGIN
  IF NEW.status <> 'pending' OR NEW.ready_at IS NOT NULL OR NEW.error <> '' THEN
    RAISE EXCEPTION 'revision must be admitted in pending state';
  END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS revision_insert_guard ON managed_data.revision;
CREATE TRIGGER revision_insert_guard BEFORE INSERT ON managed_data.revision FOR EACH ROW EXECUTE FUNCTION managed_data.guard_revision_insert();

CREATE OR REPLACE FUNCTION managed_data.guard_revision_file() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
AS $$
DECLARE st text;
BEGIN
  IF TG_OP <> 'INSERT' THEN RAISE EXCEPTION 'revision files are immutable'; END IF;
  SELECT status INTO st FROM managed_data.revision WHERE revision_id = NEW.revision_id;
  IF st IS DISTINCT FROM 'pending' THEN RAISE EXCEPTION 'revision files may only be inserted while revision is pending'; END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS revision_file_guard ON managed_data.revision_file;
CREATE TRIGGER revision_file_guard BEFORE INSERT OR UPDATE OR DELETE ON managed_data.revision_file FOR EACH ROW EXECUTE FUNCTION managed_data.guard_revision_file();

CREATE OR REPLACE FUNCTION managed_data.guard_multipart_part() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
AS $$
DECLARE st text; ust text; expiry timestamptz;
BEGIN
  IF TG_OP = 'UPDATE' THEN RAISE EXCEPTION 'multipart part rows are immutable'; END IF;
  IF TG_OP = 'DELETE' THEN
    IF current_setting('managed_data.maintenance', true) <> 'on' OR current_user = session_user THEN RAISE EXCEPTION 'multipart part deletes require bounded maintenance'; END IF;
    RETURN OLD;
  END IF;
  SELECT m.status, s.status, s.expires_at INTO st, ust, expiry
    FROM managed_data.multipart_upload m JOIN managed_data.upload_session s ON s.upload_id=m.upload_id
   WHERE m.multipart_id = NEW.multipart_id;
  IF st IS DISTINCT FROM 'open' OR ust IS DISTINCT FROM 'open' OR expiry <= clock_timestamp() THEN RAISE EXCEPTION 'multipart parts require an open, unexpired upload'; END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS multipart_part_guard ON managed_data.multipart_part;
CREATE TRIGGER multipart_part_guard BEFORE INSERT OR UPDATE OR DELETE ON managed_data.multipart_part FOR EACH ROW EXECUTE FUNCTION managed_data.guard_multipart_part();

CREATE OR REPLACE FUNCTION managed_data.guard_multipart_upload() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
AS $$
BEGIN
  IF NEW.multipart_id <> OLD.multipart_id OR NEW.upload_id <> OLD.upload_id OR NEW.logical_path <> OLD.logical_path
     OR NEW.sha256 <> OLD.sha256 OR NEW.size_bytes <> OLD.size_bytes OR NEW.created_at <> OLD.created_at
     OR NEW.idempotency_identity <> OLD.idempotency_identity THEN RAISE EXCEPTION 'multipart upload identity is immutable'; END IF;
  IF (NEW.object_key <> OLD.object_key OR NEW.provider_upload_id <> OLD.provider_upload_id OR NEW.existing <> OLD.existing)
     AND OLD.status <> 'creating' THEN RAISE EXCEPTION 'multipart object identity is immutable after initialization'; END IF;
  IF (NEW.completion_identity <> OLD.completion_identity OR NEW.completion_request_hash <> OLD.completion_request_hash)
     AND NOT (OLD.status='open' AND NEW.status='completing') THEN RAISE EXCEPTION 'multipart completion identity is immutable'; END IF;
  IF NEW.abort_identity <> OLD.abort_identity AND NOT (NEW.status='aborting' AND OLD.status IN ('creating','open','failed')) THEN RAISE EXCEPTION 'multipart abort identity is immutable'; END IF;
  IF NEW.completed_at IS DISTINCT FROM OLD.completed_at AND NOT (NEW.status='completed' AND OLD.status IN ('creating','completing')) THEN RAISE EXCEPTION 'multipart completion timestamp is immutable'; END IF;
  IF NEW.aborted_at IS DISTINCT FROM OLD.aborted_at AND NOT (NEW.status='aborted' AND OLD.status='aborting') THEN RAISE EXCEPTION 'multipart abort timestamp is immutable'; END IF;
  IF NEW.error IS DISTINCT FROM OLD.error AND NOT (NEW.status='failed' AND OLD.status IN ('creating','open','completing')) THEN RAISE EXCEPTION 'multipart error is immutable'; END IF;
  IF NEW.status = 'completed' AND NOT NEW.existing AND OLD.status <> 'completing' THEN
    RAISE EXCEPTION 'non-existing multipart uploads require completing transition';
  END IF;
  IF NEW.status IN ('completing','completed') AND NOT NEW.existing
     AND (octet_length(NEW.completion_identity) = 0 OR octet_length(NEW.completion_request_hash) = 0) THEN
    RAISE EXCEPTION 'multipart completion requires idempotency identity and request hash';
  END IF;
  IF NEW.status IN ('completing','completed') AND NOT NEW.existing THEN
    DECLARE part_count bigint; part_size bigint;
    BEGIN
      SELECT count(*), COALESCE(sum(size_bytes),0) INTO part_count,part_size FROM managed_data.multipart_part WHERE multipart_id=NEW.multipart_id;
      IF (NEW.size_bytes > 0 AND part_count = 0) OR part_size <> NEW.size_bytes THEN
        RAISE EXCEPTION 'multipart parts do not match declared object size';
      END IF;
    END;
  END IF;
  IF OLD.status='creating' AND NEW.status NOT IN ('creating','open','completed','aborting','failed') THEN RAISE EXCEPTION 'invalid multipart transition'; END IF;
  IF OLD.status='open' AND NEW.status NOT IN ('open','completing','aborting','failed') THEN RAISE EXCEPTION 'invalid multipart transition'; END IF;
  IF OLD.status='completing' AND NEW.status NOT IN ('completing','completed','aborting','failed') THEN RAISE EXCEPTION 'invalid multipart transition'; END IF;
  IF OLD.status='aborting' AND NEW.status NOT IN ('aborting','aborted') THEN RAISE EXCEPTION 'invalid multipart transition'; END IF;
  IF OLD.status='failed' AND NEW.status NOT IN ('failed','aborting') THEN RAISE EXCEPTION 'invalid multipart transition'; END IF;
  IF OLD.status IN ('completed','aborted') AND (NEW.status <> OLD.status OR NEW.object_key<>OLD.object_key OR NEW.provider_upload_id<>OLD.provider_upload_id OR NEW.existing<>OLD.existing OR NEW.completion_identity<>OLD.completion_identity OR NEW.completion_request_hash<>OLD.completion_request_hash OR NEW.abort_identity<>OLD.abort_identity) THEN RAISE EXCEPTION 'terminal multipart upload cannot transition'; END IF;
  NEW.updated_at := clock_timestamp();
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS multipart_upload_guard ON managed_data.multipart_upload;
CREATE TRIGGER multipart_upload_guard BEFORE UPDATE ON managed_data.multipart_upload FOR EACH ROW EXECUTE FUNCTION managed_data.guard_multipart_upload();
CREATE OR REPLACE FUNCTION managed_data.guard_multipart_upload_insert() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
AS $$
DECLARE st text; expiry timestamptz;
BEGIN
  IF NEW.status <> 'creating' OR NEW.existing OR NEW.object_key <> '' OR NEW.provider_upload_id <> ''
     OR NEW.completion_identity <> '' OR NEW.completion_request_hash <> '' OR NEW.abort_identity <> ''
     OR NEW.completed_at IS NOT NULL OR NEW.aborted_at IS NOT NULL OR NEW.error <> '' THEN
    RAISE EXCEPTION 'multipart upload must begin in canonical creating state';
  END IF;
  NEW.created_at := clock_timestamp();
  NEW.updated_at := NEW.created_at;
  SELECT status, expires_at INTO st, expiry FROM managed_data.upload_session WHERE upload_id=NEW.upload_id;
  IF st IS DISTINCT FROM 'open' OR expiry <= clock_timestamp() THEN RAISE EXCEPTION 'multipart upload requires an open, unexpired upload'; END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS multipart_upload_insert_guard ON managed_data.multipart_upload;
CREATE TRIGGER multipart_upload_insert_guard BEFORE INSERT ON managed_data.multipart_upload FOR EACH ROW EXECUTE FUNCTION managed_data.guard_multipart_upload_insert();

CREATE OR REPLACE FUNCTION managed_data.guard_retention_root() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
AS $$
BEGIN
  IF NEW.root_id <> OLD.root_id OR NEW.project_id <> OLD.project_id OR NEW.environment <> OLD.environment
     OR NEW.revision_id IS DISTINCT FROM OLD.revision_id
     OR NEW.created_at <> OLD.created_at OR NEW.evidence IS DISTINCT FROM OLD.evidence THEN RAISE EXCEPTION 'retention root identity is immutable'; END IF;
  IF OLD.state='live' AND NEW.state NOT IN ('live','retiring') THEN RAISE EXCEPTION 'invalid retention transition'; END IF;
  IF OLD.state='retiring' AND NEW.state NOT IN ('retiring','expired') THEN RAISE EXCEPTION 'invalid retention transition'; END IF;
  IF OLD.state='expired' AND NEW.state <> 'expired' THEN RAISE EXCEPTION 'expired retention root cannot transition'; END IF;
  NEW.updated_at := clock_timestamp(); RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS retention_root_guard ON managed_data.retention_root;
CREATE TRIGGER retention_root_guard BEFORE UPDATE ON managed_data.retention_root FOR EACH ROW EXECUTE FUNCTION managed_data.guard_retention_root();
CREATE OR REPLACE FUNCTION managed_data.reject_append_only_update() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
AS $$ BEGIN RAISE EXCEPTION 'append-only evidence cannot be mutated'; END $$;
DROP TRIGGER IF EXISTS reconciliation_evidence_guard ON managed_data.reconciliation_evidence;
CREATE TRIGGER reconciliation_evidence_guard BEFORE UPDATE OR DELETE ON managed_data.reconciliation_evidence FOR EACH ROW EXECUTE FUNCTION managed_data.reject_append_only_update();

CREATE OR REPLACE FUNCTION managed_data.guard_lease_fence() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
AS $$ BEGIN
  IF NEW.lease_key <> OLD.lease_key OR NEW.acquired_at <> OLD.acquired_at THEN RAISE EXCEPTION 'lease identity is immutable'; END IF;
  IF NEW.fencing_epoch < OLD.fencing_epoch THEN RAISE EXCEPTION 'lease fencing epoch cannot decrease'; END IF;
  IF NEW.fencing_epoch = OLD.fencing_epoch AND NEW.owner_id <> OLD.owner_id THEN RAISE EXCEPTION 'lease owner cannot change without a new fencing epoch'; END IF;
  IF OLD.state='released' AND NEW.state='held' AND NEW.fencing_epoch <= OLD.fencing_epoch THEN RAISE EXCEPTION 'released lease cannot be resurrected without a new fence'; END IF;
  IF OLD.state='held' AND NEW.state NOT IN ('held','released') THEN RAISE EXCEPTION 'invalid lease state transition'; END IF;
  IF NEW.released_at IS DISTINCT FROM OLD.released_at AND NOT ((OLD.state='held' AND NEW.state='released') OR (OLD.state='released' AND NEW.state='held')) THEN RAISE EXCEPTION 'lease release timestamp is immutable'; END IF;
  IF NEW.state='held' AND NEW.released_at IS NOT NULL THEN RAISE EXCEPTION 'held lease cannot have release timestamp'; END IF;
  IF NEW.state='held' AND NEW.expires_at <= clock_timestamp() THEN RAISE EXCEPTION 'held lease expiry must remain in the future'; END IF;
  IF NEW.state='held' AND NEW.expires_at < OLD.expires_at THEN RAISE EXCEPTION 'lease expiry cannot be shortened'; END IF;
  IF NEW.state='held' AND NEW.expires_at > clock_timestamp()+interval '24 hours' THEN RAISE EXCEPTION 'lease expiry exceeds maximum duration'; END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS lease_fence_guard ON managed_data.lease;
CREATE TRIGGER lease_fence_guard BEFORE UPDATE ON managed_data.lease FOR EACH ROW EXECUTE FUNCTION managed_data.guard_lease_fence();
DROP TRIGGER IF EXISTS multipart_digest_fence_guard ON managed_data.multipart_digest_lease;
CREATE OR REPLACE FUNCTION managed_data.guard_digest_lease_fence() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
AS $$ BEGIN
  IF NEW.sha256 <> OLD.sha256 THEN RAISE EXCEPTION 'digest lease identity is immutable'; END IF;
  IF NEW.fencing_epoch < OLD.fencing_epoch THEN RAISE EXCEPTION 'lease fencing epoch cannot decrease'; END IF;
  IF NEW.fencing_epoch = OLD.fencing_epoch AND NEW.owner_id <> OLD.owner_id THEN RAISE EXCEPTION 'lease owner cannot change without a new fencing epoch'; END IF;
  IF OLD.state='released' AND NEW.state='held' AND NEW.fencing_epoch <= OLD.fencing_epoch THEN RAISE EXCEPTION 'released digest lease cannot be resurrected without a new fence'; END IF;
  IF OLD.state='held' AND NEW.state NOT IN ('held','released') THEN RAISE EXCEPTION 'invalid digest lease state transition'; END IF;
  IF NEW.state='held' AND NEW.lease_until <= clock_timestamp() THEN RAISE EXCEPTION 'held digest lease expiry must remain in the future'; END IF;
  IF NEW.state='held' AND NEW.lease_until < OLD.lease_until AND OLD.lease_until > clock_timestamp() THEN RAISE EXCEPTION 'digest lease expiry cannot be shortened'; END IF;
  IF NEW.state='held' AND NEW.lease_until > clock_timestamp()+interval '24 hours' THEN RAISE EXCEPTION 'digest lease expiry exceeds maximum duration'; END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER multipart_digest_fence_guard BEFORE UPDATE ON managed_data.multipart_digest_lease FOR EACH ROW EXECUTE FUNCTION managed_data.guard_digest_lease_fence();

CREATE OR REPLACE FUNCTION managed_data.reject_immutable_mutation() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, managed_data
AS $$ BEGIN RAISE EXCEPTION 'managed-data immutable evidence cannot be mutated'; END $$;
DROP TRIGGER IF EXISTS revision_delete_guard ON managed_data.revision;
CREATE TRIGGER revision_delete_guard BEFORE DELETE ON managed_data.revision FOR EACH ROW EXECUTE FUNCTION managed_data.reject_immutable_mutation();
DROP TRIGGER IF EXISTS revision_immutable ON managed_data.revision;
DROP TRIGGER IF EXISTS binding_set_immutable ON managed_data.binding_set;
CREATE TRIGGER binding_set_immutable BEFORE UPDATE OR DELETE ON managed_data.binding_set FOR EACH ROW EXECUTE FUNCTION managed_data.reject_immutable_mutation();
DROP TRIGGER IF EXISTS binding_immutable ON managed_data.binding;
CREATE TRIGGER binding_immutable BEFORE UPDATE OR DELETE ON managed_data.binding FOR EACH ROW EXECUTE FUNCTION managed_data.reject_immutable_mutation();

-- Only bounded, clock-capped maintenance may delete metadata. Runtime roles
-- receive EXECUTE, never DELETE, on this function.
CREATE OR REPLACE FUNCTION managed_data.prune_upload_sessions(p_before timestamptz, p_limit integer)
RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, managed_data AS $$
DECLARE n bigint := 0; removed bigint; cutoff timestamptz; remaining integer;
BEGIN
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 1000 THEN RAISE EXCEPTION 'invalid prune bounds'; END IF;
    cutoff := LEAST(COALESCE(p_before, clock_timestamp()), clock_timestamp());
    remaining := p_limit;
    PERFORM set_config('managed_data.maintenance','on',true);
    WITH doomed AS (
      SELECT p.multipart_id, p.part_number FROM managed_data.multipart_part p
      JOIN managed_data.multipart_upload m ON m.multipart_id=p.multipart_id
      JOIN managed_data.upload_session s ON s.upload_id=m.upload_id
      WHERE p.updated_at <= cutoff AND m.status IN ('completed','aborted','failed')
        AND s.status IN ('complete','aborted','expired','failed') AND s.cleanup_completed_at IS NOT NULL
      ORDER BY p.updated_at,p.multipart_id,p.part_number FOR UPDATE OF p SKIP LOCKED LIMIT remaining
    )
    DELETE FROM managed_data.multipart_part p USING doomed d WHERE p.multipart_id=d.multipart_id AND p.part_number=d.part_number;
    GET DIAGNOSTICS removed = ROW_COUNT; n := n + removed; remaining := p_limit - n;
    IF remaining > 0 THEN
    WITH doomed AS (
      SELECT m.multipart_id FROM managed_data.multipart_upload m
      JOIN managed_data.upload_session s ON s.upload_id=m.upload_id
      WHERE m.updated_at <= cutoff AND m.status IN ('completed','aborted','failed')
        AND s.status IN ('complete','aborted','expired','failed') AND s.cleanup_completed_at IS NOT NULL
        AND NOT EXISTS (SELECT 1 FROM managed_data.multipart_part p WHERE p.multipart_id=m.multipart_id)
      ORDER BY m.updated_at,m.multipart_id FOR UPDATE OF m SKIP LOCKED LIMIT remaining
    )
    DELETE FROM managed_data.multipart_upload m USING doomed d WHERE m.multipart_id=d.multipart_id;
    GET DIAGNOSTICS removed = ROW_COUNT; n := n + removed; remaining := p_limit - n;
    END IF;
    IF remaining > 0 THEN
    WITH doomed AS (
      SELECT s.upload_id FROM managed_data.upload_session s
      WHERE s.status IN ('aborted','expired','failed') AND s.cleanup_completed_at IS NOT NULL
        AND s.updated_at <= cutoff
        AND NOT EXISTS (SELECT 1 FROM managed_data.multipart_upload m WHERE m.upload_id=s.upload_id)
      ORDER BY s.updated_at,s.upload_id FOR UPDATE SKIP LOCKED LIMIT remaining
    )
    DELETE FROM managed_data.upload_session s USING doomed d WHERE s.upload_id=d.upload_id;
    GET DIAGNOSTICS removed = ROW_COUNT; n := n + removed;
    END IF;
    RETURN n;
END $$;

CREATE OR REPLACE FUNCTION managed_data.mark_upload_cleanup(p_upload_id text)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, managed_data AS $$
BEGIN
  -- Cleanup acknowledgement is an operational capability, not a normal
  -- runtime write.  Only the dedicated maintenance role (or the schema
  -- owner/migrator, which already has administrative authority) may mint it.
  IF session_user NOT IN ('leapview_control_maintenance','leapview_control_owner','leapview_control_migrator') THEN
    RAISE EXCEPTION 'cleanup evidence requires the maintenance capability';
  END IF;
  PERFORM set_config('managed_data.maintenance','on',true);
  UPDATE managed_data.upload_session SET cleanup_completed_at=clock_timestamp(),updated_at=clock_timestamp()
    WHERE upload_id=p_upload_id AND status IN ('complete','aborted','expired','failed') AND cleanup_completed_at IS NULL;
  RETURN FOUND;
END $$;

REVOKE ALL ON SCHEMA managed_data FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA managed_data FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA managed_data FROM PUBLIC;
DO $$
DECLARE r text;
BEGIN
  FOREACH r IN ARRAY ARRAY['leapview_control_owner','leapview_control_migrator','leapview_control_maintenance','leapview_control_runtime','leapview_control_readonly','leapview_control_backup'] LOOP
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r) THEN
      EXECUTE format('GRANT USAGE ON SCHEMA managed_data TO %I', r);
      IF r IN ('leapview_control_owner','leapview_control_migrator') THEN
        EXECUTE format('GRANT ALL ON ALL TABLES IN SCHEMA managed_data TO %I', r);
        EXECUTE format('GRANT ALL ON ALL SEQUENCES IN SCHEMA managed_data TO %I', r);
        EXECUTE format('GRANT ALL ON ALL FUNCTIONS IN SCHEMA managed_data TO %I', r);
      ELSIF r = 'leapview_control_runtime' THEN
        EXECUTE 'GRANT SELECT, INSERT, UPDATE ON managed_data.collection, managed_data.upload_session, managed_data.multipart_upload, managed_data.multipart_digest_lease, managed_data.environment_pointer TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT, INSERT ON managed_data.multipart_part TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT, INSERT ON managed_data.revision_file TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT ON managed_data.binding_set, managed_data.binding TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT, INSERT, UPDATE ON managed_data.revision TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT, INSERT, UPDATE ON managed_data.lease, managed_data.retention_root TO leapview_control_runtime';
        EXECUTE 'GRANT SELECT, INSERT ON managed_data.reconciliation_evidence TO leapview_control_runtime';
        EXECUTE 'GRANT USAGE ON ALL SEQUENCES IN SCHEMA managed_data TO leapview_control_runtime';
        EXECUTE 'GRANT EXECUTE ON FUNCTION managed_data.publish_binding_set(text,text,text,text,bigint,jsonb) TO leapview_control_runtime';
      ELSIF r = 'leapview_control_maintenance' THEN
        EXECUTE 'GRANT EXECUTE ON FUNCTION managed_data.mark_upload_cleanup(text) TO leapview_control_maintenance';
        EXECUTE 'GRANT EXECUTE ON FUNCTION managed_data.prune_upload_sessions(timestamptz, integer) TO leapview_control_maintenance';
      ELSIF r = 'leapview_control_readonly' THEN
        EXECUTE 'GRANT SELECT ON managed_data.collection, managed_data.revision, managed_data.revision_file, managed_data.upload_session, managed_data.binding_set, managed_data.binding, managed_data.retention_root, managed_data.reconciliation_evidence TO leapview_control_readonly';
      ELSE
        EXECUTE 'GRANT SELECT ON ALL TABLES IN SCHEMA managed_data TO leapview_control_backup';
      END IF;
    END IF;
  END LOOP;
END $$;
