-- Immutable serving-generation evidence.  Delivery owns lifecycle status and
-- active selection; this capability stores only the generation bundle that a
-- delivery transaction admits and the reader leases rooted at its snapshot
-- seal.  There is intentionally no serving-state status or pointer table.
CREATE SCHEMA IF NOT EXISTS serving_state;

CREATE TABLE IF NOT EXISTS serving_state.bundle (
    generation_id uuid PRIMARY KEY REFERENCES delivery.delivery_generation(generation_id) ON DELETE RESTRICT,
    project_id text NOT NULL CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 255 AND project_id ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    environment text NOT NULL CHECK (environment = btrim(environment) AND octet_length(environment) BETWEEN 1 AND 128 AND environment ~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$'),
    artifact_id text NOT NULL CHECK (artifact_id = btrim(artifact_id) AND artifact_id = 'artifact-' || substr(artifact_digest, 8) AND octet_length(artifact_id) BETWEEN 1 AND 255),
    artifact_digest text NOT NULL CHECK (artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    compiled_graph_digest text NOT NULL CHECK (compiled_graph_digest ~ '^sha256:[0-9a-f]{64}$'),
    artifact_format text NOT NULL CHECK (artifact_format = 'tar.gz'),
    -- Immutable object-storage key/locator. This is deliberately not a
    -- production filesystem path; filesystem paths never enter this native
    -- schema.
    artifact_locator text NOT NULL CHECK (artifact_locator = btrim(artifact_locator) AND artifact_locator = 'serving-artifacts/' || substr(artifact_digest, 8) || '.tar.gz' AND octet_length(artifact_locator) BETWEEN 1 AND 2048),
    storage_security_domain text NOT NULL CHECK (storage_security_domain = btrim(storage_security_domain) AND octet_length(storage_security_domain) BETWEEN 1 AND 512 AND storage_security_domain !~ '[[:cntrl:]]'),
    artifact_content_type text NOT NULL CHECK (artifact_content_type = 'application/gzip'),
    artifact_metadata_digest text NOT NULL CHECK (artifact_metadata_digest ~ '^sha256:[0-9a-f]{64}$'),
    manifest_json jsonb NOT NULL CHECK (jsonb_typeof(manifest_json) = 'object' AND octet_length(manifest_json::text) <= 1048576),
    project_digest text NOT NULL CHECK (project_digest ~ '^sha256:[0-9a-f]{64}$'),
    access_policy_json jsonb NOT NULL CHECK (jsonb_typeof(access_policy_json) = 'object' AND octet_length(access_policy_json::text) <= 1048576),
    dashboard_publications_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(dashboard_publications_json) = 'object' AND octet_length(dashboard_publications_json::text) <= 1048576),
    dashboard_appearances_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(dashboard_appearances_json) = 'object' AND octet_length(dashboard_appearances_json::text) <= 1048576),
    size_bytes bigint NOT NULL CHECK (size_bytes BETWEEN 1 AND 67108864),
    created_by text NOT NULL CHECK (created_by = btrim(created_by) AND octet_length(created_by) BETWEEN 1 AND 255 AND created_by !~ '[[:cntrl:]]'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (generation_id, project_id, environment)
);

CREATE TABLE IF NOT EXISTS serving_state.asset (
    generation_id uuid NOT NULL REFERENCES serving_state.bundle(generation_id) ON DELETE RESTRICT,
    snapshot_id text NOT NULL CHECK (snapshot_id = btrim(snapshot_id) AND octet_length(snapshot_id) BETWEEN 1 AND 255),
    logical_asset_id text NOT NULL CHECK (logical_asset_id = btrim(logical_asset_id) AND octet_length(logical_asset_id) BETWEEN 1 AND 255),
    asset_type text NOT NULL CHECK (octet_length(asset_type) BETWEEN 1 AND 64),
    asset_key text NOT NULL CHECK (octet_length(asset_key) BETWEEN 1 AND 255),
    parent_logical_asset_id text NOT NULL DEFAULT '' CHECK (octet_length(parent_logical_asset_id) <= 255),
    title text NOT NULL DEFAULT '' CHECK (octet_length(title) <= 512),
    description text NOT NULL DEFAULT '' CHECK (octet_length(description) <= 4096),
    source_file text NOT NULL DEFAULT '' CHECK (octet_length(source_file) <= 2048),
    payload_schema text NOT NULL CHECK (octet_length(payload_schema) BETWEEN 1 AND 128),
    payload_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload_json) = 'object' AND octet_length(payload_json::text) <= 1048576),
    content_hash text NOT NULL CHECK (octet_length(content_hash) BETWEEN 1 AND 255),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (generation_id, logical_asset_id),
    UNIQUE (snapshot_id)
);

