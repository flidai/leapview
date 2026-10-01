package http

import (
	"context"
	"encoding/json"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type auditReadRepository struct {
	access.Repository
	filter access.AuditEventFilter
	events []access.AuditEvent
	called bool
}

func (r *auditReadRepository) ListAuditEvents(_ context.Context, filter access.AuditEventFilter) ([]access.AuditEvent, error) {
	r.called = true
	r.filter = filter
	return append([]access.AuditEvent(nil), r.events...), nil
}

func projectAuditHandler(repo *auditReadRepository) Handler {
	auditRead, err := access.NewProjectPermissionPair(access.ActionAuditRead, "project:test")
	if err != nil {
		panic(err)
	}
	return Handler{
		Repository: func() (access.Repository, error) { return repo, nil },
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: "principal-admin"}, true
		},
		CurrentEffectivePermissionOptions: func(context.Context, string) ([]access.PermissionPair, error) {
			return []access.PermissionPair{auditRead}, nil
		},
		PlatformAdmin: func(context.Context, string) (bool, error) { return true, nil },
		CurrentProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return "project:test", nil
		},
	}
}

func TestListAuditEventsBindsProjectAndPreservesProjectIdentity(t *testing.T) {
	repo := &auditReadRepository{events: []access.AuditEvent{{
		ID: "audit-1", ProjectID: "project:test", Action: "project.read",
		ResourceKind: "project", ResourceID: "project:test", MetadataJSON: `{}`,
		CreatedAt: "2026-09-13T00:00:00Z",
	}}}
	handler := projectAuditHandler(repo)
	handler.PlatformAdmin = func(context.Context, string) (bool, error) { return false, nil }
	request := httptest.NewRequest(stdhttp.MethodGet, "/api/v1/projects/project:test/audit-events", nil)
	response := httptest.NewRecorder()

	handler.ListAuditEventsForProject(response, request, "project:test")

	if response.Code != stdhttp.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if repo.filter.ProjectID != "project:test" || repo.filter.IncludeUnscoped {
		t.Fatalf("audit filter = %#v, want exact Project scope", repo.filter)
	}
	var payload struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || payload.Items[0]["projectId"] != "project:test" {
		t.Fatalf("audit response = %#v, want Project identity", payload.Items)
	}
}

func TestListProjectAuditEventsRequiresTypedProjectPermissionAndCredentialCeiling(t *testing.T) {
	auditRead, err := access.NewProjectPermissionPair(access.ActionAuditRead, "project:test")
	if err != nil {
		t.Fatal(err)
	}
	otherProjectRead, err := access.NewProjectPermissionPair(access.ActionAuditRead, "project:other")
	if err != nil {
		t.Fatal(err)
	}
	accessRead, err := access.NewProjectPermissionPair(access.ActionProjectAccessRead, "project:test")
	if err != nil {
		t.Fatal(err)
	}
	platformAuditRead, err := access.NewInstancePermissionPair(access.ActionPlatformAuditRead, "instance:test")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		permissions []access.PermissionPair
		credential  *access.APICredential
		platform    bool
		wantStatus  int
	}{
		{name: "missing audit permission", permissions: []access.PermissionPair{accessRead}, platform: true, wantStatus: stdhttp.StatusForbidden},
		{name: "audit permission for another project", permissions: []access.PermissionPair{otherProjectRead}, platform: true, wantStatus: stdhttp.StatusForbidden},
		{name: "instance audit permission is not project audit permission", permissions: []access.PermissionPair{platformAuditRead}, platform: true, wantStatus: stdhttp.StatusForbidden},
		{name: "platform administrator does not imply project audit read", permissions: []access.PermissionPair{}, platform: true, wantStatus: stdhttp.StatusForbidden},
		{
			name:        "API token attenuates project audit permission",
			permissions: []access.PermissionPair{auditRead}, platform: true,
			credential: &access.APICredential{
				Principal: access.Principal{ID: "principal-admin"},
				Token:     access.APIToken{ID: "token-1", PrincipalID: "principal-admin", PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{accessRead}},
			}, wantStatus: stdhttp.StatusForbidden,
		},
		{
			name:        "API token must target this project",
			permissions: []access.PermissionPair{auditRead}, platform: true,
			credential: &access.APICredential{
				Principal: access.Principal{ID: "principal-admin"},
				Token:     access.APIToken{ID: "token-1", PrincipalID: "principal-admin", PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{otherProjectRead}},
			}, wantStatus: stdhttp.StatusForbidden,
		},
		{
			name:        "API token with exact project audit permission",
			permissions: []access.PermissionPair{auditRead}, platform: false,
			credential: &access.APICredential{
				Principal: access.Principal{ID: "principal-admin"},
				Token:     access.APIToken{ID: "token-1", PrincipalID: "principal-admin", PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{auditRead}},
			}, wantStatus: stdhttp.StatusOK,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &auditReadRepository{}
			handler := projectAuditHandler(repo)
			handler.CurrentEffectivePermissionOptions = func(context.Context, string) ([]access.PermissionPair, error) {
				return test.permissions, nil
			}
			handler.PlatformAdmin = func(context.Context, string) (bool, error) { return test.platform, nil }
			if test.credential != nil {
				handler.CurrentCredential = func(*stdhttp.Request) (access.APICredential, bool) {
					return *test.credential, true
				}
			}
			request := httptest.NewRequest(stdhttp.MethodGet, "/api/v1/projects/project:test/audit-events", nil)
			response := httptest.NewRecorder()

			handler.ListAuditEventsForProject(response, request, "project:test")

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, body=%s; want %d", response.Code, response.Body.String(), test.wantStatus)
			}
			if test.wantStatus == stdhttp.StatusOK {
				if !repo.called || repo.filter.ProjectID != "project:test" || repo.filter.IncludeUnscoped {
					t.Fatalf("audit filter = %#v, called=%v; want project:test without unscoped events", repo.filter, repo.called)
				}
			} else if repo.called {
				t.Fatal("unauthorized project audit request reached the repository")
			}
		})
	}
}

