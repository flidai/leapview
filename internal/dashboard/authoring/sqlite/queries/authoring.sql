-- These queries are part of the dashboard sqlc group.  The repository keeps
-- transaction orchestration in Go, while this checked-in set documents the
-- durable authoring projection and remains available to generated consumers.

-- name: GetAuthoringDashboard :one
SELECT *
FROM dashboard_authoring_dashboards
WHERE workspace_id = sqlc.arg(workspace_id)
  AND dashboard_id = sqlc.arg(dashboard_id);

-- name: ListAuthoringDashboards :many
SELECT *
FROM dashboard_authoring_dashboards
WHERE workspace_id = sqlc.arg(workspace_id)
ORDER BY slug, dashboard_id;

-- name: CountAuthoringDashboardsBySemanticModel :many
SELECT semantic_model,
       COUNT(CASE WHEN visibility = 'private' THEN 1 END) AS private_count,
       COUNT(CASE WHEN visibility = 'shared' THEN 1 END) AS shared_count,
       COUNT(*) AS total_count
FROM dashboard_authoring_dashboards
WHERE workspace_id = sqlc.arg(workspace_id)
  AND status <> 'archived'
GROUP BY semantic_model
ORDER BY semantic_model;

-- name: GetAuthoringRevision :one
SELECT *
FROM dashboard_authoring_revisions
WHERE workspace_id = sqlc.arg(workspace_id)
  AND dashboard_id = sqlc.arg(dashboard_id)
  AND revision_id = sqlc.arg(revision_id);

-- name: ListAuthoringRevisions :many
SELECT *
FROM dashboard_authoring_revisions
WHERE workspace_id = sqlc.arg(workspace_id)
  AND dashboard_id = sqlc.arg(dashboard_id)
ORDER BY revision_number;

-- name: GetAuthoringDraft :one
SELECT *
FROM dashboard_authoring_drafts
WHERE workspace_id = sqlc.arg(workspace_id)
  AND dashboard_id = sqlc.arg(dashboard_id);

-- name: GetAuthoringPublished :one
SELECT *
FROM dashboard_authoring_published
WHERE workspace_id = sqlc.arg(workspace_id)
  AND dashboard_id = sqlc.arg(dashboard_id);

-- name: GetAuthoringPublishedCompilation :one
SELECT *
FROM dashboard_authoring_compiled_revisions
WHERE workspace_id = sqlc.arg(workspace_id)
  AND dashboard_id = sqlc.arg(dashboard_id)
  AND revision_id = sqlc.arg(revision_id)
  AND revision_number = sqlc.arg(revision_number)
  AND content_hash = sqlc.arg(content_hash)
  AND definition_hash = sqlc.arg(definition_hash)
  AND semantic_serving_state_id = sqlc.arg(semantic_serving_state_id);

-- name: InsertAuthoringDashboard :exec
INSERT INTO dashboard_authoring_dashboards
  (workspace_id, dashboard_id, owner_principal_id, slug, title, semantic_model, visibility, status)
VALUES (sqlc.arg(workspace_id), sqlc.arg(dashboard_id), sqlc.arg(owner_principal_id), sqlc.arg(slug), sqlc.arg(title), sqlc.arg(semantic_model), sqlc.arg(visibility), sqlc.arg(status));

-- name: UpdateAuthoringDashboard :execresult
UPDATE dashboard_authoring_dashboards
SET slug = sqlc.arg(slug), title = sqlc.arg(title), semantic_model = sqlc.arg(semantic_model), visibility = sqlc.arg(visibility),
    status = sqlc.arg(status), updated_at = CURRENT_TIMESTAMP
WHERE workspace_id = sqlc.arg(workspace_id)
  AND dashboard_id = sqlc.arg(dashboard_id);

-- name: InsertAuthoringRevision :exec
INSERT INTO dashboard_authoring_revisions
  (workspace_id, dashboard_id, revision_id, revision_number, document_json, content_hash, provenance_json, created_at)
VALUES (sqlc.arg(workspace_id), sqlc.arg(dashboard_id), sqlc.arg(revision_id), sqlc.arg(revision_number),
        sqlc.arg(document_json), sqlc.arg(content_hash), sqlc.arg(provenance_json), sqlc.arg(created_at));

-- name: InsertAuthoringDraft :exec
INSERT INTO dashboard_authoring_drafts
  (workspace_id, dashboard_id, draft_id, revision_id, revision_number, content_hash, provenance_json)
VALUES (sqlc.arg(workspace_id), sqlc.arg(dashboard_id), sqlc.arg(draft_id), sqlc.arg(revision_id),
        sqlc.arg(revision_number), sqlc.arg(content_hash), sqlc.arg(provenance_json));

