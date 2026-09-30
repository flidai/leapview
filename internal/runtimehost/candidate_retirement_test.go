package runtimehost

import (
	"context"
	"errors"
	"testing"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func candidateRetirementRequest(registration CandidateRegistration) CandidateRetirementRequest {
	return CandidateRetirementRequest{
		CandidateID: registration.CandidateID,
		OwnerID:     registration.OwnerID,
		Identity: projectgraph.ServingIdentity{
			ProjectID: "project_demo", Environment: "prod", GenerationID: "generation_candidate_cleanup",
		},
		Compatibility: registration.Compatibility,
	}
}

func TestCandidateRetirementWaitRetriesAfterReaderAndCleanupGateDrain(t *testing.T) {
	gate := newCandidateCleanupGate()
	factory := &candidateCleanupFactory{gates: []*candidateCleanupGate{gate}}
	registry, _, now := candidateCleanupFixture(t, factory, &candidateManagedData{})
	registration := prepareCandidateForCleanupTest(t, registry, now)
	generation := candidateGenerationForCleanupTest(registry, registration.CandidateID)
	lease := acquireCandidateForCleanupTest(t, registry, registration)
	retirement, err := registry.RetireCandidate(candidateRetirementRequest(registration))
	if err != nil {
		t.Fatalf("RetireCandidate() error = %v", err)
	}
	if retirement == nil {
		t.Fatal("RetireCandidate() returned a nil retirement handle")
	}

	short, cancel := context.WithTimeout(t.Context(), 15*time.Millisecond)
	err = retirement.Wait(short)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait() with candidate reader held = %v, want deadline", err)
	}
	lease.Release()
	select {
	case <-gate.started:
	case <-time.After(time.Second):
		t.Fatal("retired candidate did not enter runtime Close() after reader release")
	}
	short, cancel = context.WithTimeout(t.Context(), 15*time.Millisecond)
	err = retirement.Wait(short)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait() with cleanup blocked = %v, want deadline", err)
	}
	gate.open()
	waitForCandidateCleanupSignal(t, generation.cleanupDone, "candidate cleanup")
	if err := retirement.Wait(context.Background()); err != nil {
		t.Fatalf("Wait() after cleanup = %v", err)
	}
	if err := retirement.Wait(context.Background()); err != nil {
		t.Fatalf("repeat Wait() after cleanup = %v", err)
	}
}

type candidateRetirementLifetime struct{ err error }

func (l *candidateRetirementLifetime) Close() error { return l.err }

func candidateRetirementRegistration(now time.Time, candidateID string) CandidateRegistration {
	registration := candidateRegistration(now.Add(time.Hour))
	registration.CandidateID = candidateID
	registration.OwnerID = "owner_" + candidateID
	return registration
}

func registerCandidateRetirementCandidate(t *testing.T, registry *Registry, registration CandidateRegistration, lifetime RuntimeLifetime) {
	t.Helper()
	if err := registry.PrepareAndRegisterCandidate(t.Context(), CandidatePreparation{
		Registration: registration,
		Identity:     projectgraph.ServingIdentity{ProjectID: "project_demo", Environment: "prod", GenerationID: "generation_candidate_cleanup"},
		Lifetime:     lifetime,
	}); err != nil {
		t.Fatalf("register candidate %q: %v", registration.CandidateID, err)
	}
}

func retirementRequestFor(registration CandidateRegistration, identity projectgraph.ServingIdentity) CandidateRetirementRequest {
	request := candidateRetirementRequest(registration)
	request.Identity = identity
	return request
}

