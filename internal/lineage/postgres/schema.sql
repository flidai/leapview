-- Durable, immutable lineage graph authority (ADR-0020 / FAI-568).
--
-- This is a capability-owned schema.  It is deliberately independent from
-- the control-plane baseline so conformance tests (and a future deployment
-- migration) can apply it in isolation.
CREATE SCHEMA IF NOT EXISTS lineage;

CREATE TABLE IF NOT EXISTS lineage.graphs (
    graph_digest TEXT PRIMARY KEY,
    graph_version INTEGER NOT NULL,
    project_id TEXT NOT NULL,
    node_count INTEGER NOT NULL CHECK (node_count BETWEEN 1 AND 100000),
    edge_count INTEGER NOT NULL CHECK (edge_count BETWEEN 0 AND 500000),
    compiler_version INTEGER NOT NULL DEFAULT 1 CHECK (compiler_version > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    CHECK (graph_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (graph_version > 0),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 256)
);

-- A graph digest is immutable, while a scope advances through revisions.  A
-- scope is deliberately explicit (for example a target or serving lane); it
-- is never inferred from a delivery or environment name.  valid_from and
-- valid_to form a non-overlapping half-open validity interval.  PostgreSQL
-- owns all timestamps so callers cannot back-date or forge publication
-- evidence.
CREATE UNIQUE INDEX IF NOT EXISTS lineage_graphs_project_digest_uq
    ON lineage.graphs (project_id, graph_digest);

CREATE TABLE IF NOT EXISTS lineage.revisions (
    project_id   TEXT NOT NULL,
    scope_id     TEXT NOT NULL,
    revision_id  BIGINT NOT NULL,
    graph_digest TEXT NOT NULL,
    valid_from   TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    valid_to     TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (project_id, scope_id, revision_id),
    FOREIGN KEY (graph_digest, project_id)
        REFERENCES lineage.graphs(graph_digest, project_id) ON DELETE RESTRICT,
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 256),
    CHECK (scope_id = btrim(scope_id) AND octet_length(scope_id) BETWEEN 1 AND 256),
    CHECK (revision_id > 0),
    CHECK (valid_to IS NULL OR valid_to > valid_from),
    CHECK (created_at >= valid_from)
);

CREATE UNIQUE INDEX IF NOT EXISTS lineage_revisions_current_uq
    ON lineage.revisions (project_id, scope_id)
    WHERE valid_to IS NULL;
CREATE INDEX IF NOT EXISTS lineage_revisions_scope_validity_idx
    ON lineage.revisions (project_id, scope_id, valid_from DESC, revision_id DESC);
CREATE INDEX IF NOT EXISTS lineage_revisions_graph_idx
    ON lineage.revisions (graph_digest, project_id);

CREATE OR REPLACE FUNCTION lineage.enforce_revision_validity()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, lineage
AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF NEW.project_id IS DISTINCT FROM OLD.project_id
           OR NEW.scope_id IS DISTINCT FROM OLD.scope_id
           OR NEW.revision_id IS DISTINCT FROM OLD.revision_id
           OR NEW.graph_digest IS DISTINCT FROM OLD.graph_digest
           OR NEW.valid_from IS DISTINCT FROM OLD.valid_from
           OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
            RAISE EXCEPTION 'lineage revision identity and valid_from are immutable';
        END IF;
        IF OLD.valid_to IS NOT NULL AND NEW.valid_to IS DISTINCT FROM OLD.valid_to THEN
            RAISE EXCEPTION 'lineage revision validity is immutable after closure';
        END IF;
        IF NEW.valid_to IS NOT NULL AND NEW.valid_to <= NEW.valid_from THEN
            RAISE EXCEPTION 'lineage revision valid_to must be after valid_from';
        END IF;
        IF EXISTS (
            SELECT 1 FROM lineage.revisions r
             WHERE r.project_id = NEW.project_id AND r.scope_id = NEW.scope_id
               AND (r.project_id, r.scope_id, r.revision_id) <> (NEW.project_id, NEW.scope_id, NEW.revision_id)
               AND tstzrange(r.valid_from, COALESCE(r.valid_to, 'infinity'::timestamptz), '[)')
                   && tstzrange(NEW.valid_from, COALESCE(NEW.valid_to, 'infinity'::timestamptz), '[)')
        ) THEN
            RAISE EXCEPTION 'lineage revision validity overlaps existing scope revision';
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'lineage revisions are immutable';
    END IF;
    IF EXISTS (
        SELECT 1 FROM lineage.revisions r
         WHERE r.project_id = NEW.project_id AND r.scope_id = NEW.scope_id
           AND tstzrange(r.valid_from, COALESCE(r.valid_to, 'infinity'::timestamptz), '[)')
               && tstzrange(NEW.valid_from, COALESCE(NEW.valid_to, 'infinity'::timestamptz), '[)')
    ) THEN
        RAISE EXCEPTION 'lineage revision validity overlaps existing scope revision';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS lineage_revisions_validity ON lineage.revisions;
