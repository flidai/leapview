package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/jobs"
	jobsqlite "github.com/flidai/leapview/internal/platform/jobs/sqlite"
	"github.com/flidai/leapview/internal/platform/transaction"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
)

func TestSQLRunRepositoryRecordsInitialLifecycleInRunTransaction(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.SQLDB().ExecContext(t.Context(), `
INSERT INTO workspaces (id, title) VALUES ('sales', 'Sales');
INSERT INTO serving_states (id, workspace_id, environment, status) VALUES ('state_1', 'sales', 'dev', 'validated');`); err != nil {
		t.Fatal(err)
	}
	events := jobsqlite.NewRepository(store.SQLDB())
	repository := NewSQLRunRepositoryWithWorkflow(store.SQLDB(), events, RunWorkflowConfig{
		ResourceKind: "refresh", InitialEvent: "refresh.queued", InitialState: "queued",
	})
	run, err := repository.CreateRun(t.Context(), refreshrun.RunInput{
		WorkspaceID: "sales", Environment: "dev", ModelID: "sales", ServingStateID: "state_1",
		TargetType: refreshrun.TargetRefreshPipeline, TargetID: "sales.daily",
		TriggerType: refreshrun.TriggerManual, JobKind: refreshrun.JobKindRefreshPipeline,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := events.ListEvents(t.Context(), "refresh", run.ID, 0, 10)
	if err != nil || len(recorded) != 1 || recorded[0].EventType != "refresh.queued" {
		t.Fatalf("initial events = %#v, %v", recorded, err)
	}
}

func TestSQLRunRepositoryRollsBackRunWhenInitialLifecycleFails(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.SQLDB().ExecContext(t.Context(), `
INSERT INTO workspaces (id, title) VALUES ('sales', 'Sales');
INSERT INTO serving_states (id, workspace_id, environment, status) VALUES ('state_1', 'sales', 'dev', 'validated');`); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("initial lifecycle unavailable")
	repository := NewSQLRunRepositoryWithWorkflow(store.SQLDB(), jobs.WorkflowRecorderFunc(func(context.Context, transaction.Transaction, jobs.WorkflowIntent) error {
		return injected
	}), RunWorkflowConfig{ResourceKind: "refresh", InitialEvent: "refresh.queued", InitialState: "queued"})
	_, err = repository.CreateRun(t.Context(), refreshrun.RunInput{
		WorkspaceID: "sales", Environment: "dev", ModelID: "sales", ServingStateID: "state_1",
		TargetType: refreshrun.TargetRefreshPipeline, TargetID: "sales.daily",
		TriggerType: refreshrun.TriggerManual, JobKind: refreshrun.JobKindRefreshPipeline,
	})
	if !errors.Is(err, injected) {
		t.Fatalf("CreateRun() error = %v, want injected failure", err)
	}
	var count int
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM refresh_job_runs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("refresh run count = %d, %v", count, err)
	}
}

func TestSQLRunRepositoryAcceptsActiveLeaseFence(t *testing.T) {
	store, repo, job := seedRefreshJob(t, refreshrun.RunStatusRunning, "+5 minutes")

	if err := repo.RenewJobLease(t.Context(), job, time.Minute); err != nil {
		t.Fatalf("RenewJobLease() error = %v", err)
	}
	if _, err := repo.MarkRunPrepared(t.Context(), job); err != nil {
		t.Fatalf("MarkRunPrepared() error = %v", err)
	}
	allowed, err := repo.RunMayPublish(t.Context(), job)
	if err != nil {
		t.Fatalf("RunMayPublish() error = %v", err)
	}
	if !allowed {
		t.Fatal("RunMayPublish() = false for active lease")
	}
	assertRefreshStatuses(t, store, refreshrun.RunStatusRunning, "prepared")
}

func TestSQLRunRepositoryRejectsExpiredLeaseFence(t *testing.T) {
	t.Run("renew", func(t *testing.T) {
		store, repo, job := seedExpiredRefreshJob(t, refreshrun.RunStatusRunning)

		err := repo.RenewJobLease(t.Context(), job, time.Minute)
		if !errors.Is(err, refreshrun.ErrLeaseLost) {
			t.Fatalf("RenewJobLease() error = %v, want ErrLeaseLost", err)
		}
		assertRefreshStatuses(t, store, refreshrun.RunStatusRunning, refreshrun.RunStatusRunning)
	})

	t.Run("prepare", func(t *testing.T) {
		store, repo, job := seedExpiredRefreshJob(t, refreshrun.RunStatusRunning)

		_, err := repo.MarkRunPrepared(t.Context(), job)
		if !errors.Is(err, refreshrun.ErrLeaseLost) {
			t.Fatalf("MarkRunPrepared() error = %v, want ErrLeaseLost", err)
		}
		assertRefreshStatuses(t, store, refreshrun.RunStatusRunning, refreshrun.RunStatusRunning)
	})

	t.Run("publish eligibility", func(t *testing.T) {
		store, repo, job := seedExpiredRefreshJob(t, "prepared")

		allowed, err := repo.RunMayPublish(t.Context(), job)
		if err != nil {
			t.Fatalf("RunMayPublish() error = %v", err)
		}
		if allowed {
			t.Fatal("RunMayPublish() = true for expired lease")
		}
		assertRefreshStatuses(t, store, refreshrun.RunStatusRunning, "prepared")
	})
	t.Run("terminal transition", func(t *testing.T) {
		store, repo, job := seedExpiredRefreshJob(t, refreshrun.RunStatusRunning)
		if _, err := repo.MarkRunSucceededClaimed(t.Context(), job); !errors.Is(err, refreshrun.ErrLeaseLost) {
			t.Fatalf("terminal success error = %v, want ErrLeaseLost", err)
		}
		assertRefreshStatuses(t, store, refreshrun.RunStatusRunning, refreshrun.RunStatusRunning)
	})
}

func TestSQLRunRepositoryFencesTerminalTransitionsAcrossReclaim(t *testing.T) {
	t.Run("stale success cannot clear reclaimed lease", func(t *testing.T) {
		store, repo, job := seedRefreshJob(t, refreshrun.RunStatusRunning, "+5 minutes")
		if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE refresh_jobs SET lease_expires_at = datetime('now', '-1 second') WHERE id = ?`, job.ID); err != nil {
			t.Fatal(err)
		}
		reclaimed, ok, err := repo.ClaimNextExecutableJob(t.Context(), "dev", "worker-2", time.Minute)
		if err != nil || !ok {
			t.Fatalf("reclaim ok=%v err=%v", ok, err)
		}
		if _, err := repo.MarkRunSucceededClaimed(t.Context(), job); !errors.Is(err, refreshrun.ErrLeaseLost) {
			t.Fatalf("stale success error = %v, want ErrLeaseLost", err)
		}
		assertRefreshStatuses(t, store, refreshrun.RunStatusRunning, refreshrun.RunStatusRunning)
		if _, err := repo.MarkRunSucceededClaimed(t.Context(), reclaimed); err != nil {
			t.Fatalf("current success error = %v", err)
		}
		assertRefreshStatuses(t, store, refreshrun.RunStatusSucceeded, refreshrun.RunStatusSucceeded)
	})

	t.Run("stale failure cannot overwrite reclaimed attempt", func(t *testing.T) {
		store, repo, job := seedRefreshJob(t, refreshrun.RunStatusRunning, "+5 minutes")
		if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE refresh_jobs SET lease_expires_at = datetime('now', '-1 second') WHERE id = ?`, job.ID); err != nil {
			t.Fatal(err)
		}
		reclaimed, ok, err := repo.ClaimNextExecutableJob(t.Context(), "dev", "worker-2", time.Minute)
		if err != nil || !ok {
			t.Fatalf("reclaim ok=%v err=%v", ok, err)
		}
		if _, err := repo.MarkRunFailedClaimed(t.Context(), job, "stale failure"); !errors.Is(err, refreshrun.ErrLeaseLost) {
			t.Fatalf("stale failure error = %v, want ErrLeaseLost", err)
		}
		assertRefreshStatuses(t, store, refreshrun.RunStatusRunning, refreshrun.RunStatusRunning)
		if _, err := repo.MarkRunFailedClaimed(t.Context(), reclaimed, "current failure"); err != nil {
			t.Fatalf("current failure error = %v", err)
		}
		assertRefreshStatuses(t, store, refreshrun.RunStatusFailed, refreshrun.RunStatusFailed)
	})
}

