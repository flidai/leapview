-- Static PostgreSQL query leaves for the compiler-owned lineage graph.

-- name: InsertGraph :one
INSERT INTO lineage.graphs
    (graph_digest, graph_version, project_id, node_count, edge_count)
VALUES (sqlc.arg(graph_digest), sqlc.arg(graph_version), sqlc.arg(project_id), sqlc.arg(node_count), sqlc.arg(edge_count))
ON CONFLICT (project_id, graph_digest) DO NOTHING
RETURNING graph_digest;

-- name: InsertNode :exec
INSERT INTO lineage.nodes
    (graph_digest, project_id, node_id, resource_kind, identity_digest, properties)
VALUES (sqlc.arg(graph_digest), sqlc.arg(project_id), sqlc.arg(node_id), sqlc.arg(resource_kind), sqlc.arg(identity_digest), sqlc.arg(properties));

-- name: InsertEdge :exec
INSERT INTO lineage.edges
    (graph_digest, project_id, from_node_id, to_node_id, relation)
VALUES (sqlc.arg(graph_digest), sqlc.arg(project_id), sqlc.arg(from_node_id), sqlc.arg(to_node_id), sqlc.arg(relation));

-- name: GetGraphProjectID :one
SELECT project_id
FROM lineage.graphs
WHERE project_id = sqlc.arg(project_id)
  AND graph_digest = sqlc.arg(graph_digest)
  AND graph_version = 2;

-- name: InsertBinding :one
INSERT INTO lineage.bindings
    (delivery_id, generation_id, project_id, graph_digest)
VALUES (sqlc.arg(delivery_id), sqlc.arg(generation_id), sqlc.arg(project_id), sqlc.arg(graph_digest))
ON CONFLICT (delivery_id, generation_id) DO NOTHING
RETURNING graph_digest;

-- name: GetBinding :one
SELECT b.graph_digest, b.project_id
FROM lineage.bindings b
JOIN lineage.graphs g
  ON g.project_id = b.project_id
 AND g.graph_digest = b.graph_digest
 AND g.graph_version = 2
WHERE b.delivery_id = sqlc.arg(delivery_id)
  AND b.generation_id = sqlc.arg(generation_id);

-- name: PublishRevision :one
WITH published(row) AS (
    SELECT lineage.publish_revision(sqlc.arg(project_id), sqlc.arg(scope_id), sqlc.arg(graph_digest))
)
SELECT ((row).project_id)::text AS project_id,
       ((row).scope_id)::text AS scope_id,
       ((row).revision_id)::bigint AS revision_id,
       ((row).graph_digest)::text AS graph_digest,
       ((row).valid_from)::timestamptz AS valid_from,
       ((row).valid_to)::timestamptz AS valid_to,
       ((row).created_at)::timestamptz AS created_at
FROM published;

-- name: GetCurrentRevision :one
SELECT r.project_id, r.scope_id, r.revision_id, r.graph_digest, r.valid_from, r.valid_to, r.created_at
FROM lineage.revisions r
JOIN lineage.graphs g
  ON g.project_id = r.project_id
 AND g.graph_digest = r.graph_digest
 AND g.graph_version = 2
WHERE r.project_id = sqlc.arg(project_id)
  AND r.scope_id = sqlc.arg(scope_id)
  AND r.valid_to IS NULL;

-- name: GetRevision :one
SELECT r.project_id, r.scope_id, r.revision_id, r.graph_digest, r.valid_from, r.valid_to, r.created_at
FROM lineage.revisions r
JOIN lineage.graphs g
  ON g.project_id = r.project_id
 AND g.graph_digest = r.graph_digest
 AND g.graph_version = 2
WHERE r.project_id = sqlc.arg(project_id)
  AND r.scope_id = sqlc.arg(scope_id)
  AND r.revision_id = sqlc.arg(revision_id);

-- name: GetGraphMetadata :one
SELECT graph_version, project_id, node_count, edge_count
FROM lineage.graphs
WHERE project_id = sqlc.arg(project_id)
  AND graph_digest = sqlc.arg(graph_digest)
  AND graph_version = 2;

