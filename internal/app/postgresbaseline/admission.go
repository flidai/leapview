package postgresbaseline

import (
	"context"
	"errors"
	"fmt"

	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
)

// AdmissionReader exposes only catalog and effective-privilege probes. The
// implementation is PostgreSQL/sqlc-backed; the reviewed manifest below stays
// with application composition because it names product schemas and roles.
type AdmissionReader interface {
	RequiredExtension(context.Context, string) (platformpostgres.Extension, error)
	HasSchemaPrivilege(context.Context, string, string) (bool, error)
	HasTablePrivilege(context.Context, string, string) (bool, error)
	HasFunctionPrivilege(context.Context, string, string) (bool, error)
	HasCurrentDatabasePrivilege(context.Context, string) (bool, error)
}

type privilegeProbe struct {
	kind      string
	object    string
	privilege string
	want      bool
}

// VerifyControlRuntimeAdmission detects extension or effective-ACL drift after
// migrations and before the ordinary runtime pool is admitted to traffic.
func VerifyControlRuntimeAdmission(ctx context.Context, reader AdmissionReader) error {
	if err := verifyControlExtension(ctx, reader); err != nil {
		return err
	}
	return verifyPrivileges(ctx, reader, []privilegeProbe{
		{kind: "schema", object: "recovery", privilege: "USAGE", want: true},
		{kind: "table", object: "public.goose_db_version", privilege: "SELECT", want: true},
		{kind: "table", object: "public.goose_db_version", privilege: "UPDATE", want: false},
		{kind: "table", object: "recovery.recovery_set", privilege: "SELECT", want: true},
		{kind: "table", object: "recovery.recovery_set", privilege: "UPDATE", want: false},
		{kind: "function", object: "managed_data.publish_binding_set(text,text,text,text,bigint,jsonb)", privilege: "EXECUTE", want: true},
		{kind: "function", object: "delivery.create_recovery_retention_root(uuid,text,uuid,uuid,timestamptz,jsonb)", privilege: "EXECUTE", want: false},
	})
}

// VerifyControlMaintenanceAdmission protects the narrow recovery-maintenance
// writer from both missing grants and accidental child-evidence mutation.
func VerifyControlMaintenanceAdmission(ctx context.Context, reader AdmissionReader) error {
	return verifyPrivileges(ctx, reader, []privilegeProbe{
		{kind: "schema", object: "recovery", privilege: "USAGE", want: true},
		{kind: "table", object: "recovery.recovery_set", privilege: "SELECT", want: true},
		{kind: "table", object: "recovery.recovery_set", privilege: "INSERT", want: true},
		{kind: "table", object: "recovery.recovery_set", privilege: "UPDATE", want: true},
		{kind: "table", object: "recovery.recovery_set", privilege: "DELETE", want: false},
		{kind: "table", object: "recovery.validation_result", privilege: "INSERT", want: true},
		{kind: "table", object: "recovery.validation_result", privilege: "UPDATE", want: false},
		{kind: "function", object: "delivery.create_recovery_retention_root(uuid,text,uuid,uuid,timestamptz,jsonb)", privilege: "EXECUTE", want: true},
	})
}

// VerifyControlReadonlyAdmission samples the reporting role's positive read
// contract and its write denial at immutable platform/recovery boundaries.
func VerifyControlReadonlyAdmission(ctx context.Context, reader AdmissionReader) error {
	return verifyPrivileges(ctx, reader, []privilegeProbe{
		{kind: "schema", object: "platform", privilege: "USAGE", want: true},
		{kind: "table", object: "public.goose_db_version", privilege: "SELECT", want: true},
		{kind: "table", object: "public.goose_db_version", privilege: "INSERT", want: false},
		{kind: "table", object: "recovery.recovery_set", privilege: "SELECT", want: true},
		{kind: "table", object: "recovery.recovery_set", privilege: "UPDATE", want: false},
		{kind: "function", object: "delivery.create_recovery_retention_root(uuid,text,uuid,uuid,timestamptz,jsonb)", privilege: "EXECUTE", want: false},
	})
}

// VerifyDuckLakeAdmission proves the retained catalog credential can connect
// and cross the exact per-pool namespace boundary without database/schema
// creation authority. DuckLake attach remains responsible for its versioned
// metadata table and sequence compatibility checks.
func VerifyDuckLakeAdmission(ctx context.Context, reader AdmissionReader, metadataSchema string) error {
	if metadataSchema == "" {
		return errors.New("PostgreSQL DuckLake metadata schema is required")
	}
	return verifyPrivileges(ctx, reader, []privilegeProbe{
		{kind: "database", privilege: "CONNECT", want: true},
		{kind: "database", privilege: "CREATE", want: false},
		{kind: "schema", object: metadataSchema, privilege: "USAGE", want: true},
		{kind: "schema", object: metadataSchema, privilege: "CREATE", want: false},
	})
}

func verifyControlExtension(ctx context.Context, reader AdmissionReader) error {
	if reader == nil {
		return errors.New("PostgreSQL admission reader is required")
	}
	extension, err := reader.RequiredExtension(ctx, "pgcrypto")
	if err != nil {
		return fmt.Errorf("required PostgreSQL extension is unavailable: %w", err)
	}
	if extension.Name != "pgcrypto" || extension.Schema != "managed_data" {
		return errors.New("required PostgreSQL extension identity is invalid")
	}
	return nil
}

func verifyPrivileges(ctx context.Context, reader AdmissionReader, probes []privilegeProbe) error {
	if reader == nil {
		return errors.New("PostgreSQL admission reader is required")
	}
	for _, probe := range probes {
		var (
			allowed bool
			err     error
		)
		switch probe.kind {
		case "schema":
			allowed, err = reader.HasSchemaPrivilege(ctx, probe.object, probe.privilege)
		case "table":
			allowed, err = reader.HasTablePrivilege(ctx, probe.object, probe.privilege)
		case "function":
			allowed, err = reader.HasFunctionPrivilege(ctx, probe.object, probe.privilege)
		case "database":
			allowed, err = reader.HasCurrentDatabasePrivilege(ctx, probe.privilege)
		default:
			return errors.New("unsupported PostgreSQL privilege probe")
		}
		if err != nil {
			return fmt.Errorf("probe PostgreSQL %s privilege: %w", probe.kind, err)
		}
		if allowed != probe.want {
			object := probe.object
			if object == "" {
				object = "current database"
			}
			expected := "denied"
			if probe.want {
				expected = "granted"
			}
			return fmt.Errorf("PostgreSQL %s privilege %s on %q must be %s", probe.kind, probe.privilege, object, expected)
		}
	}
	return nil
}
