-- name: GetDashboardPublication :one
SELECT *
FROM dashboard_publications
WHERE project_id = sqlc.arg(project_id)
  AND name = sqlc.arg(name)
ORDER BY configured DESC, updated_at DESC
LIMIT 1;

-- name: GetDashboardPublicationByPublicID :one
SELECT *
FROM dashboard_publications
WHERE public_id = sqlc.arg(public_id)
  AND configured = 1
  AND active_serving_state_id IS NOT NULL;

-- name: ListDashboardPublications :many
SELECT *
FROM dashboard_publications
WHERE project_id = sqlc.arg(project_id)
ORDER BY name, project_id;

-- name: ListAllDashboardPublications :many
SELECT *
FROM dashboard_publications
ORDER BY project_id, name;

-- name: ListDashboardPublicationEvents :many
SELECT event_type, actor_id, COALESCE(serving_state_id, '') AS serving_state_id, created_at
FROM dashboard_publication_events
WHERE publication_id = sqlc.arg(publication_id)
ORDER BY id DESC;

-- name: CountDashboardPublicationEvents :one
SELECT COUNT(*)
FROM dashboard_publication_events
WHERE publication_id = sqlc.arg(publication_id);

-- name: SuspendDashboardPublication :execresult
UPDATE dashboard_publications
SET revision = revision + 1,
    suspended_at = COALESCE(suspended_at, CURRENT_TIMESTAMP),
    suspended_by = sqlc.arg(actor_id),
    updated_at = CURRENT_TIMESTAMP
WHERE project_id = sqlc.arg(project_id)
  AND name = sqlc.arg(name)
  AND configured = 1
  AND revision = sqlc.arg(expected_revision);

-- name: ResumeDashboardPublication :execresult
UPDATE dashboard_publications
SET revision = revision + 1,
    suspended_at = NULL,
    suspended_by = '',
    updated_at = CURRENT_TIMESTAMP
WHERE project_id = sqlc.arg(project_id)
  AND name = sqlc.arg(name)
  AND configured = 1
  AND revision = sqlc.arg(expected_revision);

-- name: RotateDashboardPublication :execresult
UPDATE dashboard_publications
SET revision = revision + 1,
    public_id = sqlc.arg(public_id),
    rotated_at = CURRENT_TIMESTAMP,
    updated_at = CURRENT_TIMESTAMP
WHERE project_id = sqlc.arg(project_id)
  AND name = sqlc.arg(name)
  AND configured = 1
  AND revision = sqlc.arg(expected_revision);

-- name: GetDashboardPublicationConfiguredState :one
SELECT configured
FROM dashboard_publications
WHERE project_id = sqlc.arg(project_id)
  AND name = sqlc.arg(name)
ORDER BY configured DESC
LIMIT 1;

-- name: GetConfiguredDashboardPublication :one
SELECT *
FROM dashboard_publications
WHERE project_id = sqlc.arg(project_id)
  AND name = sqlc.arg(name)
  AND configured = 1;

-- name: InsertDashboardPublicationEvent :exec
INSERT INTO dashboard_publication_events
  (publication_id, event_type, actor_id, serving_state_id)
VALUES
  (sqlc.arg(publication_id), sqlc.arg(event_type), sqlc.arg(actor_id), NULLIF(sqlc.arg(serving_state_id), ''));

-- name: ListProjectDashboardPublicationStates :many
SELECT id, name, configured, configuration_digest,
       COALESCE(active_serving_state_id, '') AS active_serving_state_id,
       revision
FROM dashboard_publications
WHERE project_id = sqlc.arg(project_id);

-- name: DisableDashboardPublication :exec
UPDATE dashboard_publications
SET revision = revision + 1,
    configured = 0,
    active_serving_state_id = NULL,
    disabled_at = CURRENT_TIMESTAMP,
    updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(id);

-- name: UpdateDashboardPublicationConfiguration :exec
UPDATE dashboard_publications
SET revision = revision + 1,
    dashboard = sqlc.arg(dashboard),
    default_page = sqlc.arg(default_page),
    configuration_digest = sqlc.arg(configuration_digest),
    allowed_origins_json = sqlc.arg(allowed_origins_json),
    dependency_asset_ids_json = sqlc.arg(dependency_asset_ids_json),
    configured = 1,
    active_serving_state_id = sqlc.arg(active_serving_state_id),
    configured_at = CASE WHEN configured = 0 THEN CURRENT_TIMESTAMP ELSE configured_at END,
    disabled_at = NULL,
    updated_at = CURRENT_TIMESTAMP
WHERE id = sqlc.arg(id);

-- name: CreateDashboardPublication :exec
INSERT INTO dashboard_publications
  (id, project_id, name, public_id, dashboard, default_page,
   configuration_digest, allowed_origins_json, dependency_asset_ids_json,
   configured, active_serving_state_id, configured_at)
