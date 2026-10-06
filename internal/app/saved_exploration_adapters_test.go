package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesspolicy "github.com/flidai/leapview/internal/access/policy"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/exploration/saved"
	savedapplication "github.com/flidai/leapview/internal/analytics/exploration/saved/application"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard/catalog"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	"github.com/flidai/leapview/internal/dashboard/queryruntime"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
	"github.com/flidai/leapview/internal/workload"
)

const savedAdapterProject = projectgraph.ResourceID("project:saved")

type savedAdapterAccessStub struct {
	groups map[string][]string
}

func (a savedAdapterAccessStub) AuthorizationSubjects(_ context.Context, principalID string) ([]access.SubjectRef, error) {
	principal, err := access.NewSubjectRef(access.SubjectKindPrincipal, principalID)
	if err != nil {
		return nil, err
	}
	subjects := []access.SubjectRef{principal}
	for _, groupID := range a.groups[principalID] {
		group, err := access.NewSubjectRef(access.SubjectKindGroup, groupID)
		if err != nil {
			return nil, err
		}
		subjects = append(subjects, group)
	}
	return subjects, nil
}

type savedAdapterLease struct {
	runtime  projectruntime.Runtime
	identity projectgraph.ServingIdentity
	snapshot accesssnapshot.AuthorizationSnapshot
}

func (l *savedAdapterLease) Runtime() projectruntime.Runtime { return l.runtime }
func (l *savedAdapterLease) Identity() projectgraph.ServingIdentity {
	return l.identity
}
func (l *savedAdapterLease) Release() {}
func (l *savedAdapterLease) AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot {
	return l.snapshot
}

type savedAdapterMetrics struct {
	queryruntime.Metrics
	model    *semanticmodel.Model
	planner  consumer.Planner
	result   dataquery.Result
	identity projectgraph.ServingIdentity
	calls    int
	query    dataquery.Query
}

func (m *savedAdapterMetrics) Close() error                           { return nil }
func (m *savedAdapterMetrics) Identity() projectgraph.ServingIdentity { return m.identity }
func (m *savedAdapterMetrics) Catalog() catalog.Catalog {
	return catalog.Catalog{Project: catalog.Project{ID: savedAdapterProject}}
}
func (m *savedAdapterMetrics) SemanticModel(modelID string) (*semanticmodel.Model, bool) {
	return m.model, m.model != nil && (modelID == "semantic:sales" || modelID == m.model.Name)
}
func (m *savedAdapterMetrics) Planner(modelID string) (consumer.Planner, bool) {
	return m.planner, m.planner != nil && (modelID == "semantic:sales" || (m.model != nil && modelID == m.model.Name))
}
func (m *savedAdapterMetrics) ExecuteDataQuery(_ context.Context, query dataquery.Query) (dataquery.Result, error) {
	m.calls++
	m.query = query
	return m.result, nil
}

type savedAdapterAudit struct {
	events []access.CanonicalAuditEvent
}

func (a *savedAdapterAudit) RecordCanonicalAuditEvent(_ context.Context, event access.CanonicalAuditEvent) error {
	a.events = append(a.events, event)
	return nil
}

type savedAdapterAdmission struct {
	acquired int
	released int
	requests []workload.Request
}

func (a *savedAdapterAdmission) Acquire(ctx context.Context, request workload.Request) (workload.Lease, error) {
	a.acquired++
	a.requests = append(a.requests, request)
	return savedAdapterAdmissionLease{ctx: ctx, owner: a}, nil
}

type savedAdapterAdmissionLease struct {
	ctx   context.Context
	owner *savedAdapterAdmission
}

func (l savedAdapterAdmissionLease) Context() context.Context { return l.ctx }
func (savedAdapterAdmissionLease) QueueWait() time.Duration   { return 0 }
func (l savedAdapterAdmissionLease) Release()                 { l.owner.released++ }

