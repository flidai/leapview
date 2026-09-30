package runtimehost

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

const retiredCleanupDigest1 = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const retiredCleanupDigest2 = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
const retiredCleanupDigest3 = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

func retiredCleanupRegistry(t *testing.T, factory RuntimeFactory, noActive bool) *Registry {
	t.Helper()
	states := map[servingstate.ID]servingstate.State{}
	artifacts := map[servingstate.ID]servingstate.Artifact{}
	for _, item := range []struct {
		id     servingstate.ID
		digest string
	}{
		{"generation_cleanup_1", retiredCleanupDigest1},
		{"generation_cleanup_2", retiredCleanupDigest2},
		{"generation_cleanup_3", retiredCleanupDigest3},
	} {
		state := servingstate.State{ID: item.id, ProjectID: "project_demo", Environment: "prod", Status: servingstate.StatusValidated, Digest: item.digest}
		states[item.id] = state
		artifacts[item.id] = servingstate.Artifact{ID: "artifact_" + string(item.id), ServingStateID: item.id, Digest: item.digest}
	}
	initial := states["generation_cleanup_1"]
	repo := &lifecycleRepo{
		state:     initial,
		artifact:  artifacts[initial.ID],
		states:    states,
		artifacts: artifacts,
		noActive:  noActive,
	}
	registry := NewRegistryWithFactory(RegistryOptions{
		Repo: repo, ProjectID: "project_demo", Environment: "prod", Factory: factory,
		Authorization: &lifecycleAuth{}, CleanupDrainTimeout: time.Second,
	})
	t.Cleanup(func() { _ = registry.Close() })
	return registry
}

type retiredCleanupPlan struct {
	release chan struct{}
	err     error
}

type retiredCleanupRuntime struct {
	authorization accesssnapshot.AuthorizationSnapshot
	plan          retiredCleanupPlan
	entered       chan struct{}
	closed        chan struct{}
	closeOnce     sync.Once
}

func (r *retiredCleanupRuntime) Close() error {
	r.closeOnce.Do(func() {
		close(r.entered)
		if r.plan.release != nil {
			<-r.plan.release
		}
		close(r.closed)
	})
	return r.plan.err
}

func (r *retiredCleanupRuntime) AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot {
	return r.authorization
}

type retiredCleanupFactory struct {
	mu       sync.Mutex
	plans    []retiredCleanupPlan
	runtimes []*retiredCleanupRuntime
}

func (f *retiredCleanupFactory) Prepare(_ context.Context, input RuntimeInput) (PreparedRuntime, error) {
	identity, err := projectgraph.NewServingIdentity(input.State.ProjectID, string(servingstate.NormalizeEnvironment(input.State.Environment)), string(input.State.ID))
	if err != nil {
		return nil, err
	}
	project, err := projectgraph.NewProjectGraph(nil, nil)
	if err != nil {
		return nil, err
	}
	authorization, err := accesssnapshot.NewAuthorizationSnapshot(identity, project, nil, nil)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	index := len(f.runtimes)
	var plan retiredCleanupPlan
	if index < len(f.plans) {
		plan = f.plans[index]
	}
	runtime := &retiredCleanupRuntime{
		authorization: authorization,
		plan:          plan,
		entered:       make(chan struct{}),
		closed:        make(chan struct{}),
	}
	f.runtimes = append(f.runtimes, runtime)
	return runtime, nil
}

func (f *retiredCleanupFactory) runtime(index int) *retiredCleanupRuntime {
	f.mu.Lock()
	defer f.mu.Unlock()
	if index < 0 || index >= len(f.runtimes) {
		return nil
	}
	return f.runtimes[index]
}

