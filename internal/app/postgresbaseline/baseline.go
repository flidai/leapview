// Package postgresbaseline owns the product-level composition of the clean
// PostgreSQL control-plane baseline. Capability packages own their DDL; the
// generic platform migration runner owns locking and revision verification.
package postgresbaseline

import (
	"context"
	"errors"
	"fmt"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	adminproductpostgres "github.com/flidai/leapview/internal/admin/product/postgres"
	agentpostgres "github.com/flidai/leapview/internal/agent/postgres"
	cachepostgres "github.com/flidai/leapview/internal/analytics/cache/postgres"
	connectionbindingpostgres "github.com/flidai/leapview/internal/analytics/connectionbinding/postgres"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	physicalpoolpostgres "github.com/flidai/leapview/internal/analytics/physicalpool/postgres"
	queryauditpostgres "github.com/flidai/leapview/internal/analytics/queryaudit/postgres"
	dashboardappearancepostgres "github.com/flidai/leapview/internal/dashboard/appearance/postgres"
	dashboardauthoringpostgres "github.com/flidai/leapview/internal/dashboard/authoring/postgres"
	dashboardpublicationpostgres "github.com/flidai/leapview/internal/dashboard/publication/postgres"
	dashboardsessionpostgres "github.com/flidai/leapview/internal/dashboard/session/postgres"
	dashboardusagepostgres "github.com/flidai/leapview/internal/dashboard/usage/postgres"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	lineagepostgres "github.com/flidai/leapview/internal/lineage/postgres"
	manageddatapostgres "github.com/flidai/leapview/internal/manageddata/postgres"
	platformbootstrappostgres "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	cursorsigningpostgres "github.com/flidai/leapview/internal/platform/http/cursorsigning/postgres"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	operationpostgres "github.com/flidai/leapview/internal/platform/operation/postgres"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	platformmigrations "github.com/flidai/leapview/internal/platform/postgres/migrations"
	projectpostgres "github.com/flidai/leapview/internal/project/postgres"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	releasepostgres "github.com/flidai/leapview/internal/release/postgres"
	servingstatepostgres "github.com/flidai/leapview/internal/servingstate/postgres"
)

const (
	BaselineRevision    = platformmigrations.BaselineRevision
	BaselineMigrationID = platformmigrations.BaselineMigrationID
	LatestRevision      = accesspostgres.AttributeRegistryMigrationRevision
)

// Ordered capability dependencies: Access owns the audit shape consumed by
// delivery, delivery installs its event ledger, and durable fan-out follows.
var plan = platformmigrations.Plan{
	Components: []platformmigrations.Component{
		{Name: "platform.bootstrap", SQL: platformbootstrappostgres.SchemaSQL()},
		{Name: "platform.operation", SQL: operationpostgres.SchemaSQL()},
		{Name: "platform.cursor_signing", SQL: cursorsigningpostgres.SchemaSQL()},
		{Name: "project", SQL: projectpostgres.SchemaSQL()},
		{Name: "access", SQL: accesspostgres.SchemaSQL()},
		{Name: "admin.product", SQL: adminproductpostgres.SchemaSQL()},
		{Name: "dashboard.session", SQL: dashboardsessionpostgres.SchemaSQL()},
		{Name: "dashboard.usage", SQL: dashboardusagepostgres.SchemaSQL()},
		{Name: "dashboard.appearance", SQL: dashboardappearancepostgres.SchemaSQL()},
		{Name: "dashboard.authoring", SQL: dashboardauthoringpostgres.SchemaSQL()},
		{Name: "dashboard.publication", SQL: dashboardpublicationpostgres.SchemaSQL()},
		{Name: "connection_binding", SQL: connectionbindingpostgres.SchemaSQL()},
		{Name: "event", SQL: eventspostgres.SchemaSQL()},
		{Name: "managed_data", SQL: manageddatapostgres.SchemaSQL()},
		{Name: "physical_pool", SQL: physicalpoolpostgres.SchemaSQL()},
		{Name: "deployment", SQL: deploymentpostgres.SchemaSQL()},
		{Name: "serving_state", SQL: servingstatepostgres.SchemaSQL()},
		{Name: "release", SQL: releasepostgres.SchemaSQL()},
		{Name: "ducklake", SQL: ducklakepostgres.SchemaSQL()},
		{Name: "jobs", SQL: jobspostgres.SchemaSQL()},
		{Name: "agent", SQL: agentpostgres.SchemaSQL()},
		{Name: "refresh", SQL: refreshpostgres.SchemaSQL()},
		{Name: "lineage", SQL: lineagepostgres.SchemaSQL()},
		{Name: "cache", SQL: cachepostgres.SchemaSQL()},
		{Name: "queryaudit", SQL: queryauditpostgres.SchemaSQL()},
	},
	Migrations: []platformmigrations.Migration{
		{
			Revision:    accesspostgres.AttributeRegistryMigrationRevision,
			MigrationID: accesspostgres.AttributeRegistryMigrationID,
			SQL:         accesspostgres.AttributeRegistryMigrationSQL(),
		},
	},
	RolePolicySQL: rolePolicySQL,
}

