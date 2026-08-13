package app

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	manageddatamodule "github.com/flidai/leapview/internal/manageddata/module"
)

func TestManagedDataCommandAuditRecorderPersistsAccessAudit(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	principal := testPrincipal(t, ctx, store, "data@example.com", "Data Author", "owner")
	accessModule, err := accessmodule.Build(ctx, accessmodule.Config{
		Database: store.SQLDB(),
		Auth:     accessmodule.AuthConfig{Disabled: true},
	})
	if err != nil {
		t.Fatalf("build access module: %v", err)
	}
	record := managedDataCommandAuditRecorder(accessModule)

	err = record(ctx, manageddatamodule.CommandAuditEvent{
		PrincipalID: principal.ID, Action: "managed_data.upload_session.created",
		TargetType: "managed_data_upload_session", TargetID: "upload-a",
		Privilege: "AUTHOR_PROJECT", Status: "success",
		RequestID: "req-upload", CorrelationID: "corr-upload",
		MetadataJSON: `{"operationId":"createManagedDataUploadSession","surface":"api"}`,
	})
	if err != nil {
		t.Fatalf("record managed-data audit: %v", err)
	}
	events, err := testAccessRepository(store).ListAuditEvents(ctx, access.AuditEventFilter{
		Action: "managed_data.upload_session.created",
	})
	if err != nil {
		t.Fatalf("list managed-data audits: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("managed-data audits = %d, want 1", len(events))
	}
	event := events[0]
	if event.WorkspaceID != "" || event.PrincipalID != principal.ID || event.TargetType != "managed_data_upload_session" ||
		event.TargetID != "upload-a" || event.Privilege != access.PrivilegeAuthorProject ||
		event.Status != "success" || event.RequestID != "req-upload" || event.CorrelationID != "corr-upload" {
		t.Fatalf("persisted managed-data audit = %#v", event)
	}
}

func TestManagedDataCommandAuditRecorderRejectsInvalidComposition(t *testing.T) {
	if err := managedDataCommandAuditRecorder(nil)(t.Context(), manageddatamodule.CommandAuditEvent{}); err == nil {
		t.Fatal("nil access module accepted")
	}
	if err := managedDataCommandAuditRecorder(&accessmodule.Module{})(t.Context(), manageddatamodule.CommandAuditEvent{Privilege: "NOT_A_PRIVILEGE"}); err == nil {
		t.Fatal("invalid privilege accepted")
	}
}
