package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

func TestAuthoringDevelopmentBypassIsRequestLocalAndIdentityBound(t *testing.T) {
	tests := []struct {
		name        string
		ctx         context.Context
		principalID string
		want        bool
	}{
		{name: "missing principal", ctx: context.Background(), principalID: "dev"},
		{name: "ordinary principal", ctx: accessmodule.WithPrincipal(context.Background(), accessmodule.Principal{ID: "dev"}), principalID: "dev"},
		{name: "different principal", ctx: accessmodule.WithPrincipal(context.Background(), accessmodule.Principal{ID: "dev", DevBypass: true}), principalID: "other"},
		{name: "development principal", ctx: accessmodule.WithPrincipal(context.Background(), accessmodule.Principal{ID: "dev", DevBypass: true}), principalID: "dev", want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := authoringDevelopmentBypass(test.ctx, test.principalID); got != test.want {
				t.Fatalf("authoringDevelopmentBypass() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestProjectAuthoringGuardRoutesManageToDashboardManageAuthorization(t *testing.T) {
	authorizer := &repositoryDashboardAuthorizerFake{}
	guarded := protectProjectAuthoringResource(
		tusAccess{principal: accessmodule.Principal{ID: "owner"}, ok: true},
		tusRuntime{project: "project_demo"},
		authorizer,
		access.CapabilityResourceManage,
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) },
	)
	router := chi.NewRouter()
	router.Post("/dashboards/{dashboard}/archive", guarded)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/dashboards/dashboard_owned/archive", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
	if authorizer.manageCalls != 1 || authorizer.editCalls != 0 {
		t.Fatalf("authorization calls = edit %d, manage %d", authorizer.editCalls, authorizer.manageCalls)
	}
}

type repositoryDashboardAuthorizerFake struct {
	editCalls   int
	manageCalls int
}

func (f *repositoryDashboardAuthorizerFake) AuthorizeDashboardEdit(context.Context, projectgraph.ResourceID, string, authoring.DashboardID) error {
	f.editCalls++
	return nil
}

func (f *repositoryDashboardAuthorizerFake) AuthorizeDashboardManage(context.Context, projectgraph.ResourceID, string, authoring.DashboardID) error {
	f.manageCalls++
	return nil
}
