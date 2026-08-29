-- Project identity persistence. Authored metadata is immutable after insert;
-- conflict handling and exact replay comparison remain in the repository.

-- name: InsertProjectIdentity :exec
INSERT INTO project.project_identity(project_id, title, description)
VALUES ($1, $2, $3)
ON CONFLICT (project_id) DO NOTHING;

-- name: InsertDefaultProjectIdentity :exec
INSERT INTO project.project_identity(project_id, title, description)
VALUES ($1, $1, '')
ON CONFLICT (project_id) DO NOTHING;

-- name: GetProjectIdentity :one
SELECT project_id, title, description, created_at, updated_at
FROM project.project_identity
WHERE project_id = $1;

-- name: ListProjectIdentities :many
SELECT project_id, title, description, created_at, updated_at
FROM project.project_identity
ORDER BY created_at, project_id;
