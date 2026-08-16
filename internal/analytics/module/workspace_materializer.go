package module

import (
	"context"
	"fmt"

	analyticsduckdb "github.com/flidai/leapview/internal/analytics/duckdb"
	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	analyticsmaterialization "github.com/flidai/leapview/internal/analytics/materialization"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type duckDBWorkspaceMaterializer struct {
	environment *analyticsducklake.Environment
	credentials analyticsduckdb.CredentialResolver
	module      *Module
}

func (e duckDBWorkspaceMaterializer) Materialize(ctx context.Context, request analyticsmaterialization.Request) (int64, error) {
	runtime, err := analyticsduckdb.OpenWorkspaceMaterializeRuntime(ctx, analyticsduckdb.WorkspaceRuntimeConfig{
		Models: request.Models, Database: e.environment,
		CredentialResolver: e.credentials,
		ConnectionResolver: e.connectionResolver(request),
		ServingStateID:     request.Identity.GenerationID, WorkspaceID: request.Identity.ProjectID.String(),
		Environment: string(servingstate.NormalizeEnvironment(request.Environment)),
		TargetType:  request.TargetType, TargetID: request.TargetID.String(),
		SemanticDigest: request.SemanticDigest, ArtifactDigest: request.ArtifactDigest,
		SkipInitialRefresh: true,
	})
	if err != nil {
		return 0, err
	}
	defer runtime.Close()
	if err := runtime.RefreshWorkspaceTables(ctx, request.Tables); err != nil {
		return 0, err
	}
	snapshotID := runtime.DuckLakeSnapshotID()
	if snapshotID <= 0 {
		return 0, fmt.Errorf("refresh did not produce a DuckLake snapshot")
	}
	return snapshotID, nil
}

func (e duckDBWorkspaceMaterializer) connectionResolver(request analyticsmaterialization.Request) analyticsruntime.ConnectionResolver {
	if e.module == nil || e.module.activeRuntimeBindingEvidence == nil || request.ConnectionEvidenceServingStateID == "" {
		return nil
	}
	return &activeRuntimeConnectionResolver{
		module: e.module, servingStateID: string(request.ConnectionEvidenceServingStateID),
		workspaceID: request.Identity.ProjectID.String(), environment: string(servingstate.NormalizeEnvironment(request.Environment)),
	}
}
