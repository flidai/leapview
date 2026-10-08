-- name: SaveVisual :one
INSERT INTO dashboard.saved_visuals (project_id,principal_id,id,source_key,title,semantic_model_id,definition_json) VALUES ($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT (project_id,principal_id,source_key) DO UPDATE SET title=EXCLUDED.title,semantic_model_id=EXCLUDED.semantic_model_id,definition_json=EXCLUDED.definition_json,saved_at=clock_timestamp() RETURNING *;
-- name: SavedVisuals :many
SELECT * FROM dashboard.saved_visuals WHERE project_id=$1 AND principal_id=$2 ORDER BY saved_at DESC,id LIMIT 500;
-- name: SavedVisual :one
SELECT * FROM dashboard.saved_visuals WHERE project_id=$1 AND principal_id=$2 AND id=$3;

-- name: UnsaveVisual :exec
DELETE FROM dashboard.saved_visuals WHERE project_id=$1 AND principal_id=$2 AND id=$3;
