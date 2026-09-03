package module

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

const reconcilerDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type reconcilerRepo struct {
	mu        sync.Mutex
	states    map[servingstate.ID]servingstate.State
	artifacts map[servingstate.ID]servingstate.Artifact
}

func (r *reconcilerRepo) ActiveArtifact(context.Context, projectgraph.ResourceID, servingstate.Environment) (servingstate.State, servingstate.Artifact, error) {
	return servingstate.State{}, servingstate.Artifact{}, servingstate.ErrNotFound
}
func (r *reconcilerRepo) ByID(_ context.Context, id servingstate.ID) (servingstate.State, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.states[id]
	if !ok {
		return servingstate.State{}, servingstate.ErrNotFound
	}
	return state, nil
}
func (r *reconcilerRepo) ArtifactByServingState(_ context.Context, id servingstate.ID) (servingstate.Artifact, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	artifact, ok := r.artifacts[id]
	if !ok {
		return servingstate.Artifact{}, servingstate.ErrNotFound
	}
	return artifact, nil
}
func (*reconcilerRepo) RecordDuckLakeSnapshot(context.Context, servingstate.ID, int64) error {
	return nil
}

type reconcilerRuntime struct {
	authorization accesssnapshot.AuthorizationSnapshot
}

func (reconcilerRuntime) Close() error { return nil }
func (r reconcilerRuntime) AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot {
	return r.authorization
}

type reconcilerFactory struct{}

func (reconcilerFactory) Prepare(context.Context, runtimehost.RuntimeInput) (runtimehost.PreparedRuntime, error) {
	return nil, errors.New("legacy prepare should not be called")
}
func (reconcilerFactory) PrepareSealed(_ context.Context, input runtimehost.RuntimeInput) (runtimehost.PreparedRuntime, error) {
	identity, err := projectgraph.NewServingIdentity(input.State.ProjectID, string(input.State.Environment), string(input.State.ID))
	if err != nil {
		return nil, err
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: input.State.ProjectID, Kind: projectgraph.KindProject, Name: "project"}}, nil)
	if err != nil {
		return nil, err
	}
	authorization, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, nil, nil)
	if err != nil {
		return nil, err
	}
	return reconcilerRuntime{authorization: authorization}, nil
}

type reconcilerAuth struct{}

func (reconcilerAuth) InstallAuthorizationSnapshot(context.Context, accesssnapshot.AuthorizationSnapshot) error {
	return nil
}

func reconcilerGeneration(id servingstate.ID) (servingstate.State, servingstate.Artifact) {
	return servingstate.State{ID: id, ProjectID: "project", Environment: "prod", Status: servingstate.StatusValidated, Digest: reconcilerDigest}, servingstate.Artifact{ID: "artifact-" + string(id), ServingStateID: id, Digest: reconcilerDigest}
}

func reconcilerRegistry(t *testing.T, ids ...servingstate.ID) *runtimehost.Registry {
	t.Helper()
	repo := &reconcilerRepo{states: map[servingstate.ID]servingstate.State{}, artifacts: map[servingstate.ID]servingstate.Artifact{}}
	for _, id := range ids {
		state, artifact := reconcilerGeneration(id)
		repo.states[id], repo.artifacts[id] = state, artifact
	}
	registry := runtimehost.NewRegistryWithFactory(runtimehost.RegistryOptions{Repo: repo, ProjectID: "project", Environment: "prod", Factory: reconcilerFactory{}, Authorization: reconcilerAuth{}, RequireSealedCatalog: true})
	t.Cleanup(func() { _ = registry.Close() })
	return registry
}

