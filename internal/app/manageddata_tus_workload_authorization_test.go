package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	manageddatacontrol "github.com/flidai/leapview/internal/manageddata/control"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

const (
	tusWorkloadTargetID     = "instance_prod"
	tusWorkloadPrincipalID  = "workload_demo"
	tusWorkloadProjectID    = projectgraph.ResourceID("project_demo")
	tusWorkloadConnectionID = projectgraph.ResourceID("connection_sales")
	tusWorkloadUploadID     = "tus_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

type tusCredentialAccess struct {
	tusAccess
	credential access.APICredential
}

type tusAuthorizingTargetResolver struct {
	tusTargetResolverFunc
	authorize connectionAuthorization
}

func (r tusAuthorizingTargetResolver) AuthorizeConnection(ctx context.Context, principalID, projectID, connectionID string, action access.Action) (bool, error) {
	if r.authorize == nil {
		return false, errors.New("connection authorizer is not configured")
	}
	return r.authorize(ctx, principalID, projectID, connectionID, action)
}

func (a tusCredentialAccess) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := accessmodule.WithAPICredential(r.Context(), a.credential)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func TestManagedDataTusAuthorizesWorkloadScopeAgainstActiveConnection(t *testing.T) {
	for _, mode := range tusWorkloadBootstrapModes() {
		t.Run(mode.name, func(t *testing.T) {
			runtime, accessValue, resolve := tusWorkloadFixture(t, true)
			resolver := tusWorkloadResolver(t, runtime, accessValue, resolve)
			handler := protectManagedDataTransportWithBootstrap(accessValue, runtime, resolver, mode.bootstrap, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))

			response := httptest.NewRecorder()
			handler.ServeHTTP(response, tusRequest(http.MethodPatch, tusWorkloadUploadID))
			if response.Code != http.StatusNoContent {
				t.Fatalf("workload TUS PATCH status = %d, want %d; body=%q", response.Code, http.StatusNoContent, response.Body.String())
			}
		})
	}
}

func TestManagedDataTusWorkloadConnectionAuthorizationFailsClosed(t *testing.T) {
	tests := []struct {
		name          string
		snapshotGrant bool
		scope         func(t *testing.T, pairs []access.PermissionPair) *access.AuthoringScope
		configured    bool
		wantStatus    int
	}{
		{name: "no active snapshot grant", snapshotGrant: false, configured: true, wantStatus: http.StatusNotFound},
		{name: "wrong connection scope", snapshotGrant: true, configured: true, wantStatus: http.StatusNotFound, scope: func(t *testing.T, _ []access.PermissionPair) *access.AuthoringScope {
			resource, err := access.NewResourceRef("connection_other", projectgraph.KindConnection)
			if err != nil {
				t.Fatal(err)
			}
			pair, err := access.NewExactPermissionPair(access.ActionConnectionUpload, tusWorkloadProjectID, resource)
			if err != nil {
				t.Fatal(err)
			}
			scope, err := access.NewAuthoringScope(tusWorkloadTargetID, tusWorkloadProjectID, []access.PermissionPair{pair})
			if err != nil {
				t.Fatal(err)
			}
			return &scope
		}},
		{name: "wrong instance scope", snapshotGrant: true, configured: true, wantStatus: http.StatusNotFound, scope: func(t *testing.T, pairs []access.PermissionPair) *access.AuthoringScope {
			scope, err := access.NewAuthoringScope("instance_other", tusWorkloadProjectID, pairs)
			if err != nil {
				t.Fatal(err)
			}
			return &scope
		}},
		{name: "missing connection authorizer", snapshotGrant: true, configured: false, wantStatus: http.StatusServiceUnavailable},
	}
	for _, test := range tests {
		for _, mode := range tusWorkloadBootstrapModes() {
			t.Run(test.name+"/"+mode.name, func(t *testing.T) {
				runtime, accessValue, resolve := tusWorkloadFixture(t, test.snapshotGrant)
				pair := tusWorkloadUploadPair(t)
				if test.scope != nil {
					accessValue.credential.Authoring.Scope = *test.scope(t, []access.PermissionPair{pair})
				}
				resolver := tusWorkloadResolver(t, runtime, accessValue, resolve)
				if !test.configured {
					resolver.authorize = nil
				}
				handler := protectManagedDataTransportWithBootstrap(accessValue, runtime, resolver, mode.bootstrap, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
					t.Fatal("unauthorized workload TUS request reached handler")
				}))
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, tusRequest(http.MethodPatch, tusWorkloadUploadID))
				if response.Code != test.wantStatus {
					t.Fatalf("workload TUS PATCH status = %d, want %d; body=%q", response.Code, test.wantStatus, response.Body.String())
				}
			})
		}
	}
}