-- name: UpdateAuthoringDraft :execresult
UPDATE dashboard_authoring_drafts
SET revision_id = sqlc.arg(revision_id), revision_number = sqlc.arg(revision_number),
    content_hash = sqlc.arg(content_hash), provenance_json = sqlc.arg(provenance_json), updated_at = CURRENT_TIMESTAMP
WHERE workspace_id = sqlc.arg(workspace_id)
  AND dashboard_id = sqlc.arg(dashboard_id)
  AND revision_id = sqlc.arg(expected_revision_id)
  AND revision_number = sqlc.arg(expected_revision_number)
  AND content_hash = sqlc.arg(expected_content_hash);

-- name: InsertAuthoringPublished :exec
INSERT INTO dashboard_authoring_published
  (workspace_id, dashboard_id, revision_id, revision_number, content_hash,
   compiled_revision_id, compiled_revision_number, compiled_content_hash,
   compiled_definition_hash, compiled_semantic_serving_state_id, provenance_json, published_at)
VALUES (sqlc.arg(workspace_id), sqlc.arg(dashboard_id), sqlc.arg(revision_id), sqlc.arg(revision_number),
        sqlc.arg(content_hash), sqlc.arg(compiled_revision_id), sqlc.arg(compiled_revision_number), sqlc.arg(compiled_content_hash),
        sqlc.arg(compiled_definition_hash), sqlc.arg(compiled_semantic_serving_state_id), sqlc.arg(provenance_json), sqlc.arg(published_at));

-- name: UpsertAuthoringPublished :exec
INSERT INTO dashboard_authoring_published
  (workspace_id, dashboard_id, revision_id, revision_number, content_hash,
   compiled_revision_id, compiled_revision_number, compiled_content_hash,
   compiled_definition_hash, compiled_semantic_serving_state_id, provenance_json, published_at)
VALUES (sqlc.arg(workspace_id), sqlc.arg(dashboard_id), sqlc.arg(revision_id), sqlc.arg(revision_number),
        sqlc.arg(content_hash), sqlc.arg(compiled_revision_id), sqlc.arg(compiled_revision_number), sqlc.arg(compiled_content_hash),
        sqlc.arg(compiled_definition_hash), sqlc.arg(compiled_semantic_serving_state_id), sqlc.arg(provenance_json), sqlc.arg(published_at))
ON CONFLICT(workspace_id, dashboard_id) DO UPDATE SET
  revision_id = excluded.revision_id, revision_number = excluded.revision_number,
  content_hash = excluded.content_hash, compiled_revision_id = excluded.compiled_revision_id,
  compiled_revision_number = excluded.compiled_revision_number, compiled_content_hash = excluded.compiled_content_hash,
  compiled_definition_hash = excluded.compiled_definition_hash,
  compiled_semantic_serving_state_id = excluded.compiled_semantic_serving_state_id,
  provenance_json = excluded.provenance_json,
  published_at = excluded.published_at;

-- name: InsertAuthoringCompiledRevision :exec
INSERT INTO dashboard_authoring_compiled_revisions
  (workspace_id, dashboard_id, revision_id, revision_number, content_hash,
   definition_json, definition_hash, semantic_serving_state_id, compiled_at)
VALUES (sqlc.arg(workspace_id), sqlc.arg(dashboard_id), sqlc.arg(revision_id), sqlc.arg(revision_number), sqlc.arg(content_hash),
        sqlc.arg(definition_json), sqlc.arg(definition_hash), sqlc.arg(semantic_serving_state_id), sqlc.arg(compiled_at));

-- name: GetAuthoringCommand :one
SELECT *
FROM dashboard_authoring_commands
WHERE workspace_id = sqlc.arg(workspace_id)
  AND dashboard_id = sqlc.arg(dashboard_id)
  AND command_id = sqlc.arg(command_id);

-- name: InsertAuthoringCommand :exec
INSERT INTO dashboard_authoring_commands
  (workspace_id, dashboard_id, command_id, request_fingerprint, action, provenance_json, occurred_at,
   result_revision_id, result_revision_number, result_content_hash)
VALUES (sqlc.arg(workspace_id), sqlc.arg(dashboard_id), sqlc.arg(command_id), sqlc.arg(request_fingerprint), sqlc.arg(action), sqlc.arg(provenance_json), sqlc.arg(occurred_at),
        sqlc.arg(result_revision_id), sqlc.arg(result_revision_number), sqlc.arg(result_content_hash));

-- name: ArchiveAuthoringDashboard :execresult
UPDATE dashboard_authoring_dashboards
SET status = 'archived', updated_at = CURRENT_TIMESTAMP
WHERE workspace_id = sqlc.arg(workspace_id)
  AND dashboard_id = sqlc.arg(dashboard_id);
