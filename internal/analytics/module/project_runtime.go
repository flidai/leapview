package module

import (
	"context"
	"fmt"

	analyticscache "github.com/flidai/leapview/internal/analytics/cache"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsduckdb "github.com/flidai/leapview/internal/analytics/duckdb"
	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
)

type projectRuntimeFactory struct {
	module      *Module
	environment *analyticsducklake.Environment
}

func (m *Module) ProjectRuntimeFactory() analyticsruntime.ProjectFactory {
	return projectRuntimeFactory{module: m}
}

// ProjectRuntimeFactoryForEnvironment builds the governed project runtime
// against one caller-owned immutable DuckLake environment. The module keeps
// credential, binding, and cache policy while the caller owns that
// environment's lifetime (for example a sealed read-only catalog reader).
func (m *Module) ProjectRuntimeFactoryForEnvironment(environment *analyticsducklake.Environment) analyticsruntime.ProjectFactory {
	return projectRuntimeFactory{module: m, environment: environment}
}

func (f projectRuntimeFactory) OpenProject(ctx context.Context, request analyticsruntime.ProjectRequest) (analyticsruntime.Project, error) {
	if f.module == nil || f.module.cache == nil {
		return nil, fmt.Errorf("analytical runtime is unavailable")
	}
	environment := f.environment
	if environment == nil {
		environment = f.module.environment
	}
	if environment == nil {
		return nil, fmt.Errorf("analytical runtime environment is unavailable")
	}
	cachePartition := request.QueryCachePartition
	if cachePartition.Version() == 0 {
		return nil, fmt.Errorf("typed query cache partition is required")
	}
	wantKind := resultidentity.PartitionProduction
	if request.CandidateID != "" {
		wantKind = resultidentity.PartitionCandidate
	}
	if cachePartition.Kind() != wantKind || cachePartition.ProjectID() != request.ProjectID || cachePartition.Environment() != request.Environment || cachePartition.CandidateID() != request.CandidateID {
		return nil, fmt.Errorf("query cache partition does not match project serving scope")
	}
	var connectionResolver analyticsruntime.ConnectionResolver
	if request.CandidateID != "" {
		var ok bool
		connectionResolver, ok = f.module.candidateRuntimeConnectionResolver(
			request.CandidateID,
			request.ProjectID,
		)
		if !ok {
			return nil, connectionbinding.ErrProviderUnavailable
		}
	} else if f.module.activeRuntimeBindingEvidence != nil {
		connectionResolver = &activeRuntimeConnectionResolver{
			module: f.module, servingStateID: request.ServingStateID,
			projectID: request.ProjectID, environment: request.Environment,
		}
	}
	cacheScope, err := f.module.cache.OpenScope(resultcache.ScopeID{
		RuntimeID: projectRuntimeCacheIdentity(request), PartitionID: analyticscache.PartitionIdentity(cachePartition),
	})
	if err != nil {
		return nil, err
	}
	runtime, err := analyticsduckdb.OpenProjectMaterializeRuntime(ctx, analyticsduckdb.ProjectRuntimeConfig{
		Models: request.Models, Database: environment,
		CredentialResolver: f.module.credentials,
		ConnectionResolver: connectionResolver,
		QueryCache:         cacheScope, ResultLimits: request.ResultLimits,
		SnapshotID: request.SnapshotID, ServingStateID: request.ServingStateID,
		ProjectID: request.ProjectID, Environment: request.Environment,
		SemanticDigest: request.SemanticDigest, ArtifactDigest: request.ArtifactDigest,
		SourceDataDigest: request.SourceDataDigest,
		CandidateID:      request.CandidateID, AuthorizationFingerprint: request.AuthorizationFingerprint,
		BindingFingerprint:  request.BindingFingerprint,
		QueryCachePartition: cachePartition,
		DependencyEvidence:  request.DependencyEvidence,
		RequiredExtensions:  request.RequiredExtensions,
		SkipInitialRefresh:  request.SkipInitialRefresh,
	})
	if err != nil {
		_ = cacheScope.Close()
		return nil, err
	}
	return runtime, nil
}

func projectRuntimeCacheIdentity(
	request analyticsruntime.ProjectRequest,
) string {
	if request.CandidateID == "" {
		return request.ServingStateID
	}
	return "candidate\x00" + request.CandidateID + "\x00" +
		request.ServingStateID
}
