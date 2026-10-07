package runtimehost

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type shutdownLeaseRepo struct{ *lifecycleRepo }

func newShutdownLeaseRepo() *shutdownLeaseRepo {
	return &shutdownLeaseRepo{lifecycleRepo: &lifecycleRepo{
		state:    servingstate.State{ID: "generation_1", ProjectID: "project_demo", Environment: "prod", Status: servingstate.StatusValidated, Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", DuckLakeSnapshotID: 42},
		artifact: servingstate.Artifact{ID: "artifact_1", ServingStateID: "generation_1", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}}
}

func (r *shutdownLeaseRepo) CreateQuerySnapshotLease(context.Context, servingstate.SnapshotLeaseInput) (string, error) {
	r.leaseMu.Lock()
	defer r.leaseMu.Unlock()
	r.leases++
	return fmt.Sprintf("lease-%d", r.leases), nil
}

func TestRegistryCloseTimeoutDrainsAllReadersBeforeReleaseQueueShutdown(t *testing.T) {
	for _, order := range []string{"candidate-only", "active-first", "candidate-first"} {
		t.Run(order, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				const drainTimeout = 10 * time.Millisecond
				repo := newShutdownLeaseRepo()
				registry := NewRegistryWithFactory(RegistryOptions{Repo: repo, ProjectID: "project_demo", Environment: "prod", Factory: &lifecycleFactory{}, Authorization: &lifecycleAuth{}, CleanupDrainTimeout: drainTimeout})
				defer registry.Close()
				var active Lease
				if order != "candidate-only" {
					prepared, err := registry.PrepareServingState(t.Context(), "generation_1")
					if err != nil {
						t.Fatal(err)
					}
					if err := registry.ActivatePrepared(prepared, func() error { return nil }); err != nil {
						t.Fatal(err)
					}
					active, err = registry.Acquire(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					defer active.Release()
				}
				registration := candidateRegistration(time.Now().Add(time.Hour))
				registration.Compatibility.ManagedDataConnections = nil
				if err := registry.PrepareAndRegisterCandidate(t.Context(), CandidatePreparation{Registration: registration, Identity: projectgraph.ServingIdentity{ProjectID: "project_demo", Environment: "prod", GenerationID: "generation_1"}}); err != nil {
					t.Fatal(err)
				}
				candidate, err := registry.AcquireCandidate(t.Context(), CandidateLeaseRequest{CandidateID: registration.CandidateID, OwnerID: registration.OwnerID, ProjectID: registration.ProjectID, Compatibility: registration.Compatibility})
				if err != nil {
					t.Fatal(err)
				}
				defer candidate.Release()
				queue := registry.manager.releaseQueue
				// This fixture exercises the legacy unsealed lease repository seam.
				// Sealed PostgreSQL runtimes release their own durable roots.
				if candidate.(*candidateRuntimeLease).generation.managed.sealed {
					t.Fatal("expected an unsealed candidate fixture")
				}
				started := time.Now()
				closeErr := registry.Close()
				if closeErr == nil || !strings.Contains(closeErr.Error(), "candidate cleanup did not drain") {
					t.Fatalf("close error = %v, want candidate drain timeout", closeErr)
				}
				if active != nil && !strings.Contains(closeErr.Error(), "runtime cleanup did not drain") {
					t.Errorf("close error = %v, want active drain timeout too", closeErr)
				}
				if elapsed := time.Since(started); elapsed > 2*drainTimeout {
					t.Errorf("close took %s, want at most two bounded drain waits", elapsed)
				}
				if repeated := registry.Close(); repeated != closeErr {
					t.Errorf("repeated close error = %v, want original error %v", repeated, closeErr)
				}
				assertAccepting := func() {
					t.Helper()
					queue.mu.Lock()
					accepting := queue.accepting
					queue.mu.Unlock()
					if !accepting {
						t.Error("release queue closed while a reader still owns its snapshot lease")
					}
				}
				assertAccepting()
				// Advance the fake clock beyond the former second bounded wait.
				// Wait then synchronizes all runnable cleanup and queue goroutines.
				deadline := time.NewTimer(3 * drainTimeout)
				<-deadline.C
				synctest.Wait()
				assertAccepting()
				if active != nil {
					first, remaining := active, candidate
					if order == "candidate-first" {
						first, remaining = candidate, active
					}
					first.Release()
					first.Release()
					synctest.Wait()
					assertAccepting()
					repo.leaseMu.Lock()
					released := len(repo.releasedLeases)
					repo.leaseMu.Unlock()
					if released != 1 {
						t.Errorf("released leases after first reader drains = %d, want one", released)
					}
					remaining.Release()
					remaining.Release()
				} else {
					candidate.Release()
					candidate.Release()
				}
				synctest.Wait()
				select {
				case <-queue.workerDone:
				default:
					t.Error("release queue did not finish after all readers drained")
				}
				repo.leaseMu.Lock()
				defer repo.leaseMu.Unlock()
				counts := map[string]int{}
				for _, id := range repo.releasedLeases {
					counts[id]++
				}
				for number := 1; number <= repo.leases; number++ {
					id := fmt.Sprintf("lease-%d", number)
					if counts[id] != 1 {
						t.Errorf("repository releases for %s = %d, want exactly one", id, counts[id])
					}
				}
			})
		})
	}
}

func TestManagerCloseTimeoutRetainsReleaseQueueUntilReaderDrains(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo := newShutdownLeaseRepo()
		manager := NewManagerWithFactory(ManagerOptions{Repo: repo, ProjectID: "project_demo", Environment: "prod", Factory: &lifecycleFactory{}, Authorization: &lifecycleAuth{}, CleanupDrainTimeout: 10 * time.Millisecond})
		defer manager.Close()
		prepared, err := manager.PrepareServingState(t.Context(), "generation_1")
		if err != nil {
			t.Fatal(err)
		}
		if err := manager.ActivatePrepared(prepared, func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		reader, err := manager.Acquire(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Release()
		if err := manager.Close(); err == nil || !strings.Contains(err.Error(), "runtime cleanup did not drain") {
			t.Fatalf("manager close error = %v, want bounded reader drain timeout", err)
		}
		synctest.Wait()
		manager.releaseQueue.mu.Lock()
		accepting := manager.releaseQueue.accepting
		manager.releaseQueue.mu.Unlock()
		if !accepting {
			t.Error("standalone manager closed release queue while reader remained")
		}
		reader.Release()
		reader.Release()
		synctest.Wait()
		select {
		case <-manager.releaseQueue.workerDone:
		default:
			t.Error("standalone manager did not close release queue after reader drained")
		}
		repo.leaseMu.Lock()
		defer repo.leaseMu.Unlock()
		if len(repo.releasedLeases) != 1 || repo.releasedLeases[0] != "lease-1" {
			t.Errorf("standalone repository releases = %v, want [lease-1]", repo.releasedLeases)
		}
	})
}
