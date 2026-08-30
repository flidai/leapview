-- Native PostgreSQL dashboard authoring authority.
--
-- This capability is also applied in isolation by package tests, so project
-- and access foreign keys are attached conditionally below.  The production
-- baseline creates those authorities first and therefore always installs the
-- same constraints and owner trigger.
CREATE SCHEMA IF NOT EXISTS dashboard;

CREATE TABLE IF NOT EXISTS dashboard.authoring_dashboards (
    project_id text NOT NULL,
    dashboard_id text NOT NULL,
    owner_principal_id uuid NOT NULL,
    slug text NOT NULL,
    title text NOT NULL,
    semantic_model text NOT NULL,
    visibility text NOT NULL CHECK (visibility IN ('private','restricted','organization')),
    status text NOT NULL CHECK (status IN ('draft','published','archived')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (project_id, dashboard_id),
    UNIQUE (project_id, slug),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255),
    CHECK (dashboard_id = btrim(dashboard_id) AND octet_length(dashboard_id) BETWEEN 1 AND 255),
    CHECK (slug = btrim(slug) AND octet_length(slug) BETWEEN 1 AND 128 AND slug ~ '^[a-z0-9][a-z0-9-]*$'),
    CHECK (title = btrim(title) AND octet_length(title) BETWEEN 1 AND 512),
    CHECK (semantic_model = btrim(semantic_model) AND octet_length(semantic_model) BETWEEN 1 AND 255),
    CHECK (updated_at >= created_at)
);