func Apply(ctx context.Context, tx platformmigrations.Tx) error {
	return platformmigrations.Apply(ctx, tx, plan)
}

func Checksum() string { return plan.Checksum() }

func Components() []platformmigrations.Component {
	return append([]platformmigrations.Component(nil), plan.Components...)
}

func Migrations() []platformmigrations.Migration {
	return append([]platformmigrations.Migration(nil), plan.Migrations...)
}

func FoundationSQL() string { return platformmigrations.BaselineSQL() }

// RevisionReader reads one platform schema revision from the authoritative
// PostgreSQL control database. Keeping this interface here lets every
// production entrypoint verify the same product baseline without importing
// generated SQLC details or duplicating the comparison logic.
type RevisionReader interface {
	SchemaRevision(context.Context, int64) (platformpostgres.SchemaRevision, error)
}

// Verify checks the exact product baseline identity before a capability uses
// the control database. A revision number by itself is insufficient: a
// tampered migration ID or checksum must fail closed as well.
func Verify(ctx context.Context, reader RevisionReader) error {
	if reader == nil {
		return errors.New("PostgreSQL baseline revision reader is required")
	}
	expected := []struct {
		revision int64
		id       string
		checksum string
	}{
		{revision: BaselineRevision, id: BaselineMigrationID, checksum: Checksum()},
	}
	for _, migration := range plan.Migrations {
		expected = append(expected, struct {
			revision int64
			id       string
			checksum string
		}{revision: migration.Revision, id: migration.MigrationID, checksum: migration.Checksum()})
	}
	for _, want := range expected {
		revision, err := reader.SchemaRevision(ctx, want.revision)
		if err != nil {
			return fmt.Errorf("read PostgreSQL schema revision %d: %w", want.revision, err)
		}
		if revision.Revision != want.revision || revision.MigrationID != want.id || revision.Checksum != want.checksum {
			return fmt.Errorf("PostgreSQL schema mismatch: got revision=%d migration=%q checksum=%q, want revision=%d migration=%q checksum=%q", revision.Revision, revision.MigrationID, revision.Checksum, want.revision, want.id, want.checksum)
		}
	}
	return nil
}