CREATE TABLE IF NOT EXISTS serving_state.asset_edge (
    generation_id uuid NOT NULL REFERENCES serving_state.bundle(generation_id) ON DELETE RESTRICT,
    id text NOT NULL CHECK (id = btrim(id) AND octet_length(id) BETWEEN 1 AND 255),
    from_logical_asset_id text NOT NULL CHECK (octet_length(from_logical_asset_id) BETWEEN 1 AND 255),
    to_logical_asset_id text NOT NULL CHECK (octet_length(to_logical_asset_id) BETWEEN 1 AND 255),
    edge_type text NOT NULL CHECK (octet_length(edge_type) BETWEEN 1 AND 128),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (generation_id, id),
    UNIQUE (generation_id, from_logical_asset_id, to_logical_asset_id, edge_type),
    FOREIGN KEY (generation_id, from_logical_asset_id) REFERENCES serving_state.asset(generation_id, logical_asset_id),
    FOREIGN KEY (generation_id, to_logical_asset_id) REFERENCES serving_state.asset(generation_id, logical_asset_id)
);

CREATE TABLE IF NOT EXISTS serving_state.reader_lease (
    lease_id text PRIMARY KEY CHECK (lease_id = btrim(lease_id) AND octet_length(lease_id) BETWEEN 1 AND 255),
    generation_id uuid NOT NULL REFERENCES delivery.delivery_generation(generation_id) ON DELETE RESTRICT,
    ducklake_snapshot_id bigint NOT NULL CHECK (ducklake_snapshot_id > 0),
    owner_id text NOT NULL CHECK (owner_id = btrim(owner_id) AND octet_length(owner_id) BETWEEN 1 AND 255),
    acquired_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    released_at timestamptz,
    CHECK (expires_at > acquired_at),
    CHECK (released_at IS NULL OR released_at >= acquired_at)
);
CREATE INDEX IF NOT EXISTS reader_lease_live_idx ON serving_state.reader_lease (generation_id, ducklake_snapshot_id, expires_at) WHERE released_at IS NULL;
CREATE INDEX IF NOT EXISTS bundle_scope_idx ON serving_state.bundle (project_id, environment, created_at DESC);
CREATE INDEX IF NOT EXISTS asset_generation_idx ON serving_state.asset (generation_id, asset_type, asset_key);

-- Row locking is performed by a fixed-search-path, read-only definer so the
-- runtime role needs no UPDATE privilege on delivery retention evidence.
--
-- A candidate preview is allowed to lease the exact snapshot sealed on its
-- delivery generation while that candidate root remains live.  Activation
-- still creates the generation root; candidate roots are intentionally bound
-- through the generation's candidate_id, generation_id and snapshot seal
-- rather than by a mutable serving pointer. Candidate roots are always
-- bounded by an explicit expiry; generation roots may remain unbounded.
CREATE OR REPLACE FUNCTION serving_state.guard_reader_snapshot_retention(p_generation uuid, p_snapshot bigint)
RETURNS boolean SECURITY DEFINER LANGUAGE plpgsql SET search_path = serving_state, delivery, pg_catalog AS $$
BEGIN
    RETURN EXISTS (
        SELECT 1
        FROM delivery.delivery_generation g
        JOIN delivery.delivery_snapshot_seal s ON s.seal_id = g.snapshot_seal_id
        JOIN delivery.delivery_retention_root r
          ON r.target_id = g.target_id
         AND r.snapshot_seal_id = s.seal_id
         AND (
             (r.root_kind = 'generation' AND r.generation_id = g.generation_id)
             OR (r.root_kind = 'candidate' AND r.candidate_id = g.candidate_id AND r.generation_id = g.generation_id)
         )
        WHERE g.generation_id = p_generation AND s.ducklake_snapshot_id = p_snapshot
          AND r.state = 'live'
          AND (
              (r.root_kind = 'generation' AND (r.expires_at IS NULL OR r.expires_at > clock_timestamp()))
              OR (r.root_kind = 'candidate' AND r.expires_at IS NOT NULL AND r.expires_at > clock_timestamp())
          )
        FOR SHARE OF r
    );
END; $$;