func TestSavedExplorationAuthorizerUsesOwnerVisibilityAndTypedSemanticPermissions(t *testing.T) {
	graph, identity := savedAdapterGraph(t)
	owner := mustSavedAdapterSubject(t, access.SubjectKindPrincipal, "owner")
	ownerRead := mustSavedAdapterGrant(t, graph, "owner-read", owner, access.CapabilityResourceRead)
	admin := savedAdapterRole(t, "admin", "admin", access.ProjectRoleAdmin)
	reader := savedAdapterRole(t, "reader", "reader", access.ProjectRoleViewer)
	editor := savedAdapterRole(t, "editor", "editor", access.ProjectRoleEditor)

	newAuthorizer := func(t *testing.T, snapshot accesssnapshot.AuthorizationSnapshot, groups map[string][]string) *SavedExplorationAuthorizer {
		t.Helper()
		authorizer, err := NewSavedExplorationAuthorizer(savedAdapterAccessStub{groups: groups}, "target:saved")
		if err != nil {
			t.Fatal(err)
		}
		return authorizer
	}
	request := savedapplication.AuthorizationRequest{ActorID: "owner", ProjectID: savedAdapterProject, ExplorationID: "exploration-1", OwnerPrincipalID: "owner", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", Action: savedapplication.AuthorizationActionView}

	privateOwner := newSavedAdapterSnapshot(t, graph, identity, nil, []accesssnapshot.Grant{ownerRead})
	if err := newAuthorizer(t, privateOwner, nil).Authorize(savedAdapterContext("owner"), &savedAdapterLease{identity: identity, snapshot: privateOwner}, request); err != nil {
		t.Fatalf("private owner view: %v", err)
	}

	privateAdmin := newSavedAdapterSnapshot(t, graph, identity, []accesssnapshot.RoleBinding{admin}, nil)
	request.ActorID = "admin"
	if err := newAuthorizer(t, privateAdmin, nil).Authorize(savedAdapterContext("admin"), &savedAdapterLease{identity: identity, snapshot: privateAdmin}, request); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("private non-owner view = %v, want forbidden", err)
	}

	request.ActorID = "stranger"
	if err := newAuthorizer(t, privateOwner, nil).Authorize(savedAdapterContext("stranger"), &savedAdapterLease{identity: identity, snapshot: privateOwner}, request); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("private stranger error = %v, want forbidden", err)
	}

	request.Visibility = saved.VisibilityOrganization
	request.ActorID = "reader"
	orgReader := newSavedAdapterSnapshot(t, graph, identity, []accesssnapshot.RoleBinding{reader}, nil)
	if err := newAuthorizer(t, orgReader, nil).Authorize(savedAdapterContext("reader"), &savedAdapterLease{identity: identity, snapshot: orgReader}, request); err != nil {
		t.Fatalf("organization reader view: %v", err)
	}

	request.ActorID = "stranger"
	if err := newAuthorizer(t, orgReader, nil).Authorize(savedAdapterContext("stranger"), &savedAdapterLease{identity: identity, snapshot: orgReader}, request); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("organization stranger error = %v, want forbidden", err)
	}

	request = savedapplication.AuthorizationRequest{ActorID: "editor", ProjectID: savedAdapterProject, ExplorationID: "new", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", Action: savedapplication.AuthorizationActionCreate}
	editSnapshot := newSavedAdapterSnapshot(t, graph, identity, []accesssnapshot.RoleBinding{editor}, nil)
	if err := newAuthorizer(t, editSnapshot, nil).Authorize(savedAdapterContext("editor"), &savedAdapterLease{identity: identity, snapshot: editSnapshot}, request); err != nil {
		t.Fatalf("editor create: %v", err)
	}

	request = savedapplication.AuthorizationRequest{ActorID: "admin", ProjectID: savedAdapterProject, ExplorationID: "exploration-1", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", Action: savedapplication.AuthorizationActionArchive, Lifecycle: savedAdapterLifecycle(identity, savedAdapterProject, "exploration-1", "other", saved.VisibilityPrivate, saved.StatusActive, "semantic:sales")}
	if err := newAuthorizer(t, privateAdmin, nil).Authorize(savedAdapterContext("admin"), &savedAdapterLease{identity: identity, snapshot: privateAdmin}, request); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("non-owner archive = %v, want forbidden", err)
	}
}

func TestSavedExplorationAuthorizerRequiresModelCapabilityAndExpandedSubjects(t *testing.T) {
	graph, identity := savedAdapterGraph(t)
	group := mustSavedAdapterSubject(t, access.SubjectKindGroup, "group-readers")
	grant := mustSavedAdapterGrant(t, graph, "group-read", group, access.CapabilityResourceRead)
	snapshot := newSavedAdapterSnapshot(t, graph, identity, nil, []accesssnapshot.Grant{grant})
	authorizer, err := NewSavedExplorationAuthorizer(savedAdapterAccessStub{groups: map[string][]string{"member": {"group-readers"}}}, "target:saved")
	if err != nil {
		t.Fatal(err)
	}
	request := savedapplication.AuthorizationRequest{ActorID: "member", ProjectID: savedAdapterProject, ExplorationID: "exploration-1", OwnerPrincipalID: "owner", Visibility: saved.VisibilityOrganization, SemanticModelID: "semantic:sales", Action: savedapplication.AuthorizationActionView}
	if err := authorizer.Authorize(savedAdapterContext("member"), &savedAdapterLease{identity: identity, snapshot: snapshot}, request); err != nil {
		t.Fatalf("group-expanded view: %v", err)
	}

	request.ActorID = "owner"
	noModelAccess := newSavedAdapterSnapshot(t, graph, identity, nil, nil)
	if err := authorizer.Authorize(savedAdapterContext("owner"), &savedAdapterLease{identity: identity, snapshot: noModelAccess}, request); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("missing semantic model read error = %v, want forbidden", err)
	}
}

func TestSavedExplorationAuthorizerLifecycleVisibilityIsAuthoritative(t *testing.T) {
	graph, identity := savedAdapterGraph(t)
	reader := savedAdapterRole(t, "reader", "reader", access.ProjectRoleViewer)
	snapshot := newSavedAdapterSnapshot(t, graph, identity, []accesssnapshot.RoleBinding{reader}, nil)
	authorizer, err := NewSavedExplorationAuthorizer(savedAdapterAccessStub{}, "target:saved")
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name                string
		lifecycleVisibility saved.Visibility
		requestVisibility   saved.Visibility
		action              savedapplication.AuthorizationAction
		wantForbidden       bool
	}{
		{name: "private view cannot widen", lifecycleVisibility: saved.VisibilityPrivate, requestVisibility: saved.VisibilityOrganization, action: savedapplication.AuthorizationActionView, wantForbidden: true},
		{name: "private execute cannot widen", lifecycleVisibility: saved.VisibilityPrivate, requestVisibility: saved.VisibilityOrganization, action: savedapplication.AuthorizationActionExecute, wantForbidden: true},
		{name: "organization view cannot narrow", lifecycleVisibility: saved.VisibilityOrganization, requestVisibility: saved.VisibilityPrivate, action: savedapplication.AuthorizationActionView},
		{name: "organization execute cannot narrow", lifecycleVisibility: saved.VisibilityOrganization, requestVisibility: saved.VisibilityPrivate, action: savedapplication.AuthorizationActionExecute},
	} {
		t.Run(test.name, func(t *testing.T) {
			lifecycle := savedAdapterLifecycle(identity, savedAdapterProject, "exploration-1", "owner", test.lifecycleVisibility, saved.StatusActive, "semantic:sales")
			request := savedapplication.AuthorizationRequest{
				ActorID: "reader", ProjectID: savedAdapterProject, ExplorationID: lifecycle.ID,
				OwnerPrincipalID: lifecycle.OwnerPrincipalID, Visibility: test.requestVisibility,
				SemanticModelID: lifecycle.SemanticModelID, Action: test.action, Lifecycle: lifecycle,
			}
			err := authorizer.Authorize(savedAdapterContext("reader"), &savedAdapterLease{identity: identity, snapshot: snapshot}, request)
			if test.wantForbidden {
				if !errors.Is(err, access.ErrForbidden) {
					t.Fatalf("error = %v, want forbidden", err)
				}
			} else if err != nil {
				t.Fatalf("error = %v, want success", err)
			}
		})
	}
}

