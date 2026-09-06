-- +goose Up
SET LOCAL ROLE leapview_control_owner;

-- Project authoring was removed in FAI-666. Retained source snapshots now
-- identify only the deterministic source file set; no authored entrypoint is
-- stored or required by source admission.
ALTER TABLE project.source_sync_plan
    DROP COLUMN IF EXISTS project_file;
ALTER TABLE project.source_snapshot
    DROP COLUMN IF EXISTS project_file;

-- FAI-666 source identity v2 is the only identity admitted by new writes.
-- Existing baseline rows are retained as v1 for inspection and historical
-- integrity, but remain quarantined from every runtime read and transition.
-- A NOT VALID check preserves those rows while still rejecting new v1 rows.
ALTER TABLE project.source_snapshot
    ADD COLUMN source_identity_version integer NOT NULL DEFAULT 1;
ALTER TABLE project.source_snapshot
    ALTER COLUMN source_identity_version SET DEFAULT 2;
ALTER TABLE project.source_snapshot
    ADD CONSTRAINT source_snapshot_source_identity_version_v2_ck
    CHECK (source_identity_version >= 2) NOT VALID;

ALTER TABLE project.source_sync_plan
    ADD COLUMN source_identity_version integer NOT NULL DEFAULT 1;
ALTER TABLE project.source_sync_plan
    ALTER COLUMN source_identity_version SET DEFAULT 2;
ALTER TABLE project.source_sync_plan
    ADD CONSTRAINT source_sync_plan_source_identity_version_v2_ck
    CHECK (source_identity_version >= 2) NOT VALID;

CREATE OR REPLACE FUNCTION project.guard_source_sync_plan_mutation()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, project AS $$
BEGIN
    IF NEW.plan_id IS DISTINCT FROM OLD.plan_id OR NEW.operation_id IS DISTINCT FROM OLD.operation_id
       OR NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.storage_security_domain IS DISTINCT FROM OLD.storage_security_domain
       OR NEW.source_identity_version IS DISTINCT FROM OLD.source_identity_version
       OR NEW.owner_id IS DISTINCT FROM OLD.owner_id OR NEW.candidate_key IS DISTINCT FROM OLD.candidate_key
       OR NEW.source_digest IS DISTINCT FROM OLD.source_digest
       OR NEW.request_digest IS DISTINCT FROM OLD.request_digest OR NEW.expires_at IS DISTINCT FROM OLD.expires_at
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'source synchronization plan identity is immutable';
    END IF;
    IF OLD.source_identity_version <> 2 OR NEW.source_identity_version <> 2
       OR OLD.state <> 'open' OR NEW.state NOT IN ('committed', 'expired') OR NEW.state = OLD.state THEN
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
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION project.guard_source_snapshot_mutation()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, project AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'project source history is immutable';
    END IF;
    IF NEW.snapshot_id IS DISTINCT FROM OLD.snapshot_id OR NEW.project_id IS DISTINCT FROM OLD.project_id
       OR NEW.source_identity_version IS DISTINCT FROM OLD.source_identity_version
       OR NEW.storage_security_domain IS DISTINCT FROM OLD.storage_security_domain OR NEW.source_digest IS DISTINCT FROM OLD.source_digest
       OR NEW.project_digest IS DISTINCT FROM OLD.project_digest
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
    IF OLD.source_identity_version <> 2 OR NEW.source_identity_version <> 2
       OR OLD.state <> 'building' OR NEW.state <> 'sealed' OR NEW.sealed_at IS NOT NULL THEN
        RAISE EXCEPTION 'project source snapshot transition is invalid';
    END IF;
    NEW.sealed_at := clock_timestamp();
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION project.guard_source_sync_plan_entry_insert()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, project AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM project.source_sync_plan p
        WHERE p.plan_id = NEW.plan_id AND p.source_identity_version = 2
          AND p.state = 'open' AND p.expires_at > clock_timestamp()
    ) THEN
        RAISE EXCEPTION 'source synchronization plan is not open';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION project.guard_source_snapshot_child_insert()
