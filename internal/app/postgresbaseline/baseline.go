// Package postgresbaseline owns the product-level policy applied around the
// immutable Goose control-plane migrations.
package postgresbaseline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	platformmigrations "github.com/flidai/leapview/internal/platform/postgres/migrations"
)

const (
	BaselineRevision    = platformmigrations.BaselineRevision
	BaselineMigrationID = platformmigrations.BaselineMigrationID
	LatestRevision      = BaselineRevision
)

// Apply is the explicit initialization/upgrade path. Goose owns migration
// execution and versioning; LeapView reapplies its cross-capability role
// policy afterward in a separate transaction.
func Apply(ctx context.Context, db *sql.DB) error {
	if err := platformmigrations.ApplyGoose(ctx, db); err != nil {
		return err
	}
	return platformmigrations.ReconcileRolePolicy(ctx, db, rolePolicySQL)
}

// Verify is the read-only serving-startup compatibility check.
func Verify(ctx context.Context, db *sql.DB) error {
	return platformmigrations.VerifyGoose(ctx, db)
}

// SQLDBProvider exposes the migration-only adapter without broadening the
// ordinary capability interfaces to database/sql query methods.
type SQLDBProvider interface {
	SQLDB() (*sql.DB, error)
}

// VerifyProvider verifies a live pgx-backed pool through its read-only Goose
// adapter and closes only the adapter handle, never the underlying pool.
func VerifyProvider(ctx context.Context, provider SQLDBProvider) error {
	if provider == nil {
		return errors.New("PostgreSQL Goose verification provider is required")
	}
	db, err := provider.SQLDB()
	if err != nil {
		return fmt.Errorf("open PostgreSQL Goose verification adapter: %w", err)
	}
	defer db.Close()
	return Verify(ctx, db)
}

