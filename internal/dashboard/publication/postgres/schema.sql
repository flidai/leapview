-- Native PostgreSQL dashboard publication and public stream authority.
CREATE SCHEMA IF NOT EXISTS dashboard;

CREATE TABLE IF NOT EXISTS dashboard.publications (
 id uuid PRIMARY KEY,
 project_id text NOT NULL,
 name text NOT NULL,
 public_id text NOT NULL UNIQUE,
 dashboard text NOT NULL,
 default_page text NOT NULL,
 configuration_digest text NOT NULL CHECK (configuration_digest ~ '^sha256:[0-9a-f]{64}$'),
 allowed_origins_json jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(allowed_origins_json) = 'array' AND octet_length(allowed_origins_json::text) <= 32768),
 dependency_asset_ids_json jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(dependency_asset_ids_json) = 'array' AND octet_length(dependency_asset_ids_json::text) <= 32768),
 revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
 configured boolean NOT NULL DEFAULT true,
 active_serving_state_id text,
 suspended_at timestamptz,
 suspended_by text NOT NULL DEFAULT '',
 configured_at timestamptz,
 disabled_at timestamptz,
 rotated_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(project_id,name),
 CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255),
 CHECK (name = btrim(name) AND octet_length(name) BETWEEN 1 AND 255),
 CHECK (public_id = btrim(public_id) AND octet_length(public_id) BETWEEN 1 AND 255),
 CHECK (dashboard = btrim(dashboard) AND octet_length(dashboard) BETWEEN 1 AND 512),
 CHECK (default_page = btrim(default_page) AND octet_length(default_page) BETWEEN 1 AND 255),
 CHECK (suspended_by = btrim(suspended_by) AND octet_length(suspended_by) <= 255),
 CHECK (active_serving_state_id IS NULL OR (active_serving_state_id = btrim(active_serving_state_id) AND octet_length(active_serving_state_id) BETWEEN 1 AND 255)),
 CHECK (updated_at >= created_at),
 CHECK ((configured AND active_serving_state_id IS NOT NULL AND configured_at IS NOT NULL AND disabled_at IS NULL)
     OR (NOT configured AND active_serving_state_id IS NULL AND disabled_at IS NOT NULL)),
 CHECK ((suspended_at IS NULL AND suspended_by = '') OR (suspended_at IS NOT NULL AND suspended_by <> ''))
);

CREATE TABLE IF NOT EXISTS dashboard.publication_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 publication_id uuid NOT NULL REFERENCES dashboard.publications(id) ON DELETE RESTRICT,
 domain_event_id uuid NOT NULL,
 aggregate_version bigint NOT NULL CHECK (aggregate_version > 0),
 revision bigint NOT NULL CHECK (revision > 0),
 event_type text NOT NULL CHECK (event_type IN ('dashboard_publication.configured','dashboard_publication.configuration_changed','dashboard_publication.serving_state_changed','dashboard_publication.disabled','dashboard_publication.suspended','dashboard_publication.resumed','dashboard_publication.rotated')),
 actor_id text NOT NULL DEFAULT '' CHECK (actor_id = btrim(actor_id) AND octet_length(actor_id) <= 255),
 correlation_id text NOT NULL DEFAULT '' CHECK (correlation_id = btrim(correlation_id) AND octet_length(correlation_id) <= 255),
 serving_state_id text CHECK (serving_state_id IS NULL OR (serving_state_id = btrim(serving_state_id) AND octet_length(serving_state_id) BETWEEN 1 AND 255)),
 payload_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload_json) = 'object' AND octet_length(payload_json::text) <= 65536),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE (domain_event_id),
 UNIQUE (publication_id, aggregate_version),
 UNIQUE (publication_id, revision)
);
CREATE INDEX IF NOT EXISTS publication_events_publication_idx ON dashboard.publication_events(publication_id,id DESC);
CREATE INDEX IF NOT EXISTS publication_events_publication_revision_idx ON dashboard.publication_events(publication_id,revision DESC);

CREATE TABLE IF NOT EXISTS dashboard.publication_streams (
 publication_id uuid NOT NULL,
 stream_id text NOT NULL,
 public_id text NOT NULL,
 serving_state_id text NOT NULL,
 registration_id uuid NOT NULL,
 filters_json jsonb NOT NULL CHECK (jsonb_typeof(filters_json) = 'object' AND octet_length(filters_json::text) <= 32768),
 generation bigint NOT NULL DEFAULT 1 CHECK (generation > 0),
 expires_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(publication_id,stream_id),
 FOREIGN KEY (publication_id) REFERENCES dashboard.publications(id) ON DELETE RESTRICT,
 CHECK (stream_id = btrim(stream_id) AND octet_length(stream_id) BETWEEN 1 AND 255),
 CHECK (public_id = btrim(public_id) AND octet_length(public_id) BETWEEN 1 AND 255),
 CHECK (serving_state_id = btrim(serving_state_id) AND octet_length(serving_state_id) BETWEEN 1 AND 255)
);