func TestListAuditEventsRejectsForeignProjectBeforeRepositoryRead(t *testing.T) {
	repo := &auditReadRepository{}
	request := httptest.NewRequest(stdhttp.MethodGet, "/api/v1/projects/project:foreign/audit-events", nil)
	response := httptest.NewRecorder()

	projectAuditHandler(repo).ListAuditEventsForProject(response, request, "project:foreign")

	if response.Code != stdhttp.StatusNotFound {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if repo.called {
		t.Fatal("foreign Project reached the audit repository")
	}
}

func TestListPlatformAuditEventsLimitsProjectRowsAndIncludesPlatformEvents(t *testing.T) {
	repo := &auditReadRepository{}
	request := httptest.NewRequest(stdhttp.MethodGet, "/api/v1/audit-events", nil)
	response := httptest.NewRecorder()

	projectAuditHandler(repo).ListPlatformAuditEvents(response, request)

	if response.Code != stdhttp.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if repo.filter.ProjectID != "project:test" || !repo.filter.IncludeUnscoped {
		t.Fatalf("platform audit filter = %#v, want bound Project plus unscoped events", repo.filter)
	}
}

func TestListPlatformAuditEventsStillRequiresPlatformAdmin(t *testing.T) {
	repo := &auditReadRepository{}
	handler := projectAuditHandler(repo)
	handler.PlatformAdmin = func(context.Context, string) (bool, error) { return false, nil }
	request := httptest.NewRequest(stdhttp.MethodGet, "/api/v1/audit-events", nil)
	response := httptest.NewRecorder()

	handler.ListPlatformAuditEvents(response, request)

	if response.Code != stdhttp.StatusForbidden {
		t.Fatalf("status = %d, body=%s; want %d", response.Code, response.Body.String(), stdhttp.StatusForbidden)
	}
	if repo.called {
		t.Fatal("non-admin platform audit request reached the repository")
	}
}

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
	if err == nil || err.Error() != "transactional access repository is required" {
		t.Fatalf("run audited mutation error = %v, want missing transaction error", err)
	}
	if mutationCalled {
		t.Fatal("mutation ran without transactional audit support")
	}
}