func TestSavedExplorationAuthorizerLifecycleModelIsAuthoritativeForReadAndExecute(t *testing.T) {
	graph, identity := savedAdapterGraph(t)
	modelReader := mustSavedAdapterSubject(t, access.SubjectKindPrincipal, "owner")
	marketingRead := mustSavedAdapterGrantForResource(t, graph, "marketing-read", modelReader, "semantic:marketing", access.CapabilityResourceRead)
	marketingUse := mustSavedAdapterGrantForResource(t, graph, "marketing-use", modelReader, "semantic:marketing", access.CapabilityResourceUse)
	snapshot := newSavedAdapterSnapshot(t, graph, identity, nil, []accesssnapshot.Grant{marketingRead, marketingUse})
	authorizer, err := NewSavedExplorationAuthorizer(savedAdapterAccessStub{}, "target:saved")
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := savedAdapterLifecycle(identity, savedAdapterProject, "exploration-1", "owner", saved.VisibilityPrivate, saved.StatusActive, "semantic:sales")
	request := savedapplication.AuthorizationRequest{
		ActorID: "owner", ProjectID: savedAdapterProject, ExplorationID: lifecycle.ID,
		OwnerPrincipalID: lifecycle.OwnerPrincipalID, Visibility: saved.VisibilityOrganization,
		SemanticModelID: "semantic:marketing", Action: savedapplication.AuthorizationActionExecute, Lifecycle: lifecycle,
	}
	if err := authorizer.Authorize(savedAdapterContext("owner"), &savedAdapterLease{identity: identity, snapshot: snapshot}, request); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("wrong lifecycle model error = %v, want forbidden", err)
	}
}

func TestSavedExplorationAuthorizerEditFailsClosedForUnauthorizedProposedModel(t *testing.T) {
	graph, identity := savedAdapterGraph(t)
	owner := mustSavedAdapterSubject(t, access.SubjectKindPrincipal, "owner")
	salesUse := mustSavedAdapterGrantForResource(t, graph, "sales-use", owner, "semantic:sales", access.CapabilityResourceUse)
	snapshot := newSavedAdapterSnapshot(t, graph, identity, nil, []accesssnapshot.Grant{salesUse})
	authorizer, err := NewSavedExplorationAuthorizer(savedAdapterAccessStub{}, "target:saved")
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := savedAdapterLifecycle(identity, savedAdapterProject, "exploration-1", "owner", saved.VisibilityPrivate, saved.StatusActive, "semantic:sales")
	request := savedapplication.AuthorizationRequest{
		ActorID: "owner", ProjectID: savedAdapterProject, ExplorationID: lifecycle.ID,
		OwnerPrincipalID: lifecycle.OwnerPrincipalID, Visibility: lifecycle.Visibility,
		SemanticModelID: "semantic:marketing", Action: savedapplication.AuthorizationActionEdit, Lifecycle: lifecycle,
	}
	if err := authorizer.Authorize(savedAdapterContext("owner"), &savedAdapterLease{identity: identity, snapshot: snapshot}, request); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("unauthorized proposed model error = %v, want forbidden", err)
	}
}

func TestSavedExplorationAuthorizerValidatesExplorationIDWithoutLifecycle(t *testing.T) {
	graph, identity := savedAdapterGraph(t)
	snapshot := newSavedAdapterSnapshot(t, graph, identity, nil, nil)
	authorizer, err := NewSavedExplorationAuthorizer(savedAdapterAccessStub{}, "target:saved")
	if err != nil {
		t.Fatal(err)
	}
	request := savedapplication.AuthorizationRequest{
		ActorID: "owner", ProjectID: savedAdapterProject, ExplorationID: "",
		OwnerPrincipalID: "owner", Visibility: saved.VisibilityPrivate,
		SemanticModelID: "semantic:sales", Action: savedapplication.AuthorizationActionCreate,
	}
	if err := authorizer.Authorize(savedAdapterContext("owner"), &savedAdapterLease{identity: identity, snapshot: snapshot}, request); !errors.Is(err, saved.ErrInvalidIdentifier) {
		t.Fatalf("missing lifecycle exploration ID error = %v, want invalid identifier", err)
	}
}

func savedAdapterPairs(t *testing.T, modelID projectgraph.ResourceID, actions ...access.Action) []access.PermissionPair {
	t.Helper()
	resource, err := access.NewResourceRef(modelID, projectgraph.KindSemanticModel)
	if err != nil {
		t.Fatal(err)
	}
	pairs := make([]access.PermissionPair, 0, len(actions))
	for _, action := range actions {
		pair, err := access.NewExactPermissionPair(action, savedAdapterProject, resource)
		if err != nil {
			t.Fatal(err)
		}
		pairs = append(pairs, pair)
	}
	return pairs
}

