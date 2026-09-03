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

// ObservationWriter is invoked by a materializer inside its DuckLake commit
// transaction, immediately after Model relations are materialized and while the
// prepared source session remains live. Returning an error aborts that
// transaction. The external transaction handle never crosses this capability
// boundary. The writer may durably record control-plane evidence even if the
// later DuckLake commit fails; callers must resolve the commit marker and must
// never treat observation capture itself as proof of an external commit.
type ObservationWriter func(context.Context, []materialize.SourceObservation) error

// ObservationWriterExecutor is the native physical-build extension. Unlike
// ObservationExecutor, it guarantees that observation persistence occurs
// before the external DuckLake commit is acknowledged.
type ObservationWriterExecutor interface {
	MaterializeWithObservationWriter(context.Context, Request, ObservationWriter) (int64, []materialize.SourceObservation, error)
}