func TestCreatePrincipalAuditsDuplicateRejectionSeparately(t *testing.T) {
	ctx := t.Context()
	repo := openAccessHTTPTestStore(t).repository
	admin, err := repo.SetPlatformRole(ctx, access.PlatformRoleInput{Email: "admin@example.com", Role: access.PlatformRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler{Repository: func() (access.Repository, error) { return repo, nil }, CurrentEffectiveCapabilities: allowProjectAdmin, CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
		return Principal{ID: admin.ID, Kind: access.PrincipalKindUser}, true
	}}

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
	if duplicate.Code != stdhttp.StatusBadRequest {
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
	repo := openAccessHTTPTestStore(t).repository
	admin, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{
		Kind:  access.PrincipalKindUser,
		Email: "admin@example.com", DisplayName: "Admin",
	})
	if err != nil {
		t.Fatalf("create audit actor: %v", err)
	}
	if _, err := repo.SetPlatformRole(ctx, access.PlatformRoleInput{PrincipalID: admin.ID, Email: admin.Email, Role: access.PlatformRoleAdmin}); err != nil {
		t.Fatalf("set audit actor role: %v", err)
	}
	created, err := repo.CreateLocalUser(ctx, access.LocalUserInput{
		Email: "mutable@example.com", DisplayName: "Original", MustChange: true,
	})
	if err != nil {
		t.Fatalf("create local user: %v", err)
	}
	handler := Handler{
		Repository:                   func() (access.Repository, error) { return repo, nil },
		CurrentEffectiveCapabilities: allowProjectAdmin,
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: admin.ID}, true
		},
	}

	updateRequest := requestWithRouteParam(stdhttp.MethodPatch, "/api/v1/principals/"+created.Principal.ID, "principal", created.Principal.ID)
	updateRequest.Body = io.NopCloser(strings.NewReader(`{"displayName":"Updated"}`))
	updateRequest.Header.Set("Content-Type", "application/json")
	updateRequest.Header.Set("If-Match", "*")
	updateOperation := accessgen.GenCommandOperationUpdatePrincipal().APIGenOperationID()
	updateContract, ok := accessgen.GetAPIGenCommandRuntimeContract(updateOperation)
	if !ok {
		t.Fatalf("missing generated command contract for %s", updateOperation)
	}
	updateContext, updateGuard, err := apigencommand.Begin(updateRequest.Context(), updateContract)
	if err != nil {
		t.Fatal(err)
	}
	updateRequest = updateRequest.WithContext(updateContext)
	updated := httptest.NewRecorder()
	handler.UpdatePrincipal(updated, updateRequest)
	if updated.Code != stdhttp.StatusOK {
		t.Fatalf("update status = %d, body=%s", updated.Code, updated.Body.String())
	}
	if !updateGuard.Completed() {
		t.Fatal("principal update bypassed its generated transactional command")
	}

	deleteRequest := requestWithRouteParam(stdhttp.MethodDelete, "/api/v1/principals/"+created.Principal.ID, "principal", created.Principal.ID)
	deleteOperation := accessgen.GenCommandOperationDeletePrincipal().APIGenOperationID()
	deleteContract, ok := accessgen.GetAPIGenCommandRuntimeContract(deleteOperation)
	if !ok {
		t.Fatalf("missing generated command contract for %s", deleteOperation)
	}
	deleteContext, deleteGuard, err := apigencommand.Begin(deleteRequest.Context(), deleteContract)
	if err != nil {
		t.Fatal(err)
	}
	deleteRequest = deleteRequest.WithContext(deleteContext)
	deleted := httptest.NewRecorder()
	handler.DeletePrincipal(deleted, deleteRequest)
	if deleted.Code != stdhttp.StatusNoContent {
		t.Fatalf("delete status = %d, body=%s", deleted.Code, deleted.Body.String())
	}
	if !deleteGuard.Completed() {
		t.Fatal("principal deletion bypassed its generated transactional command")
	}

	for _, action := range []string{"principal.updated", "principal.deleted"} {
		events, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{Action: action})
		if err != nil {
			t.Fatalf("list %s audits: %v", action, err)
		}
		if len(events) != 1 || events[0].PrincipalID != admin.ID || events[0].ResourceID != created.Principal.ID || events[0].Status != "success" {
			t.Fatalf("%s audits = %#v", action, events)
		}
	}
}