-- Bundle scope and digest identity must agree with the delivery generation and
-- its target.  The target/generation rows are immutable delivery evidence.
CREATE OR REPLACE FUNCTION serving_state.validate_bundle_generation() RETURNS trigger LANGUAGE plpgsql SET search_path = serving_state, delivery, pg_catalog AS $$
DECLARE target_project text; target_environment text; generation_digest text; generation_graph_digest text;
BEGIN
    SELECT t.project_id, t.environment, g.serving_artifact_digest, g.compiled_graph_digest
      INTO target_project, target_environment, generation_digest, generation_graph_digest
      FROM delivery.delivery_generation g JOIN delivery.delivery_target t ON t.target_id = g.target_id
     WHERE g.generation_id = NEW.generation_id;
    IF target_project IS NULL OR NEW.project_id <> target_project OR NEW.environment <> target_environment THEN
        RAISE EXCEPTION 'serving bundle scope does not match delivery generation';
    END IF;
    IF NEW.artifact_digest <> generation_digest THEN
        RAISE EXCEPTION 'serving bundle artifact digest does not match delivery generation';
    END IF;
    IF NEW.compiled_graph_digest <> generation_graph_digest THEN
        RAISE EXCEPTION 'serving bundle graph digest does not match delivery generation';
    END IF;
    RETURN NEW;
END; $$;
DROP TRIGGER IF EXISTS bundle_generation_consistency ON serving_state.bundle;
CREATE TRIGGER bundle_generation_consistency BEFORE INSERT ON serving_state.bundle FOR EACH ROW EXECUTE FUNCTION serving_state.validate_bundle_generation();

CREATE OR REPLACE FUNCTION serving_state.reject_bundle_mutation() RETURNS trigger LANGUAGE plpgsql SET search_path = serving_state, delivery, pg_catalog AS $$
BEGIN RAISE EXCEPTION 'serving generation bundle evidence is immutable'; END; $$;
DROP TRIGGER IF EXISTS bundle_immutable ON serving_state.bundle;
CREATE TRIGGER bundle_immutable BEFORE UPDATE OR DELETE ON serving_state.bundle FOR EACH ROW EXECUTE FUNCTION serving_state.reject_bundle_mutation();
DROP TRIGGER IF EXISTS asset_immutable ON serving_state.asset;
CREATE TRIGGER asset_immutable BEFORE UPDATE OR DELETE ON serving_state.asset FOR EACH ROW EXECUTE FUNCTION serving_state.reject_bundle_mutation();
DROP TRIGGER IF EXISTS asset_edge_immutable ON serving_state.asset_edge;
CREATE TRIGGER asset_edge_immutable BEFORE UPDATE OR DELETE ON serving_state.asset_edge FOR EACH ROW EXECUTE FUNCTION serving_state.reject_bundle_mutation();

-- Reader leases are the sole mutable serving rows. They are query leases, not
-- an independent retention authority: every lease must reference a live
-- delivery generation retention root and may only move once from live to
-- released (or extend forward while that root remains live).
CREATE OR REPLACE FUNCTION serving_state.validate_reader_lease_mutation() RETURNS trigger SECURITY DEFINER LANGUAGE plpgsql SET search_path = serving_state, delivery, pg_catalog AS $$
DECLARE expected_snapshot bigint;
BEGIN
    IF TG_OP = 'INSERT' THEN
        SELECT s.ducklake_snapshot_id INTO expected_snapshot
          FROM delivery.delivery_generation g
          JOIN delivery.delivery_snapshot_seal s ON s.seal_id = g.snapshot_seal_id
         WHERE g.generation_id = NEW.generation_id;
        IF expected_snapshot IS NULL OR expected_snapshot <> NEW.ducklake_snapshot_id THEN
            RAISE EXCEPTION 'reader lease snapshot does not match delivery snapshot seal';
        END IF;
        IF NOT serving_state.guard_reader_snapshot_retention(NEW.generation_id, NEW.ducklake_snapshot_id) THEN
            RAISE EXCEPTION 'reader lease requires a live delivery retention root';
        END IF;
        IF NEW.expires_at <= clock_timestamp() OR NEW.expires_at > clock_timestamp() + interval '24 hours' THEN
            RAISE EXCEPTION 'reader lease expiry must be within the 24-hour DB-clock window';
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.lease_id <> OLD.lease_id OR NEW.generation_id <> OLD.generation_id OR NEW.ducklake_snapshot_id <> OLD.ducklake_snapshot_id OR NEW.owner_id <> OLD.owner_id OR NEW.acquired_at <> OLD.acquired_at THEN
        RAISE EXCEPTION 'reader lease identity is immutable';
    END IF;
    IF OLD.released_at IS NOT NULL AND (NEW.released_at IS DISTINCT FROM OLD.released_at OR NEW.expires_at IS DISTINCT FROM OLD.expires_at) THEN
        RAISE EXCEPTION 'released reader lease is immutable';
    END IF;
    IF NEW.released_at IS NULL AND NEW.expires_at < OLD.expires_at THEN
        RAISE EXCEPTION 'reader lease expiry cannot move backwards';
    END IF;
    -- Direct UPDATE callers receive the same retention fence as the
    -- repository's renewal query.  In particular, a renewal must take a
    -- share lock on the exact live delivery root before it can move the
    -- expiry forward; this serializes with delivery root retirement/expiry
    -- and prevents extending a lease after its root has expired.
    IF NEW.released_at IS NULL AND NEW.expires_at > OLD.expires_at
       AND NOT serving_state.guard_reader_snapshot_retention(NEW.generation_id, NEW.ducklake_snapshot_id) THEN
        RAISE EXCEPTION 'reader lease renewal requires a live delivery retention root';
    END IF;
    RETURN NEW;
