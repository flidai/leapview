package runtimehost

import (
	"context"
	"errors"
	"testing"
	"time"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

// The PostgreSQL sealed factory reports terminal renewal failures through this
// callback, then releases its lease on Close without a subsequent nil callback.
// Keep that boundary visible instead of teaching this fake to clear host health.
type leaseHealthFactory struct {
	lifecycleFactory
	callbacks            []func(error)
	prepareFailure       error
	nilRuntime           bool
	invalidAuthorization bool
}

func (*leaseHealthFactory) PinnedSnapshotSealed() {}

func (f *leaseHealthFactory) PrepareSealed(ctx context.Context, input RuntimeInput) (PreparedRuntime, error) {
	f.callbacks = append(f.callbacks, input.OnLeaseRenewalFailure)
	if f.prepareFailure != nil || f.nilRuntime {
		input.OnLeaseRenewalFailure(errors.New("preparation lease expired"))
		return nil, f.prepareFailure
	}
	runtime, err := f.Prepare(ctx, input)
	if f.invalidAuthorization && err == nil {
		input.OnLeaseRenewalFailure(errors.New("preparation lease expired"))
		runtime.(*lifecycleRuntime).authorization = accesssnapshot.AuthorizationSnapshot{}
	}
	return runtime, err
}

func leaseHealthRegistry(t *testing.T, factory *leaseHealthFactory, onDrained func(servingstate.ID, int64)) *Registry {
	t.Helper()
	state1 := servingstate.State{ID: "generation_1", ProjectID: "project_demo", Environment: "prod", Status: servingstate.StatusValidated, Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", DuckLakeSnapshotID: 42}
	state2 := state1
	state2.ID = "generation_2"
	state2.Digest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	artifact1 := servingstate.Artifact{ID: "artifact_1", ServingStateID: state1.ID, Digest: state1.Digest}
	artifact2 := servingstate.Artifact{ID: "artifact_2", ServingStateID: state2.ID, Digest: state2.Digest}
	repo := &lifecycleRepo{state: state1, artifact: artifact1, states: map[servingstate.ID]servingstate.State{state1.ID: state1, state2.ID: state2}, artifacts: map[servingstate.ID]servingstate.Artifact{state1.ID: artifact1, state2.ID: artifact2}}
	registry := NewRegistryWithFactory(RegistryOptions{Repo: repo, ProjectID: "project_demo", Environment: "prod", Factory: factory, Authorization: &lifecycleAuth{}, RequireSealedCatalog: true, OnDrained: onDrained})
	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Errorf("registry cleanup: %v", err)
		}
	})
	return registry
}

