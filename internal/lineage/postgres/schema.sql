-- Durable, immutable lineage graph projection (FAI-568).
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
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    CHECK (graph_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (graph_version > 0),
    CHECK (project_id = btrim(project_id) AND octet_length(project_id) BETWEEN 1 AND 256)
);

CREATE TABLE IF NOT EXISTS lineage.nodes (
    graph_digest TEXT NOT NULL REFERENCES lineage.graphs(graph_digest) ON DELETE CASCADE,
    node_id TEXT NOT NULL,
    resource_kind TEXT NOT NULL,
    identity_digest TEXT NOT NULL,
    properties JSONB NOT NULL,
    PRIMARY KEY (graph_digest, node_id),
    CHECK (node_id = btrim(node_id) AND octet_length(node_id) BETWEEN 1 AND 256),
    CHECK (resource_kind = btrim(resource_kind) AND octet_length(resource_kind) BETWEEN 1 AND 128),
    CHECK (identity_digest ~ '^sha256:[0-9a-f]{64}$'),
    CHECK (jsonb_typeof(properties) = 'object' AND octet_length(properties::text) <= 65536)
);

CREATE TABLE IF NOT EXISTS lineage.edges (
    graph_digest TEXT NOT NULL REFERENCES lineage.graphs(graph_digest) ON DELETE CASCADE,
    from_node_id TEXT NOT NULL,
    to_node_id TEXT NOT NULL,
    relation TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (graph_digest, from_node_id, to_node_id),
    FOREIGN KEY (graph_digest, from_node_id)
        REFERENCES lineage.nodes(graph_digest, node_id) ON DELETE CASCADE,
    FOREIGN KEY (graph_digest, to_node_id)
        REFERENCES lineage.nodes(graph_digest, node_id) ON DELETE CASCADE,
    CHECK (from_node_id <> to_node_id),
    CHECK (relation = btrim(relation) AND octet_length(relation) <= 128)
);

-- A binding is the only serving-facing selector.  Delivery and generation
-- are explicit, immutable identities; no environment or graph metadata is
-- inferred by this capability.
CREATE TABLE IF NOT EXISTS lineage.bindings (
    delivery_id TEXT NOT NULL,
    generation_id TEXT NOT NULL,
    graph_digest TEXT NOT NULL REFERENCES lineage.graphs(graph_digest) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (delivery_id, generation_id),
    CHECK (delivery_id = btrim(delivery_id) AND octet_length(delivery_id) BETWEEN 1 AND 256),
    CHECK (generation_id = btrim(generation_id) AND octet_length(generation_id) BETWEEN 1 AND 256)
);

CREATE INDEX IF NOT EXISTS lineage_edges_from_idx
    ON lineage.edges (graph_digest, from_node_id, to_node_id);
CREATE INDEX IF NOT EXISTS lineage_edges_to_idx
    ON lineage.edges (graph_digest, to_node_id, from_node_id);
CREATE INDEX IF NOT EXISTS lineage_bindings_graph_idx
    ON lineage.bindings (graph_digest, delivery_id, generation_id);

CREATE OR REPLACE FUNCTION lineage.reject_immutable_change()
RETURNS trigger
LANGUAGE plpgsql
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
