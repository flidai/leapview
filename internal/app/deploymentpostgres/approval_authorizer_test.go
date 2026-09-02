package deploymentpostgres

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	depauth "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestAccessApprovalAuthorizerUsesExactBootstrapAuthorization(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	target := depauth.DeliveryTarget{TargetID: "target_demo", ProjectID: projectID.String(), Environment: "production"}
	tests := []struct {
		name       string
		action     depauth.ApprovalAction
		actor      string
		marker     accessmodule.BootstrapAuthorization
		marked     bool
		wantDenied bool
	}{
		{name: "publication request", action: depauth.ApprovalActionRequest, actor: "publisher", marked: true, marker: accessmodule.BootstrapAuthorization{ProjectID: projectID, PrincipalID: "publisher", Capability: access.CapabilityResourcePublish}},
		{name: "generic marker cannot decide", action: depauth.ApprovalActionApprove, actor: "reviewer", marked: true, marker: accessmodule.BootstrapAuthorization{ProjectID: projectID, PrincipalID: "reviewer", Capability: access.CapabilityProjectAdmin}, wantDenied: true},
		{name: "missing marker", action: depauth.ApprovalActionRequest, actor: "publisher", wantDenied: true},
		{name: "foreign project", action: depauth.ApprovalActionRequest, actor: "publisher", marked: true, marker: accessmodule.BootstrapAuthorization{ProjectID: "project_foreign", PrincipalID: "publisher", Capability: access.CapabilityResourcePublish}, wantDenied: true},
		{name: "foreign principal", action: depauth.ApprovalActionRequest, actor: "publisher", marked: true, marker: accessmodule.BootstrapAuthorization{ProjectID: projectID, PrincipalID: "other", Capability: access.CapabilityResourcePublish}, wantDenied: true},
		{name: "wrong capability", action: depauth.ApprovalActionRequest, actor: "publisher", marked: true, marker: accessmodule.BootstrapAuthorization{ProjectID: projectID, PrincipalID: "publisher", Capability: access.CapabilityProjectAdmin}, wantDenied: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authorizer, err := NewAccessApprovalAuthorizer(target.TargetID, func(context.Context, string) (depauth.DeliveryTarget, error) { return target, nil })
			if err != nil {
				t.Fatal(err)
			}
			authorizer.bootstrapAuthorization = func(context.Context) (accessmodule.BootstrapAuthorization, bool) { return test.marker, test.marked }
			err = authorizer.AuthorizeApproval(t.Context(), depauth.ApprovalAuthorizationInput{
				Action: test.action, Request: depauth.ApprovalRequestInput{TargetID: target.TargetID}, Actor: depauth.ApprovalActor{PrincipalID: test.actor},
			})
			if test.wantDenied && !errors.Is(err, depauth.ErrApprovalUnauthorized) {
				t.Fatalf("AuthorizeApproval() error = %v, want approval unauthorized", err)
			}
			if !test.wantDenied && err != nil {
				t.Fatalf("AuthorizeApproval() error = %v", err)
			}
		})
	}
}

