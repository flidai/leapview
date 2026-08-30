-- Native dashboard authoring query leaves.
-- name: GetDashboard :one
SELECT project_id,dashboard_id,owner_principal_id,slug,title,semantic_model,visibility,status,created_at,updated_at FROM dashboard.authoring_dashboards WHERE project_id=sqlc.arg(project_id) AND dashboard_id=sqlc.arg(dashboard_id);
-- name: LockDashboard :one
SELECT 1::bigint AS locked FROM dashboard.authoring_dashboards WHERE project_id=sqlc.arg(project_id) AND dashboard_id=sqlc.arg(dashboard_id) FOR UPDATE;
-- name: ListDashboards :many
SELECT project_id,dashboard_id,owner_principal_id,slug,title,semantic_model,visibility,status,created_at,updated_at FROM dashboard.authoring_dashboards WHERE project_id=sqlc.arg(project_id) ORDER BY dashboard_id;
-- name: GetDraft :one
SELECT project_id,dashboard_id,draft_id::text,revision_id::text,revision_number,content_hash,provenance_json::text,updated_at FROM dashboard.authoring_drafts WHERE project_id=sqlc.arg(project_id) AND dashboard_id=sqlc.arg(dashboard_id);
-- name: GetPublished :one
SELECT project_id,dashboard_id,revision_id::text,revision_number,content_hash,compiled_revision_id::text,compiled_revision_number,compiled_content_hash,compiled_definition_hash,compiled_semantic_model_id,compiled_semantic_identity_json::text,provenance_json::text,published_at FROM dashboard.authoring_published WHERE project_id=sqlc.arg(project_id) AND dashboard_id=sqlc.arg(dashboard_id);
-- name: GetRevision :one
SELECT project_id,dashboard_id,revision_id::text,revision_number,document_json::text,content_hash,provenance_json::text,created_at FROM dashboard.authoring_revisions WHERE project_id=sqlc.arg(project_id) AND dashboard_id=sqlc.arg(dashboard_id) AND revision_id=sqlc.arg(revision_id)::uuid;
-- name: GetCommand :one
SELECT project_id,dashboard_id,command_id::text,request_fingerprint,action,provenance_json::text,occurred_at,result_revision_id::text,result_revision_number,result_content_hash,created_at FROM dashboard.authoring_commands WHERE project_id=sqlc.arg(project_id) AND dashboard_id=sqlc.arg(dashboard_id) AND command_id=sqlc.arg(command_id)::uuid;
-- name: GetCompilation :one
SELECT project_id,dashboard_id,revision_id::text,revision_number,content_hash,definition_json::text,definition_hash,semantic_model_id,semantic_identity_json::text,compiled_at FROM dashboard.authoring_compiled_revisions WHERE project_id=sqlc.arg(project_id) AND dashboard_id=sqlc.arg(dashboard_id) AND revision_id=sqlc.arg(revision_id)::uuid AND revision_number=sqlc.arg(revision_number) AND content_hash=sqlc.arg(content_hash) AND definition_hash=sqlc.arg(definition_hash) AND semantic_model_id=sqlc.arg(semantic_model_id) AND semantic_identity_json=sqlc.arg(semantic_identity_json)::jsonb;
-- name: CountBySemanticModel :many
SELECT semantic_model, count(*) FILTER (WHERE visibility='private') private_count, count(*) FILTER (WHERE visibility='organization') organization_count, count(*) total_count FROM dashboard.authoring_dashboards WHERE project_id=sqlc.arg(project_id) AND status <> 'archived' GROUP BY semantic_model ORDER BY semantic_model;
-- name: CreateDashboard :one
SELECT dashboard.authoring_create_dashboard(
 sqlc.arg(project_id),sqlc.arg(dashboard_id),sqlc.arg(owner_principal_id)::uuid,
 sqlc.arg(slug),sqlc.arg(title),sqlc.arg(semantic_model),sqlc.arg(visibility),
 sqlc.arg(status),sqlc.arg(revision_id)::uuid,sqlc.arg(revision_number),
 sqlc.arg(document_json)::jsonb,sqlc.arg(content_hash),sqlc.arg(provenance_json)::jsonb,
 sqlc.arg(created_at),sqlc.arg(draft_id)::uuid,sqlc.arg(draft_provenance_json)::jsonb,
 sqlc.arg(operation_enabled),sqlc.arg(actor_id),sqlc.arg(operation_kind),
 sqlc.arg(idempotency_key),sqlc.arg(conversation_id),sqlc.arg(tool_call_id),
 sqlc.arg(request_fingerprint),sqlc.arg(event_id)::uuid) AS applied;
