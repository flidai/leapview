package runtimefactory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	dashboardruntimefactory "github.com/flidai/leapview/internal/dashboard/runtimefactory"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type FactoryConfig struct {
	DuckDBDir          string
	RuntimeDir         string
	SealedLeaseHolder  string
	ActivationEvidence ActivationEvidenceSource
}

type servingStateRuntimeFactory struct {
	duckDBDir          string
	runtimeDir         string
	activationEvidence ActivationEvidenceSource
	servingArtifacts   ServingArtifactReader
}

// ServingArtifactReader is the least-privilege object capability needed by a
// native runtime. PostgreSQL serving-state rows retain a provider-neutral
// immutable locator rather than a process-local filesystem path.
type ServingArtifactReader = projectbundle.ArtifactObjectReader

// prepareDashboard is the common sealed path project-artifact loader. The
// catalog environment is supplied by the caller after durable lease/fence
// acquisition; this helper never opens or writes a DuckLake catalog itself.
func (f servingStateRuntimeFactory) prepareDashboard(ctx context.Context, input runtimehost.RuntimeInput, builder SealedDashboardRuntimeBuilder, environment *ducklake.Environment, relationNamespace, targetID, snapshotSealID string) (*dashboardRuntimeWithGraph, error) {
	if builder == nil || environment == nil {
		return nil, fmt.Errorf("sealed dashboard builder and environment are required")
	}
	// PostgreSQL serving roots carry the exact candidate schema selected by
	// durable delivery state. Never allow the downstream runtime to fall back
	// to the legacy model schema for this path.
	if environment.IsPostgresCatalog() {
		if relationNamespace == "" || relationNamespace != strings.TrimSpace(relationNamespace) {
			return nil, fmt.Errorf("%w: PostgreSQL relation namespace is unavailable", ErrSealedRootUnavailable)
		}
		if err := ducklake.ValidateRelationNamespace(relationNamespace); err != nil {
			return nil, fmt.Errorf("%w: PostgreSQL relation namespace: %v", ErrSealedRootUnavailable, err)
		}
	}
	runtimeDir := runtimeFirstNonEmpty(input.RuntimeDir, f.runtimeDir)
	targetDir := filepath.Join(runtimeDir, runtimeExtractionIdentity(input)+"-"+shortDigest(input.Artifact.Digest))
	if err := os.RemoveAll(targetDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return nil, err
	}
	compiled, err := f.loadCompiledProjectArtifact(ctx, input.Artifact, targetDir)
	if err != nil {
		return nil, err
	}
	if compiled.ProjectID != input.State.ProjectID {
		return nil, fmt.Errorf("compiled artifact project = %q, want %q", compiled.ProjectID, input.State.ProjectID)
	}
	compiledProject, err := projectartifact.NewProject(compiled.Graph, compiled.Manifest)
	if err != nil {
		return nil, fmt.Errorf("compiled project dependency evidence: %w", err)
	}
	if err := bindManagedDataRoots(&compiled.Manifest, input.ManagedData.Roots); err != nil {
		return nil, err
	}
	identity, err := projectgraph.NewServingIdentity(input.State.ProjectID, string(servingstate.NormalizeEnvironment(input.State.Environment)), string(input.State.ID))
	if err != nil {
		return nil, err
	}
	dependencyEvidence, err := dependencyEvidenceForRuntime(ctx, identity, compiled, compiledProject, input.ManagedData, input.Candidate, f.activationEvidence)
	if err != nil {
		return nil, fmt.Errorf("resolve runtime dependency evidence: %w", err)
	}
	policy := projectmanifest.AccessPolicy{}
	if value := input.State.AccessPolicyJSON; value != "" {
		if err := json.Unmarshal([]byte(value), &policy); err != nil {
			return nil, fmt.Errorf("decode serving authorization policy: %w", err)
		}
	}
	authorization, err := projectmanifest.CompileAuthorizationSnapshot(identity, compiled.Graph, policy)
	if err != nil {
		return nil, fmt.Errorf("compile serving authorization snapshot: %w", err)
	}
	models := make(map[projectgraph.ResourceID]*semanticmodel.Model, len(compiled.Manifest.SemanticModels))
	for id, model := range compiled.Manifest.SemanticModels {
		resourceID, err := projectgraph.NewResourceID(id)
		if err != nil {
			return nil, fmt.Errorf("semantic model %q: %w", id, err)
		}
		models[resourceID] = model
	}
	dashboards := make(map[projectgraph.ResourceID]dashboarddefinition.Definition, len(compiled.Manifest.DashboardDefinitions))
	for id, definition := range compiled.Manifest.DashboardDefinitions {
		resourceID, err := projectgraph.NewResourceID(id)
		if err != nil {
			return nil, fmt.Errorf("dashboard %q: %w", id, err)
		}
		dashboards[resourceID] = definition
	}
	projectDefinition, err := dashboardruntime.NewTargetBoundProjectDefinition(input.State.ProjectID, compiled.Manifest.Title, compiled.Manifest.Description, models, dashboards)
	if err != nil {
		return nil, fmt.Errorf("dashboard project definition: %w", err)
	}
	authoredSources, err := authoredDashboardSources(compiled.Manifest, input.State.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("authored dashboard sources: %w", err)
	}
	runtimeInput := dashboardruntimefactory.Input{
		Directory: targetDir, Identity: identity, SemanticModelDigest: input.State.Digest,
		ArtifactDigest: input.Artifact.Digest, SourceDataDigest: input.ManagedData.RevisionID,
		TargetID: targetID, SnapshotSealID: snapshotSealID,
		SkipInitialRefresh: true,
		Definition:         projectDefinition, DependencyEvidence: dependencyEvidence,
	}
	// PostgreSQL-backed serving environments pin an exact snapshot at ATTACH;
	// propagate that identity into the dashboard runtime so it cannot fall back
	// to the catalog's moving head. Legacy sealed file readers retain their
	// existing state-driven behavior.
	if environment.IsPostgresCatalog() {
		runtimeInput.SnapshotID = environment.PostgresSnapshotVersion()
		runtimeInput.RelationNamespace = relationNamespace
	}
	if input.Candidate != nil {
		runtimeInput.CandidateID = input.Candidate.CandidateID
		runtimeInput.AuthorizationFingerprint = input.Candidate.AuthorizationFingerprint
		runtimeInput.BindingFingerprint = input.Candidate.BindingFingerprint
	}
	service, err := builder(ctx, runtimeInput, environment)
	if err != nil {
		return nil, err
	}
	return &dashboardRuntimeWithGraph{Service: service, projectID: input.State.ProjectID, servingStateID: string(input.State.ID), authorization: authorization, authoredSources: authoredSources, projectManifest: compiled.Manifest}, nil
}

func (f servingStateRuntimeFactory) loadCompiledProjectArtifact(ctx context.Context, artifact servingstate.Artifact, targetDir string) (projectbundle.CompiledProjectArtifact, error) {
	return (projectbundle.ServingArtifactLoader{Objects: f.servingArtifacts}).LoadCompiled(ctx, artifact, targetDir)
}

func runtimeExtractionIdentity(input runtimehost.RuntimeInput) string {
	stateID := string(input.State.ID)
	if input.Candidate == nil {
		return stateID
	}
	sum := sha256.Sum256(
		[]byte(input.Candidate.CandidateID + "\x00" + stateID),
	)
	return "candidate-" + hex.EncodeToString(sum[:8]) + "-" + stateID
}

func runtimeFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func shortDigest(value string) string {
	if len(value) > 12 {
		return value[:12]
	}
	return value
}
