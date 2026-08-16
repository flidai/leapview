-- name: UpsertDashboardViewDay :exec
INSERT INTO dashboard_view_days
  (project_id, dashboard_id, principal_id, viewed_on, page_id, first_viewed_at, last_viewed_at)
VALUES
  (sqlc.arg(project_id), sqlc.arg(dashboard_id), sqlc.arg(principal_id), sqlc.arg(viewed_on),
   sqlc.arg(page_id), sqlc.arg(viewed_at), sqlc.arg(viewed_at))
ON CONFLICT(project_id, dashboard_id, principal_id, viewed_on) DO UPDATE SET
  page_id = excluded.page_id,
  last_viewed_at = MAX(dashboard_view_days.last_viewed_at, excluded.last_viewed_at);

-- name: DeleteDashboardViewDaysBefore :exec
DELETE FROM dashboard_view_days
WHERE viewed_on < sqlc.arg(cutoff_date);

-- name: ListDashboardUsageSummaries :many
SELECT
  project_id,
  dashboard_id,
  COUNT(DISTINCT principal_id) AS viewer_count,
  COUNT(*) AS viewer_days,
  CAST(MAX(last_viewed_at) AS TEXT) AS last_viewed_at
FROM dashboard_view_days
WHERE last_viewed_at >= sqlc.arg(cutoff_time)
GROUP BY project_id, dashboard_id
ORDER BY viewer_count DESC, viewer_days DESC, last_viewed_at DESC, project_id, dashboard_id;
