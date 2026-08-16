package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	manageddatacontrol "github.com/flidai/leapview/internal/manageddata/control"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
	"github.com/flidai/leapview/internal/runtimehost"
	"github.com/go-chi/chi/v5"
)

type tusTargetResolverFunc func(context.Context, string) (projectgraph.ResourceID, projectgraph.ResourceID, error)

func (f tusTargetResolverFunc) ResolveTusTarget(ctx context.Context, id string) (projectgraph.ResourceID, projectgraph.ResourceID, error) {
	return f(ctx, id)
}

type tusRuntime struct {
	project projectgraph.ResourceID
	lease   runtimehost.Lease
	err     error
}

func (r tusRuntime) ProjectID() projectgraph.ResourceID { return r.project }
func (r tusRuntime) Acquire(context.Context) (runtimehost.Lease, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.lease, nil
}

type tusLease struct {
	identity projectgraph.ServingIdentity
	snapshot accesssnapshot.AuthorizationSnapshot
}

func (l tusLease) Runtime() projectruntime.Runtime                             { return nil }
func (l tusLease) Identity() projectgraph.ServingIdentity                      { return l.identity }
func (l tusLease) Release()                                                    {}
func (l tusLease) AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot { return l.snapshot }

type tusAccess struct {
	principal accessmodule.Principal
	ok        bool
	subjects  []access.SubjectRef
	err       error
}

func (a tusAccess) Authenticate(next http.Handler) http.Handler { return next }
func (a tusAccess) CurrentPrincipal(*http.Request) (accessmodule.Principal, bool) {
	return a.principal, a.ok
}
func (a tusAccess) AuthorizationSubjects(context.Context, string) ([]access.SubjectRef, error) {
	if a.err != nil {
		return nil, a.err
	}
	return a.subjects, nil
}

