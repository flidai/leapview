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
	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/deployment/sealedcontrol"
	manageddatacontrol "github.com/flidai/leapview/internal/manageddata/control"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
	"github.com/flidai/leapview/internal/runtimehost"
	"github.com/go-chi/chi/v5"
)

type tusTargetResolverFunc func(context.Context, string) (projectgraph.ResourceID, projectgraph.ResourceID, error)

type canonicalRuntimeActivatorFake struct {
	prepareID     string
	prepareErr    error
	activateErr   error
	activateCalls int
	commitCalls   int
}

type canonicalTargetReaderFake struct {
	target deployment.DeliveryTarget
	err    error
}

func (f canonicalTargetReaderFake) DeliveryTargetRevision(context.Context, string) (deployment.DeliveryTarget, error) {
	return f.target, f.err
}

func (f *canonicalRuntimeActivatorFake) PrepareServingState(_ context.Context, id string) (*runtimehost.Prepared, error) {
	f.prepareID = id
	if f.prepareErr != nil {
		return nil, f.prepareErr
	}
	return nil, nil
}

func (f *canonicalRuntimeActivatorFake) ActivatePreparedContext(_ context.Context, _ *runtimehost.Prepared, commit func() error) error {
	f.activateCalls++
	if f.activateErr != nil {
		return f.activateErr
	}
	f.commitCalls++
	return commit()
}

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

type bootstrapTusAccess struct {
	tusAccess
	allowed bool
	err     error
}

