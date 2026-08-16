package app

import (
	"context"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/jobs"
	jobsqlite "github.com/flidai/leapview/internal/platform/jobs/sqlite"
)

func TestBackgroundLifecycleReclaimsPersistedAPIJobs(t *testing.T) {
	store := testStore(t)
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{JobLeaseTimeout: time.Second}))
	repo := server.platform.asyncJobs
	if repo == nil {
		t.Fatal("async job repository is required")
	}
	// Seed through persistence to simulate an unknown kind left by a former
	// process version; the module intentionally rejects new unknown enqueues.
	if _, err := jobsqlite.NewRepository(store.SQLDB()).Enqueue(t.Context(), jobs.EnqueueInput{ID: "job-restart", Kind: "test.unsupported", WorkloadClass: "control", PrincipalID: jobs.SystemPrincipalID, GroupIDs: []string{}, EstimatedMemoryBytes: 1, ResourceKind: "test", ResourceID: "resource-1", Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	server.StartBackgroundJobs(ctx)
	t.Cleanup(func() {
		cancel()
		stopCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = server.StopBackgroundJobs(stopCtx)
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, getErr := repo.Get(t.Context(), "job-restart")
		if getErr == nil && job.Status == jobs.StatusFailed {
			if job.Attempts != 1 || job.FinishedAt == "" {
				t.Fatalf("failed job = %#v", job)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("persisted API job was not claimed by the background worker")
}
