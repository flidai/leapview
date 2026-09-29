package module

import (
	"context"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestBootstrapAuthorizationContextBindsExactRequestScope(t *testing.T) {
	project, err := projectgraph.NewResourceID("project_demo")
	if err != nil {
		t.Fatal(err)
	}
	ctx := withBootstrapAuthorization(context.Background(), project, "principal_admin", access.CapabilityResourceEdit)
	marker, ok := BootstrapAuthorizationFromContext(ctx)
	if !ok || marker.ProjectID != project || marker.PrincipalID != "principal_admin" || marker.Capability != access.CapabilityResourceEdit {
		t.Fatalf("bootstrap marker = %#v, ok=%v", marker, ok)
	}
	if _, ok := BootstrapAuthorizationFromContext(context.Background()); ok {
		t.Fatal("missing bootstrap marker unexpectedly resolved")
	}
}

func TestBootstrapAuthorizationContextRejectsInvalidValues(t *testing.T) {
	ctx := withBootstrapAuthorization(context.Background(), "", "principal_admin", access.CapabilityResourceEdit)
	if _, ok := BootstrapAuthorizationFromContext(ctx); ok {
		t.Fatal("invalid project marker unexpectedly resolved")
	}
	project, err := projectgraph.NewResourceID("project_demo")
	if err != nil {
		t.Fatal(err)
	}
	ctx = withBootstrapAuthorization(context.Background(), project, "", access.CapabilityResourceEdit)
	if _, ok := BootstrapAuthorizationFromContext(ctx); ok {
		t.Fatal("invalid principal marker unexpectedly resolved")
	}
}

func TestManagedDataStagingAuthorizationBindsExactConnection(t *testing.T) {
	project := projectgraph.ResourceID("project_demo")
	connection := projectgraph.ResourceID("connection:new")
	ctx := withManagedDataStagingAuthorization(context.Background(), project, connection, "publisher", access.CapabilityResourceEdit)
	marker, ok := ManagedDataStagingAuthorizationFromContext(ctx)
	if !ok || marker.ProjectID != project || marker.ConnectionID != connection || marker.PrincipalID != "publisher" || marker.Capability != access.CapabilityResourceEdit {
		t.Fatalf("managed-data marker = %#v, ok=%t", marker, ok)
	}
	if _, ok := BootstrapAuthorizationFromContext(ctx); ok {
		t.Fatal("managed-data marker must not satisfy generic bootstrap authorization")
	}
}

func TestPublicationApprovalBootstrapAuthorizationContextIsOperationSpecific(t *testing.T) {
	project, err := projectgraph.NewResourceID("project_demo")
	if err != nil {
		t.Fatal(err)
	}
	ctx := withPublicationApprovalBootstrapAuthorization(context.Background(), project, "principal_reviewer")
	marker, ok := PublicationApprovalBootstrapAuthorizationFromContext(ctx)
	if !ok || marker.ProjectID != project || marker.PrincipalID != "principal_reviewer" || marker.Capability != access.CapabilityProjectAdmin {
		t.Fatalf("publication approval marker = %#v, ok=%v", marker, ok)
	}
	if _, ok := BootstrapAuthorizationFromContext(ctx); ok {
		t.Fatal("approval marker unexpectedly resolved as generic bootstrap authorization")
	}
	if _, ok := PublicationApprovalBootstrapAuthorizationFromContext(context.Background()); ok {
		t.Fatal("missing publication approval marker unexpectedly resolved")
	}
}

func TestPublicationApprovalBootstrapAuthorizationContextRejectsInvalidValues(t *testing.T) {
	ctx := withPublicationApprovalBootstrapAuthorization(context.Background(), "", "principal_reviewer")
	if _, ok := PublicationApprovalBootstrapAuthorizationFromContext(ctx); ok {
		t.Fatal("invalid project approval marker unexpectedly resolved")
	}
	project, err := projectgraph.NewResourceID("project_demo")
	if err != nil {
		t.Fatal(err)
	}
	ctx = withPublicationApprovalBootstrapAuthorization(context.Background(), project, "")
	if _, ok := PublicationApprovalBootstrapAuthorizationFromContext(ctx); ok {
		t.Fatal("invalid principal approval marker unexpectedly resolved")
	}
}

func TestAccessTransitionApprovalContextRequiresBoundDistinctActorsAndSnapshot(t *testing.T) {
	marker := AccessTransitionApprovalAuthorization{
		TargetID: "target_demo", ProjectID: "project_demo", Environment: "production",
		ExpectedActiveGenerationID: "generation_old", CandidateID: "candidate_new", CandidateGenerationID: "generation_new", PublicationID: "publication_new",
		PublisherPrincipalID: "publisher", ReviewerPrincipalID: "reviewer", IntentDigest: "sha256:" + strings.Repeat("a", 64), CandidateSnapshotDigest: "sha256:" + strings.Repeat("b", 64),
	}
	ctx, err := WithAccessTransitionApprovalAuthorization(context.Background(), marker)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := AccessTransitionApprovalAuthorizationFromContext(ctx)
	if !ok || got != marker {
		t.Fatalf("transition marker = %#v, %v; want %#v", got, ok, marker)
	}
	for name, mutate := range map[string]func(*AccessTransitionApprovalAuthorization){
		"same actors": func(value *AccessTransitionApprovalAuthorization) {
			value.ReviewerPrincipalID = value.PublisherPrincipalID
		},
		"missing candidate binding": func(value *AccessTransitionApprovalAuthorization) { value.CandidateGenerationID = "" },
		"missing publication":       func(value *AccessTransitionApprovalAuthorization) { value.PublicationID = "" },
		"invalid semantic digest":   func(value *AccessTransitionApprovalAuthorization) { value.IntentDigest = "sha256:invalid" },
		"invalid candidate digest":  func(value *AccessTransitionApprovalAuthorization) { value.CandidateSnapshotDigest = "sha256:invalid" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := marker
			mutate(&invalid)
			if _, err := WithAccessTransitionApprovalAuthorization(context.Background(), invalid); err == nil {
				t.Fatal("invalid transition marker was accepted")
			}
		})
	}
}
