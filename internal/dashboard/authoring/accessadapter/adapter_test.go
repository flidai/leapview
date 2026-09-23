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
	kind     string
	actor    string
	project  graph.ResourceID
	resource access.ResourceRef
	action   access.Action
}

type authorizationPolicy struct {
	resourceAllowed map[access.Action]bool
	projectAllowed  map[access.Action]bool
	untypedAction   access.Action
	err             error
	calls           []authorizationCall
}

func (p *authorizationPolicy) adapter(t *testing.T) *Adapter {
	t.Helper()
	adapter, err := New(Options{
		AuthorizeTypedResource: func(_ context.Context, actor string, project graph.ResourceID, resource access.ResourceRef, action access.Action) (bool, bool, error) {
			p.calls = append(p.calls, authorizationCall{kind: "resource", actor: actor, project: project, resource: resource, action: action})
			return action != p.untypedAction, p.resourceAllowed[action], p.err
		},
		AuthorizeTypedProject: func(_ context.Context, actor string, project graph.ResourceID, action access.Action) (bool, bool, error) {
			p.calls = append(p.calls, authorizationCall{kind: "project", actor: actor, project: project, action: action})
			return action != p.untypedAction, p.projectAllowed[action], p.err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func TestProjectDashboardMapsEveryActionToExactTypedResourcePair(t *testing.T) {
	tests := []struct {
		action authoring.AuthorizationAction
		want   access.Action
	}{
		{authoring.AuthorizationActionView, access.ActionDashboardRead},
		{authoring.AuthorizationActionEdit, access.ActionDashboardUpdate},
		{authoring.AuthorizationActionPublish, access.ActionDashboardPublish},
		{authoring.AuthorizationActionArchive, access.ActionDashboardDelete},
	}
	for _, test := range tests {
		t.Run(string(test.action), func(t *testing.T) {
			policy := &authorizationPolicy{resourceAllowed: map[access.Action]bool{test.want: true}}
			err := policy.adapter(t).Authorize(t.Context(), service.AuthorizationRequest{
				ActorID: " actor-1 ", ProjectID: "project-1", DashboardID: "dashboard-1",
				Target: service.AuthorizationTargetProjectDashboard, Action: test.action,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(policy.calls) != 1 || policy.calls[0].kind != "resource" || policy.calls[0].action != test.want ||
				policy.calls[0].actor != "actor-1" || policy.calls[0].project != "project-1" ||
				policy.calls[0].resource.CanonicalID() != "dashboard-1" || policy.calls[0].resource.Kind() != graph.KindDashboard {
				t.Fatalf("authorization calls = %#v", policy.calls)
			}
		})
	}
}

func TestNewDashboardRequiresTypedCreateAndSemanticRead(t *testing.T) {
	request := service.AuthorizationRequest{
		ActorID: "author", ProjectID: "project", DashboardID: "dashboard-new", OwnerPrincipalID: "other-owner", SemanticModel: "semantic-model",
		Target: service.AuthorizationTargetNewDashboard, Visibility: authoring.VisibilityPrivate, Action: authoring.AuthorizationActionEdit,
	}
	policy := &authorizationPolicy{
		resourceAllowed: map[access.Action]bool{access.ActionSemanticRead: true},
		projectAllowed:  map[access.Action]bool{access.ActionDashboardCreate: true},
	}
	if err := policy.adapter(t).Authorize(t.Context(), request); err != nil {
		t.Fatalf("typed dashboard creation for another owner = %v", err)
	}
	if len(policy.calls) != 2 || policy.calls[0].kind != "project" || policy.calls[0].action != access.ActionDashboardCreate ||
		policy.calls[1].kind != "resource" || policy.calls[1].action != access.ActionSemanticRead ||
		policy.calls[1].resource.Kind() != graph.KindSemanticModel || policy.calls[1].resource.CanonicalID() != "semantic-model" {
		t.Fatalf("authorization calls = %#v", policy.calls)
	}

	policy = &authorizationPolicy{projectAllowed: map[access.Action]bool{access.ActionDashboardCreate: true}}
	err := policy.adapter(t).Authorize(t.Context(), request)
	if !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("missing semantic read error = %v, want forbidden", err)
	}
}

func TestAuthoredDashboardRequiresExactTypedActionRegardlessOfOwnerOrVisibility(t *testing.T) {
	for _, test := range []struct {
		name       string
		actor      string
		owner      string
		visibility authoring.Visibility
	}{
		{name: "owner private", actor: "owner", owner: "owner", visibility: authoring.VisibilityPrivate},
		{name: "organization reader", actor: "reader", owner: "owner", visibility: authoring.VisibilityOrganization},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy := &authorizationPolicy{resourceAllowed: map[access.Action]bool{access.ActionDashboardRead: true}}
			err := policy.adapter(t).Authorize(t.Context(), service.AuthorizationRequest{
				ActorID: test.actor, ProjectID: "project", DashboardID: "dashboard", OwnerPrincipalID: test.owner,
				Target: service.AuthorizationTargetAuthoredDashboard, Visibility: test.visibility, Action: authoring.AuthorizationActionView,
			})
			if err != nil {
				t.Fatalf("typed dashboard read = %v", err)
			}
			if len(policy.calls) != 1 || policy.calls[0].kind != "resource" || policy.calls[0].action != access.ActionDashboardRead {
				t.Fatalf("authorization calls = %#v", policy.calls)
			}
		})
	}

	// Ownership and visibility metadata do not replace an exact assignment.
	policy := &authorizationPolicy{}
	err := policy.adapter(t).Authorize(t.Context(), service.AuthorizationRequest{
		ActorID: "owner", ProjectID: "project", DashboardID: "dashboard", OwnerPrincipalID: "owner",
		Target: service.AuthorizationTargetAuthoredDashboard, Visibility: authoring.VisibilityPrivate, Action: authoring.AuthorizationActionView,
	})
	if !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("owner without a typed action = %v, want forbidden", err)
	}
}

func TestAuthoredDashboardDependencyChangeRequiresIndependentSemanticRead(t *testing.T) {
	request := service.AuthorizationRequest{
		ActorID: "owner", ProjectID: "project", DashboardID: "dashboard", OwnerPrincipalID: "owner",
		SemanticModel: "semantic-replacement", DependencyChange: true,
		Target: service.AuthorizationTargetAuthoredDashboard, Visibility: authoring.VisibilityPrivate,
		Action: authoring.AuthorizationActionEdit,
	}
	policy := &authorizationPolicy{resourceAllowed: map[access.Action]bool{
		access.ActionDashboardUpdate: true,
		access.ActionSemanticRead:    true,
	}}
	if err := policy.adapter(t).Authorize(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if len(policy.calls) != 2 || policy.calls[0].action != access.ActionDashboardUpdate ||
		policy.calls[1].resource.Kind() != graph.KindSemanticModel || policy.calls[1].action != access.ActionSemanticRead {
		t.Fatalf("dependency authorization calls = %#v", policy.calls)
	}

	policy = &authorizationPolicy{resourceAllowed: map[access.Action]bool{access.ActionDashboardUpdate: true}}
	if err := policy.adapter(t).Authorize(t.Context(), request); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("missing semantic read error = %v, want forbidden", err)
	}
}

func TestMissingTypedDecisionFailsClosed(t *testing.T) {
	policy := &authorizationPolicy{
		resourceAllowed: map[access.Action]bool{access.ActionDashboardUpdate: true},
		untypedAction:   access.ActionDashboardUpdate,
	}
	err := policy.adapter(t).Authorize(t.Context(), service.AuthorizationRequest{
		ActorID: "actor", ProjectID: "project", DashboardID: "dashboard",
		Target: service.AuthorizationTargetProjectDashboard, Action: authoring.AuthorizationActionEdit,
	})
	if !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("untyped allowed result = %v, want forbidden", err)
	}
}

func TestAuthorizePreservesDecisionErrorsAndRejectsInvalidContracts(t *testing.T) {
	backendErr := errors.New("authorization backend unavailable")
	policy := &authorizationPolicy{err: backendErr}
	if err := policy.adapter(t).Authorize(t.Context(), validRequest()); !errors.Is(err, backendErr) || errors.Is(err, access.ErrForbidden) {
		t.Fatalf("backend error = %v", err)
	}
	if _, err := New(Options{}); err == nil {
		t.Fatal("New(empty) succeeded")
	}
	invalid := validRequest()
	invalid.Target = "unknown"
	if err := (&authorizationPolicy{}).adapter(t).Authorize(t.Context(), invalid); err == nil {
		t.Fatal("unknown target succeeded")
	}
}

func validRequest() service.AuthorizationRequest {
	return service.AuthorizationRequest{
		ActorID: "actor", ProjectID: "project", DashboardID: "dashboard",
		Target: service.AuthorizationTargetProjectDashboard, Action: authoring.AuthorizationActionView,
	}
}
