// Package materialization exposes the narrow analytical execution port used by
// control-plane capabilities without exposing DuckDB lifecycle internals.
package materialization

import (
	"context"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type WorkspaceRequest struct {
	Models                           map[string]*semanticmodel.Model
	ServingStateID                   string
	ConnectionEvidenceServingStateID string
	WorkspaceID                      string
	Environment                      servingstate.Environment
	TargetType                       string
	TargetID                         string
	SemanticDigest                   string
	ArtifactDigest                   string
	Tables                           []string
}

type WorkspaceExecutor interface {
	MaterializeWorkspace(context.Context, WorkspaceRequest) (int64, error)
}