-- Publication identity is stable for the lifetime of a row.  Every
-- projection mutation advances its optimistic revision exactly once; public
-- identifiers may only rotate together with their rotation timestamp.
CREATE OR REPLACE FUNCTION dashboard.guard_publication_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.id IS DISTINCT FROM NEW.id
       OR OLD.project_id IS DISTINCT FROM NEW.project_id
       OR OLD.name IS DISTINCT FROM NEW.name
       OR OLD.created_at IS DISTINCT FROM NEW.created_at THEN
        RAISE EXCEPTION 'publication identity is immutable';
    END IF;
    IF NEW.revision <> OLD.revision + 1 THEN
        RAISE EXCEPTION 'publication revision must advance exactly one';
    END IF;
    IF OLD.public_id IS DISTINCT FROM NEW.public_id
       AND OLD.rotated_at IS NOT DISTINCT FROM NEW.rotated_at THEN
        RAISE EXCEPTION 'publication public_id changes require rotated_at change';
    END IF;
    IF OLD.rotated_at IS DISTINCT FROM NEW.rotated_at
       AND OLD.public_id IS NOT DISTINCT FROM NEW.public_id THEN
        RAISE EXCEPTION 'publication rotated_at changes require public_id change';
    END IF;
    IF NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'publication updated_at is monotonic';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION dashboard.guard_publication_stream_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.publication_id IS DISTINCT FROM NEW.publication_id
       OR OLD.stream_id IS DISTINCT FROM NEW.stream_id THEN
        RAISE EXCEPTION 'publication stream primary key is immutable';
    END IF;
    IF NEW.updated_at < OLD.updated_at THEN
        RAISE EXCEPTION 'publication stream updated_at is monotonic';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS publications_mutation ON dashboard.publications;
CREATE TRIGGER publications_mutation
    BEFORE UPDATE ON dashboard.publications
    FOR EACH ROW EXECUTE FUNCTION dashboard.guard_publication_update();
DROP TRIGGER IF EXISTS publication_streams_mutation ON dashboard.publication_streams;
CREATE TRIGGER publication_streams_mutation
    BEFORE UPDATE ON dashboard.publication_streams
    FOR EACH ROW EXECUTE FUNCTION dashboard.guard_publication_stream_update();

-- Application runtime connections do not receive projection-table DML.  All
-- publication mutations enter through the owner-executed boundary below.
-- The boundary validates the canonical event and audit rows already appended
-- by the source transaction, then applies the CAS and projection event in one
-- statement-level transaction.  The event and audit schemas are baseline
-- prerequisites for invoking audited publication mutation functions.
CREATE OR REPLACE FUNCTION dashboard.check_publication_evidence(
    p_publication_id uuid,
    p_project_id text,
    p_name text,
    p_actor_id text,
    p_operation text,
    p_resource_kind text,
    p_resource_id text,
    p_domain_event_id uuid,
    p_aggregate_version bigint,
    p_event_type text,
    p_correlation_id text,
    p_payload jsonb,
    p_audit_metadata jsonb
) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, dashboard, event, audit
AS $$
DECLARE ok boolean;
BEGIN
    IF p_domain_event_id IS NULL OR p_aggregate_version IS NULL OR p_aggregate_version < 1
       OR NULLIF(btrim(p_operation), '') IS NULL
       OR NULLIF(btrim(p_resource_kind), '') IS NULL
       OR NULLIF(btrim(p_resource_id), '') IS NULL
       OR p_event_type IS NULL OR p_payload IS NULL OR p_audit_metadata IS NULL THEN
        RAISE EXCEPTION 'publication mutation evidence is required';
    END IF;
    SELECT EXISTS (
        SELECT 1 FROM event.event_log e
         WHERE e.event_id = p_domain_event_id
           AND e.scope_id = p_project_id
           AND e.aggregate_type = 'dashboard_publication'
           AND e.aggregate_id = p_publication_id::text
           AND e.aggregate_version = p_aggregate_version
           AND e.event_type = p_event_type
           AND e.schema_version = 1
           AND e.correlation_id IS NOT DISTINCT FROM NULLIF(p_correlation_id, '')::uuid
           AND e.payload = p_payload
    ) INTO ok;
    IF NOT ok THEN
        RAISE EXCEPTION 'publication mutation canonical domain event is missing or mismatched';
    END IF;
    SELECT EXISTS (
        SELECT 1 FROM audit.audit_event a
         WHERE a.event_id = p_domain_event_id
           AND a.scope_id = p_project_id
           AND a.actor_id = p_actor_id
           AND a.operation = p_operation
           AND a.source = 'dashboard.publication'
           AND a.action = p_event_type
           AND a.resource_kind = p_resource_kind
           AND a.resource_id = p_resource_id
           AND a.capability = 'RESOURCE_PUBLISH'
           AND a.outcome = 'success'
           AND a.correlation_id IS NOT DISTINCT FROM NULLIF(p_correlation_id, '')::uuid
           AND a.aggregate_key = 'dashboard_publication:' || p_project_id || ':' || p_name
           AND a.aggregate_sequence = p_aggregate_version
           AND a.metadata = p_audit_metadata
    ) INTO ok;
    IF NOT ok THEN
        RAISE EXCEPTION 'publication mutation audit evidence is missing or mismatched';
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION dashboard.record_publication_event(
    p_publication_id uuid,
    p_domain_event_id uuid,
    p_aggregate_version bigint,
    p_revision bigint,
    p_event_type text,
    p_actor_id text,
    p_correlation_id text,
    p_serving_state_id text,
    p_payload jsonb
) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, dashboard, event, audit
AS $$
DECLARE existing dashboard.publication_events%ROWTYPE;
BEGIN
    IF p_publication_id IS NULL OR p_domain_event_id IS NULL OR p_aggregate_version < 1 OR p_revision < 1
       OR p_event_type IS NULL OR p_payload IS NULL THEN
        RAISE EXCEPTION 'publication event identity is required';
    END IF;
    SELECT * INTO existing FROM dashboard.publication_events
     WHERE domain_event_id = p_domain_event_id;
    IF FOUND THEN
        IF existing.publication_id <> p_publication_id
           OR existing.aggregate_version <> p_aggregate_version
           OR existing.revision <> p_revision
           OR existing.event_type <> p_event_type
           OR existing.actor_id <> p_actor_id
           OR existing.correlation_id <> p_correlation_id
           OR existing.serving_state_id IS DISTINCT FROM NULLIF(p_serving_state_id, '')
           OR existing.payload_json <> p_payload THEN
            RAISE EXCEPTION 'publication event replay differs';
        END IF;
        RETURN;
    END IF;
    INSERT INTO dashboard.publication_events(
        publication_id, domain_event_id, aggregate_version, revision,
        event_type, actor_id, correlation_id, serving_state_id, payload_json
    ) VALUES (
        p_publication_id, p_domain_event_id, p_aggregate_version, p_revision,
        p_event_type, p_actor_id, p_correlation_id,
        NULLIF(p_serving_state_id, ''), p_payload
    );
