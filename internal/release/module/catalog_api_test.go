package module

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	"github.com/flidai/leapview/internal/release"
)

type catalogRepository struct {
}

func (catalogRepository) GetProject(context.Context, string) (release.ProjectRecord, error) {
	return release.ProjectRecord{}, nil
}
func (catalogRepository) ListConnections(context.Context, string, string) ([]release.ConnectionRecord, error) {
	return nil, nil
}
func (catalogRepository) GetConnection(context.Context, string, string, string) (release.ConnectionRecord, error) {
	return release.ConnectionRecord{}, nil
}

type countingCatalogRepository struct {
	catalogRepository
	connections          []release.ConnectionRecord
	connection           release.ConnectionRecord
	listConnectionsCalls int
	getConnectionCalls   int
}

func (r *countingCatalogRepository) ListConnections(context.Context, string, string) ([]release.ConnectionRecord, error) {
	r.listConnectionsCalls++
	return r.connections, nil
}

func (r *countingCatalogRepository) GetConnection(context.Context, string, string, string) (release.ConnectionRecord, error) {
	r.getConnectionCalls++
	return r.connection, nil
}

func testPrincipal(_ *http.Request) (Principal, bool) { return Principal{ID: "principal-1"}, true }

func TestListManagedConnectionsAuthenticatesBeforeCatalogAndFilters(t *testing.T) {
	repo := &countingCatalogRepository{connections: []release.ConnectionRecord{
		{ID: "allowed", Title: "Allowed"}, {ID: "denied", Title: "Denied"},
	}}
	module := &Module{
		catalog: repo, environment: "dev",
		api: APIConfig{
			CurrentPrincipal: testPrincipal,
			AuthorizeConnection: func(_ context.Context, _ string, _ string, connectionID string, _ access.Capability) (bool, error) {
				return connectionID == "allowed", nil
			},
		},
	}
	recorder := httptest.NewRecorder()
	module.ListManagedConnections(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/projects/p/connections", nil), "project-1", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"id":"allowed"`) || strings.Contains(recorder.Body.String(), `"id":"denied"`) {
		t.Fatalf("body = %s", recorder.Body.String())
	}
	if repo.listConnectionsCalls != 1 {
		t.Fatalf("list calls = %d, want 1", repo.listConnectionsCalls)
	}
}

func TestManagedConnectionAPIsReceiveAuthenticatedBearerPrincipal(t *testing.T) {
	accessSurface, err := accessmodule.Build(t.Context(), accessmodule.Config{
		ExistingAuth: accessmodule.NewAuth(nil, accessmodule.AuthConfig{DevBypass: true, DevAPIToken: "dev"}),
	})
	if err != nil {
		t.Fatalf("build auth surface: %v", err)
	}
	repo := &countingCatalogRepository{
		connections: []release.ConnectionRecord{{ID: "connection:warehouse", Title: "Warehouse"}},
		connection:  release.ConnectionRecord{ID: "connection:warehouse", Title: "Warehouse"},
	}
	var authorizedPrincipalIDs []string
	module := &Module{
		catalog: repo, environment: "prod",
		api: APIConfig{
			CurrentPrincipal: func(r *http.Request) (Principal, bool) {
				principal, ok := accessSurface.CurrentPrincipal(r)
				return Principal{ID: principal.ID}, ok
			},
			AuthorizeConnection: func(_ context.Context, principalID, projectID, connectionID string, capability access.Capability) (bool, error) {
				authorizedPrincipalIDs = append(authorizedPrincipalIDs, principalID)
				return principalID == accessmodule.DevelopmentPrincipalID && projectID == "project:leapview-showcase" && connectionID == "connection:warehouse" && capability == access.CapabilityResourceRead, nil
			},
		},
	}
	serve := func(path string, handler func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer dev")
		response := httptest.NewRecorder()
		accessSurface.Authenticate(http.HandlerFunc(handler)).ServeHTTP(response, request)
		return response
	}

	list := serve("/api/v1/projects/project:leapview-showcase/connections", func(w http.ResponseWriter, r *http.Request) {
		module.ListManagedConnections(w, r, "project:leapview-showcase", nil, nil)
	})
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"id":"connection:warehouse"`) {
		t.Fatalf("list connections = %d body=%s", list.Code, list.Body.String())
	}
	detail := serve("/api/v1/projects/project:leapview-showcase/connections/connection:warehouse", func(w http.ResponseWriter, r *http.Request) {
		module.GetManagedConnection(w, r, "project:leapview-showcase", "connection:warehouse")
	})
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"id":"connection:warehouse"`) {
		t.Fatalf("get connection = %d body=%s", detail.Code, detail.Body.String())
	}
	if len(authorizedPrincipalIDs) != 2 || authorizedPrincipalIDs[0] != accessmodule.DevelopmentPrincipalID || authorizedPrincipalIDs[1] != accessmodule.DevelopmentPrincipalID {
		t.Fatalf("authorized principal IDs = %#v, want developer principal for list and detail", authorizedPrincipalIDs)
	}
}

func TestListManagedConnectionsDoesNotQueryWithoutAuthenticationOrAuthorizer(t *testing.T) {
	for name, api := range map[string]APIConfig{
		"unauthenticated":    {CurrentPrincipal: func(*http.Request) (Principal, bool) { return Principal{}, false }, AuthorizeConnection: func(context.Context, string, string, string, access.Capability) (bool, error) { return true, nil }},
		"missing authorizer": {CurrentPrincipal: testPrincipal},
	} {
		t.Run(name, func(t *testing.T) {
			repo := &countingCatalogRepository{connections: []release.ConnectionRecord{{ID: "hidden"}}}
			module := &Module{catalog: repo, environment: "dev", api: api}
			recorder := httptest.NewRecorder()
			module.ListManagedConnections(recorder, httptest.NewRequest(http.MethodGet, "/connections", nil), "project-1", nil, nil)
			wantStatus := http.StatusInternalServerError
			if name == "unauthenticated" {
				wantStatus = http.StatusUnauthorized
			}
			if recorder.Code != wantStatus {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, wantStatus, recorder.Body.String())
			}
			if repo.listConnectionsCalls != 0 {
				t.Fatalf("list calls = %d, want 0", repo.listConnectionsCalls)
			}
		})
	}
}

func TestGetManagedConnectionDoesNotQueryWhenForbidden(t *testing.T) {
	repo := &countingCatalogRepository{}
	module := &Module{
		catalog: repo,
		api: APIConfig{
			CurrentPrincipal:    testPrincipal,
			AuthorizeConnection: func(context.Context, string, string, string, access.Capability) (bool, error) { return false, nil },
		},
	}
	recorder := httptest.NewRecorder()
	module.GetManagedConnection(recorder, httptest.NewRequest(http.MethodGet, "/connection", nil), "project-1", "hidden")
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repo.getConnectionCalls != 0 {
		t.Fatalf("get calls = %d, want 0", repo.getConnectionCalls)
	}
}
