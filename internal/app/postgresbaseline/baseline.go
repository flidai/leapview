// Package postgresbaseline owns the application-level composition of the
// PostgreSQL control-plane baseline. Capability packages own their DDL; the
// platform migration runner owns locking and revision verification.
package postgresbaseline

import (
	"context"
	"errors"
	"fmt"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	platformmigrations "github.com/flidai/leapview/internal/platform/postgres/migrations"
)

const (
	BaselineRevision    = platformmigrations.BaselineRevision
	BaselineMigrationID = platformmigrations.BaselineMigrationID
)

// Keep the reconciliation baseline intentionally small. New durable
// capabilities append their own SchemaSQL component when they are reconciled.
var plan = platformmigrations.Plan{
	Components: []platformmigrations.Component{
		{Name: "access", SQL: accesspostgres.SchemaSQL()},
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

func FoundationSQL() string { return platformmigrations.BaselineSQL() }

type RevisionReader interface {
	SchemaRevision(context.Context, int64) (platformpostgres.SchemaRevision, error)
}

func Verify(ctx context.Context, reader RevisionReader) error {
	if reader == nil {
		return errors.New("PostgreSQL baseline revision reader is required")
	}
	revision, err := reader.SchemaRevision(ctx, BaselineRevision)
	if err != nil {
		return fmt.Errorf("read PostgreSQL baseline revision: %w", err)
	}
	wantChecksum := Checksum()
	if revision.Revision != BaselineRevision || revision.MigrationID != BaselineMigrationID || revision.Checksum != wantChecksum {
		return fmt.Errorf("PostgreSQL baseline mismatch: got revision=%d migration=%q checksum=%q, want revision=%d migration=%q checksum=%q", revision.Revision, revision.MigrationID, revision.Checksum, BaselineRevision, BaselineMigrationID, wantChecksum)
	}
	return nil
}

// Capability schemas remain independently testable, so the application plan
// reasserts the least-privilege production role boundary on every replay.
const rolePolicySQL = `
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_runtime') THEN
        GRANT USAGE ON SCHEMA access, audit TO leapview_control_runtime;
        GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA access TO leapview_control_runtime;
        GRANT DELETE ON access.oauth_session, access.oauth_client_assertion TO leapview_control_runtime;
        GRANT EXECUTE ON FUNCTION access.valid_capabilities(jsonb) TO leapview_control_runtime;
        GRANT SELECT, INSERT ON audit.audit_event TO leapview_control_runtime;
        REVOKE UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON audit.audit_event FROM leapview_control_runtime;
        REVOKE DELETE ON access.session, access.api_token, access.service_principal_secret,
            access.desktop_authorization_code, access.device_authorization,
            access.authoring_session, access.authoring_credential FROM leapview_control_runtime;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_maintenance') THEN
        GRANT USAGE ON SCHEMA access, audit TO leapview_control_maintenance;
        REVOKE ALL ON ALL TABLES IN SCHEMA access, audit FROM leapview_control_maintenance;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_readonly') THEN
        GRANT USAGE ON SCHEMA access, audit TO leapview_control_readonly;
        GRANT SELECT ON ALL TABLES IN SCHEMA access, audit TO leapview_control_readonly;
        REVOKE SELECT ON access.session, access.local_credential, access.api_token,
            access.service_principal_secret, access.desktop_authorization_code,
            access.device_authorization, access.authoring_credential,
            access.oauth_client, access.oauth_session,
            access.oauth_client_assertion FROM leapview_control_readonly;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON ALL TABLES IN SCHEMA access, audit FROM leapview_control_readonly;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'leapview_control_backup') THEN
        GRANT USAGE ON SCHEMA access, audit TO leapview_control_backup;
        GRANT SELECT ON ALL TABLES IN SCHEMA access, audit TO leapview_control_backup;
        REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER
            ON ALL TABLES IN SCHEMA access, audit FROM leapview_control_backup;
    END IF;
END
$$;`