func TestSavedExplorationAuthorizerAttenuatesEveryAPITokenAction(t *testing.T) {
	graph, identity := savedAdapterGraph(t)
	snapshot := newSavedAdapterSnapshot(t, graph, identity, []accesssnapshot.RoleBinding{savedAdapterRole(t, "owner", "owner", access.ProjectRoleEditor)}, nil)
	authorizer, err := NewSavedExplorationAuthorizer(savedAdapterAccessStub{}, "target:saved")
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []savedapplication.AuthorizationAction{savedapplication.AuthorizationActionView, savedapplication.AuthorizationActionExecute, savedapplication.AuthorizationActionCreate, savedapplication.AuthorizationActionEdit, savedapplication.AuthorizationActionArchive} {
		for _, ceiling := range []string{"complete", "read-only", "missing-consume", "wrong-model", "empty", "legacy", "wrong-actor"} {
			t.Run(string(action)+"/"+ceiling, func(t *testing.T) {
				request := savedapplication.AuthorizationRequest{ActorID: "owner", ProjectID: savedAdapterProject, ExplorationID: "exploration-1", OwnerPrincipalID: "owner", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", Action: action}
				if action != savedapplication.AuthorizationActionCreate {
					request.Lifecycle = savedAdapterLifecycle(identity, savedAdapterProject, request.ExplorationID, "owner", saved.VisibilityPrivate, saved.StatusActive, request.SemanticModelID)
				}
				token := access.APIToken{ID: "token", PrincipalID: "owner", PermissionProfile: access.PermissionCatalogProfile, Permissions: savedAdapterPairs(t, "semantic:sales", access.ActionSemanticRead, access.ActionSemanticQuery, access.ActionSemanticConsume)}
				switch ceiling {
				case "read-only":
					token.Permissions = savedAdapterPairs(t, "semantic:sales", access.ActionSemanticRead)
				case "missing-consume":
					token.Permissions = savedAdapterPairs(t, "semantic:sales", access.ActionSemanticRead, access.ActionSemanticQuery)
				case "wrong-model":
					token.Permissions = savedAdapterPairs(t, "semantic:marketing", access.ActionSemanticRead, access.ActionSemanticQuery, access.ActionSemanticConsume)
				case "empty":
					token.Permissions = []access.PermissionPair{}
				case "legacy":
					token.PermissionProfile = ""
					token.Permissions = nil
					token.Capabilities = []access.Capability{access.CapabilityProjectAdmin}
				case "wrong-actor":
					token.PrincipalID = "other"
				}
				ctx := accessmodule.WithAPICredential(savedAdapterContext("owner"), access.APICredential{Principal: access.Principal{ID: "owner"}, Token: token})
				err := authorizer.Authorize(ctx, &savedAdapterLease{identity: identity, snapshot: snapshot}, request)
				allowed := ceiling == "complete" || action == savedapplication.AuthorizationActionView && (ceiling == "read-only" || ceiling == "missing-consume")
				if allowed && err != nil {
					t.Fatalf("error = %v, want success", err)
				}
				if !allowed && !errors.Is(err, access.ErrForbidden) {
					t.Fatalf("error = %v, want forbidden", err)
				}
			})
		}
	}
}

func TestSavedExplorationAuthorizerAttenuatesAuthoringCredentialScope(t *testing.T) {
	graph, identity := savedAdapterGraph(t)
	snapshot := newSavedAdapterSnapshot(t, graph, identity, []accesssnapshot.RoleBinding{savedAdapterRole(t, "owner", "owner", access.ProjectRoleEditor)}, nil)
	authorizer, err := NewSavedExplorationAuthorizer(savedAdapterAccessStub{}, "target:saved")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, target, project, actor string
		actions                      []access.Action
		tokenCeiling                 bool
		allowed                      bool
	}{
		{name: "exact read", target: "target:saved", project: string(savedAdapterProject), actor: "owner", actions: []access.Action{access.ActionSemanticRead}, allowed: true},
		{name: "wrong target", target: "target:other", project: string(savedAdapterProject), actor: "owner", actions: []access.Action{access.ActionSemanticRead}},
		{name: "wrong project", target: "target:saved", project: "project:other", actor: "owner", actions: []access.Action{access.ActionSemanticRead}},
		{name: "wrong actor", target: "target:saved", project: string(savedAdapterProject), actor: "other", actions: []access.Action{access.ActionSemanticRead}},
		{name: "query cannot read", target: "target:saved", project: string(savedAdapterProject), actor: "owner", actions: []access.Action{access.ActionSemanticQuery, access.ActionSemanticConsume}},
		{name: "independent token ceiling", target: "target:saved", project: string(savedAdapterProject), actor: "owner", actions: []access.Action{access.ActionSemanticRead}, tokenCeiling: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			pairs := savedAdapterPairs(t, "semantic:sales", test.actions...)
			for i := range pairs {
				pairs[i].Target.ProjectID = projectgraph.ResourceID(test.project)
			}
			scope, err := access.NewAuthoringScope(test.target, projectgraph.ResourceID(test.project), pairs)
			if err != nil {
				t.Fatal(err)
			}
			credential := access.APICredential{Principal: access.Principal{ID: "owner"}, Token: access.APIToken{ID: "authoring-token", PrincipalID: "owner"}, Authoring: &access.AuthoringSession{ID: "session", PrincipalID: test.actor, Scope: scope}}
			if test.tokenCeiling {
				credential.Token.PermissionProfile = access.PermissionCatalogProfile
				credential.Token.Permissions = []access.PermissionPair{}
			}
			ctx := accessmodule.WithAPICredential(savedAdapterContext("owner"), credential)
			request := savedapplication.AuthorizationRequest{ActorID: "owner", ProjectID: savedAdapterProject, ExplorationID: "exploration-1", OwnerPrincipalID: "owner", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", Action: savedapplication.AuthorizationActionView}
			err = authorizer.Authorize(ctx, &savedAdapterLease{identity: identity, snapshot: snapshot}, request)
			if test.allowed && err != nil {
				t.Fatal(err)
			}
			if !test.allowed && !errors.Is(err, access.ErrForbidden) {
				t.Fatalf("error = %v, want forbidden", err)
			}
			request.Action = savedapplication.AuthorizationActionArchive
			if err := authorizer.Authorize(ctx, &savedAdapterLease{identity: identity, snapshot: snapshot}, request); !errors.Is(err, access.ErrForbidden) {
				t.Fatalf("read-only authoring archive = %v, want forbidden", err)
			}
		})
	}
}