END;
$$;

CREATE OR REPLACE FUNCTION dashboard.publication_payload(p_publication_id uuid, p_event_type text)
RETURNS jsonb
LANGUAGE sql SECURITY DEFINER
SET search_path = pg_catalog, dashboard
AS $$
    SELECT jsonb_build_object(
        'eventType', p_event_type,
        'publicationId', p.id::text,
        'projectId', p.project_id,
        'name', p.name,
        'publicId', p.public_id,
        'dashboard', p.dashboard,
        'defaultPage', p.default_page,
        'configurationDigest', p.configuration_digest,
        'allowedOrigins', p.allowed_origins_json,
        'dependencyAssetIds', p.dependency_asset_ids_json,
        'revision', p.revision,
        'configured', p.configured,
        'servingStateId', COALESCE(p.active_serving_state_id, '')
    ) FROM dashboard.publications p WHERE p.id = p_publication_id
$$;

-- p_operation is deliberately allow-listed.  The wrappers below expose only
-- narrow typed entrypoints to the runtime role; this implementation is kept
-- private to the owner/migrator capabilities.
CREATE OR REPLACE FUNCTION dashboard.mutate_publication(
    p_operation text,
    p_id uuid,
    p_project_id text,
    p_name text,
    p_public_id text,
    p_dashboard text,
    p_default_page text,
    p_configuration_digest text,
    p_allowed_origins_json jsonb,
    p_dependency_asset_ids_json jsonb,
    p_active_serving_state_id text,
    p_expected_revision bigint,
    p_actor_id text,
    p_domain_event_id uuid,
    p_aggregate_version bigint,
    p_event_type text,
    p_correlation_id text,
    p_payload jsonb,
    p_audit_operation text,
    p_audit_resource_kind text,
    p_audit_resource_id text,
    p_audit_metadata jsonb
) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, dashboard, event, audit
AS $$
DECLARE
    v_row dashboard.publications%ROWTYPE;
    v_now timestamptz := clock_timestamp();
    v_project text := p_project_id;
    v_name text := p_name;
    v_id uuid := p_id;
    v_changed bigint;
    v_expected_payload jsonb;
