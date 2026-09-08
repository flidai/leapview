-- Lifecycle projections intentionally enumerate metadata columns and never
-- select spec_canonical_json. Payloads are fetched only by exact revision
-- token after the caller has authorized the lifecycle projection.
-- name: GetSavedExplorationLifecycle :one
SELECT e.project_id, e.exploration_id, e.owner_principal_id, e.title, e.slug,
       e.visibility, e.status, e.semantic_model_id, e.created_at, e.updated_at,
       e.archived_at, r.revision_id, r.revision_number, r.content_hash,
       r.created_by, r.created_at AS revision_created_at,
       r.serving_project_id, r.serving_environment, r.serving_generation_id
FROM saved_explorations e
JOIN saved_exploration_revisions r
  ON r.project_id = e.project_id
 AND r.exploration_id = e.exploration_id
 AND r.revision_id = e.current_revision_id
 AND r.revision_number = e.current_revision_number
 AND r.content_hash = e.current_content_hash
WHERE e.project_id = sqlc.arg(project_id)
  AND e.exploration_id = sqlc.arg(exploration_id);

-- name: ListSavedExplorationLifecycles :many
SELECT e.project_id, e.exploration_id, e.owner_principal_id, e.title, e.slug,
       e.visibility, e.status, e.semantic_model_id, e.created_at, e.updated_at,
       e.archived_at, r.revision_id, r.revision_number, r.content_hash,
       r.created_by, r.created_at AS revision_created_at,
       r.serving_project_id, r.serving_environment, r.serving_generation_id
FROM saved_explorations e
JOIN saved_exploration_revisions r
  ON r.project_id = e.project_id
 AND r.exploration_id = e.exploration_id
 AND r.revision_id = e.current_revision_id
 AND r.revision_number = e.current_revision_number
 AND r.content_hash = e.current_content_hash
WHERE e.project_id = sqlc.arg(project_id)
  AND (sqlc.arg(include_archived) <> 0 OR e.status = 'active')
  AND (sqlc.arg(cursor) = '' OR e.exploration_id > sqlc.arg(cursor))
-- API cursors are keyed by immutable exploration identity. Ordering by that
-- same key keeps a page token stable when editable metadata changes between
-- requests.
ORDER BY e.exploration_id
LIMIT sqlc.arg(limit);

-- name: GetSavedExplorationRevision :one
SELECT project_id, exploration_id, revision_id, revision_number,
       spec_envelope_version, spec_canonical_json, content_hash, created_by,
       created_at, serving_project_id, serving_environment, serving_generation_id
FROM saved_exploration_revisions
WHERE project_id = sqlc.arg(project_id)
  AND exploration_id = sqlc.arg(exploration_id)
  AND revision_id = sqlc.arg(revision_id)
  AND revision_number = sqlc.arg(revision_number)
  AND content_hash = sqlc.arg(content_hash);

-- name: GetSavedExplorationOperation :one
SELECT project_id, actor_id, operation_kind, idempotency_key,
       request_fingerprint, result_exploration_id, result_owner_principal_id,
       result_title, result_slug, result_visibility, result_status,
       result_semantic_model_id, result_created_at, result_updated_at,
       result_archived_at, result_revision_id, result_revision_number,
       result_content_hash, result_revision_created_at,
       result_revision_created_by, result_serving_project_id,
       result_serving_environment, result_serving_generation_id,
       evidence_version, evidence_request_id, evidence_correlation_id,
       evidence_admin_override, evidence_admin_reason, evidence_occurred_at,
       created_at
FROM saved_exploration_operations
WHERE project_id = sqlc.arg(project_id)
  AND actor_id = sqlc.arg(actor_id)
  AND operation_kind = sqlc.arg(operation_kind)
  AND idempotency_key = sqlc.arg(idempotency_key);

-- name: InsertSavedExploration :exec
INSERT INTO saved_explorations
 (project_id, exploration_id, owner_principal_id, title, slug, visibility,
  status, semantic_model_id, created_at, updated_at, archived_at,
  current_revision_id, current_revision_number, current_content_hash)
VALUES
 (sqlc.arg(project_id), sqlc.arg(exploration_id), sqlc.arg(owner_principal_id),
  sqlc.arg(title), sqlc.arg(slug), sqlc.arg(visibility), 'active',
  sqlc.arg(semantic_model_id), sqlc.arg(created_at), sqlc.arg(updated_at), NULL,
  sqlc.arg(current_revision_id), sqlc.arg(current_revision_number),
  sqlc.arg(current_content_hash));

