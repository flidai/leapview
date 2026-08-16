package module

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/platform"
	projectsqlite "github.com/flidai/leapview/internal/project/sqlite"
	"github.com/flidai/leapview/internal/workspace"
	workspaceapi "github.com/flidai/leapview/internal/workspace/api"
)

func TestWorkspaceAdministrationReturnsScopedAccessAndCapabilityLinks(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	accessRepo := accesssqlite.NewRepository(store.SQLDB())
	workspaceRepo := projectsqlite.NewRepository(store.SQLDB())
	for _, input := range []workspace.EnsureInput{{ID: "sales", Title: "Sales"}, {ID: "other", Title: "Other"}} {
		if err := workspaceRepo.Ensure(t.Context(), input); err != nil {
			t.Fatal(err)
		}
	}
	owner, err := accessRepo.UpsertPrincipal(t.Context(), access.PrincipalInput{
		ID: "sales-owner", Kind: access.PrincipalKindUser, Email: "owner@example.test", DisplayName: "Sales Owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := accessRepo.UpsertPrincipal(t.Context(), access.PrincipalInput{
		ID: "sales-admin", Kind: access.PrincipalKindUser, Email: "admin@example.test", DisplayName: "Sales Admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	otherAdmin, err := accessRepo.UpsertPrincipal(t.Context(), access.PrincipalInput{
		ID: "other-admin", Kind: access.PrincipalKindUser, Email: "other@example.test", DisplayName: "Other Admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accessRepo.UpsertSecurableObject(t.Context(), access.WorkspaceObject("sales"), owner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := accessRepo.CreateRoleBinding(t.Context(), access.RoleBindingInput{
		ID: "sales-admin-binding", WorkspaceID: "sales", SubjectType: access.SubjectPrincipal, SubjectID: admin.ID, Role: access.RoleAdmin,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := accessRepo.CreateRoleBinding(t.Context(), access.RoleBindingInput{
		ID: "other-admin-binding", WorkspaceID: "other", SubjectType: access.SubjectPrincipal, SubjectID: otherAdmin.ID, Role: access.RoleAdmin,
	}); err != nil {
		t.Fatal(err)
	}

	module, err := Build(t.Context(), Config{
		ReadModel: workspaceRepo, AccessService: accessRepo, RuntimeEnvironment: "prod",
		Environment: func(*http.Request) string { return "prod" },
		CurrentPrincipal: func(*http.Request) (Principal, bool) {
			return Principal{ID: admin.ID, Email: admin.Email, DisplayName: admin.DisplayName}, true
		},
		AuthConfigured: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/sales/administration", nil)
	module.GetWorkspaceAdministration(recorder, request, "sales")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response workspaceapi.WorkspaceAdministrationResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v body=%s", err, recorder.Body.String())
	}
	if response.Workspace.ID != "sales" || response.Owner == nil || response.Owner.SubjectID != owner.ID {
		t.Fatalf("workspace/owner = %#v / %#v", response.Workspace, response.Owner)
	}
	if len(response.Administrators) != 1 || response.Administrators[0].SubjectID != admin.ID || response.Administrators[0].Role != access.RoleAdmin {
		t.Fatalf("administrators = %#v", response.Administrators)
	}
	if response.Administrators[0].SubjectID == otherAdmin.ID {
		t.Fatalf("other workspace administrator leaked: %#v", response.Administrators)
	}
	if !response.Capabilities.ManageWorkspace || !response.Capabilities.ManageAccess || !response.Capabilities.ManagePublications {
		t.Fatalf("admin capabilities = %#v", response.Capabilities)
	}
	if response.Links.RoleBindings != "/api/v1/workspaces/sales/role-bindings" || response.Links.Publications != "/api/v1/workspaces/sales/dashboard-publications" {
		t.Fatalf("domain links = %#v", response.Links)
	}
	if response.Links.ManagedConnections != "" || response.Links.Releases != "" || response.Links.Deployments != "" {
		t.Fatalf("project links emitted without an active project: %#v", response.Links)
	}
}

func TestWorkspaceAdministrationLinksRequireCollectionViewPrivileges(t *testing.T) {
	links := workspaceAdministrationLinks("sales team", "project", workspaceapi.WorkspaceAdministrationCapabilitiesResponse{
		ViewManagedData: true,
		ViewDeployments: true,
		ViewAgent:       true,
	})
	if links.ManagedConnections != "/api/v1/projects/project/connections" {
		t.Fatalf("managed connections link = %q", links.ManagedConnections)
	}
	if links.Deployments != "/api/v1/projects/project/deployments" {
		t.Fatalf("deployments link = %q", links.Deployments)
	}
	if links.AgentConversations != "/api/v1/agent/conversations" {
		t.Fatalf("agent conversations link = %q", links.AgentConversations)
	}

	actionOnly := workspaceAdministrationLinks("sales", "project", workspaceapi.WorkspaceAdministrationCapabilitiesResponse{
		IngestManagedData:  true,
		RequestDeployments: true,
		UseAgent:           true,
	})
	if actionOnly.ManagedConnections != "" || actionOnly.Deployments != "" || actionOnly.AgentConversations != "" {
		t.Fatalf("collection links emitted from action-only privileges: %#v", actionOnly)
	}
}

type administrationReadModel struct {
	workspace.ReadModel
	state workspace.AdministrationState
}

func (m administrationReadModel) AdministrationByID(context.Context, workspace.WorkspaceID, string) (workspace.AdministrationState, error) {
	return m.state, nil
}

func TestGetWorkspaceAdministrationNormalizesPublicTimestamps(t *testing.T) {
	state := workspace.AdministrationState{
		Workspace: workspace.Summary{
			ID: "sales", CreatedAt: "2026-08-10 11:12:13", UpdatedAt: "2026-08-10 11:13:14.123456789",
		},
		Environment:             "prod",
		ActiveServingStateSince: "2026-08-10T13:14:15+02:00",
		CurrentDeploymentSince:  "2026-08-10 11:15:16",
	}
	module, err := Build(t.Context(), Config{ReadModel: administrationReadModel{state: state}, RuntimeEnvironment: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	module.GetWorkspaceAdministration(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/sales/administration", nil), "sales")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response workspaceapi.WorkspaceAdministrationResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v body=%s", err, recorder.Body.String())
	}
	for field, value := range map[string]string{
		"workspace.createdAt":             response.Workspace.CreatedAt,
		"workspace.updatedAt":             response.Workspace.UpdatedAt,
		"runtime.activeServingStateSince": response.Runtime.ActiveServingStateSince,
		"runtime.currentDeploymentSince":  response.Runtime.CurrentDeploymentSince,
	} {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			t.Fatalf("%s = %q is not RFC3339: %v", field, value, err)
		}
		if parsed.Location() != time.UTC {
			t.Fatalf("%s = %q is not UTC", field, value)
		}
	}
}

func TestGetWorkspaceAdministrationOmitsEmptyOptionalTimestamps(t *testing.T) {
	state := workspace.AdministrationState{
		Workspace: workspace.Summary{ID: "sales", CreatedAt: "2026-08-10 11:12:13", UpdatedAt: "2026-08-10 11:13:14"},
	}
	module, err := Build(t.Context(), Config{ReadModel: administrationReadModel{state: state}, RuntimeEnvironment: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	module.GetWorkspaceAdministration(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/sales/administration", nil), "sales")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Runtime map[string]any `json:"runtime"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v body=%s", err, recorder.Body.String())
	}
	for _, field := range []string{"activeServingStateSince", "currentDeploymentSince"} {
		if _, present := response.Runtime[field]; present {
			t.Fatalf("empty optional field %q was emitted: %s", field, recorder.Body.String())
		}
	}
}
