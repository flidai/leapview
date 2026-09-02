package runtimehost

import (
	"context"
	"testing"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type pinnedLifecycleFactory struct {
	lifecycleFactory
	reached chan struct{}
}

func (f *pinnedLifecycleFactory) PinnedSnapshotSealed() {}

func (f *pinnedLifecycleFactory) PrepareSealed(ctx context.Context, input RuntimeInput) (PreparedRuntime, error) {
	select {
	case f.reached <- struct{}{}:
	default:
	}
	return f.Prepare(ctx, input)
}

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

func TestPinnedSnapshotSealedFactoryReachesExactSnapshot(t *testing.T) {
	state := servingstate.State{
		ID: "generation_pinned", ProjectID: "project_demo", Environment: "prod", Status: servingstate.StatusValidated,
		Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", DuckLakeSnapshotID: 42,
	}
	repo := &lifecycleRepo{state: state, artifact: servingstate.Artifact{ID: "artifact_pinned", ServingStateID: state.ID, Digest: state.Digest}}
	factory := &pinnedLifecycleFactory{reached: make(chan struct{}, 1)}
	m := NewManagerWithFactory(ManagerOptions{Repo: repo, ProjectID: projectgraph.ResourceID("project_demo"), Environment: "prod", Factory: factory, RequireSealedCatalog: true})
	defer m.Close()
	prepared, err := m.PrepareServingState(context.Background(), string(state.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	select {
	case <-factory.reached:
	case <-time.After(time.Second):
		t.Fatal("pinned sealed factory was not reached")
	}
}

func TestSealedRefreshCandidateAllowsPositiveSnapshot(t *testing.T) {
	now := time.Now().UTC()
	state := servingstate.State{
		ID: "generation_sealed_refresh", ProjectID: "project_demo", Environment: "prod",
		Status:             servingstate.StatusValidated,
		Digest:             "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		DuckLakeSnapshotID: 42,
	}
	repo := &lifecycleRepo{state: state, artifact: servingstate.Artifact{ID: "artifact_sealed_refresh", ServingStateID: state.ID, Digest: state.Digest}}
	factory := &pinnedLifecycleFactory{reached: make(chan struct{}, 1)}
	registry := NewRegistryWithFactory(RegistryOptions{
		Repo: repo, ProjectID: projectgraph.ResourceID("project_demo"), Environment: "prod",
		Factory: factory, ManagedData: &candidateResolver{lifetime: &candidateManagedData{}},
		Authorization: &lifecycleAuth{}, Now: func() time.Time { return now }, RequireSealedCatalog: true,
	})
	defer registry.Close()
	registration := candidateRegistration(now.Add(time.Hour))
	registration.Compatibility.DataMode = CandidateDataRefreshSources
	registration.Compatibility.DataRevision = "sources:managed"
	if err := registry.PrepareAndRegisterCandidate(t.Context(), CandidatePreparation{
		Registration: registration,
		Identity:     projectgraph.ServingIdentity{ProjectID: "project_demo", Environment: "prod", GenerationID: string(state.ID)},
	}); err != nil {
		t.Fatalf("sealed refresh candidate with positive snapshot: %v", err)
	}
	select {
	case <-factory.reached:
	case <-time.After(time.Second):
		t.Fatal("sealed refresh candidate did not reach pinned sealed preparation")
	}
}

func TestLegacySealedFactoryStillRejectsPinnedSnapshot(t *testing.T) {
	state := servingstate.State{
		ID: "generation_legacy_pinned", ProjectID: "project_demo", Environment: "prod", Status: servingstate.StatusValidated,
		Digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", DuckLakeSnapshotID: 42,
	}
	repo := &lifecycleRepo{state: state, artifact: servingstate.Artifact{ID: "artifact_legacy_pinned", ServingStateID: state.ID, Digest: state.Digest}}
	m := NewManagerWithFactory(ManagerOptions{Repo: repo, ProjectID: projectgraph.ResourceID("project_demo"), Environment: "prod", Factory: &lifecycleFactory{}, RequireSealedCatalog: true})
	defer m.Close()
	if _, err := m.PrepareServingState(context.Background(), string(state.ID)); err == nil {
		t.Fatal("legacy sealed factory accepted pinned snapshot")
	}
}
