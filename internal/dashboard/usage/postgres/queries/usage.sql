-- Native PostgreSQL dashboard usage query leaves.
-- name: UpsertViewDay :exec
INSERT INTO dashboard.view_day
  (project_id, dashboard_id, principal_id, viewed_on, page_id, first_viewed_at, last_viewed_at)
VALUES (sqlc.arg(project_id), sqlc.arg(dashboard_id), sqlc.arg(principal_id), sqlc.arg(viewed_on), sqlc.arg(page_id), sqlc.arg(viewed_at), sqlc.arg(viewed_at))
ON CONFLICT(project_id, dashboard_id, principal_id, viewed_on) DO UPDATE SET
  page_id = excluded.page_id,
  last_viewed_at = GREATEST(dashboard.view_day.last_viewed_at, excluded.last_viewed_at);

-- name: DeleteBefore :execrows
WITH victims AS (
    SELECT project_id, dashboard_id, principal_id, viewed_on
    FROM dashboard.view_day AS old
    WHERE old.viewed_on < sqlc.arg(cutoff_date)
    ORDER BY old.viewed_on, old.project_id, old.dashboard_id, old.principal_id
    LIMIT sqlc.arg(batch_size)::integer
)
DELETE FROM dashboard.view_day AS d
USING victims
WHERE d.project_id = victims.project_id
  AND d.dashboard_id = victims.dashboard_id
  AND d.principal_id = victims.principal_id
  AND d.viewed_on = victims.viewed_on;

-- name: ListSummaries :many
SELECT project_id, dashboard_id, COUNT(DISTINCT principal_id) AS viewer_count,
       COUNT(*) AS viewer_days,
       (SELECT vd2.last_viewed_at
          FROM dashboard.view_day AS vd2
         WHERE vd2.project_id = vd.project_id
           AND vd2.dashboard_id = vd.dashboard_id
         ORDER BY vd2.last_viewed_at DESC
         LIMIT 1) AS last_viewed_at
FROM dashboard.view_day AS vd
WHERE vd.last_viewed_at >= sqlc.arg(cutoff_time)
GROUP BY vd.project_id, vd.dashboard_id
ORDER BY viewer_count DESC, viewer_days DESC, last_viewed_at DESC, project_id, dashboard_id;

-- name: Ping :one
SELECT 1 AS ping;
