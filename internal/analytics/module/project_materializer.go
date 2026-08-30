package module

import (
	"context"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsduckdb "github.com/flidai/leapview/internal/analytics/duckdb"
	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	analyticsmaterialization "github.com/flidai/leapview/internal/analytics/materialization"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type duckDBProjectMaterializer struct {
	environment *analyticsducklake.Environment
	credentials analyticsduckdb.CredentialResolver
	module      *Module
}

func (e duckDBProjectMaterializer) Materialize(ctx context.Context, request analyticsmaterialization.Request) (int64, error) {
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
		return 0, err
	}
	defer runtime.Close()
	if err := runtime.RefreshProjectTables(ctx, request.Tables); err != nil {
		return 0, err
	}
	snapshotID := runtime.DuckLakeSnapshotID()
	if snapshotID <= 0 {
		return 0, fmt.Errorf("refresh did not produce a DuckLake snapshot")
	}
	return snapshotID, nil
}

func (e duckDBProjectMaterializer) connectionResolver(request analyticsmaterialization.Request) analyticsruntime.ConnectionResolver {
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
