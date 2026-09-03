// Package postgresbaseline owns the application-level composition of the
// PostgreSQL control-plane baseline. Capability packages own their DDL; the
// platform migration runner owns locking and revision verification.
package postgresbaseline

import (
	"context"
	"errors"
	"fmt"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	physicalpoolpostgres "github.com/flidai/leapview/internal/analytics/physicalpool/postgres"
	platformbootstrappostgres "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	platformmigrations "github.com/flidai/leapview/internal/platform/postgres/migrations"
)

const (
	BaselineRevision    = platformmigrations.BaselineRevision
	BaselineMigrationID = platformmigrations.BaselineMigrationID
	LatestRevision      = accesspostgres.SemanticAttributeControlMigrationRevision
)

// Keep the reconciliation baseline intentionally small. New durable
// capabilities append their own SchemaSQL component when they are reconciled.
var plan = platformmigrations.Plan{
	Components: []platformmigrations.Component{
		{Name: "platform.bootstrap", SQL: platformbootstrappostgres.SchemaSQL()},
		{Name: "access", SQL: accesspostgres.SchemaSQL()},
		{Name: "physical_pool", SQL: physicalpoolpostgres.SchemaSQL()},
		{Name: "ducklake.bootstrap", SQL: ducklakepostgres.SchemaSQL()},
	},
	Migrations: []platformmigrations.Migration{
		{
			Revision:    accesspostgres.AttributeRegistryMigrationRevision,
			MigrationID: accesspostgres.AttributeRegistryMigrationID,
			SQL:         accesspostgres.AttributeRegistryMigrationSQL(),
		},
		{
			Revision:    accesspostgres.SemanticAttributeControlMigrationRevision,
			MigrationID: accesspostgres.SemanticAttributeControlMigrationID,
			SQL:         accesspostgres.SemanticAttributeControlMigrationSQL(),
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

type RevisionReader interface {
	SchemaRevision(context.Context, int64) (platformpostgres.SchemaRevision, error)
}

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

// Capability schemas remain independently testable, so the application plan
// reasserts the least-privilege production role boundary on every replay.
const rolePolicySQL = `
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA platform TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON platform.setting TO leapview_control_runtime;
        GRANT SELECT, INSERT ON platform.instance_identity, platform.instance_environment,
            platform.instance_project_claim TO leapview_control_runtime;
        GRANT USAGE ON SCHEMA access, audit TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA access TO leapview_control_runtime;
        GRANT DELETE ON access.oauth_session, access.oauth_client_assertion TO leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION access.valid_capabilities(jsonb) TO leapview_control_runtime;
        GRANT SELECT, INSERT ON audit.audit_event TO leapview_control_runtime;
        REVOKE UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON audit.audit_event FROM leapview_control_runtime;
        REVOKE DELETE ON access.session, access.api_token, access.service_principal_secret,
            access.desktop_authorization_code, access.device_authorization,
            access.authoring_session, access.authoring_credential,
            access.semantic_attribute_control_state, access.semantic_attribute_assignment,
            access.semantic_attribute_claim_mapping FROM leapview_control_runtime;
        GRANT USAGE ON SCHEMA physical_pool TO leapview_control_runtime;
        GRANT SELECT ON ALL TABLES IN SCHEMA physical_pool TO leapview_control_runtime;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON ALL TABLES IN SCHEMA physical_pool FROM leapview_control_runtime;
        GRANT USAGE ON SCHEMA ducklake TO leapview_control_runtime;
        GRANT SELECT ON ALL TABLES IN SCHEMA ducklake TO leapview_control_runtime;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON ALL TABLES IN SCHEMA ducklake FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA access, audit TO leapview_control_maintenance;
        REVOKE ALL ON ALL TABLES IN SCHEMA access, audit FROM leapview_control_maintenance;
        GRANT USAGE ON SCHEMA physical_pool TO leapview_control_maintenance;
        GRANT SELECT ON physical_pool.physical_pools,
            physical_pool.physical_pool_admissions,
            physical_pool.namespace_ownership_claims TO leapview_control_maintenance;
        GRANT SELECT, INSERT, UPDATE, DELETE
            ON physical_pool.namespace_deletion_leases TO leapview_control_maintenance;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA platform TO leapview_control_readonly;
        GRANT SELECT ON platform.setting, platform.instance_identity,
            platform.instance_environment, platform.instance_project_claim TO leapview_control_readonly;
        GRANT USAGE ON SCHEMA access, audit TO leapview_control_readonly;
        GRANT SELECT ON ALL TABLES IN SCHEMA access, audit TO leapview_control_readonly;
        REVOKE SELECT ON access.session, access.local_credential, access.api_token,
            access.service_principal_secret, access.desktop_authorization_code,
            access.device_authorization, access.authoring_credential,
            access.oauth_client, access.oauth_session,
            access.oauth_client_assertion FROM leapview_control_readonly;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON ALL TABLES IN SCHEMA access, audit FROM leapview_control_readonly;
        GRANT USAGE ON SCHEMA physical_pool TO leapview_control_readonly;
        GRANT SELECT ON ALL TABLES IN SCHEMA physical_pool TO leapview_control_readonly;
        GRANT USAGE ON SCHEMA ducklake TO leapview_control_readonly;
        GRANT SELECT ON ALL TABLES IN SCHEMA ducklake TO leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA platform TO leapview_control_backup;
        GRANT SELECT ON platform.setting, platform.instance_identity,
            platform.instance_environment, platform.instance_project_claim TO leapview_control_backup;
        GRANT USAGE ON SCHEMA access, audit TO leapview_control_backup;
        GRANT SELECT ON ALL TABLES IN SCHEMA access, audit TO leapview_control_backup;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON ALL TABLES IN SCHEMA access, audit FROM leapview_control_backup;
        GRANT USAGE ON SCHEMA physical_pool TO leapview_control_backup;
        GRANT SELECT ON ALL TABLES IN SCHEMA physical_pool TO leapview_control_backup;
        GRANT USAGE ON SCHEMA ducklake TO leapview_control_backup;
        GRANT SELECT ON ALL TABLES IN SCHEMA ducklake TO leapview_control_backup;
    END IF;
END
$$;`
