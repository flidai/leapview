package runtimehost

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type candidateCleanupGate struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newCandidateCleanupGate() *candidateCleanupGate {
	return &candidateCleanupGate{started: make(chan struct{}), release: make(chan struct{})}
}

func (g *candidateCleanupGate) open() {
	if g != nil {
		g.once.Do(func() { close(g.release) })
	}
}

type candidateCleanupRuntime struct {
	PreparedRuntime
	gate      *candidateCleanupGate
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func (r *candidateCleanupRuntime) Close() error {
	r.closeOnce.Do(func() {
		if r.gate != nil {
			close(r.gate.started)
			<-r.gate.release
		}
		r.closeErr = r.PreparedRuntime.Close()
		close(r.done)
	})
	return r.closeErr
}

type candidateCleanupFactory struct {
	base    lifecycleFactory
	mu      sync.Mutex
	gates   []*candidateCleanupGate
	runtime []*candidateCleanupRuntime
}

func (f *candidateCleanupFactory) Prepare(ctx context.Context, input RuntimeInput) (PreparedRuntime, error) {
	prepared, err := f.base.Prepare(ctx, input)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	index := len(f.runtime)
	var gate *candidateCleanupGate
	if index < len(f.gates) {
		gate = f.gates[index]
	}
	runtime := &candidateCleanupRuntime{PreparedRuntime: prepared, gate: gate, done: make(chan struct{})}
	f.runtime = append(f.runtime, runtime)
	return runtime, nil
}

func (f *candidateCleanupFactory) getRuntime(index int) *candidateCleanupRuntime {
	f.mu.Lock()
	defer f.mu.Unlock()
	if index < 0 || index >= len(f.runtime) {
		return nil
	}
	return f.runtime[index]
}

const candidateCleanupTimeout = 60 * time.Millisecond

func candidateCleanupFixture(t *testing.T, factory *candidateCleanupFactory, managed *candidateManagedData) (*Registry, *lifecycleRepo, time.Time) {
	t.Helper()
	now := time.Now().UTC()
	state := servingstate.State{
		ID: "generation_candidate_cleanup", ProjectID: "project_demo", Environment: "prod",
		Status:             servingstate.StatusValidated,
		Digest:             "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		DuckLakeSnapshotID: 42,
	}
	artifact := servingstate.Artifact{ID: "artifact_candidate_cleanup", ServingStateID: state.ID, Digest: state.Digest}
	repo := &lifecycleRepo{
		state: state, artifact: artifact,
		states:    map[servingstate.ID]servingstate.State{state.ID: state},
		artifacts: map[servingstate.ID]servingstate.Artifact{state.ID: artifact},
		releaseCh: make(chan string, 16),
	}
	registry := NewRegistryWithFactory(RegistryOptions{
		Repo: repo, ProjectID: projectgraph.ResourceID("project_demo"), Environment: "prod",
		Factory: factory, ManagedData: &candidateResolver{lifetime: managed}, Authorization: &lifecycleAuth{},
		Now: func() time.Time { return now }, CleanupDrainTimeout: candidateCleanupTimeout,
		LeaseReleaseShutdownTimeout: time.Second,
	})
	t.Cleanup(func() {
		for _, gate := range factory.gates {
			gate.open()
		}
		_ = registry.Close()
	})
	return registry, repo, now
}

func prepareCandidateForCleanupTest(t *testing.T, registry *Registry, now time.Time) CandidateRegistration {
	t.Helper()
	registration := candidateRegistration(now.Add(time.Hour))
	registration.CandidateID = "candidate_cleanup"
	registration.OwnerID = "owner_cleanup"
	if err := registry.PrepareAndRegisterCandidate(t.Context(), CandidatePreparation{
		Registration: registration,
		Identity:     projectgraph.ServingIdentity{ProjectID: "project_demo", Environment: "prod", GenerationID: "generation_candidate_cleanup"},
	}); err != nil {
		t.Fatalf("prepare candidate: %v", err)
	}
	return registration
}

func acquireCandidateForCleanupTest(t *testing.T, registry *Registry, registration CandidateRegistration) Lease {
	t.Helper()
	lease, err := registry.AcquireCandidate(t.Context(), CandidateLeaseRequest{
		CandidateID: registration.CandidateID, OwnerID: registration.OwnerID,
		ProjectID: registration.ProjectID, Compatibility: registration.Compatibility,
	})
	if err != nil {
		t.Fatalf("acquire candidate: %v", err)
	}
	return lease
}

func TestRegistryCloseWaitsForRetiredCandidateCleanupAfterReaderRelease(t *testing.T) {
	gate := newCandidateCleanupGate()
	factory := &candidateCleanupFactory{gates: []*candidateCleanupGate{gate}}
	registry, _, now := candidateCleanupFixture(t, factory, &candidateManagedData{})
	registration := prepareCandidateForCleanupTest(t, registry, now)
	generation := candidateGenerationForCleanupTest(registry, registration.CandidateID)
	if generation == nil {
		t.Fatal("registered candidate generation was not found")
	}
	lease := acquireCandidateForCleanupTest(t, registry, registration)
	if _, err := registry.RetireCandidate(CandidateRetirementRequest{
		CandidateID: registration.CandidateID, OwnerID: registration.OwnerID,
		Identity: lease.Identity(), Compatibility: registration.Compatibility,
	}); err != nil {
		t.Fatalf("RetireCandidate() error = %v", err)
	}
	releaseDone := make(chan struct{})
	go func() {
		lease.Release()
		close(releaseDone)
	}()
	select {
	case <-gate.started:
	case <-time.After(time.Second):
		t.Fatal("retired candidate cleanup did not enter runtime Close()")
	}

	started := time.Now()
	closeErr := registry.Close()
	if time.Since(started) > time.Second {
		t.Fatalf("Registry.Close() exceeded bounded cleanup wait: %v", time.Since(started))
	}
	gate.open()
	select {
	case <-releaseDone:
	case <-time.After(time.Second):
		t.Fatal("candidate cleanup did not finish after releasing Close() gate")
	}
	waitForCandidateCleanupSignal(t, generation.cleanupDone, "retired candidate cleanup")
	if closeErr == nil {
		t.Fatal("Registry.Close() succeeded while a retired candidate cleanup was still blocked")
	}
}

func candidateGenerationForCleanupTest(registry *Registry, candidateID string) *candidateGeneration {
	registry.candidates.mu.Lock()
	defer registry.candidates.mu.Unlock()
	if generation := registry.candidates.current[candidateRuntimeKey{candidateID: candidateID}]; generation != nil {
		return generation
	}
	for generation := range registry.candidates.retired {
		if generation.key.candidateID == candidateID {
			return generation
		}
	}
	return nil
}

func waitForCandidateCleanupSignal(t *testing.T, done <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("%s did not finish", label)
	}
}

