package app

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
)

func TestCandidateSourceBlobAuditRecorderPersistsAccessAudit(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	principal := testPrincipal(t, ctx, store, "author@example.com", "Author", "owner")
	accessModule, err := accessmodule.Build(ctx, accessmodule.Config{
		Database: store.SQLDB(), WorkspaceID: "test",
		Auth: accessmodule.AuthConfig{Disabled: true},
	})
	if err != nil {
		t.Fatalf("build access module: %v", err)
	}
	record := candidateSourceBlobAuditRecorder(accessModule, "test")

	err = record(ctx, deploymentmodule.CandidateSourceBlobAuditEvent{
		PrincipalID: principal.ID, ProjectID: "finance",
		Digest: "sha256:test", Action: "candidate.source_blob_uploaded",
		Privilege: "AUTHOR_PROJECT", Status: "success",
		RequestID: "req-source-blob", CorrelationID: "corr-source-blob",
		MetadataJSON: `{"operationId":"uploadProjectCandidateSourceBlob","surface":"api"}`,
	})
	if err != nil {
		t.Fatalf("record candidate source blob audit: %v", err)
	}
	events, err := testAccessRepository(store).ListAuditEvents(ctx, access.AuditEventFilter{
		WorkspaceID: "test", Action: "candidate.source_blob_uploaded",
	})
	if err != nil {
		t.Fatalf("list candidate source blob audits: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("candidate source blob audits = %d, want 1", len(events))
	}
	event := events[0]
	if event.PrincipalID != principal.ID || event.TargetType != "project" ||
		event.TargetID != "finance" || event.Privilege != access.PrivilegeAuthorProject ||
		event.Status != "success" || event.RequestID != "req-source-blob" ||
		event.CorrelationID != "corr-source-blob" {
		t.Fatalf("persisted candidate source blob audit = %#v", event)
	}
}
