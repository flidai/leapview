// Package materialization exposes the narrow analytical execution port used by
// control-plane capabilities without exposing DuckDB lifecycle internals.
package materialization

import (
	"context"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type Request struct {
	Models      map[string]*semanticmodel.Model
	ModelTables map[string]semanticmodel.Table
	Identity    projectgraph.ServingIdentity
	CandidateID string
	// RelationNamespace is the value-only, authority-derived DuckDB schema
	// used by candidate materialization. Native candidate callers must supply
	// it; legacy callers may leave it empty and retain the model schema.
	RelationNamespace                string
	ConnectionEvidenceServingStateID servingstate.ID
	Environment                      servingstate.Environment
	TargetType                       string
	TargetID                         projectgraph.ResourceID
	SemanticDigest                   string
	ArtifactDigest                   string
	Tables                           []string
}

type Executor interface {
	Materialize(context.Context, Request) (int64, error)
}