func TestCandidateRetirementWaitCapturesMatchingCurrentAndRetiredIncarnations(t *testing.T) {
	oldGate, currentGate := newCandidateCleanupGate(), newCandidateCleanupGate()
	factory := &candidateCleanupFactory{gates: []*candidateCleanupGate{oldGate, currentGate}}
	registry, _, now := candidateCleanupFixture(t, factory, &candidateManagedData{})
	registration := prepareCandidateForCleanupTest(t, registry, now)
	oldGeneration := candidateGenerationForCleanupTest(registry, registration.CandidateID)
	oldReader := acquireCandidateForCleanupTest(t, registry, registration)
	request := candidateRetirementRequest(registration)
	oldRetirement, err := registry.RetireCandidate(request)
	if err != nil {
		t.Fatalf("retire first incarnation: %v", err)
	}

	registerCandidateRetirementCandidate(t, registry, registration, nil)
	currentGeneration := candidateGenerationForCleanupTest(registry, registration.CandidateID)
	currentReader := acquireCandidateForCleanupTest(t, registry, registration)
	retirement, err := registry.RetireCandidate(request)
	if err != nil {
		t.Fatalf("retire all matching incarnations: %v", err)
	}
	if retirement == nil {
		t.Fatal("retiring current and older matching candidates returned a nil handle")
	}

	oldReader.Release()
	currentReader.Release()
	for name, gate := range map[string]*candidateCleanupGate{"older retired": oldGate, "current": currentGate} {
		select {
		case <-gate.started:
		case <-time.After(time.Second):
			t.Fatalf("%s candidate cleanup did not start", name)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	if err := retirement.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		cancel()
		t.Fatalf("Wait() before either cleanup gate opens = %v, want deadline", err)
	}
	cancel()
	currentGate.open()
	waitForCandidateCleanupSignal(t, currentGeneration.cleanupDone, "current incarnation cleanup")
	ctx, cancel = context.WithTimeout(t.Context(), 20*time.Millisecond)
	if err := retirement.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		cancel()
		t.Fatalf("Wait() while older incarnation remains blocked = %v, want deadline", err)
	}
	cancel()
	oldGate.open()
	waitForCandidateCleanupSignal(t, oldGeneration.cleanupDone, "older incarnation cleanup")
	if err := retirement.Wait(context.Background()); err != nil {
		t.Fatalf("Wait() after both matching cleanups = %v", err)
	}
	if err := oldRetirement.Wait(context.Background()); err != nil {
		t.Fatalf("original handle Wait() after cleanup = %v", err)
	}
}

func TestCandidateRetirementHandleDoesNotDriftToLaterReplacement(t *testing.T) {
	oldGate := newCandidateCleanupGate()
	factory := &candidateCleanupFactory{gates: []*candidateCleanupGate{oldGate}}
	registry, _, now := candidateCleanupFixture(t, factory, &candidateManagedData{})
	registration := prepareCandidateForCleanupTest(t, registry, now)
	oldGeneration := candidateGenerationForCleanupTest(registry, registration.CandidateID)
	oldReader := acquireCandidateForCleanupTest(t, registry, registration)
	request := candidateRetirementRequest(registration)
	retirement, err := registry.RetireCandidate(request)
	if err != nil {
		t.Fatalf("retire original candidate: %v", err)
	}

	// Register an identical tuple after capture. The opaque handle must retain
	// only the original pointer even though the new candidate matches its tuple.
	registerCandidateRetirementCandidate(t, registry, registration, nil)
	replacement := candidateGenerationForCleanupTest(registry, registration.CandidateID)
	if replacement == nil || replacement == oldGeneration {
		t.Fatal("later replacement was not installed")
	}
	oldReader.Release()
	select {
	case <-oldGate.started:
	case <-time.After(time.Second):
		t.Fatal("original candidate cleanup did not start")
	}
	oldGate.open()
	waitForCandidateCleanupSignal(t, oldGeneration.cleanupDone, "original candidate cleanup")
	if err := retirement.Wait(context.Background()); err != nil {
		t.Fatalf("captured retirement handle followed a later replacement: %v", err)
	}
	lease := acquireCandidateForCleanupTest(t, registry, registration)
	if got := lease.Runtime(); got != factory.getRuntime(1) {
		lease.Release()
		t.Fatalf("replacement lease runtime = %T, want later replacement", got)
	}
	lease.Release()
	if current := candidateGenerationForCleanupTest(registry, registration.CandidateID); current != replacement {
		t.Fatal("waiting on the old handle retired or removed the later replacement")
	}
}

func TestCandidateRetirementMismatchDoesNotFenceCurrentCandidate(t *testing.T) {
	factory := &candidateCleanupFactory{}
	registry, _, now := candidateCleanupFixture(t, factory, &candidateManagedData{})
	registration := prepareCandidateForCleanupTest(t, registry, now)
	valid := candidateRetirementRequest(registration)
	otherIdentity := func(projectID, environment, generation string) projectgraph.ServingIdentity {
		identity, err := projectgraph.NewServingIdentity(projectgraph.ResourceID(projectID), environment, generation)
		if err != nil {
			t.Fatal(err)
		}
		return identity
	}
	tests := map[string]func(*CandidateRetirementRequest){
		"candidate": func(request *CandidateRetirementRequest) { request.CandidateID = "candidate_other" },
		"owner":     func(request *CandidateRetirementRequest) { request.OwnerID = "owner_other" },
		"project": func(request *CandidateRetirementRequest) {
			request.Identity = otherIdentity("project_other", "prod", "generation_candidate_cleanup")
		},
		"environment": func(request *CandidateRetirementRequest) {
			request.Identity = otherIdentity("project_demo", "dev", "generation_candidate_cleanup")
		},
		"generation": func(request *CandidateRetirementRequest) {
			request.Identity = otherIdentity("project_demo", "prod", "generation_other")
		},
		"compatibility": func(request *CandidateRetirementRequest) {
			request.Compatibility.DataRevision = "snapshot:other"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			request := valid
			mutate(&request)
			if retirement, err := registry.RetireCandidate(request); !errors.Is(err, ErrCandidateRuntimeNotFound) || retirement != nil {
				t.Fatalf("mismatched RetireCandidate() = (%v, %v), want (nil, not found)", retirement, err)
			}
			lease := acquireCandidateForCleanupTest(t, registry, registration)
			lease.Release()
		})
	}
	lease := acquireCandidateForCleanupTest(t, registry, registration)
	lease.Release()
}