func TestSQLRunRepositoryFailsClaimedRunTreeAtomically(t *testing.T) {
	store, repo, job := seedRefreshJob(t, refreshrun.RunStatusRunning, "+5 minutes")
	if _, err := store.SQLDB().ExecContext(t.Context(), `
INSERT INTO refresh_jobs (id, workspace_id, model_id, kind, status) VALUES ('child_job', 'sales', 'sales', 'child_run', 'queued');
INSERT INTO refresh_job_runs (id, job_id, environment, target_type, target_id, target_generation, trigger_type, parent_run_id, status, created_sequence)
VALUES ('child_run', 'child_job', 'dev', 'model_table', 'sales.orders', 1, 'dependency', 'run_1', 'running', 2);`); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkRunTreeFailedClaimed(t.Context(), job, "pipeline failed"); err != nil {
		t.Fatalf("tree failure = %v", err)
	}
	var rootStatus, childStatus, childJobStatus string
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT status FROM refresh_job_runs WHERE id='run_1'`).Scan(&rootStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT status FROM refresh_job_runs WHERE id='child_run'`).Scan(&childStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT status FROM refresh_jobs WHERE id='child_job'`).Scan(&childJobStatus); err != nil {
		t.Fatal(err)
	}
	if rootStatus != refreshrun.RunStatusFailed || childStatus != refreshrun.RunStatusFailed || childJobStatus != refreshrun.RunStatusFailed {
		t.Fatalf("tree statuses = %q/%q/%q, want failed", rootStatus, childStatus, childJobStatus)
	}
}

func TestSQLRunRepositoryRejectsIneligibleChildTree(t *testing.T) {
	store, repo, job := seedRefreshJob(t, refreshrun.RunStatusRunning, "+5 minutes")
	defer store.Close()
	if _, err := store.SQLDB().ExecContext(t.Context(), `
INSERT INTO refresh_jobs (id, workspace_id, model_id, kind, status) VALUES ('child_job', 'sales', 'sales', 'child_run', 'queued');
INSERT INTO refresh_job_runs (id, job_id, environment, target_type, target_id, target_generation, trigger_type, parent_run_id, status, created_sequence)
VALUES ('child_run', 'child_job', 'dev', 'model_table', 'sales.orders', 1, 'dependency', 'run_1', 'succeeded', 2);`); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkRunTreeFailedClaimed(t.Context(), job, "pipeline failed"); !errors.Is(err, refreshrun.ErrLeaseLost) {
		t.Fatalf("tree failure = %v, want ErrLeaseLost", err)
	}
	assertRefreshStatuses(t, store, refreshrun.RunStatusRunning, refreshrun.RunStatusRunning)
}

func seedExpiredRefreshJob(t *testing.T, runStatus string) (*platform.Store, *SQLRunRepository, refreshrun.JobRecord) {
	t.Helper()
	return seedRefreshJob(t, runStatus, "-1 second")
}

func seedRefreshJob(t *testing.T, runStatus, leaseOffset string) (*platform.Store, *SQLRunRepository, refreshrun.JobRecord) {
	t.Helper()
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.SQLDB().ExecContext(t.Context(), `
INSERT INTO workspaces (id, title) VALUES ('sales', 'Sales');
INSERT INTO refresh_jobs (
  id, workspace_id, model_id, kind, status, lease_owner, lease_generation
) VALUES (
  'job_1', 'sales', 'sales', 'refresh_pipeline', 'running', 'worker-1', 1
);
INSERT INTO refresh_job_runs (
  id, job_id, environment, target_type, target_id, target_generation, trigger_type, status, created_sequence
) VALUES (
  'run_1', 'job_1', 'dev', 'refresh_pipeline', 'sales.daily', 1, 'manual', ?, 1
);`, runStatus); err != nil {
		t.Fatalf("seed refresh job: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(),
		`UPDATE refresh_jobs SET lease_expires_at = datetime('now', ?) WHERE id = 'job_1'`,
		leaseOffset,
	); err != nil {
		t.Fatalf("set refresh job lease: %v", err)
	}
	job := refreshrun.JobRecord{
		ID: "job_1", WorkspaceID: "sales", Environment: "dev", ModelID: "sales",
		Kind: refreshrun.JobKindRefreshPipeline, RunID: "run_1",
		TargetType: refreshrun.TargetRefreshPipeline, TargetID: "sales.daily", TargetGeneration: 1,
		TriggerType: refreshrun.TriggerManual, LeaseOwner: "worker-1", LeaseGeneration: 1,
	}
	return store, NewSQLRunRepository(store.SQLDB()), job
}

func assertRefreshStatuses(t *testing.T, store *platform.Store, wantJob, wantRun string) {
	t.Helper()
	var jobStatus, runStatus string
	if err := store.SQLDB().QueryRowContext(t.Context(), `
SELECT j.status, r.status
FROM refresh_jobs j
JOIN refresh_job_runs r ON r.job_id = j.id
WHERE j.id = 'job_1'`).Scan(&jobStatus, &runStatus); err != nil {
		t.Fatalf("read refresh statuses: %v", err)
	}
	if jobStatus != wantJob || runStatus != wantRun {
		t.Fatalf("refresh statuses = %q/%q, want %q/%q", jobStatus, runStatus, wantJob, wantRun)
	}
}