func TestSavedExplorationAdaptersRejectSnapshotActorAndRuntimeMismatches(t *testing.T) {
	graph, identity := savedAdapterGraph(t)
	grant := mustSavedAdapterGrant(t, graph, "owner-read", mustSavedAdapterSubject(t, access.SubjectKindPrincipal, "owner"), access.CapabilityResourceRead)
	snapshot := newSavedAdapterSnapshot(t, graph, identity, nil, []accesssnapshot.Grant{grant})
	authorizer, err := NewSavedExplorationAuthorizer(savedAdapterAccessStub{}, "target:saved")
	if err != nil {
		t.Fatal(err)
	}
	request := savedapplication.AuthorizationRequest{ActorID: "owner", ProjectID: savedAdapterProject, ExplorationID: "exploration-1", OwnerPrincipalID: "owner", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", Action: savedapplication.AuthorizationActionView}
	if err := authorizer.Authorize(savedAdapterContext("spoof"), &savedAdapterLease{identity: identity, snapshot: snapshot}, request); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("actor spoof error = %v, want forbidden", err)
	}
	wrongIdentity := identity
	wrongIdentity.GenerationID = "generation:other"
	if err := authorizer.Authorize(savedAdapterContext("owner"), &savedAdapterLease{identity: wrongIdentity, snapshot: snapshot}, request); !errors.Is(err, saved.ErrUnavailable) {
		t.Fatalf("snapshot identity mismatch error = %v, want unavailable", err)
	}
	if err := authorizer.Authorize(savedAdapterContext("owner"), &savedAdapterLease{identity: identity}, request); !errors.Is(err, saved.ErrUnavailable) {
		t.Fatalf("missing snapshot error = %v, want unavailable", err)
	}
	devRequest := request
	devRequest.ActorID = "dev"
	devRequest.Visibility = saved.VisibilityRestricted
	devContext := accessmodule.WithPrincipal(context.Background(), accessmodule.Principal{ID: "dev", DevBypass: true})
	if err := authorizer.Authorize(devContext, &savedAdapterLease{identity: identity, snapshot: snapshot}, devRequest); !errors.Is(err, saved.ErrInvalid) {
		t.Fatalf("development bypass reserved visibility error = %v, want invalid", err)
	}
}