type tusWorkloadBootstrapMode struct {
	name      string
	bootstrap accessmodule.APIGenBootstrapAuthorizer
}

func tusWorkloadBootstrapModes() []tusWorkloadBootstrapMode {
	return []tusWorkloadBootstrapMode{
		{name: "no bootstrap callback"},
		{name: "bootstrap declines active request", bootstrap: func(context.Context, *http.Request, string, projectgraph.ResourceID, access.Capability) (accessmodule.APIGenBootstrapDecision, error) {
			return accessmodule.APIGenBootstrapDecision{Handled: false}, nil
		}},
	}
}

func tusWorkloadResolver(t *testing.T, runtime tusRuntime, accessValue tusCredentialAccess, resolve tusTargetResolverFunc) tusAuthorizingTargetResolver {
	t.Helper()
	authorize := tusWorkloadConnectionAuthorizer(t, runtime, accessValue)
	return tusAuthorizingTargetResolver{tusTargetResolverFunc: resolve, authorize: func(ctx context.Context, principalID, projectID, connectionID string, action access.Action) (bool, error) {
		if principalID != tusWorkloadPrincipalID || projectID != string(tusWorkloadProjectID) || connectionID != string(tusWorkloadConnectionID) || action != access.ActionConnectionUpload {
			return false, errors.New("TUS authorization did not use the session-bound connection upload action")
		}
		return authorize(ctx, principalID, projectID, connectionID, action)
	}}
}

func tusWorkloadUploadPair(t *testing.T) access.PermissionPair {
	t.Helper()
	resource, err := access.NewResourceRef(tusWorkloadConnectionID, projectgraph.KindConnection)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := access.NewExactPermissionPair(access.ActionConnectionUpload, tusWorkloadProjectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

func tusWorkloadFixture(t *testing.T, snapshotGrant bool) (tusRuntime, tusCredentialAccess, tusTargetResolverFunc) {
	t.Helper()
	identity, err := projectgraph.NewServingIdentity(tusWorkloadProjectID, "prod", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := tusSnapshot(t, tusWorkloadPrincipalID, tusWorkloadConnectionID, snapshotGrant)
	runtime := tusRuntime{project: tusWorkloadProjectID, lease: tusLease{identity: identity, snapshot: snapshot}}
	resource, err := access.NewResourceRef(tusWorkloadConnectionID, projectgraph.KindConnection)
	if err != nil {
		t.Fatal(err)
	}
	upload, err := access.NewExactPermissionPair(access.ActionConnectionUpload, identity.ProjectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := access.NewAuthoringScope(tusWorkloadTargetID, identity.ProjectID, []access.PermissionPair{upload})
	if err != nil {
		t.Fatal(err)
	}
	principal := access.Principal{ID: tusWorkloadPrincipalID, Kind: access.PrincipalKindServicePrincipal}
	credential := access.APICredential{
		Principal: principal,
		Token:     access.APIToken{ID: "credential_demo", PrincipalID: principal.ID},
		Authoring: &access.AuthoringSession{
			ID: "session_demo", Kind: access.AuthoringSessionWorkload, ClientID: principal.ID,
			PrincipalID: principal.ID, Scope: scope,
		},
	}
	accessValue := tusCredentialAccess{
		tusAccess: tusAccess{
			principal: accessmodule.Principal{ID: principal.ID, Kind: principal.Kind}, ok: true,
			subjects: []access.SubjectRef{{Kind: access.SubjectKindPrincipal, ID: principal.ID}},
		},
		credential: credential,
	}
	resolver := tusTargetResolverFunc(func(_ context.Context, id string) (projectgraph.ResourceID, projectgraph.ResourceID, error) {
		if id != tusWorkloadUploadID {
			return "", "", manageddatacontrol.ErrNotFound
		}
		return tusWorkloadProjectID, tusWorkloadConnectionID, nil
	})
	return runtime, accessValue, resolver
}

func tusWorkloadConnectionAuthorizer(t *testing.T, runtime tusRuntime, accessValue tusCredentialAccess) connectionAuthorization {
	t.Helper()
	return accessmodule.ConnectionAuthorizerFromSnapshot(tusWorkloadTargetID,
		func(ctx context.Context) (accesssnapshot.AuthorizationSnapshot, error) {
			lease, err := runtime.Acquire(ctx)
			if err != nil {
				return accesssnapshot.AuthorizationSnapshot{}, err
			}
			return lease.(tusLease).AuthorizationSnapshot(), nil
		}, accessValue.AuthorizationSubjects)
}