VALUES
  (sqlc.arg(id), sqlc.arg(project_id), sqlc.arg(name),
   sqlc.arg(public_id), sqlc.arg(dashboard), sqlc.arg(default_page),
   sqlc.arg(configuration_digest), sqlc.arg(allowed_origins_json),
   sqlc.arg(dependency_asset_ids_json), 1, sqlc.arg(active_serving_state_id),
   CURRENT_TIMESTAMP);

-- name: UpsertDashboardPublicationStream :exec
INSERT INTO dashboard_publication_streams
  (publication_id, stream_id, public_id, serving_state_id, registration_id, filters_json, expires_at)
VALUES
  (sqlc.arg(publication_id), sqlc.arg(stream_id), sqlc.arg(public_id),
   sqlc.arg(serving_state_id), sqlc.arg(registration_id), sqlc.arg(filters_json),
   sqlc.arg(expires_at))
ON CONFLICT(publication_id, stream_id) DO UPDATE SET
  public_id = excluded.public_id,
  serving_state_id = excluded.serving_state_id,
  registration_id = excluded.registration_id,
  filters_json = excluded.filters_json,
  generation = 1,
  expires_at = excluded.expires_at,
  updated_at = CURRENT_TIMESTAMP;

-- name: DeleteDashboardPublicationStreamRegistration :exec
DELETE FROM dashboard_publication_streams
WHERE publication_id = sqlc.arg(publication_id)
  AND stream_id = sqlc.arg(stream_id)
  AND registration_id = sqlc.arg(registration_id);

-- name: GetDashboardPublicationCommandState :one
SELECT filters_json, generation
FROM dashboard_publication_streams
WHERE publication_id = sqlc.arg(publication_id)
  AND stream_id = sqlc.arg(stream_id)
  AND public_id = sqlc.arg(public_id)
  AND serving_state_id = sqlc.arg(serving_state_id)
  AND expires_at > sqlc.arg(now);

-- name: UpdateDashboardPublicationCommandState :execresult
UPDATE dashboard_publication_streams
SET filters_json = sqlc.arg(filters_json),
    generation = sqlc.arg(next_generation),
    expires_at = sqlc.arg(expires_at),
    updated_at = CURRENT_TIMESTAMP
WHERE publication_id = sqlc.arg(publication_id)
  AND stream_id = sqlc.arg(stream_id)
  AND public_id = sqlc.arg(public_id)
  AND serving_state_id = sqlc.arg(serving_state_id)
  AND generation = sqlc.arg(current_generation)
  AND expires_at > sqlc.arg(now);

-- name: DashboardPublicationStreamIsActive :one
SELECT EXISTS(
  SELECT 1
  FROM dashboard_publication_streams
  WHERE publication_id = sqlc.arg(publication_id)
    AND stream_id = sqlc.arg(stream_id)
    AND public_id = sqlc.arg(public_id)
    AND serving_state_id = sqlc.arg(serving_state_id)
    AND expires_at > sqlc.arg(now)
);

-- name: DeleteExpiredDashboardPublicationStreams :exec
DELETE FROM dashboard_publication_streams
WHERE expires_at <= sqlc.arg(now);

-- name: DeleteExpiredDashboardPublicationStreamEvents :exec
DELETE FROM dashboard_publication_stream_events
WHERE created_at <= sqlc.arg(cutoff);

-- name: ListActiveDashboardPublicationStreamRegistrations :many
SELECT publication_id, stream_id, registration_id
FROM dashboard_publication_streams
WHERE expires_at > sqlc.arg(now);

-- name: DeleteDashboardPublicationStreams :exec
DELETE FROM dashboard_publication_streams
WHERE publication_id = sqlc.arg(publication_id);

-- name: ExtendDashboardPublicationStreamRegistration :execresult
UPDATE dashboard_publication_streams
SET expires_at = sqlc.arg(expires_at),
    updated_at = CURRENT_TIMESTAMP
WHERE publication_id = sqlc.arg(publication_id)
  AND stream_id = sqlc.arg(stream_id)
  AND registration_id = sqlc.arg(registration_id);

-- name: InsertDashboardPublicationStreamEvent :exec
INSERT INTO dashboard_publication_stream_events
  (stream_id, envelope_json, created_at)
VALUES
  (sqlc.arg(stream_id), sqlc.arg(envelope_json), sqlc.arg(created_at));

-- name: GetLatestDashboardPublicationStreamEventID :one
SELECT CAST(COALESCE(MAX(id), 0) AS INTEGER)
FROM dashboard_publication_stream_events
WHERE stream_id = sqlc.arg(stream_id);

-- name: ListDashboardPublicationStreamEventsAfter :many
SELECT id, envelope_json
FROM dashboard_publication_stream_events
WHERE stream_id = sqlc.arg(stream_id)
  AND id > sqlc.arg(cursor)
ORDER BY id;
