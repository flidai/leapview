-- Native PostgreSQL dashboard session query leaves.
-- name: Create :execrows
INSERT INTO dashboard.view_session(id, project_id, publication_id, principal_or_client, dashboard_id, serving_state_id, stream_instance_id, key_json, version, state_json, expires_at)
VALUES (sqlc.arg(id), sqlc.arg(project_id), sqlc.arg(publication_id), sqlc.arg(principal_or_client), sqlc.arg(dashboard_id), sqlc.arg(serving_state_id), sqlc.arg(stream_instance_id), sqlc.arg(key_json)::jsonb, 1, sqlc.arg(state_json)::jsonb, sqlc.arg(expires_at))
ON CONFLICT (id) DO UPDATE SET
  project_id = EXCLUDED.project_id, publication_id = EXCLUDED.publication_id,
  principal_or_client = EXCLUDED.principal_or_client, dashboard_id = EXCLUDED.dashboard_id,
  serving_state_id = EXCLUDED.serving_state_id, stream_instance_id = EXCLUDED.stream_instance_id,
  key_json = EXCLUDED.key_json, version = 1, state_json = EXCLUDED.state_json,
  expires_at = EXCLUDED.expires_at, updated_at = clock_timestamp()
WHERE dashboard.view_session.expires_at <= clock_timestamp();

-- name: GetActive :one
SELECT project_id, publication_id, principal_or_client, dashboard_id, serving_state_id, stream_instance_id, key_json::text, version, state_json::text, expires_at
FROM dashboard.view_session
WHERE id = sqlc.arg(id) AND expires_at > sqlc.arg(now);

-- name: CompareAndSwap :execrows
UPDATE dashboard.view_session
SET version = version + 1, state_json = sqlc.arg(state_json)::jsonb,
    expires_at = sqlc.arg(expires_at), updated_at = clock_timestamp()
WHERE id = sqlc.arg(id) AND version = sqlc.arg(version)
  AND expires_at > sqlc.arg(now);

-- name: Touch :execrows
UPDATE dashboard.view_session
SET expires_at = sqlc.arg(expires_at), updated_at = clock_timestamp()
WHERE id = sqlc.arg(id) AND expires_at > sqlc.arg(now);

-- name: DeleteExpired :execrows
WITH victims AS (
    SELECT id
    FROM dashboard.view_session AS old
    WHERE old.expires_at <= sqlc.arg(now)
    ORDER BY old.expires_at, old.id
    LIMIT sqlc.arg(batch_size)::integer
)
DELETE FROM dashboard.view_session AS s
USING victims
WHERE s.id = victims.id;

-- name: Ping :one
SELECT 1 AS ping;
