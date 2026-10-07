package runtimehost

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type lateSnapshotLeaseRepo struct {
	*lifecycleRepo
	started chan context.Context
	finish  chan struct{}
}

func (r *lateSnapshotLeaseRepo) ExtendQuerySnapshotLease(ctx context.Context, _ string, _ time.Time) error {
	r.started <- ctx
	<-r.finish
	return ctx.Err()
}

type expiringSnapshotLeaseRepo struct {
	*lifecycleRepo
}

func (*expiringSnapshotLeaseRepo) ExtendQuerySnapshotLease(ctx context.Context, _ string, _ time.Time) error {
	<-ctx.Done()
	return ctx.Err()
}

func snapshotLeaseHealthRegistry(t *testing.T, onDrained func(servingstate.ID, int64)) (*Registry, *expiringSnapshotLeaseRepo, <-chan error) {
	t.Helper()
	first := servingstate.State{ID: "generation_1", ProjectID: "project_demo", Environment: "prod", Status: servingstate.StatusValidated, Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", DuckLakeSnapshotID: 42}
	second := first
	second.ID, second.Digest, second.DuckLakeSnapshotID = "generation_2", "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 0
	firstArtifact := servingstate.Artifact{ID: "artifact_1", ServingStateID: first.ID, Digest: first.Digest}
	secondArtifact := servingstate.Artifact{ID: "artifact_2", ServingStateID: second.ID, Digest: second.Digest}
	repo := &expiringSnapshotLeaseRepo{lifecycleRepo: &lifecycleRepo{
		state: first, artifact: firstArtifact,
		states:    map[servingstate.ID]servingstate.State{first.ID: first, second.ID: second},
		artifacts: map[servingstate.ID]servingstate.Artifact{first.ID: firstArtifact, second.ID: secondArtifact},
		releaseCh: make(chan string, 1),
	}}
	expired := make(chan error, 1)
	registry := NewRegistryWithFactory(RegistryOptions{
		Repo: repo, ProjectID: "project_demo", Environment: "prod", Factory: &lifecycleFactory{}, Authorization: &lifecycleAuth{},
		LeaseTTL: 25 * time.Millisecond, OnDrained: onDrained,
		OnLeaseRenewalFailure: func(err error) {
			if err != nil {
				expired <- err
			}
		},
	})
	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Errorf("registry cleanup: %v", err)
		}
	})
	return registry, repo, expired
}

func waitSnapshotExpiry(t *testing.T, expired <-chan error) {
	t.Helper()
	select {
	case err := <-expired:
		if err == nil {
			t.Fatal("terminal expiry did not report a failure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("lease heartbeat did not reach confirmed expiry")
	}
}

func TestSnapshotLeaseHealthClearsAfterReaderDrain(t *testing.T) {
	synctest.Test(t, testSnapshotLeaseHealthClearsAfterReaderDrain)
}

func testSnapshotLeaseHealthClearsAfterReaderDrain(t *testing.T) {
	drained := make(chan servingstate.ID, 2)
	registry, repo, expired := snapshotLeaseHealthRegistry(t, func(id servingstate.ID, _ int64) { drained <- id })
	first, err := registry.PrepareServingState(t.Context(), "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if err := registry.ActivatePrepared(first, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	reader, err := registry.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Release()
	waitSnapshotExpiry(t, expired)
	second, err := registry.PrepareServingState(t.Context(), "generation_2")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := registry.ActivatePrepared(second, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := registry.LeaseRenewalError(); err == nil {
		t.Fatal("expired snapshot became healthy while its reader was still held")
	}
	reader.Release()
	select {
	case id := <-drained:
		if id != "generation_1" {
			t.Fatalf("drained generation = %s, want generation_1", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("old generation did not drain")
	}
	if err := registry.LeaseRenewalError(); err != nil {
		t.Fatalf("health after expired snapshot drained = %v, want nil", err)
	}
	select {
	case id := <-repo.releaseCh:
		if id != "lease" {
			t.Fatalf("released snapshot lease = %s, want lease", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("drained snapshot lease was not released")
	}
}

func TestSnapshotLeaseHealthClearsClosedPreparation(t *testing.T) {
	synctest.Test(t, testSnapshotLeaseHealthClearsClosedPreparation)
}

func testSnapshotLeaseHealthClearsClosedPreparation(t *testing.T) {
	registry, repo, expired := snapshotLeaseHealthRegistry(t, nil)
	prepared, err := registry.PrepareServingState(t.Context(), "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	waitSnapshotExpiry(t, expired)
	if err := registry.LeaseRenewalError(); err == nil {
		t.Fatal("expired private preparation did not fail health")
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
	if err := registry.LeaseRenewalError(); err != nil {
		t.Fatalf("closed preparation retained expired lease health: %v", err)
	}
	select {
	case <-repo.releaseCh:
	case <-time.After(5 * time.Second):
		t.Fatal("closed preparation's snapshot lease was not released")
	}
}

func TestSnapshotLeaseHealthIgnoresRenewalReturningAfterClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo := &lateSnapshotLeaseRepo{lifecycleRepo: &lifecycleRepo{}, started: make(chan context.Context, 1), finish: make(chan struct{})}
		finish := func() {
			select {
			case <-repo.finish:
			default:
				close(repo.finish)
			}
		}
		defer finish()
		callbacks := make(chan error, 1)
		manager := NewManagerWithFactory(ManagerOptions{Repo: repo, LeaseTTL: 25 * time.Millisecond, OnLeaseRenewalFailure: func(err error) { callbacks <- err }})
		defer manager.Close()
		lease, err := manager.createPersistentLease(t.Context(), "generation_1", 42)
		if err != nil || lease == nil {
			t.Fatalf("create snapshot lease = %v, %v", lease, err)
		}
		defer lease.Close()
		var renewalCtx context.Context
		select {
		case renewalCtx = <-repo.started:
		case <-time.After(time.Second):
			t.Fatal("snapshot renewal did not start")
		}
		// The provider response outlives both the confirmed expiry and teardown.
		// A closed lease must not become unhealthy when that response arrives.
		time.Sleep(50 * time.Millisecond)
		if renewalCtx.Err() != context.DeadlineExceeded {
			t.Fatalf("renewal context = %v, want confirmed expiry", renewalCtx.Err())
		}
		if err := lease.Close(); err != nil {
			t.Fatal(err)
		}
		finish()
		synctest.Wait()
		if err := manager.LeaseRenewalError(); err != nil {
			t.Fatalf("late renewal recreated closed snapshot health: %v", err)
		}
		select {
		case err := <-callbacks:
			t.Fatalf("closed snapshot notified lease observer: %v", err)
		default:
		}
	})
}
