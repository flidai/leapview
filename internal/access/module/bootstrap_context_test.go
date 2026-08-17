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
