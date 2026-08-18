package runtimehost

import (
	"context"
	"testing"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

// lifecycleFactory is intentionally tiny, but production sealed registries
// require the distinct PrepareSealed capability. Keep the fixture's runtime
// behavior identical while exercising that boundary.
func (f *lifecycleFactory) PrepareSealed(ctx context.Context, input RuntimeInput) (PreparedRuntime, error) {
	return f.Prepare(ctx, input)
}

func TestSealedBaseCandidateAllowsZeroDuckLakeSnapshotID(t *testing.T) {
	now := time.Now().UTC()
	state := servingstate.State{
		ID: "generation_sealed_zero", ProjectID: "project_demo", Environment: "prod",
		Status: servingstate.StatusValidated,
		Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		// Sealed catalog generations deliberately carry no legacy DuckLake
		// snapshot number. Their immutable identity is the serving state digest
		// and the sealed runtime evidence, not this historical field.
		DuckLakeSnapshotID: 0,
	}
	repo := &lifecycleRepo{state: state, artifact: servingstate.Artifact{ID: "artifact_sealed_zero", ServingStateID: state.ID, Digest: state.Digest}}
	registry := NewRegistryWithFactory(RegistryOptions{
		Repo: repo, ProjectID: projectgraph.ResourceID("project_demo"), Environment: "prod",
		Factory: &lifecycleFactory{}, ManagedData: &candidateResolver{lifetime: &candidateManagedData{}},
		Authorization: &lifecycleAuth{}, Now: func() time.Time { return now }, RequireSealedCatalog: true,
	})
	defer registry.Close()
	registration := candidateRegistration(now.Add(time.Hour))
	registration.Compatibility.DataRevision = "sealed:base-catalog"
	if err := registry.PrepareAndRegisterCandidate(t.Context(), CandidatePreparation{
		Registration: registration,
		Identity:     projectgraph.ServingIdentity{ProjectID: "project_demo", Environment: "prod", GenerationID: string(state.ID)},
	}); err != nil {
		t.Fatalf("sealed-base candidate with zero snapshot ID: %v", err)
	}
}
