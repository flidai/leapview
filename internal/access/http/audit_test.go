package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/workspace"
	workspacesqlite "github.com/flidai/leapview/internal/workspace/sqlite"
)

type auditedMutationRepository struct {
	access.Repository
	called bool
}

func (r *auditedMutationRepository) RunAuditedMutation(ctx context.Context, mutation func(access.Repository) (access.AuditEventInput, error)) error {
	r.called = true
	_, err := mutation(r.Repository)
	return err
}

func TestRunAuditedMutationUsesRepositoryTransaction(t *testing.T) {
	repo := &auditedMutationRepository{}
	request := httptest.NewRequest(stdhttp.MethodPost, "/", nil)
	mutationCalled := false

	err := runAuditedMutation(request, repo, func(access.Repository) (access.AuditEventInput, error) {
		mutationCalled = true
		return access.AuditEventInput{Action: "grant.created"}, nil
	})
	if err != nil {
		t.Fatalf("run audited mutation: %v", err)
	}
	if !repo.called || !mutationCalled {
		t.Fatalf("transaction called = %v, mutation called = %v", repo.called, mutationCalled)
	}
}

func TestRunAuditedMutationRejectsRepositoryWithoutTransactionBeforeMutation(t *testing.T) {
	repo := struct{ access.Repository }{}
	request := httptest.NewRequest(stdhttp.MethodPost, "/", nil)
	mutationCalled := false

	err := runAuditedMutation(request, repo, func(access.Repository) (access.AuditEventInput, error) {
		mutationCalled = true
		return access.AuditEventInput{Action: "grant.created"}, nil
	})
	if !errors.Is(err, access.ErrAuditTransaction) {
		t.Fatalf("run audited mutation error = %v, want %v", err, access.ErrAuditTransaction)
	}
	if mutationCalled {
		t.Fatal("mutation ran without transactional audit support")
	}
}

func TestCreatePrincipalAuditsDuplicateRejectionSeparately(t *testing.T) {
	ctx := t.Context()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "access.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo := accesssqlite.NewRepository(store.SQLDB())
	handler := Handler{Repository: func() (access.Repository, error) { return repo, nil }}

	request := func(displayName string) *stdhttp.Request {
		r := httptest.NewRequest(stdhttp.MethodPost, "/api/v1/principals", strings.NewReader(
			`{"email":"duplicate@example.com","displayName":"`+displayName+`"}`,
		))
		r.Header.Set("Content-Type", "application/json")
		return r
	}
	first := httptest.NewRecorder()
	handler.CreatePrincipal(first, request("Original"))
	if first.Code != stdhttp.StatusCreated {
		t.Fatalf("first status = %d, body=%s", first.Code, first.Body.String())
	}
	duplicate := httptest.NewRecorder()
	handler.CreatePrincipal(duplicate, request("Replacement"))
	if duplicate.Code != stdhttp.StatusConflict {
		t.Fatalf("duplicate status = %d, body=%s", duplicate.Code, duplicate.Body.String())
	}

	created, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{Action: "principal.local_user.created"})
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{Action: "principal.local_user.create_rejected"})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 || len(rejected) != 1 || rejected[0].Status != "conflict" {
		t.Fatalf("created/rejected audit events = %d/%d (%+v)", len(created), len(rejected), rejected)
	}
}

func TestUpdateAndDeletePrincipalPersistRequiredSuccessAudits(t *testing.T) {
	ctx := t.Context()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "access.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo := accesssqlite.NewRepository(store.SQLDB())
	if _, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{
		ID: "principal-admin", Kind: access.PrincipalKindUser,
		Email: "admin@example.com", DisplayName: "Admin",
	}); err != nil {
		t.Fatalf("create audit actor: %v", err)
	}
	created, err := repo.CreateLocalUser(ctx, access.LocalUserInput{
		Email: "mutable@example.com", DisplayName: "Original", MustChange: true,
	})
	if err != nil {
		t.Fatalf("create local user: %v", err)
	}
	handler := Handler{
		Repository: func() (access.Repository, error) { return repo, nil },
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: "principal-admin"}, true
		},
	}

	updateRequest := requestWithRouteParam(stdhttp.MethodPatch, "/api/v1/principals/"+created.Principal.ID, "principal", created.Principal.ID)
	updateRequest.Body = io.NopCloser(strings.NewReader(`{"displayName":"Updated"}`))
	updateRequest.Header.Set("Content-Type", "application/json")
	updateRequest.Header.Set("If-Match", resourceETag(principalDTO(created.Principal)))
	updated := httptest.NewRecorder()
	handler.UpdatePrincipal(updated, updateRequest)
	if updated.Code != stdhttp.StatusOK {
		t.Fatalf("update status = %d, body=%s", updated.Code, updated.Body.String())
	}

	deleteRequest := requestWithRouteParam(stdhttp.MethodDelete, "/api/v1/principals/"+created.Principal.ID, "principal", created.Principal.ID)
	deleted := httptest.NewRecorder()
	handler.DeletePrincipal(deleted, deleteRequest)
	if deleted.Code != stdhttp.StatusNoContent {
		t.Fatalf("delete status = %d, body=%s", deleted.Code, deleted.Body.String())
	}

	for _, action := range []string{"principal.updated", "principal.deleted"} {
		events, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{Action: action})
		if err != nil {
			t.Fatalf("list %s audits: %v", action, err)
		}
		if len(events) != 1 || events[0].PrincipalID != "principal-admin" || events[0].TargetID != created.Principal.ID || events[0].Status != "success" {
			t.Fatalf("%s audits = %#v", action, events)
		}
	}
}

