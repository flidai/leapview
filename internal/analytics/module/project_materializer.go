package module

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsduckdb "github.com/flidai/leapview/internal/analytics/duckdb"
	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	analyticsmaterialization "github.com/flidai/leapview/internal/analytics/materialization"
	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type duckDBProjectMaterializer struct {
	environment *analyticsducklake.Environment
	credentials analyticsduckdb.CredentialResolver
	module      *Module

	mu                 sync.RWMutex
	runSequence        uint64
	sourceObservations []analyticsmaterialize.SourceObservation
}

var _ analyticsmaterialization.SourceObservationProvider = (*duckDBProjectMaterializer)(nil)
var _ analyticsmaterialization.ObservationExecutor = (*duckDBProjectMaterializer)(nil)

func (e *duckDBProjectMaterializer) Materialize(ctx context.Context, request analyticsmaterialization.Request) (int64, error) {
	snapshotID, _, err := e.MaterializeWithObservations(ctx, request)
	return snapshotID, err
}

// MaterializeWithObservations executes one writer refresh and returns the
// observations captured by that same runtime before it is closed. The latest
// value is retained only as a compatibility getter for callers that cannot
// use the atomic extension.
func (e *duckDBProjectMaterializer) MaterializeWithObservations(ctx context.Context, request analyticsmaterialization.Request) (snapshotID int64, observations []analyticsmaterialize.SourceObservation, err error) {
	if e == nil || e.environment == nil {
		return 0, nil, fmt.Errorf("analytical runtime environment is unavailable")
	}
	run := e.beginMaterializationRun()
	runtime, err := analyticsduckdb.OpenProjectMaterializeRuntime(ctx, analyticsduckdb.ProjectRuntimeConfig{
		Models: request.Models, ModelTables: request.ModelTables, Database: e.environment,
		CredentialResolver: e.credentials,
		ConnectionResolver: e.connectionResolver(request),
		ServingStateID:     request.Identity.GenerationID, ProjectID: request.Identity.ProjectID,
		CandidateID:       request.CandidateID,
		Environment:       string(servingstate.NormalizeEnvironment(request.Environment)),
		RelationNamespace: request.RelationNamespace,
		TargetType:        request.TargetType, TargetID: request.TargetID.String(),
		SemanticDigest: request.SemanticDigest, ArtifactDigest: request.ArtifactDigest,
		SkipInitialRefresh: true, MaterializationOnly: true,
	})
	if err != nil {
		e.clearMaterializationRun(run)
		return 0, nil, err
	}
	defer func() {
		if closeErr := runtime.Close(); closeErr != nil {
			e.clearMaterializationRun(run)
			snapshotID = 0
			observations = nil
			err = errors.Join(err, fmt.Errorf("closing materialization runtime: %w", closeErr))
		}
	}()
	if err := runtime.RefreshProjectTables(ctx, request.Tables); err != nil {
		e.clearMaterializationRun(run)
		return 0, nil, err
	}
	// Capture evidence while the runtime still owns the resolved source
	// session. RefreshProjectTables populated this value from that exact
	// writer session; no second source resolution is permitted here.
	observations = runtime.SourceObservations()
	snapshotID = runtime.DuckLakeSnapshotID()
	if snapshotID <= 0 {
		e.clearMaterializationRun(run)
		return 0, nil, fmt.Errorf("refresh did not produce a DuckLake snapshot")
	}
	e.setMaterializationRun(run, observations)
	return snapshotID, cloneSourceObservations(observations), nil
}

// SourceObservations returns evidence captured by the most recent successful
// materialization. The returned schema slices are copied so callers cannot
// mutate state retained for subsequent native-build evidence.
func (e *duckDBProjectMaterializer) SourceObservations(ctx context.Context) ([]analyticsmaterialize.SourceObservation, error) {
	if e == nil {
		return nil, nil
	}
	if ctx != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return cloneSourceObservations(e.sourceObservations), nil
}

func (e *duckDBProjectMaterializer) beginMaterializationRun() uint64 {
	if e == nil {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.runSequence++
	e.sourceObservations = nil
	return e.runSequence
}

func (e *duckDBProjectMaterializer) clearMaterializationRun(run uint64) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if run == e.runSequence {
		e.sourceObservations = nil
	}
}

func (e *duckDBProjectMaterializer) setMaterializationRun(run uint64, observations []analyticsmaterialize.SourceObservation) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if run == e.runSequence {
		e.sourceObservations = cloneSourceObservations(observations)
	}
}

func cloneSourceObservations(observations []analyticsmaterialize.SourceObservation) []analyticsmaterialize.SourceObservation {
	if observations == nil {
		return nil
	}
	result := make([]analyticsmaterialize.SourceObservation, len(observations))
	for i, observation := range observations {
		result[i] = observation
		if observation.Schema != nil {
			result[i].Schema = make([]semanticmodel.ColumnSchema, len(observation.Schema))
			for j, column := range observation.Schema {
				result[i].Schema[j] = column
				if column.Nullable != nil {
					nullable := *column.Nullable
					result[i].Schema[j].Nullable = &nullable
				}
			}
		}
	}
	return result
}

func (e *duckDBProjectMaterializer) connectionResolver(request analyticsmaterialization.Request) analyticsruntime.ConnectionResolver {
	if request.CandidateID != "" {
		// Candidate bindings are private to one canonical candidate/project
		// identity. Never repair an alias or fall back to active-serving
		// evidence when that binding is unavailable.
		if request.CandidateID != strings.TrimSpace(request.CandidateID) ||
			request.Identity.ProjectID.Validate() != nil ||
			e.module == nil {
			return unavailableConnectionResolver{}
		}
		resolver, ok := e.module.candidateRuntimeConnectionResolver(
			request.CandidateID, request.Identity.ProjectID,
		)
		if !ok || resolver == nil {
			return unavailableConnectionResolver{}
		}
		return resolver
	}
	if request.ConnectionEvidenceServingStateID == "" {
		return nil
	}
	return &activeRuntimeConnectionResolver{
		module: e.module, servingStateID: string(request.ConnectionEvidenceServingStateID),
		projectID: request.Identity.ProjectID, environment: string(servingstate.NormalizeEnvironment(request.Environment)),
	}
}

// unavailableConnectionResolver preserves the resolver contract while
// failing closed when a requested candidate binding cannot be proven. A nil
// resolver would allow authored connections to proceed without target-bound
// candidate evidence.
type unavailableConnectionResolver struct{}

func (unavailableConnectionResolver) Resolve(
	context.Context, string, semanticmodel.Connection,
) (semanticmodel.Connection, error) {
	return semanticmodel.Connection{}, connectionbinding.ErrProviderUnavailable
}

var _ analyticsruntime.ConnectionResolver = unavailableConnectionResolver{}