func TestActiveReconcilerConvergesAfterMissedNotification(t *testing.T) {
	registry := reconcilerRegistry(t, "generation-1", "generation-2")
	if err := registry.ReconcileSealed(t.Context(), "generation-1"); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	current := servingstate.ID("generation-1")
	module := &Module{registry: registry}
	if err := module.reconcileActive(t.Context(), func(context.Context) (servingstate.ID, error) { return "generation-2", nil }); err != nil {
		t.Fatalf("active reconcile: %v", err)
	}
	if got := registry.CurrentServingStateID(); got != "generation-2" {
		t.Fatalf("reconciled generation = %q, want generation-2 (previous=%s)", got, current)
	}
}

func TestActiveReconcilerRetiresAfterConfirmedMissingPointer(t *testing.T) {
	registry := reconcilerRegistry(t, "generation-1")
	if err := registry.ReconcileSealed(t.Context(), "generation-1"); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	module := &Module{registry: registry}
	if err := module.reconcileActive(t.Context(), func(context.Context) (servingstate.ID, error) { return "", servingstate.ErrNotFound }); err != nil {
		t.Fatalf("active reconcile: %v", err)
	}
	if got := registry.CurrentServingStateID(); got != "" {
		t.Fatalf("generation remained active after confirmed absent pointer: %q", got)
	}
}

func TestActiveReconcilerRejectsEmptyPointer(t *testing.T) {
	module := &Module{registry: reconcilerRegistry(t)}
	err := module.reconcileActive(t.Context(), func(context.Context) (servingstate.ID, error) { return "", nil })
	if err == nil {
		t.Fatal("empty active pointer was accepted")
	}
}

func TestActiveReconcilerShutdownCancelsTicker(t *testing.T) {
	registry := reconcilerRegistry(t, "generation-1")
	if err := registry.ReconcileSealed(t.Context(), "generation-1"); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	module := &Module{registry: registry, reconcileStop: make(chan struct{}), reconcileDone: make(chan struct{}), reapStop: make(chan struct{}), reapDone: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		defer close(module.reconcileDone)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = module.reconcileActive(ctx, func(context.Context) (servingstate.ID, error) { return "generation-1", nil })
			case <-module.reconcileStop:
				return
			}
		}
	}()
	close(module.reconcileStop)
	select {
	case <-module.reconcileDone:
	case <-time.After(time.Second):
		t.Fatal("active reconciler did not stop")
	}
}

func TestBuildActiveReconcilerConvergesAndCloseDoesNotWaitOnResolver(t *testing.T) {
	repo := &reconcilerRepo{states: map[servingstate.ID]servingstate.State{}, artifacts: map[servingstate.ID]servingstate.Artifact{}}
	for _, id := range []servingstate.ID{"generation-1", "generation-2"} {
		state, artifact := reconcilerGeneration(id)
		repo.states[id], repo.artifacts[id] = state, artifact
	}
	var activeMu sync.Mutex
	active := servingstate.ID("generation-1")
	resolverStarted := make(chan struct{})
	releaseResolver := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseResolver) }) }
	t.Cleanup(release)
	var startedOnce sync.Once
	resolve := func(_ context.Context) (servingstate.ID, error) {
		activeMu.Lock()
		id := active
		activeMu.Unlock()
		if id == "blocked" {
			startedOnce.Do(func() { close(resolverStarted) })
			<-releaseResolver // deliberately ignores cancellation
			return "", servingstate.ErrNotFound
		}
		return id, nil
	}
	module, err := Build(t.Context(), Config{
		States: repo, ProjectID: "project", Environment: "prod", Factory: reconcilerFactory{}, Authorization: reconcilerAuth{},
		RequireSealedCatalog: true, ResolveSealedActiveState: resolve, ActiveReconcileInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	t.Cleanup(func() { _ = module.Close() })
	activeMu.Lock()
	active = "generation-2"
	activeMu.Unlock()
	deadline := time.Now().Add(time.Second)
	for module.registry.CurrentServingStateID() != "generation-2" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := module.registry.CurrentServingStateID(); got != "generation-2" {
		t.Fatalf("reconciler generation = %q, want generation-2", got)
	}
	activeMu.Lock()
	active = "blocked"
	activeMu.Unlock()
	select {
	case <-resolverStarted:
	case <-time.After(time.Second):
		t.Fatal("reconciler did not enter blocked resolver")
	}
	started := time.Now()
	if err := module.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("Close() waited %s on blocked resolver", elapsed)
	}
	release()
}

