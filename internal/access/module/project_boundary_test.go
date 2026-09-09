package module

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
)

func TestAPIGenProjectBoundaryPrincipalAndPlatformLocators(t *testing.T) {
	for _, scope := range []string{"principal", "platform"} {
		t.Run(scope, func(t *testing.T) {
			module := browserGuardModule(browserGuardRepository{admin: true}, Principal{ID: "admin"}, true)
			contract := APIGenOperationContract{
				OperationID: "boundMetadata",
				Method:      http.MethodGet,
				Path:        "/api/v1/projects/{project}/metadata",
				Protected:   true,
				AuthzMode:   "authenticated",
				Extensions:  map[string]any{apiGenObjectScopeExtension: scope},
			}
			for _, bound := range []projectgraph.ResourceID{"project_demo", "", "invalid project"} {
				authorizer, err := module.APIGenAuthorizer(
					apigenRuntimeFake{project: bound},
					map[string]APIGenOperationContract{"boundMetadata": contract},
					APIGenResourceResolvers{},
				)
				if err != nil {
					t.Fatal(err)
				}
				calls := 0
				handler, ok := authorizer.Protect("boundMetadata", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					calls++
					w.WriteHeader(http.StatusNoContent)
				}))
				if !ok {
					t.Fatal("authorizer did not construct")
				}
				for _, locator := range []string{"project_demo", "foreign", ""} {
					request := apigenRequest(http.MethodGet, "/api/v1/projects/"+locator+"/metadata", map[string]string{"project": locator})
					response := httptest.NewRecorder()
					before := calls
					handler.ServeHTTP(response, request)
					allowed := bound != "" && locator == bound.String()
					if allowed {
						if response.Code != http.StatusNoContent || calls != before+1 || !authorizer.AuthorizeReplay(request) {
							t.Fatal("bound metadata/replay was rejected")
						}
					} else if response.Code != http.StatusNotFound || calls != before || authorizer.AuthorizeReplay(request) {
						t.Fatalf("foreign or missing bound identity dispatched: %q / %q", bound, locator)
					}
				}
			}
		})
	}
}

func TestAPIGenProjectBoundaryUsesConfiguredResolverWithoutRuntime(t *testing.T) {
	bound := projectgraph.ResourceID("project_demo")
	contract := APIGenOperationContract{
		OperationID: "boundMetadata",
		Method:      http.MethodGet,
		Path:        "/api/v1/projects/{project}/metadata",
		Protected:   true,
		AuthzMode:   "authenticated",
		Extensions:  map[string]any{apiGenObjectScopeExtension: "principal"},
	}
	for _, test := range []struct {
		name      string
		configure func(*Module)
		allowed   bool
	}{
		{
			name: "configured resolver",
			configure: func(module *Module) {
				module.SetCurrentProjectID(func(context.Context) (projectgraph.ResourceID, error) {
					return bound, nil
				})
			},
			allowed: true,
		},
		{
			name: "resolver error",
			configure: func(module *Module) {
				module.SetCurrentProjectID(func(context.Context) (projectgraph.ResourceID, error) {
					return "", errors.New("project resolver unavailable")
				})
			},
		},
		{
			name: "invalid resolver identity",
			configure: func(module *Module) {
				module.SetCurrentProjectID(func(context.Context) (projectgraph.ResourceID, error) {
					return "!", nil
				})
			},
		},
		{name: "missing resolver"},
	} {
		t.Run(test.name, func(t *testing.T) {
			module := browserGuardModule(nil, Principal{ID: "principal"}, true)
			if test.configure != nil {
				test.configure(module)
			}
			authorizer, err := module.APIGenAuthorizer(nil, map[string]APIGenOperationContract{"boundMetadata": contract}, APIGenResourceResolvers{})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			handler, ok := authorizer.Protect("boundMetadata", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(http.StatusNoContent)
			}))
			if !ok {
				t.Fatal("authorizer did not construct")
			}
			for _, locator := range []string{"project_demo", "foreign"} {
				request := apigenRequest(http.MethodGet, "/api/v1/projects/"+locator+"/metadata", map[string]string{"project": locator})
				response := httptest.NewRecorder()
				before := calls
				handler.ServeHTTP(response, request)
				allowed := test.allowed && locator == bound.String()
				if allowed {
					if response.Code != http.StatusNoContent || calls != before+1 || !authorizer.AuthorizeReplay(request) {
						t.Fatal("configured project resolver rejected bound metadata/replay")
					}
				} else if response.Code != http.StatusNotFound || calls != before || authorizer.AuthorizeReplay(request) {
					t.Fatalf("unconfigured or foreign project identity dispatched: %q / %q", test.name, locator)
				}
			}
		})
	}
}

