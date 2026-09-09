package module

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsduckdb "github.com/flidai/leapview/internal/analytics/duckdb"
	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type projectRuntimeFactory struct {
	module      *Module
	environment *analyticsducklake.Environment
}

// ModelRequiresSemanticAccess exposes the existing analytics-owned selection
// predicate to composition without making the application import query
// internals or create a second policy detector.
func ModelRequiresSemanticAccess(model *semanticmodel.Model) bool {
	return semanticquery.ModelRequiresSemanticAccess(model)
}

// SetSemanticAccessAuthority is composition-only and must precede opening
// project runtimes. Access retains ownership of principal and control state.
func (m *Module) SetSemanticAccessAuthority(authority materialize.SemanticAccessAuthority) {
	m.semanticAccessAuthority = authority
}

// SetSemanticAudit supplies the Access-owned canonical audit sink and the
// authenticated actor resolver for protected production reads. The exact
// request serving identity is bound when a project runtime is opened; callers
// cannot provide an independent identity through this setter.
func (m *Module) SetSemanticAudit(instanceID string, recorder access.CanonicalAuditRecorder, actorFromContext func(context.Context) (string, error)) {
	m.semanticAuditInstanceID = instanceID
	m.semanticAuditRecorder = recorder
	m.semanticAuditActorFromContext = actorFromContext
}

// SetCandidateSemanticRegistry supplies only definition authority for
// candidate compilation. It does not configure principal resolution or
// production semantic access activation.
func (m *Module) SetCandidateSemanticRegistry(instanceID string, reader access.SemanticRegistryReader) {
	m.candidateSemanticInstanceID, m.candidateSemanticRegistry = instanceID, reader
}

func (m *Module) candidateSemanticCompileContext(ctx context.Context, request analyticsruntime.ProjectRequest) (*semanticquery.SemanticAccessCompileContext, error) {
	if request.CandidateID == "" {
		return nil, fmt.Errorf("candidate identity is required")
	}
	if request.CandidateSemanticRegistry == nil || m.candidateSemanticRegistry == nil || m.candidateSemanticInstanceID == "" {
		return nil, fmt.Errorf("candidate semantic registry evidence is unavailable")
	}
	expected := request.CandidateSemanticRegistry
	if err := expected.Validate(m.candidateSemanticInstanceID, request.ProjectID); err != nil {
		return nil, err
	}
	current, err := m.candidateSemanticRegistry.ReadSemanticRegistry(ctx, m.candidateSemanticInstanceID)
	if err != nil {
		return nil, err
	}
	if err := current.Validate(m.candidateSemanticInstanceID, request.ProjectID); err != nil {
		return nil, err
	}
	if !expected.Equal(current) {
		return nil, fmt.Errorf("candidate semantic registry evidence is stale")
	}
	return &semanticquery.SemanticAccessCompileContext{Registry: current.Registry}, nil
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
	queryResultCache, err := f.module.cache.OpenSharedScope(resultcache.ScopeID{
		RuntimeID: projectResultCacheIdentity(partition),
	})
	if err != nil {
		return nil, err
	}
	immutableByteCache, err := f.module.cache.OpenScope(resultcache.ScopeID{
		RuntimeID: projectRuntimeCacheIdentity(request),
	})
	if err != nil {
		_ = queryResultCache.Close()
		return nil, err
	}
	var semanticContext *semanticquery.SemanticAccessCompileContext
	var semanticAudit *materialize.SemanticAuditConfig
	protected := false
	for _, model := range request.Models {
		protected = protected || semanticquery.ModelRequiresSemanticAccess(model)
	}
	if protected && request.CandidateID != "" {
		semanticContext, err = f.module.candidateSemanticCompileContext(ctx, request)
		if err != nil {
			_ = queryResultCache.Close()
			_ = immutableByteCache.Close()
			return nil, err
		}
	} else if protected && f.module.semanticAccessAuthority != nil {
		registry, readErr := f.module.semanticAccessAuthority.SemanticAttributeRegistry(ctx)
		if readErr != nil {
			_ = queryResultCache.Close()
			_ = immutableByteCache.Close()
			return nil, fmt.Errorf("semantic access activation registry: %w", readErr)
		}
		semanticContext = &semanticquery.SemanticAccessCompileContext{Registry: registry}
	}
	if protected && request.CandidateID == "" {
		semanticAudit = &materialize.SemanticAuditConfig{
			InstanceID: f.module.semanticAuditInstanceID,
			Identity: projectgraph.ServingIdentity{
				ProjectID: request.ProjectID, Environment: request.Environment, GenerationID: request.ServingStateID,
			},
			Recorder: f.module.semanticAuditRecorder, ActorFromContext: f.module.semanticAuditActorFromContext,
		}
	}
	runtime, err := analyticsduckdb.OpenProjectMaterializeRuntime(ctx, analyticsduckdb.ProjectRuntimeConfig{
		SemanticAccessAuthority:      f.module.semanticAccessAuthority,
		SemanticAccessCompileContext: semanticContext,
		SemanticAudit:                semanticAudit,
		Models:                       request.Models, Database: environment,
		CredentialResolver: f.module.credentials,
		ConnectionResolver: connectionResolver,
		ResultPartition:    partition, QueryResultCache: queryResultCache,
		ImmutableByteCache: immutableByteCache, ResultLimits: request.ResultLimits,
		SnapshotID: request.SnapshotID, ServingStateID: request.ServingStateID,
		ProjectID: request.ProjectID, Environment: request.Environment,
		SemanticDigest: request.SemanticDigest, ArtifactDigest: request.ArtifactDigest,
		SourceDataDigest:   request.SourceDataDigest,
		DependencyEvidence: request.DependencyEvidence,
		RequiredExtensions: request.RequiredExtensions,
		SkipInitialRefresh: request.SkipInitialRefresh,
	})
	if err != nil {
		_ = queryResultCache.Close()
		_ = immutableByteCache.Close()
		return nil, err
	}
	if protected && request.CandidateID != "" {
		if _, err := f.module.candidateSemanticCompileContext(ctx, request); err != nil {
			_ = runtime.Close()
			_ = queryResultCache.Close()
			_ = immutableByteCache.Close()
			return nil, err
		}
	}
	return runtime, nil
}

func projectResultPartition(request analyticsruntime.ProjectRequest) (resultidentity.Partition, error) {
	kind := resultidentity.PartitionProduction
	if request.CandidateID != "" {
		kind = resultidentity.PartitionCandidate
	}
	partition, err := resultidentity.NewPartition(resultidentity.PartitionInput{
		Kind: kind, ProjectID: request.ProjectID, Environment: request.Environment,
		CandidateID: request.CandidateID,
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