func TestCandidateRetirementUnknownCompletedAndInvalidRequests(t *testing.T) {
	factory := &candidateCleanupFactory{}
	registry, _, now := candidateCleanupFixture(t, factory, &candidateManagedData{})
	registration := prepareCandidateForCleanupTest(t, registry, now)
	valid := candidateRetirementRequest(registration)
	unknown := valid
	unknown.CandidateID = "candidate_missing"
	if retirement, err := registry.RetireCandidate(unknown); !errors.Is(err, ErrCandidateRuntimeNotFound) || retirement != nil {
		t.Fatalf("unknown retirement = (%v, %v), want (nil, not found)", retirement, err)
	}
	retirement, err := registry.RetireCandidate(valid)
	if err != nil {
		t.Fatalf("retire candidate: %v", err)
	}
	if err := retirement.Wait(context.Background()); err != nil {
		t.Fatalf("Wait() for immediate cleanup = %v", err)
	}
	if next, err := registry.RetireCandidate(valid); !errors.Is(err, ErrCandidateRuntimeNotFound) || next != nil {
		t.Fatalf("completed candidate retirement = (%v, %v), want (nil, not found)", next, err)
	}

	var nilRegistry *Registry
	if retirement, err := nilRegistry.RetireCandidate(valid); !errors.Is(err, ErrCandidateRuntimeClosed) || retirement != nil {
		t.Fatalf("nil registry retirement = (%v, %v), want closed", retirement, err)
	}
	if err := retirement.Wait(nil); !errors.Is(err, ErrCandidateRuntimeInvalid) {
		t.Fatalf("Wait(nil) = %v, want invalid", err)
	}
	var nilRetirement *CandidateRetirement
	if err := nilRetirement.Wait(context.Background()); !errors.Is(err, ErrCandidateRuntimeInvalid) {
		t.Fatalf("nil handle Wait() = %v, want invalid", err)
	}
	if err := new(CandidateRetirement).Wait(context.Background()); !errors.Is(err, ErrCandidateRuntimeInvalid) {
		t.Fatalf("zero-value Wait() = %v, want invalid", err)
	}
	invalidRequests := []CandidateRetirementRequest{
		{OwnerID: valid.OwnerID, Identity: valid.Identity, Compatibility: valid.Compatibility},
		{CandidateID: valid.CandidateID, Identity: valid.Identity, Compatibility: valid.Compatibility},
		{CandidateID: valid.CandidateID, OwnerID: valid.OwnerID, Compatibility: valid.Compatibility},
		{CandidateID: valid.CandidateID, OwnerID: valid.OwnerID, Identity: valid.Identity},
	}
	for i, request := range invalidRequests {
		if handle, err := registry.RetireCandidate(request); !errors.Is(err, ErrCandidateRuntimeInvalid) || handle != nil {
			t.Fatalf("invalid request %d = (%v, %v), want (nil, invalid)", i, handle, err)
		}
	}
	closed, _, _ := candidateCleanupFixture(t, &candidateCleanupFactory{}, &candidateManagedData{})
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if handle, err := closed.RetireCandidate(valid); !errors.Is(err, ErrCandidateRuntimeClosed) || handle != nil {
		t.Fatalf("closed registry retirement = (%v, %v), want closed", handle, err)
	}
}