// rolePolicySQL closes the privilege gaps left by schemas that are also
// independently testable. Readonly never receives cursor secrets or job
// payloads, and append-only evidence remains non-mutable at the role edge.
const rolePolicySQL = `
DO $$
BEGIN
	IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
		GRANT USAGE ON SCHEMA access, admin, dashboard, delivery, event, audit, release, ducklake, jobs, agent, lineage, cache, physical_pool, serving_state TO leapview_control_runtime;
		GRANT USAGE ON SCHEMA platform TO leapview_control_runtime;
		GRANT SELECT, INSERT, UPDATE ON platform.setting TO leapview_control_runtime;
		GRANT SELECT, INSERT ON platform.instance_identity, platform.instance_environment, platform.instance_project_claim TO leapview_control_runtime;
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
        GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA delivery, jobs TO leapview_control_runtime;
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
        GRANT SELECT, INSERT ON serving_state.bundle, serving_state.asset, serving_state.asset_edge TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON serving_state.reader_lease TO leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION serving_state.guard_reader_snapshot_retention(uuid, bigint) TO leapview_control_runtime;
        REVOKE UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON serving_state.bundle, serving_state.asset, serving_state.asset_edge FROM leapview_control_runtime;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON ALL TABLES IN SCHEMA physical_pool FROM leapview_control_runtime;
    END IF;
	IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
		GRANT USAGE ON SCHEMA dashboard, event, jobs, physical_pool TO leapview_control_maintenance;
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
		GRANT USAGE ON SCHEMA access, admin, dashboard, delivery, event, audit, release, ducklake, jobs, agent, lineage, cache, physical_pool, serving_state TO leapview_control_readonly;
		GRANT USAGE ON SCHEMA platform TO leapview_control_readonly;
		GRANT SELECT ON platform.setting, platform.instance_identity, platform.instance_environment, platform.instance_project_claim TO leapview_control_readonly;
		GRANT SELECT ON admin.product_identity TO leapview_control_readonly;
		GRANT SELECT ON dashboard.view_session, dashboard.view_day, dashboard.appearance_override TO leapview_control_readonly;
		GRANT SELECT ON dashboard.authoring_dashboards, dashboard.authoring_revisions, dashboard.authoring_drafts, dashboard.authoring_compiled_revisions, dashboard.authoring_published, dashboard.authoring_commands, dashboard.authoring_create_operations, dashboard.authoring_revalidation_attempts, dashboard.publications, dashboard.publication_events, dashboard.publication_streams TO leapview_control_readonly;
        GRANT SELECT ON ALL TABLES IN SCHEMA release TO leapview_control_readonly;
        GRANT SELECT ON ALL TABLES IN SCHEMA access, delivery, event, audit, ducklake, lineage, cache, physical_pool TO leapview_control_readonly;
        GRANT SELECT ON ALL TABLES IN SCHEMA serving_state TO leapview_control_readonly;
        GRANT SELECT ON ALL TABLES IN SCHEMA agent TO leapview_control_readonly;
        GRANT SELECT ON jobs.job_observability TO leapview_control_readonly;
        REVOKE SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON jobs.job, jobs.attempt, jobs.event_sequence, jobs.event FROM leapview_control_readonly;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON ALL TABLES IN SCHEMA access, delivery, event, audit, ducklake, lineage, cache, physical_pool FROM leapview_control_readonly;
        REVOKE ALL ON FUNCTION ducklake.admit_snapshot_retention_from_seal(uuid) FROM leapview_control_readonly;
        REVOKE SELECT ON access.session, access.local_credential, access.api_token, access.service_principal_secret, access.desktop_authorization_code, access.device_authorization, access.authoring_credential FROM leapview_control_readonly;
        GRANT USAGE ON SCHEMA platform TO leapview_control_readonly;
        GRANT SELECT ON platform.schema_revision, platform.operation, platform.operation_successor_attempt, platform.api_cursor_signing_key_metadata TO leapview_control_readonly;
        REVOKE ALL ON platform.api_cursor_signing_keys FROM leapview_control_readonly;
    END IF;
	IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
		GRANT USAGE ON SCHEMA project, access, admin, dashboard, delivery, event, audit, release, ducklake, jobs, agent, lineage, cache, physical_pool, serving_state TO leapview_control_backup;
		GRANT USAGE ON SCHEMA platform TO leapview_control_backup;
		GRANT SELECT ON platform.setting, platform.instance_identity, platform.instance_environment, platform.instance_project_claim TO leapview_control_backup;
		GRANT SELECT ON admin.product_identity TO leapview_control_backup;
		GRANT SELECT ON dashboard.view_session, dashboard.view_day, dashboard.appearance_override TO leapview_control_backup;
		GRANT SELECT ON dashboard.authoring_dashboards, dashboard.authoring_revisions, dashboard.authoring_drafts, dashboard.authoring_compiled_revisions, dashboard.authoring_published, dashboard.authoring_commands, dashboard.authoring_create_operations, dashboard.authoring_revalidation_attempts, dashboard.publications, dashboard.publication_events, dashboard.publication_streams TO leapview_control_backup;
        GRANT SELECT ON ALL TABLES IN SCHEMA project, access, delivery, event, audit, release, ducklake, jobs, lineage, cache, physical_pool TO leapview_control_backup;
        GRANT SELECT ON ALL TABLES IN SCHEMA serving_state TO leapview_control_backup;
        GRANT SELECT ON ALL TABLES IN SCHEMA agent TO leapview_control_backup;
        GRANT USAGE ON SCHEMA platform TO leapview_control_backup;
        GRANT SELECT ON platform.schema_revision, platform.operation, platform.operation_successor_attempt, platform.api_cursor_signing_keys TO leapview_control_backup;
        REVOKE ALL ON FUNCTION ducklake.admit_snapshot_retention_from_seal(uuid) FROM leapview_control_backup;
    END IF;
END
$$;`
