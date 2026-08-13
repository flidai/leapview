package app

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
)

func TestWorkspaceAssetGrantUIUsesCommandContractAndAudits(t *testing.T) {
	store := testStore(t)
	seedActiveDeployment(t, store, "test")
	ctx := context.Background()
	owner := testPrincipal(t, ctx, store, "owner@example.com", "Owner", "owner")
	token := testAPIToken(t, ctx, store, owner.ID, "test")
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, WorkspaceID: "test"}))
	repository := testAccessRepository(store)

	upsert := httptest.NewRequest(http.MethodPost, "/workspaces/test/assets/semantic_model:test.sales/access/upsert", bytes.NewBufferString(
		`{"workspaceAccess":{"command":{"email":"analyst@example.com","privilege":"VIEW_ITEM"}}}`,
	))
	upsert.Header.Set("Authorization", "Bearer "+token)
	claimUICommands(upsert, accessgen.GenUIActionCreateGrant())
	upsert.Header.Set("X-Request-ID", "req-grant-create-ui")
	upsertRecorder := httptest.NewRecorder()
	server.Routes().ServeHTTP(upsertRecorder, upsert)
	if upsertRecorder.Code != http.StatusOK || !strings.Contains(upsertRecorder.Body.String(), "Access updated.") {
		t.Fatalf("upsert status = %d body=%s", upsertRecorder.Code, upsertRecorder.Body.String())
	}

	grants, err := repository.ListGrants(ctx, access.ItemObject(access.SecurableSemanticModel, "test", "test.sales"))
	if err != nil || len(grants) != 1 {
		t.Fatalf("grants after UI upsert = %#v, %v", grants, err)
	}
	createdEvents, err := repository.ListAuditEvents(ctx, access.AuditEventFilter{WorkspaceID: "test", Action: "grant.created"})
	if err != nil || len(createdEvents) != 1 {
		t.Fatalf("grant.created audit events = %#v, %v", createdEvents, err)
	}
	assertOperationAuditMetadata(t, createdEvents[0], "createGrant", "ui")
	if createdEvents[0].RequestID != "req-grant-create-ui" {
		t.Fatalf("grant.created request id = %q", createdEvents[0].RequestID)
	}

	remove := httptest.NewRequest(http.MethodPost, "/workspaces/test/assets/semantic_model:test.sales/access/remove", bytes.NewBufferString(
		`{"workspaceAccess":{"command":{"bindingId":"`+grants[0].ID+`"}}}`,
	))
	remove.Header.Set("Authorization", "Bearer "+token)
	claimUICommands(remove, accessgen.GenUIActionDeleteGrant())
	remove.Header.Set("X-Request-ID", "req-grant-delete-ui")
	removeRecorder := httptest.NewRecorder()
	server.Routes().ServeHTTP(removeRecorder, remove)
	if removeRecorder.Code != http.StatusOK || !strings.Contains(removeRecorder.Body.String(), "Access removed.") {
		t.Fatalf("remove status = %d body=%s", removeRecorder.Code, removeRecorder.Body.String())
	}
	deletedEvents, err := repository.ListAuditEvents(ctx, access.AuditEventFilter{WorkspaceID: "test", Action: "grant.deleted"})
	if err != nil || len(deletedEvents) != 1 {
		t.Fatalf("grant.deleted audit events = %#v, %v", deletedEvents, err)
	}
	assertOperationAuditMetadata(t, deletedEvents[0], "deleteGrant", "ui")
}

func TestWorkspaceAssetGrantUIRejectsViewerWithoutMutationOrAudit(t *testing.T) {
	store := testStore(t)
	seedActiveDeployment(t, store, "test")
	ctx := context.Background()
	viewer := testPrincipal(t, ctx, store, "viewer@example.com", "Viewer", "viewer")
	token := testAPIToken(t, ctx, store, viewer.ID, "test")
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, WorkspaceID: "test"}))
	repository := testAccessRepository(store)

	request := httptest.NewRequest(http.MethodPost, "/workspaces/test/assets/semantic_model:test.sales/access/upsert", bytes.NewBufferString(
		`{"workspaceAccess":{"command":{"email":"analyst@example.com","privilege":"VIEW_ITEM"}}}`,
	))
	request.Header.Set("Authorization", "Bearer "+token)
	claimUICommands(request, accessgen.GenUIActionCreateGrant())
	recorder := httptest.NewRecorder()
	server.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}

	grants, err := repository.ListGrants(ctx, access.ItemObject(access.SecurableSemanticModel, "test", "test.sales"))
	if err != nil || len(grants) != 0 {
		t.Fatalf("grants after denied UI command = %#v, %v", grants, err)
	}
	events, err := repository.ListAuditEvents(ctx, access.AuditEventFilter{WorkspaceID: "test", Action: "grant.created"})
	if err != nil || len(events) != 0 {
		t.Fatalf("grant.created audits after denied UI command = %#v, %v", events, err)
	}
}