BEGIN
    IF p_operation NOT IN ('create','suspend','resume','rotate','disable','update_configuration') THEN
        RAISE EXCEPTION 'unsupported publication mutation';
    END IF;
    IF (p_operation = 'create' AND p_event_type <> 'dashboard_publication.configured')
       OR (p_operation = 'suspend' AND p_event_type <> 'dashboard_publication.suspended')
       OR (p_operation = 'resume' AND p_event_type <> 'dashboard_publication.resumed')
       OR (p_operation = 'rotate' AND p_event_type <> 'dashboard_publication.rotated')
       OR (p_operation = 'disable' AND p_event_type <> 'dashboard_publication.disabled')
       OR (p_operation = 'update_configuration' AND p_event_type NOT IN ('dashboard_publication.configured','dashboard_publication.configuration_changed','dashboard_publication.serving_state_changed')) THEN
        RAISE EXCEPTION 'publication mutation event type does not match operation';
    END IF;
    IF p_operation <> 'create' AND (p_expected_revision IS NULL OR p_expected_revision < 1) THEN
        RAISE EXCEPTION 'publication expected revision must be positive';
    ELSIF p_operation <> 'create' AND p_expected_revision = 9223372036854775807 THEN
        RAISE EXCEPTION 'publication revision is exhausted';
    END IF;
    IF p_operation = 'create' THEN
        IF EXISTS (SELECT 1 FROM dashboard.publications WHERE id = p_id) THEN
            SELECT * INTO v_row FROM dashboard.publications WHERE id = p_id FOR UPDATE;
            IF v_row.revision = 1 AND EXISTS (SELECT 1 FROM dashboard.publication_events WHERE publication_id = p_id AND domain_event_id = p_domain_event_id) THEN
                v_expected_payload := dashboard.publication_payload(p_id, p_event_type);
                IF v_expected_payload IS DISTINCT FROM p_payload THEN RAISE EXCEPTION 'publication event payload does not describe projection'; END IF;
                PERFORM dashboard.check_publication_evidence(p_id, p_project_id, p_name, p_actor_id, p_audit_operation, p_audit_resource_kind, p_audit_resource_id, p_domain_event_id, p_aggregate_version, p_event_type, p_correlation_id, v_expected_payload, p_audit_metadata);
                PERFORM dashboard.record_publication_event(p_id, p_domain_event_id, p_aggregate_version, 1, p_event_type, p_actor_id, p_correlation_id, p_active_serving_state_id, p_payload);
                RETURN 1;
            END IF;
            RAISE EXCEPTION 'publication identity already exists';
        END IF;
        INSERT INTO dashboard.publications(
            id, project_id, name, public_id, dashboard, default_page,
            configuration_digest, allowed_origins_json, dependency_asset_ids_json,
            revision, configured, active_serving_state_id, configured_at
        ) VALUES (
            p_id, p_project_id, p_name, p_public_id, p_dashboard, p_default_page,
            p_configuration_digest, p_allowed_origins_json, p_dependency_asset_ids_json,
            1, true, NULLIF(p_active_serving_state_id, ''), v_now
        );
        v_expected_payload := dashboard.publication_payload(p_id, p_event_type);
        IF v_expected_payload IS DISTINCT FROM p_payload THEN RAISE EXCEPTION 'publication event payload does not describe projection'; END IF;
        PERFORM dashboard.check_publication_evidence(p_id, p_project_id, p_name, p_actor_id, p_audit_operation, p_audit_resource_kind, p_audit_resource_id, p_domain_event_id, p_aggregate_version, p_event_type, p_correlation_id, v_expected_payload, p_audit_metadata);
        PERFORM dashboard.record_publication_event(p_id, p_domain_event_id, p_aggregate_version, 1, p_event_type, p_actor_id, p_correlation_id, p_active_serving_state_id, p_payload);
        RETURN 1;
    END IF;

    IF p_operation = 'disable' THEN
        SELECT * INTO v_row FROM dashboard.publications WHERE id = p_id FOR UPDATE;
    ELSE
        SELECT * INTO v_row FROM dashboard.publications
         WHERE (p_id IS NOT NULL AND id = p_id)
            OR (p_id IS NULL AND project_id = p_project_id AND name = p_name)
         ORDER BY configured DESC LIMIT 1 FOR UPDATE;
    END IF;
    IF NOT FOUND THEN
        RETURN 0;
    END IF;
    v_project := v_row.project_id;
    v_name := v_row.name;
    v_id := v_row.id;
    -- A replay with the same domain-event identity is accepted only when the
    -- projection event is already complete at the expected next revision.
    IF v_row.revision = p_expected_revision + 1
       AND EXISTS (SELECT 1 FROM dashboard.publication_events WHERE publication_id = v_id AND domain_event_id = p_domain_event_id) THEN
        v_expected_payload := dashboard.publication_payload(v_id, p_event_type);
        IF v_expected_payload IS DISTINCT FROM p_payload THEN RAISE EXCEPTION 'publication event payload does not describe projection'; END IF;
        PERFORM dashboard.check_publication_evidence(v_id, v_project, v_name, p_actor_id, p_audit_operation, p_audit_resource_kind, p_audit_resource_id, p_domain_event_id, p_aggregate_version, p_event_type, p_correlation_id, v_expected_payload, p_audit_metadata);
        PERFORM dashboard.record_publication_event(v_id, p_domain_event_id, p_aggregate_version, v_row.revision, p_event_type, p_actor_id, p_correlation_id, COALESCE(v_row.active_serving_state_id,''), p_payload);
        RETURN 1;
    END IF;
    IF v_row.revision <> p_expected_revision
       OR (p_operation IN ('suspend','resume','rotate') AND NOT v_row.configured) THEN
        RETURN 0;
    END IF;

    IF p_operation = 'suspend' THEN
        UPDATE dashboard.publications SET revision = revision + 1,
            suspended_at = COALESCE(suspended_at, v_now), suspended_by = p_actor_id,
            updated_at = v_now WHERE id = v_id;
    ELSIF p_operation = 'resume' THEN
        UPDATE dashboard.publications SET revision = revision + 1,
            suspended_at = NULL, suspended_by = '', updated_at = v_now WHERE id = v_id;
    ELSIF p_operation = 'rotate' THEN
        UPDATE dashboard.publications SET revision = revision + 1,
            public_id = p_public_id, rotated_at = v_now, updated_at = v_now WHERE id = v_id;
    ELSIF p_operation = 'disable' THEN
        UPDATE dashboard.publications SET revision = revision + 1,
            configured = false, active_serving_state_id = NULL,
            disabled_at = v_now, updated_at = v_now WHERE id = v_id;
    ELSIF p_operation = 'update_configuration' THEN
        UPDATE dashboard.publications SET revision = revision + 1,
            dashboard = p_dashboard, default_page = p_default_page,
            configuration_digest = p_configuration_digest,
            allowed_origins_json = p_allowed_origins_json,
            dependency_asset_ids_json = p_dependency_asset_ids_json,
            configured = true, active_serving_state_id = NULLIF(p_active_serving_state_id, ''),
            configured_at = COALESCE(configured_at, v_now), disabled_at = NULL,
            updated_at = v_now WHERE id = v_id;
    END IF;
    GET DIAGNOSTICS v_changed = ROW_COUNT;
    IF v_changed <> 1 THEN
        RETURN 0;
    END IF;
    v_expected_payload := dashboard.publication_payload(v_id, p_event_type);
    IF v_expected_payload IS DISTINCT FROM p_payload THEN RAISE EXCEPTION 'publication event payload does not describe projection'; END IF;
    PERFORM dashboard.check_publication_evidence(v_id, v_project, v_name, p_actor_id, p_audit_operation, p_audit_resource_kind, p_audit_resource_id, p_domain_event_id, p_aggregate_version, p_event_type, p_correlation_id, v_expected_payload, p_audit_metadata);
    PERFORM dashboard.record_publication_event(v_id, p_domain_event_id, p_aggregate_version, p_expected_revision + 1, p_event_type, p_actor_id, p_correlation_id, COALESCE(p_active_serving_state_id, (SELECT active_serving_state_id FROM dashboard.publications WHERE id=v_id), ''), p_payload);
    RETURN 1;