func (a bootstrapTusAccess) AuthorizeBootstrapRequest(context.Context, *http.Request, access.Capability) (bool, error) {
	return a.allowed, a.err
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

func TestDeliveryAuthorizationRequiresEveryAffectedResource(t *testing.T) {
	identity, err := projectgraph.NewServingIdentity("project_demo", "prod", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	dashboardA, err := projectgraph.NewResourceID("dashboard_a")
	if err != nil {
		t.Fatal(err)
	}
	dashboardB, err := projectgraph.NewResourceID("dashboard_b")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project_demo", Kind: projectgraph.KindProject, Name: "project_demo"},
		{ID: dashboardA, Kind: projectgraph.KindDashboard, Name: "A"},
		{ID: dashboardB, Kind: projectgraph.KindDashboard, Name: "B"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	subject := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal_alice"}
	resourceA, err := access.NewResourceRef(dashboardA, projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := access.NewCanonicalGrant(graph, subject, resourceA, access.CapabilityResourcePublish)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, []accesssnapshot.Grant{{ID: "grant_a", Canonical: grant}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resourceB, err := access.NewResourceRef(dashboardB, projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	subjects := []access.SubjectRef{subject}
	if allowed, err := deliverySnapshotAllows(snapshot, subjects, []access.ResourceRef{resourceA}, access.CapabilityResourcePublish); err != nil || !allowed {
		t.Fatalf("grant on A did not authorize A: allowed=%t err=%v", allowed, err)
	}
	if allowed, err := deliverySnapshotAllows(snapshot, subjects, []access.ResourceRef{resourceB}, access.CapabilityResourcePublish); err != nil || allowed {
		t.Fatalf("grant on A authorized B: allowed=%t err=%v", allowed, err)
	}
	unknown, err := access.NewResourceRef(projectgraph.ResourceID("dashboard_unknown"), projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	if allowed, err := deliverySnapshotAllows(snapshot, subjects, []access.ResourceRef{unknown}, access.CapabilityResourcePublish); err == nil || allowed {
		t.Fatalf("unknown resource did not fail closed: allowed=%t err=%v", allowed, err)
	}

	roleSnapshot, err := accesssnapshot.NewAuthorizationSnapshotWithRoleBindings(identity, graph, []accesssnapshot.RoleBinding{{ID: "role_deployer", Subject: subject, Role: access.ProjectRoleDeployer, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleDeployer)}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !deliveryRoleAllows(roleSnapshot, subjects, access.CapabilityResourcePublish) {
		t.Fatal("explicit deployer role did not authorize publish")
	}
	viewerSnapshot, err := accesssnapshot.NewAuthorizationSnapshotWithRoleBindings(identity, graph, []accesssnapshot.RoleBinding{{ID: "role_viewer", Subject: subject, Role: access.ProjectRoleViewer, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleViewer)}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if deliveryRoleAllows(viewerSnapshot, subjects, access.CapabilityResourcePublish) {
		t.Fatal("viewer role unexpectedly authorized publish")
	}
}

type sealedAuthorizationDeliveryStub struct {
	candidate deployment.DeliveryCandidate
	plan      deployment.DeliveryPlan
}

func TestSealedPublicationBootstrapDecisionBindsExactMarkerAndActiveFence(t *testing.T) {
	binding := sealedcontrol.SealBinding{ProjectID: "project_demo", ActorID: "principal_alice", Operation: "publish"}
	exact := accessmodule.BootstrapAuthorization{ProjectID: projectgraph.ResourceID("project_demo"), PrincipalID: "principal_alice", Capability: access.CapabilityResourcePublish}
	activeErr := errors.New("active generation lookup failed")
	for _, test := range []struct {
		name             string
		marker           accessmodule.BootstrapAuthorization
		marked           bool
		active           bool
		activeErr        error
		bindingBootstrap bool
		wantHandle       bool
		wantErr          bool
	}{
		{name: "exact marker on fresh target", marker: exact, marked: true, wantHandle: true},
		{name: "missing marker", marker: exact, wantHandle: true, wantErr: true},
		{name: "principal mismatch", marker: accessmodule.BootstrapAuthorization{ProjectID: exact.ProjectID, PrincipalID: "principal_other", Capability: exact.Capability}, marked: true, wantHandle: true, wantErr: true},
		{name: "project mismatch", marker: accessmodule.BootstrapAuthorization{ProjectID: "project_other", PrincipalID: exact.PrincipalID, Capability: exact.Capability}, marked: true, wantHandle: true, wantErr: true},
		{name: "capability mismatch", marker: accessmodule.BootstrapAuthorization{ProjectID: exact.ProjectID, PrincipalID: exact.PrincipalID, Capability: access.CapabilityResourceUse}, marked: true, wantHandle: true, wantErr: true},
		{name: "durable worker bootstrap authorization", marker: accessmodule.BootstrapAuthorization{}, wantHandle: true, bindingBootstrap: true},
		{name: "durable worker rejects mismatched request marker", marker: accessmodule.BootstrapAuthorization{ProjectID: exact.ProjectID, PrincipalID: "principal_other", Capability: exact.Capability}, marked: true, wantHandle: true, wantErr: true, bindingBootstrap: true},
		{name: "active race falls through live snapshot", marker: accessmodule.BootstrapAuthorization{}, active: true, wantHandle: false},
		{name: "active check error", marker: exact, marked: true, activeErr: activeErr, wantHandle: false, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			decisionBinding := binding
			decisionBinding.Bootstrap = test.bindingBootstrap
			handled, err := sealedPublicationBootstrapDecision(t.Context(), decisionBinding, test.marker, test.marked, func(context.Context) (bool, error) {
				return test.active, test.activeErr
			})
			if handled != test.wantHandle || (err != nil) != test.wantErr {
				t.Fatalf("sealedPublicationBootstrapDecision() = handled=%t err=%v, want handled=%t err=%t", handled, err, test.wantHandle, test.wantErr)
			}
		})
	}
}

func TestActivateCanonicalServingStatePreparesBeforeCommit(t *testing.T) {
	var committed int
	fake := &canonicalRuntimeActivatorFake{}
	if err := activateCanonicalServingState(t.Context(), fake, "state_pending", func() error { committed++; return nil }); err != nil {
		t.Fatal(err)
	}
	if fake.prepareID != "state_pending" || fake.activateCalls != 1 || fake.commitCalls != 1 || committed != 1 {
		t.Fatalf("activation ordering = prepare %q/activate %d/commit %d/%d", fake.prepareID, fake.activateCalls, fake.commitCalls, committed)
	}

	prepareFailure := &canonicalRuntimeActivatorFake{prepareErr: errors.New("prepare failed")}
	committed = 0
	if err := activateCanonicalServingState(t.Context(), prepareFailure, "state_pending", func() error { committed++; return nil }); err == nil {
		t.Fatal("prepare failure unexpectedly succeeded")
	}
	if committed != 0 || prepareFailure.activateCalls != 0 {
		t.Fatalf("prepare failure committed=%d activate=%d", committed, prepareFailure.activateCalls)
	}

	activateFailure := &canonicalRuntimeActivatorFake{activateErr: errors.New("activation failed")}
	committed = 0
	if err := activateCanonicalServingState(t.Context(), activateFailure, "state_pending", func() error { committed++; return nil }); err == nil {
		t.Fatal("activation failure unexpectedly succeeded")
	}
	if committed != 0 || activateFailure.activateCalls != 1 {
		t.Fatalf("activation failure committed=%d activate=%d", committed, activateFailure.activateCalls)
	}
}

func TestVerifyCanonicalDeliveryTargetRejectsStaleCommittedReplay(t *testing.T) {
	reader := canonicalTargetReaderFake{target: deployment.DeliveryTarget{TargetID: "target-1", ProjectID: "project-1", Environment: "prod", ActiveGenerationID: "generation-new", TargetRevision: 2}}
	if err := verifyCanonicalDeliveryTarget(t.Context(), reader, "target-1", "project-1", "prod", "generation-old", 1); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("stale committed target = %v, want ErrDeliveryConflict", err)
	}
	reader.target.ActiveGenerationID = "generation-old"
	reader.target.TargetRevision = 1
	if err := verifyCanonicalDeliveryTarget(t.Context(), reader, "target-1", "project-1", "prod", "generation-old", 1); err != nil {
		t.Fatalf("matching committed target = %v", err)
	}
}

func (s sealedAuthorizationDeliveryStub) DeliveryCandidateByID(context.Context, string) (deployment.DeliveryCandidate, error) {
	return s.candidate, nil
}

func (s sealedAuthorizationDeliveryStub) PlanByID(context.Context, string) (deployment.DeliveryPlan, error) {
	return s.plan, nil
}

func TestSealedCoordinatorAuthorizationBindsEveryGraphImpactResource(t *testing.T) {
	identity, err := projectgraph.NewServingIdentity("project_demo", "prod", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	dashboardA, err := projectgraph.NewResourceID("dashboard_a")
	if err != nil {
		t.Fatal(err)
	}
	dashboardB, err := projectgraph.NewResourceID("dashboard_b")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project_demo", Kind: projectgraph.KindProject, Name: "project_demo"},
		{ID: dashboardA, Kind: projectgraph.KindDashboard, Name: "A"},
		{ID: dashboardB, Kind: projectgraph.KindDashboard, Name: "B"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	principal := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal_alice"}
	grant := func(id projectgraph.ResourceID) accesssnapshot.Grant {
		resource, err := access.NewResourceRef(id, projectgraph.KindDashboard)
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := access.NewCanonicalGrant(graph, principal, resource, access.CapabilityResourcePublish)
		if err != nil {
			t.Fatal(err)
		}
		return accesssnapshot.Grant{ID: "grant:" + id.String(), Canonical: canonical}
	}
	newSnapshot := func(grants ...accesssnapshot.Grant) accesssnapshot.AuthorizationSnapshot {
		snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, grants, nil)
		if err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	planDigest := "sha256:" + strings.Repeat("a", 64)
	plan := deployment.DeliveryPlan{
		ID: "plan_demo", Digest: planDigest, TargetID: "target_demo", ProjectID: projectgraph.ResourceID("project_demo"), Environment: "prod",
		Evidence: deployment.DeliveryPlanEvidence{GraphImpact: deployment.DeliveryGraphImpact{DirectlyModified: []deployment.DeliveryImpactResource{{ID: dashboardA.String(), Kind: string(projectgraph.KindDashboard), Change: "modified"}, {ID: dashboardB.String(), Kind: string(projectgraph.KindDashboard), Change: "modified"}}}},
	}
	candidate := deployment.DeliveryCandidate{ID: "candidate_demo", PlanID: plan.ID, PlanDigest: planDigest, TargetID: plan.TargetID, ProjectID: plan.ProjectID}
	binding := sealedcontrol.SealBinding{ProjectID: "project_demo", Environment: "prod", TargetID: "target_demo", CandidateID: candidate.ID, GenerationID: "generation_demo", PlanDigest: planDigest, ActorID: principal.ID, Operation: "publish"}
	accessModule := tusAccess{principal: accessmodule.Principal{ID: principal.ID}, ok: true, subjects: []access.SubjectRef{principal}}
	for _, test := range []struct {
		name     string
		snapshot accesssnapshot.AuthorizationSnapshot
		wantErr  bool
	}{
		{name: "one impacted resource missing", snapshot: newSnapshot(grant(dashboardA)), wantErr: true},
		{name: "every impacted resource granted", snapshot: newSnapshot(grant(dashboardA), grant(dashboardB)), wantErr: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := tusRuntime{project: projectgraph.ResourceID("project_demo"), lease: tusLease{identity: identity, snapshot: test.snapshot}}
			err := authorizeSealedPublication(context.Background(), binding, "target_demo", sealedAuthorizationDeliveryStub{candidate: candidate, plan: plan}, accessModule, runtime)
			if (err != nil) != test.wantErr {
				t.Fatalf("authorizeSealedPublication() error=%v, wantErr=%t", err, test.wantErr)
			}
		})
	}
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

func TestProtectManagedDataTransportBootstrapUsesExactTargetAndActiveFallback(t *testing.T) {
	const uploadID = "tus_" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	projectID := projectgraph.ResourceID("project_demo")
	connectionID := projectgraph.ResourceID("connection_sales")
	identity, err := projectgraph.NewServingIdentity(projectID, "prod", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	resolve := tusTargetResolverFunc(func(_ context.Context, id string) (projectgraph.ResourceID, projectgraph.ResourceID, error) {
		if id != uploadID {
			return "", "", manageddatacontrol.ErrNotFound
		}
		return projectID, connectionID, nil
	})
	allowedBootstrap := func(_ context.Context, _ *http.Request, operation string, target projectgraph.ResourceID, capability access.Capability) (accessmodule.APIGenBootstrapDecision, error) {
		if operation != "managedDataTusTransport" || target != projectID || capability != access.CapabilityResourceEdit {
			t.Fatalf("bootstrap identity = %q/%q/%q", operation, target, capability)
		}
		return accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: true}, nil
	}
	activeFallback := func(_ context.Context, _ *http.Request, _ string, _ projectgraph.ResourceID, _ access.Capability) (accessmodule.APIGenBootstrapDecision, error) {
		return accessmodule.APIGenBootstrapDecision{Handled: false}, nil
	}
	serve := func(name string, accessValue canonicalAccessModule, runtime canonicalRuntimeHost, bootstrap accessmodule.APIGenBootstrapAuthorizer) {
		t.Run(name, func(t *testing.T) {
			handler := protectManagedDataTransportWithBootstrap(accessValue, runtime, resolve, bootstrap, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, tusRequest(http.MethodPatch, uploadID))
			if response.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d; body %q", response.Code, http.StatusNoContent, response.Body.String())
			}
		})
	}
	bootstrapAccess := bootstrapTusAccess{tusAccess: tusAccess{principal: accessmodule.Principal{ID: "principal_admin"}, ok: true}, allowed: true}
	serve("bound but unactivated", bootstrapAccess, tusRuntime{project: projectID}, allowedBootstrap)
	activeAccess := tusAccess{principal: accessmodule.Principal{ID: "principal_admin"}, ok: true, subjects: []access.SubjectRef{{Kind: access.SubjectKindPrincipal, ID: "principal_admin"}}}
	serve("active generation falls through snapshot", activeAccess, tusRuntime{project: projectID, lease: tusLease{identity: identity, snapshot: tusSnapshot(t, "principal_admin", connectionID, true)}}, activeFallback)

	for _, test := range []struct {
		name      string
		access    bootstrapTusAccess
		bootstrap accessmodule.APIGenBootstrapAuthorizer
		want      int
	}{
		{name: "denied bootstrap target", access: bootstrapTusAccess{tusAccess: tusAccess{principal: accessmodule.Principal{ID: "principal_admin"}, ok: true}, allowed: true}, bootstrap: func(context.Context, *http.Request, string, projectgraph.ResourceID, access.Capability) (accessmodule.APIGenBootstrapDecision, error) {
			return accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: false}, nil
		}, want: http.StatusNotFound},
		{name: "bootstrap evidence unavailable", access: bootstrapTusAccess{tusAccess: tusAccess{principal: accessmodule.Principal{ID: "principal_admin"}, ok: true}, allowed: true}, bootstrap: func(context.Context, *http.Request, string, projectgraph.ResourceID, access.Capability) (accessmodule.APIGenBootstrapDecision, error) {
			return accessmodule.APIGenBootstrapDecision{}, errors.New("state unavailable")
		}, want: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := protectManagedDataTransportWithBootstrap(test.access, tusRuntime{}, resolve, test.bootstrap, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, tusRequest(http.MethodPatch, uploadID))
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d; body %q", response.Code, test.want, response.Body.String())
			}
		})
	}
	foreignResolve := tusTargetResolverFunc(func(_ context.Context, _ string) (projectgraph.ResourceID, projectgraph.ResourceID, error) {
		return projectgraph.ResourceID("project_foreign"), connectionID, nil
	})
	foreignBootstrap := func(_ context.Context, _ *http.Request, operation string, target projectgraph.ResourceID, _ access.Capability) (accessmodule.APIGenBootstrapDecision, error) {
		if operation != "managedDataTusTransport" || target != "project_foreign" {
			t.Fatalf("foreign bootstrap identity = %q/%q", operation, target)
		}
		return accessmodule.APIGenBootstrapDecision{Handled: true, Allowed: false}, nil
	}
	foreignHandler := protectManagedDataTransportWithBootstrap(bootstrapAccess, tusRuntime{}, foreignResolve, foreignBootstrap, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	foreignResponse := httptest.NewRecorder()
	foreignHandler.ServeHTTP(foreignResponse, tusRequest(http.MethodPatch, uploadID))
	if foreignResponse.Code != http.StatusNotFound {
		t.Fatalf("foreign target status = %d, want %d", foreignResponse.Code, http.StatusNotFound)
	}
}
