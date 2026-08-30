-- Static PostgreSQL query leaves for query-audit persistence.

-- name: PruneQueryEvents :one
SELECT prune.cutoff::timestamptz AS cutoff,
       prune.floor_at::timestamptz AS floor_at,
       prune.removed::bigint AS removed
FROM audit.prune_query_events(sqlc.arg(before)::timestamptz, sqlc.arg(batch)::integer) AS prune(cutoff, floor_at, removed);

-- name: InsertQueryEvent :one
INSERT INTO audit.query_event (
    event_id, retry_identity, project_id, principal_id, surface, operation,
    query_kind, model_id, target, object_type, object_id, request_id,
    correlation_id, status, duration_ms, queue_wait_ms, planning_ms,
    connection_wait_ms, database_ms, execution_ms, execution_state,
    rows_returned, bytes_estimate, error, sql_text, plan_text, query_json
) VALUES (
    sqlc.arg(event_id)::uuid, sqlc.arg(retry_identity), sqlc.arg(project_id),
    sqlc.arg(principal_id), sqlc.arg(surface), sqlc.arg(operation),
    sqlc.arg(query_kind), sqlc.arg(model_id), sqlc.arg(target),
    sqlc.arg(object_type), sqlc.arg(object_id), sqlc.arg(request_id),
    sqlc.arg(correlation_id), sqlc.arg(status), sqlc.arg(duration_ms),
    sqlc.arg(queue_wait_ms), sqlc.arg(planning_ms), sqlc.arg(connection_wait_ms),
    sqlc.arg(database_ms), sqlc.arg(execution_ms), sqlc.arg(execution_state),
    sqlc.arg(rows_returned), sqlc.arg(bytes_estimate), sqlc.arg(error),
    sqlc.arg(sql_text), sqlc.arg(plan_text), sqlc.arg(query_json)::jsonb
)
ON CONFLICT DO NOTHING
RETURNING event_id::text;

-- name: GetQueryEvent :one
SELECT event_id::text AS event_id, retry_identity, project_id, principal_id,
       surface, operation, query_kind, model_id, target, object_type, object_id,
       request_id, correlation_id, status, duration_ms, queue_wait_ms,
       planning_ms, connection_wait_ms, database_ms, execution_ms,
       execution_state, rows_returned, bytes_estimate, error, sql_text,
       plan_text, query_json::text AS query_json, created_at
FROM audit.query_event
WHERE event_id = sqlc.arg(event_id)::uuid;

-- name: FindQueryEventByIdentity :one
SELECT event_id::text AS event_id, retry_identity, project_id, principal_id,
       surface, operation, query_kind, model_id, target, object_type, object_id,
       request_id, correlation_id, status, duration_ms, queue_wait_ms,
       planning_ms, connection_wait_ms, database_ms, execution_ms,
       execution_state, rows_returned, bytes_estimate, error, sql_text,
       plan_text, query_json::text AS query_json, created_at
FROM audit.query_event
WHERE event_id = sqlc.arg(event_id)::uuid
   OR retry_identity = sqlc.arg(retry_identity)
ORDER BY (event_id = sqlc.arg(event_id)::uuid) DESC
LIMIT 1;

-- name: ListQueryEvents :many
SELECT event_id::text AS event_id, retry_identity, project_id, principal_id,
       surface, operation, query_kind, model_id, target, object_type, object_id,
       request_id, correlation_id, status, duration_ms, queue_wait_ms,
       planning_ms, connection_wait_ms, database_ms, execution_ms,
       execution_state, rows_returned, bytes_estimate, error, sql_text,
       plan_text, query_json::text AS query_json, created_at
FROM audit.query_event
WHERE (NOT sqlc.arg(has_project)::boolean
       OR project_id = ANY(sqlc.arg(project_ids)::text[]))
  AND (NOT sqlc.arg(has_principal)::boolean
       OR principal_id = ANY(sqlc.arg(principal_ids)::text[]))
  AND (NOT sqlc.arg(has_surface)::boolean
       OR surface = ANY(sqlc.arg(surfaces)::text[]))
  AND (NOT sqlc.arg(has_operation)::boolean
       OR operation = sqlc.arg(operation)::text)
  AND (NOT sqlc.arg(has_query_kind)::boolean
       OR query_kind = ANY(sqlc.arg(query_kinds)::text[]))
  AND (NOT sqlc.arg(has_model)::boolean
       OR model_id = sqlc.arg(model_id)::text)
  AND (NOT sqlc.arg(has_target)::boolean
       OR target = sqlc.arg(target)::text)
  AND (NOT sqlc.arg(has_status)::boolean
       OR status = ANY(sqlc.arg(statuses)::text[]))
  AND (NOT sqlc.arg(has_search)::boolean
       OR search_document @@ websearch_to_tsquery('simple', sqlc.arg(search)::text))
  AND (NOT sqlc.arg(has_from)::boolean
       OR created_at >= sqlc.arg(from_time)::timestamptz)
  AND (NOT sqlc.arg(has_to)::boolean
       OR created_at <= sqlc.arg(to_time)::timestamptz)
  AND (NOT sqlc.arg(has_cursor)::boolean
       OR created_at < sqlc.arg(cursor_time)::timestamptz
       OR (created_at = sqlc.arg(cursor_time)::timestamptz
           AND event_id < sqlc.arg(cursor_id)::uuid))
ORDER BY created_at DESC, event_id DESC
LIMIT sqlc.arg(page_size)::int;

-- name: ListQueryEventFilterOptions :many
SELECT project_id AS value, count(*) AS count
FROM audit.query_event
WHERE project_id <> ''
  AND (sqlc.arg(search)::text = '' OR project_id ILIKE '%' || sqlc.arg(search)::text || '%')
GROUP BY project_id
ORDER BY count DESC, value ASC
LIMIT sqlc.arg(page_size)::int;

-- name: ListPrincipalFilterOptions :many
SELECT principal_id AS value, count(*) AS count
FROM audit.query_event
WHERE principal_id <> ''
  AND (sqlc.arg(search)::text = '' OR principal_id ILIKE '%' || sqlc.arg(search)::text || '%')
GROUP BY principal_id
ORDER BY count DESC, value ASC
LIMIT sqlc.arg(page_size)::int;

-- name: ListSurfaceFilterOptions :many
SELECT surface AS value, count(*) AS count
FROM audit.query_event
WHERE surface <> ''
  AND (sqlc.arg(search)::text = '' OR surface ILIKE '%' || sqlc.arg(search)::text || '%')
GROUP BY surface
ORDER BY count DESC, value ASC
LIMIT sqlc.arg(page_size)::int;

-- name: ListKindFilterOptions :many
SELECT query_kind AS value, count(*) AS count
FROM audit.query_event
WHERE query_kind <> ''
  AND (sqlc.arg(search)::text = '' OR query_kind ILIKE '%' || sqlc.arg(search)::text || '%')
GROUP BY query_kind
ORDER BY count DESC, value ASC
LIMIT sqlc.arg(page_size)::int;

-- name: ListStatusFilterOptions :many
SELECT status AS value, count(*) AS count
FROM audit.query_event
WHERE status <> ''
  AND (sqlc.arg(search)::text = '' OR status ILIKE '%' || sqlc.arg(search)::text || '%')
GROUP BY status
ORDER BY count DESC, value ASC
LIMIT sqlc.arg(page_size)::int;
