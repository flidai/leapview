package module

import (
	"context"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestPublisherResolvesWorkspaceSupportAtPublicationTime(t *testing.T) {
	resolutions := 0
	semanticModel := ""
	publisher := Publisher{
		Workspace: func() WorkspaceSupport {
			resolutions++
			return WorkspaceSupport{}
		},
		SemanticModelVersion: func(_ context.Context, _, _, modelID string) {
			semanticModel = modelID
		},
	}

	publisher.PublishRefreshTarget(context.Background(), projectgraph.ServingIdentity{ProjectID: "sales", Environment: "production", GenerationID: "generation"}, "model", "orders")
	publisher.PublishSemanticModelVersion(context.Background(), "sales", "production", "orders")

	if resolutions != 1 {
		t.Fatalf("workspace support resolutions = %d, want 1", resolutions)
	}
	if semanticModel != "orders" {
		t.Fatalf("semantic model = %q, want orders", semanticModel)
	}
}