-- name: ListNodes :many
SELECT n.project_id, n.node_id, n.resource_kind, n.identity_digest, n.properties
FROM lineage.nodes n
JOIN lineage.graphs g
  ON g.project_id = n.project_id
 AND g.graph_digest = n.graph_digest
 AND g.graph_version = 2
WHERE n.project_id = sqlc.arg(project_id)
  AND n.graph_digest = sqlc.arg(graph_digest)
ORDER BY n.node_id
LIMIT sqlc.arg(row_limit);

-- name: ListEdges :many
SELECT e.project_id, e.from_node_id, e.to_node_id, e.relation
FROM lineage.edges e
JOIN lineage.graphs g
  ON g.project_id = e.project_id
 AND g.graph_digest = e.graph_digest
 AND g.graph_version = 2
WHERE e.project_id = sqlc.arg(project_id)
  AND e.graph_digest = sqlc.arg(graph_digest)
ORDER BY e.from_node_id, e.to_node_id
LIMIT sqlc.arg(row_limit);

-- name: GetRevisionDigest :one
SELECT r.graph_digest
FROM lineage.revisions r
JOIN lineage.graphs g
  ON g.project_id = r.project_id
 AND g.graph_digest = r.graph_digest
 AND g.graph_version = 2
WHERE r.project_id = sqlc.arg(project_id)
  AND r.scope_id = sqlc.arg(scope_id)
  AND r.valid_to IS NULL;

-- name: GetBindingDigestForProject :one
SELECT b.graph_digest
FROM lineage.bindings b
JOIN lineage.graphs g
  ON g.project_id = b.project_id
 AND g.graph_digest = b.graph_digest
 AND g.graph_version = 2
WHERE b.project_id = sqlc.arg(project_id)
  AND b.delivery_id = sqlc.arg(delivery_id)
  AND b.generation_id = sqlc.arg(generation_id);

-- name: NodeExists :one
SELECT EXISTS (
    SELECT 1
    FROM lineage.nodes n
    JOIN lineage.graphs g
      ON g.project_id = n.project_id
     AND g.graph_digest = n.graph_digest
     AND g.graph_version = 2
    WHERE n.project_id = sqlc.arg(project_id)
      AND n.graph_digest = sqlc.arg(graph_digest)
      AND n.node_id = sqlc.arg(node_id)
);

-- name: TraverseUpstream :many
WITH result(row) AS (
    SELECT lineage.traverse_upstream(sqlc.arg(graph_digest), sqlc.arg(project_id), sqlc.arg(root_id), sqlc.arg(allowed)::text[], sqlc.arg(max_depth), sqlc.arg(row_limit))
)
SELECT ((row).node_id)::text AS node_id,
       ((row).resource_kind)::text AS resource_kind,
       ((row).identity_digest)::text AS identity_digest,
       ((row).properties)::jsonb AS properties,
       ((row).depth)::integer AS depth
FROM result;

-- name: TraverseDownstream :many
WITH result(row) AS (
    SELECT lineage.traverse_downstream(sqlc.arg(graph_digest), sqlc.arg(project_id), sqlc.arg(root_id), sqlc.arg(allowed)::text[], sqlc.arg(max_depth), sqlc.arg(row_limit))
)
SELECT ((row).node_id)::text AS node_id,
       ((row).resource_kind)::text AS resource_kind,
       ((row).identity_digest)::text AS identity_digest,
       ((row).properties)::jsonb AS properties,
       ((row).depth)::integer AS depth
FROM result;

-- name: CountUpstreamEdges :one
SELECT lineage.count_upstream_edges(sqlc.arg(graph_digest), sqlc.arg(project_id), sqlc.arg(root_id), sqlc.arg(allowed)::text[], sqlc.arg(max_depth))::bigint AS edge_count;

-- name: CountDownstreamEdges :one
SELECT lineage.count_downstream_edges(sqlc.arg(graph_digest), sqlc.arg(project_id), sqlc.arg(root_id), sqlc.arg(allowed)::text[], sqlc.arg(max_depth))::bigint AS edge_count;