CREATE TRIGGER lineage_revisions_validity
    BEFORE INSERT OR UPDATE OR DELETE ON lineage.revisions
    FOR EACH ROW EXECUTE FUNCTION lineage.enforce_revision_validity();

-- The application runtime is not granted UPDATE on revisions.  Publication
-- executes through this narrowly scoped SECURITY DEFINER function owned by
-- the migration/owner role, preserving atomic replacement without broadening
-- table privileges.  The caller must already have admitted the referenced
-- graph through the normal graph/node/edge inserts in the same transaction.
CREATE OR REPLACE FUNCTION lineage.publish_revision(p_project_id TEXT, p_scope_id TEXT, p_graph_digest TEXT)
RETURNS TABLE(project_id TEXT, scope_id TEXT, revision_id BIGINT, graph_digest TEXT, valid_from TIMESTAMPTZ, valid_to TIMESTAMPTZ, created_at TIMESTAMPTZ)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, lineage
AS $$
DECLARE
    current_row lineage.revisions%ROWTYPE;
    next_revision BIGINT;
BEGIN
    IF p_project_id IS NULL OR p_scope_id IS NULL OR p_graph_digest IS NULL THEN
        RAISE EXCEPTION 'lineage publication identity is required';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended(p_project_id || '|' || p_scope_id, 0));
    SELECT * INTO current_row
      FROM lineage.revisions
     WHERE lineage.revisions.project_id = p_project_id
       AND lineage.revisions.scope_id = p_scope_id
       AND lineage.revisions.valid_to IS NULL;
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

CREATE TABLE IF NOT EXISTS lineage.nodes (
    graph_digest TEXT NOT NULL,
    project_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    resource_kind TEXT NOT NULL,
    identity_digest TEXT NOT NULL,
    properties JSONB NOT NULL,
    PRIMARY KEY (graph_digest, node_id),
    FOREIGN KEY (graph_digest, project_id)
        REFERENCES lineage.graphs(graph_digest, project_id) ON DELETE CASCADE,
    CHECK (node_id = btrim(node_id) AND octet_length(node_id) BETWEEN 1 AND 256),
    CHECK (resource_kind = btrim(resource_kind) AND octet_length(resource_kind) BETWEEN 1 AND 128),
    CHECK (identity_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (jsonb_typeof(properties) = 'object' AND octet_length(properties::text) <= 65536),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 256)
);

