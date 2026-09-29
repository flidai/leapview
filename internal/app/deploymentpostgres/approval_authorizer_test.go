package deploymentpostgres

import (
	"context"
	"errors"
	"strings"
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
	approvePermission := approvalPermission(t, access.ActionDeliveryApprove, projectID)
	target := depauth.DeliveryTarget{TargetID: "target_demo", ProjectID: projectID.String(), Environment: "production"}
	tests := []struct {
		name        string
		action      depauth.ApprovalAction
		marker      accessmodule.PublicationApprovalBootstrapAuthorization
		project     string
		environment string
		permissions []access.PermissionPair
		resolveErr  error
		wantDenied  bool
	}{
		{name: "exact candidate reviewer", action: depauth.ApprovalActionApprove, marker: accessmodule.PublicationApprovalBootstrapAuthorization{ProjectID: projectID, PrincipalID: "reviewer", Capability: access.CapabilityProjectAdmin}, project: projectID.String(), environment: target.Environment, permissions: approvePermission},
		{name: "wrong action", action: depauth.ApprovalActionDeny, marker: accessmodule.PublicationApprovalBootstrapAuthorization{ProjectID: projectID, PrincipalID: "reviewer", Capability: access.CapabilityProjectAdmin}, project: projectID.String(), environment: target.Environment, permissions: approvePermission, wantDenied: true},
		{name: "foreign marker project", action: depauth.ApprovalActionApprove, marker: accessmodule.PublicationApprovalBootstrapAuthorization{ProjectID: "project_foreign", PrincipalID: "reviewer", Capability: access.CapabilityProjectAdmin}, project: projectID.String(), environment: target.Environment, permissions: approvePermission, wantDenied: true},
		{name: "candidate project mismatch", action: depauth.ApprovalActionApprove, marker: accessmodule.PublicationApprovalBootstrapAuthorization{ProjectID: projectID, PrincipalID: "reviewer", Capability: access.CapabilityProjectAdmin}, project: "project_foreign", environment: target.Environment, permissions: approvePermission, wantDenied: true},
		{name: "candidate environment mismatch", action: depauth.ApprovalActionApprove, marker: accessmodule.PublicationApprovalBootstrapAuthorization{ProjectID: projectID, PrincipalID: "reviewer", Capability: access.CapabilityProjectAdmin}, project: projectID.String(), environment: "staging", permissions: approvePermission, wantDenied: true},
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
			authorizer.SetCandidateResolver(func(_ context.Context, generationID, principalID string) (string, string, []access.PermissionPair, error) {
				if generationID != "generation_demo" || principalID != "reviewer" {
					t.Fatalf("candidate authorization identity = %q/%q", generationID, principalID)
				}
				return test.project, test.environment, test.permissions, test.resolveErr
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
	projectID := projectgraph.ResourceID("project_demo")
	target := depauth.DeliveryTarget{TargetID: "target_demo", ProjectID: projectID.String(), Environment: "production"}
	approvePermission := approvalPermission(t, access.ActionDeliveryApprove, projectID)
	authorizer, err := NewAccessApprovalAuthorizer(target.TargetID, func(context.Context, string) (depauth.DeliveryTarget, error) { return target, nil })
	if err != nil {
		t.Fatal(err)
	}
	authorizer.bootstrapAuthorization = func(context.Context) (accessmodule.BootstrapAuthorization, bool) {
		return accessmodule.BootstrapAuthorization{}, false
	}
	authorizer.SetResolvers(
		func(context.Context) (string, error) { return target.ProjectID, nil },
		func(_ context.Context, principalID string) ([]access.PermissionPair, error) {
			if principalID != "reviewer" {
				t.Fatalf("effective permission principal = %q", principalID)
			}
			return approvePermission, nil
		},
	)
	if err := authorizer.AuthorizeApproval(t.Context(), depauth.ApprovalAuthorizationInput{
		Action: depauth.ApprovalActionApprove, Request: depauth.ApprovalRequestInput{TargetID: target.TargetID}, Actor: depauth.ApprovalActor{PrincipalID: "reviewer"},
	}); err != nil {
		t.Fatalf("AuthorizeApproval() error = %v", err)
	}
}

func TestAccessApprovalAuthorizerBindsLegacyTransitionToExactCandidateAndIndependentActors(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	publishPermission := approvalPermission(t, access.ActionDeliveryPublish, projectID)
	approvePermission := approvalPermission(t, access.ActionDeliveryApprove, projectID)
	target := depauth.DeliveryTarget{
		TargetID: "target_demo", ProjectID: projectID.String(), Environment: "production",
		ActiveGenerationID: "generation_old",
	}
	marker := accessmodule.AccessTransitionApprovalAuthorization{
		TargetID: target.TargetID, ProjectID: projectID, Environment: target.Environment,
		ExpectedActiveGenerationID: "generation_old", CandidateID: "candidate_new",
		CandidateGenerationID: "generation_new", PublicationID: "publication_new",
		PublisherPrincipalID: "publisher", ReviewerPrincipalID: "reviewer",
		IntentDigest: "sha256:" + strings.Repeat("a", 64), CandidateSnapshotDigest: "sha256:" + strings.Repeat("b", 64),
	}
	baseRequest := depauth.ApprovalRequestInput{
		RequestID: "request_new", PublicationID: marker.PublicationID, TargetID: marker.TargetID,
		CandidateID: marker.CandidateID, GenerationID: marker.CandidateGenerationID,
	}
	current := &depauth.ApprovalRequest{
		RequestID: baseRequest.RequestID, PublicationID: marker.PublicationID, TargetID: marker.TargetID,
		CandidateID: marker.CandidateID, GenerationID: marker.CandidateGenerationID,
		RequestedBy: depauth.ApprovalActor{PrincipalID: marker.PublisherPrincipalID},
	}

	tests := []struct {
		name                 string
		action               depauth.ApprovalAction
		actor                string
		current              *depauth.ApprovalRequest
		mutateTarget         func(*depauth.DeliveryTarget)
		mutateMarker         func(*accessmodule.AccessTransitionApprovalAuthorization)
		mutateRequest        func(*depauth.ApprovalRequestInput)
		candidateProject     string
		candidateEnvironment string
		permissions          []access.PermissionPair
		candidateDigest      string
		wantDenied           bool
	}{
		{name: "publisher requests exact candidate", action: depauth.ApprovalActionRequest, actor: "publisher", permissions: publishPermission, candidateProject: projectID.String(), candidateEnvironment: target.Environment, candidateDigest: marker.CandidateSnapshotDigest},
		{name: "independent reviewer approves exact request", action: depauth.ApprovalActionApprove, actor: "reviewer", current: current, permissions: approvePermission, candidateProject: projectID.String(), candidateEnvironment: target.Environment, candidateDigest: marker.CandidateSnapshotDigest},
		{name: "publisher cannot approve their own request", action: depauth.ApprovalActionApprove, actor: "publisher", current: current, permissions: approvePermission, candidateProject: projectID.String(), candidateEnvironment: target.Environment, candidateDigest: marker.CandidateSnapshotDigest, wantDenied: true},
		{name: "reviewer cannot request publication", action: depauth.ApprovalActionRequest, actor: "reviewer", permissions: publishPermission, candidateProject: projectID.String(), candidateEnvironment: target.Environment, candidateDigest: marker.CandidateSnapshotDigest, wantDenied: true},
		{name: "wrong current requester", action: depauth.ApprovalActionApprove, actor: "reviewer", current: &depauth.ApprovalRequest{PublicationID: marker.PublicationID, GenerationID: marker.CandidateGenerationID, RequestedBy: depauth.ApprovalActor{PrincipalID: "other"}}, permissions: approvePermission, candidateProject: projectID.String(), candidateEnvironment: target.Environment, candidateDigest: marker.CandidateSnapshotDigest, wantDenied: true},
		{name: "wrong target active generation", action: depauth.ApprovalActionRequest, actor: "publisher", permissions: publishPermission, candidateProject: projectID.String(), candidateEnvironment: target.Environment, candidateDigest: marker.CandidateSnapshotDigest, mutateTarget: func(value *depauth.DeliveryTarget) { value.ActiveGenerationID = "generation_changed" }, wantDenied: true},
		{name: "wrong candidate identity", action: depauth.ApprovalActionRequest, actor: "publisher", permissions: publishPermission, candidateProject: projectID.String(), candidateEnvironment: target.Environment, candidateDigest: marker.CandidateSnapshotDigest, mutateRequest: func(value *depauth.ApprovalRequestInput) { value.CandidateID = "candidate_other" }, wantDenied: true},
		{name: "wrong candidate generation", action: depauth.ApprovalActionApprove, actor: "reviewer", current: current, permissions: approvePermission, candidateProject: projectID.String(), candidateEnvironment: target.Environment, candidateDigest: marker.CandidateSnapshotDigest, mutateRequest: func(value *depauth.ApprovalRequestInput) { value.GenerationID = "generation_other" }, wantDenied: true},
		{name: "wrong publication identity", action: depauth.ApprovalActionApprove, actor: "reviewer", current: current, permissions: approvePermission, candidateProject: projectID.String(), candidateEnvironment: target.Environment, candidateDigest: marker.CandidateSnapshotDigest, mutateRequest: func(value *depauth.ApprovalRequestInput) { value.PublicationID = "publication_other" }, wantDenied: true},
		{name: "wrong reviewer", action: depauth.ApprovalActionApprove, actor: "other", current: current, permissions: approvePermission, candidateProject: projectID.String(), candidateEnvironment: target.Environment, candidateDigest: marker.CandidateSnapshotDigest, wantDenied: true},
		{name: "deny is outside transition admission", action: depauth.ApprovalActionDeny, actor: "reviewer", current: current, permissions: approvePermission, candidateProject: projectID.String(), candidateEnvironment: target.Environment, candidateDigest: marker.CandidateSnapshotDigest, wantDenied: true},
		{name: "candidate project mismatch", action: depauth.ApprovalActionRequest, actor: "publisher", permissions: publishPermission, candidateProject: "project_foreign", candidateEnvironment: target.Environment, candidateDigest: marker.CandidateSnapshotDigest, wantDenied: true},
		{name: "candidate environment mismatch", action: depauth.ApprovalActionApprove, actor: "reviewer", current: current, permissions: approvePermission, candidateProject: projectID.String(), candidateEnvironment: "staging", candidateDigest: marker.CandidateSnapshotDigest, wantDenied: true},
		{name: "candidate snapshot digest mismatch", action: depauth.ApprovalActionApprove, actor: "reviewer", current: current, permissions: approvePermission, candidateProject: projectID.String(), candidateEnvironment: target.Environment, candidateDigest: "sha256:" + strings.Repeat("c", 64), wantDenied: true},
		{name: "candidate permission missing", action: depauth.ApprovalActionApprove, actor: "reviewer", current: current, candidateProject: projectID.String(), candidateEnvironment: target.Environment, candidateDigest: marker.CandidateSnapshotDigest, wantDenied: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolvedTarget := target
			if test.mutateTarget != nil {
				test.mutateTarget(&resolvedTarget)
			}
			contextMarker := marker
			if test.mutateMarker != nil {
				test.mutateMarker(&contextMarker)
			}
			ctx, err := accessmodule.WithAccessTransitionApprovalAuthorization(t.Context(), contextMarker)
			if err != nil {
				t.Fatalf("create transition approval context: %v", err)
			}
			authorizer, err := NewAccessApprovalAuthorizer(target.TargetID, func(context.Context, string) (depauth.DeliveryTarget, error) {
				return resolvedTarget, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			authorizer.bootstrapAuthorization = func(context.Context) (accessmodule.BootstrapAuthorization, bool) {
				return accessmodule.BootstrapAuthorization{}, false
			}
			authorizer.publicationApprovalAuthorization = func(context.Context) (accessmodule.PublicationApprovalBootstrapAuthorization, bool) {
				return accessmodule.PublicationApprovalBootstrapAuthorization{}, false
			}
			authorizer.SetCandidateResolver(func(_ context.Context, generationID, principalID string) (string, string, []access.PermissionPair, error) {
				if generationID != marker.CandidateGenerationID || principalID != test.actor {
					t.Fatalf("candidate resolver identity = %q/%q, want %q/%q", generationID, principalID, marker.CandidateGenerationID, test.actor)
				}
				return test.candidateProject, test.candidateEnvironment, test.permissions, nil
			})
			authorizer.CandidateSnapshotDigest = func(_ context.Context, generationID string) (string, error) {
				if generationID != marker.CandidateGenerationID {
					t.Fatalf("candidate digest generation = %q, want %q", generationID, marker.CandidateGenerationID)
				}
				return test.candidateDigest, nil
			}
			request := baseRequest
			if test.mutateRequest != nil {
				test.mutateRequest(&request)
			}
			err = authorizer.AuthorizeApproval(ctx, depauth.ApprovalAuthorizationInput{
				Action: test.action, Request: request, Current: test.current,
				Actor: depauth.ApprovalActor{PrincipalID: test.actor},
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

func approvalPermission(t *testing.T, action access.Action, projectID projectgraph.ResourceID) []access.PermissionPair {
	t.Helper()
	pair, err := access.NewProjectPermissionPair(action, projectID)
	if err != nil {
		t.Fatal(err)
	}
	return []access.PermissionPair{pair}
}
