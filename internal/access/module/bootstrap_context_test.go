package module

import (
	"context"
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
