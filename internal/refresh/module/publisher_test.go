package module

import (
	"context"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestPublisherUsesExactServingIdentityAtPublicationTime(t *testing.T) {
	var published projectgraph.ServingIdentity
	var semanticModel projectgraph.ResourceID
	publisher := Publisher{
		RefreshTarget: func(_ context.Context, identity projectgraph.ServingIdentity, _ string, _ projectgraph.ResourceID) {
			published = identity
		},
		SemanticModelVersion: func(_ context.Context, _ projectgraph.ServingIdentity, modelID projectgraph.ResourceID) {
			semanticModel = modelID
		},
	}

	publisher.PublishRefreshTarget(context.Background(), projectgraph.ServingIdentity{ProjectID: "sales", Environment: "production", GenerationID: "generation"}, "model", "orders")
	publisher.PublishSemanticModelVersion(context.Background(), projectgraph.ServingIdentity{ProjectID: "sales", Environment: "production", GenerationID: "generation"}, "orders")

	if published.ProjectID != "sales" || published.GenerationID != "generation" {
		t.Fatalf("published identity = %+v", published)
	}
	if semanticModel != "orders" {
		t.Fatalf("semantic model = %q, want orders", semanticModel)
	}
}