// rolePolicySQL closes the privilege gaps left by schemas that are also
// independently testable. Readonly never receives cursor secrets or job
// payloads, and append-only evidence remains non-mutable at the role edge.
const rolePolicySQL = `
REVOKE ALL ON FUNCTION delivery.lock_retention_root(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION delivery.commit_activation_transition(uuid, text, uuid, bigint, bigint) FROM PUBLIC;
DO $$
BEGIN
	IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
		GRANT USAGE ON SCHEMA access, admin, dashboard, delivery, event, audit, release, ducklake, jobs, agent, lineage, physical_pool, serving_state, recovery TO leapview_control_runtime;
		GRANT USAGE ON SCHEMA platform TO leapview_control_runtime;
		GRANT SELECT, INSERT, UPDATE ON platform.setting TO leapview_control_runtime;
		GRANT SELECT, INSERT ON platform.instance_identity, platform.instance_environment, platform.instance_project_claim TO leapview_control_runtime;
		GRANT DELETE ON access.oauth_session, access.oauth_client_assertion TO leapview_control_runtime;
		GRANT EXECUTE ON FUNCTION access.valid_capabilities(jsonb) TO leapview_control_runtime;
		REVOKE DELETE ON access.session, access.api_token, access.service_principal_secret, access.desktop_authorization_code, access.device_authorization, access.authoring_session, access.authoring_credential, access.semantic_attribute_control_state, access.semantic_attribute_assignment, access.semantic_attribute_claim_mapping FROM leapview_control_runtime;
		GRANT SELECT, UPDATE ON admin.product_identity TO leapview_control_runtime;
		GRANT SELECT, INSERT, UPDATE ON dashboard.view_session, dashboard.view_day, dashboard.appearance_override TO leapview_control_runtime;
		GRANT SELECT ON dashboard.authoring_dashboards, dashboard.authoring_revisions, dashboard.authoring_drafts, dashboard.authoring_compiled_revisions, dashboard.authoring_published, dashboard.authoring_commands, dashboard.authoring_create_operations, dashboard.authoring_revalidation_attempts, dashboard.publications, dashboard.publication_events, dashboard.publication_streams TO leapview_control_runtime;
		REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON dashboard.authoring_dashboards, dashboard.authoring_revisions, dashboard.authoring_drafts, dashboard.authoring_compiled_revisions, dashboard.authoring_published, dashboard.authoring_commands, dashboard.authoring_create_operations, dashboard.authoring_revalidation_attempts, dashboard.publications, dashboard.publication_events, dashboard.publication_streams FROM leapview_control_runtime;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON release.release_record, release.release_connection
            FROM leapview_control_runtime;
        GRANT SELECT ON release.release_record, release.release_connection TO leapview_control_runtime;
        GRANT INSERT (release_id, project_id, environment, generation_id, project_digest,
                      artifact_digest, request_digest, idempotency_key, provenance, created_by)
            ON release.release_record TO leapview_control_runtime;
        GRANT UPDATE (artifact_actual_digest, artifact_size_bytes, artifact_uploaded_at,
                      status, finalized_at, error)
            ON release.release_record TO leapview_control_runtime;
        GRANT INSERT ON release.release_connection TO leapview_control_runtime;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON release.candidate_provenance, release.deployment_linkage
            FROM leapview_control_runtime;
        GRANT SELECT ON release.candidate_provenance, release.deployment_linkage TO leapview_control_runtime;
        GRANT INSERT (project_id, candidate_id, candidate_revision, provenance_digest, provenance)
			ON release.candidate_provenance TO leapview_control_runtime;
        GRANT INSERT (deployment_id, project_id, release_id, rollback_of)
            ON release.deployment_linkage TO leapview_control_runtime;
		GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA delivery TO leapview_control_runtime;
		REVOKE UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON delivery.delivery_target FROM leapview_control_runtime;
		-- PostgreSQL row-locking clauses require UPDATE on at least one column.
		-- The trigger rejects every direct runtime mutation of this row, while
		-- the owner-only activation capability can still advance its revision.
		GRANT UPDATE (updated_at) ON delivery.delivery_target TO leapview_control_runtime;
		REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON delivery.delivery_active_pointer FROM leapview_control_runtime;
		GRANT EXECUTE ON FUNCTION delivery.commit_activation_transition(uuid, text, uuid, bigint, bigint) TO leapview_control_runtime;
		GRANT SELECT, INSERT, UPDATE ON jobs.job_history, jobs.event_sequence, jobs.event TO leapview_control_runtime;
		REVOKE DELETE, TRUNCATE, REFERENCES, TRIGGER ON jobs.job_history, jobs.event_sequence, jobs.event FROM leapview_control_runtime;
		-- Retention-root lifecycle is capability-owned. Runtime activation may
		-- lock/read a root and retire a predecessor through definer functions,
		-- but runtime must never perform direct lifecycle UPDATE/DELETE or force
		-- terminal expiry.
		REVOKE UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON delivery.delivery_retention_root FROM leapview_control_runtime;
		GRANT EXECUTE ON FUNCTION delivery.lock_retention_root(uuid) TO leapview_control_runtime;
		GRANT EXECUTE ON FUNCTION delivery.retire_retention_root(uuid) TO leapview_control_runtime;
        REVOKE EXECUTE ON FUNCTION delivery.create_recovery_retention_root(uuid, text, uuid, uuid, timestamptz, jsonb) FROM leapview_control_runtime;
        REVOKE EXECUTE ON FUNCTION delivery.expire_retention_root(uuid, interval) FROM leapview_control_runtime;
        REVOKE EXECUTE ON FUNCTION delivery.maintain_retention_roots(text, text, interval, integer) FROM leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON agent.conversations, agent.runs TO leapview_control_runtime;
        GRANT SELECT, INSERT ON agent.messages, agent.events TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA event TO leapview_control_runtime;
        GRANT SELECT, INSERT ON ALL TABLES IN SCHEMA audit TO leapview_control_runtime;
		REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
			ON audit.audit_retention_floor, audit.query_event_retention_floor
			FROM leapview_control_runtime;
        GRANT SELECT ON ALL TABLES IN SCHEMA lineage TO leapview_control_runtime;
        GRANT INSERT ON lineage.graphs, lineage.nodes, lineage.edges, lineage.bindings TO leapview_control_runtime;
        REVOKE INSERT, UPDATE, DELETE ON lineage.revisions FROM leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION lineage.publish_revision(text, text, text) TO leapview_control_runtime;
        REVOKE UPDATE, DELETE ON event.event_log FROM leapview_control_runtime;
        REVOKE UPDATE, DELETE ON audit.audit_event FROM leapview_control_runtime;
        -- Native delivery planning resolves the exact catalog and generation
        -- binding before a build or serving cutover. Reassert read access here
        -- because Apply skips already-recorded capability SQL on replay.
        -- INSERT remains capability-owned (bootstrap/admission); UPDATE and
        -- DELETE stay forbidden for these immutable identity rows.
        GRANT SELECT ON ducklake.catalog_identity TO leapview_control_runtime;
        REVOKE UPDATE, DELETE ON ducklake.catalog_identity FROM leapview_control_runtime;
        GRANT SELECT ON ducklake.snapshot_retention TO leapview_control_runtime;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON ducklake.snapshot_retention FROM leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION ducklake.admit_snapshot_retention_from_seal(uuid) TO leapview_control_runtime;
        REVOKE INSERT, UPDATE, DELETE ON ducklake.catalog_runtime_compatibility, ducklake.migration_fence, ducklake.catalog_migration, ducklake.snapshot_requalification FROM leapview_control_runtime;
        REVOKE EXECUTE ON FUNCTION event.prune_event_log(timestamptz, integer) FROM leapview_control_runtime;
        REVOKE EXECUTE ON FUNCTION jobs.prune(timestamptz, integer) FROM leapview_control_runtime;
		GRANT SELECT ON ALL TABLES IN SCHEMA physical_pool TO leapview_control_runtime;
		GRANT SELECT ON ALL TABLES IN SCHEMA recovery TO leapview_control_runtime;
		REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON ALL TABLES IN SCHEMA recovery FROM leapview_control_runtime;
        GRANT SELECT, INSERT ON serving_state.bundle, serving_state.asset, serving_state.asset_edge TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON serving_state.reader_lease TO leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION serving_state.guard_reader_snapshot_retention(uuid, bigint) TO leapview_control_runtime;
        REVOKE UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON serving_state.bundle, serving_state.asset, serving_state.asset_edge FROM leapview_control_runtime;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON ALL TABLES IN SCHEMA physical_pool FROM leapview_control_runtime;
    END IF;
	IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
		GRANT USAGE ON SCHEMA dashboard, event, jobs, physical_pool, recovery TO leapview_control_maintenance;
		GRANT SELECT, INSERT, UPDATE ON recovery.recovery_set, recovery.validation_attempt TO leapview_control_maintenance;
		GRANT SELECT, INSERT ON recovery.recovery_cluster_point, recovery.recovery_object_root, recovery.validation_result TO leapview_control_maintenance;
		REVOKE DELETE, TRUNCATE, REFERENCES, TRIGGER ON ALL TABLES IN SCHEMA recovery FROM leapview_control_maintenance;
		REVOKE UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON recovery.recovery_cluster_point, recovery.recovery_object_root, recovery.validation_result FROM leapview_control_maintenance;
		GRANT USAGE ON SCHEMA delivery TO leapview_control_maintenance;
		GRANT SELECT ON delivery.delivery_retention_root TO leapview_control_maintenance;
		GRANT EXECUTE ON FUNCTION delivery.lock_retention_root(uuid), delivery.retire_retention_root(uuid), delivery.expire_retention_root(uuid, interval), delivery.maintain_retention_roots(text, text, interval, integer), delivery.create_recovery_retention_root(uuid, text, uuid, uuid, timestamptz, jsonb) TO leapview_control_maintenance;
		GRANT SELECT, DELETE ON dashboard.view_session, dashboard.view_day TO leapview_control_maintenance;
		GRANT SELECT ON dashboard.publication_streams TO leapview_control_maintenance;
		REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON dashboard.publication_streams FROM leapview_control_maintenance;
        GRANT EXECUTE ON FUNCTION event.prune_event_log(timestamptz, integer) TO leapview_control_maintenance;
        GRANT EXECUTE ON FUNCTION jobs.prune(timestamptz, integer) TO leapview_control_maintenance;
        GRANT SELECT ON physical_pool.physical_pools, physical_pool.physical_pool_admissions, physical_pool.namespace_ownership_claims TO leapview_control_maintenance;
        GRANT SELECT, INSERT, UPDATE, DELETE ON physical_pool.namespace_deletion_leases TO leapview_control_maintenance;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON physical_pool.physical_pools, physical_pool.physical_pool_admissions, physical_pool.namespace_ownership_claims FROM leapview_control_maintenance;
        REVOKE ALL ON FUNCTION ducklake.admit_snapshot_retention_from_seal(uuid) FROM leapview_control_maintenance;
    END IF;
	IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
		GRANT USAGE ON SCHEMA access, admin, dashboard, delivery, event, audit, release, ducklake, jobs, agent, lineage, physical_pool, serving_state, recovery TO leapview_control_readonly;
		GRANT USAGE ON SCHEMA platform TO leapview_control_readonly;
		GRANT SELECT ON platform.setting, platform.instance_identity, platform.instance_environment, platform.instance_project_claim TO leapview_control_readonly;
		GRANT SELECT ON admin.product_identity TO leapview_control_readonly;
		GRANT SELECT ON dashboard.view_session, dashboard.view_day, dashboard.appearance_override TO leapview_control_readonly;
		GRANT SELECT ON dashboard.authoring_dashboards, dashboard.authoring_revisions, dashboard.authoring_drafts, dashboard.authoring_compiled_revisions, dashboard.authoring_published, dashboard.authoring_commands, dashboard.authoring_create_operations, dashboard.authoring_revalidation_attempts, dashboard.publications, dashboard.publication_events, dashboard.publication_streams TO leapview_control_readonly;
        GRANT SELECT ON ALL TABLES IN SCHEMA release TO leapview_control_readonly;
        GRANT SELECT ON ALL TABLES IN SCHEMA access, delivery, event, audit, ducklake, lineage, physical_pool TO leapview_control_readonly;
		GRANT SELECT ON ALL TABLES IN SCHEMA serving_state TO leapview_control_readonly;
		GRANT SELECT ON ALL TABLES IN SCHEMA recovery TO leapview_control_readonly;
		REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON ALL TABLES IN SCHEMA recovery FROM leapview_control_readonly;
        GRANT SELECT ON ALL TABLES IN SCHEMA agent TO leapview_control_readonly;
		GRANT SELECT ON jobs.job_history TO leapview_control_readonly;
		REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON jobs.event_sequence, jobs.event FROM leapview_control_readonly;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON ALL TABLES IN SCHEMA access, delivery, event, audit, ducklake, lineage, physical_pool FROM leapview_control_readonly;
        REVOKE ALL ON FUNCTION ducklake.admit_snapshot_retention_from_seal(uuid) FROM leapview_control_readonly;
        REVOKE EXECUTE ON FUNCTION delivery.create_recovery_retention_root(uuid, text, uuid, uuid, timestamptz, jsonb) FROM leapview_control_readonly;
        REVOKE SELECT ON access.session, access.local_credential, access.api_token, access.service_principal_secret, access.desktop_authorization_code, access.device_authorization, access.authoring_credential FROM leapview_control_readonly;
        GRANT USAGE ON SCHEMA platform TO leapview_control_readonly;
        GRANT SELECT ON platform.operation, platform.operation_successor_attempt, platform.api_cursor_signing_key_metadata TO leapview_control_readonly;
        REVOKE ALL ON platform.api_cursor_signing_keys FROM leapview_control_readonly;
    END IF;
	IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
		GRANT USAGE ON SCHEMA project, access, admin, dashboard, delivery, event, audit, release, ducklake, jobs, agent, lineage, physical_pool, serving_state, recovery TO leapview_control_backup;
		GRANT USAGE ON SCHEMA platform TO leapview_control_backup;
		GRANT SELECT ON platform.setting, platform.instance_identity, platform.instance_environment, platform.instance_project_claim TO leapview_control_backup;
		GRANT SELECT ON admin.product_identity TO leapview_control_backup;
		GRANT SELECT ON dashboard.view_session, dashboard.view_day, dashboard.appearance_override TO leapview_control_backup;
		GRANT SELECT ON dashboard.authoring_dashboards, dashboard.authoring_revisions, dashboard.authoring_drafts, dashboard.authoring_compiled_revisions, dashboard.authoring_published, dashboard.authoring_commands, dashboard.authoring_create_operations, dashboard.authoring_revalidation_attempts, dashboard.publications, dashboard.publication_events, dashboard.publication_streams TO leapview_control_backup;
        GRANT SELECT ON ALL TABLES IN SCHEMA project, access, delivery, event, audit, release, ducklake, jobs, lineage, physical_pool TO leapview_control_backup;
		GRANT SELECT ON ALL TABLES IN SCHEMA serving_state TO leapview_control_backup;
		GRANT SELECT ON ALL TABLES IN SCHEMA recovery TO leapview_control_backup;
		REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON ALL TABLES IN SCHEMA recovery FROM leapview_control_backup;
        GRANT SELECT ON ALL TABLES IN SCHEMA agent TO leapview_control_backup;
        GRANT USAGE ON SCHEMA platform TO leapview_control_backup;
        GRANT SELECT ON platform.operation, platform.operation_successor_attempt, platform.api_cursor_signing_keys TO leapview_control_backup;
        REVOKE ALL ON FUNCTION ducklake.admit_snapshot_retention_from_seal(uuid) FROM leapview_control_backup;
        REVOKE EXECUTE ON FUNCTION delivery.create_recovery_retention_root(uuid, text, uuid, uuid, timestamptz, jsonb) FROM leapview_control_backup;
    END IF;
END
$$;`
