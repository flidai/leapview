-- Static PostgreSQL query leaves for the durable development-session pointer.

-- name: GetDevelopmentSession :one
SELECT id, owner_id, checkout_id, worktree_id, project_id, target_id, environment,
       attempted_candidate_id, attempted_artifact_digest, attempted_graph_digest,
       attempted_preview_url, last_valid_candidate_id, last_valid_artifact_digest,
       last_valid_graph_digest, last_valid_preview_url, diagnostics_json,
       revision, created_at, updated_at
FROM project.development_session
WHERE owner_id = sqlc.arg(owner_id)
  AND checkout_id = sqlc.arg(checkout_id)
  AND worktree_id = sqlc.arg(worktree_id)
  AND project_id = sqlc.arg(project_id)
  AND target_id = sqlc.arg(target_id)
  AND environment = sqlc.arg(environment);

-- name: GetDevelopmentSessionForUpdate :one
SELECT id, owner_id, checkout_id, worktree_id, project_id, target_id, environment,
       attempted_candidate_id, attempted_artifact_digest, attempted_graph_digest,
       attempted_preview_url, last_valid_candidate_id, last_valid_artifact_digest,
       last_valid_graph_digest, last_valid_preview_url, diagnostics_json,
       revision, created_at, updated_at
FROM project.development_session
WHERE owner_id = sqlc.arg(owner_id)
  AND checkout_id = sqlc.arg(checkout_id)
  AND worktree_id = sqlc.arg(worktree_id)
  AND project_id = sqlc.arg(project_id)
  AND target_id = sqlc.arg(target_id)
  AND environment = sqlc.arg(environment)
FOR UPDATE;

-- name: InsertDevelopmentSession :execrows
INSERT INTO project.development_session (
    id, owner_id, checkout_id, worktree_id, project_id, target_id, environment,
    attempted_candidate_id, attempted_artifact_digest, attempted_graph_digest,
    attempted_preview_url, last_valid_candidate_id, last_valid_artifact_digest,
    last_valid_graph_digest, last_valid_preview_url, diagnostics_json,
    revision, created_at, updated_at
) VALUES (
    sqlc.arg(id), sqlc.arg(owner_id), sqlc.arg(checkout_id), sqlc.arg(worktree_id),
    sqlc.arg(project_id), sqlc.arg(target_id), sqlc.arg(environment),
    sqlc.arg(attempted_candidate_id), sqlc.arg(attempted_artifact_digest),
    sqlc.arg(attempted_graph_digest), sqlc.arg(attempted_preview_url),
    sqlc.arg(last_valid_candidate_id), sqlc.arg(last_valid_artifact_digest),
    sqlc.arg(last_valid_graph_digest), sqlc.arg(last_valid_preview_url),
    sqlc.arg(diagnostics_json)::jsonb, 1, clock_timestamp(), clock_timestamp()
);

-- name: UpdateDevelopmentSession :execrows
UPDATE project.development_session
SET attempted_candidate_id = sqlc.arg(attempted_candidate_id),
    attempted_artifact_digest = sqlc.arg(attempted_artifact_digest),
    attempted_graph_digest = sqlc.arg(attempted_graph_digest),
    attempted_preview_url = sqlc.arg(attempted_preview_url),
    last_valid_candidate_id = sqlc.arg(last_valid_candidate_id),
    last_valid_artifact_digest = sqlc.arg(last_valid_artifact_digest),
    last_valid_graph_digest = sqlc.arg(last_valid_graph_digest),
    last_valid_preview_url = sqlc.arg(last_valid_preview_url),
    diagnostics_json = sqlc.arg(diagnostics_json)::jsonb,
    revision = revision + 1,
    updated_at = clock_timestamp()
WHERE id = sqlc.arg(id)
  AND owner_id = sqlc.arg(owner_id)
  AND checkout_id = sqlc.arg(checkout_id)
  AND worktree_id = sqlc.arg(worktree_id)
  AND revision = sqlc.arg(expected_revision);