func retiredCleanupIdentity(t *testing.T, generation string) projectgraph.ServingIdentity {
	t.Helper()
	identity, err := projectgraph.NewServingIdentity("project_demo", "prod", generation)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func activateRetiredCleanupGeneration(t *testing.T, registry *Registry, generation string) {
	t.Helper()
	prepared, err := registry.PrepareServingState(t.Context(), generation)
	if err != nil {
		t.Fatalf("prepare %s: %v", generation, err)
	}
	if err := registry.ActivatePrepared(prepared, func() error { return nil }); err != nil {
		t.Fatalf("activate %s: %v", generation, err)
	}
}

func TestWithRetiredRuntimeCleanupWaitsForReaderLease(t *testing.T) {
	factory := &lifecycleFactory{}
	registry := retiredCleanupRegistry(t, factory, false)
	activateRetiredCleanupGeneration(t, registry, "generation_cleanup_1")
	reader, err := registry.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	oldRuntime := factory.last()
	activateRetiredCleanupGeneration(t, registry, "generation_cleanup_2")

	callbackStarted := make(chan struct{})
	completed := make(chan error, 1)
	expected := retiredCleanupIdentity(t, "generation_cleanup_2")
	go func() {
		completed <- registry.WithRetiredRuntimeCleanup(context.Background(), expected, func() error {
			close(callbackStarted)
			return nil
		})
	}()
	select {
	case <-callbackStarted:
		t.Fatal("callback ran while a retired runtime still had a reader")
	case <-time.After(30 * time.Millisecond):
	}
	select {
	case <-oldRuntime.closed:
		t.Fatal("retired runtime closed while its reader lease was held")
	default:
	}

	reader.Release()
	select {
	case err := <-completed:
		if err != nil {
			t.Fatalf("WithRetiredRuntimeCleanup() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cleanup callback did not run after releasing the reader")
	}
	select {
	case <-callbackStarted:
	default:
		t.Fatal("cleanup callback did not run")
	}
}

func TestWithRetiredRuntimeCleanupWaitsForEveryRetiredGeneration(t *testing.T) {
	factory := &retiredCleanupFactory{}
	registry := retiredCleanupRegistry(t, factory, false)
	activateRetiredCleanupGeneration(t, registry, "generation_cleanup_1")
	oldestReader, err := registry.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	oldestRuntime := factory.runtime(0)

	activateRetiredCleanupGeneration(t, registry, "generation_cleanup_2")
	middleRuntime := factory.runtime(1)
	activateRetiredCleanupGeneration(t, registry, "generation_cleanup_3")
	select {
	case <-middleRuntime.closed:
	case <-time.After(time.Second):
		t.Fatal("immediate predecessor cleanup did not finish")
	}
	select {
	case <-oldestRuntime.entered:
		t.Fatal("oldest runtime cleanup started despite its active reader")
	default:
	}

	callbackStarted := make(chan struct{})
	completed := make(chan error, 1)
	expected := retiredCleanupIdentity(t, "generation_cleanup_3")
	go func() {
		completed <- registry.WithRetiredRuntimeCleanup(context.Background(), expected, func() error {
			close(callbackStarted)
			return nil
		})
	}()
	select {
	case <-callbackStarted:
		t.Fatal("callback ran while the oldest retired generation still had a reader")
	case <-time.After(30 * time.Millisecond):
	}
	oldestReader.Release()
	select {
	case err := <-completed:
		if err != nil {
			t.Fatalf("WithRetiredRuntimeCleanup() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("callback did not run after the oldest reader drained")
	}
}

func TestWithRetiredRuntimeCleanupWaitsForBlockedClose(t *testing.T) {
	release := make(chan struct{})
	factory := &retiredCleanupFactory{plans: []retiredCleanupPlan{{release: release}}}
	registry := retiredCleanupRegistry(t, factory, false)
	activateRetiredCleanupGeneration(t, registry, "generation_cleanup_1")
	activateRetiredCleanupGeneration(t, registry, "generation_cleanup_2")
	oldRuntime := factory.runtime(0)
	select {
	case <-oldRuntime.entered:
	case <-time.After(time.Second):
		t.Fatal("retired runtime Close() did not start")
	}

	callbackStarted := make(chan struct{})
	completed := make(chan error, 1)
	expected := retiredCleanupIdentity(t, "generation_cleanup_2")
	go func() {
		completed <- registry.WithRetiredRuntimeCleanup(context.Background(), expected, func() error {
			close(callbackStarted)
			return nil
		})
	}()
	select {
	case <-callbackStarted:
		t.Fatal("callback ran before retired runtime Close() returned")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-completed:
		if err != nil {
			t.Fatalf("WithRetiredRuntimeCleanup() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("callback did not run after Close() returned")
	}
}

func TestWithRetiredRuntimeCleanupTimeoutCanRetry(t *testing.T) {
	registry := retiredCleanupRegistry(t, &lifecycleFactory{}, false)
	activateRetiredCleanupGeneration(t, registry, "generation_cleanup_1")
	reader, err := registry.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	activateRetiredCleanupGeneration(t, registry, "generation_cleanup_2")
	expected := retiredCleanupIdentity(t, "generation_cleanup_2")
	callbackRan := make(chan struct{}, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	err = registry.WithRetiredRuntimeCleanup(ctx, expected, func() error {
		callbackRan <- struct{}{}
		return nil
	})
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed out cleanup error = %v, want context deadline", err)
	}
	select {
	case <-callbackRan:
		t.Fatal("callback ran after cleanup wait timed out")
	default:
	}
	reader.Release()
	if err := registry.WithRetiredRuntimeCleanup(context.Background(), expected, func() error {
		callbackRan <- struct{}{}
		return nil
	}); err != nil {
		t.Fatalf("retry WithRetiredRuntimeCleanup() error = %v", err)
	}
	select {
	case <-callbackRan:
	default:
		t.Fatal("callback did not run on retry")
	}
}

func TestWithRetiredRuntimeCleanupRetainsEarlierCleanupFailure(t *testing.T) {
	closeErr := errors.New("old runtime close failed")
	factory := &retiredCleanupFactory{plans: []retiredCleanupPlan{{err: closeErr}}}
	registry := retiredCleanupRegistry(t, factory, false)
	activateRetiredCleanupGeneration(t, registry, "generation_cleanup_1")
	activateRetiredCleanupGeneration(t, registry, "generation_cleanup_2")
	firstExpected := retiredCleanupIdentity(t, "generation_cleanup_2")
	callbackRan := false
	if err := registry.WithRetiredRuntimeCleanup(context.Background(), firstExpected, func() error {
		callbackRan = true
		return nil
	}); !errors.Is(err, closeErr) {
		t.Fatalf("first cleanup error = %v, want retained close failure", err)
	}
	if callbackRan {
		t.Fatal("callback ran despite prior cleanup failure")
	}
	registry.manager.mu.RLock()
	retiredCount := len(registry.manager.retired)
	registry.manager.mu.RUnlock()
	if retiredCount != 0 {
		t.Fatalf("retired runtime count after cleanup completion = %d, want 0", retiredCount)
	}

	activateRetiredCleanupGeneration(t, registry, "generation_cleanup_3")
	secondExpected := retiredCleanupIdentity(t, "generation_cleanup_3")
	callbackRan = false
	if err := registry.WithRetiredRuntimeCleanup(context.Background(), secondExpected, func() error {
		callbackRan = true
		return nil
	}); !errors.Is(err, closeErr) {
		t.Fatalf("later cleanup error = %v, want retained close failure", err)
	}
	if callbackRan {
		t.Fatal("callback ran after an earlier cleanup failure had been removed from retired list")
	}
}

func TestWithRetiredRuntimeCleanupRejectsWrongIdentity(t *testing.T) {
	registry := retiredCleanupRegistry(t, &lifecycleFactory{}, false)
	activateRetiredCleanupGeneration(t, registry, "generation_cleanup_1")
	callbackRan := false
	err := registry.WithRetiredRuntimeCleanup(context.Background(), retiredCleanupIdentity(t, "generation_cleanup_2"), func() error {
		callbackRan = true
		return nil
	})
	if !errors.Is(err, ErrPreparedStale) {
		t.Fatalf("wrong identity error = %v, want ErrPreparedStale", err)
	}
	if callbackRan {
		t.Fatal("callback ran for a mismatched serving identity")
	}
}

func TestWithRetiredRuntimeCleanupHoldsCutoverFenceThroughCallback(t *testing.T) {
	registry := retiredCleanupRegistry(t, &lifecycleFactory{}, false)
	activateRetiredCleanupGeneration(t, registry, "generation_cleanup_1")
	prepared, err := registry.PrepareServingState(t.Context(), "generation_cleanup_2")
	if err != nil {
		t.Fatal(err)
	}

	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	cleanupDone := make(chan error, 1)
	expected := retiredCleanupIdentity(t, "generation_cleanup_1")
	go func() {
		cleanupDone <- registry.WithRetiredRuntimeCleanup(context.Background(), expected, func() error {
			close(callbackStarted)
			<-releaseCallback
			return nil
		})
	}()
	select {
	case <-callbackStarted:
	case <-time.After(time.Second):
		t.Fatal("cleanup callback did not start")
	}

	activationStarted := make(chan struct{})
	activationDone := make(chan error, 1)
	go func() {
		close(activationStarted)
		activationDone <- registry.ActivatePrepared(prepared, func() error { return nil })
	}()
	closeStarted := make(chan struct{})
	closeDone := make(chan error, 1)
	go func() {
		close(closeStarted)
		closeDone <- registry.Close()
	}()
	<-activationStarted
	<-closeStarted
	select {
	case err := <-activationDone:
		t.Fatalf("competing activation completed while callback held cutover fence: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	select {
	case err := <-closeDone:
		t.Fatalf("manager close completed while callback held cutover fence: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseCallback)
	if err := <-cleanupDone; err != nil {
		t.Fatalf("WithRetiredRuntimeCleanup() error = %v", err)
	}
	select {
	case activationErr := <-activationDone:
		_ = activationErr // Either the activation or close may acquire the fence first.
	case <-time.After(time.Second):
		t.Fatal("competing activation did not finish after callback returned")
	}
	select {
	case closeErr := <-closeDone:
		if closeErr != nil {
			t.Fatalf("Registry.Close() error = %v", closeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("registry close did not finish after callback returned")
	}
}

func TestWithRetiredRuntimeCleanupPreservesCallbackError(t *testing.T) {
	registry := retiredCleanupRegistry(t, &lifecycleFactory{}, false)
	activateRetiredCleanupGeneration(t, registry, "generation_cleanup_1")
	callbackErr := errors.New("metadata completion failed")
	err := registry.WithRetiredRuntimeCleanup(context.Background(), retiredCleanupIdentity(t, "generation_cleanup_1"), func() error {
		return callbackErr
	})
	if !errors.Is(err, callbackErr) {
		t.Fatalf("callback error = %v, want %v", err, callbackErr)
	}
}

func TestWithRetiredRuntimeCleanupReturnsCallbackOutcomeAfterCancellation(t *testing.T) {
	registry := retiredCleanupRegistry(t, &lifecycleFactory{}, false)
	activateRetiredCleanupGeneration(t, registry, "generation_cleanup_1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	callbackErr := errors.New("completion callback outcome")
	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	completed := make(chan error, 1)
	expected := retiredCleanupIdentity(t, "generation_cleanup_1")
	go func() {
		completed <- registry.WithRetiredRuntimeCleanup(ctx, expected, func() error {
			close(callbackStarted)
			<-releaseCallback
			return callbackErr
		})
	}()
	select {
	case <-callbackStarted:
	case <-time.After(time.Second):
		t.Fatal("cleanup callback did not start")
	}
	cancel()
	close(releaseCallback)
	select {
	case err := <-completed:
		if !errors.Is(err, callbackErr) {
			t.Fatalf("result after cancellation during callback = %v, want callback error %v", err, callbackErr)
		}
	case <-time.After(time.Second):
		t.Fatal("cleanup method did not return after callback completed")
	}
}

func TestWithRetiredRuntimeCleanupFailsClosedForEmptyInputs(t *testing.T) {
	identity := retiredCleanupIdentity(t, "generation_cleanup_1")
	var nilRegistry *Registry
	if err := nilRegistry.WithRetiredRuntimeCleanup(context.Background(), identity, func() error { return nil }); !errors.Is(err, ErrRegistryClosed) {
		t.Fatalf("nil registry error = %v, want ErrRegistryClosed", err)
	}

	registry := retiredCleanupRegistry(t, &lifecycleFactory{}, false)
	if err := registry.WithRetiredRuntimeCleanup(context.Background(), identity, nil); err == nil {
		t.Fatal("nil callback was accepted")
	}
	if err := registry.WithRetiredRuntimeCleanup(context.Background(), projectgraph.ServingIdentity{}, func() error { return nil }); err == nil {
		t.Fatal("empty identity was accepted")
	}
	if err := registry.WithRetiredRuntimeCleanup(nil, identity, func() error { return nil }); err == nil {
		t.Fatal("nil context was accepted")
	}

	empty := retiredCleanupRegistry(t, &lifecycleFactory{}, true)
	if err := empty.WithRetiredRuntimeCleanup(context.Background(), identity, func() error { return nil }); !errors.Is(err, ErrNoActiveServingState) {
		t.Fatalf("no-active error = %v, want ErrNoActiveServingState", err)
	}

	closed := retiredCleanupRegistry(t, &lifecycleFactory{}, false)
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := closed.WithRetiredRuntimeCleanup(context.Background(), identity, func() error { return nil }); !errors.Is(err, ErrRegistryClosed) {
		t.Fatalf("closed registry error = %v, want ErrRegistryClosed", err)
	}
}
