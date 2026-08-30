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
-- name: Suspend :one
SELECT dashboard.suspend_publication(sqlc.arg(project_id),sqlc.arg(name),sqlc.arg(actor_id),sqlc.arg(expected_revision),sqlc.arg(domain_event_id)::uuid,sqlc.arg(aggregate_version),sqlc.arg(correlation_id),sqlc.arg(payload_json)::jsonb,sqlc.arg(audit_operation),sqlc.arg(audit_resource_kind),sqlc.arg(audit_resource_id),sqlc.arg(audit_metadata_json)::jsonb);
-- name: Resume :one
SELECT dashboard.resume_publication(sqlc.arg(project_id),sqlc.arg(name),sqlc.arg(actor_id),sqlc.arg(expected_revision),sqlc.arg(domain_event_id)::uuid,sqlc.arg(aggregate_version),sqlc.arg(correlation_id),sqlc.arg(payload_json)::jsonb,sqlc.arg(audit_operation),sqlc.arg(audit_resource_kind),sqlc.arg(audit_resource_id),sqlc.arg(audit_metadata_json)::jsonb);
-- name: Rotate :one
SELECT dashboard.rotate_publication(sqlc.arg(project_id),sqlc.arg(name),sqlc.arg(actor_id),sqlc.arg(expected_revision),sqlc.arg(public_id),sqlc.arg(domain_event_id)::uuid,sqlc.arg(aggregate_version),sqlc.arg(correlation_id),sqlc.arg(payload_json)::jsonb,sqlc.arg(audit_operation),sqlc.arg(audit_resource_kind),sqlc.arg(audit_resource_id),sqlc.arg(audit_metadata_json)::jsonb);
-- name: GetConfiguredState :one
SELECT configured FROM dashboard.publications WHERE project_id=sqlc.arg(project_id) AND name=sqlc.arg(name) ORDER BY configured DESC LIMIT 1;
-- name: ListProjectStates :many
SELECT id::text,name,configured,configuration_digest,revision,coalesce(active_serving_state_id,'') AS active_serving_state_id FROM dashboard.publications WHERE project_id=sqlc.arg(project_id);
-- name: GetByID :one
SELECT id::text,project_id,name,public_id,dashboard,default_page,configuration_digest,allowed_origins_json::text,dependency_asset_ids_json::text,revision,configured,active_serving_state_id,suspended_at,suspended_by,configured_at,disabled_at,rotated_at,created_at,updated_at FROM dashboard.publications WHERE id=sqlc.arg(id)::uuid;
-- name: Disable :one
SELECT dashboard.disable_publication(sqlc.arg(id)::uuid,sqlc.arg(expected_revision),sqlc.arg(actor_id),sqlc.arg(domain_event_id)::uuid,sqlc.arg(aggregate_version),sqlc.arg(correlation_id),sqlc.arg(payload_json)::jsonb,sqlc.arg(audit_operation),sqlc.arg(audit_resource_kind),sqlc.arg(audit_resource_id),sqlc.arg(audit_metadata_json)::jsonb);
-- name: UpdateConfiguration :one
SELECT dashboard.update_publication_configuration(sqlc.arg(id)::uuid,sqlc.arg(dashboard),sqlc.arg(default_page),sqlc.arg(configuration_digest),sqlc.arg(allowed_origins_json)::jsonb,sqlc.arg(dependency_asset_ids_json)::jsonb,sqlc.arg(active_serving_state_id),sqlc.arg(expected_revision),sqlc.arg(actor_id),sqlc.arg(domain_event_id)::uuid,sqlc.arg(aggregate_version),sqlc.arg(event_type),sqlc.arg(correlation_id),sqlc.arg(payload_json)::jsonb,sqlc.arg(audit_operation),sqlc.arg(audit_resource_kind),sqlc.arg(audit_resource_id),sqlc.arg(audit_metadata_json)::jsonb);
-- name: Create :one
SELECT dashboard.create_publication(sqlc.arg(id)::uuid,sqlc.arg(project_id),sqlc.arg(name),sqlc.arg(public_id),sqlc.arg(dashboard),sqlc.arg(default_page),sqlc.arg(configuration_digest),sqlc.arg(allowed_origins_json)::jsonb,sqlc.arg(dependency_asset_ids_json)::jsonb,sqlc.arg(active_serving_state_id),sqlc.arg(actor_id),sqlc.arg(domain_event_id)::uuid,sqlc.arg(aggregate_version),sqlc.arg(correlation_id),sqlc.arg(payload_json)::jsonb,sqlc.arg(audit_operation),sqlc.arg(audit_resource_kind),sqlc.arg(audit_resource_id),sqlc.arg(audit_metadata_json)::jsonb);
-- name: UpsertStream :exec
SELECT dashboard.upsert_publication_stream(sqlc.arg(publication_id)::uuid,sqlc.arg(stream_id),sqlc.arg(public_id),sqlc.arg(serving_state_id),sqlc.arg(registration_id)::uuid,sqlc.arg(filters_json)::jsonb,sqlc.arg(expires_at));
-- name: DeleteStreamRegistration :exec
SELECT dashboard.delete_stream_registration(sqlc.arg(publication_id)::uuid,sqlc.arg(stream_id),sqlc.arg(registration_id)::uuid);
-- name: ExpireStreamRegistration :one
SELECT dashboard.expire_stream_registration(sqlc.arg(publication_id)::uuid,sqlc.arg(stream_id),sqlc.arg(registration_id)::uuid);
-- name: GetCommandState :one
SELECT filters_json::text,generation FROM dashboard.publication_streams WHERE publication_id=sqlc.arg(publication_id)::uuid AND stream_id=sqlc.arg(stream_id) AND public_id=sqlc.arg(public_id) AND serving_state_id=sqlc.arg(serving_state_id) AND registration_id=sqlc.arg(registration_id)::uuid AND expires_at>clock_timestamp();
-- name: UpdateCommandState :one
SELECT dashboard.update_command_state(sqlc.arg(publication_id)::uuid,sqlc.arg(stream_id),sqlc.arg(public_id),sqlc.arg(serving_state_id),sqlc.arg(registration_id)::uuid,sqlc.arg(current_generation),sqlc.arg(filters_json)::jsonb,sqlc.arg(next_generation),sqlc.arg(expires_at));
-- name: StreamActive :one
SELECT exists(SELECT 1 FROM dashboard.publication_streams WHERE publication_id=sqlc.arg(publication_id)::uuid AND stream_id=sqlc.arg(stream_id) AND public_id=sqlc.arg(public_id) AND serving_state_id=sqlc.arg(serving_state_id) AND registration_id=sqlc.arg(registration_id)::uuid AND expires_at>clock_timestamp());
-- name: DeleteExpiredStreams :one
SELECT dashboard.prune_expired_publication_streams(sqlc.arg(now),sqlc.arg(batch_limit));
-- name: ListActiveStreams :many
SELECT publication_id::text,stream_id,registration_id::text FROM dashboard.publication_streams WHERE expires_at>clock_timestamp();
-- name: ExpirePublicationStreams :one
SELECT dashboard.expire_publication_streams(sqlc.arg(publication_id)::uuid);
-- name: ExtendStream :one
SELECT dashboard.extend_stream(sqlc.arg(publication_id)::uuid,sqlc.arg(stream_id),sqlc.arg(public_id),sqlc.arg(serving_state_id),sqlc.arg(registration_id)::uuid,sqlc.arg(expires_at));
-- name: Ping :one
SELECT 1 AS ping;
