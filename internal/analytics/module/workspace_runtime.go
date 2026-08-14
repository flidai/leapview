package module

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsduckdb "github.com/flidai/leapview/internal/analytics/duckdb"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
)

type workspaceRuntimeFactory struct {
	module *Module
}

func (m *Module) WorkspaceRuntimeFactory() analyticsruntime.WorkspaceFactory {
	return workspaceRuntimeFactory{module: m}
}

func (f workspaceRuntimeFactory) OpenWorkspace(ctx context.Context, request analyticsruntime.WorkspaceRequest) (analyticsruntime.Workspace, error) {
	if f.module == nil || f.module.environment == nil || f.module.cache == nil {
		return nil, fmt.Errorf("analytical runtime is unavailable")
	}
	var connectionResolver analyticsruntime.ConnectionResolver
	if request.CandidateID != "" {
		var ok bool
		connectionResolver, ok = f.module.candidateRuntimeConnectionResolver(
			request.CandidateID,
			request.WorkspaceID,
		)
		if !ok {
			return nil, connectionbinding.ErrProviderUnavailable
		}
	} else if f.module.activeRuntimeBindingEvidence != nil {
		connectionResolver = &activeRuntimeConnectionResolver{
			module: f.module, servingStateID: request.ServingStateID,
			workspaceID: request.WorkspaceID, environment: request.Environment,
		}
	}
	cacheScope, err := f.module.cache.OpenScope(resultcache.ScopeID{
		WorkspaceID: request.WorkspaceID,
		RuntimeID:   workspaceRuntimeCacheIdentity(request),
	})
	if err != nil {
		return nil, err
	}
	runtime, err := analyticsduckdb.OpenWorkspaceMaterializeRuntime(ctx, analyticsduckdb.WorkspaceRuntimeConfig{
		Models: request.Models, Database: f.module.environment,
		CredentialResolver: f.module.credentials,
		ConnectionResolver: connectionResolver,
		QueryCache:         cacheScope, ResultLimits: request.ResultLimits,
		SnapshotID: request.SnapshotID, ServingStateID: request.ServingStateID,
		WorkspaceID: request.WorkspaceID, Environment: request.Environment,
		SemanticDigest: request.SemanticDigest, ArtifactDigest: request.ArtifactDigest,
		SourceDataDigest: request.SourceDataDigest,
		CandidateID:      request.CandidateID, AuthorizationFingerprint: request.AuthorizationFingerprint,
		BindingFingerprint: request.BindingFingerprint,
		RequiredExtensions: request.RequiredExtensions,
	})
	if err != nil {
		_ = cacheScope.Close()
		return nil, err
	}
	return runtime, nil
}

func workspaceRuntimeCacheIdentity(
	request analyticsruntime.WorkspaceRequest,
) string {
	if request.CandidateID == "" {
		return request.ServingStateID
	}
	return "candidate\x00" + request.CandidateID + "\x00" +
		request.ServingStateID
}