-- +goose StatementBegin
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog, project AS $$
DECLARE parent_id uuid;
BEGIN
    parent_id := NEW.snapshot_id;
    IF NOT EXISTS (
        SELECT 1 FROM project.source_snapshot s
        WHERE s.snapshot_id = parent_id AND s.source_identity_version = 2
          AND s.state = 'building'
    ) THEN
        RAISE EXCEPTION 'project source snapshot is not building';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- A portable graph digest is intentionally identical when the same source
-- bundle is delivered to two Projects. Scope lineage storage by the external
-- Project identity instead of contaminating that portable digest.
ALTER TABLE lineage.revisions DROP CONSTRAINT IF EXISTS revisions_graph_digest_project_id_fkey;
ALTER TABLE lineage.nodes DROP CONSTRAINT IF EXISTS nodes_graph_digest_project_id_fkey;
ALTER TABLE lineage.edges DROP CONSTRAINT IF EXISTS edges_graph_digest_project_id_fkey;
ALTER TABLE lineage.edges DROP CONSTRAINT IF EXISTS edges_graph_digest_from_node_id_fkey;
ALTER TABLE lineage.edges DROP CONSTRAINT IF EXISTS edges_graph_digest_to_node_id_fkey;
ALTER TABLE lineage.bindings DROP CONSTRAINT IF EXISTS bindings_graph_digest_project_id_fkey;

ALTER TABLE lineage.graphs DROP CONSTRAINT IF EXISTS graphs_pkey;
ALTER TABLE lineage.nodes DROP CONSTRAINT IF EXISTS nodes_pkey;
ALTER TABLE lineage.edges DROP CONSTRAINT IF EXISTS edges_pkey;
DROP INDEX IF EXISTS lineage.lineage_graphs_project_digest_uq;

ALTER TABLE lineage.graphs
    ADD CONSTRAINT graphs_pkey PRIMARY KEY (project_id, graph_digest);
ALTER TABLE lineage.nodes
    ADD CONSTRAINT nodes_pkey PRIMARY KEY (project_id, graph_digest, node_id);
ALTER TABLE lineage.edges
    ADD CONSTRAINT edges_pkey PRIMARY KEY (project_id, graph_digest, from_node_id, to_node_id);

-- Existing rootful v1 projections are retained for forensic/upgrade purposes,
-- but the quarantine applies to every graph admitted after this migration.
-- NOT VALID is intentional: validating would reject those retained rows.
ALTER TABLE lineage.graphs
    ADD CONSTRAINT graphs_graph_version_v2_ck
    CHECK (graph_version >= 2) NOT VALID;

ALTER TABLE lineage.revisions
    ADD CONSTRAINT revisions_project_graph_fkey
    FOREIGN KEY (project_id, graph_digest)
    REFERENCES lineage.graphs(project_id, graph_digest) ON DELETE RESTRICT;
ALTER TABLE lineage.nodes
    ADD CONSTRAINT nodes_project_graph_fkey
    FOREIGN KEY (project_id, graph_digest)
    REFERENCES lineage.graphs(project_id, graph_digest) ON DELETE CASCADE;
ALTER TABLE lineage.edges
    ADD CONSTRAINT edges_project_graph_fkey
    FOREIGN KEY (project_id, graph_digest)
    REFERENCES lineage.graphs(project_id, graph_digest) ON DELETE CASCADE,
    ADD CONSTRAINT edges_from_node_fkey
    FOREIGN KEY (project_id, graph_digest, from_node_id)
    REFERENCES lineage.nodes(project_id, graph_digest, node_id) ON DELETE CASCADE,
    ADD CONSTRAINT edges_to_node_fkey
    FOREIGN KEY (project_id, graph_digest, to_node_id)
    REFERENCES lineage.nodes(project_id, graph_digest, node_id) ON DELETE CASCADE;
ALTER TABLE lineage.bindings
    ADD CONSTRAINT bindings_project_graph_fkey
    FOREIGN KEY (project_id, graph_digest)
    REFERENCES lineage.graphs(project_id, graph_digest) ON DELETE RESTRICT;

