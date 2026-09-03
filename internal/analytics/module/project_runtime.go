package module

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

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
	partition, err := projectResultPartition(request)
	if err != nil {
		return nil, err
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
	runtimeIdentity := projectRuntimeCacheIdentity(request)
	queryResultCache, err := f.module.cache.OpenScope(resultcache.ScopeID{
		RuntimeID: runtimeIdentity + "\x00results", PartitionID: analyticscache.PartitionIdentity(partition),
	})
	if err != nil {
		return nil, err
	}
	immutableByteCache, err := f.module.cache.OpenScope(resultcache.ScopeID{
		RuntimeID: runtimeIdentity + "\x00bytes", PartitionID: "runtime-bytes:" + runtimeIdentity,
	})
	if err != nil {
		_ = queryResultCache.Close()
		return nil, err
	}
	runtime, err := analyticsduckdb.OpenProjectMaterializeRuntime(ctx, analyticsduckdb.ProjectRuntimeConfig{
		Models: request.Models, Database: environment,
		CredentialResolver: f.module.credentials,
		ConnectionResolver: connectionResolver,
		ResultPartition:    partition, QueryResultCache: queryResultCache,
		ImmutableByteCache: immutableByteCache, ResultLimits: request.ResultLimits,
		SnapshotID: request.SnapshotID, ServingStateID: request.ServingStateID,
		ProjectID: request.ProjectID, Environment: request.Environment,
		TargetType: "deployment", TargetID: request.TargetID,
		SemanticDigest: request.SemanticDigest, ArtifactDigest: request.ArtifactDigest,
		SourceDataDigest:   request.SourceDataDigest,
		RelationNamespace:  request.RelationNamespace,
		DependencyEvidence: request.DependencyEvidence,
		RequiredExtensions: request.RequiredExtensions,
		SkipInitialRefresh: request.SkipInitialRefresh,
	})
	if err != nil {
		_ = queryResultCache.Close()
		_ = immutableByteCache.Close()
		return nil, err
	}
	return runtime, nil
}

func projectResultPartition(request analyticsruntime.ProjectRequest) (resultidentity.Partition, error) {
	targetID := strings.TrimSpace(request.TargetID)
	if targetID == "" || targetID != request.TargetID {
		return resultidentity.Partition{}, fmt.Errorf("query result cache partition: target ID must be non-empty and canonical")
	}
	kind := resultidentity.PartitionProduction
	if request.CandidateID != "" {
		kind = resultidentity.PartitionCandidate
	}
	partition, err := resultidentity.NewPartition(resultidentity.PartitionInput{
		Kind: kind, ProjectID: request.ProjectID, Environment: request.Environment,
		CandidateID: request.CandidateID, TargetID: targetID,
	})
	if err != nil {
		return resultidentity.Partition{}, fmt.Errorf("query result cache partition: %w", err)
	}
	return partition, nil
}

func projectResultCacheIdentity(partition resultidentity.Partition) string {
	return "result-partition:" + base64.RawURLEncoding.EncodeToString(partition.Canonical())
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