func TestRegistryCloseBoundsUnleasedBlockedCandidateCleanup(t *testing.T) {
	gate := newCandidateCleanupGate()
	factory := &candidateCleanupFactory{gates: []*candidateCleanupGate{gate}}
	registry, _, now := candidateCleanupFixture(t, factory, &candidateManagedData{})
	registration := prepareCandidateForCleanupTest(t, registry, now)
	generation := candidateGenerationForCleanupTest(registry, registration.CandidateID)
	if generation == nil {
		t.Fatal("registered candidate generation was not found")
	}

	closeDone := make(chan error, 1)
	started := time.Now()
	go func() { closeDone <- registry.Close() }()
	select {
	case <-gate.started:
	case <-time.After(time.Second):
		t.Fatal("registry close did not enter candidate runtime Close()")
	}
	select {
	case err := <-closeDone:
		if err == nil {
			t.Fatal("Registry.Close() succeeded while candidate runtime Close() was blocked")
		}
		if elapsed := time.Since(started); elapsed > 8*candidateCleanupTimeout {
			t.Fatalf("Registry.Close() elapsed %v, want bounded by cleanup drain timeout", elapsed)
		}
	case <-time.After(8 * candidateCleanupTimeout):
		gate.open()
		t.Fatal("Registry.Close() remained blocked beyond the configured cleanup drain bound")
	}
	gate.open()
	waitForCandidateCleanupSignal(t, generation.cleanupDone, "candidate cleanup")
}

