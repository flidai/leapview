package module

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestWithAgentQueryMetadata(t *testing.T) {
	metadata := dataquery.MetadataFromContext(WithAgentQueryMetadata(context.Background(), projectgraph.ResourceID("project_demo"), "principal_1"))
	if metadata.ProjectID != projectgraph.ResourceID("project_demo") {
		t.Fatalf("project ID = %q, want project_demo", metadata.ProjectID)
	}
	if metadata.Surface != dataquery.SurfaceAgent || metadata.Operation != dataquery.OperationAgentQuery {
		t.Fatalf("surface/operation = %q/%q, want agent/agent_query", metadata.Surface, metadata.Operation)
	}
	if metadata.PrincipalID != "principal_1" {
		t.Fatalf("principal ID = %q, want principal_1", metadata.PrincipalID)
	}
}