END;
$$;

CREATE OR REPLACE FUNCTION dashboard.suspend_publication(p_project_id text, p_name text, p_actor_id text, p_expected_revision bigint, p_domain_event_id uuid, p_aggregate_version bigint, p_correlation_id text, p_payload jsonb, p_audit_operation text, p_audit_resource_kind text, p_audit_resource_id text, p_audit_metadata jsonb)
RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, dashboard, event, audit AS $$
BEGIN RETURN dashboard.mutate_publication('suspend', NULL, p_project_id, p_name, '', '', '', '', '{}'::jsonb, '{}'::jsonb, '', p_expected_revision, p_actor_id, p_domain_event_id, p_aggregate_version, 'dashboard_publication.suspended', p_correlation_id, p_payload, p_audit_operation, p_audit_resource_kind, p_audit_resource_id, p_audit_metadata); END $$;
CREATE OR REPLACE FUNCTION dashboard.resume_publication(p_project_id text, p_name text, p_actor_id text, p_expected_revision bigint, p_domain_event_id uuid, p_aggregate_version bigint, p_correlation_id text, p_payload jsonb, p_audit_operation text, p_audit_resource_kind text, p_audit_resource_id text, p_audit_metadata jsonb)
RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, dashboard, event, audit AS $$
BEGIN RETURN dashboard.mutate_publication('resume', NULL, p_project_id, p_name, '', '', '', '', '{}'::jsonb, '{}'::jsonb, '', p_expected_revision, p_actor_id, p_domain_event_id, p_aggregate_version, 'dashboard_publication.resumed', p_correlation_id, p_payload, p_audit_operation, p_audit_resource_kind, p_audit_resource_id, p_audit_metadata); END $$;
CREATE OR REPLACE FUNCTION dashboard.rotate_publication(p_project_id text, p_name text, p_actor_id text, p_expected_revision bigint, p_public_id text, p_domain_event_id uuid, p_aggregate_version bigint, p_correlation_id text, p_payload jsonb, p_audit_operation text, p_audit_resource_kind text, p_audit_resource_id text, p_audit_metadata jsonb)
RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, dashboard, event, audit AS $$
BEGIN RETURN dashboard.mutate_publication('rotate', NULL, p_project_id, p_name, p_public_id, '', '', '', '{}'::jsonb, '{}'::jsonb, '', p_expected_revision, p_actor_id, p_domain_event_id, p_aggregate_version, 'dashboard_publication.rotated', p_correlation_id, p_payload, p_audit_operation, p_audit_resource_kind, p_audit_resource_id, p_audit_metadata); END $$;
CREATE OR REPLACE FUNCTION dashboard.disable_publication(p_id uuid, p_expected_revision bigint, p_actor_id text, p_domain_event_id uuid, p_aggregate_version bigint, p_correlation_id text, p_payload jsonb, p_audit_operation text, p_audit_resource_kind text, p_audit_resource_id text, p_audit_metadata jsonb)
RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, dashboard, event, audit AS $$
DECLARE v_project text; v_name text;
BEGIN SELECT project_id,name INTO v_project,v_name FROM dashboard.publications WHERE id=p_id; RETURN dashboard.mutate_publication('disable', p_id, v_project, v_name, '', '', '', '', '{}'::jsonb, '{}'::jsonb, '', p_expected_revision, p_actor_id, p_domain_event_id, p_aggregate_version, 'dashboard_publication.disabled', p_correlation_id, p_payload, p_audit_operation, p_audit_resource_kind, p_audit_resource_id, p_audit_metadata); END $$;
CREATE OR REPLACE FUNCTION dashboard.update_publication_configuration(p_id uuid, p_dashboard text, p_default_page text, p_configuration_digest text, p_allowed_origins_json jsonb, p_dependency_asset_ids_json jsonb, p_active_serving_state_id text, p_expected_revision bigint, p_actor_id text, p_domain_event_id uuid, p_aggregate_version bigint, p_event_type text, p_correlation_id text, p_payload jsonb, p_audit_operation text, p_audit_resource_kind text, p_audit_resource_id text, p_audit_metadata jsonb)
RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, dashboard, event, audit AS $$
DECLARE v_project text; v_name text;
BEGIN SELECT project_id,name INTO v_project,v_name FROM dashboard.publications WHERE id=p_id; RETURN dashboard.mutate_publication('update_configuration', p_id, v_project, v_name, '', p_dashboard, p_default_page, p_configuration_digest, p_allowed_origins_json, p_dependency_asset_ids_json, p_active_serving_state_id, p_expected_revision, p_actor_id, p_domain_event_id, p_aggregate_version, p_event_type, p_correlation_id, p_payload, p_audit_operation, p_audit_resource_kind, p_audit_resource_id, p_audit_metadata); END $$;
CREATE OR REPLACE FUNCTION dashboard.create_publication(p_id uuid, p_project_id text, p_name text, p_public_id text, p_dashboard text, p_default_page text, p_configuration_digest text, p_allowed_origins_json jsonb, p_dependency_asset_ids_json jsonb, p_active_serving_state_id text, p_actor_id text, p_domain_event_id uuid, p_aggregate_version bigint, p_correlation_id text, p_payload jsonb, p_audit_operation text, p_audit_resource_kind text, p_audit_resource_id text, p_audit_metadata jsonb)
RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, dashboard, event, audit AS $$
BEGIN RETURN dashboard.mutate_publication('create', p_id, p_project_id, p_name, p_public_id, p_dashboard, p_default_page, p_configuration_digest, p_allowed_origins_json, p_dependency_asset_ids_json, p_active_serving_state_id, 0, p_actor_id, p_domain_event_id, p_aggregate_version, 'dashboard_publication.configured', p_correlation_id, p_payload, p_audit_operation, p_audit_resource_kind, p_audit_resource_id, p_audit_metadata); END $$;