END; $$;
DROP TRIGGER IF EXISTS reader_lease_mutation ON serving_state.reader_lease;
CREATE TRIGGER reader_lease_mutation BEFORE INSERT OR UPDATE ON serving_state.reader_lease FOR EACH ROW EXECUTE FUNCTION serving_state.validate_reader_lease_mutation();

-- Expired query-lease release is an operational capability, not
-- request-serving authority. Delivery owns retention-root lifecycle and this
-- capability deliberately does not mutate those roots or immutable delivery
-- evidence. A maintenance batch only advances reader-lease release markers
-- in a bounded, deterministic order.
CREATE OR REPLACE FUNCTION serving_state.release_expired_query_snapshot_leases(
    p_environment text,
    p_limit integer
)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, serving_state, delivery
AS $$
DECLARE
    removed bigint := 0;
BEGIN
    IF p_environment IS NULL OR p_environment <> btrim(p_environment)
       OR octet_length(p_environment) < 1 OR octet_length(p_environment) > 128
       OR p_environment !~ '^[A-Za-z0-9][A-Za-z0-9_.:-]*$' THEN
        RAISE EXCEPTION 'serving-state retention environment is invalid';
    END IF;
    IF p_limit IS NULL OR p_limit < 1 OR p_limit > 1000 THEN
        RAISE EXCEPTION 'serving-state retention limit must be between 1 and 1000';
    END IF;
    WITH doomed AS (
        SELECT l.lease_id
        FROM serving_state.reader_lease l
        JOIN delivery.delivery_generation g ON g.generation_id = l.generation_id
        JOIN delivery.delivery_target t ON t.target_id = g.target_id
        WHERE t.environment = p_environment
          AND l.released_at IS NULL
          AND l.expires_at <= clock_timestamp()
        ORDER BY l.expires_at, l.lease_id
        LIMIT p_limit
        FOR UPDATE OF l SKIP LOCKED
    )
    UPDATE serving_state.reader_lease l
       SET released_at = clock_timestamp()
      FROM doomed d
     WHERE l.lease_id = d.lease_id;
    GET DIAGNOSTICS removed = ROW_COUNT;
    RETURN removed;
END;
$$;

REVOKE ALL ON SCHEMA serving_state FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA serving_state FROM PUBLIC;
REVOKE ALL ON FUNCTION serving_state.guard_reader_snapshot_retention(uuid,bigint) FROM PUBLIC;
REVOKE ALL ON FUNCTION serving_state.validate_reader_lease_mutation() FROM PUBLIC;
REVOKE ALL ON FUNCTION serving_state.release_expired_query_snapshot_leases(text,integer) FROM PUBLIC;
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_owner') THEN
        GRANT ALL ON FUNCTION serving_state.release_expired_query_snapshot_leases(text,integer) TO leapview_control_owner;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_migrator') THEN
        GRANT ALL ON FUNCTION serving_state.release_expired_query_snapshot_leases(text,integer) TO leapview_control_migrator;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA serving_state TO leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION serving_state.guard_reader_snapshot_retention(uuid,bigint) TO leapview_control_runtime;
        REVOKE EXECUTE ON FUNCTION serving_state.release_expired_query_snapshot_leases(text,integer) FROM leapview_control_runtime;
        GRANT SELECT, INSERT ON serving_state.bundle, serving_state.asset, serving_state.asset_edge TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON serving_state.reader_lease TO leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA serving_state TO leapview_control_maintenance;
        GRANT EXECUTE ON FUNCTION serving_state.release_expired_query_snapshot_leases(text,integer) TO leapview_control_maintenance;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA serving_state TO leapview_control_readonly;
        GRANT SELECT ON ALL TABLES IN SCHEMA serving_state TO leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA serving_state TO leapview_control_backup;
        GRANT SELECT ON ALL TABLES IN SCHEMA serving_state TO leapview_control_backup;
    END IF;
END $$;
