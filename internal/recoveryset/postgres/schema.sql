-- Durable PostgreSQL recovery-set frontier (FAI-573).
-- The frontier is normalized so recovery validation can read exact typed
-- identities without inferring a latest/current row.

CREATE SCHEMA IF NOT EXISTS recovery;

CREATE TABLE IF NOT EXISTS recovery.recovery_set (
    set_id                         uuid PRIMARY KEY,
    schema_version                 integer NOT NULL DEFAULT 1 CHECK (schema_version = 1),
    expected_cluster_points        integer NOT NULL DEFAULT 2 CHECK (expected_cluster_points = 2),
    expected_object_roots           integer NOT NULL CHECK (expected_object_roots BETWEEN 1 AND 128),
    target_id                      text NOT NULL,
    generation_id                  uuid NOT NULL,
    publication_id                 uuid NOT NULL,
    target_revision                bigint NOT NULL CHECK (target_revision > 0),
    snapshot_seal_id               uuid NOT NULL,
    physical_pool_id               text NOT NULL,
    tenant_domain                  text NOT NULL,
    region                         text NOT NULL,
    encryption_domain              text NOT NULL,
    object_namespace               text NOT NULL,
    catalog_database               text NOT NULL,
    catalog_id                     text NOT NULL,
    catalog_uuid                   text NOT NULL,
    catalog_version                bigint NOT NULL CHECK (catalog_version > 0),
    ducklake_snapshot_id           bigint NOT NULL CHECK (ducklake_snapshot_id > 0),
    relation_namespace             text NOT NULL,
    relation_manifest_digest       text NOT NULL,
    closure_digest                 text NOT NULL,
    object_root                    text NOT NULL,
    object_root_digest             text NOT NULL,
    artifact_root                  text NOT NULL,
    artifact_root_digest           text NOT NULL,
    serving_artifact_id            text NOT NULL,
    serving_artifact_digest        text NOT NULL,
    compiled_graph_digest          text NOT NULL,
    compiled_config_digest         text NOT NULL,
    security_domain_fingerprint    text NOT NULL,
    request_digest                 text NOT NULL,
    plan_digest                    text NOT NULL,
    compatibility_digest           text NOT NULL,
    duckdb_version                 text NOT NULL,
    runtime_version                text NOT NULL,
    ducklake_extension_version     text NOT NULL,
    ducklake_spec_version          text NOT NULL,
    catalog_schema_version         text NOT NULL,
    duckdb_runtime                 text NOT NULL,
    ducklake_extension             text NOT NULL,
    catalog_format                 text NOT NULL,
    storage_implementation         text NOT NULL,
    object_naming_contract         text NOT NULL,
    fence_epoch                    bigint NOT NULL CHECK (fence_epoch > 0),
    audit_identity                 text NOT NULL,
    frontier_digest                text NOT NULL,
    status                         text NOT NULL DEFAULT 'prepared'
        CHECK (status IN ('prepared', 'published', 'superseded', 'invalid')),
    created_by                     text NOT NULL,
    created_at                     timestamptz NOT NULL DEFAULT clock_timestamp(),
    published_by                   text,
    published_at                   timestamptz,
    -- The attempt row is created after this frontier row, so the explicit
    -- publication binding is verified transactionally by PublishRecoverySet
    -- rather than expressed as a circular foreign key.
    published_validation_attempt_id uuid,
    CHECK (target_id = btrim(target_id) AND octet_length(target_id) BETWEEN 1 AND 255),
    CHECK (physical_pool_id = btrim(physical_pool_id) AND octet_length(physical_pool_id) BETWEEN 1 AND 255),
    CHECK (tenant_domain = btrim(tenant_domain) AND octet_length(tenant_domain) BETWEEN 1 AND 255),
    CHECK (region = btrim(region) AND octet_length(region) BETWEEN 1 AND 128),
    CHECK (encryption_domain = btrim(encryption_domain) AND octet_length(encryption_domain) BETWEEN 1 AND 255),
    CHECK (object_namespace = btrim(object_namespace) AND octet_length(object_namespace) BETWEEN 1 AND 512),
    CHECK (catalog_database = btrim(catalog_database) AND octet_length(catalog_database) BETWEEN 1 AND 255),
    CHECK (catalog_id = btrim(catalog_id) AND octet_length(catalog_id) BETWEEN 1 AND 255),
    CHECK (catalog_uuid = btrim(catalog_uuid) AND octet_length(catalog_uuid) BETWEEN 1 AND 255),
    CHECK (relation_namespace = btrim(relation_namespace) AND octet_length(relation_namespace) BETWEEN 1 AND 512),
    CHECK (serving_artifact_id = btrim(serving_artifact_id) AND octet_length(serving_artifact_id) BETWEEN 1 AND 255),
    CHECK (duckdb_runtime = btrim(duckdb_runtime) AND octet_length(duckdb_runtime) BETWEEN 1 AND 255),
    CHECK (ducklake_extension = btrim(ducklake_extension) AND octet_length(ducklake_extension) BETWEEN 1 AND 255),
    CHECK (catalog_format = btrim(catalog_format) AND octet_length(catalog_format) BETWEEN 1 AND 255),
    CHECK (storage_implementation = btrim(storage_implementation) AND octet_length(storage_implementation) BETWEEN 1 AND 255),
    CHECK (object_naming_contract = btrim(object_naming_contract) AND octet_length(object_naming_contract) BETWEEN 1 AND 255),
    CHECK (audit_identity = btrim(audit_identity) AND octet_length(audit_identity) BETWEEN 1 AND 255),
    CHECK (created_by = btrim(created_by) AND octet_length(created_by) BETWEEN 1 AND 255),
    CHECK (published_by IS NULL OR (published_by = btrim(published_by) AND octet_length(published_by) BETWEEN 1 AND 255)),
    CHECK (generation_id IS NOT NULL AND publication_id IS NOT NULL AND snapshot_seal_id IS NOT NULL),
    CHECK (relation_manifest_digest ~ '^sha256:[0-9a-f]{64}$' AND closure_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (object_root_digest ~ '^sha256:[0-9a-f]{64}$' AND artifact_root_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (serving_artifact_digest ~ '^sha256:[0-9a-f]{64}$' AND compiled_graph_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (compiled_config_digest ~ '^sha256:[0-9a-f]{64}$' AND security_domain_fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (request_digest ~ '^sha256:[0-9a-f]{64}$' AND plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (compatibility_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (frontier_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK ((status = 'prepared' AND published_by IS NULL AND published_at IS NULL AND published_validation_attempt_id IS NULL)
        OR (status IN ('published', 'superseded') AND published_by IS NOT NULL AND published_at IS NOT NULL AND published_validation_attempt_id IS NOT NULL)
        OR status = 'invalid')
);

CREATE TABLE IF NOT EXISTS recovery.recovery_cluster_point (
    set_id               uuid NOT NULL REFERENCES recovery.recovery_set(set_id) ON DELETE RESTRICT,
    database_role        text NOT NULL CHECK (database_role IN ('control', 'ducklake')),
    cluster_identity     text NOT NULL,
    database_identity    text NOT NULL,
    recovery_identity    text NOT NULL,
    PRIMARY KEY (set_id, database_role),
    CHECK (cluster_identity = btrim(cluster_identity) AND octet_length(cluster_identity) BETWEEN 1 AND 255),
    CHECK (database_identity = btrim(database_identity) AND octet_length(database_identity) BETWEEN 1 AND 255),
    CHECK (recovery_identity = btrim(recovery_identity) AND octet_length(recovery_identity) BETWEEN 1 AND 512)
);

CREATE TABLE IF NOT EXISTS recovery.recovery_object_root (
    set_id                      uuid NOT NULL REFERENCES recovery.recovery_set(set_id) ON DELETE RESTRICT,
    root_kind                   text NOT NULL,
    root_uri                    text NOT NULL,
    version_id                  text NOT NULL,
    digest                      text NOT NULL,
    provider_recovery_frontier text NOT NULL DEFAULT '',
    PRIMARY KEY (set_id, root_kind, root_uri, version_id),
    CHECK (root_kind = btrim(root_kind) AND octet_length(root_kind) BETWEEN 1 AND 128),
    CHECK (root_uri = btrim(root_uri) AND octet_length(root_uri) BETWEEN 1 AND 2048),
    CHECK (version_id = btrim(version_id) AND octet_length(version_id) BETWEEN 1 AND 512),
    CHECK (provider_recovery_frontier = btrim(provider_recovery_frontier) AND octet_length(provider_recovery_frontier) <= 512),
    CHECK (digest ~ '^sha256:[0-9a-f]{64}$')
);

CREATE TABLE IF NOT EXISTS recovery.validation_attempt (
    attempt_id       uuid PRIMARY KEY,
    set_id           uuid NOT NULL REFERENCES recovery.recovery_set(set_id) ON DELETE RESTRICT,
    owner_id         text NOT NULL,
    fence_epoch      bigint NOT NULL CHECK (fence_epoch > 0),
    audit_identity   text NOT NULL,
    status           text NOT NULL CHECK (status IN ('running', 'passed', 'failed')),
    result_digest    text,
    error            text,
    started_at       timestamptz NOT NULL DEFAULT clock_timestamp(),
    completed_at     timestamptz,
    CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255),
    CHECK (audit_identity = btrim(audit_identity) AND octet_length(audit_identity) BETWEEN 1 AND 255),
    CHECK (result_digest IS NULL OR result_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (error IS NULL OR octet_length(error) <= 16384),
    CHECK ((status = 'running' AND completed_at IS NULL AND result_digest IS NULL AND error IS NULL)
        OR (status = 'passed' AND completed_at IS NOT NULL AND result_digest IS NOT NULL AND error IS NULL)
        OR (status = 'failed' AND completed_at IS NOT NULL AND error IS NOT NULL))
);

CREATE TABLE IF NOT EXISTS recovery.validation_result (
    attempt_id       uuid PRIMARY KEY REFERENCES recovery.validation_attempt(attempt_id) ON DELETE RESTRICT,
    result_digest    text NOT NULL CHECK (result_digest ~ '^sha256:[0-9a-f]{64}$'),
    evidence         jsonb NOT NULL CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) BETWEEN 2 AND 65536),
    recorded_at      timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE OR REPLACE FUNCTION recovery.reject_frontier_mutation()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, recovery AS $$
BEGIN
    IF TG_OP = 'UPDATE'
       AND NEW.set_id = OLD.set_id
       AND NEW.schema_version = OLD.schema_version
       AND NEW.expected_cluster_points = OLD.expected_cluster_points AND NEW.expected_object_roots = OLD.expected_object_roots
       AND NEW.target_id = OLD.target_id AND NEW.generation_id = OLD.generation_id
       AND NEW.publication_id = OLD.publication_id AND NEW.target_revision = OLD.target_revision
       AND NEW.snapshot_seal_id = OLD.snapshot_seal_id AND NEW.physical_pool_id = OLD.physical_pool_id
       AND NEW.tenant_domain = OLD.tenant_domain AND NEW.region = OLD.region
       AND NEW.encryption_domain = OLD.encryption_domain AND NEW.object_namespace = OLD.object_namespace
       AND NEW.catalog_database = OLD.catalog_database AND NEW.catalog_id = OLD.catalog_id
       AND NEW.catalog_uuid = OLD.catalog_uuid AND NEW.catalog_version = OLD.catalog_version
       AND NEW.ducklake_snapshot_id = OLD.ducklake_snapshot_id AND NEW.relation_namespace = OLD.relation_namespace
       AND NEW.relation_manifest_digest = OLD.relation_manifest_digest AND NEW.closure_digest = OLD.closure_digest
       AND NEW.object_root = OLD.object_root AND NEW.object_root_digest = OLD.object_root_digest
       AND NEW.artifact_root = OLD.artifact_root AND NEW.artifact_root_digest = OLD.artifact_root_digest
       AND NEW.serving_artifact_id = OLD.serving_artifact_id AND NEW.serving_artifact_digest = OLD.serving_artifact_digest
       AND NEW.compiled_graph_digest = OLD.compiled_graph_digest AND NEW.compiled_config_digest = OLD.compiled_config_digest
       AND NEW.security_domain_fingerprint = OLD.security_domain_fingerprint AND NEW.request_digest = OLD.request_digest
       AND NEW.plan_digest = OLD.plan_digest AND NEW.compatibility_digest = OLD.compatibility_digest
       AND NEW.frontier_digest = OLD.frontier_digest
       AND NEW.duckdb_version = OLD.duckdb_version AND NEW.runtime_version = OLD.runtime_version
       AND NEW.ducklake_extension_version = OLD.ducklake_extension_version AND NEW.ducklake_spec_version = OLD.ducklake_spec_version
       AND NEW.catalog_schema_version = OLD.catalog_schema_version AND NEW.duckdb_runtime = OLD.duckdb_runtime
       AND NEW.ducklake_extension = OLD.ducklake_extension AND NEW.catalog_format = OLD.catalog_format
       AND NEW.storage_implementation = OLD.storage_implementation AND NEW.object_naming_contract = OLD.object_naming_contract
       AND NEW.fence_epoch = OLD.fence_epoch AND NEW.audit_identity = OLD.audit_identity
       AND NEW.created_by = OLD.created_by AND NEW.created_at = OLD.created_at
       AND ((OLD.status = 'prepared' AND NEW.published_validation_attempt_id IS NOT NULL)
            OR NEW.published_validation_attempt_id IS NOT DISTINCT FROM OLD.published_validation_attempt_id)
       AND ((OLD.status = 'prepared' AND NEW.status = 'published' AND NEW.published_by IS NOT NULL AND NEW.published_at IS NOT NULL
             AND NEW.published_validation_attempt_id IS NOT NULL
             AND EXISTS (
                 SELECT 1
                 FROM recovery.validation_attempt AS validation
                 JOIN recovery.validation_result AS result ON result.attempt_id = validation.attempt_id
                 WHERE validation.attempt_id = NEW.published_validation_attempt_id
                   AND validation.set_id = NEW.set_id
                   AND validation.fence_epoch = NEW.fence_epoch
                   AND validation.status = 'passed'
                   AND validation.result_digest = result.result_digest
             )
             AND (SELECT count(*) FROM recovery.recovery_cluster_point WHERE set_id = NEW.set_id) = 2
             AND (SELECT count(*) FROM recovery.recovery_object_root WHERE set_id = NEW.set_id) = (SELECT expected_object_roots FROM recovery.recovery_set WHERE set_id = NEW.set_id))
         OR (OLD.status = 'published' AND NEW.status IN ('published', 'superseded') AND NEW.published_by = OLD.published_by AND NEW.published_at = OLD.published_at)) THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'recovery-set frontier identity is immutable';
END;
$$;

CREATE OR REPLACE FUNCTION recovery.reject_frontier_insert()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, recovery AS $$
BEGIN
    -- A frontier is created prepared and can become published only through
    -- the fenced transition below, which proves one exact passed validation.
    IF NEW.status = 'prepared' AND NEW.published_validation_attempt_id IS NULL THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'recovery-set frontier must be created prepared';
END;
$$;

DROP TRIGGER IF EXISTS recovery_set_immutable ON recovery.recovery_set;
CREATE TRIGGER recovery_set_immutable BEFORE UPDATE OR DELETE ON recovery.recovery_set
FOR EACH ROW EXECUTE FUNCTION recovery.reject_frontier_mutation();

DROP TRIGGER IF EXISTS recovery_set_insert_guard ON recovery.recovery_set;
CREATE TRIGGER recovery_set_insert_guard BEFORE INSERT ON recovery.recovery_set
FOR EACH ROW EXECUTE FUNCTION recovery.reject_frontier_insert();

CREATE OR REPLACE FUNCTION recovery.reject_child_mutation()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, recovery AS $$
DECLARE expected_count integer; current_count integer; frontier_status text;
BEGIN
    IF TG_OP = 'INSERT' THEN
        SELECT CASE WHEN TG_TABLE_NAME = 'recovery_cluster_point' THEN expected_cluster_points ELSE expected_object_roots END,
               status
          INTO STRICT expected_count, frontier_status
          FROM recovery.recovery_set WHERE set_id = NEW.set_id FOR UPDATE;
        IF frontier_status <> 'prepared' THEN
            RAISE EXCEPTION 'recovery-set evidence cannot be appended after publication';
        END IF;
        IF TG_TABLE_NAME = 'recovery_cluster_point' THEN
            SELECT expected_cluster_points INTO expected_count FROM recovery.recovery_set WHERE set_id = NEW.set_id;
            SELECT count(*) INTO current_count FROM recovery.recovery_cluster_point WHERE set_id = NEW.set_id;
        ELSE
            SELECT expected_object_roots INTO expected_count FROM recovery.recovery_set WHERE set_id = NEW.set_id;
            SELECT count(*) INTO current_count FROM recovery.recovery_object_root WHERE set_id = NEW.set_id;
        END IF;
        IF current_count >= expected_count THEN
            RAISE EXCEPTION 'recovery-set evidence is complete and cannot be extended';
        END IF;
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'recovery-set evidence is append-only';
END;
$$;

CREATE OR REPLACE FUNCTION recovery.guard_validation_attempt_transition()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, recovery AS $$
BEGIN
    IF TG_OP = 'INSERT'
       AND NEW.status = 'running' AND NEW.result_digest IS NULL
       AND NEW.error IS NULL AND NEW.completed_at IS NULL
       AND EXISTS (
           SELECT 1 FROM recovery.recovery_set AS frontier
           WHERE frontier.set_id = NEW.set_id
             AND frontier.fence_epoch = NEW.fence_epoch
             AND frontier.status = 'prepared'
       ) THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'UPDATE'
       AND OLD.status = 'running' AND NEW.status IN ('passed', 'failed')
       AND NEW.attempt_id = OLD.attempt_id AND NEW.set_id = OLD.set_id
       AND NEW.owner_id = OLD.owner_id AND NEW.fence_epoch = OLD.fence_epoch
       AND NEW.audit_identity = OLD.audit_identity AND NEW.started_at = OLD.started_at
       AND NEW.completed_at IS NOT NULL
       AND ((NEW.status = 'passed' AND EXISTS (
                SELECT 1 FROM recovery.validation_result AS result
                WHERE result.attempt_id = NEW.attempt_id AND result.result_digest = NEW.result_digest
            ))
         OR (NEW.status = 'failed' AND (NEW.result_digest IS NULL OR EXISTS (
                SELECT 1 FROM recovery.validation_result AS result
                WHERE result.attempt_id = NEW.attempt_id AND result.result_digest = NEW.result_digest
            )))) THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'recovery validation attempt identity or terminal result is immutable';
END;
$$;

DROP TRIGGER IF EXISTS recovery_validation_attempt_guard ON recovery.validation_attempt;
CREATE TRIGGER recovery_validation_attempt_guard BEFORE INSERT OR UPDATE OR DELETE ON recovery.validation_attempt
FOR EACH ROW EXECUTE FUNCTION recovery.guard_validation_attempt_transition();

CREATE OR REPLACE FUNCTION recovery.reject_validation_result_mutation()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, recovery AS $$
BEGIN
    RAISE EXCEPTION 'recovery validation evidence is immutable';
END;
$$;

-- Validation results are capability evidence, not an arbitrary JSON side
-- channel.  Reconstruct the exact v1 envelope from the immutable frontier
-- and its append-only child rows before accepting a direct SQL INSERT.  The
-- Go repository performs the same ValidateFor comparison; this trigger keeps
-- maintenance-role SQL from bypassing that contract before a passed attempt
-- can be published.
CREATE OR REPLACE FUNCTION recovery.guard_validation_result_insert()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, recovery AS $$
DECLARE
    frontier recovery.recovery_set;
    expected_evidence jsonb;
BEGIN
    SELECT selected.*
      INTO frontier
      FROM recovery.validation_attempt AS attempt
      JOIN recovery.recovery_set AS selected ON selected.set_id = attempt.set_id
     WHERE attempt.attempt_id = NEW.attempt_id
     FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'validation result requires an exact recovery frontier';
    END IF;
    IF (SELECT count(*) FROM recovery.recovery_cluster_point WHERE set_id = frontier.set_id) <> frontier.expected_cluster_points
       OR (SELECT count(*) FROM recovery.recovery_object_root WHERE set_id = frontier.set_id) <> frontier.expected_object_roots THEN
        RAISE EXCEPTION 'validation result requires complete recovery frontier evidence';
    END IF;
    expected_evidence := jsonb_build_object(
        'schema_version', 1,
        'set_id', frontier.set_id::text,
        'attempt_id', NEW.attempt_id::text,
        'frontier_digest', frontier.frontier_digest,
        'cluster_points', COALESCE((
            SELECT jsonb_agg(jsonb_build_object(
                'database_role', point.database_role,
                'cluster_identity', point.cluster_identity,
                'database_identity', point.database_identity,
                'recovery_identity', point.recovery_identity
            ) ORDER BY point.database_role)
              FROM recovery.recovery_cluster_point AS point
             WHERE point.set_id = frontier.set_id
        ), '[]'::jsonb),
        'object_roots', COALESCE((
            SELECT jsonb_agg(jsonb_build_object(
                'kind', root.root_kind,
                'uri', root.root_uri,
                'version_id', root.version_id,
                'digest', root.digest,
                'provider_recovery_frontier', root.provider_recovery_frontier
            ) ORDER BY root.root_kind, root.root_uri, root.version_id)
              FROM recovery.recovery_object_root AS root
             WHERE root.set_id = frontier.set_id
        ), '[]'::jsonb),
        'relation_namespace', frontier.relation_namespace,
        'relation_manifest_digest', frontier.relation_manifest_digest,
        'closure_digest', frontier.closure_digest
    );
    IF NEW.evidence IS DISTINCT FROM expected_evidence THEN
        RAISE EXCEPTION 'validation result evidence does not match exact recovery frontier';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS recovery_validation_result_guard ON recovery.validation_result;
CREATE TRIGGER recovery_validation_result_guard BEFORE INSERT ON recovery.validation_result
FOR EACH ROW EXECUTE FUNCTION recovery.guard_validation_result_insert();

DROP TRIGGER IF EXISTS recovery_validation_result_immutable ON recovery.validation_result;
CREATE TRIGGER recovery_validation_result_immutable BEFORE UPDATE OR DELETE ON recovery.validation_result
FOR EACH ROW EXECUTE FUNCTION recovery.reject_validation_result_mutation();

DROP TRIGGER IF EXISTS recovery_cluster_point_immutable ON recovery.recovery_cluster_point;
CREATE TRIGGER recovery_cluster_point_immutable BEFORE INSERT OR UPDATE OR DELETE ON recovery.recovery_cluster_point
FOR EACH ROW EXECUTE FUNCTION recovery.reject_child_mutation();
DROP TRIGGER IF EXISTS recovery_object_root_immutable ON recovery.recovery_object_root;
CREATE TRIGGER recovery_object_root_immutable BEFORE INSERT OR UPDATE OR DELETE ON recovery.recovery_object_root
FOR EACH ROW EXECUTE FUNCTION recovery.reject_child_mutation();

CREATE INDEX IF NOT EXISTS recovery_set_target_status_idx ON recovery.recovery_set (target_id, status, created_at DESC, set_id DESC);
CREATE INDEX IF NOT EXISTS recovery_validation_set_idx ON recovery.validation_attempt (set_id, started_at DESC, attempt_id DESC);

REVOKE ALL ON SCHEMA recovery FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA recovery FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA recovery FROM PUBLIC;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_migrator') THEN
        GRANT USAGE ON SCHEMA recovery TO leapview_control_migrator;
        GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA recovery TO leapview_control_migrator;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA recovery TO leapview_control_runtime;
        GRANT SELECT ON ALL TABLES IN SCHEMA recovery TO leapview_control_runtime;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON ALL TABLES IN SCHEMA recovery FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA recovery TO leapview_control_maintenance;
        GRANT SELECT, INSERT, UPDATE ON recovery.recovery_set, recovery.validation_attempt TO leapview_control_maintenance;
        GRANT SELECT, INSERT ON recovery.recovery_cluster_point, recovery.recovery_object_root, recovery.validation_result TO leapview_control_maintenance;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA recovery TO leapview_control_backup;
        GRANT SELECT ON ALL TABLES IN SCHEMA recovery TO leapview_control_backup;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA recovery TO leapview_control_readonly;
        GRANT SELECT ON ALL TABLES IN SCHEMA recovery TO leapview_control_readonly;
    END IF;
END
$$;