func TestSavedExplorationExecutorUsesExactRuntimeGovernanceAndAdmission(t *testing.T) {
	graph, identity := savedAdapterGraph(t)
	role := savedAdapterRole(t, "editor", "owner", access.ProjectRoleEditor)
	snapshot := newSavedAdapterSnapshot(t, graph, identity, []accesssnapshot.RoleBinding{role}, nil)
	model := savedAdapterModel()
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	metrics := &savedAdapterMetrics{model: model, planner: planner, result: dataquery.Result{Status: dataquery.StatusSuccess}, identity: identity}
	lease := &savedAdapterLease{runtime: metrics, identity: identity, snapshot: snapshot}
	audit := &savedAdapterAudit{}
	admission := &savedAdapterAdmission{}
	executor, err := NewSavedExplorationExecutor(SavedExplorationExecutorOptions{AccessModule: savedAdapterAccessStub{}, Admitter: admission, AuditRecorder: audit})
	if err != nil {
		t.Fatal(err)
	}
	query := dataquery.SemanticAggregate("semantic:sales", "orders", nil, []dataquery.Field{{Field: "order_count"}}, nil, nil, 0, 10)
	query.Surface = dataquery.SurfaceSavedExploration
	query.ProjectID = savedAdapterProject
	query.PrincipalID = "owner"
	query.Operation = "saved_exploration_execute"
	result, err := executor.Execute(savedAdapterContext("owner"), lease, "owner", query)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Status != dataquery.StatusSuccess || metrics.calls != 1 || metrics.query.ProjectID != query.ProjectID || metrics.query.ModelID != query.ModelID || metrics.query.PrincipalID != query.PrincipalID || metrics.query.EffectivePolicyFingerprint == "" {
		t.Fatalf("result=%#v calls=%d query=%#v", result, metrics.calls, metrics.query)
	}
	if admission.acquired != 1 || admission.released != 1 {
		t.Fatalf("admission acquire/release=%d/%d, want 1/1", admission.acquired, admission.released)
	}
	if len(admission.requests) != 1 || admission.requests[0].Class != workload.Interactive || admission.requests[0].PrincipalID != "system:query" || admission.requests[0].Operation != "saved_exploration_execute" || admission.requests[0].EstimatedMemoryBytes != 64<<20 {
		t.Fatalf("workload admission request = %#v", admission.requests)
	}
	if len(audit.events) != 1 || audit.events[0].Identity != identity || audit.events[0].PrincipalID != "owner" || audit.events[0].Capability != access.CapabilityResourceUse {
		t.Fatalf("audit events=%#v", audit.events)
	}

	query.PrincipalID = "spoof"
	if _, err := executor.Execute(savedAdapterContext("owner"), lease, "owner", query); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("query actor spoof error=%v, want forbidden", err)
	}
	wrongCredential := access.APICredential{Principal: access.Principal{ID: "creator"}, Token: access.APIToken{ID: "creator-token", PrincipalID: "creator", Capabilities: []access.Capability{access.CapabilityResourceUse}}}
	query.PrincipalID = "owner"
	wrongCredentialContext := accessmodule.WithAPICredential(savedAdapterContext("owner"), wrongCredential)
	if _, err := executor.Execute(wrongCredentialContext, lease, "owner", query); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("mismatched API credential error=%v, want forbidden", err)
	}
	if metrics.calls != 1 {
		t.Fatalf("mismatched credential reached runtime: calls=%d, want 1", metrics.calls)
	}
	wrongLeaseIdentity := identity
	wrongLeaseIdentity.GenerationID = "generation:other"
	wrongLeaseRuntime := &savedAdapterMetrics{model: model, planner: planner, result: dataquery.Result{Status: dataquery.StatusSuccess}, identity: identity}
	wrongLease := &savedAdapterLease{runtime: wrongLeaseRuntime, identity: wrongLeaseIdentity, snapshot: snapshot}
	if _, err := executor.Execute(savedAdapterContext("owner"), wrongLease, "owner", query); !errors.Is(err, saved.ErrUnavailable) {
		t.Fatalf("mismatched lease/snapshot identity error=%v, want unavailable", err)
	}
	if wrongLeaseRuntime.calls != 0 {
		t.Fatalf("mismatched lease/snapshot executed: calls=%d, want zero", wrongLeaseRuntime.calls)
	}
	missingMetricsLease := &savedAdapterLease{runtime: savedAdapterRuntime{identity: identity}, identity: identity, snapshot: snapshot}
	if _, err := executor.Execute(savedAdapterContext("owner"), missingMetricsLease, "owner", dataquery.Query{ProjectID: savedAdapterProject, PrincipalID: "owner", ModelID: "semantic:sales", Kind: dataquery.KindModelRows}); !errors.Is(err, saved.ErrUnavailable) {
		t.Fatal("missing metrics was accepted")
	}
}

func TestSavedExplorationExecutorAppliesGovernedRLSAndMasking(t *testing.T) {
	graph, identity := savedAdapterGraph(t)
	role := savedAdapterRole(t, "editor", "owner", access.ProjectRoleEditor)
	physical, err := access.NewResourceRef("model:orders", projectgraph.KindModel)
	if err != nil {
		t.Fatal(err)
	}
	row, err := accesspolicy.Compile("saved-rls", accesspolicy.TypeRowFilter, `{"field":"orders.order_id","operator":"equals","values":[7]}`)
	if err != nil {
		t.Fatal(err)
	}
	mask, err := accesspolicy.Compile("saved-mask", accesspolicy.TypeColumnMask, `{"field":"orders.order_id","mask":"null"}`)
	if err != nil {
		t.Fatal(err)
	}
	policies := []accesssnapshot.DataPolicy{
		{ID: "saved-rls", Resource: physical, PolicyType: accesspolicy.TypeRowFilter, ExpressionJSON: `{"field":"orders.order_id","operator":"equals","values":[7]}`, Compiled: row},
		{ID: "saved-mask", Resource: physical, PolicyType: accesspolicy.TypeColumnMask, ExpressionJSON: `{"field":"orders.order_id","mask":"null"}`, Compiled: mask},
	}
	snapshot := newSavedAdapterSnapshotWithPolicies(t, graph, identity, []accesssnapshot.RoleBinding{role}, nil, policies)
	metrics := &savedAdapterMetrics{model: savedAdapterModel(), planner: nil, result: dataquery.Result{Status: dataquery.StatusSuccess}, identity: identity}
	metrics.planner, err = semanticquery.NewCompiledPlanner(metrics.model)
	if err != nil {
		t.Fatal(err)
	}
	lease := &savedAdapterLease{runtime: metrics, identity: identity, snapshot: snapshot}
	admission := &savedAdapterAdmission{}
	executor, err := NewSavedExplorationExecutor(SavedExplorationExecutorOptions{AccessModule: savedAdapterAccessStub{}, Admitter: admission, AuditRecorder: &savedAdapterAudit{}})
	if err != nil {
		t.Fatal(err)
	}
	query := dataquery.SemanticAggregate("semantic:sales", "orders", []dataquery.Field{{Field: "orders.order_id"}}, nil, nil, nil, 0, 10)
	query.Surface = dataquery.SurfaceSavedExploration
	query.ProjectID = savedAdapterProject
	query.PrincipalID = "owner"
	if _, err := executor.Execute(savedAdapterContext("owner"), lease, "owner", query); err != nil {
		t.Fatalf("execute governed policies: %v", err)
	}
	if len(metrics.query.Filters) != 1 || metrics.query.Filters[0].Field != "orders.order_id" || len(metrics.query.Filters[0].Values) != 1 || metrics.query.Filters[0].Values[0] != float64(7) {
		t.Fatalf("governed RLS query = %#v", metrics.query)
	}
	if len(metrics.query.ColumnMasks) != 1 || metrics.query.ColumnMasks[0].Field != "orders.order_id" || metrics.query.ColumnMasks[0].Mask != "null" {
		t.Fatalf("governed masking query = %#v", metrics.query)
	}
	if metrics.query.EffectivePolicyFingerprint == "" {
		t.Fatal("governed policy fingerprint is empty")
	}
}

