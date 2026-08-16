package module

import (
	"context"
	"net/http"
	"testing"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var authorizationIdentity = projectgraph.ServingIdentity{ProjectID: "sales", Environment: "dev", GenerationID: "generation"}

func TestAuthorizePipelineAllowsDevelopmentBypassWithoutResolution(t *testing.T) {
	allowed, err := authorizePipeline(newAuthorizationRequest(t), authorizationIdentity, "daily", access.CapabilityResourceUse, AuthorizationConfig{
		CurrentPrincipal: func(*http.Request) (AuthorizationPrincipal, bool) {
			return AuthorizationPrincipal{ID: "dev", DevBypass: true}, true
		},
	})
	if err != nil || !allowed {
		t.Fatalf("allowed = %v, err = %v", allowed, err)
	}
}

func TestAuthorizePipelineUsesCanonicalPipelineReference(t *testing.T) {
	var resource access.ResourceRef
	allowed, err := authorizePipeline(newAuthorizationRequest(t), authorizationIdentity, "daily", access.CapabilityResourceRead, AuthorizationConfig{
		CurrentPrincipal: func(*http.Request) (AuthorizationPrincipal, bool) {
			return AuthorizationPrincipal{ID: "reader"}, true
		},
		AuthorizeObject: func(_ context.Context, principalID string, capability access.Capability, candidate access.ResourceRef) (bool, error) {
			if principalID != "reader" || capability != access.CapabilityResourceRead {
				t.Fatalf("authorization input = %q %q", principalID, capability)
			}
			resource = candidate
			return true, nil
		},
	})
	if err != nil || !allowed {
		t.Fatalf("allowed = %v, err = %v", allowed, err)
	}
	if resource.ID() != "daily" || resource.Kind() != projectgraph.KindPipeline {
		t.Fatalf("resource = %#v", resource)
	}
}

func TestAuthorizePipelineDoesNotSubstituteModelPermission(t *testing.T) {
	allowed, err := authorizePipeline(newAuthorizationRequest(t), authorizationIdentity, "daily", access.CapabilityResourceRead, AuthorizationConfig{
		CurrentPrincipal: func(*http.Request) (AuthorizationPrincipal, bool) {
			return AuthorizationPrincipal{ID: "reader"}, true
		},
		AuthorizeObject: func(_ context.Context, _ string, _ access.Capability, candidate access.ResourceRef) (bool, error) {
			return candidate.Kind() == projectgraph.KindModel, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("model permission substituted for pipeline permission")
	}
}

func TestAuthorizePipelineFailsClosedWithoutAuthorizer(t *testing.T) {
	allowed, err := authorizePipeline(newAuthorizationRequest(t), authorizationIdentity, "daily", access.CapabilityResourceRead, AuthorizationConfig{
		CurrentPrincipal: func(*http.Request) (AuthorizationPrincipal, bool) {
			return AuthorizationPrincipal{ID: "reader"}, true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("missing object authorizer allowed pipeline")
	}
}

func newAuthorizationRequest(t *testing.T) *http.Request {
	t.Helper()
	return (&http.Request{}).WithContext(t.Context())
}
