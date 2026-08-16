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

type lifecycleRuntime struct {
	closed        chan struct{}
	verifyErr     error
	authorization accesssnapshot.AuthorizationSnapshot
}

func (r *lifecycleRuntime) Close() error {
	select {
	case <-r.closed:
	default:
		close(r.closed)
	}
	return nil
}
func (r *lifecycleRuntime) Verify(context.Context) error { return r.verifyErr }
func (r *lifecycleRuntime) AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot {
	return r.authorization
}

type lifecycleFactory struct {
	mu       sync.Mutex
	runtimes []*lifecycleRuntime
	fail     error
}

func (f *lifecycleFactory) Prepare(_ context.Context, input RuntimeInput) (PreparedRuntime, error) {
	if f.fail != nil {
		return nil, f.fail
	}
	identity, err := projectgraph.NewServingIdentity(input.State.ProjectID, string(servingstate.NormalizeEnvironment(input.State.Environment)), string(input.State.ID))
	if err != nil {
		return nil, err
	}
	project, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: input.State.ProjectID, Kind: projectgraph.KindProject, Name: "demo"}}, nil)
	if err != nil {
		return nil, err
	}
	authorization, err := accesssnapshot.NewAuthorizationSnapshot(identity, project, nil, nil)
	if err != nil {
		return nil, err
	}
	r := &lifecycleRuntime{closed: make(chan struct{}), authorization: authorization}
	f.mu.Lock()
	f.runtimes = append(f.runtimes, r)
	f.mu.Unlock()
	return r, nil
}

func (f *lifecycleFactory) last() *lifecycleRuntime {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.runtimes) == 0 {
		return nil
	}
	return f.runtimes[len(f.runtimes)-1]
}

type lifecycleRepo struct {
	state     servingstate.State
	artifact  servingstate.Artifact
	states    map[servingstate.ID]servingstate.State
	artifacts map[servingstate.ID]servingstate.Artifact
	leases    int
	noActive  bool
}

func (r *lifecycleRepo) ActiveArtifact(context.Context, projectgraph.ResourceID, servingstate.Environment) (servingstate.State, servingstate.Artifact, error) {
	if r.noActive {
		return servingstate.State{}, servingstate.Artifact{}, servingstate.ErrNotFound
	}
	return r.state, r.artifact, nil
}
func (r *lifecycleRepo) ByID(_ context.Context, id servingstate.ID) (servingstate.State, error) {
	if r.states != nil {
		state, ok := r.states[id]
		if !ok {
			return servingstate.State{}, servingstate.ErrNotFound
		}
		return state, nil
	}
	if id != r.state.ID {
		return servingstate.State{}, servingstate.ErrNotFound
	}
	return r.state, nil
}
func (r *lifecycleRepo) ArtifactByServingState(_ context.Context, id servingstate.ID) (servingstate.Artifact, error) {
	if r.artifacts != nil {
		artifact, ok := r.artifacts[id]
		if !ok {
			return servingstate.Artifact{}, servingstate.ErrNotFound
		}
		return artifact, nil
	}
	if id != r.artifact.ServingStateID {
		return servingstate.Artifact{}, servingstate.ErrNotFound
	}
	return r.artifact, nil
}
func (r *lifecycleRepo) RecordDuckLakeSnapshot(context.Context, servingstate.ID, int64) error {
	return nil
}
func (r *lifecycleRepo) CreateQuerySnapshotLease(context.Context, servingstate.SnapshotLeaseInput) (string, error) {
	r.leases++
	return "lease", nil
}
func (r *lifecycleRepo) ReleaseQuerySnapshotLease(context.Context, string) error { return nil }
func (r *lifecycleRepo) ExtendQuerySnapshotLease(context.Context, string, time.Time) error {
	return nil
}

type lifecycleAuth struct {
	calls      int
	project    projectgraph.ResourceID
	env        servingstate.Environment
	generation servingstate.ID
	snapshot   accesssnapshot.AuthorizationSnapshot
	err        error
}

