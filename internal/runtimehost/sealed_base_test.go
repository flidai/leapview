package runtimehost

import (
	"context"
	"errors"
	"testing"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type pinnedLifecycleFactory struct {
	lifecycleFactory
	reached              chan struct{}
	activationCandidates chan string
}

func (f *pinnedLifecycleFactory) PinnedSnapshotSealed() {}

func (f *pinnedLifecycleFactory) PrepareSealed(ctx context.Context, input RuntimeInput) (PreparedRuntime, error) {
	select {
	case f.reached <- struct{}{}:
	default:
	}
	if f.activationCandidates != nil && input.SealedActivationCandidate != nil {
		f.activationCandidates <- input.SealedActivationCandidate.CandidateID
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

func TestPrepareSealedActivationUsesExactCandidateAndRemainsActivatable(t *testing.T) {
	state := servingstate.State{
		ID: "generation_activation_candidate", ProjectID: "project_demo", Environment: "prod", Status: servingstate.StatusValidated,
		Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	repo := &lifecycleRepo{state: state, artifact: servingstate.Artifact{ID: "artifact_activation_candidate", ServingStateID: state.ID, Digest: state.Digest}}
	factory := &pinnedLifecycleFactory{activationCandidates: make(chan string, 1)}
	authorization := &lifecycleAuth{}
	m := NewManagerWithFactory(ManagerOptions{Repo: repo, ProjectID: projectgraph.ResourceID("project_demo"), Environment: "prod", Factory: factory, Authorization: authorization, RequireSealedCatalog: true})
	defer m.Close()
	prepared, err := m.PrepareSealedActivation(context.Background(), string(state.ID), "candidate_exact")
	if err != nil {
		t.Fatal(err)
	}
	activated := false
	if err := m.ActivatePrepared(prepared, func() error {
		activated = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case candidateID := <-factory.activationCandidates:
		if candidateID != "candidate_exact" {
			t.Fatalf("sealed activation candidate=%q, want candidate_exact", candidateID)
		}
	default:
		t.Fatal("sealed activation candidate was not passed to factory")
	}
	if !activated {
		t.Fatal("sealed activation callback was not invoked")
	}
	if authorization.generation != state.ID {
		t.Fatalf("authorization generation=%q, want %q", authorization.generation, state.ID)
	}
}

func TestReconcileSealedDoesNotUseNoChangeFastPath(t *testing.T) {
	state := servingstate.State{
		ID: "generation_active_only", ProjectID: "project_demo", Environment: "prod", Status: servingstate.StatusValidated,
		Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	repo := &lifecycleRepo{state: state, artifact: servingstate.Artifact{ID: "artifact_active_only", ServingStateID: state.ID, Digest: state.Digest}}
	factory := &pinnedLifecycleFactory{}
	m := NewManagerWithFactory(ManagerOptions{Repo: repo, ProjectID: projectgraph.ResourceID("project_demo"), Environment: "prod", Factory: factory, Authorization: &lifecycleAuth{}, RequireSealedCatalog: true})
	defer m.Close()
	prepared, err := m.PrepareServingState(t.Context(), string(state.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ActivatePrepared(prepared, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("sealed resolver rejected inactive pointer")
	factory.fail = wantErr
	if err := m.ReconcileSealed(t.Context(), state.ID); !errors.Is(err, wantErr) {
		t.Fatalf("ReconcileSealed() error=%v, want resolver error %v", err, wantErr)
	}
}

func TestCloseSerializesAgainstPreparedActivation(t *testing.T) {
	state := servingstate.State{
		ID: "generation_close_race", ProjectID: "project_demo", Environment: "prod", Status: servingstate.StatusValidated,
		Digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	repo := &lifecycleRepo{state: state, artifact: servingstate.Artifact{ID: "artifact_close_race", ServingStateID: state.ID, Digest: state.Digest}}
	factory := &pinnedLifecycleFactory{}
	m := NewManagerWithFactory(ManagerOptions{Repo: repo, ProjectID: projectgraph.ResourceID("project_demo"), Environment: "prod", Factory: factory, Authorization: &lifecycleAuth{}, RequireSealedCatalog: true})
	prepared, err := m.PrepareServingState(t.Context(), string(state.ID))
	if err != nil {
		t.Fatal(err)
	}
	activationStarted := make(chan struct{})
	allowActivation := make(chan struct{})
	activationDone := make(chan error, 1)
	go func() {
		activationDone <- m.ActivatePrepared(prepared, func() error {
			close(activationStarted)
			<-allowActivation
			return nil
		})
	}()
	select {
	case <-activationStarted:
	case <-time.After(time.Second):
		t.Fatal("activation did not enter durable callback")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- m.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("close returned while activation callback was blocked: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(allowActivation)
	if err := <-activationDone; err != nil {
		t.Fatalf("activation failed: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("close failed: %v", err)
	}
	select {
	case <-factory.last().closed:
	default:
		t.Fatal("activated runtime was not drained by close")
	}
}

func TestPreparedActivationAfterCloseAbortsRuntime(t *testing.T) {
	state := servingstate.State{
		ID: "generation_closed_host", ProjectID: "project_demo", Environment: "prod", Status: servingstate.StatusValidated,
		Digest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}
	repo := &lifecycleRepo{state: state, artifact: servingstate.Artifact{ID: "artifact_closed_host", ServingStateID: state.ID, Digest: state.Digest}}
	factory := &pinnedLifecycleFactory{}
	m := NewManagerWithFactory(ManagerOptions{Repo: repo, ProjectID: projectgraph.ResourceID("project_demo"), Environment: "prod", Factory: factory, Authorization: &lifecycleAuth{}, RequireSealedCatalog: true})
	prepared, err := m.PrepareServingState(t.Context(), string(state.ID))
	if err != nil {
		t.Fatal(err)
	}
	preparedRuntime := factory.last()
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	callbackCalled := false
	if err := m.ActivatePrepared(prepared, func() error {
		callbackCalled = true
		return nil
	}); err == nil || err.Error() != "runtime host is closed" {
		t.Fatalf("activation after close error=%v, want runtime host is closed", err)
	}
	if callbackCalled {
		t.Fatal("durable callback ran after runtime host close")
	}
	select {
	case <-preparedRuntime.closed:
	default:
		t.Fatal("prepared runtime was not aborted after closed-host activation")
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
