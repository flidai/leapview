package accessadapter

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/project/graph"
)

type authorizationCall struct {
	kind       string
	actor      string
	project    graph.ResourceID
	resource   access.ResourceRef
	capability access.Capability
}

type authorizationPolicy struct {
	resourceAllowed bool
	projectAllowed  map[access.Capability]bool
	err             error
	calls           []authorizationCall
}

func (p *authorizationPolicy) adapter(t *testing.T) *Adapter {
	t.Helper()
	adapter, err := New(Options{
		AuthorizeResource: func(_ context.Context, actor string, project graph.ResourceID, resource access.ResourceRef, capability access.Capability) (bool, error) {
			p.calls = append(p.calls, authorizationCall{kind: "resource", actor: actor, project: project, resource: resource, capability: capability})
			return p.resourceAllowed, p.err
		},
		AuthorizeProjectCapability: func(_ context.Context, actor string, project graph.ResourceID, capability access.Capability) (bool, error) {
			p.calls = append(p.calls, authorizationCall{kind: "project", actor: actor, project: project, capability: capability})
			return p.projectAllowed[capability], p.err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func TestProjectDashboardMapsEveryActionToExactResourceCapability(t *testing.T) {
	tests := []struct {
		action     authoring.AuthorizationAction
		capability access.Capability
	}{
		{authoring.AuthorizationActionView, access.CapabilityResourceRead},
		{authoring.AuthorizationActionEdit, access.CapabilityResourceEdit},
		{authoring.AuthorizationActionPublish, access.CapabilityResourcePublish},
		{authoring.AuthorizationActionArchive, access.CapabilityResourceManage},
	}
	for _, test := range tests {
		t.Run(string(test.action), func(t *testing.T) {
			policy := &authorizationPolicy{resourceAllowed: true, projectAllowed: map[access.Capability]bool{}}
			err := policy.adapter(t).Authorize(t.Context(), service.AuthorizationRequest{
				ActorID: " actor-1 ", ProjectID: "project-1", DashboardID: "dashboard-1",
				Target: service.AuthorizationTargetProjectDashboard, Action: test.action,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(policy.calls) != 1 || policy.calls[0].kind != "resource" || policy.calls[0].capability != test.capability || policy.calls[0].resource.CanonicalID() != "dashboard-1" {
				t.Fatalf("authorization calls = %#v", policy.calls)
			}
		})
	}
}

func TestNewDashboardRequiresProjectEditAndOwnerOrAdmin(t *testing.T) {
	policy := &authorizationPolicy{resourceAllowed: true, projectAllowed: map[access.Capability]bool{access.CapabilityResourceEdit: true}}
	request := service.AuthorizationRequest{
		ActorID: "author", ProjectID: "project", DashboardID: "dashboard-new", OwnerPrincipalID: "author", SemanticModel: "semantic-model",
		Target: service.AuthorizationTargetNewDashboard, Visibility: authoring.VisibilityPrivate, Action: authoring.AuthorizationActionEdit,
	}
	if err := policy.adapter(t).Authorize(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if len(policy.calls) != 2 || policy.calls[0].kind != "project" || policy.calls[0].capability != access.CapabilityResourceEdit ||
		policy.calls[1].kind != "resource" || policy.calls[1].resource.Kind() != graph.KindSemanticModel || policy.calls[1].capability != access.CapabilityResourceRead {
		t.Fatalf("authorization calls = %#v", policy.calls)
	}

	policy = &authorizationPolicy{projectAllowed: map[access.Capability]bool{access.CapabilityResourceEdit: true}}
	request.OwnerPrincipalID = "other"
	if err := policy.adapter(t).Authorize(t.Context(), request); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("non-owner create error = %v", err)
	}
	policy = &authorizationPolicy{resourceAllowed: true, projectAllowed: map[access.Capability]bool{access.CapabilityResourceEdit: true, access.CapabilityProjectAdmin: true}}
	if err := policy.adapter(t).Authorize(t.Context(), request); err != nil {
		t.Fatalf("admin create error = %v", err)
	}
}

func TestNewDashboardRequiresGovernedSemanticModelRead(t *testing.T) {
	policy := &authorizationPolicy{projectAllowed: map[access.Capability]bool{access.CapabilityResourceEdit: true}}
	err := policy.adapter(t).Authorize(t.Context(), service.AuthorizationRequest{
		ActorID: "actor", ProjectID: "project", DashboardID: "allocated-dashboard", OwnerPrincipalID: "actor", SemanticModel: "semantic-model",
		Target: service.AuthorizationTargetNewDashboard, Visibility: authoring.VisibilityPrivate, Action: authoring.AuthorizationActionEdit,
	})
	if !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("missing semantic-model access error = %v, want forbidden", err)
	}
	if len(policy.calls) != 2 || policy.calls[1].kind != "resource" || policy.calls[1].resource.Kind() != graph.KindSemanticModel || policy.calls[1].capability != access.CapabilityResourceRead {
		t.Fatalf("authorization calls = %#v", policy.calls)
	}
}

func TestAuthoredDashboardCombinesVisibilityOwnershipAndProjectCapability(t *testing.T) {
	request := service.AuthorizationRequest{
		ActorID: "reader", ProjectID: "project", DashboardID: "dashboard-authored", OwnerPrincipalID: "owner",
		Target: service.AuthorizationTargetAuthoredDashboard, Visibility: authoring.VisibilityOrganization, Action: authoring.AuthorizationActionView,
	}
	policy := &authorizationPolicy{projectAllowed: map[access.Capability]bool{access.CapabilityResourceRead: true}}
	if err := policy.adapter(t).Authorize(t.Context(), request); err != nil {
		t.Fatalf("organization read error = %v", err)
	}

	for _, visibility := range []authoring.Visibility{authoring.VisibilityPrivate, authoring.VisibilityRestricted} {
		policy = &authorizationPolicy{projectAllowed: map[access.Capability]bool{access.CapabilityResourceRead: true}}
		request.Visibility = visibility
		if err := policy.adapter(t).Authorize(t.Context(), request); !errors.Is(err, access.ErrForbidden) {
			t.Fatalf("%s non-owner read error = %v", visibility, err)
		}
	}

	request.ActorID = "owner"
	request.Visibility = authoring.VisibilityPrivate
	policy = &authorizationPolicy{projectAllowed: map[access.Capability]bool{access.CapabilityResourceRead: true}}
	if err := policy.adapter(t).Authorize(t.Context(), request); err != nil {
		t.Fatalf("owner read error = %v", err)
	}

	request.ActorID = "editor"
	request.Action = authoring.AuthorizationActionEdit
	policy = &authorizationPolicy{projectAllowed: map[access.Capability]bool{access.CapabilityResourceEdit: true}}
	if err := policy.adapter(t).Authorize(t.Context(), request); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("non-owner edit error = %v", err)
	}
	policy = &authorizationPolicy{projectAllowed: map[access.Capability]bool{access.CapabilityResourceEdit: true, access.CapabilityProjectAdmin: true}}
	if err := policy.adapter(t).Authorize(t.Context(), request); err != nil {
		t.Fatalf("admin edit error = %v", err)
	}
}

func TestAuthoredLifecycleActionsUsePublishAndManageCapabilities(t *testing.T) {
	for _, test := range []struct {
		action authoring.AuthorizationAction
		want   access.Capability
	}{{authoring.AuthorizationActionPublish, access.CapabilityResourcePublish}, {authoring.AuthorizationActionArchive, access.CapabilityResourceManage}} {
		policy := &authorizationPolicy{projectAllowed: map[access.Capability]bool{test.want: true}}
		err := policy.adapter(t).Authorize(t.Context(), service.AuthorizationRequest{
			ActorID: "owner", ProjectID: "project", DashboardID: "dashboard", OwnerPrincipalID: "owner",
			Target: service.AuthorizationTargetAuthoredDashboard, Visibility: authoring.VisibilityPrivate, Action: test.action,
		})
		if err != nil || len(policy.calls) != 1 || policy.calls[0].capability != test.want {
			t.Fatalf("%s authorization = %#v, %v", test.action, policy.calls, err)
		}
	}
}

func TestAuthorizePreservesDecisionErrorsAndRejectsInvalidContracts(t *testing.T) {
	backendErr := errors.New("authorization backend unavailable")
	policy := &authorizationPolicy{resourceAllowed: true, projectAllowed: map[access.Capability]bool{}, err: backendErr}
	if err := policy.adapter(t).Authorize(t.Context(), validRequest()); !errors.Is(err, backendErr) || errors.Is(err, access.ErrForbidden) {
		t.Fatalf("backend error = %v", err)
	}
	if _, err := New(Options{}); err == nil {
		t.Fatal("New(empty) succeeded")
	}
	invalid := validRequest()
	invalid.Target = "unknown"
	if err := (&authorizationPolicy{projectAllowed: map[access.Capability]bool{}}).adapter(t).Authorize(t.Context(), invalid); err == nil {
		t.Fatal("unknown target succeeded")
	}
}

func validRequest() service.AuthorizationRequest {
	return service.AuthorizationRequest{
		ActorID: "actor", ProjectID: "project", DashboardID: "dashboard",
		Target: service.AuthorizationTargetProjectDashboard, Action: authoring.AuthorizationActionView,
	}
}