CREATE TABLE IF NOT EXISTS lineage.edges (
    graph_digest TEXT NOT NULL,
    project_id TEXT NOT NULL,
    from_node_id TEXT NOT NULL,
    to_node_id TEXT NOT NULL,
    relation TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (graph_digest, from_node_id, to_node_id),
    FOREIGN KEY (graph_digest, project_id)
        REFERENCES lineage.graphs(graph_digest, project_id) ON DELETE CASCADE,
    FOREIGN KEY (graph_digest, from_node_id)
        REFERENCES lineage.nodes(graph_digest, node_id) ON DELETE CASCADE,
    FOREIGN KEY (graph_digest, to_node_id)
        REFERENCES lineage.nodes(graph_digest, node_id) ON DELETE CASCADE,
    CHECK (from_node_id <> to_node_id),
    CHECK (relation = btrim(relation) AND octet_length(relation) <= 128),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 256)
);

-- Traversal functions keep the two fixed recursive result shapes in the
-- capability schema so sqlc can expose typed methods.  The caller still
-- supplies the generation digest and access-authority allow-set; every
-- recursive step joins that allow-set, preventing denied transit nodes.
CREATE OR REPLACE FUNCTION lineage.traverse_upstream(
    p_graph_digest TEXT,
    p_project_id TEXT,
    p_root_id TEXT,
    p_allowed TEXT[],
    p_max_depth INTEGER,
    p_row_limit INTEGER
)
RETURNS TABLE(node_id TEXT, resource_kind TEXT, identity_digest TEXT, properties JSONB, depth INTEGER)
LANGUAGE SQL STABLE
SET search_path = pg_catalog, lineage
AS $function$
WITH RECURSIVE allowed(node_id) AS (SELECT unnest(p_allowed)), walk(node_id, depth) AS (
    SELECT p_root_id, 0
    UNION
    SELECT e.to_node_id, w.depth + 1
    FROM walk w
    JOIN lineage.edges e ON e.graph_digest = p_graph_digest
        AND e.project_id = p_project_id
        AND e.from_node_id = w.node_id
    JOIN allowed a ON a.node_id = e.to_node_id
    WHERE w.depth < p_max_depth
)
SELECT node_id, resource_kind, identity_digest, properties, depth
FROM (
    SELECT DISTINCT ON (n.node_id)
        n.node_id, n.resource_kind, n.identity_digest, n.properties, w.depth
    FROM walk w
    JOIN allowed a ON a.node_id = w.node_id
    JOIN lineage.nodes n ON n.graph_digest = p_graph_digest
        AND n.project_id = p_project_id
        AND n.node_id = w.node_id
    ORDER BY n.node_id, w.depth
) unique_nodes
ORDER BY depth, node_id
LIMIT p_row_limit
$function$;

CREATE OR REPLACE FUNCTION lineage.traverse_downstream(
    p_graph_digest TEXT,
    p_project_id TEXT,
    p_root_id TEXT,
    p_allowed TEXT[],
    p_max_depth INTEGER,
    p_row_limit INTEGER
)
RETURNS TABLE(node_id TEXT, resource_kind TEXT, identity_digest TEXT, properties JSONB, depth INTEGER)
LANGUAGE SQL STABLE
SET search_path = pg_catalog, lineage
AS $function$
WITH RECURSIVE allowed(node_id) AS (SELECT unnest(p_allowed)), walk(node_id, depth) AS (
    SELECT p_root_id, 0
    UNION
    SELECT e.from_node_id, w.depth + 1
    FROM walk w
    JOIN lineage.edges e ON e.graph_digest = p_graph_digest
        AND e.project_id = p_project_id
        AND e.to_node_id = w.node_id
    JOIN allowed a ON a.node_id = e.from_node_id
    WHERE w.depth < p_max_depth
)
SELECT node_id, resource_kind, identity_digest, properties, depth
FROM (
    SELECT DISTINCT ON (n.node_id)
        n.node_id, n.resource_kind, n.identity_digest, n.properties, w.depth
    FROM walk w
    JOIN allowed a ON a.node_id = w.node_id
    JOIN lineage.nodes n ON n.graph_digest = p_graph_digest
        AND n.project_id = p_project_id
        AND n.node_id = w.node_id
    ORDER BY n.node_id, w.depth
) unique_nodes
ORDER BY depth, node_id
LIMIT p_row_limit
$function$;