func TestAccessApprovalAuthorizerRechecksCandidateSnapshotForFreshReviewer(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	target := depauth.DeliveryTarget{TargetID: "target_demo", ProjectID: projectID.String(), Environment: "production"}
	tests := []struct {
		name         string
		action       depauth.ApprovalAction
		marker       accessmodule.PublicationApprovalBootstrapAuthorization
		project      string
		environment  string
		capabilities []access.Capability
		resolveErr   error
		wantDenied   bool
	}{
		{name: "exact candidate reviewer", action: depauth.ApprovalActionApprove, marker: accessmodule.PublicationApprovalBootstrapAuthorization{ProjectID: projectID, PrincipalID: "reviewer", Capability: access.CapabilityProjectAdmin}, project: projectID.String(), environment: target.Environment, capabilities: []access.Capability{access.CapabilityProjectAdmin}},
		{name: "wrong action", action: depauth.ApprovalActionDeny, marker: accessmodule.PublicationApprovalBootstrapAuthorization{ProjectID: projectID, PrincipalID: "reviewer", Capability: access.CapabilityProjectAdmin}, project: projectID.String(), environment: target.Environment, capabilities: []access.Capability{access.CapabilityProjectAdmin}, wantDenied: true},
		{name: "foreign marker project", action: depauth.ApprovalActionApprove, marker: accessmodule.PublicationApprovalBootstrapAuthorization{ProjectID: "project_foreign", PrincipalID: "reviewer", Capability: access.CapabilityProjectAdmin}, project: projectID.String(), environment: target.Environment, capabilities: []access.Capability{access.CapabilityProjectAdmin}, wantDenied: true},
		{name: "candidate project mismatch", action: depauth.ApprovalActionApprove, marker: accessmodule.PublicationApprovalBootstrapAuthorization{ProjectID: projectID, PrincipalID: "reviewer", Capability: access.CapabilityProjectAdmin}, project: "project_foreign", environment: target.Environment, capabilities: []access.Capability{access.CapabilityProjectAdmin}, wantDenied: true},
		{name: "candidate environment mismatch", action: depauth.ApprovalActionApprove, marker: accessmodule.PublicationApprovalBootstrapAuthorization{ProjectID: projectID, PrincipalID: "reviewer", Capability: access.CapabilityProjectAdmin}, project: projectID.String(), environment: "staging", capabilities: []access.Capability{access.CapabilityProjectAdmin}, wantDenied: true},
		{name: "candidate grant missing", action: depauth.ApprovalActionApprove, marker: accessmodule.PublicationApprovalBootstrapAuthorization{ProjectID: projectID, PrincipalID: "reviewer", Capability: access.CapabilityProjectAdmin}, project: projectID.String(), environment: target.Environment, wantDenied: true},
		{name: "candidate lookup fails", action: depauth.ApprovalActionApprove, marker: accessmodule.PublicationApprovalBootstrapAuthorization{ProjectID: projectID, PrincipalID: "reviewer", Capability: access.CapabilityProjectAdmin}, resolveErr: errors.New("candidate unavailable"), wantDenied: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authorizer, err := NewAccessApprovalAuthorizer(target.TargetID, func(context.Context, string) (depauth.DeliveryTarget, error) { return target, nil })
			if err != nil {
				t.Fatal(err)
			}
			authorizer.bootstrapAuthorization = func(context.Context) (accessmodule.BootstrapAuthorization, bool) {
				return accessmodule.BootstrapAuthorization{}, false
			}
			authorizer.publicationApprovalAuthorization = func(context.Context) (accessmodule.PublicationApprovalBootstrapAuthorization, bool) {
				return test.marker, true
			}
			authorizer.SetCandidateResolver(func(_ context.Context, generationID, principalID string) (string, string, []access.Capability, error) {
				if generationID != "generation_demo" || principalID != "reviewer" {
					t.Fatalf("candidate authorization identity = %q/%q", generationID, principalID)
				}
				return test.project, test.environment, test.capabilities, test.resolveErr
			})
			err = authorizer.AuthorizeApproval(t.Context(), depauth.ApprovalAuthorizationInput{
				Action: test.action, Request: depauth.ApprovalRequestInput{TargetID: target.TargetID, GenerationID: "generation_demo"}, Actor: depauth.ApprovalActor{PrincipalID: "reviewer"},
			})
			if test.wantDenied && !errors.Is(err, depauth.ErrApprovalUnauthorized) {
				t.Fatalf("AuthorizeApproval() error = %v, want approval unauthorized", err)
			}
			if !test.wantDenied && err != nil {
				t.Fatalf("AuthorizeApproval() error = %v", err)
			}
		})
	}
}

func TestAccessApprovalAuthorizerRetainsActiveSnapshotAuthorization(t *testing.T) {
	target := depauth.DeliveryTarget{TargetID: "target_demo", ProjectID: "project_demo", Environment: "production"}
	authorizer, err := NewAccessApprovalAuthorizer(target.TargetID, func(context.Context, string) (depauth.DeliveryTarget, error) { return target, nil })
	if err != nil {
		t.Fatal(err)
	}
	authorizer.bootstrapAuthorization = func(context.Context) (accessmodule.BootstrapAuthorization, bool) {
		return accessmodule.BootstrapAuthorization{}, false
	}
	authorizer.SetResolvers(
		func(context.Context) (string, error) { return target.ProjectID, nil },
		func(_ context.Context, principalID string) ([]access.Capability, error) {
			if principalID != "reviewer" {
				t.Fatalf("effective capability principal = %q", principalID)
			}
			return []access.Capability{access.CapabilityProjectAdmin}, nil
		},
	)
	if err := authorizer.AuthorizeApproval(t.Context(), depauth.ApprovalAuthorizationInput{
		Action: depauth.ApprovalActionApprove, Request: depauth.ApprovalRequestInput{TargetID: target.TargetID}, Actor: depauth.ApprovalActor{PrincipalID: "reviewer"},
	}); err != nil {
		t.Fatalf("AuthorizeApproval() error = %v", err)
	}
}
