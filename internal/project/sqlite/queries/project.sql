-- Project identity persistence. Serving generations and catalog assets are
-- owned by their immutable project-scoped stores, not this row repository.

-- name: UpsertProject :exec
INSERT INTO projects (id, title, description, updated_at)
VALUES (?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(id) DO UPDATE SET
  title = excluded.title,
  description = excluded.description,
  updated_at = CURRENT_TIMESTAMP;

-- name: GetProject :one
SELECT * FROM projects WHERE id = ?;

-- name: ListProjects :many
SELECT * FROM projects ORDER BY created_at;