CREATE OR REPLACE FUNCTION lineage.count_upstream_edges(
    p_graph_digest TEXT,
    p_project_id TEXT,
    p_root_id TEXT,
    p_allowed TEXT[],
    p_max_depth INTEGER
)
RETURNS BIGINT
LANGUAGE SQL STABLE
SET search_path = pg_catalog, lineage
AS $function$
WITH RECURSIVE allowed(node_id) AS (SELECT unnest(p_allowed)), walk(node_id, depth) AS (
    SELECT p_root_id, 0
    UNION
    SELECT e.to_node_id, w.depth + 1
    FROM walk w
    JOIN lineage.edges e ON e.graph_digest = p_graph_digest
        AND e.project_id = p_project_id
        AND e.from_node_id = w.node_id
    JOIN allowed a ON a.node_id = e.to_node_id
    WHERE w.depth < p_max_depth
)
SELECT count(*)
FROM walk w
JOIN lineage.edges e ON e.graph_digest = p_graph_digest
    AND e.project_id = p_project_id
    AND e.from_node_id = w.node_id
JOIN allowed a ON a.node_id = e.to_node_id
WHERE w.depth < p_max_depth
$function$;

CREATE OR REPLACE FUNCTION lineage.count_downstream_edges(
    p_graph_digest TEXT,
    p_project_id TEXT,
    p_root_id TEXT,
    p_allowed TEXT[],
    p_max_depth INTEGER
)
RETURNS BIGINT
LANGUAGE SQL STABLE
SET search_path = pg_catalog, lineage
AS $function$
WITH RECURSIVE allowed(node_id) AS (SELECT unnest(p_allowed)), walk(node_id, depth) AS (
    SELECT p_root_id, 0
    UNION
    SELECT e.from_node_id, w.depth + 1
    FROM walk w
    JOIN lineage.edges e ON e.graph_digest = p_graph_digest
        AND e.project_id = p_project_id
        AND e.to_node_id = w.node_id
    JOIN allowed a ON a.node_id = e.from_node_id
    WHERE w.depth < p_max_depth
)
SELECT count(*)
FROM walk w
JOIN lineage.edges e ON e.graph_digest = p_graph_digest
    AND e.project_id = p_project_id
    AND e.to_node_id = w.node_id
JOIN allowed a ON a.node_id = e.from_node_id
WHERE w.depth < p_max_depth
$function$;

-- A binding is the only serving-facing selector.  Delivery and generation
-- are explicit, immutable identities; no environment or graph metadata is
-- inferred by this capability.
CREATE TABLE IF NOT EXISTS lineage.bindings (
    delivery_id TEXT NOT NULL,
    generation_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    graph_digest TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (delivery_id, generation_id),
    FOREIGN KEY (graph_digest, project_id)
        REFERENCES lineage.graphs(graph_digest, project_id) ON DELETE RESTRICT,
    CHECK (delivery_id = btrim(delivery_id) AND octet_length(delivery_id) BETWEEN 1 AND 256),
    CHECK (generation_id = btrim(generation_id) AND octet_length(generation_id) BETWEEN 1 AND 256),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 256)
);

CREATE INDEX IF NOT EXISTS lineage_edges_from_idx
    ON lineage.edges (graph_digest, from_node_id, to_node_id);
CREATE INDEX IF NOT EXISTS lineage_edges_to_idx
    ON lineage.edges (graph_digest, to_node_id, from_node_id);
CREATE INDEX IF NOT EXISTS lineage_nodes_project_idx
    ON lineage.nodes (project_id, graph_digest, node_id);
CREATE INDEX IF NOT EXISTS lineage_edges_project_from_idx
    ON lineage.edges (project_id, graph_digest, from_node_id, to_node_id);
CREATE INDEX IF NOT EXISTS lineage_edges_project_to_idx
    ON lineage.edges (project_id, graph_digest, to_node_id, from_node_id);
