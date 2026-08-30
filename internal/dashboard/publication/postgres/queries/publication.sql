-- Native dashboard publication query leaves.
-- name: Get :one
SELECT id::text,project_id,name,public_id,dashboard,default_page,configuration_digest,allowed_origins_json::text,dependency_asset_ids_json::text,revision,configured,active_serving_state_id,suspended_at,suspended_by,configured_at,disabled_at,rotated_at,created_at,updated_at FROM dashboard.publications WHERE project_id=sqlc.arg(project_id) AND name=sqlc.arg(name) ORDER BY configured DESC LIMIT 1;
-- name: GetByPublicID :one
SELECT id::text,project_id,name,public_id,dashboard,default_page,configuration_digest,allowed_origins_json::text,dependency_asset_ids_json::text,revision,configured,active_serving_state_id,suspended_at,suspended_by,configured_at,disabled_at,rotated_at,created_at,updated_at FROM dashboard.publications WHERE public_id=sqlc.arg(public_id) AND configured AND active_serving_state_id IS NOT NULL;
-- name: List :many
SELECT id::text,project_id,name,public_id,dashboard,default_page,configuration_digest,allowed_origins_json::text,dependency_asset_ids_json::text,revision,configured,active_serving_state_id,suspended_at,suspended_by,configured_at,disabled_at,rotated_at,created_at,updated_at FROM dashboard.publications WHERE project_id=sqlc.arg(project_id) ORDER BY name LIMIT 1000;
-- name: ListAll :many
SELECT id::text,project_id,name,public_id,dashboard,default_page,configuration_digest,allowed_origins_json::text,dependency_asset_ids_json::text,revision,configured,active_serving_state_id,suspended_at,suspended_by,configured_at,disabled_at,rotated_at,created_at,updated_at FROM dashboard.publications ORDER BY project_id,name LIMIT 1000;
-- name: ListEvents :many
SELECT event_type,actor_id,coalesce(serving_state_id,'') serving_state_id,created_at FROM dashboard.publication_events WHERE publication_id=sqlc.arg(publication_id)::uuid ORDER BY revision DESC LIMIT 1000;
-- name: Suspend :execrows
UPDATE dashboard.publications SET revision=revision+1,suspended_at=coalesce(suspended_at,clock_timestamp()),suspended_by=sqlc.arg(actor_id),updated_at=clock_timestamp() WHERE project_id=sqlc.arg(project_id) AND name=sqlc.arg(name) AND configured AND revision=sqlc.arg(expected_revision);
-- name: Resume :execrows
UPDATE dashboard.publications SET revision=revision+1,suspended_at=NULL,suspended_by='',updated_at=clock_timestamp() WHERE project_id=sqlc.arg(project_id) AND name=sqlc.arg(name) AND configured AND revision=sqlc.arg(expected_revision);
-- name: Rotate :execrows
UPDATE dashboard.publications SET revision=revision+1,public_id=sqlc.arg(public_id),rotated_at=clock_timestamp(),updated_at=clock_timestamp() WHERE project_id=sqlc.arg(project_id) AND name=sqlc.arg(name) AND configured AND revision=sqlc.arg(expected_revision);
-- name: GetConfigured :one
SELECT id::text,project_id,name,public_id,dashboard,default_page,configuration_digest,allowed_origins_json::text,dependency_asset_ids_json::text,revision,configured,active_serving_state_id,suspended_at,suspended_by,configured_at,disabled_at,rotated_at,created_at,updated_at FROM dashboard.publications WHERE project_id=sqlc.arg(project_id) AND name=sqlc.arg(name) AND configured;
-- name: GetConfiguredState :one
SELECT configured FROM dashboard.publications WHERE project_id=sqlc.arg(project_id) AND name=sqlc.arg(name) ORDER BY configured DESC LIMIT 1;
-- name: InsertEvent :execrows
INSERT INTO dashboard.publication_events(publication_id,domain_event_id,aggregate_version,revision,event_type,actor_id,correlation_id,serving_state_id,payload_json)
VALUES(sqlc.arg(publication_id)::uuid,sqlc.arg(domain_event_id)::uuid,sqlc.arg(aggregate_version),sqlc.arg(revision),sqlc.arg(event_type),sqlc.arg(actor_id),sqlc.arg(correlation_id),nullif(sqlc.arg(serving_state_id),''),sqlc.arg(payload_json)::jsonb)
ON CONFLICT (domain_event_id) DO NOTHING;
-- name: GetEventProjection :one
SELECT publication_id::text,domain_event_id::text,aggregate_version,revision,event_type,actor_id,coalesce(serving_state_id,'') serving_state_id,correlation_id,payload_json::text
FROM dashboard.publication_events
WHERE domain_event_id=sqlc.arg(domain_event_id)::uuid;
-- name: ListProjectStates :many
SELECT id::text,name,configured,configuration_digest,revision,coalesce(active_serving_state_id,'') AS active_serving_state_id FROM dashboard.publications WHERE project_id=sqlc.arg(project_id);
-- name: GetByID :one
SELECT id::text,project_id,name,public_id,dashboard,default_page,configuration_digest,allowed_origins_json::text,dependency_asset_ids_json::text,revision,configured,active_serving_state_id,suspended_at,suspended_by,configured_at,disabled_at,rotated_at,created_at,updated_at FROM dashboard.publications WHERE id=sqlc.arg(id)::uuid;
-- name: Disable :execrows
UPDATE dashboard.publications SET revision=revision+1,configured=false,active_serving_state_id=NULL,disabled_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=sqlc.arg(id)::uuid AND revision=sqlc.arg(expected_revision);
-- name: UpdateConfiguration :execrows
UPDATE dashboard.publications SET revision=revision+1,dashboard=sqlc.arg(dashboard),default_page=sqlc.arg(default_page),configuration_digest=sqlc.arg(configuration_digest),allowed_origins_json=sqlc.arg(allowed_origins_json)::jsonb,dependency_asset_ids_json=sqlc.arg(dependency_asset_ids_json)::jsonb,configured=true,active_serving_state_id=sqlc.arg(active_serving_state_id),configured_at=coalesce(configured_at,clock_timestamp()),disabled_at=NULL,updated_at=clock_timestamp() WHERE id=sqlc.arg(id)::uuid AND revision=sqlc.arg(expected_revision);
-- name: Create :exec
INSERT INTO dashboard.publications(id,project_id,name,public_id,dashboard,default_page,configuration_digest,allowed_origins_json,dependency_asset_ids_json,revision,configured,active_serving_state_id,configured_at) VALUES(sqlc.arg(id)::uuid,sqlc.arg(project_id),sqlc.arg(name),sqlc.arg(public_id),sqlc.arg(dashboard),sqlc.arg(default_page),sqlc.arg(configuration_digest),sqlc.arg(allowed_origins_json)::jsonb,sqlc.arg(dependency_asset_ids_json)::jsonb,1,true,sqlc.arg(active_serving_state_id),clock_timestamp());
-- name: UpsertStream :exec
INSERT INTO dashboard.publication_streams(publication_id,stream_id,public_id,serving_state_id,registration_id,filters_json,expires_at) VALUES(sqlc.arg(publication_id)::uuid,sqlc.arg(stream_id),sqlc.arg(public_id),sqlc.arg(serving_state_id),sqlc.arg(registration_id)::uuid,sqlc.arg(filters_json)::jsonb,sqlc.arg(expires_at)) ON CONFLICT(publication_id,stream_id) DO UPDATE SET public_id=excluded.public_id,serving_state_id=excluded.serving_state_id,registration_id=excluded.registration_id,filters_json=excluded.filters_json,generation=1,expires_at=excluded.expires_at,updated_at=clock_timestamp();
-- name: DeleteStreamRegistration :exec
DELETE FROM dashboard.publication_streams WHERE publication_id=sqlc.arg(publication_id)::uuid AND stream_id=sqlc.arg(stream_id) AND registration_id=sqlc.arg(registration_id)::uuid;
-- name: ExpireStreamRegistration :execrows
UPDATE dashboard.publication_streams SET expires_at=clock_timestamp(),updated_at=clock_timestamp() WHERE publication_id=sqlc.arg(publication_id)::uuid AND stream_id=sqlc.arg(stream_id) AND registration_id=sqlc.arg(registration_id)::uuid;
-- name: GetCommandState :one
SELECT filters_json::text,generation FROM dashboard.publication_streams WHERE publication_id=sqlc.arg(publication_id)::uuid AND stream_id=sqlc.arg(stream_id) AND public_id=sqlc.arg(public_id) AND serving_state_id=sqlc.arg(serving_state_id) AND registration_id=sqlc.arg(registration_id)::uuid AND expires_at>sqlc.arg(now);
-- name: UpdateCommandState :execrows
UPDATE dashboard.publication_streams SET filters_json=sqlc.arg(filters_json)::jsonb,generation=sqlc.arg(next_generation),expires_at=sqlc.arg(expires_at),updated_at=clock_timestamp() WHERE publication_id=sqlc.arg(publication_id)::uuid AND stream_id=sqlc.arg(stream_id) AND public_id=sqlc.arg(public_id) AND serving_state_id=sqlc.arg(serving_state_id) AND registration_id=sqlc.arg(registration_id)::uuid AND generation=sqlc.arg(current_generation) AND expires_at>sqlc.arg(now);
-- name: StreamActive :one
SELECT exists(SELECT 1 FROM dashboard.publication_streams WHERE publication_id=sqlc.arg(publication_id)::uuid AND stream_id=sqlc.arg(stream_id) AND public_id=sqlc.arg(public_id) AND serving_state_id=sqlc.arg(serving_state_id) AND registration_id=sqlc.arg(registration_id)::uuid AND expires_at>sqlc.arg(now));
-- name: DeleteExpiredStreams :execrows
WITH claimed AS (
    SELECT s.publication_id, s.stream_id
    FROM dashboard.publication_streams AS s
    WHERE s.expires_at <= sqlc.arg(now)
    ORDER BY s.expires_at, s.publication_id, s.stream_id
    LIMIT sqlc.arg(batch_limit)::integer
    FOR UPDATE SKIP LOCKED
)
DELETE FROM dashboard.publication_streams AS target
USING claimed
WHERE target.publication_id = claimed.publication_id AND target.stream_id = claimed.stream_id;
-- name: ListActiveStreams :many
SELECT publication_id::text,stream_id,registration_id::text FROM dashboard.publication_streams WHERE expires_at>sqlc.arg(now);
-- name: DeletePublicationStreams :execrows
DELETE FROM dashboard.publication_streams WHERE publication_id=sqlc.arg(publication_id)::uuid;
-- name: ExpirePublicationStreams :execrows
UPDATE dashboard.publication_streams SET expires_at=clock_timestamp(),updated_at=clock_timestamp() WHERE publication_id=sqlc.arg(publication_id)::uuid;
-- name: ExtendStream :execrows
UPDATE dashboard.publication_streams SET expires_at=sqlc.arg(expires_at),updated_at=clock_timestamp() WHERE publication_id=sqlc.arg(publication_id)::uuid AND stream_id=sqlc.arg(stream_id) AND public_id=sqlc.arg(public_id) AND serving_state_id=sqlc.arg(serving_state_id) AND registration_id=sqlc.arg(registration_id)::uuid AND expires_at>sqlc.arg(now);
-- name: Ping :one
SELECT 1 AS ping;
