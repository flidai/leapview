package analyticsruntime

import (
	"context"
	"fmt"

	analyticsmaterialization "github.com/flidai/leapview/internal/analytics/materialization"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refresh "github.com/flidai/leapview/internal/refresh/run"
	"github.com/flidai/leapview/internal/runtimehost"
)

type WorkspaceRefreshMaterializer struct {
	Executor    analyticsmaterialization.WorkspaceExecutor
	ManagedData runtimehost.ManagedDataResolver
}

func (m WorkspaceRefreshMaterializer) Materialize(ctx context.Context, input refresh.MaterializeInput) (snapshotID int64, err error) {
	if m.ManagedData != nil {
		resolution, resolveErr := m.ManagedData.ResolveManagedData(ctx, input.Candidate.ID)
		if resolveErr != nil {
			return 0, resolveErr
		}
		if resolution.Lifetime != nil {
			defer func() {
				if releaseErr := resolution.Lifetime.Release(); err == nil && releaseErr != nil {
					snapshotID = 0
					err = fmt.Errorf("release managed data after workspace refresh: %w", releaseErr)
				}
			}()
		}
		if bindErr := bindManagedDataRoots(input.Definition, resolution.Roots); bindErr != nil {
			return 0, bindErr
		}
	}
	if m.Executor == nil {
		return 0, fmt.Errorf("analytical workspace materializer is unavailable")
	}
	identity, identityErr := projectgraph.NewServingIdentity(input.Candidate.ProjectID, string(input.Candidate.Environment), string(input.Candidate.ID))
	if identityErr != nil {
		return 0, fmt.Errorf("candidate serving identity: %w", identityErr)
	}
	return m.Executor.MaterializeWorkspace(ctx, analyticsmaterialization.WorkspaceRequest{
		Models: input.Definition.Models, Identity: identity,
		ConnectionEvidenceServingStateID: input.Active.ID,
		Environment:                      input.Environment, TargetType: input.Plan.TargetType, TargetID: input.Plan.TargetID,
		SemanticDigest: input.Candidate.Digest, ArtifactDigest: input.Artifact.Digest,
		Tables: input.Plan.Tables,
	})
}