CREATE INDEX IF NOT EXISTS lineage_bindings_graph_idx
    ON lineage.bindings (project_id, graph_digest, delivery_id, generation_id);

CREATE OR REPLACE FUNCTION lineage.reject_immutable_change()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, lineage
AS $$
BEGIN
    RAISE EXCEPTION 'lineage projections and bindings are immutable';
END;
$$;

DROP TRIGGER IF EXISTS lineage_graphs_immutable ON lineage.graphs;
CREATE TRIGGER lineage_graphs_immutable
    BEFORE UPDATE OR DELETE ON lineage.graphs
    FOR EACH ROW EXECUTE FUNCTION lineage.reject_immutable_change();
DROP TRIGGER IF EXISTS lineage_nodes_immutable ON lineage.nodes;
CREATE TRIGGER lineage_nodes_immutable
    BEFORE UPDATE OR DELETE ON lineage.nodes
    FOR EACH ROW EXECUTE FUNCTION lineage.reject_immutable_change();
DROP TRIGGER IF EXISTS lineage_edges_immutable ON lineage.edges;
CREATE TRIGGER lineage_edges_immutable
    BEFORE UPDATE OR DELETE ON lineage.edges
    FOR EACH ROW EXECUTE FUNCTION lineage.reject_immutable_change();
DROP TRIGGER IF EXISTS lineage_bindings_immutable ON lineage.bindings;
CREATE TRIGGER lineage_bindings_immutable
    BEFORE UPDATE OR DELETE ON lineage.bindings
    FOR EACH ROW EXECUTE FUNCTION lineage.reject_immutable_change();

-- PUBLIC must not discover or mutate lineage state. Runtime may admit
-- immutable graph/binding rows and invoke the narrow publication function, but
-- cannot directly mutate revision rows. Conditional grants keep isolated
-- tests usable when deployment roles have not yet been provisioned.
REVOKE ALL ON SCHEMA lineage FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA lineage FROM PUBLIC;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA lineage FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA lineage FROM PUBLIC;
DO $$
DECLARE role_name TEXT;
BEGIN
    FOREACH role_name IN ARRAY ARRAY['leapview_control_owner','leapview_control_migrator','leapview_control_runtime','leapview_control_readonly','leapview_control_backup'] LOOP
        IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = role_name) THEN
            EXECUTE format('GRANT USAGE ON SCHEMA lineage TO %I', role_name);
            EXECUTE format('GRANT EXECUTE ON FUNCTION lineage.traverse_upstream(text,text,text,text[],integer,integer), lineage.traverse_downstream(text,text,text,text[],integer,integer), lineage.count_upstream_edges(text,text,text,text[],integer), lineage.count_downstream_edges(text,text,text,text[],integer) TO %I', role_name);
            IF role_name IN ('leapview_control_owner','leapview_control_migrator') THEN
                EXECUTE format('GRANT ALL ON ALL TABLES IN SCHEMA lineage TO %I', role_name);
                EXECUTE format('GRANT ALL ON ALL SEQUENCES IN SCHEMA lineage TO %I', role_name);
                EXECUTE format('GRANT EXECUTE ON FUNCTION lineage.publish_revision(text,text,text) TO %I', role_name);
            ELSE
                EXECUTE format('GRANT SELECT ON ALL TABLES IN SCHEMA lineage TO %I', role_name);
                IF role_name = 'leapview_control_runtime' THEN
                    EXECUTE format('GRANT INSERT ON lineage.graphs, lineage.nodes, lineage.edges, lineage.bindings TO %I', role_name);
                    EXECUTE format('REVOKE INSERT, UPDATE, DELETE ON lineage.revisions FROM %I', role_name);
                    EXECUTE format('GRANT EXECUTE ON FUNCTION lineage.publish_revision(text,text,text) TO %I', role_name);
                END IF;
            END IF;
        END IF;
    END LOOP;
END
$$;