-- name: AppendDraft :one
SELECT dashboard.authoring_append_draft(
 sqlc.arg(project_id),sqlc.arg(dashboard_id),sqlc.arg(slug),sqlc.arg(title),
 sqlc.arg(semantic_model),sqlc.arg(visibility),sqlc.arg(status),
 sqlc.arg(revision_id)::uuid,sqlc.arg(revision_number),sqlc.arg(document_json)::jsonb,
 sqlc.arg(content_hash),sqlc.arg(provenance_json)::jsonb,sqlc.arg(created_at),
 sqlc.arg(draft_provenance_json)::jsonb,sqlc.arg(expected_revision_id)::uuid,
 sqlc.arg(expected_revision_number),sqlc.arg(expected_content_hash),
 sqlc.arg(command_id)::uuid,sqlc.arg(request_fingerprint),sqlc.arg(action),
 sqlc.arg(command_provenance_json)::jsonb,sqlc.arg(occurred_at),sqlc.arg(event_id)::uuid) AS applied;
-- name: PublishDashboard :one
SELECT dashboard.authoring_publish_dashboard(
 sqlc.arg(project_id),sqlc.arg(dashboard_id),sqlc.arg(slug),sqlc.arg(title),
 sqlc.arg(semantic_model),sqlc.arg(visibility),sqlc.arg(status),
 sqlc.arg(revision_id)::uuid,sqlc.arg(revision_number),sqlc.arg(content_hash),
 sqlc.arg(definition_json)::jsonb,sqlc.arg(definition_hash),sqlc.arg(semantic_model_id),
 sqlc.arg(semantic_identity_json)::jsonb,sqlc.arg(compiled_at),sqlc.arg(provenance_json)::jsonb,
 sqlc.arg(published_at),sqlc.arg(command_id)::uuid,sqlc.arg(request_fingerprint),
 sqlc.arg(action),sqlc.arg(command_provenance_json)::jsonb,sqlc.arg(occurred_at),sqlc.arg(event_id)::uuid) AS applied;
-- name: ArchiveDashboard :one
SELECT dashboard.authoring_archive_dashboard(
 sqlc.arg(project_id),sqlc.arg(dashboard_id),sqlc.arg(expected_revision_id)::uuid,
 sqlc.arg(expected_revision_number),sqlc.arg(expected_content_hash),sqlc.arg(command_id)::uuid,
 sqlc.arg(request_fingerprint),sqlc.arg(action),sqlc.arg(command_provenance_json)::jsonb,
 sqlc.arg(occurred_at),sqlc.arg(event_id)::uuid) AS applied;
-- name: CommitRevalidation :one
SELECT dashboard.authoring_commit_revalidation(
 sqlc.arg(project_id),sqlc.arg(dashboard_id),sqlc.arg(revision_id)::uuid,
 sqlc.arg(revision_number),sqlc.arg(content_hash),sqlc.arg(definition_json)::jsonb,
 sqlc.arg(definition_hash),sqlc.arg(semantic_model_id),sqlc.arg(semantic_identity_json)::jsonb,
 sqlc.arg(compiled_at),sqlc.arg(generation_id),sqlc.arg(attempt_id)::uuid,
 sqlc.arg(generation_identity_json)::jsonb,sqlc.arg(graph_digest),sqlc.arg(dependency_ids_json)::jsonb,
 sqlc.arg(authored_revision_id)::uuid,sqlc.arg(authored_revision_number),sqlc.arg(authored_content_hash),
 sqlc.arg(prior_compiled_identity_json)::jsonb,sqlc.arg(attempted_at),sqlc.arg(prior_compiled_revision_id)::uuid,
 sqlc.arg(prior_compiled_revision_number),sqlc.arg(prior_compiled_content_hash),
 sqlc.arg(prior_compiled_definition_hash),sqlc.arg(prior_compiled_semantic_model_id)) AS applied;
-- name: RecordRevalidationFailure :one
SELECT dashboard.authoring_record_revalidation_failure(
 sqlc.arg(project_id),sqlc.arg(dashboard_id),sqlc.arg(generation_id),sqlc.arg(attempt_id)::uuid,
 sqlc.arg(generation_identity_json)::jsonb,sqlc.arg(graph_digest),sqlc.arg(dependency_ids_json)::jsonb,
 sqlc.arg(authored_revision_id)::uuid,sqlc.arg(authored_revision_number),sqlc.arg(authored_content_hash),
 sqlc.arg(prior_compiled_identity_json)::jsonb,sqlc.arg(error_code),sqlc.arg(error_message),sqlc.arg(attempted_at)) AS applied;
-- name: GetCreateOperation :one
SELECT request_fingerprint,dashboard_id,result_revision_id::text,result_revision_number,result_content_hash FROM dashboard.authoring_create_operations WHERE project_id=sqlc.arg(project_id) AND actor_id=sqlc.arg(actor_id) AND operation_kind=sqlc.arg(operation_kind) AND idempotency_key=sqlc.arg(idempotency_key);
-- name: LatestRevalidationFailure :one
SELECT status,generation_id,generation_identity_json::text,dependency_ids_json::text,error_code,error_message,attempted_at FROM dashboard.authoring_revalidation_attempts WHERE project_id=sqlc.arg(project_id) AND dashboard_id=sqlc.arg(dashboard_id) ORDER BY attempted_at DESC,attempt_id DESC LIMIT 1;
-- name: Ping :one
SELECT 1 AS ping;