func (a *lifecycleAuth) InstallAuthorizationSnapshot(_ context.Context, snapshot accesssnapshot.AuthorizationSnapshot) error {
	a.calls++
	a.project = snapshot.Identity().ProjectID
	a.env = servingstate.Environment(snapshot.Identity().Environment)
	a.generation = servingstate.ID(snapshot.Identity().GenerationID)
	a.snapshot = snapshot
	return a.err
}

func TestProjectEnvironmentLifecyclePublishesAndDrains(t *testing.T) {
	repo := &lifecycleRepo{state: servingstate.State{ID: "generation_1", ProjectID: "project_demo", Environment: "prod", Status: servingstate.StatusValidated, AccessPolicyJSON: `{"revision":1}`}, artifact: servingstate.Artifact{ID: "artifact_1", ServingStateID: "generation_1", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	auth := &lifecycleAuth{}
	registry := NewRegistryWithFactory(RegistryOptions{Repo: repo, ProjectID: "project_demo", Environment: "prod", Factory: &lifecycleFactory{}, Authorization: auth})
	defer registry.Close()
	prepared, err := registry.PrepareServingState(t.Context(), "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ActivatePrepared(prepared, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	lease, err := registry.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if lease.Identity().GenerationID != "generation_1" {
		t.Fatalf("generation = %s", lease.Identity().GenerationID)
	}
	if auth.calls != 1 || auth.project != "project_demo" || auth.env != "prod" || auth.generation != "generation_1" {
		t.Fatalf("authorization install = %#v", auth)
	}
	lease.Release()
}

func TestAuthorizationInstallFailureLeavesGenerationPrivate(t *testing.T) {
	repo := &lifecycleRepo{state: servingstate.State{ID: "generation_1", ProjectID: "project_demo", Environment: "prod", Status: servingstate.StatusValidated}, artifact: servingstate.Artifact{ID: "artifact_1", ServingStateID: "generation_1", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	auth := &lifecycleAuth{err: errors.New("authorization unavailable")}
	registry := NewRegistryWithFactory(RegistryOptions{Repo: repo, ProjectID: "project_demo", Environment: "prod", Factory: &lifecycleFactory{}, Authorization: auth})
	defer registry.Close()
	prepared, err := registry.PrepareServingState(t.Context(), "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ActivatePrepared(prepared, func() error { return nil }); err == nil {
		t.Fatal("expected authorization install failure")
	}
	if _, err := registry.Acquire(t.Context()); err == nil {
		t.Fatal("failed activation exposed a runtime")
	}
}

func TestMissingAuthorizationInstallerFailsClosed(t *testing.T) {
	repo := &lifecycleRepo{state: servingstate.State{ID: "generation_1", ProjectID: "project_demo", Environment: "prod", Status: servingstate.StatusValidated}, artifact: servingstate.Artifact{ID: "artifact_1", ServingStateID: "generation_1", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	registry := NewRegistryWithFactory(RegistryOptions{Repo: repo, ProjectID: "project_demo", Environment: "prod", Factory: &lifecycleFactory{}})
	defer registry.Close()
	prepared, err := registry.PrepareServingState(t.Context(), "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ActivatePrepared(prepared, func() error { return nil }); err == nil {
		t.Fatal("expected missing authorization installer failure")
	}
	if _, err := registry.Acquire(t.Context()); err == nil {
		t.Fatal("missing installer exposed runtime")
	}
}

func TestPrepareOrDurableActivationFailureKeepsPriorGeneration(t *testing.T) {
	repo := &lifecycleRepo{state: servingstate.State{ID: "generation_1", ProjectID: "project_demo", Environment: "prod", Status: servingstate.StatusValidated}, artifact: servingstate.Artifact{ID: "artifact_1", ServingStateID: "generation_1", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	factory := &lifecycleFactory{}
	registry := NewRegistryWithFactory(RegistryOptions{Repo: repo, ProjectID: "project_demo", Environment: "prod", Factory: factory, Authorization: &lifecycleAuth{}})
	defer registry.Close()
	first, err := registry.PrepareServingState(t.Context(), "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ActivatePrepared(first, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	repo.state.ID = "generation_2"
	repo.artifact.ServingStateID = "generation_2"
	repo.artifact.Digest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	factory.fail = errors.New("factory failed")
	if _, err := registry.PrepareServingState(t.Context(), "generation_2"); err == nil {
		t.Fatal("expected factory failure")
	}
	factory.fail = nil
	lease, err := registry.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if lease.Identity().GenerationID != "generation_1" {
		t.Fatalf("prior generation replaced after prepare failure: %s", lease.Identity().GenerationID)
	}
	lease.Release()
	second, err := registry.PrepareServingState(t.Context(), "generation_2")
	if err != nil {
		t.Fatal(err)
	}
	callbackErr := errors.New("durable activation failed")
	if err := registry.ActivatePrepared(second, func() error { return callbackErr }); err == nil {
		t.Fatal("expected activation callback failure")
	}
	lease, err = registry.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if lease.Identity().GenerationID != "generation_1" {
		t.Fatalf("prior generation replaced after callback failure")
	}
	lease.Release()
}

func TestRetiredRuntimeDrainsAfterReaderRelease(t *testing.T) {
	repo := &lifecycleRepo{state: servingstate.State{ID: "generation_1", ProjectID: "project_demo", Environment: "prod", Status: servingstate.StatusValidated}, artifact: servingstate.Artifact{ID: "artifact_1", ServingStateID: "generation_1", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	factory := &lifecycleFactory{}
	registry := NewRegistryWithFactory(RegistryOptions{Repo: repo, ProjectID: "project_demo", Environment: "prod", Factory: factory, Authorization: &lifecycleAuth{}})
	defer registry.Close()
	first, err := registry.PrepareServingState(t.Context(), "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ActivatePrepared(first, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	reader, err := registry.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	old := factory.last()
	repo.state.ID = "generation_2"
	repo.artifact.ServingStateID = "generation_2"
	repo.artifact.Digest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	second, err := registry.PrepareServingState(t.Context(), "generation_2")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ActivatePrepared(second, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-old.closed:
		t.Fatal("retired runtime closed while reader was leased")
	default:
	}
	reader.Release()
	deadline := time.After(time.Second)
	for {
		select {
		case <-old.closed:
			return
		case <-deadline:
			t.Fatal("retired runtime did not drain")
		}
	}
}

func TestCloseWaitsForActiveReaderBeforeSnapshotQueueShutdown(t *testing.T) {
	repo := &lifecycleRepo{state: servingstate.State{ID: "generation_1", ProjectID: "project_demo", Environment: "prod", Status: servingstate.StatusValidated, DuckLakeSnapshotID: 42}, artifact: servingstate.Artifact{ID: "artifact_1", ServingStateID: "generation_1", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	registry := NewRegistryWithFactory(RegistryOptions{Repo: repo, ProjectID: "project_demo", Environment: "prod", Factory: &lifecycleFactory{}, Authorization: &lifecycleAuth{}, CleanupDrainTimeout: time.Second})
	prepared, err := registry.PrepareServingState(t.Context(), "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ActivatePrepared(prepared, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	lease, err := registry.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- registry.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("close returned while reader active: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	lease.Release()
	if err := <-closed; err != nil {
		t.Fatalf("close after reader release = %v", err)
	}
}

func TestCloseTimeoutPreservesReleaseQueueUntilReaderDrains(t *testing.T) {
	repo := &lifecycleRepo{state: servingstate.State{ID: "generation_1", ProjectID: "project_demo", Environment: "prod", Status: servingstate.StatusValidated, DuckLakeSnapshotID: 42}, artifact: servingstate.Artifact{ID: "artifact_1", ServingStateID: "generation_1", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	registry := NewRegistryWithFactory(RegistryOptions{Repo: repo, ProjectID: "project_demo", Environment: "prod", Factory: &lifecycleFactory{}, Authorization: &lifecycleAuth{}, CleanupDrainTimeout: 20 * time.Millisecond})
	prepared, err := registry.PrepareServingState(t.Context(), "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ActivatePrepared(prepared, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	lease, err := registry.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	closeErr := registry.Close()
	if closeErr == nil {
		t.Fatal("expected bounded close timeout")
	}
	lease.Release()
	deadline := time.After(time.Second)
	for {
		if registry.manager.SnapshotLeaseReleaseBacklog() == 0 {
			return
		}
		select {
		case <-deadline:
			t.Fatal("release queue did not drain after reader release")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestStalePreparedGenerationCannotOverwriteNewerActive(t *testing.T) {
	state1 := servingstate.State{ID: "generation_1", ProjectID: "project_demo", Environment: "prod", Status: servingstate.StatusValidated}
	state2 := state1
	state2.ID = "generation_2"
	repo := &lifecycleRepo{state: state1, artifact: servingstate.Artifact{ID: "artifact_1", ServingStateID: "generation_1", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, states: map[servingstate.ID]servingstate.State{"generation_1": state1, "generation_2": state2}, artifacts: map[servingstate.ID]servingstate.Artifact{"generation_1": {ID: "artifact_1", ServingStateID: "generation_1", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, "generation_2": {ID: "artifact_2", ServingStateID: "generation_2", Digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}}
	registry := NewRegistryWithFactory(RegistryOptions{Repo: repo, ProjectID: "project_demo", Environment: "prod", Factory: &lifecycleFactory{}, Authorization: &lifecycleAuth{}})
	defer registry.Close()
	prepared, err := registry.PrepareServingState(t.Context(), "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	other, err := registry.PrepareServingState(t.Context(), "generation_2")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ActivatePrepared(other, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := registry.ActivatePrepared(prepared, func() error { return nil }); err == nil {
		t.Fatal("expected stale prepared rejection")
	}
	lease, err := registry.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if lease.Identity().GenerationID != "generation_2" {
		t.Fatal("stale candidate changed active generation")
	}
	lease.Release()
}

func TestProjectAndEnvironmentMismatchRejected(t *testing.T) {
	repo := &lifecycleRepo{state: servingstate.State{ID: "generation_1", ProjectID: "other_project", Environment: "prod", Status: servingstate.StatusValidated}, artifact: servingstate.Artifact{ServingStateID: "generation_1", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	registry := NewRegistryWithFactory(RegistryOptions{Repo: repo, ProjectID: "project_demo", Environment: "prod", Factory: &lifecycleFactory{}, Authorization: &lifecycleAuth{}})
	defer registry.Close()
	if _, err := registry.PrepareServingState(t.Context(), "generation_1"); err == nil {
		t.Fatal("expected project mismatch")
	}
	repo.state.ProjectID = "project_demo"
	repo.state.Environment = "staging"
	if _, err := registry.PrepareServingState(t.Context(), "generation_1"); err == nil {
		t.Fatal("expected environment mismatch")
	}
	repo.state.Environment = "prod"
	repo.artifact.Digest = "sha256:not-a-digest"
	if _, err := registry.PrepareServingState(t.Context(), "generation_1"); err == nil {
		t.Fatal("expected malformed artifact digest rejection")
	}
}

func TestReloadNoChangeAndCloseAreIdempotent(t *testing.T) {
	repo := &lifecycleRepo{state: servingstate.State{ID: "generation_1", ProjectID: "project_demo", Environment: "prod", Status: servingstate.StatusActive}, artifact: servingstate.Artifact{ID: "artifact_1", ServingStateID: "generation_1", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	factory := &lifecycleFactory{}
	registry := NewRegistryWithFactory(RegistryOptions{Repo: repo, ProjectID: "project_demo", Environment: "prod", Factory: factory, Authorization: &lifecycleAuth{}})
	if err := registry.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(factory.runtimes) != 1 {
		t.Fatalf("reload prepares %d runtimes", len(factory.runtimes))
	}
	if err := registry.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(factory.runtimes) != 1 {
		t.Fatal("unchanged generation was rebuilt")
	}
	repo.noActive = true
	if err := registry.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Acquire(t.Context()); err == nil {
		t.Fatal("runtime remained active after repository reported no generation")
	}
	repo.noActive = false
	repo.state.Status = servingstate.StatusActive
	if err := registry.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	lease, err := registry.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
}
