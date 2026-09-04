package runtimefactory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
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

// AuthorizationSnapshotProjector replaces an artifact compatibility snapshot
// with the live instance control projection for an active runtime. Candidate
// preparation deliberately skips this callback because candidate identities
// are private and may not yet be activated in the live control store.
type AuthorizationSnapshotProjector func(context.Context, accesssnapshot.AuthorizationSnapshot, projectgraph.ServingIdentity, projectgraph.ProjectGraph) (accesssnapshot.AuthorizationSnapshot, error)

type FactoryConfig struct {
	DuckDBDir                      string
	RuntimeDir                     string
	DashboardRuntime               dashboardruntimefactory.Builder
	SealedLeaseHolder              string
	ActivationEvidence             ActivationEvidenceSource
	AuthorizationSnapshotProjector AuthorizationSnapshotProjector
}

type servingStateRuntimeFactory struct {
	duckDBDir                      string
	runtimeDir                     string
	dashboardRuntime               dashboardruntimefactory.Builder
	activationEvidence             ActivationEvidenceSource
	authorizationSnapshotProjector AuthorizationSnapshotProjector
}

func NewFactory(config FactoryConfig) runtimehost.RuntimeFactory {
	return servingStateRuntimeFactory{
		duckDBDir: config.DuckDBDir, runtimeDir: config.RuntimeDir, dashboardRuntime: config.DashboardRuntime,
		activationEvidence: config.ActivationEvidence, authorizationSnapshotProjector: config.AuthorizationSnapshotProjector,
	}
}

// RebuildOnSameGeneration refreshes the mutable live authorization projection
// when a generation is prepared again after its first activation.
func (f servingStateRuntimeFactory) RebuildOnSameGeneration() bool {
	return f.authorizationSnapshotProjector != nil
}