func TestAPIGenProjectBoundaryConfiguredResolverCrossChecksRuntimeHost(t *testing.T) {
	bound := projectgraph.ResourceID("project_demo")
	contract := APIGenOperationContract{
		OperationID: "boundMetadata",
		Method:      http.MethodGet,
		Path:        "/api/v1/projects/{project}/metadata",
		Protected:   true,
		AuthzMode:   "authenticated",
		Extensions:  map[string]any{apiGenObjectScopeExtension: "principal"},
	}
	var typedNilHost *runtimehostmodule.Module
	for _, test := range []struct {
		name       string
		runtime    apigenRuntimeHost
		configure  func(*Module)
		wantStatus int
	}{
		{
			name:       "matching runtime host",
			runtime:    apigenRuntimeFake{project: bound},
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "differing runtime host",
			runtime:    apigenRuntimeFake{project: "project_other"},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "invalid nonempty runtime host",
			runtime:    apigenRuntimeFake{project: "!"},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "empty runtime host",
			runtime:    apigenRuntimeFake{project: ""},
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "typed nil runtime host",
			runtime:    typedNilHost,
			wantStatus: http.StatusNoContent,
		},
		{
			name:    "resolver error takes precedence over valid runtime host",
			runtime: apigenRuntimeFake{project: bound},
			configure: func(module *Module) {
				module.SetCurrentProjectID(func(context.Context) (projectgraph.ResourceID, error) {
					return "", errors.New("project resolver unavailable")
				})
			},
			wantStatus: http.StatusNotFound,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			module := browserGuardModule(nil, Principal{ID: "principal"}, true)
			if test.configure != nil {
				test.configure(module)
			} else {
				module.SetCurrentProjectID(func(context.Context) (projectgraph.ResourceID, error) {
					return bound, nil
				})
			}
			authorizer, err := module.APIGenAuthorizer(test.runtime, map[string]APIGenOperationContract{"boundMetadata": contract}, APIGenResourceResolvers{})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			handler, ok := authorizer.Protect("boundMetadata", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(http.StatusNoContent)
			}))
			if !ok {
				t.Fatal("authorizer did not construct")
			}
			request := apigenRequest(http.MethodGet, "/api/v1/projects/project_demo/metadata", map[string]string{"project": bound.String()})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("bound metadata status = %d, want %d", response.Code, test.wantStatus)
			}
			if test.wantStatus == http.StatusNoContent {
				if calls != 1 || !authorizer.AuthorizeReplay(request) {
					t.Fatalf("matching configured/runtime boundary did not dispatch and replay: calls=%d", calls)
				}
			} else if calls != 0 || authorizer.AuthorizeReplay(request) {
				t.Fatalf("invalid configured/runtime boundary dispatched or replayed: calls=%d", calls)
			}
		})
	}
}

func TestAPIGenProjectBoundaryRejectsNilRuntime(t *testing.T) {
	module := browserGuardModule(browserGuardRepository{admin: true}, Principal{ID: "admin"}, true)
	contract := APIGenOperationContract{OperationID: "boundMetadata", Method: http.MethodGet, Path: "/api/v1/projects/{project}/metadata", Protected: true, AuthzMode: "authenticated", Extensions: map[string]any{apiGenObjectScopeExtension: "principal"}}
	authorizer, err := module.APIGenAuthorizer(nil, map[string]APIGenOperationContract{"boundMetadata": contract}, APIGenResourceResolvers{})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	handler, ok := authorizer.Protect("boundMetadata", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	if !ok {
		t.Fatal("authorizer did not construct")
	}
	request := apigenRequest(http.MethodGet, "/api/v1/projects/project_demo/metadata", map[string]string{"project": "project_demo"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || calls != 0 || authorizer.AuthorizeReplay(request) {
		t.Fatalf("nil runtime dispatched or replayed: status=%d calls=%d", response.Code, calls)
	}
}