func TestSealedRuntimeLeaseHealthClearsAfterReaderDrain(t *testing.T) {
	factory := &leaseHealthFactory{}
	drained := make(chan servingstate.ID, 2)
	registry := leaseHealthRegistry(t, factory, func(id servingstate.ID, _ int64) { drained <- id })
	first, err := registry.PrepareServingState(t.Context(), "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	renewalErr := errors.New("old generation lease expired")
	factory.callbacks[0](renewalErr)
	if err := registry.ActivatePrepared(first, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	reader, err := registry.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Release()
	second, err := registry.PrepareServingState(t.Context(), "generation_2")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ActivatePrepared(second, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := registry.LeaseRenewalError(); !errors.Is(err, renewalErr) {
		t.Fatalf("health while old reader remains = %v, want %v", err, renewalErr)
	}
	reader.Release()
	select {
	case id := <-drained:
		if id != "generation_1" {
			t.Fatalf("drained generation = %s, want generation_1", id)
		}
	case <-time.After(time.Second):
		t.Fatal("old generation did not drain")
	}
	if err := registry.LeaseRenewalError(); err != nil {
		t.Fatalf("health after failed generation drained = %v, want nil", err)
	}
	factory.callbacks[0](renewalErr)
	if err := registry.LeaseRenewalError(); err != nil {
		t.Fatalf("late callback resurrected drained generation health: %v", err)
	}
}

func TestSealedRuntimeLeaseHealthIsolatesOverlappingPreparations(t *testing.T) {
	factory := &leaseHealthFactory{}
	registry := leaseHealthRegistry(t, factory, nil)
	first, err := registry.PrepareServingState(t.Context(), "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := registry.PrepareServingState(t.Context(), "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	firstErr := errors.New("first preparation lease expired")
	secondErr := errors.New("second preparation lease expired")
	factory.callbacks[0](firstErr)
	factory.callbacks[1](nil)
	if err := registry.LeaseRenewalError(); !errors.Is(err, firstErr) {
		t.Fatalf("healthy overlapping preparation erased first failure: %v", err)
	}
	factory.callbacks[1](secondErr)
	callbacksDone := make(chan struct{})
	go func() {
		for range 100 {
			factory.callbacks[0](firstErr)
			factory.callbacks[0](nil)
		}
		close(callbacksDone)
	}()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-callbacksDone:
	case <-time.After(time.Second):
		t.Fatal("concurrent first-preparation callbacks did not finish")
	}
	factory.callbacks[0](nil)
	if err := registry.LeaseRenewalError(); !errors.Is(err, secondErr) || errors.Is(err, firstErr) {
		t.Fatalf("health after first close = %v, want only second failure", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	factory.callbacks[0](firstErr)
	factory.callbacks[1](secondErr)
	if err := registry.LeaseRenewalError(); err != nil {
		t.Fatalf("late closed-preparation callback restored health: %v", err)
	}
}

func TestSealedRuntimeLeaseHealthClearsPreparationFailure(t *testing.T) {
	prepareErr := errors.New("sealed attach failed")
	for _, test := range []struct {
		name    string
		factory *leaseHealthFactory
	}{
		{name: "factory failure", factory: &leaseHealthFactory{prepareFailure: prepareErr}},
		{name: "nil runtime", factory: &leaseHealthFactory{nilRuntime: true}},
		{name: "authorization validation failure", factory: &leaseHealthFactory{invalidAuthorization: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			factory := test.factory
			registry := leaseHealthRegistry(t, factory, nil)
			prepared, err := registry.PrepareServingState(t.Context(), "generation_1")
			if prepared != nil || err == nil || factory.prepareFailure != nil && !errors.Is(err, prepareErr) {
				t.Fatalf("failed prepare = %v, %v", prepared, err)
			}
			if factory.nilRuntime && err.Error() != "runtime factory returned nil" {
				t.Fatalf("nil runtime error = %v", err)
			}
			if factory.invalidAuthorization {
				select {
				case <-factory.last().closed:
				default:
					t.Fatal("invalid runtime was not closed")
				}
			}
			if err := registry.LeaseRenewalError(); err != nil {
				t.Fatalf("failed preparation left health behind: %v", err)
			}
			factory.callbacks[0](errors.New("late failed preparation callback"))
			if err := registry.LeaseRenewalError(); err != nil {
				t.Fatalf("failed preparation late callback restored health: %v", err)
			}
		})
	}
}

func TestSealedRuntimeLeaseHealthClearsAbortedPreparation(t *testing.T) {
	factory := &leaseHealthFactory{}
	registry := leaseHealthRegistry(t, factory, nil)
	prepared, err := registry.PrepareServingState(t.Context(), "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	renewalErr := errors.New("aborted preparation lease expired")
	factory.callbacks[0](renewalErr)
	activationErr := errors.New("durable activation rejected")
	if err := registry.ActivatePrepared(prepared, func() error { return activationErr }); !errors.Is(err, activationErr) {
		t.Fatalf("activation error = %v, want %v", err, activationErr)
	}
	select {
	case <-factory.last().closed:
	default:
		t.Fatal("aborted runtime was not closed")
	}
	if err := registry.LeaseRenewalError(); err != nil {
		t.Fatalf("aborted preparation left health behind: %v", err)
	}
	factory.callbacks[0](renewalErr)
	if err := registry.LeaseRenewalError(); err != nil {
		t.Fatalf("aborted preparation late callback restored health: %v", err)
	}
}

func TestSealedRuntimeLeaseHealthClearsRetiredCandidate(t *testing.T) {
	factory := &leaseHealthFactory{}
	registry := leaseHealthRegistry(t, factory, nil)
	registration := candidateRegistration(time.Now().UTC().Add(time.Hour))
	registration.Compatibility.ManagedDataConnections = nil
	prepared, err := registry.PrepareCandidate(t.Context(), CandidatePreparation{Registration: registration, Identity: projectgraph.ServingIdentity{ProjectID: "project_demo", Environment: "prod", GenerationID: "generation_1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	renewalErr := errors.New("candidate lease expired")
	factory.callbacks[0](renewalErr)
	if err := registry.RegisterPreparedCandidate(registration, prepared); err != nil {
		t.Fatal(err)
	}
	reader, err := registry.AcquireCandidate(t.Context(), CandidateLeaseRequest{CandidateID: registration.CandidateID, OwnerID: registration.OwnerID, ProjectID: registration.ProjectID, Compatibility: registration.Compatibility})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Release()
	if registry.RetireCandidate(registration.CandidateID) != 1 {
		t.Fatal("candidate was not retired")
	}
	if err := registry.LeaseRenewalError(); !errors.Is(err, renewalErr) {
		t.Fatalf("health while candidate reader remains = %v, want %v", err, renewalErr)
	}
	reader.Release()
	if err := registry.LeaseRenewalError(); err != nil {
		t.Fatalf("retired candidate left health behind: %v", err)
	}
	factory.callbacks[0](renewalErr)
	if err := registry.LeaseRenewalError(); err != nil {
		t.Fatalf("retired candidate late callback restored health: %v", err)
	}
}

func TestSealedRuntimeLeaseHealthIgnoresClosedCallbacksAndAllowsObserverReads(t *testing.T) {
	factory := &leaseHealthFactory{}
	registry := leaseHealthRegistry(t, factory, nil)
	observed := make(chan error, 2)
	registry.manager.onLeaseRenewalFailure = func(err error) {
		// Observers may query aggregate readiness without reentering a held lock.
		observed <- registry.LeaseRenewalError()
	}
	prepared, err := registry.PrepareServingState(t.Context(), "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	renewalErr := errors.New("preparation lease expired")
	reported := make(chan struct{})
	go func() {
		factory.callbacks[0](renewalErr)
		close(reported)
	}()
	select {
	case <-reported:
	case <-time.After(time.Second):
		t.Fatal("health observer could not query aggregate readiness")
	}
	if err := <-observed; !errors.Is(err, renewalErr) {
		t.Fatalf("observer readiness = %v, want %v", err, renewalErr)
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
	factory.callbacks[0](renewalErr)
	factory.callbacks[0](nil)
	select {
	case err := <-observed:
		t.Fatalf("closed runtime callback reached observer: %v", err)
	default:
	}
	if err := registry.LeaseRenewalError(); err != nil {
		t.Fatalf("closed runtime callback restored health: %v", err)
	}
}
