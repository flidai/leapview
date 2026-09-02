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
    -- Last audited domain-event identity.  Guarded mutation functions set it
    -- on every audited lifecycle transition; the deferred trigger below
    -- proves matching event and audit rows before a runtime transaction can
    -- commit.
    last_event_id uuid,
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

-- Application writes enter through owner-owned SECURITY DEFINER functions.
-- The runtime role deliberately has no projection-table DML privileges; these
-- entrypoints keep the state transition and its command/attempt evidence in
-- one statement while the caller retains the outer transaction (so audit and
-- domain-event adapters can still roll the entire operation back).
CREATE OR REPLACE FUNCTION dashboard.authoring_create_dashboard(
    p_project_id text, p_dashboard_id text, p_owner_principal_id uuid,
    p_slug text, p_title text, p_semantic_model text, p_visibility text,
    p_status text, p_revision_id uuid, p_revision_number bigint,
    p_document_json jsonb, p_content_hash text, p_provenance_json jsonb,
    p_created_at timestamptz, p_draft_id uuid, p_draft_provenance_json jsonb,
    p_operation_enabled boolean, p_actor_id text, p_operation_kind text,
    p_idempotency_key text, p_conversation_id text, p_tool_call_id text,
    p_request_fingerprint text, p_event_id uuid
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, dashboard
AS $$
DECLARE
    v_existing_fingerprint text;
    v_inserted bigint;
BEGIN
    IF p_status <> 'draft' THEN
        RAISE EXCEPTION 'authoring dashboard creation requires draft lifecycle status';
    END IF;
    IF p_operation_enabled THEN
        INSERT INTO dashboard.authoring_create_operations(
            project_id, actor_id, operation_kind, idempotency_key,
            conversation_id, tool_call_id, request_fingerprint,
            dashboard_id, result_revision_id, result_revision_number,
            result_content_hash
        ) VALUES (
            p_project_id, p_actor_id, p_operation_kind, p_idempotency_key,
            p_conversation_id, p_tool_call_id, p_request_fingerprint,
            p_dashboard_id, p_revision_id, p_revision_number, p_content_hash
        ) ON CONFLICT (project_id, actor_id, operation_kind, idempotency_key)
          DO NOTHING;
        GET DIAGNOSTICS v_inserted = ROW_COUNT;
        IF v_inserted = 0 THEN
            SELECT request_fingerprint INTO v_existing_fingerprint
              FROM dashboard.authoring_create_operations
             WHERE project_id = p_project_id AND actor_id = p_actor_id
               AND operation_kind = p_operation_kind
               AND idempotency_key = p_idempotency_key;
            IF v_existing_fingerprint IS DISTINCT FROM p_request_fingerprint THEN
                RAISE EXCEPTION 'authoring create operation request fingerprint differs';
            END IF;
            RETURN 0;
        END IF;
    END IF;

    INSERT INTO dashboard.authoring_dashboards(
        project_id, dashboard_id, owner_principal_id, slug, title,
        semantic_model, visibility, status, last_event_id
    ) VALUES (
        p_project_id, p_dashboard_id, p_owner_principal_id, p_slug, p_title,
        p_semantic_model, p_visibility, p_status, p_event_id
    );
    INSERT INTO dashboard.authoring_revisions(
        project_id, dashboard_id, revision_id, revision_number,
        document_json, content_hash, provenance_json, created_at
    ) VALUES (
        p_project_id, p_dashboard_id, p_revision_id, p_revision_number,
        p_document_json, p_content_hash, p_provenance_json, p_created_at
    );
    INSERT INTO dashboard.authoring_drafts(
        project_id, dashboard_id, draft_id, revision_id, revision_number,
        content_hash, provenance_json
    ) VALUES (
        p_project_id, p_dashboard_id, p_draft_id, p_revision_id,
        p_revision_number, p_content_hash, p_draft_provenance_json
    );
    RETURN 1;
END;
$$;

CREATE OR REPLACE FUNCTION dashboard.authoring_append_draft(
    p_project_id text, p_dashboard_id text, p_slug text, p_title text,
    p_semantic_model text, p_visibility text, p_status text,
    p_revision_id uuid, p_revision_number bigint, p_document_json jsonb,
    p_content_hash text, p_provenance_json jsonb, p_created_at timestamptz,
    p_draft_provenance_json jsonb, p_expected_revision_id uuid,
    p_expected_revision_number bigint, p_expected_content_hash text,
    p_command_id uuid, p_request_fingerprint text, p_action text,
    p_command_provenance_json jsonb, p_occurred_at timestamptz,
    p_event_id uuid
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, dashboard
AS $$
DECLARE
    v_existing_fingerprint text;
    v_rows bigint;
    v_status text;
BEGIN
    SELECT status INTO v_status FROM dashboard.authoring_dashboards
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
     FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'authoring dashboard was not found'; END IF;
    SELECT request_fingerprint INTO v_existing_fingerprint
      FROM dashboard.authoring_commands
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
       AND command_id = p_command_id;
    IF FOUND THEN
        IF v_existing_fingerprint IS DISTINCT FROM p_request_fingerprint THEN
            RAISE EXCEPTION 'authoring command request fingerprint differs';
        END IF;
        RETURN 0;
    END IF;
    IF p_action <> 'edit' OR p_status IS DISTINCT FROM v_status OR v_status = 'archived' THEN
        RAISE EXCEPTION 'authoring draft append does not match lifecycle state';
    END IF;

    INSERT INTO dashboard.authoring_revisions(
        project_id, dashboard_id, revision_id, revision_number,
        document_json, content_hash, provenance_json, created_at
    ) VALUES (
        p_project_id, p_dashboard_id, p_revision_id, p_revision_number,
        p_document_json, p_content_hash, p_provenance_json, p_created_at
    );
    UPDATE dashboard.authoring_dashboards
       SET slug = p_slug, title = p_title, semantic_model = p_semantic_model,
           visibility = p_visibility, status = p_status,
           last_event_id = p_event_id,
           updated_at = clock_timestamp()
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id;
    UPDATE dashboard.authoring_drafts
       SET revision_id = p_revision_id, revision_number = p_revision_number,
           content_hash = p_content_hash,
           provenance_json = p_draft_provenance_json,
           updated_at = clock_timestamp()
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
       AND revision_id = p_expected_revision_id
       AND revision_number = p_expected_revision_number
       AND content_hash = p_expected_content_hash;
    GET DIAGNOSTICS v_rows = ROW_COUNT;
    IF v_rows <> 1 THEN RAISE EXCEPTION 'authoring draft compare-and-swap conflict'; END IF;

    INSERT INTO dashboard.authoring_commands(
        project_id, dashboard_id, command_id, request_fingerprint, action,
        provenance_json, occurred_at, result_revision_id,
        result_revision_number, result_content_hash
    ) VALUES (
        p_project_id, p_dashboard_id, p_command_id, p_request_fingerprint,
        p_action, p_command_provenance_json, p_occurred_at,
        p_revision_id, p_revision_number, p_content_hash
    );
    RETURN 1;
END;
$$;

CREATE OR REPLACE FUNCTION dashboard.authoring_publish_dashboard(
    p_project_id text, p_dashboard_id text, p_slug text, p_title text,
    p_semantic_model text, p_visibility text, p_status text,
    p_revision_id uuid, p_revision_number bigint, p_content_hash text,
    p_definition_json jsonb, p_definition_hash text,
    p_compiled_semantic_model_id text, p_compiled_semantic_identity_json jsonb,
    p_compiled_at timestamptz, p_provenance_json jsonb, p_published_at timestamptz,
    p_command_id uuid, p_request_fingerprint text, p_action text,
    p_command_provenance_json jsonb, p_occurred_at timestamptz,
    p_event_id uuid
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, dashboard
AS $$
DECLARE
    v_existing_fingerprint text;
    v_existing_definition jsonb;
    v_status text;
BEGIN
    SELECT status INTO v_status FROM dashboard.authoring_dashboards
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
     FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'authoring dashboard was not found'; END IF;
    SELECT request_fingerprint INTO v_existing_fingerprint
      FROM dashboard.authoring_commands
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
       AND command_id = p_command_id;
    IF FOUND THEN
        IF v_existing_fingerprint IS DISTINCT FROM p_request_fingerprint THEN
            RAISE EXCEPTION 'authoring command request fingerprint differs';
        END IF;
        RETURN 0;
    END IF;
    IF p_action <> 'publish' OR p_status <> 'published' OR v_status NOT IN ('draft','published') THEN
        RAISE EXCEPTION 'authoring dashboard is not publishable';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM dashboard.authoring_drafts
         WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
           AND revision_id = p_revision_id
           AND revision_number = p_revision_number
           AND content_hash = p_content_hash
    ) THEN
        RAISE EXCEPTION 'authoring publish compare-and-swap conflict';
    END IF;
    INSERT INTO dashboard.authoring_compiled_revisions(
        project_id, dashboard_id, revision_id, revision_number, content_hash,
        definition_json, definition_hash, semantic_model_id,
        semantic_identity_json, compiled_at
    ) VALUES (
        p_project_id, p_dashboard_id, p_revision_id, p_revision_number,
        p_content_hash, p_definition_json, p_definition_hash,
        p_compiled_semantic_model_id, p_compiled_semantic_identity_json,
        p_compiled_at
    ) ON CONFLICT DO NOTHING;
    SELECT definition_json INTO v_existing_definition
      FROM dashboard.authoring_compiled_revisions
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
       AND revision_id = p_revision_id AND revision_number = p_revision_number
       AND content_hash = p_content_hash AND definition_hash = p_definition_hash
       AND semantic_model_id = p_compiled_semantic_model_id
       AND semantic_identity_json = p_compiled_semantic_identity_json;
    IF NOT FOUND OR v_existing_definition IS DISTINCT FROM p_definition_json THEN
        RAISE EXCEPTION 'authoring compiled revision identity is immutable';
    END IF;
    UPDATE dashboard.authoring_dashboards
       SET slug = p_slug, title = p_title, semantic_model = p_semantic_model,
           visibility = p_visibility, status = p_status,
           last_event_id = p_event_id,
           updated_at = clock_timestamp()
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id;
    INSERT INTO dashboard.authoring_published(
        project_id, dashboard_id, revision_id, revision_number, content_hash,
        compiled_revision_id, compiled_revision_number, compiled_content_hash,
        compiled_definition_hash, compiled_semantic_model_id,
        compiled_semantic_identity_json, provenance_json, published_at
    ) VALUES (
        p_project_id, p_dashboard_id, p_revision_id, p_revision_number,
        p_content_hash, p_revision_id, p_revision_number, p_content_hash,
        p_definition_hash, p_compiled_semantic_model_id,
        p_compiled_semantic_identity_json, p_provenance_json, p_published_at
    ) ON CONFLICT (project_id, dashboard_id) DO UPDATE SET
        revision_id = excluded.revision_id, revision_number = excluded.revision_number,
        content_hash = excluded.content_hash,
        compiled_revision_id = excluded.compiled_revision_id,
        compiled_revision_number = excluded.compiled_revision_number,
        compiled_content_hash = excluded.compiled_content_hash,
        compiled_definition_hash = excluded.compiled_definition_hash,
        compiled_semantic_model_id = excluded.compiled_semantic_model_id,
        compiled_semantic_identity_json = excluded.compiled_semantic_identity_json,
        provenance_json = excluded.provenance_json,
        published_at = excluded.published_at;
    INSERT INTO dashboard.authoring_commands(
        project_id, dashboard_id, command_id, request_fingerprint, action,
        provenance_json, occurred_at, result_revision_id,
        result_revision_number, result_content_hash
    ) VALUES (
        p_project_id, p_dashboard_id, p_command_id, p_request_fingerprint,
        p_action, p_command_provenance_json, p_occurred_at,
        p_revision_id, p_revision_number, p_content_hash
    );
    RETURN 1;
END;
$$;

CREATE OR REPLACE FUNCTION dashboard.authoring_archive_dashboard(
    p_project_id text, p_dashboard_id text, p_expected_revision_id uuid,
    p_expected_revision_number bigint, p_expected_content_hash text,
    p_command_id uuid, p_request_fingerprint text, p_action text,
    p_command_provenance_json jsonb, p_occurred_at timestamptz,
    p_event_id uuid
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, dashboard
AS $$
DECLARE
    v_existing_fingerprint text;
    v_rows bigint;
BEGIN
    PERFORM 1 FROM dashboard.authoring_dashboards
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
     FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'authoring dashboard was not found'; END IF;
    SELECT request_fingerprint INTO v_existing_fingerprint
      FROM dashboard.authoring_commands
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
       AND command_id = p_command_id;
    IF FOUND THEN
        IF v_existing_fingerprint IS DISTINCT FROM p_request_fingerprint THEN
            RAISE EXCEPTION 'authoring command request fingerprint differs';
        END IF;
        RETURN 0;
    END IF;
    IF p_action <> 'archive' THEN
        RAISE EXCEPTION 'authoring archive requires archive command evidence';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM dashboard.authoring_drafts
         WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
           AND revision_id = p_expected_revision_id
           AND revision_number = p_expected_revision_number
           AND content_hash = p_expected_content_hash
    ) AND NOT EXISTS (
        SELECT 1 FROM dashboard.authoring_published
         WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
           AND revision_id = p_expected_revision_id
           AND revision_number = p_expected_revision_number
           AND content_hash = p_expected_content_hash
    ) THEN
        RAISE EXCEPTION 'authoring archive compare-and-swap conflict';
    END IF;
    UPDATE dashboard.authoring_dashboards
       SET status = 'archived', last_event_id = p_event_id, updated_at = clock_timestamp()
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
       AND status <> 'archived';
    GET DIAGNOSTICS v_rows = ROW_COUNT;
    IF v_rows <> 1 THEN RAISE EXCEPTION 'authoring dashboard lifecycle conflict'; END IF;
    INSERT INTO dashboard.authoring_commands(
        project_id, dashboard_id, command_id, request_fingerprint, action,
        provenance_json, occurred_at, result_revision_id,
        result_revision_number, result_content_hash
    ) VALUES (
        p_project_id, p_dashboard_id, p_command_id, p_request_fingerprint,
        p_action, p_command_provenance_json, p_occurred_at,
        p_expected_revision_id, p_expected_revision_number, p_expected_content_hash
    );
    RETURN 1;
END;
$$;

CREATE OR REPLACE FUNCTION dashboard.authoring_commit_revalidation(
    p_project_id text, p_dashboard_id text, p_revision_id uuid,
    p_revision_number bigint, p_content_hash text, p_definition_json jsonb,
    p_definition_hash text, p_semantic_model_id text,
    p_semantic_identity_json jsonb, p_compiled_at timestamptz,
    p_generation_id text, p_attempt_id uuid, p_generation_identity_json jsonb,
    p_graph_digest text, p_dependency_ids_json jsonb,
    p_authored_revision_id uuid, p_authored_revision_number bigint,
    p_authored_content_hash text, p_prior_compiled_identity_json jsonb,
    p_attempted_at timestamptz, p_prior_compiled_revision_id uuid,
    p_prior_compiled_revision_number bigint, p_prior_compiled_content_hash text,
    p_prior_compiled_definition_hash text, p_prior_compiled_semantic_model_id text
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, dashboard
AS $$
DECLARE v_rows bigint;
BEGIN
    INSERT INTO dashboard.authoring_compiled_revisions(
        project_id, dashboard_id, revision_id, revision_number, content_hash,
        definition_json, definition_hash, semantic_model_id,
        semantic_identity_json, compiled_at
    ) VALUES (
        p_project_id, p_dashboard_id, p_revision_id, p_revision_number,
        p_content_hash, p_definition_json, p_definition_hash,
        p_semantic_model_id, p_semantic_identity_json, p_compiled_at
    ) ON CONFLICT DO NOTHING;
    INSERT INTO dashboard.authoring_revalidation_attempts(
        project_id, dashboard_id, generation_id, attempt_id,
        generation_identity_json, graph_digest, dependency_ids_json,
        authored_revision_id, authored_revision_number, authored_content_hash,
        prior_compiled_identity_json, status, compiled_definition_hash,
        compiled_semantic_model_id, compiled_semantic_identity_json, attempted_at
    ) VALUES (
        p_project_id, p_dashboard_id, p_generation_id, p_attempt_id,
        p_generation_identity_json, p_graph_digest, p_dependency_ids_json,
        p_authored_revision_id, p_authored_revision_number, p_authored_content_hash,
        p_prior_compiled_identity_json, 'succeeded', p_definition_hash,
        p_semantic_model_id, p_semantic_identity_json, p_attempted_at
    );
    UPDATE dashboard.authoring_published
       SET compiled_revision_id = p_revision_id,
           compiled_revision_number = p_revision_number,
           compiled_content_hash = p_content_hash,
           compiled_definition_hash = p_definition_hash,
           compiled_semantic_model_id = p_semantic_model_id,
           compiled_semantic_identity_json = p_semantic_identity_json
     WHERE project_id = p_project_id AND dashboard_id = p_dashboard_id
       AND revision_id = p_authored_revision_id
       AND revision_number = p_authored_revision_number
       AND content_hash = p_authored_content_hash
       AND compiled_revision_id = p_prior_compiled_revision_id
       AND compiled_revision_number = p_prior_compiled_revision_number
       AND compiled_content_hash = p_prior_compiled_content_hash
       AND compiled_definition_hash = p_prior_compiled_definition_hash
       AND compiled_semantic_model_id = p_prior_compiled_semantic_model_id
       AND compiled_semantic_identity_json = p_prior_compiled_identity_json;
    GET DIAGNOSTICS v_rows = ROW_COUNT;
    IF v_rows <> 1 THEN RAISE EXCEPTION 'authoring revalidation compare-and-swap conflict'; END IF;
    RETURN 1;
END;
$$;

CREATE OR REPLACE FUNCTION dashboard.authoring_record_revalidation_failure(
    p_project_id text, p_dashboard_id text, p_generation_id text,
    p_attempt_id uuid, p_generation_identity_json jsonb, p_graph_digest text,
    p_dependency_ids_json jsonb, p_authored_revision_id uuid,
    p_authored_revision_number bigint, p_authored_content_hash text,
    p_prior_compiled_identity_json jsonb, p_error_code text,
    p_error_message text, p_attempted_at timestamptz
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, dashboard
AS $$
BEGIN
    INSERT INTO dashboard.authoring_revalidation_attempts(
        project_id, dashboard_id, generation_id, attempt_id,
        generation_identity_json, graph_digest, dependency_ids_json,
        authored_revision_id, authored_revision_number, authored_content_hash,
        prior_compiled_identity_json, status, error_code, error_message,
        attempted_at
    ) VALUES (
        p_project_id, p_dashboard_id, p_generation_id, p_attempt_id,
        p_generation_identity_json, p_graph_digest, p_dependency_ids_json,
        p_authored_revision_id, p_authored_revision_number, p_authored_content_hash,
        p_prior_compiled_identity_json, 'failed', p_error_code, p_error_message,
        p_attempted_at
    );
    RETURN 1;
END;
$$;

CREATE OR REPLACE FUNCTION dashboard.guard_authoring_dashboard_evidence()
RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, dashboard, event, audit
AS $$
DECLARE v_ok boolean;
BEGIN
    IF NEW.last_event_id IS NULL THEN
        RAISE EXCEPTION 'authoring dashboard mutation requires canonical event identity';
    END IF;
    SELECT EXISTS (
        SELECT 1
          FROM event.event_log e
          JOIN audit.audit_event a ON a.event_id = e.event_id
         WHERE e.event_id = NEW.last_event_id
           AND e.scope_id = NEW.project_id
           AND e.aggregate_type = 'dashboard_authoring'
           AND e.aggregate_id = NEW.dashboard_id
           AND e.aggregate_version > 0
           AND a.scope_id = e.scope_id
           AND a.source = 'dashboard.authoring'
           AND a.capability = 'RESOURCE_EDIT'
           AND a.outcome = 'success'
           AND a.actor_id IS NOT NULL
           AND a.resource_kind = 'dashboard'
           AND a.resource_id = e.aggregate_id
           AND a.aggregate_key = ('dashboard_authoring:' || e.scope_id || ':' || e.aggregate_id)
           AND a.aggregate_sequence = e.aggregate_version
           AND a.correlation_id IS NOT DISTINCT FROM e.correlation_id::text
           AND a.action = e.event_type
           AND a.metadata = e.payload)
       INTO v_ok;
    IF NOT v_ok THEN
        RAISE EXCEPTION 'authoring dashboard mutation requires linked canonical event and audit evidence';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS authoring_command_evidence_guard ON dashboard.authoring_commands;
DROP TRIGGER IF EXISTS authoring_create_evidence_guard ON dashboard.authoring_create_operations;
DROP TRIGGER IF EXISTS authoring_dashboard_evidence_guard ON dashboard.authoring_dashboards;
CREATE CONSTRAINT TRIGGER authoring_dashboard_evidence_guard
    AFTER INSERT OR UPDATE OF last_event_id ON dashboard.authoring_dashboards
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION dashboard.guard_authoring_dashboard_evidence();

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
REVOKE ALL ON FUNCTION dashboard.authoring_create_dashboard(text,text,uuid,text,text,text,text,text,uuid,bigint,jsonb,text,jsonb,timestamptz,uuid,jsonb,boolean,text,text,text,text,text,text,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION dashboard.authoring_append_draft(text,text,text,text,text,text,text,uuid,bigint,jsonb,text,jsonb,timestamptz,jsonb,uuid,bigint,text,uuid,text,text,jsonb,timestamptz,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION dashboard.authoring_publish_dashboard(text,text,text,text,text,text,text,uuid,bigint,text,jsonb,text,text,jsonb,timestamptz,jsonb,timestamptz,uuid,text,text,jsonb,timestamptz,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION dashboard.authoring_archive_dashboard(text,text,uuid,bigint,text,uuid,text,text,jsonb,timestamptz,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION dashboard.authoring_commit_revalidation(text,text,uuid,bigint,text,jsonb,text,text,jsonb,timestamptz,text,uuid,jsonb,text,jsonb,uuid,bigint,text,jsonb,timestamptz,uuid,bigint,text,text,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION dashboard.authoring_record_revalidation_failure(text,text,text,uuid,jsonb,text,jsonb,uuid,bigint,text,jsonb,text,text,timestamptz) FROM PUBLIC;
REVOKE ALL ON FUNCTION dashboard.guard_authoring_dashboard_evidence() FROM PUBLIC;
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_owner') THEN
 GRANT USAGE ON SCHEMA dashboard TO leapview_control_owner;
  GRANT ALL ON TABLE dashboard.authoring_dashboards, dashboard.authoring_revisions, dashboard.authoring_drafts, dashboard.authoring_compiled_revisions, dashboard.authoring_published, dashboard.authoring_commands, dashboard.authoring_create_operations, dashboard.authoring_revalidation_attempts TO leapview_control_owner;
  GRANT ALL ON FUNCTION dashboard.guard_authoring_dashboard_update(), dashboard.guard_authoring_draft_update(), dashboard.guard_authoring_published_update(), dashboard.guard_authoring_dashboard_evidence(), dashboard.authoring_create_dashboard(text,text,uuid,text,text,text,text,text,uuid,bigint,jsonb,text,jsonb,timestamptz,uuid,jsonb,boolean,text,text,text,text,text,text,uuid), dashboard.authoring_append_draft(text,text,text,text,text,text,text,uuid,bigint,jsonb,text,jsonb,timestamptz,jsonb,uuid,bigint,text,uuid,text,text,jsonb,timestamptz,uuid), dashboard.authoring_publish_dashboard(text,text,text,text,text,text,text,uuid,bigint,text,jsonb,text,text,jsonb,timestamptz,jsonb,timestamptz,uuid,text,text,jsonb,timestamptz,uuid), dashboard.authoring_archive_dashboard(text,text,uuid,bigint,text,uuid,text,text,jsonb,timestamptz,uuid), dashboard.authoring_commit_revalidation(text,text,uuid,bigint,text,jsonb,text,text,jsonb,timestamptz,text,uuid,jsonb,text,jsonb,uuid,bigint,text,jsonb,timestamptz,uuid,bigint,text,text,text), dashboard.authoring_record_revalidation_failure(text,text,text,uuid,jsonb,text,jsonb,uuid,bigint,text,jsonb,text,text,timestamptz) TO leapview_control_owner;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_migrator') THEN
 GRANT USAGE ON SCHEMA dashboard TO leapview_control_migrator;
  GRANT ALL ON TABLE dashboard.authoring_dashboards, dashboard.authoring_revisions, dashboard.authoring_drafts, dashboard.authoring_compiled_revisions, dashboard.authoring_published, dashboard.authoring_commands, dashboard.authoring_create_operations, dashboard.authoring_revalidation_attempts TO leapview_control_migrator;
  GRANT ALL ON FUNCTION dashboard.guard_authoring_dashboard_update(), dashboard.guard_authoring_draft_update(), dashboard.guard_authoring_published_update(), dashboard.guard_authoring_dashboard_evidence(), dashboard.authoring_create_dashboard(text,text,uuid,text,text,text,text,text,uuid,bigint,jsonb,text,jsonb,timestamptz,uuid,jsonb,boolean,text,text,text,text,text,text,uuid), dashboard.authoring_append_draft(text,text,text,text,text,text,text,uuid,bigint,jsonb,text,jsonb,timestamptz,jsonb,uuid,bigint,text,uuid,text,text,jsonb,timestamptz,uuid), dashboard.authoring_publish_dashboard(text,text,text,text,text,text,text,uuid,bigint,text,jsonb,text,text,jsonb,timestamptz,jsonb,timestamptz,uuid,text,text,jsonb,timestamptz,uuid), dashboard.authoring_archive_dashboard(text,text,uuid,bigint,text,uuid,text,text,jsonb,timestamptz,uuid), dashboard.authoring_commit_revalidation(text,text,uuid,bigint,text,jsonb,text,text,jsonb,timestamptz,text,uuid,jsonb,text,jsonb,uuid,bigint,text,jsonb,timestamptz,uuid,bigint,text,text,text), dashboard.authoring_record_revalidation_failure(text,text,text,uuid,jsonb,text,jsonb,uuid,bigint,text,jsonb,text,text,timestamptz) TO leapview_control_migrator;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_runtime') THEN
 GRANT USAGE ON SCHEMA dashboard TO leapview_control_runtime;
  GRANT SELECT ON dashboard.authoring_dashboards,dashboard.authoring_revisions,dashboard.authoring_drafts,dashboard.authoring_compiled_revisions,dashboard.authoring_published,dashboard.authoring_commands,dashboard.authoring_create_operations,dashboard.authoring_revalidation_attempts TO leapview_control_runtime;
  REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON dashboard.authoring_dashboards,dashboard.authoring_revisions,dashboard.authoring_drafts,dashboard.authoring_compiled_revisions,dashboard.authoring_published,dashboard.authoring_commands,dashboard.authoring_create_operations,dashboard.authoring_revalidation_attempts FROM leapview_control_runtime;
  GRANT EXECUTE ON FUNCTION dashboard.authoring_create_dashboard(text,text,uuid,text,text,text,text,text,uuid,bigint,jsonb,text,jsonb,timestamptz,uuid,jsonb,boolean,text,text,text,text,text,text,uuid) TO leapview_control_runtime;
  GRANT EXECUTE ON FUNCTION dashboard.authoring_append_draft(text,text,text,text,text,text,text,uuid,bigint,jsonb,text,jsonb,timestamptz,jsonb,uuid,bigint,text,uuid,text,text,jsonb,timestamptz,uuid) TO leapview_control_runtime;
  GRANT EXECUTE ON FUNCTION dashboard.authoring_publish_dashboard(text,text,text,text,text,text,text,uuid,bigint,text,jsonb,text,text,jsonb,timestamptz,jsonb,timestamptz,uuid,text,text,jsonb,timestamptz,uuid) TO leapview_control_runtime;
  GRANT EXECUTE ON FUNCTION dashboard.authoring_archive_dashboard(text,text,uuid,bigint,text,uuid,text, text,jsonb,timestamptz,uuid) TO leapview_control_runtime;
  GRANT EXECUTE ON FUNCTION dashboard.authoring_commit_revalidation(text,text,uuid,bigint,text,jsonb,text,text,jsonb,timestamptz,text,uuid,jsonb,text,jsonb,uuid,bigint,text,jsonb,timestamptz,uuid,bigint,text,text,text) TO leapview_control_runtime;
  GRANT EXECUTE ON FUNCTION dashboard.authoring_record_revalidation_failure(text,text,text,uuid,jsonb,text,jsonb,uuid,bigint,text,jsonb,text,text,timestamptz) TO leapview_control_runtime;
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