-- Reinstall publication against the upgraded composite-key graph authority.
-- The graph-version guard prevents a caller from reconstructing or publishing
-- a retained v1 projection through the SECURITY DEFINER path.
CREATE OR REPLACE FUNCTION lineage.publish_revision(p_project_id TEXT, p_scope_id TEXT, p_graph_digest TEXT)
RETURNS TABLE(project_id TEXT, scope_id TEXT, revision_id BIGINT, graph_digest TEXT, valid_from TIMESTAMPTZ, valid_to TIMESTAMPTZ, created_at TIMESTAMPTZ)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, lineage
-- +goose StatementBegin
AS $$
DECLARE
    current_row lineage.revisions%ROWTYPE;
    next_revision BIGINT;
BEGIN
    IF p_project_id IS NULL OR p_scope_id IS NULL OR p_graph_digest IS NULL THEN
        RAISE EXCEPTION 'lineage publication identity is required';
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM lineage.graphs g
        WHERE g.project_id = p_project_id
          AND g.graph_digest = p_graph_digest
          AND g.graph_version = 2
    ) THEN
        RAISE EXCEPTION 'lineage graph is not a canonical v2 projection';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended(p_project_id || '|' || p_scope_id, 0));
    SELECT r.* INTO current_row
      FROM lineage.revisions r
      JOIN lineage.graphs g
        ON g.project_id = r.project_id
       AND g.graph_digest = r.graph_digest
       AND g.graph_version = 2
     WHERE r.project_id = p_project_id
       AND r.scope_id = p_scope_id
       AND r.valid_to IS NULL;
    IF FOUND AND current_row.graph_digest = p_graph_digest THEN
        project_id := current_row.project_id;
        scope_id := current_row.scope_id;
        revision_id := current_row.revision_id;
        graph_digest := current_row.graph_digest;
        valid_from := current_row.valid_from;
        valid_to := current_row.valid_to;
        created_at := current_row.created_at;
        RETURN NEXT;
        RETURN;
    END IF;
    SELECT COALESCE(MAX(r.revision_id), 0) + 1 INTO next_revision
      FROM lineage.revisions r
     WHERE r.project_id = p_project_id AND r.scope_id = p_scope_id;
    UPDATE lineage.revisions r
       SET valid_to = GREATEST(clock_timestamp(), r.valid_from + interval '1 microsecond')
     WHERE r.project_id = p_project_id AND r.scope_id = p_scope_id AND r.valid_to IS NULL;
    INSERT INTO lineage.revisions (project_id, scope_id, revision_id, graph_digest)
    VALUES (p_project_id, p_scope_id, next_revision, p_graph_digest)
    RETURNING lineage.revisions.project_id, lineage.revisions.scope_id,
              lineage.revisions.revision_id, lineage.revisions.graph_digest,
              lineage.revisions.valid_from, lineage.revisions.valid_to,
              lineage.revisions.created_at
         INTO project_id, scope_id, revision_id, graph_digest,
              valid_from, valid_to, created_at;
    RETURN NEXT;
END;
$$;
-- +goose StatementEnd

DROP INDEX IF EXISTS lineage.lineage_revisions_graph_idx;
CREATE INDEX lineage_revisions_graph_idx
    ON lineage.revisions (project_id, graph_digest);
DROP INDEX IF EXISTS lineage.lineage_edges_from_idx;
DROP INDEX IF EXISTS lineage.lineage_edges_to_idx;
DROP INDEX IF EXISTS lineage.lineage_nodes_project_idx;
-- The composite edge primary key already covers (project_id, graph_digest,
-- from_node_id, to_node_id); retain only the reverse-direction lookup index.
DROP INDEX IF EXISTS lineage.lineage_edges_project_from_idx;

-- Return to the migrator login before Goose records version 2 in its standard
-- table, which is intentionally owned by that login rather than the durable
-- product-object owner role.
RESET ROLE;