CREATE TABLE IF NOT EXISTS dashboard.authoring_revisions (
    project_id text NOT NULL,
    dashboard_id text NOT NULL,
    revision_id uuid NOT NULL,
    revision_number bigint NOT NULL CHECK (revision_number > 0),
    document_json jsonb NOT NULL CHECK (jsonb_typeof(document_json) = 'object' AND octet_length(document_json::text) <= 1048576),
    content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
    provenance_json jsonb NOT NULL CHECK (jsonb_typeof(provenance_json) = 'object' AND octet_length(provenance_json::text) <= 32768),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (project_id, dashboard_id, revision_id),
    UNIQUE (project_id, dashboard_id, revision_number),
    UNIQUE (project_id, dashboard_id, revision_id, revision_number, content_hash),
    FOREIGN KEY (project_id, dashboard_id) REFERENCES dashboard.authoring_dashboards(project_id, dashboard_id) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS dashboard.authoring_drafts (
    project_id text NOT NULL,
    dashboard_id text NOT NULL,
    draft_id uuid NOT NULL,
    revision_id uuid NOT NULL,
    revision_number bigint NOT NULL CHECK (revision_number > 0),
    content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
    provenance_json jsonb NOT NULL CHECK (jsonb_typeof(provenance_json) = 'object' AND octet_length(provenance_json::text) <= 32768),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (project_id, dashboard_id),
    UNIQUE (project_id, dashboard_id, draft_id),
    FOREIGN KEY (project_id, dashboard_id, revision_id, revision_number, content_hash)
        REFERENCES dashboard.authoring_revisions(project_id, dashboard_id, revision_id, revision_number, content_hash) ON DELETE RESTRICT,
    FOREIGN KEY (project_id, dashboard_id)
        REFERENCES dashboard.authoring_dashboards(project_id, dashboard_id) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS dashboard.authoring_compiled_revisions (
    project_id text NOT NULL,
    dashboard_id text NOT NULL,
    revision_id uuid NOT NULL,
    revision_number bigint NOT NULL CHECK (revision_number > 0),
    content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
    definition_json jsonb NOT NULL CHECK (jsonb_typeof(definition_json) = 'object' AND octet_length(definition_json::text) <= 1048576),
    definition_hash text NOT NULL CHECK (definition_hash ~ '^sha256:[0-9a-f]{64}$'),
    semantic_model_id text NOT NULL CHECK (semantic_model_id = btrim(semantic_model_id) AND octet_length(semantic_model_id) BETWEEN 1 AND 255),
    semantic_identity_json jsonb NOT NULL CHECK (jsonb_typeof(semantic_identity_json) = 'object' AND octet_length(semantic_identity_json::text) <= 32768),
    compiled_at timestamptz NOT NULL,
    PRIMARY KEY (project_id, dashboard_id, revision_id, revision_number, content_hash, definition_hash, semantic_model_id, semantic_identity_json),
    FOREIGN KEY (project_id, dashboard_id, revision_id, revision_number, content_hash)
        REFERENCES dashboard.authoring_revisions(project_id, dashboard_id, revision_id, revision_number, content_hash) ON DELETE RESTRICT,
    FOREIGN KEY (project_id, dashboard_id)
        REFERENCES dashboard.authoring_dashboards(project_id, dashboard_id) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS dashboard.authoring_published (
    project_id text NOT NULL,
    dashboard_id text NOT NULL,
    revision_id uuid NOT NULL,
    revision_number bigint NOT NULL CHECK (revision_number > 0),
    content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
    compiled_revision_id uuid NOT NULL,
    compiled_revision_number bigint NOT NULL CHECK (compiled_revision_number > 0),
    compiled_content_hash text NOT NULL CHECK (compiled_content_hash ~ '^sha256:[0-9a-f]{64}$'),
    compiled_definition_hash text NOT NULL CHECK (compiled_definition_hash ~ '^sha256:[0-9a-f]{64}$'),
    compiled_semantic_model_id text NOT NULL CHECK (compiled_semantic_model_id = btrim(compiled_semantic_model_id) AND octet_length(compiled_semantic_model_id) BETWEEN 1 AND 255),
    compiled_semantic_identity_json jsonb NOT NULL CHECK (jsonb_typeof(compiled_semantic_identity_json) = 'object' AND octet_length(compiled_semantic_identity_json::text) <= 32768),
    provenance_json jsonb NOT NULL CHECK (jsonb_typeof(provenance_json) = 'object' AND octet_length(provenance_json::text) <= 32768),
    published_at timestamptz NOT NULL,
    PRIMARY KEY (project_id, dashboard_id),
    FOREIGN KEY (project_id, dashboard_id, revision_id, revision_number, content_hash)
        REFERENCES dashboard.authoring_revisions(project_id, dashboard_id, revision_id, revision_number, content_hash) ON DELETE RESTRICT,
    FOREIGN KEY (project_id, dashboard_id, compiled_revision_id, compiled_revision_number, compiled_content_hash, compiled_definition_hash, compiled_semantic_model_id, compiled_semantic_identity_json)
        REFERENCES dashboard.authoring_compiled_revisions(project_id, dashboard_id, revision_id, revision_number, content_hash, definition_hash, semantic_model_id, semantic_identity_json) ON DELETE RESTRICT,
    FOREIGN KEY (project_id, dashboard_id)
        REFERENCES dashboard.authoring_dashboards(project_id, dashboard_id) ON DELETE RESTRICT,
    CHECK (revision_id = compiled_revision_id AND revision_number = compiled_revision_number AND content_hash = compiled_content_hash)
);

CREATE TABLE IF NOT EXISTS dashboard.authoring_commands (
    project_id text NOT NULL,
    dashboard_id text NOT NULL,
    command_id uuid NOT NULL,
    request_fingerprint text NOT NULL CHECK (request_fingerprint = btrim(request_fingerprint) AND octet_length(request_fingerprint) BETWEEN 1 AND 255),
    action text NOT NULL CHECK (action IN ('edit','publish','archive')),
    provenance_json jsonb NOT NULL CHECK (jsonb_typeof(provenance_json) = 'object' AND octet_length(provenance_json::text) <= 32768),
    occurred_at timestamptz NOT NULL,
    result_revision_id uuid,
    result_revision_number bigint,
    result_content_hash text,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (project_id, dashboard_id, command_id),
    FOREIGN KEY (project_id, dashboard_id) REFERENCES dashboard.authoring_dashboards(project_id, dashboard_id) ON DELETE RESTRICT,
    FOREIGN KEY (project_id, dashboard_id, result_revision_id, result_revision_number, result_content_hash)
        REFERENCES dashboard.authoring_revisions(project_id, dashboard_id, revision_id, revision_number, content_hash) ON DELETE RESTRICT,
    CHECK ((result_revision_id IS NULL AND result_revision_number IS NULL AND result_content_hash IS NULL)
        OR (result_revision_id IS NOT NULL AND result_revision_number > 0 AND result_content_hash ~ '^sha256:[0-9a-f]{64}$'))
);

CREATE TABLE IF NOT EXISTS dashboard.authoring_create_operations (
    project_id text NOT NULL,
    actor_id text NOT NULL CHECK (actor_id = btrim(actor_id) AND octet_length(actor_id) BETWEEN 1 AND 255),
    operation_kind text NOT NULL CHECK (operation_kind IN ('create','fork')),
    idempotency_key text NOT NULL CHECK (idempotency_key = btrim(idempotency_key) AND octet_length(idempotency_key) BETWEEN 1 AND 512),
    conversation_id text NOT NULL DEFAULT '' CHECK (octet_length(conversation_id) <= 512),
    tool_call_id text NOT NULL DEFAULT '' CHECK (octet_length(tool_call_id) <= 512),
    request_fingerprint text NOT NULL CHECK (request_fingerprint = btrim(request_fingerprint) AND octet_length(request_fingerprint) BETWEEN 1 AND 255),
    dashboard_id text NOT NULL,
    result_revision_id uuid NOT NULL,
    result_revision_number bigint NOT NULL CHECK (result_revision_number > 0),
    result_content_hash text NOT NULL CHECK (result_content_hash ~ '^sha256:[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (project_id, actor_id, operation_kind, idempotency_key),
    FOREIGN KEY (project_id, dashboard_id) REFERENCES dashboard.authoring_dashboards(project_id, dashboard_id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY (project_id, dashboard_id, result_revision_id, result_revision_number, result_content_hash)
        REFERENCES dashboard.authoring_revisions(project_id, dashboard_id, revision_id, revision_number, content_hash) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE IF NOT EXISTS dashboard.authoring_revalidation_attempts (
    project_id text NOT NULL,
    dashboard_id text NOT NULL,
    generation_id text NOT NULL CHECK (generation_id = btrim(generation_id) AND octet_length(generation_id) BETWEEN 1 AND 255),
    attempt_id uuid NOT NULL,
    generation_identity_json jsonb NOT NULL CHECK (jsonb_typeof(generation_identity_json) = 'object' AND octet_length(generation_identity_json::text) <= 32768),
    graph_digest text NOT NULL CHECK (graph_digest ~ '^sha256:[0-9a-f]{64}$'),
    dependency_ids_json jsonb NOT NULL CHECK (jsonb_typeof(dependency_ids_json) = 'array' AND octet_length(dependency_ids_json::text) <= 32768),
    authored_revision_id uuid NOT NULL,
    authored_revision_number bigint NOT NULL CHECK (authored_revision_number > 0),
    authored_content_hash text NOT NULL CHECK (authored_content_hash ~ '^sha256:[0-9a-f]{64}$'),
    prior_compiled_identity_json jsonb NOT NULL CHECK (jsonb_typeof(prior_compiled_identity_json) = 'object' AND octet_length(prior_compiled_identity_json::text) <= 32768),
    status text NOT NULL CHECK (status IN ('succeeded','failed')),
    error_code text CHECK (error_code IS NULL OR (error_code = btrim(error_code) AND octet_length(error_code) BETWEEN 1 AND 255)),
    error_message text CHECK (error_message IS NULL OR (error_message = btrim(error_message) AND octet_length(error_message) BETWEEN 1 AND 4096)),
    compiled_definition_hash text CHECK (compiled_definition_hash IS NULL OR compiled_definition_hash ~ '^sha256:[0-9a-f]{64}$'),
    compiled_semantic_model_id text CHECK (compiled_semantic_model_id IS NULL OR (compiled_semantic_model_id = btrim(compiled_semantic_model_id) AND octet_length(compiled_semantic_model_id) BETWEEN 1 AND 255)),
    compiled_semantic_identity_json jsonb CHECK (compiled_semantic_identity_json IS NULL OR (jsonb_typeof(compiled_semantic_identity_json) = 'object' AND octet_length(compiled_semantic_identity_json::text) <= 32768)),
    attempted_at timestamptz NOT NULL,
    PRIMARY KEY (project_id, dashboard_id, generation_id, attempt_id),
    FOREIGN KEY (project_id, dashboard_id) REFERENCES dashboard.authoring_dashboards(project_id, dashboard_id) ON DELETE RESTRICT,
    FOREIGN KEY (project_id, dashboard_id, authored_revision_id, authored_revision_number, authored_content_hash)
        REFERENCES dashboard.authoring_revisions(project_id, dashboard_id, revision_id, revision_number, content_hash) ON DELETE RESTRICT,
    FOREIGN KEY (project_id, dashboard_id, authored_revision_id, authored_revision_number, authored_content_hash, compiled_definition_hash, compiled_semantic_model_id, compiled_semantic_identity_json)
        REFERENCES dashboard.authoring_compiled_revisions(project_id, dashboard_id, revision_id, revision_number, content_hash, definition_hash, semantic_model_id, semantic_identity_json) ON DELETE RESTRICT,
    CHECK ((status = 'failed' AND error_code IS NOT NULL AND btrim(error_code) <> '' AND error_message IS NOT NULL AND btrim(error_message) <> '' AND compiled_definition_hash IS NULL AND compiled_semantic_model_id IS NULL AND compiled_semantic_identity_json IS NULL)
        OR (status = 'succeeded' AND error_code IS NULL AND error_message IS NULL AND compiled_definition_hash IS NOT NULL AND compiled_semantic_model_id IS NOT NULL AND btrim(compiled_semantic_model_id) <> '' AND compiled_semantic_identity_json IS NOT NULL AND jsonb_typeof(compiled_semantic_identity_json) = 'object'))
);

CREATE INDEX IF NOT EXISTS authoring_dashboards_project_idx ON dashboard.authoring_dashboards(project_id, semantic_model, status, visibility, slug, dashboard_id);
CREATE INDEX IF NOT EXISTS authoring_revisions_project_idx ON dashboard.authoring_revisions(project_id, dashboard_id, revision_number);
CREATE INDEX IF NOT EXISTS authoring_compiled_project_idx ON dashboard.authoring_compiled_revisions(project_id, dashboard_id, revision_number);
CREATE INDEX IF NOT EXISTS authoring_revalidation_project_idx ON dashboard.authoring_revalidation_attempts(project_id, dashboard_id, attempted_at DESC);

-- Authoring projections retain stable identities while their pointers advance.
-- Keep these invariants in the database as well as in the repository so a
-- capability role cannot rewrite a dashboard or bypass lifecycle/CAS rules
-- with an ad-hoc UPDATE.
CREATE OR REPLACE FUNCTION dashboard.guard_authoring_dashboard_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.project_id IS DISTINCT FROM NEW.project_id
       OR OLD.dashboard_id IS DISTINCT FROM NEW.dashboard_id
       OR OLD.owner_principal_id IS DISTINCT FROM NEW.owner_principal_id
       OR OLD.created_at IS DISTINCT FROM NEW.created_at THEN
        RAISE EXCEPTION 'authoring dashboard identity is immutable';
    END IF;
    IF OLD.status = 'draft' AND NEW.status NOT IN ('draft','published','archived') THEN
        RAISE EXCEPTION 'invalid authoring dashboard lifecycle transition';
    ELSIF OLD.status = 'published' AND NEW.status NOT IN ('published','archived') THEN
        RAISE EXCEPTION 'invalid authoring dashboard lifecycle transition';
    ELSIF OLD.status = 'archived' AND NEW.status <> 'archived' THEN
        RAISE EXCEPTION 'invalid authoring dashboard lifecycle transition';
    END IF;
    IF NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'authoring dashboard updated_at is monotonic';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION dashboard.guard_authoring_draft_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.project_id IS DISTINCT FROM NEW.project_id
       OR OLD.dashboard_id IS DISTINCT FROM NEW.dashboard_id
       OR OLD.draft_id IS DISTINCT FROM NEW.draft_id THEN
        RAISE EXCEPTION 'authoring draft identity is immutable';
    END IF;
    IF NEW.revision_number <> OLD.revision_number + 1 THEN
        RAISE EXCEPTION 'authoring draft revision must advance exactly one';
    END IF;
    IF NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'authoring draft updated_at is monotonic';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION dashboard.guard_authoring_published_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.project_id IS DISTINCT FROM NEW.project_id
       OR OLD.dashboard_id IS DISTINCT FROM NEW.dashboard_id THEN
        RAISE EXCEPTION 'authoring published identity is immutable';
    END IF;
    IF NEW.revision_number < OLD.revision_number
       OR NEW.compiled_revision_number < OLD.compiled_revision_number THEN
        RAISE EXCEPTION 'authoring published revision cannot move backwards';
    END IF;
    IF NEW.published_at < OLD.published_at THEN
        RAISE EXCEPTION 'authoring published timestamp is monotonic';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS authoring_dashboards_mutation ON dashboard.authoring_dashboards;
CREATE TRIGGER authoring_dashboards_mutation
    BEFORE UPDATE ON dashboard.authoring_dashboards
    FOR EACH ROW EXECUTE FUNCTION dashboard.guard_authoring_dashboard_update();
DROP TRIGGER IF EXISTS authoring_drafts_mutation ON dashboard.authoring_drafts;
CREATE TRIGGER authoring_drafts_mutation
    BEFORE UPDATE ON dashboard.authoring_drafts
    FOR EACH ROW EXECUTE FUNCTION dashboard.guard_authoring_draft_update();
DROP TRIGGER IF EXISTS authoring_published_mutation ON dashboard.authoring_published;
CREATE TRIGGER authoring_published_mutation
    BEFORE UPDATE ON dashboard.authoring_published
    FOR EACH ROW EXECUTE FUNCTION dashboard.guard_authoring_published_update();

-- Attach cross-capability authority relationships when their baseline tables
-- are present. Access owns the opaque UUID principal identity.
DO $$
BEGIN
    IF to_regclass('project.project_identity') IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'authoring_dashboards_project_fk') THEN
        ALTER TABLE dashboard.authoring_dashboards
            ADD CONSTRAINT authoring_dashboards_project_fk FOREIGN KEY (project_id)
            REFERENCES project.project_identity(project_id) ON DELETE RESTRICT;
    END IF;
    IF to_regclass('access.principal') IS NOT NULL THEN
        IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'authoring_dashboards_owner_fk') THEN
            ALTER TABLE dashboard.authoring_dashboards
                ADD CONSTRAINT authoring_dashboards_owner_fk FOREIGN KEY (owner_principal_id)
                REFERENCES access.principal(id) ON DELETE RESTRICT;
        END IF;
    END IF;
END $$;

REVOKE ALL ON SCHEMA dashboard FROM PUBLIC;
REVOKE ALL ON TABLE dashboard.authoring_dashboards, dashboard.authoring_revisions, dashboard.authoring_drafts, dashboard.authoring_compiled_revisions, dashboard.authoring_published, dashboard.authoring_commands, dashboard.authoring_create_operations, dashboard.authoring_revalidation_attempts FROM PUBLIC;
REVOKE ALL ON FUNCTION dashboard.guard_authoring_dashboard_update() FROM PUBLIC;
REVOKE ALL ON FUNCTION dashboard.guard_authoring_draft_update() FROM PUBLIC;
REVOKE ALL ON FUNCTION dashboard.guard_authoring_published_update() FROM PUBLIC;
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_owner') THEN
  GRANT USAGE ON SCHEMA dashboard TO leapview_control_owner;
  GRANT ALL ON TABLE dashboard.authoring_dashboards, dashboard.authoring_revisions, dashboard.authoring_drafts, dashboard.authoring_compiled_revisions, dashboard.authoring_published, dashboard.authoring_commands, dashboard.authoring_create_operations, dashboard.authoring_revalidation_attempts TO leapview_control_owner;
  GRANT ALL ON FUNCTION dashboard.guard_authoring_dashboard_update(), dashboard.guard_authoring_draft_update(), dashboard.guard_authoring_published_update() TO leapview_control_owner;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_migrator') THEN
  GRANT USAGE ON SCHEMA dashboard TO leapview_control_migrator;
  GRANT ALL ON TABLE dashboard.authoring_dashboards, dashboard.authoring_revisions, dashboard.authoring_drafts, dashboard.authoring_compiled_revisions, dashboard.authoring_published, dashboard.authoring_commands, dashboard.authoring_create_operations, dashboard.authoring_revalidation_attempts TO leapview_control_migrator;
  GRANT ALL ON FUNCTION dashboard.guard_authoring_dashboard_update(), dashboard.guard_authoring_draft_update(), dashboard.guard_authoring_published_update() TO leapview_control_migrator;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_runtime') THEN
  GRANT USAGE ON SCHEMA dashboard TO leapview_control_runtime;
  GRANT SELECT ON dashboard.authoring_dashboards,dashboard.authoring_drafts,dashboard.authoring_published TO leapview_control_runtime;
  GRANT INSERT (project_id,dashboard_id,owner_principal_id,slug,title,semantic_model,visibility,status)
      ON dashboard.authoring_dashboards TO leapview_control_runtime;
  GRANT UPDATE (slug,title,semantic_model,visibility,status,updated_at)
      ON dashboard.authoring_dashboards TO leapview_control_runtime;
  GRANT INSERT (project_id,dashboard_id,draft_id,revision_id,revision_number,content_hash,provenance_json)
      ON dashboard.authoring_drafts TO leapview_control_runtime;
  GRANT UPDATE (revision_id,revision_number,content_hash,provenance_json,updated_at)
      ON dashboard.authoring_drafts TO leapview_control_runtime;
  GRANT INSERT (project_id,dashboard_id,revision_id,revision_number,content_hash,compiled_revision_id,compiled_revision_number,compiled_content_hash,compiled_definition_hash,compiled_semantic_model_id,compiled_semantic_identity_json,provenance_json,published_at)
      ON dashboard.authoring_published TO leapview_control_runtime;
  GRANT UPDATE (revision_id,revision_number,content_hash,compiled_revision_id,compiled_revision_number,compiled_content_hash,compiled_definition_hash,compiled_semantic_model_id,compiled_semantic_identity_json,provenance_json,published_at)
      ON dashboard.authoring_published TO leapview_control_runtime;
  GRANT SELECT,INSERT ON dashboard.authoring_revisions,dashboard.authoring_compiled_revisions,dashboard.authoring_commands,dashboard.authoring_create_operations,dashboard.authoring_revalidation_attempts TO leapview_control_runtime;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_readonly') THEN
  GRANT USAGE ON SCHEMA dashboard TO leapview_control_readonly;
  GRANT SELECT ON dashboard.authoring_dashboards,dashboard.authoring_revisions,dashboard.authoring_drafts,dashboard.authoring_compiled_revisions,dashboard.authoring_published,dashboard.authoring_commands,dashboard.authoring_create_operations,dashboard.authoring_revalidation_attempts TO leapview_control_readonly;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_backup') THEN
  GRANT USAGE ON SCHEMA dashboard TO leapview_control_backup;
  GRANT SELECT ON dashboard.authoring_dashboards,dashboard.authoring_revisions,dashboard.authoring_drafts,dashboard.authoring_compiled_revisions,dashboard.authoring_published,dashboard.authoring_commands,dashboard.authoring_create_operations,dashboard.authoring_revalidation_attempts TO leapview_control_backup;
 END IF;
END $$;
