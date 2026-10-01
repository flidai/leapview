-- Saved Data Explorer commands are scoped to the server-bound project,
-- environment, and authenticated principal. The handler validates commands
-- against the active governed projection before reaching these leaves.

-- name: CreateSavedExploration :one
INSERT INTO project.saved_exploration
    (id, project_id, environment, principal_id, title, command_json)
VALUES (
    sqlc.arg(id)::uuid,
    sqlc.arg(project_id),
    sqlc.arg(environment),
    sqlc.arg(principal_id),
    sqlc.arg(title),
    sqlc.arg(command_json)::jsonb
)
RETURNING id::text, title, command_json::text, created_at, updated_at;

-- name: ListSavedExplorations :many
SELECT id::text, title, command_json::text, created_at, updated_at
FROM project.saved_exploration
WHERE project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment)
  AND principal_id = sqlc.arg(principal_id)
ORDER BY updated_at DESC, id;

-- name: GetSavedExploration :one
SELECT id::text, title, command_json::text, created_at, updated_at
FROM project.saved_exploration
WHERE id = sqlc.arg(id)::uuid
  AND project_id = sqlc.arg(project_id)
  AND environment = sqlc.arg(environment)
  AND principal_id = sqlc.arg(principal_id);
