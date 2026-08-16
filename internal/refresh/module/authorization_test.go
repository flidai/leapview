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

func TestAuthorizePipelineUsesCanonicalSemanticModelReference(t *testing.T) {
	var resource access.ResourceRef
	allowed, err := authorizePipeline(newAuthorizationRequest(t), authorizationIdentity, "daily", access.CapabilityResourceRead, AuthorizationConfig{
		CurrentPrincipal: func(*http.Request) (AuthorizationPrincipal, bool) {
			return AuthorizationPrincipal{ID: "reader"}, true
		},
		ResolvePipelineModel: func(_ context.Context, identity projectgraph.ServingIdentity, pipelineID string) (string, bool, error) {
			if identity != authorizationIdentity || pipelineID != "daily" {
				t.Fatalf("resolution input = %+v %q", identity, pipelineID)
			}
			return "orders", true, nil
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
	if resource.ID() != "orders" || resource.Kind() != projectgraph.KindModel {
		t.Fatalf("resource = %#v", resource)
	}
}

func newAuthorizationRequest(t *testing.T) *http.Request {
	t.Helper()
	return (&http.Request{}).WithContext(t.Context())
}