-- name: InsertSavedExplorationRevision :exec
INSERT INTO saved_exploration_revisions
 (project_id, exploration_id, revision_id, revision_number,
  spec_envelope_version, spec_canonical_json, content_hash, created_by,
  created_at, serving_project_id, serving_environment, serving_generation_id)
VALUES
 (sqlc.arg(project_id), sqlc.arg(exploration_id), sqlc.arg(revision_id),
  sqlc.arg(revision_number), sqlc.arg(spec_envelope_version),
  sqlc.arg(spec_canonical_json), sqlc.arg(content_hash), sqlc.arg(created_by),
  sqlc.arg(created_at), sqlc.arg(serving_project_id),
  sqlc.arg(serving_environment), sqlc.arg(serving_generation_id));

-- name: InsertSavedExplorationOperation :exec
INSERT INTO saved_exploration_operations
 (project_id, actor_id, operation_kind, idempotency_key, request_fingerprint,
  result_exploration_id, result_owner_principal_id, result_title, result_slug,
  result_visibility, result_status, result_semantic_model_id,
  result_created_at, result_updated_at, result_archived_at, result_revision_id,
  result_revision_number, result_content_hash, result_revision_created_at,
  result_revision_created_by, result_serving_project_id,
  result_serving_environment, result_serving_generation_id, evidence_version,
  evidence_request_id, evidence_correlation_id, evidence_admin_override,
  evidence_admin_reason, evidence_occurred_at, created_at)
VALUES
 (sqlc.arg(project_id), sqlc.arg(actor_id), sqlc.arg(operation_kind),
  sqlc.arg(idempotency_key), sqlc.arg(request_fingerprint),
  sqlc.arg(result_exploration_id), sqlc.arg(result_owner_principal_id),
  sqlc.arg(result_title), sqlc.arg(result_slug), sqlc.arg(result_visibility),
  sqlc.arg(result_status), sqlc.arg(result_semantic_model_id),
  sqlc.arg(result_created_at), sqlc.arg(result_updated_at),
  sqlc.arg(result_archived_at), sqlc.arg(result_revision_id),
  sqlc.arg(result_revision_number), sqlc.arg(result_content_hash),
  sqlc.arg(result_revision_created_at), sqlc.arg(result_revision_created_by),
  sqlc.arg(result_serving_project_id), sqlc.arg(result_serving_environment),
  sqlc.arg(result_serving_generation_id), sqlc.arg(evidence_version),
  sqlc.arg(evidence_request_id), sqlc.arg(evidence_correlation_id),
  sqlc.arg(evidence_admin_override), sqlc.arg(evidence_admin_reason),
  sqlc.arg(evidence_occurred_at), sqlc.arg(created_at));

-- name: UpdateSavedExplorationVersion :execresult
UPDATE saved_explorations
SET title = sqlc.arg(title), slug = sqlc.arg(slug),
    visibility = sqlc.arg(visibility), semantic_model_id = sqlc.arg(semantic_model_id),
    updated_at = sqlc.arg(updated_at), current_revision_id = sqlc.arg(revision_id),
    current_revision_number = sqlc.arg(revision_number),
    current_content_hash = sqlc.arg(content_hash)
WHERE project_id = sqlc.arg(project_id)
  AND exploration_id = sqlc.arg(exploration_id)
  AND status = 'active'
  AND current_revision_id = sqlc.arg(expected_revision_id)
  AND current_revision_number = sqlc.arg(expected_revision_number)
  AND current_content_hash = sqlc.arg(expected_content_hash);

-- name: ArchiveSavedExploration :execresult
UPDATE saved_explorations
SET status = 'archived', archived_at = sqlc.arg(archived_at),
    updated_at = sqlc.arg(archived_at)
WHERE project_id = sqlc.arg(project_id)
  AND exploration_id = sqlc.arg(exploration_id)
  AND status = 'active'
  AND current_revision_id = sqlc.arg(expected_revision_id)
  AND current_revision_number = sqlc.arg(expected_revision_number)
  AND current_content_hash = sqlc.arg(expected_content_hash);