-- Stream liveness is operational state, not a user-visible domain mutation.
CREATE OR REPLACE FUNCTION dashboard.upsert_publication_stream(p_publication_id uuid, p_stream_id text, p_public_id text, p_serving_state_id text, p_registration_id uuid, p_filters_json jsonb, p_expires_at timestamptz)
RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, dashboard AS $$
BEGIN
 IF p_expires_at IS NULL OR p_expires_at <= clock_timestamp() OR p_expires_at > clock_timestamp()+interval '24 hours' THEN RAISE EXCEPTION 'publication stream expiry is outside the database-clock window'; END IF;
 INSERT INTO dashboard.publication_streams(publication_id,stream_id,public_id,serving_state_id,registration_id,filters_json,expires_at)
 VALUES(p_publication_id,p_stream_id,p_public_id,p_serving_state_id,p_registration_id,p_filters_json,p_expires_at)
 ON CONFLICT(publication_id,stream_id) DO UPDATE SET public_id=excluded.public_id,serving_state_id=excluded.serving_state_id,registration_id=excluded.registration_id,filters_json=excluded.filters_json,generation=1,expires_at=excluded.expires_at,updated_at=clock_timestamp();
END $$;
CREATE OR REPLACE FUNCTION dashboard.delete_stream_registration(p_publication_id uuid, p_stream_id text, p_registration_id uuid)
RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, dashboard AS $$ BEGIN DELETE FROM dashboard.publication_streams WHERE publication_id=p_publication_id AND stream_id=p_stream_id AND registration_id=p_registration_id; END $$;
CREATE OR REPLACE FUNCTION dashboard.expire_stream_registration(p_publication_id uuid, p_stream_id text, p_registration_id uuid)
RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, dashboard AS $$ DECLARE n bigint; BEGIN UPDATE dashboard.publication_streams SET expires_at=clock_timestamp(),updated_at=clock_timestamp() WHERE publication_id=p_publication_id AND stream_id=p_stream_id AND registration_id=p_registration_id; GET DIAGNOSTICS n=ROW_COUNT; RETURN n; END $$;
CREATE OR REPLACE FUNCTION dashboard.update_command_state(p_publication_id uuid, p_stream_id text, p_public_id text, p_serving_state_id text, p_registration_id uuid, p_current_generation bigint, p_filters_json jsonb, p_next_generation bigint, p_expires_at timestamptz)
RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, dashboard AS $$ DECLARE n bigint; v_now timestamptz := clock_timestamp(); BEGIN IF p_next_generation <> p_current_generation + 1 OR p_expires_at IS NULL OR p_expires_at <= v_now OR p_expires_at > v_now+interval '24 hours' THEN RAISE EXCEPTION 'publication stream generation or expiry is invalid'; END IF; UPDATE dashboard.publication_streams SET filters_json=p_filters_json,generation=p_next_generation,expires_at=p_expires_at,updated_at=v_now WHERE publication_id=p_publication_id AND stream_id=p_stream_id AND public_id=p_public_id AND serving_state_id=p_serving_state_id AND registration_id=p_registration_id AND generation=p_current_generation AND expires_at>v_now; GET DIAGNOSTICS n=ROW_COUNT; RETURN n; END $$;
CREATE OR REPLACE FUNCTION dashboard.extend_stream(p_publication_id uuid, p_stream_id text, p_public_id text, p_serving_state_id text, p_registration_id uuid, p_expires_at timestamptz)
RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, dashboard AS $$ DECLARE n bigint; v_now timestamptz := clock_timestamp(); BEGIN IF p_expires_at IS NULL OR p_expires_at <= v_now OR p_expires_at > v_now+interval '24 hours' THEN RAISE EXCEPTION 'publication stream expiry is outside the database-clock window'; END IF; UPDATE dashboard.publication_streams SET expires_at=p_expires_at,updated_at=v_now WHERE publication_id=p_publication_id AND stream_id=p_stream_id AND public_id=p_public_id AND serving_state_id=p_serving_state_id AND registration_id=p_registration_id AND expires_at>v_now; GET DIAGNOSTICS n=ROW_COUNT; RETURN n; END $$;
CREATE OR REPLACE FUNCTION dashboard.expire_publication_streams(p_publication_id uuid)
RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, dashboard AS $$ DECLARE n bigint; BEGIN UPDATE dashboard.publication_streams SET expires_at=clock_timestamp(),updated_at=clock_timestamp() WHERE publication_id=p_publication_id; GET DIAGNOSTICS n=ROW_COUNT; RETURN n; END $$;
CREATE OR REPLACE FUNCTION dashboard.prune_expired_publication_streams(p_now timestamptz, p_batch_limit integer)
RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, dashboard AS $$ DECLARE n bigint; v_now timestamptz := LEAST(COALESCE(p_now,clock_timestamp()),clock_timestamp()); BEGIN IF p_batch_limit IS NULL OR p_batch_limit < 1 OR p_batch_limit > 1000 THEN RAISE EXCEPTION 'publication maintenance batch limit must be between 1 and 1000'; END IF; WITH claimed AS (SELECT s.publication_id,s.stream_id FROM dashboard.publication_streams s WHERE s.expires_at<=v_now ORDER BY s.expires_at,s.publication_id,s.stream_id LIMIT p_batch_limit FOR UPDATE SKIP LOCKED) DELETE FROM dashboard.publication_streams t USING claimed c WHERE t.publication_id=c.publication_id AND t.stream_id=c.stream_id; GET DIAGNOSTICS n=ROW_COUNT; RETURN n; END $$;