func TestRegistryCloseRetainsCompletedCandidateCleanupFailure(t *testing.T) {
	cleanupErr := errors.New("candidate managed data release failed")
	managed := &candidateManagedData{err: cleanupErr}
	factory := &candidateCleanupFactory{}
	registry, _, now := candidateCleanupFixture(t, factory, managed)
	registration := prepareCandidateForCleanupTest(t, registry, now)
	generation := candidateGenerationForCleanupTest(registry, registration.CandidateID)
	lease := acquireCandidateForCleanupTest(t, registry, registration)
	if _, err := registry.RetireCandidate(CandidateRetirementRequest{
		CandidateID: registration.CandidateID, OwnerID: registration.OwnerID,
		Identity: lease.Identity(), Compatibility: registration.Compatibility,
	}); err != nil {
		t.Fatalf("RetireCandidate() error = %v", err)
	}
	lease.Release()
	waitForCandidateCleanupSignal(t, generation.cleanupDone, "retired candidate cleanup")
	registry.candidates.mu.Lock()
	retiredCount := len(registry.candidates.retired)
	registry.candidates.mu.Unlock()
	if retiredCount != 0 {
		t.Fatalf("retired candidate count after cleanup = %d, want 0", retiredCount)
	}
	if err := registry.Close(); !errors.Is(err, cleanupErr) {
		t.Fatalf("Registry.Close() error = %v, want retained candidate cleanup error %v", err, cleanupErr)
	}
}

func TestRetiredCandidateCleanupKeepsReplacementWithSameID(t *testing.T) {
	oldCleanupGate := newCandidateCleanupGate()
	factory := &candidateCleanupFactory{gates: []*candidateCleanupGate{oldCleanupGate}}
	registry, _, now := candidateCleanupFixture(t, factory, &candidateManagedData{})
	registration := prepareCandidateForCleanupTest(t, registry, now)
	oldGeneration := candidateGenerationForCleanupTest(registry, registration.CandidateID)
	oldLease := acquireCandidateForCleanupTest(t, registry, registration)
	if err := registry.PrepareAndRegisterCandidate(t.Context(), CandidatePreparation{
		Registration: registration,
		Identity:     projectgraph.ServingIdentity{ProjectID: "project_demo", Environment: "prod", GenerationID: "generation_candidate_cleanup"},
	}); err != nil {
		t.Fatalf("replace candidate: %v", err)
	}
	newGeneration := candidateGenerationForCleanupTest(registry, registration.CandidateID)
	if newGeneration == nil || newGeneration == oldGeneration {
		t.Fatal("replacement candidate was not installed")
	}

	oldReleaseDone := make(chan struct{})
	go func() {
		oldLease.Release()
		close(oldReleaseDone)
	}()
	select {
	case <-oldCleanupGate.started:
	case <-time.After(time.Second):
		t.Fatal("retired candidate cleanup did not start")
	}
	newLease := acquireCandidateForCleanupTest(t, registry, registration)
	if got := newLease.Runtime(); got != factory.getRuntime(1) {
		t.Fatalf("candidate acquired during old cleanup = %T, want replacement runtime", got)
	}
	oldCleanupGate.open()
	waitForCandidateCleanupSignal(t, oldGeneration.cleanupDone, "old candidate cleanup")
	select {
	case <-oldReleaseDone:
	case <-time.After(time.Second):
		t.Fatal("old candidate release did not finish")
	}
	if current := candidateGenerationForCleanupTest(registry, registration.CandidateID); current != newGeneration {
		t.Fatal("finishing retired cleanup removed or replaced the current candidate with the same ID")
	}
	newLease.Release()
	if _, err := registry.RetireCandidate(CandidateRetirementRequest{
		CandidateID: registration.CandidateID, OwnerID: registration.OwnerID,
		Identity: newLease.Identity(), Compatibility: registration.Compatibility,
	}); err != nil {
		t.Fatalf("RetireCandidate(replacement) error = %v", err)
	}
	waitForCandidateCleanupSignal(t, newGeneration.cleanupDone, "replacement candidate cleanup")
}

func activateCandidateCleanupServingRuntime(t *testing.T, registry *Registry) {
	t.Helper()
	prepared, err := registry.PrepareServingState(t.Context(), "generation_candidate_cleanup")
	if err != nil {
		t.Fatalf("prepare serving runtime: %v", err)
	}
	if err := registry.ActivatePrepared(prepared, func() error { return nil }); err != nil {
		t.Fatalf("activate serving runtime: %v", err)
	}
}

func waitForRepoSnapshotReleases(t *testing.T, repo *lifecycleRepo, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		select {
		case <-repo.releaseCh:
		case <-time.After(time.Second):
			t.Fatalf("snapshot release %d/%d did not reach the repository", i+1, count)
		}
	}
}