func TestDashboardPublicationSubjectsAreLimitedToDataPolicies(t *testing.T) {
	if knownGrantSubjectType(access.SubjectDashboardPublication) {
		t.Fatal("dashboard publication subject was accepted for an RBAC grant")
	}
	if !knownDataPolicySubjectType(access.SubjectDashboardPublication) {
		t.Fatal("dashboard publication subject was rejected for a data policy")
	}
}

func TestDashboardPublicationPrincipalsRejectGenericIdentityMutations(t *testing.T) {
	if principalKindAllowsGenericMutation(access.PrincipalKindDashboardPublication) {
		t.Fatal("dashboard publication principal accepted a generic identity mutation")
	}
	if !principalKindAllowsGenericMutation(access.PrincipalKindUser) {
		t.Fatal("user principal rejected a generic identity mutation")
	}
}
func TestListPlatformAuditEventsIncludesProductEventsAndFiltersWorkspace(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "access.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo := accesssqlite.NewRepository(store.SQLDB())
	workspaceRepo := workspacesqlite.NewRepository(store.SQLDB())
	for _, workspaceID := range []string{"sales", "finance"} {
		if err := workspaceRepo.Ensure(t.Context(), workspace.EnsureInput{ID: workspace.WorkspaceID(workspaceID), Title: workspaceID}); err != nil {
			t.Fatal(err)
		}
	}
	for _, input := range []access.AuditEventInput{
		{Action: "product.updated", TargetType: "product", TargetID: "instance", Status: "success"},
		{WorkspaceID: "sales", Action: "workspace.updated", TargetType: "workspace", TargetID: "sales", Status: "success"},
		{WorkspaceID: "finance", Action: "workspace.updated", TargetType: "workspace", TargetID: "finance", Status: "success"},
	} {
		if err := repo.RecordAuditEvent(t.Context(), input); err != nil {
			t.Fatal(err)
		}
	}
	handler := Handler{Repository: func() (access.Repository, error) { return repo, nil }}

	type event struct {
		Action      string  `json:"action"`
		WorkspaceID *string `json:"workspaceId"`
		PrincipalID *string `json:"principalId"`
		Privilege   *string `json:"privilege"`
		RequestID   *string `json:"requestId"`
		Correlation *string `json:"correlationId"`
	}
	type response struct {
		Items []event `json:"items"`
	}
	list := func(path string) response {
		t.Helper()
		recorder := httptest.NewRecorder()
		handler.ListPlatformAuditEvents(recorder, httptest.NewRequest(stdhttp.MethodGet, path, nil))
		if recorder.Code != stdhttp.StatusOK {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		var result response
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatalf("decode response: %v body=%s", err, recorder.Body.String())
		}
		return result
	}

	all := list("/api/v1/audit-events")
	if len(all.Items) != 3 {
		t.Fatalf("all audit events = %#v, want 3", all.Items)
	}
	productFound := false
	for _, item := range all.Items {
		if item.Action == "product.updated" {
			productFound = true
			if item.WorkspaceID != nil {
				t.Fatalf("product event workspaceId = %q, want omitted", *item.WorkspaceID)
			}
			if item.PrincipalID != nil || item.Privilege != nil || item.RequestID != nil || item.Correlation != nil {
				t.Fatalf("product event emitted empty optional fields: %#v", item)
			}
		}
	}
	if !productFound {
		t.Fatalf("product event missing from %#v", all.Items)
	}

	sales := list("/api/v1/audit-events?workspace=sales")
	if len(sales.Items) != 1 || sales.Items[0].WorkspaceID == nil || *sales.Items[0].WorkspaceID != "sales" {
		t.Fatalf("sales audit events = %#v", sales.Items)
	}
}