func tusSnapshot(t *testing.T, principalID string, connectionID projectgraph.ResourceID, allowed bool) accesssnapshot.AuthorizationSnapshot {
	t.Helper()
	identity, err := projectgraph.NewServingIdentity("project_demo", "prod", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: "project_demo", Kind: projectgraph.KindProject, Name: "project_demo"}, {ID: connectionID, Kind: projectgraph.KindConnection, Name: "connection_sales"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var grants []accesssnapshot.Grant
	if allowed {
		subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, principalID)
		if err != nil {
			t.Fatal(err)
		}
		resource, err := access.NewResourceRef(connectionID, projectgraph.KindConnection)
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := access.NewCanonicalGrant(graph, subject, resource, access.CapabilityResourceEdit)
		if err != nil {
			t.Fatal(err)
		}
		grants = []accesssnapshot.Grant{{ID: "grant:connection-edit", Name: "connection_edit", Canonical: canonical}}
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, grants, nil)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestValidTusTransportIDRequiresCanonicalOpaqueToken(t *testing.T) {
	valid := "tus_" + strings.Repeat("a", 64)
	for _, value := range []string{valid, "tus_" + strings.Repeat("A", 64), " tus_" + strings.Repeat("a", 64), "tus_" + strings.Repeat("a", 63)} {
		if got := validTusTransportID(value); got != (value == valid) {
			t.Errorf("validTusTransportID(%q) = %t, want %t", value, got, value == valid)
		}
	}
}

func TestActiveProjectResourceIsExactCanonicalReference(t *testing.T) {
	resources := activeProjectResource(nil, projectgraph.ResourceID("project_demo"))
	if len(resources) != 1 {
		t.Fatalf("resource count = %d, want 1", len(resources))
	}
	if resources[0].ID() != "project_demo" || resources[0].Kind() != projectgraph.KindProject {
		t.Fatalf("resource = %#v, want project_demo/project", resources[0])
	}
	if err := resources[0].Validate(); err != nil {
		t.Fatalf("resource is not canonical: %v", err)
	}
}

func tusRequest(method, uploadID string) *http.Request {
	r := httptest.NewRequest(method, "http://example.test/files/"+uploadID, nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("*", uploadID)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
}

func runTusTransport(t *testing.T, method, uploadID string, accessModule canonicalAccessModule, runtimeHost canonicalRuntimeHost, resolver tusTargetResolverFunc) *httptest.ResponseRecorder {
	t.Helper()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := protectManagedDataTransport(accessModule, runtimeHost, resolver, next)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tusRequest(method, uploadID))
	return response
}

func TestProtectManagedDataTransportEnforcesCanonicalUploadMatrix(t *testing.T) {
	const (
		principalID = "principal_alice"
		projectID   = projectgraph.ResourceID("project_demo")
		connection  = projectgraph.ResourceID("connection_sales")
		uploadID    = "tus_" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	)
	identity, err := projectgraph.NewServingIdentity(projectID, "prod", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	allowedSnapshot := tusSnapshot(t, principalID, connection, true)
	allowedLease := tusLease{identity: identity, snapshot: allowedSnapshot}
	allowedRuntime := tusRuntime{project: projectID, lease: allowedLease}
	deniedSnapshot := tusSnapshot(t, principalID, connection, false)
	deniedRuntime := tusRuntime{project: projectID, lease: tusLease{identity: identity, snapshot: deniedSnapshot}}
	allowedAccess := tusAccess{
		principal: accessmodule.Principal{ID: principalID},
		ok:        true,
		subjects:  []access.SubjectRef{{Kind: access.SubjectKindPrincipal, ID: principalID}},
	}
	resolveAllowed := tusTargetResolverFunc(func(_ context.Context, id string) (projectgraph.ResourceID, projectgraph.ResourceID, error) {
		if id != uploadID {
			return "", "", manageddatacontrol.ErrNotFound
		}
		return projectID, connection, nil
	})

	for _, test := range []struct {
		name    string
		method  string
		id      string
		access  canonicalAccessModule
		runtime canonicalRuntimeHost
		resolve tusTargetResolverFunc
		want    int
	}{
		{name: "options bypasses token and authz", method: http.MethodOptions, id: "", access: tusAccess{}, runtime: tusRuntime{}, resolve: func(ctx context.Context, id string) (projectgraph.ResourceID, projectgraph.ResourceID, error) {
			t.Fatalf("OPTIONS resolver called for %q", id)
			return "", "", nil
		}, want: http.StatusNoContent},
		{name: "head allowed", method: http.MethodHead, id: uploadID, access: allowedAccess, runtime: allowedRuntime, resolve: resolveAllowed, want: http.StatusNoContent},
		{name: "patch allowed", method: http.MethodPatch, id: uploadID, access: allowedAccess, runtime: allowedRuntime, resolve: resolveAllowed, want: http.StatusNoContent},
		{name: "delete allowed", method: http.MethodDelete, id: uploadID, access: allowedAccess, runtime: allowedRuntime, resolve: resolveAllowed, want: http.StatusNoContent},
		{name: "get is delegated", method: http.MethodGet, id: "malformed", access: allowedAccess, runtime: allowedRuntime, resolve: func(context.Context, string) (projectgraph.ResourceID, projectgraph.ResourceID, error) {
			t.Fatalf("GET resolver called")
			return "", "", nil
		}, want: http.StatusNoContent},
		{name: "malformed opaque token", method: http.MethodPatch, id: "tus_" + strings.Repeat("A", 64), access: allowedAccess, runtime: allowedRuntime, resolve: resolveAllowed, want: http.StatusNotFound},
		{name: "unknown opaque token", method: http.MethodPatch, id: "tus_" + strings.Repeat("b", 64), access: allowedAccess, runtime: allowedRuntime, resolve: func(context.Context, string) (projectgraph.ResourceID, projectgraph.ResourceID, error) {
			return "", "", manageddatacontrol.ErrNotFound
		}, want: http.StatusNotFound},
		{name: "resolver backend failure", method: http.MethodPatch, id: uploadID, access: allowedAccess, runtime: allowedRuntime, resolve: func(context.Context, string) (projectgraph.ResourceID, projectgraph.ResourceID, error) {
			return "", "", errors.New("resolver unavailable")
		}, want: http.StatusServiceUnavailable},
		{name: "project mismatch is not disclosed", method: http.MethodPatch, id: uploadID, access: allowedAccess, runtime: allowedRuntime, resolve: func(context.Context, string) (projectgraph.ResourceID, projectgraph.ResourceID, error) {
			return projectgraph.ResourceID("project_other"), connection, nil
		}, want: http.StatusNotFound},
		{name: "invalid connection selector", method: http.MethodPatch, id: uploadID, access: allowedAccess, runtime: allowedRuntime, resolve: func(context.Context, string) (projectgraph.ResourceID, projectgraph.ResourceID, error) {
			return projectID, projectgraph.ResourceID("not canonical"), nil
		}, want: http.StatusNotFound},
		{name: "missing principal", method: http.MethodPatch, id: uploadID, access: tusAccess{}, runtime: allowedRuntime, resolve: resolveAllowed, want: http.StatusUnauthorized},
		{name: "authorization deny is not disclosed", method: http.MethodPatch, id: uploadID, access: tusAccess{principal: accessmodule.Principal{ID: principalID}, ok: true, subjects: []access.SubjectRef{{Kind: access.SubjectKindPrincipal, ID: principalID}}}, runtime: deniedRuntime, resolve: tusTargetResolverFunc(func(context.Context, string) (projectgraph.ResourceID, projectgraph.ResourceID, error) {
			return projectID, connection, nil
		}), want: http.StatusNotFound},
		{name: "subject lookup failure", method: http.MethodPatch, id: uploadID, access: tusAccess{principal: accessmodule.Principal{ID: principalID}, ok: true, err: errors.New("identity unavailable")}, runtime: allowedRuntime, resolve: resolveAllowed, want: http.StatusServiceUnavailable},
		{name: "runtime acquisition failure", method: http.MethodPatch, id: uploadID, access: allowedAccess, runtime: tusRuntime{project: projectID, err: errors.New("generation unavailable")}, resolve: resolveAllowed, want: http.StatusServiceUnavailable},
		{name: "nil lease failure", method: http.MethodPatch, id: uploadID, access: allowedAccess, runtime: tusRuntime{project: projectID}, resolve: resolveAllowed, want: http.StatusServiceUnavailable},
		{name: "snapshot generation mismatch", method: http.MethodPatch, id: uploadID, access: allowedAccess, runtime: tusRuntime{project: projectID, lease: tusLease{identity: func() projectgraph.ServingIdentity {
			mismatched, err := projectgraph.NewServingIdentity(projectID, "prod", "generation_2")
			if err != nil {
				t.Fatal(err)
			}
			return mismatched
		}(), snapshot: allowedSnapshot}}, resolve: resolveAllowed, want: http.StatusServiceUnavailable},
		{name: "snapshot failure", method: http.MethodPatch, id: uploadID, access: allowedAccess, runtime: tusRuntime{project: projectID, lease: tusLease{identity: identity}}, resolve: resolveAllowed, want: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := runTusTransport(t, test.method, test.id, test.access, test.runtime, test.resolve)
			if got.Code != test.want {
				t.Fatalf("status = %d, want %d; body %q", got.Code, test.want, got.Body.String())
			}
		})
	}
}

func TestProtectManagedDataTransportValidatesBeforeDevBypass(t *testing.T) {
	const uploadID = "tus_" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	projectID := projectgraph.ResourceID("project_demo")
	connectionID := projectgraph.ResourceID("connection_sales")
	identity, err := projectgraph.NewServingIdentity(projectID, "prod", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	accessModule := tusAccess{principal: accessmodule.Principal{ID: "dev", DevBypass: true}, ok: true}
	runtimeHost := tusRuntime{project: projectID, lease: tusLease{identity: identity, snapshot: tusSnapshot(t, "dev", connectionID, false)}}
	var resolved []string
	resolver := tusTargetResolverFunc(func(_ context.Context, id string) (projectgraph.ResourceID, projectgraph.ResourceID, error) {
		resolved = append(resolved, id)
		if id != uploadID {
			return "", "", manageddatacontrol.ErrNotFound
		}
		return projectID, connectionID, nil
	})
	if got := runTusTransport(t, http.MethodPatch, uploadID, accessModule, runtimeHost, resolver); got.Code != http.StatusNoContent {
		t.Fatalf("dev bypass valid target status = %d, want %d", got.Code, http.StatusNoContent)
	}
	if got := runTusTransport(t, http.MethodPatch, "tus_"+strings.Repeat("A", 64), accessModule, runtimeHost, resolver); got.Code != http.StatusNotFound {
		t.Fatalf("dev bypass malformed target status = %d, want %d", got.Code, http.StatusNotFound)
	}
	if len(resolved) != 1 || resolved[0] != uploadID {
		t.Fatalf("resolver calls = %#v, want only validated target %q", resolved, uploadID)
	}
}