func assertCandidateCleanupReleaseQueueAccepting(t *testing.T, registry *Registry, want bool) {
	t.Helper()
	queue := registry.manager.releaseQueue
	queue.mu.Lock()
	got := queue.accepting
	queue.mu.Unlock()
	if got != want {
		t.Fatalf("snapshot release queue accepting = %t, want %t", got, want)
	}
}

func TestRegistryCloseKeepsSnapshotReleaseQueueUntilCandidateAndServingCleanupFinish(t *testing.T) {
	for _, first := range []string{"candidate", "serving"} {
		t.Run(first+" first", func(t *testing.T) {
			servingGate := newCandidateCleanupGate()
			candidateGate := newCandidateCleanupGate()
			factory := &candidateCleanupFactory{gates: []*candidateCleanupGate{servingGate, candidateGate}}
			registry, repo, now := candidateCleanupFixture(t, factory, &candidateManagedData{})
			activateCandidateCleanupServingRuntime(t, registry)
			servingReader, err := registry.Acquire(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			registration := prepareCandidateForCleanupTest(t, registry, now)
			candidateReader := acquireCandidateForCleanupTest(t, registry, registration)
			candidateGeneration := candidateGenerationForCleanupTest(registry, registration.CandidateID)
			registry.manager.mu.RLock()
			servingGeneration := registry.manager.current
			registry.manager.mu.RUnlock()
			if candidateGeneration == nil || servingGeneration == nil {
				t.Fatal("candidate and serving generations must both be active")
			}
			defer func() {
				candidateGate.open()
				servingGate.open()
				candidateReader.Release()
				servingReader.Release()
			}()

			closed := make(chan error, 1)
			closeStarted := time.Now()
			go func() { closed <- registry.Close() }()
			select {
			case closeErr := <-closed:
				if closeErr == nil {
					t.Fatal("Registry.Close() succeeded with candidate and serving readers still held")
				}
				if elapsed := time.Since(closeStarted); elapsed > 8*candidateCleanupTimeout {
					t.Fatalf("Registry.Close() elapsed %v, want bounded drain timeout", elapsed)
				}
			case <-time.After(8 * candidateCleanupTimeout):
				t.Fatal("Registry.Close() did not return within bounded cleanup drains")
			}
			registry.manager.mu.RLock()
			servingCleanupDone := servingGeneration.cleanupDone
			registry.manager.mu.RUnlock()
			if servingCleanupDone == nil {
				t.Fatal("serving generation has no cleanup completion signal after close")
			}

			// The old implementation used a second drain timeout in a background
			// goroutine, then closed the queue while these leases still owned it.
			time.Sleep(3 * candidateCleanupTimeout)
			assertCandidateCleanupReleaseQueueAccepting(t, registry, true)

			releaseCandidate := func() {
				go candidateReader.Release()
				select {
				case <-candidateGate.started:
				case <-time.After(time.Second):
					t.Fatal("candidate cleanup did not enter runtime Close()")
				}
				candidateGate.open()
				waitForCandidateCleanupSignal(t, candidateGeneration.cleanupDone, "candidate cleanup")
			}
			releaseServing := func() {
				servingReader.Release()
				select {
				case <-servingGate.started:
				case <-time.After(time.Second):
					t.Fatal("serving cleanup did not enter runtime Close()")
				}
				servingGate.open()
				waitForCandidateCleanupSignal(t, servingCleanupDone, "serving runtime cleanup")
			}

			if first == "candidate" {
				releaseCandidate()
				waitForRepoSnapshotReleases(t, repo, 1)
				time.Sleep(2 * candidateCleanupTimeout)
				assertCandidateCleanupReleaseQueueAccepting(t, registry, true)
				releaseServing()
			} else {
				releaseServing()
				waitForRepoSnapshotReleases(t, repo, 1)
				time.Sleep(2 * candidateCleanupTimeout)
				assertCandidateCleanupReleaseQueueAccepting(t, registry, true)
				releaseCandidate()
			}
			waitForRepoSnapshotReleases(t, repo, 1)
			select {
			case <-registry.manager.releaseQueue.workerDone:
			case <-time.After(time.Second):
				t.Fatal("snapshot release queue did not close after both runtime cleanups finished")
			}
			assertCandidateCleanupReleaseQueueAccepting(t, registry, false)
		})
	}
}