func TestBuildActiveReconcilerCloseDoesNotWaitOnMissingConfirmation(t *testing.T) {
	repo := &reconcilerRepo{states: map[servingstate.ID]servingstate.State{}, artifacts: map[servingstate.ID]servingstate.Artifact{}}
	state, artifact := reconcilerGeneration("generation-1")
	repo.states["generation-1"], repo.artifacts["generation-1"] = state, artifact
	resolverStarted := make(chan struct{})
	releaseResolver := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseResolver) }) }
	t.Cleanup(release)
	var startedOnce sync.Once
	var calls atomic.Int32
	resolve := func(_ context.Context) (servingstate.ID, error) {
		call := calls.Add(1)
		if call == 1 {
			return "generation-1", nil // startup load
		}
		if call == 2 {
			return "", servingstate.ErrNotFound // pass's first authoritative read
		}
		// The third read is the under-lock confirmation and deliberately ignores
		// cancellation to prove Manager.Close cannot be held by cutoverMu.
		startedOnce.Do(func() { close(resolverStarted) })
		<-releaseResolver
		return "", servingstate.ErrNotFound
	}
	module, err := Build(t.Context(), Config{
		States: repo, ProjectID: "project", Environment: "prod", Factory: reconcilerFactory{}, Authorization: reconcilerAuth{},
		RequireSealedCatalog: true, ResolveSealedActiveState: resolve, ActiveReconcileInterval: time.Millisecond, ActiveReconcileTimeout: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	t.Cleanup(func() { _ = module.Close() })
	select {
	case <-resolverStarted:
	case <-time.After(time.Second):
		t.Fatal("confirmation resolver did not block")
	}
	started := time.Now()
	if err := module.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("Close() waited %s on blocked missing confirmation", elapsed)
	}
	release()
}

func TestBuildActiveReconcilerTimeoutAllowsSubsequentPolls(t *testing.T) {
	repo := &reconcilerRepo{states: map[servingstate.ID]servingstate.State{}, artifacts: map[servingstate.ID]servingstate.Artifact{}}
	state, artifact := reconcilerGeneration("generation-1")
	repo.states["generation-1"], repo.artifacts["generation-1"] = state, artifact
	confirmationStarted := make(chan struct{})
	progress := make(chan struct{})
	var startedOnce, progressOnce sync.Once
	var calls atomic.Int32
	resolve := func(ctx context.Context) (servingstate.ID, error) {
		call := calls.Add(1)
		if call == 1 {
			return "generation-1", nil // startup load
		}
		if call == 2 {
			return "", servingstate.ErrNotFound // pass's first authoritative read
		}
		if call == 3 {
			startedOnce.Do(func() { close(confirmationStarted) })
			<-ctx.Done() // context-aware wedge is bounded by ActiveReconcileTimeout
			return "", ctx.Err()
		}
		progressOnce.Do(func() { close(progress) })
		return "", servingstate.ErrNotFound
	}
	module, err := Build(t.Context(), Config{
		States: repo, ProjectID: "project", Environment: "prod", Factory: reconcilerFactory{}, Authorization: reconcilerAuth{},
		RequireSealedCatalog: true, ResolveSealedActiveState: resolve, ActiveReconcileInterval: time.Millisecond, ActiveReconcileTimeout: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	t.Cleanup(func() { _ = module.Close() })
	select {
	case <-confirmationStarted:
	case <-time.After(time.Second):
		t.Fatal("confirmation resolver did not start")
	}
	select {
	case <-progress:
	case <-time.After(time.Second):
		t.Fatal("active reconciler did not poll again after timeout")
	}
	if err := module.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}
