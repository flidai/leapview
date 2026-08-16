package runtimefactory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	dashboardruntimefactory "github.com/flidai/leapview/internal/dashboard/runtimefactory"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type FactoryConfig struct {
	DuckDBDir        string
	RuntimeDir       string
	DashboardRuntime dashboardruntimefactory.Builder
}

type servingStateRuntimeFactory struct {
	duckDBDir        string
	runtimeDir       string
	dashboardRuntime dashboardruntimefactory.Builder
}

func NewFactory(config FactoryConfig) runtimehost.RuntimeFactory {
	return servingStateRuntimeFactory{
		duckDBDir: config.DuckDBDir, runtimeDir: config.RuntimeDir, dashboardRuntime: config.DashboardRuntime,
	}
}

func (f servingStateRuntimeFactory) Prepare(ctx context.Context, input runtimehost.RuntimeInput) (runtimehost.PreparedRuntime, error) {
	duckDBDir := runtimeFirstNonEmpty(input.DuckDBDir, f.duckDBDir)
	runtimeDir := runtimeFirstNonEmpty(input.RuntimeDir, f.runtimeDir)
	targetDir := filepath.Join(
		runtimeDir,
		runtimeExtractionIdentity(input)+"-"+shortDigest(input.Artifact.Digest),
	)
	if err := os.RemoveAll(targetDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return nil, err
	}
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
	if err := bindManagedDataRoots(compiled.Manifest, input.ManagedData.Roots); err != nil {
		return nil, err
	}
	identity, err := projectgraph.NewServingIdentity(input.State.ProjectID, string(servingstate.NormalizeEnvironment(input.State.Environment)), string(input.State.ID))
	if err != nil {
		return nil, err
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
	if f.dashboardRuntime == nil {
		return nil, fmt.Errorf("dashboard runtime builder is required")
	}
	runtimeInput := dashboardruntimefactory.Input{
		Directory: duckDir, SnapshotID: input.State.DuckLakeSnapshotID,
		Identity: identity, SemanticModelDigest: input.State.Digest,
		ArtifactDigest: input.Artifact.Digest, SourceDataDigest: input.ManagedData.RevisionID,
		Definition: projectartifact.DashboardProjection(compiled.Manifest),
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
	authoredSources, err := projectartifact.AuthoredDashboardSourcesChecked(compiled.Manifest)
	if err != nil {
		_ = service.Close()
		return nil, fmt.Errorf("authored dashboard sources: %w", err)
	}
	return dashboardRuntimeWithGraph{
		Service: service, projectID: input.State.ProjectID,
		servingStateID:  string(input.State.ID),
		authorization:   authorization,
		authoredSources: authoredSources,
	}, nil
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