func TestCandidateRetirementStickyFailureStillFencesMatchingCurrent(t *testing.T) {
	priorErr := errors.New("prior candidate cleanup failed")
	factory := &candidateCleanupFactory{}
	registry, _, now := candidateCleanupFixture(t, factory, &candidateManagedData{})
	failed := candidateRetirementRegistration(now, "candidate_failed_history")
	registerCandidateRetirementCandidate(t, registry, failed, &candidateRetirementLifetime{err: priorErr})
	failedHandle, err := registry.RetireCandidate(candidateRetirementRequest(failed))
	if err != nil {
		t.Fatalf("retire candidate with cleanup failure: %v", err)
	}
	if err := failedHandle.Wait(context.Background()); !errors.Is(err, priorErr) {
		t.Fatalf("failed history Wait() = %v, want prior cleanup error", err)
	}

	current := candidateRetirementRegistration(now, "candidate_after_failure")
	registerCandidateRetirementCandidate(t, registry, current, nil)
	currentHandle, err := registry.RetireCandidate(candidateRetirementRequest(current))
	if err != nil {
		t.Fatalf("matching retirement after sticky cleanup failure: %v", err)
	}
	if _, err := registry.AcquireCandidate(t.Context(), CandidateLeaseRequest{
		CandidateID: current.CandidateID, OwnerID: current.OwnerID,
		ProjectID: current.ProjectID, Compatibility: current.Compatibility,
	}); !errors.Is(err, ErrCandidateRuntimeNotFound) {
		t.Fatalf("candidate remained leaseable after retirement: %v", err)
	}
	if err := currentHandle.Wait(context.Background()); !errors.Is(err, priorErr) {
		t.Fatalf("new handle after sticky failure = %v, want captured prior error", err)
	}
}

func TestCandidateRetirementHandleIgnoresLaterUnrelatedCleanupFailure(t *testing.T) {
	gate := newCandidateCleanupGate()
	factory := &candidateCleanupFactory{gates: []*candidateCleanupGate{gate}}
	registry, _, now := candidateCleanupFixture(t, factory, &candidateManagedData{})
	first := candidateRetirementRegistration(now, "candidate_handle_success")
	registerCandidateRetirementCandidate(t, registry, first, nil)
	firstHandle, err := registry.RetireCandidate(candidateRetirementRequest(first))
	if err != nil {
		t.Fatalf("retire successful candidate: %v", err)
	}
	select {
	case <-gate.started:
	case <-time.After(time.Second):
		t.Fatal("first candidate cleanup did not start")
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- firstHandle.Wait(context.Background()) }()
	select {
	case err := <-waitDone:
		t.Fatalf("first handle completed before its cleanup: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	otherFailure := errors.New("later unrelated cleanup failed")
	second := candidateRetirementRegistration(now, "candidate_handle_failure")
	registerCandidateRetirementCandidate(t, registry, second, &candidateRetirementLifetime{err: otherFailure})
	secondHandle, err := registry.RetireCandidate(candidateRetirementRequest(second))
	if err != nil {
		t.Fatalf("retire unrelated candidate: %v", err)
	}
	if err := secondHandle.Wait(context.Background()); !errors.Is(err, otherFailure) {
		t.Fatalf("unrelated failed handle = %v, want %v", err, otherFailure)
	}
	gate.open()
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("earlier successful handle inherited later cleanup failure: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first handle did not finish after its cleanup drained")
	}
}

func TestCandidateRetirementWaitIsStableWithConcurrentRegistryClose(t *testing.T) {
	gate := newCandidateCleanupGate()
	factory := &candidateCleanupFactory{gates: []*candidateCleanupGate{gate}}
	registry, _, now := candidateCleanupFixture(t, factory, &candidateManagedData{})
	registration := prepareCandidateForCleanupTest(t, registry, now)
	retirement, err := registry.RetireCandidate(candidateRetirementRequest(registration))
	if err != nil {
		t.Fatalf("RetireCandidate() error = %v", err)
	}
	select {
	case <-gate.started:
	case <-time.After(time.Second):
		t.Fatal("candidate cleanup did not start")
	}

	const waiters = 4
	results := make(chan error, waiters)
	waitersStarted := make(chan struct{}, waiters)
	for i := 0; i < waiters; i++ {
		go func() {
			waitersStarted <- struct{}{}
			results <- retirement.Wait(context.Background())
		}()
	}
	for i := 0; i < waiters; i++ {
		<-waitersStarted
	}
	closeDone := make(chan error, 1)
	closeStarted := make(chan struct{})
	go func() {
		close(closeStarted)
		closeDone <- registry.Close()
	}()
	<-closeStarted
	time.Sleep(20 * time.Millisecond)
	select {
	case err := <-closeDone:
		t.Fatalf("Registry.Close() completed while candidate cleanup was blocked: %v", err)
	default:
	}
	gate.open()
	for i := 0; i < waiters; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("concurrent Wait() %d error = %v", i, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("concurrent Wait() %d did not finish", i)
		}
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Registry.Close() after cleanup = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Registry.Close() did not finish after candidate cleanup")
	}
}
