-- name: InsertQueryEvent :exec
INSERT INTO query_events (
  id,
  project_id,
  principal_id,
  surface,
  operation,
  query_kind,
  model_id,
  target,
  object_type,
  object_id,
  request_id,
  correlation_id,
  status,
  duration_ms,
  queue_wait_ms,
  planning_ms,
  connection_wait_ms,
  database_ms,
  execution_ms,
  execution_state,
  rows_returned,
  bytes_estimate,
  error,
  sql_text,
  plan_text,
  query_json
)
VALUES (
  sqlc.arg(id),
  sqlc.arg(project_id),
  sqlc.arg(principal_id),
  sqlc.arg(surface),
  sqlc.arg(operation),
  sqlc.arg(query_kind),
  sqlc.arg(model_id),
  sqlc.arg(target),
  sqlc.arg(object_type),
  sqlc.arg(object_id),
  sqlc.arg(request_id),
  sqlc.arg(correlation_id),
  sqlc.arg(status),
  sqlc.arg(duration_ms),
  sqlc.arg(queue_wait_ms),
  sqlc.arg(planning_ms),
  sqlc.arg(connection_wait_ms),
  sqlc.arg(database_ms),
  sqlc.arg(execution_ms),
  sqlc.arg(execution_state),
  sqlc.arg(rows_returned),
  sqlc.arg(bytes_estimate),
  sqlc.arg(error),
  sqlc.arg(sql_text),
  sqlc.arg(plan_text),
  sqlc.arg(query_json)
);

-- name: GetQueryEvent :one
SELECT *
FROM query_events
WHERE id = sqlc.arg(id);

-- name: ListQueryEvents :many
WITH params AS (
  SELECT
    CAST(sqlc.arg(project_ids_json) AS TEXT) AS project_ids_json,
    CAST(sqlc.arg(principal_ids_json) AS TEXT) AS principal_ids_json,
    CAST(sqlc.arg(surfaces_json) AS TEXT) AS surfaces_json,
    CAST(sqlc.arg(query_kinds_json) AS TEXT) AS query_kinds_json,
    CAST(sqlc.arg(statuses_json) AS TEXT) AS statuses_json
)
SELECT query_events.*
FROM query_events CROSS JOIN params
WHERE (
    NOT EXISTS (SELECT 1 FROM json_each(params.project_ids_json))
    OR project_id IN (SELECT CAST(value AS TEXT) FROM json_each(params.project_ids_json))
  )
  AND (
    NOT EXISTS (SELECT 1 FROM json_each(params.principal_ids_json))
    OR principal_id IN (SELECT CAST(value AS TEXT) FROM json_each(params.principal_ids_json))
  )
  AND (
    NOT EXISTS (SELECT 1 FROM json_each(params.surfaces_json))
    OR surface IN (SELECT CAST(value AS TEXT) FROM json_each(params.surfaces_json))
  )
  AND (sqlc.arg(operation) = '' OR operation = sqlc.arg(operation))
  AND (
    NOT EXISTS (SELECT 1 FROM json_each(params.query_kinds_json))
    OR query_kind IN (SELECT CAST(value AS TEXT) FROM json_each(params.query_kinds_json))
  )
  AND (sqlc.arg(model_id) = '' OR model_id = sqlc.arg(model_id))
  AND (sqlc.arg(target) = '' OR target = sqlc.arg(target))
  AND (
    NOT EXISTS (SELECT 1 FROM json_each(params.statuses_json))
    OR status IN (SELECT CAST(value AS TEXT) FROM json_each(params.statuses_json))
  )
  AND (sqlc.arg(from_time) = '' OR created_at >= sqlc.arg(from_time))
  AND (sqlc.arg(to_time) = '' OR created_at <= sqlc.arg(to_time))
  AND (
    sqlc.arg(search) = ''
    OR target LIKE '%' || sqlc.arg(search) || '%'
    OR sql_text LIKE '%' || sqlc.arg(search) || '%'
    OR query_json LIKE '%' || sqlc.arg(search) || '%'
  )
  AND (
    sqlc.arg(cursor_time) = ''
    OR created_at < sqlc.arg(cursor_time)
    OR (created_at = sqlc.arg(cursor_time) AND id < sqlc.arg(cursor_id))
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(limit);
-- name: ListQueryEventFilterOptions :many
WITH option_values AS (
  SELECT CASE CAST(sqlc.arg(field) AS TEXT)
    WHEN 'project' THEN project_id
    WHEN 'principal' THEN principal_id
    WHEN 'surface' THEN surface
    WHEN 'kind' THEN query_kind
    WHEN 'status' THEN status
    ELSE ''
  END AS value
  FROM query_events
)
SELECT value, COUNT(*) AS count
FROM option_values
WHERE value <> ''
  AND (CAST(sqlc.arg(search) AS TEXT) = '' OR value LIKE '%' || CAST(sqlc.arg(search) AS TEXT) || '%')
GROUP BY value
ORDER BY count DESC, value ASC
LIMIT sqlc.arg(limit);