type savedAdapterRuntime struct{ identity projectgraph.ServingIdentity }

func (savedAdapterRuntime) Close() error                             { return nil }
func (r savedAdapterRuntime) Identity() projectgraph.ServingIdentity { return r.identity }

func savedAdapterContext(principalID string) context.Context {
	return accessmodule.WithPrincipal(context.Background(), accessmodule.Principal{ID: principalID})
}

func savedAdapterGraph(t *testing.T) (projectgraph.ProjectGraph, projectgraph.ServingIdentity) {
	t.Helper()
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "semantic:sales", Kind: projectgraph.KindSemanticModel, Name: "sales"},
		{ID: "semantic:marketing", Kind: projectgraph.KindSemanticModel, Name: "marketing"},
		{ID: "model:orders", Kind: projectgraph.KindModel, Name: "orders"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity(savedAdapterProject, "production", "generation:saved")
	if err != nil {
		t.Fatal(err)
	}
	return graph, identity
}

func savedAdapterModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Name:     "sales",
		Sources:  map[string]semanticmodel.Source{"orders": {}},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Tables: map[string]semanticmodel.Table{"orders": {
			Execution:   semanticmodel.ExecutionDefinition{Source: "orders"},
			ModelName:   "orders",
			GrainEntity: "order",
			Entities:    map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"order_id"}}},
			Dimensions: map[string]semanticmodel.MetricDimension{
				"order_id": {Field: "orders.order_id", Table: "orders", Name: "order_id", Datatype: semanticmodel.DataTypeInteger},
			},
		}},
		Metrics: map[string]semanticmodel.Metric{
			"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}, Empty: "zero"},
		},
	}
}

func savedAdapterLifecycle(identity projectgraph.ServingIdentity, projectID projectgraph.ResourceID, id saved.ExplorationID, owner string, visibility saved.Visibility, status saved.Status, modelID projectgraph.ResourceID) saved.Lifecycle {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	return saved.Lifecycle{
		ProjectID: projectID, ID: id, OwnerPrincipalID: owner, Title: "Orders", Slug: "orders", Visibility: visibility, SemanticModelID: modelID, Status: status,
		CreatedAt: now, UpdatedAt: now,
		CurrentRevision: saved.RevisionMetadata{ID: "revision-1", Number: 1, ContentHash: "sha256:" + strings.Repeat("a", 64), CreatedAt: now, CreatedBy: owner, ServingIdentity: identity},
	}
}

func newSavedAdapterSnapshot(t *testing.T, graph projectgraph.ProjectGraph, identity projectgraph.ServingIdentity, roles []accesssnapshot.RoleBinding, grants []accesssnapshot.Grant) accesssnapshot.AuthorizationSnapshot {
	return newSavedAdapterSnapshotWithPolicies(t, graph, identity, roles, grants, nil)
}

