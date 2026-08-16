// Package materialization exposes the narrow analytical execution port used by
// control-plane capabilities without exposing DuckDB lifecycle internals.
package materialization

import (
	"context"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type WorkspaceRequest struct {
	Models                           map[string]*semanticmodel.Model
	Identity                         projectgraph.ServingIdentity
	ConnectionEvidenceServingStateID servingstate.ID
	Environment                      servingstate.Environment
	TargetType                       string
	TargetID                         projectgraph.ResourceID
	SemanticDigest                   string
	ArtifactDigest                   string
	Tables                           []string
}

type WorkspaceExecutor interface {
	MaterializeWorkspace(context.Context, WorkspaceRequest) (int64, error)
}
