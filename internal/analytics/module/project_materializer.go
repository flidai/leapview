package module

import (
	"context"
	"fmt"

	analyticsduckdb "github.com/flidai/leapview/internal/analytics/duckdb"
	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	analyticsmaterialization "github.com/flidai/leapview/internal/analytics/materialization"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type duckDBProjectMaterializer struct {
	environment *analyticsducklake.Environment
	credentials analyticsduckdb.CredentialResolver
	module      *Module
}

func (e duckDBProjectMaterializer) Materialize(ctx context.Context, request analyticsmaterialization.Request) (int64, error) {
	runtime, err := analyticsduckdb.OpenProjectMaterializeRuntime(ctx, analyticsduckdb.ProjectRuntimeConfig{
		Models: request.Models, Database: e.environment,
		CredentialResolver: e.credentials,
		ConnectionResolver: e.connectionResolver(request),
		ServingStateID:     request.Identity.GenerationID, ProjectID: request.Identity.ProjectID,
		Environment: string(servingstate.NormalizeEnvironment(request.Environment)),
		TargetType:  request.TargetType, TargetID: request.TargetID.String(),
		SemanticDigest: request.SemanticDigest, ArtifactDigest: request.ArtifactDigest,
		SkipInitialRefresh: true,
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
	if e.module == nil || e.module.activeRuntimeBindingEvidence == nil || request.ConnectionEvidenceServingStateID == "" {
		return nil
	}
	return &activeRuntimeConnectionResolver{
		module: e.module, servingStateID: string(request.ConnectionEvidenceServingStateID),
		projectID: request.Identity.ProjectID, environment: string(servingstate.NormalizeEnvironment(request.Environment)),
	}
}