func newSavedAdapterSnapshotWithPolicies(t *testing.T, graph projectgraph.ProjectGraph, identity projectgraph.ServingIdentity, roles []accesssnapshot.RoleBinding, grants []accesssnapshot.Grant, policies []accesssnapshot.DataPolicy) accesssnapshot.AuthorizationSnapshot {
	t.Helper()
	snapshot, err := accesssnapshot.NewAuthorizationSnapshotWithRoleBindings(identity, graph, roles, grants, policies)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func savedAdapterRole(t *testing.T, id, principal string, role access.ProjectRole) accesssnapshot.RoleBinding {
	t.Helper()
	subject := mustSavedAdapterSubject(t, access.SubjectKindPrincipal, principal)
	typedRole := access.PermissionRoleExplorer
	if role == access.ProjectRoleEditor || role == access.ProjectRoleAdmin {
		typedRole = access.PermissionRoleEditor
	}
	binding, err := access.NewTypedRoleBinding(id, "", subject, typedRole, savedAdapterProject)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func mustSavedAdapterSubject(t *testing.T, kind access.SubjectKind, id string) access.SubjectRef {
	t.Helper()
	subject, err := access.NewSubjectRef(kind, id)
	if err != nil {
		t.Fatal(err)
	}
	return subject
}

func mustSavedAdapterGrant(t *testing.T, graph projectgraph.ProjectGraph, id string, subject access.SubjectRef, capability access.Capability) accesssnapshot.Grant {
	t.Helper()
	return mustSavedAdapterGrantForResource(t, graph, id, subject, "semantic:sales", capability)
}

func mustSavedAdapterGrantForResource(t *testing.T, graph projectgraph.ProjectGraph, id string, subject access.SubjectRef, resourceID projectgraph.ResourceID, capability access.Capability) accesssnapshot.Grant {
	t.Helper()
	kind := projectgraph.KindSemanticModel
	if strings.HasPrefix(resourceID.String(), "model:") {
		kind = projectgraph.KindModel
	}
	resource, err := access.NewResourceRef(resourceID, kind)
	if err != nil {
		t.Fatal(err)
	}
	var actions []access.Action
	switch capability {
	case access.CapabilityResourceRead:
		actions = []access.Action{access.ActionSemanticRead}
	case access.CapabilityResourceUse:
		actions = []access.Action{access.ActionSemanticQuery, access.ActionSemanticConsume}
	default:
		t.Fatalf("unsupported test capability %q", capability)
	}
	var pairs []access.PermissionPair
	for _, action := range actions {
		pair, err := access.NewExactPermissionPair(action, savedAdapterProject, resource)
		if err != nil {
			t.Fatal(err)
		}
		pairs = append(pairs, pair)
	}
	grant, err := accesssnapshot.NewTypedGrant(id, "", subject, pairs)
	if err != nil {
		t.Fatal(err)
	}
	return grant
}

func TestSavedExplorationAuthorizerRejectsCrossOwnerMutationAndRequiresBothReboundModels(t *testing.T) {
	graph, identity := savedAdapterGraph(t)
	authorizer, err := NewSavedExplorationAuthorizer(savedAdapterAccessStub{}, "target:saved")
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := savedAdapterLifecycle(identity, savedAdapterProject, "exploration-1", "owner", saved.VisibilityPrivate, saved.StatusActive, "semantic:sales")
	for _, action := range []savedapplication.AuthorizationAction{savedapplication.AuthorizationActionEdit, savedapplication.AuthorizationActionArchive} {
		for _, visibility := range []saved.Visibility{saved.VisibilityPrivate, saved.VisibilityOrganization} {
			snapshot := newSavedAdapterSnapshot(t, graph, identity, []accesssnapshot.RoleBinding{savedAdapterRole(t, "other-editor", "other", access.ProjectRoleEditor)}, nil)
			lifecycle.Visibility = visibility
			request := savedapplication.AuthorizationRequest{ActorID: "other", ProjectID: savedAdapterProject, ExplorationID: lifecycle.ID, OwnerPrincipalID: "other", Visibility: saved.VisibilityOrganization, SemanticModelID: "semantic:marketing", Action: action, Lifecycle: lifecycle}
			if err := authorizer.Authorize(savedAdapterContext("other"), &savedAdapterLease{identity: identity, snapshot: snapshot}, request); !errors.Is(err, access.ErrForbidden) {
				t.Fatalf("cross-owner %s %s = %v, want forbidden", action, visibility, err)
			}
		}
	}
	lifecycle.Visibility = saved.VisibilityPrivate
	request := savedapplication.AuthorizationRequest{ActorID: "owner", ProjectID: savedAdapterProject, ExplorationID: lifecycle.ID, OwnerPrincipalID: "owner", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:marketing", Action: savedapplication.AuthorizationActionEdit, Lifecycle: lifecycle}
	for _, haveOldRead := range []bool{false, true} {
		pairs := savedAdapterPairs(t, "semantic:marketing", access.ActionSemanticRead, access.ActionSemanticQuery, access.ActionSemanticConsume)
		if haveOldRead {
			pairs = append(pairs, savedAdapterPairs(t, "semantic:sales", access.ActionSemanticRead)...)
		}
		grant, err := accesssnapshot.NewTypedGrant("owner-rebind", "", mustSavedAdapterSubject(t, access.SubjectKindPrincipal, "owner"), pairs)
		if err != nil {
			t.Fatal(err)
		}
		snapshot := newSavedAdapterSnapshot(t, graph, identity, nil, []accesssnapshot.Grant{grant})
		err = authorizer.Authorize(savedAdapterContext("owner"), &savedAdapterLease{identity: identity, snapshot: snapshot}, request)
		if haveOldRead && err != nil {
			t.Fatalf("authorized rebind = %v", err)
		}
		if !haveOldRead && !errors.Is(err, access.ErrForbidden) {
			t.Fatalf("rebind without old read = %v, want forbidden", err)
		}
		if haveOldRead {
			credential := access.APICredential{Principal: access.Principal{ID: "owner"}, Token: access.APIToken{ID: "new-only", PrincipalID: "owner", PermissionProfile: access.PermissionCatalogProfile, Permissions: savedAdapterPairs(t, "semantic:marketing", access.ActionSemanticRead, access.ActionSemanticQuery, access.ActionSemanticConsume)}}
			ctx := accessmodule.WithAPICredential(savedAdapterContext("owner"), credential)
			if err := authorizer.Authorize(ctx, &savedAdapterLease{identity: identity, snapshot: snapshot}, request); !errors.Is(err, access.ErrForbidden) {
				t.Fatalf("rebind token without old read = %v, want forbidden", err)
			}
		}
	}
}

func TestSavedExplorationAuthorizerDoesNotTreatHistoricalRoleAsTypedAuthority(t *testing.T) {
	graph, identity := savedAdapterGraph(t)
	subject := mustSavedAdapterSubject(t, access.SubjectKindPrincipal, "owner")
	role := accesssnapshot.RoleBinding{ID: "legacy-admin", Subject: subject, Role: access.ProjectRoleAdmin, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleAdmin)}
	snapshot := newSavedAdapterSnapshot(t, graph, identity, []accesssnapshot.RoleBinding{role}, nil)
	authorizer, err := NewSavedExplorationAuthorizer(savedAdapterAccessStub{}, "target:saved")
	if err != nil {
		t.Fatal(err)
	}
	request := savedapplication.AuthorizationRequest{ActorID: "owner", ProjectID: savedAdapterProject, ExplorationID: "exploration-1", OwnerPrincipalID: "owner", Visibility: saved.VisibilityPrivate, SemanticModelID: "semantic:sales", Action: savedapplication.AuthorizationActionCreate}
	if err := authorizer.Authorize(savedAdapterContext("owner"), &savedAdapterLease{identity: identity, snapshot: snapshot}, request); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("historical admin create = %v, want forbidden", err)
	}
	if err := authorizer.Authorize(context.Background(), &savedAdapterLease{identity: identity, snapshot: snapshot}, request); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("anonymous create = %v, want forbidden", err)
	}
}