func (f servingStateRuntimeFactory) Prepare(ctx context.Context, input runtimehost.RuntimeInput) (runtimehost.PreparedRuntime, error) {
	duckDBDir := runtimeFirstNonEmpty(input.DuckDBDir, f.duckDBDir)
	targetDir, ownedExtraction, err := f.prepareExtractionDirectory(input)
	if err != nil {
		return nil, err
	}
	keepExtraction := false
	defer func() {
		if ownedExtraction && !keepExtraction {
			_ = os.RemoveAll(targetDir)
		}
	}()
	if err := projectbundle.ExtractArtifact(input.Artifact.Path, targetDir); err != nil {
		return nil, err
	}
	duckDir := filepath.Join(duckDBDir, string(servingstate.NormalizeEnvironment(input.State.Environment)))
	compiled, _, err := projectbundle.LoadCompiledProjectArtifact(targetDir)
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
	dependencyEvidence, _ := dependencyEvidenceForRuntime(ctx, identity, compiled, compiledProject, input.ManagedData, input.Candidate, f.activationEvidence)
	policy := projectmanifest.AccessPolicy{}
	if value := input.State.AccessPolicyJSON; value != "" {
		if err := json.Unmarshal([]byte(value), &policy); err != nil {
			return nil, fmt.Errorf("decode serving authorization policy: %w", err)
		}
	}
	compatibility, err := projectmanifest.CompileAuthorizationSnapshot(identity, compiled.Graph, policy)
	if err != nil {
		return nil, fmt.Errorf("compile serving authorization snapshot: %w", err)
	}
	authorization, err := f.projectAuthorization(ctx, input, identity, compiled.Graph, compatibility)
	if err != nil {
		return nil, err
	}
	if f.dashboardRuntime == nil {
		return nil, fmt.Errorf("dashboard runtime builder is required")
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
	runtimeInput := dashboardruntimefactory.Input{
		Directory: duckDir, SnapshotID: input.State.DuckLakeSnapshotID,
		Identity: identity, SemanticModelDigest: input.State.Digest,
		ArtifactDigest: input.Artifact.Digest, SourceDataDigest: input.ManagedData.RevisionID,
		Definition: projectDefinition, DependencyEvidence: dependencyEvidence,
	}
	if input.Candidate != nil {
		runtimeInput.CandidateID = input.Candidate.CandidateID
		runtimeInput.AuthorizationFingerprint = input.Candidate.AuthorizationFingerprint
		runtimeInput.BindingFingerprint = input.Candidate.BindingFingerprint
	}
	service, err := f.dashboardRuntime(ctx, runtimeInput)
	if err != nil {
		return nil, err
	}
	if input.State.DuckLakeSnapshotID == 0 {
		snapshotID := service.DuckLakeSnapshotID()
		if snapshotID > 0 {
			if err := service.Close(); err != nil {
				return nil, err
			}
			runtimeInput.SnapshotID = snapshotID
			service, err = f.dashboardRuntime(ctx, runtimeInput)
			if err != nil {
				return nil, err
			}
		}
	}
	authoredSources, err := authoredDashboardSources(compiled.Manifest, input.State.ProjectID)
	if err != nil {
		_ = service.Close()
		return nil, fmt.Errorf("authored dashboard sources: %w", err)
	}
	runtime := &dashboardRuntimeWithGraph{
		Service: service, projectID: input.State.ProjectID,
		servingStateID: string(input.State.ID),
		authorization:  authorization, authorizationEvidence: compatibility,
		authoredSources: authoredSources,
		projectManifest: compiled.Manifest,
	}
	if ownedExtraction {
		runtime.closeState = &runtimeCloseState{extractionDir: targetDir}
	}
	keepExtraction = true
	return runtime, nil
}

// prepareDashboard is the common sealed path project-artifact loader. The
// catalog environment is supplied by the caller after durable lease/fence
// acquisition; this helper never opens or writes a DuckLake catalog itself.
func (f servingStateRuntimeFactory) prepareDashboard(ctx context.Context, input runtimehost.RuntimeInput, builder SealedDashboardRuntimeBuilder, environment *ducklake.Environment) (*dashboardRuntimeWithGraph, error) {
	if builder == nil || environment == nil {
		return nil, fmt.Errorf("sealed dashboard builder and environment are required")
	}
	targetDir, ownedExtraction, err := f.prepareExtractionDirectory(input)
	if err != nil {
		return nil, err
	}
	keepExtraction := false
	defer func() {
		if ownedExtraction && !keepExtraction {
			_ = os.RemoveAll(targetDir)
		}
	}()
	if err := projectbundle.ExtractArtifact(input.Artifact.Path, targetDir); err != nil {
		return nil, err
	}
	compiled, _, err := projectbundle.LoadCompiledProjectArtifact(targetDir)
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
	dependencyEvidence, _ := dependencyEvidenceForRuntime(ctx, identity, compiled, compiledProject, input.ManagedData, input.Candidate, f.activationEvidence)
	policy := projectmanifest.AccessPolicy{}
	if value := input.State.AccessPolicyJSON; value != "" {
		if err := json.Unmarshal([]byte(value), &policy); err != nil {
			return nil, fmt.Errorf("decode serving authorization policy: %w", err)
		}
	}
	compatibility, err := projectmanifest.CompileAuthorizationSnapshot(identity, compiled.Graph, policy)
	if err != nil {
		return nil, fmt.Errorf("compile serving authorization snapshot: %w", err)
	}
	authorization, err := f.projectAuthorization(ctx, input, identity, compiled.Graph, compatibility)
	if err != nil {
		return nil, err
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
		SkipInitialRefresh: true,
		Definition:         projectDefinition, DependencyEvidence: dependencyEvidence,
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
	runtime := &dashboardRuntimeWithGraph{Service: service, projectID: input.State.ProjectID, servingStateID: string(input.State.ID), authorization: authorization, authorizationEvidence: compatibility, authoredSources: authoredSources, projectManifest: compiled.Manifest}
	if ownedExtraction {
		runtime.closeState = &runtimeCloseState{extractionDir: targetDir}
	}
	keepExtraction = true
	return runtime, nil
}

func (f servingStateRuntimeFactory) prepareExtractionDirectory(input runtimehost.RuntimeInput) (string, bool, error) {
	runtimeDir := runtimeFirstNonEmpty(input.RuntimeDir, f.runtimeDir)
	base := runtimeExtractionIdentity(input) + "-" + shortDigest(input.Artifact.Digest)
	if input.Candidate == nil && f.authorizationSnapshotProjector != nil {
		if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
			return "", false, err
		}
		targetDir, err := os.MkdirTemp(runtimeDir, base+"-")
		return targetDir, true, err
	}
	targetDir := filepath.Join(runtimeDir, base)
	if err := os.RemoveAll(targetDir); err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", false, err
	}
	return targetDir, false, nil
}

func (f servingStateRuntimeFactory) projectAuthorization(ctx context.Context, input runtimehost.RuntimeInput, identity projectgraph.ServingIdentity, project projectgraph.ProjectGraph, compatibility accesssnapshot.AuthorizationSnapshot) (accesssnapshot.AuthorizationSnapshot, error) {
	if input.Candidate != nil || f.authorizationSnapshotProjector == nil {
		return compatibility, nil
	}
	projected, err := f.authorizationSnapshotProjector(ctx, compatibility, identity, project)
	if err != nil {
		return accesssnapshot.AuthorizationSnapshot{}, fmt.Errorf("project live serving authorization snapshot: %w", err)
	}
	if err := projected.Validate(project); err != nil {
		return accesssnapshot.AuthorizationSnapshot{}, fmt.Errorf("validate live serving authorization snapshot: %w", err)
	}
	if projected.Identity() != identity {
		return accesssnapshot.AuthorizationSnapshot{}, fmt.Errorf("live serving authorization snapshot identity does not match serving identity")
	}
	return projected, nil
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