DO $$
BEGIN
    IF to_regclass('project.project_identity') IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'publications_project_fk') THEN
        ALTER TABLE dashboard.publications
            ADD CONSTRAINT publications_project_fk FOREIGN KEY (project_id)
            REFERENCES project.project_identity(project_id) ON DELETE RESTRICT;
    END IF;
END $$;

REVOKE ALL ON SCHEMA dashboard FROM PUBLIC;
REVOKE ALL ON TABLE dashboard.publications,dashboard.publication_events,dashboard.publication_streams FROM PUBLIC;
REVOKE ALL ON SEQUENCE dashboard.publication_events_id_seq FROM PUBLIC;
REVOKE ALL ON FUNCTION dashboard.guard_publication_update(), dashboard.guard_publication_stream_update(), dashboard.check_publication_evidence(uuid,text,text,text,text,text,text,uuid,bigint,text,text,jsonb,jsonb), dashboard.record_publication_event(uuid,uuid,bigint,bigint,text,text,text,text,jsonb), dashboard.publication_payload(uuid,text), dashboard.mutate_publication(text,uuid,text,text,text,text,text,text,jsonb,jsonb,text,bigint,text,uuid,bigint,text,text,jsonb,text,text,text,jsonb), dashboard.suspend_publication(text,text,text,bigint,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.resume_publication(text,text,text,bigint,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.rotate_publication(text,text,text,bigint,text,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.disable_publication(uuid,bigint,text,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.update_publication_configuration(uuid,text,text,text,jsonb,jsonb,text,bigint,text,uuid,bigint,text,text,jsonb,text,text,text,jsonb), dashboard.create_publication(uuid,text,text,text,text,text,text,jsonb,jsonb,text,text,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.upsert_publication_stream(uuid,text,text,text,uuid,jsonb,timestamptz), dashboard.delete_stream_registration(uuid,text,uuid), dashboard.expire_stream_registration(uuid,text,uuid), dashboard.update_command_state(uuid,text,text,text,uuid,bigint,jsonb,bigint,timestamptz), dashboard.extend_stream(uuid,text,text,text,uuid,timestamptz), dashboard.expire_publication_streams(uuid), dashboard.prune_expired_publication_streams(timestamptz,integer) FROM PUBLIC;
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_owner') THEN
  GRANT USAGE ON SCHEMA dashboard TO leapview_control_owner;
  GRANT ALL ON TABLE dashboard.publications,dashboard.publication_events,dashboard.publication_streams TO leapview_control_owner;
  GRANT ALL ON SEQUENCE dashboard.publication_events_id_seq TO leapview_control_owner;
  GRANT ALL ON FUNCTION dashboard.guard_publication_update(), dashboard.guard_publication_stream_update(), dashboard.check_publication_evidence(uuid,text,text,text,text,text,text,uuid,bigint,text,text,jsonb,jsonb), dashboard.record_publication_event(uuid,uuid,bigint,bigint,text,text,text,text,jsonb), dashboard.publication_payload(uuid,text), dashboard.mutate_publication(text,uuid,text,text,text,text,text,text,jsonb,jsonb,text,bigint,text,uuid,bigint,text,text,jsonb,text,text,text,jsonb), dashboard.suspend_publication(text,text,text,bigint,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.resume_publication(text,text,text,bigint,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.rotate_publication(text,text,text,bigint,text,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.disable_publication(uuid,bigint,text,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.update_publication_configuration(uuid,text,text,text,jsonb,jsonb,text,bigint,text,uuid,bigint,text,text,jsonb,text,text,text,jsonb), dashboard.create_publication(uuid,text,text,text,text,text,text,jsonb,jsonb,text,text,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.upsert_publication_stream(uuid,text,text,text,uuid,jsonb,timestamptz), dashboard.delete_stream_registration(uuid,text,uuid), dashboard.expire_stream_registration(uuid,text,uuid), dashboard.update_command_state(uuid,text,text,text,uuid,bigint,jsonb,bigint,timestamptz), dashboard.extend_stream(uuid,text,text,text,uuid,timestamptz), dashboard.expire_publication_streams(uuid), dashboard.prune_expired_publication_streams(timestamptz,integer) TO leapview_control_owner;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_migrator') THEN
  GRANT USAGE ON SCHEMA dashboard TO leapview_control_migrator;
  GRANT ALL ON TABLE dashboard.publications,dashboard.publication_events,dashboard.publication_streams TO leapview_control_migrator;
  GRANT ALL ON SEQUENCE dashboard.publication_events_id_seq TO leapview_control_migrator;
  GRANT ALL ON FUNCTION dashboard.guard_publication_update(), dashboard.guard_publication_stream_update(), dashboard.check_publication_evidence(uuid,text,text,text,text,text,text,uuid,bigint,text,text,jsonb,jsonb), dashboard.record_publication_event(uuid,uuid,bigint,bigint,text,text,text,text,jsonb), dashboard.publication_payload(uuid,text), dashboard.mutate_publication(text,uuid,text,text,text,text,text,text,jsonb,jsonb,text,bigint,text,uuid,bigint,text,text,jsonb,text,text,text,jsonb), dashboard.suspend_publication(text,text,text,bigint,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.resume_publication(text,text,text,bigint,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.rotate_publication(text,text,text,bigint,text,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.disable_publication(uuid,bigint,text,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.update_publication_configuration(uuid,text,text,text,jsonb,jsonb,text,bigint,text,uuid,bigint,text,text,jsonb,text,text,text,jsonb), dashboard.create_publication(uuid,text,text,text,text,text,text,jsonb,jsonb,text,text,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.upsert_publication_stream(uuid,text,text,text,uuid,jsonb,timestamptz), dashboard.delete_stream_registration(uuid,text,uuid), dashboard.expire_stream_registration(uuid,text,uuid), dashboard.update_command_state(uuid,text,text,text,uuid,bigint,jsonb,bigint,timestamptz), dashboard.extend_stream(uuid,text,text,text,uuid,timestamptz), dashboard.expire_publication_streams(uuid) TO leapview_control_migrator;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_runtime') THEN
  GRANT USAGE ON SCHEMA dashboard TO leapview_control_runtime;
  GRANT SELECT ON dashboard.publications,dashboard.publication_events,dashboard.publication_streams TO leapview_control_runtime;
  REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON dashboard.publications,dashboard.publication_events,dashboard.publication_streams FROM leapview_control_runtime;
  GRANT EXECUTE ON FUNCTION dashboard.suspend_publication(text,text,text,bigint,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.resume_publication(text,text,text,bigint,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.rotate_publication(text,text,text,bigint,text,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.disable_publication(uuid,bigint,text,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.update_publication_configuration(uuid,text,text,text,jsonb,jsonb,text,bigint,text,uuid,bigint,text,text,jsonb,text,text,text,jsonb), dashboard.create_publication(uuid,text,text,text,text,text,text,jsonb,jsonb,text,text,uuid,bigint,text,jsonb,text,text,text,jsonb), dashboard.upsert_publication_stream(uuid,text,text,text,uuid,jsonb,timestamptz), dashboard.delete_stream_registration(uuid,text,uuid), dashboard.expire_stream_registration(uuid,text,uuid), dashboard.update_command_state(uuid,text,text,text,uuid,bigint,jsonb,bigint,timestamptz), dashboard.extend_stream(uuid,text,text,text,uuid,timestamptz), dashboard.expire_publication_streams(uuid) TO leapview_control_runtime;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_maintenance') THEN
  GRANT USAGE ON SCHEMA dashboard TO leapview_control_maintenance;
  GRANT SELECT ON dashboard.publication_streams TO leapview_control_maintenance;
  GRANT EXECUTE ON FUNCTION dashboard.prune_expired_publication_streams(timestamptz,integer) TO leapview_control_maintenance;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_readonly') THEN
  GRANT USAGE ON SCHEMA dashboard TO leapview_control_readonly;
  GRANT SELECT ON dashboard.publications,dashboard.publication_events,dashboard.publication_streams TO leapview_control_readonly;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_backup') THEN
  GRANT USAGE ON SCHEMA dashboard TO leapview_control_backup;
  GRANT SELECT ON dashboard.publications,dashboard.publication_events,dashboard.publication_streams TO leapview_control_backup;
 END IF;
END $$;
