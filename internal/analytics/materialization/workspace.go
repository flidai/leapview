// Package materialization exposes the narrow analytical execution port used by
// control-plane capabilities without exposing DuckDB lifecycle internals.
package materialization

import (
	"context"

	materialize "github.com/flidai/leapview/internal/analytics/materialize"
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

// SourceObservationProvider is an optional extension to Executor. It exposes
// source evidence captured by the same writer session that performed the
// materialization. Implementations must return defensive copies; callers may
// retain and mutate the returned values without affecting runtime state.
// Keeping this separate from Executor preserves existing materializer
// implementations and allows callers to degrade cleanly when evidence is not
// available.
type SourceObservationProvider interface {
	SourceObservations(context.Context) ([]materialize.SourceObservation, error)
}

// ObservationExecutor is an optional atomic extension to Executor. It
// returns source observations captured by the exact writer session that
// produced the snapshot, closing the gap between a Materialize call and a
// subsequent latest-observation read when callers run concurrently.
type ObservationExecutor interface {
	MaterializeWithObservations(context.Context, Request) (int64, []materialize.SourceObservation, error)
}
