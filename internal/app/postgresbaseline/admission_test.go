package postgresbaseline

import (
	"context"
	"errors"
	"testing"

	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
)

type admissionReaderFake struct {
	extension platformpostgres.Extension
	err       error
	allowed   map[string]bool
}

func (f admissionReaderFake) RequiredExtension(context.Context, string) (platformpostgres.Extension, error) {
	return f.extension, f.err
}

func (f admissionReaderFake) HasSchemaPrivilege(_ context.Context, object, privilege string) (bool, error) {
	return f.allowed["schema:"+object+":"+privilege], f.err
}

func (f admissionReaderFake) HasTablePrivilege(_ context.Context, object, privilege string) (bool, error) {
	return f.allowed["table:"+object+":"+privilege], f.err
}

func (f admissionReaderFake) HasFunctionPrivilege(_ context.Context, object, privilege string) (bool, error) {
	return f.allowed["function:"+object+":"+privilege], f.err
}

func (f admissionReaderFake) HasCurrentDatabasePrivilege(_ context.Context, privilege string) (bool, error) {
	return f.allowed["database::"+privilege], f.err
}

func TestServingAdmissionsAcceptReviewedPrivileges(t *testing.T) {
	const metadataSchema = "leapview_catalog_0123456789abcdef0123456789abcdef"
	for name, test := range map[string]struct {
		verify  func(context.Context, AdmissionReader, string) error
		allowed map[string]bool
	}{
		"runtime": {
			verify: func(ctx context.Context, reader AdmissionReader, _ string) error {
				return VerifyControlRuntimeAdmission(ctx, reader)
			},
			allowed: map[string]bool{
				"schema:recovery:USAGE":                true,
				"table:public.goose_db_version:SELECT": true,
				"table:recovery.recovery_set:SELECT":   true,
				"function:managed_data.publish_binding_set(text,text,text,text,bigint,jsonb):EXECUTE": true,
			},
		},
		"maintenance": {
			verify: func(ctx context.Context, reader AdmissionReader, _ string) error {
				return VerifyControlMaintenanceAdmission(ctx, reader)
			},
			allowed: map[string]bool{
				"schema:recovery:USAGE":                   true,
				"table:recovery.recovery_set:SELECT":      true,
				"table:recovery.recovery_set:INSERT":      true,
				"table:recovery.recovery_set:UPDATE":      true,
				"table:recovery.validation_result:INSERT": true,
				"function:delivery.create_recovery_retention_root(uuid,text,uuid,uuid,timestamptz,jsonb):EXECUTE": true,
			},
		},
		"readonly": {
			verify: func(ctx context.Context, reader AdmissionReader, _ string) error {
				return VerifyControlReadonlyAdmission(ctx, reader)
			},
			allowed: map[string]bool{
				"schema:platform:USAGE":                true,
				"table:public.goose_db_version:SELECT": true,
				"table:recovery.recovery_set:SELECT":   true,
			},
		},
		"ducklake": {
			verify: VerifyDuckLakeAdmission,
			allowed: map[string]bool{
				"schema:" + metadataSchema + ":USAGE": true,
				"database::CONNECT":                   true,
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			reader := admissionReaderFake{
				extension: platformpostgres.Extension{Name: "pgcrypto", Schema: "managed_data"},
				allowed:   test.allowed,
			}
			if err := test.verify(t.Context(), reader, metadataSchema); err != nil {
				t.Fatalf("admission = %v", err)
			}
		})
	}
}

func TestServingAdmissionsRejectExtensionAndPrivilegeDrift(t *testing.T) {
	valid := admissionReaderFake{
		extension: platformpostgres.Extension{Name: "pgcrypto", Schema: "managed_data"},
		allowed: map[string]bool{
			"schema:recovery:USAGE":                true,
			"table:public.goose_db_version:SELECT": true,
			"table:recovery.recovery_set:SELECT":   true,
			"function:managed_data.publish_binding_set(text,text,text,text,bigint,jsonb):EXECUTE": true,
		},
	}
	for _, test := range []struct {
		name   string
		mutate func(*admissionReaderFake)
	}{
		{name: "extension schema", mutate: func(f *admissionReaderFake) { f.extension.Schema = "public" }},
		{name: "missing positive privilege", mutate: func(f *admissionReaderFake) { delete(f.allowed, "schema:recovery:USAGE") }},
		{name: "forbidden privilege", mutate: func(f *admissionReaderFake) { f.allowed["table:public.goose_db_version:UPDATE"] = true }},
		{name: "runtime recovery-root function grant", mutate: func(f *admissionReaderFake) {
			f.allowed["function:delivery.create_recovery_retention_root(uuid,text,uuid,uuid,timestamptz,jsonb):EXECUTE"] = true
		}},
		{name: "probe error", mutate: func(f *admissionReaderFake) { f.err = errors.New("probe unavailable") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := valid
			reader.allowed = make(map[string]bool, len(valid.allowed))
			for key, value := range valid.allowed {
				reader.allowed[key] = value
			}
			test.mutate(&reader)
			if err := VerifyControlRuntimeAdmission(t.Context(), reader); err == nil {
				t.Fatal("runtime admission accepted drift")
			}
		})
	}
}
