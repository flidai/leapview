package postgres

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

func TestMarkRunningRejectsStaleRiverAttempt(t *testing.T) {
	repository, pool := cancelClaimedRepository(t)
	job := enqueueClaimedJob(t, repository, pool, "mark-running-stale", "owner-a", 1)
	riverID := jobRiverID(t, pool, job.ID)

	if _, err := pool.Exec(t.Context(), `
		UPDATE public.river_job
		SET attempt = 2, attempted_by = ARRAY['owner-a', 'owner-b']
		WHERE id = $1`, riverID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.MarkRunning(t.Context(), job.ID, 1); !errors.Is(err, jobs.ErrConflict) {
		t.Fatalf("stale MarkRunning() error = %v, want conflict", err)
	}
	assertProductRunning(t, repository, job.ID, 1)
	assertRiverFence(t, pool, riverID, rivertype.JobStateRunning, 2, []string{"owner-a", "owner-b"}, false)
}

func TestMarkRunningRejectsMismatchedRiverOwner(t *testing.T) {
	repository, pool := cancelClaimedRepository(t)
	job := enqueueClaimedJob(t, repository, pool, "mark-running-owner", "owner-a", 1)
	riverID := jobRiverID(t, pool, job.ID)
	if _, err := pool.Exec(t.Context(), `
		UPDATE public.river_job
		SET attempted_by = ARRAY['owner-b']
		WHERE id = $1`, riverID); err != nil {
		t.Fatal(err)
	}

	riverJob := &river.Job[ReleaseFinalizeArgs]{JobRow: &rivertype.JobRow{
		ID: riverID, Attempt: 1, State: rivertype.JobStateRunning, AttemptedBy: []string{"owner-a"},
	}}
	ctx := ContextWithRiverExecution(t.Context(), riverJob, "owner-a", time.Minute)
	if _, err := repository.MarkRunning(ctx, job.ID, 1); !errors.Is(err, jobs.ErrConflict) {
		t.Fatalf("owner-mismatched MarkRunning() error = %v, want conflict", err)
	}
	assertProductRunning(t, repository, job.ID, 1)
	assertRiverFence(t, pool, riverID, rivertype.JobStateRunning, 1, []string{"owner-b"}, false)
}

func TestMarkRunningRejectsOlderProductAttempt(t *testing.T) {
	repository, pool := cancelClaimedRepository(t)
	job := enqueueClaimedJob(t, repository, pool, "mark-running-product-newer", "owner-a", 1)
	riverID := jobRiverID(t, pool, job.ID)
	if _, err := pool.Exec(t.Context(), `UPDATE jobs.job_history SET attempt_count = 2 WHERE id = $1`, job.ID); err != nil {
		t.Fatal(err)
	}
	riverJob := &river.Job[ReleaseFinalizeArgs]{JobRow: &rivertype.JobRow{
		ID: riverID, Attempt: 1, State: rivertype.JobStateRunning, AttemptedBy: []string{"owner-a"},
	}}
	ctx := ContextWithRiverExecution(t.Context(), riverJob, "owner-a", time.Minute)
	if _, err := repository.MarkRunning(ctx, job.ID, 1); !errors.Is(err, jobs.ErrConflict) {
		t.Fatalf("MarkRunning() with newer product attempt error = %v, want conflict", err)
	}
	assertProductRunning(t, repository, job.ID, 2)
	assertRiverFence(t, pool, riverID, rivertype.JobStateRunning, 1, []string{"owner-a"}, false)
}

func TestRequeueAfterFailureRejectsStaleRiverAttempt(t *testing.T) {
	repository, pool := cancelClaimedRepository(t)
	job := enqueueClaimedJob(t, repository, pool, "requeue-stale", "owner-a", 1)
	riverID := jobRiverID(t, pool, job.ID)
	if _, err := pool.Exec(t.Context(), `
		UPDATE public.river_job
		SET attempt = 2, attempted_by = ARRAY['owner-a', 'owner-b']
		WHERE id = $1`, riverID); err != nil {
		t.Fatal(err)
	}
	if err := repository.RequeueAfterFailure(t.Context(), job.ID, 1, []byte(`{"code":"retry"}`)); !errors.Is(err, jobs.ErrConflict) {
		t.Fatalf("stale RequeueAfterFailure() error = %v, want conflict", err)
	}
	assertProductRunning(t, repository, job.ID, 1)
	assertRiverFence(t, pool, riverID, rivertype.JobStateRunning, 2, []string{"owner-a", "owner-b"}, false)
}

func TestRequeueAfterFailureRejectsOlderProductAttempt(t *testing.T) {
	repository, pool := cancelClaimedRepository(t)
	job := enqueueClaimedJob(t, repository, pool, "requeue-product-newer", "owner-a", 1)
	riverID := jobRiverID(t, pool, job.ID)
	if _, err := pool.Exec(t.Context(), `UPDATE jobs.job_history SET attempt_count = 2 WHERE id = $1`, job.ID); err != nil {
		t.Fatal(err)
	}
	riverJob := &river.Job[ReleaseFinalizeArgs]{JobRow: &rivertype.JobRow{
		ID: riverID, Attempt: 1, State: rivertype.JobStateRunning, AttemptedBy: []string{"owner-a"},
	}}
	ctx := ContextWithRiverExecution(t.Context(), riverJob, "owner-a", time.Minute)
	if err := repository.RequeueAfterFailure(ctx, job.ID, 1, []byte(`{"code":"retry"}`)); !errors.Is(err, jobs.ErrConflict) {
		t.Fatalf("RequeueAfterFailure() with newer product attempt error = %v, want conflict", err)
	}
	assertProductRunning(t, repository, job.ID, 2)
	assertRiverFence(t, pool, riverID, rivertype.JobStateRunning, 1, []string{"owner-a"}, false)
}

func TestRequeueAfterFailureRejectsMismatchedRiverOwner(t *testing.T) {
	repository, pool := cancelClaimedRepository(t)
	job := enqueueClaimedJob(t, repository, pool, "requeue-owner", "owner-a", 1)
	riverID := jobRiverID(t, pool, job.ID)
	if _, err := pool.Exec(t.Context(), `
		UPDATE public.river_job
		SET attempted_by = ARRAY['owner-b']
		WHERE id = $1`, riverID); err != nil {
		t.Fatal(err)
	}
	riverJob := &river.Job[ReleaseFinalizeArgs]{JobRow: &rivertype.JobRow{
		ID: riverID, Attempt: 1, State: rivertype.JobStateRunning, AttemptedBy: []string{"owner-a"},
	}}
	ctx := ContextWithRiverExecution(t.Context(), riverJob, "owner-a", time.Minute)
	if err := repository.RequeueAfterFailure(ctx, job.ID, 1, []byte(`{"code":"retry"}`)); !errors.Is(err, jobs.ErrConflict) {
		t.Fatalf("owner-mismatched RequeueAfterFailure() error = %v, want conflict", err)
	}
	assertProductRunning(t, repository, job.ID, 1)
	assertRiverFence(t, pool, riverID, rivertype.JobStateRunning, 1, []string{"owner-b"}, false)
}

func TestCompleteTxRejectsLaterRiverAttempt(t *testing.T) {
	repository, pool := cancelClaimedRepository(t)
	job := enqueueClaimedJob(t, repository, pool, "complete-stale", "owner-a", 1)
	riverID := jobRiverID(t, pool, job.ID)
	advanceRiverAttempt(t, pool, riverID)

	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	err = repository.CompleteTx(t.Context(), tx, job.ID, jobs.Fence{Owner: "owner-a", Generation: 1})
	if !errors.Is(err, jobs.ErrConflict) {
		_ = tx.Rollback(t.Context())
		t.Fatalf("stale CompleteTx() error = %v, want conflict", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertProductRunning(t, repository, job.ID, 1)
	assertRiverFence(t, pool, riverID, rivertype.JobStateRunning, 2, []string{"owner-a", "owner-b"}, false)
}

func TestFailTxRejectsLaterRiverAttempt(t *testing.T) {
	repository, pool := cancelClaimedRepository(t)
	job := enqueueClaimedJob(t, repository, pool, "fail-stale", "owner-a", 1)
	riverID := jobRiverID(t, pool, job.ID)
	advanceRiverAttempt(t, pool, riverID)

	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	err = repository.FailTx(t.Context(), tx, job.ID, jobs.Fence{Owner: "owner-a", Generation: 1}, []byte(`{"code":"failed"}`))
	if !errors.Is(err, jobs.ErrConflict) {
		_ = tx.Rollback(t.Context())
		t.Fatalf("stale FailTx() error = %v, want conflict", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertProductRunning(t, repository, job.ID, 1)
	assertRiverFence(t, pool, riverID, rivertype.JobStateRunning, 2, []string{"owner-a", "owner-b"}, false)
}

func TestTerminalTxRejectsMismatchedRiverOwner(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*Repository, jobs.Job, pgx.Tx) error
	}{
		{name: "complete", call: func(repository *Repository, job jobs.Job, tx pgx.Tx) error {
			return repository.CompleteTx(context.Background(), tx, job.ID, jobs.Fence{Owner: "owner-a", Generation: 1})
		}},
		{name: "fail", call: func(repository *Repository, job jobs.Job, tx pgx.Tx) error {
			return repository.FailTx(context.Background(), tx, job.ID, jobs.Fence{Owner: "owner-a", Generation: 1}, []byte(`{"code":"failed"}`))
		}},
		{name: "cancel-claimed", call: func(repository *Repository, job jobs.Job, tx pgx.Tx) error {
			return repository.CancelClaimedTx(context.Background(), tx, job.ID, jobs.Fence{Owner: "owner-a", Generation: 1})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repository, pool := cancelClaimedRepository(t)
			job := enqueueClaimedJob(t, repository, pool, "terminal-owner-"+tc.name, "owner-b", 1)
			riverID := jobRiverID(t, pool, job.ID)
			tx, err := pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			err = tc.call(repository, job, tx)
			if !errors.Is(err, jobs.ErrConflict) {
				_ = tx.Rollback(t.Context())
				t.Fatalf("owner-mismatched %s() error = %v, want conflict", tc.name, err)
			}
			if err := tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			assertProductRunning(t, repository, job.ID, 1)
			assertRiverFence(t, pool, riverID, rivertype.JobStateRunning, 1, []string{"owner-b"}, false)
		})
	}
}

func TestCancelClaimedRejectsStaleRiverAttempt(t *testing.T) {
	repository, pool := cancelClaimedRepository(t)
	job := enqueueClaimedJob(t, repository, pool, "cancel-claimed-stale", "owner-a", 1)

	if _, err := pool.Exec(t.Context(), `
		UPDATE public.river_job
		SET attempt = 2, attempted_by = ARRAY['owner-a', 'owner-b']
		WHERE id = $1`, jobRiverID(t, pool, job.ID)); err != nil {
		t.Fatal(err)
	}
	err := repository.CancelClaimed(t.Context(), job.ID, jobs.Fence{Owner: "owner-a", Generation: 1})
	if !errors.Is(err, jobs.ErrConflict) {
		t.Fatalf("stale CancelClaimed() error = %v, want conflict", err)
	}
	current, err := repository.Get(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != jobs.StatusRunning || current.Attempts != 1 {
		t.Fatalf("stale cancellation product row = status %q attempts %d, want running/1", current.Status, current.Attempts)
	}
	var riverState rivertype.JobState
	var cancelAttempted bool
	if err := pool.QueryRow(t.Context(), `SELECT state, metadata ? 'cancel_attempted_at' FROM public.river_job WHERE id = $1`, jobRiverID(t, pool, job.ID)).Scan(&riverState, &cancelAttempted); err != nil {
		t.Fatal(err)
	}
	if riverState != rivertype.JobStateRunning || cancelAttempted {
		t.Fatalf("stale cancellation River row = state %q cancel_attempted=%v, want running/false", riverState, cancelAttempted)
	}
}

func TestCancelClaimedCancelsMatchingRiverAttempt(t *testing.T) {
	repository, pool := cancelClaimedRepository(t)
	job := enqueueClaimedJob(t, repository, pool, "cancel-claimed-current", "owner-a", 1)

	if err := repository.CancelClaimed(t.Context(), job.ID, jobs.Fence{Owner: "owner-a", Generation: 1}); err != nil {
		t.Fatalf("matching CancelClaimed() error = %v", err)
	}
	current, err := repository.Get(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != jobs.StatusCancelled || current.Attempts != 1 {
		t.Fatalf("matching cancellation product row = status %q attempts %d, want cancelled/1", current.Status, current.Attempts)
	}
	var riverState rivertype.JobState
	var cancelAttempted bool
	if err := pool.QueryRow(t.Context(), `SELECT state, metadata ? 'cancel_attempted_at' FROM public.river_job WHERE id = $1`, jobRiverID(t, pool, job.ID)).Scan(&riverState, &cancelAttempted); err != nil {
		t.Fatal(err)
	}
	if riverState != rivertype.JobStateRunning || !cancelAttempted {
		t.Fatalf("matching cancellation River row = state %q cancel_attempted=%v, want running/true", riverState, cancelAttempted)
	}
}

func cancelClaimedRepository(t *testing.T) (*Repository, *pgxpool.Pool) {
	t.Helper()
	harness := postgrestest.Start(t)
	database := harness.NewDatabase(t, "jobs_cancel_claimed")
	pool, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := migrations.ApplyRiver(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), SchemaSQL()); err != nil {
		t.Fatal(err)
	}
	return NewRepository(pool), pool
}

func enqueueClaimedJob(t *testing.T, repository *Repository, pool *pgxpool.Pool, id, owner string, attempt int) jobs.Job {
	t.Helper()
	job, err := repository.Enqueue(t.Context(), jobs.EnqueueInput{
		ID: id, Kind: "release.finalize", WorkloadClass: "background", PrincipalID: "test-principal",
		PartitionKey: id, ResourceKind: "release", ResourceID: id, EstimatedMemoryBytes: 1,
		Payload: []byte(`{"value":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	riverID := jobRiverID(t, pool, id)
	if _, err := pool.Exec(t.Context(), `
		UPDATE public.river_job
		SET state = 'running', attempt = $2, attempted_by = ARRAY[$3]
		WHERE id = $1`, riverID, attempt, owner); err != nil {
		t.Fatal(err)
	}
	riverJob := &river.Job[ReleaseFinalizeArgs]{JobRow: &rivertype.JobRow{
		ID: riverID, Attempt: attempt, State: rivertype.JobStateRunning, AttemptedBy: []string{owner},
	}}
	ctx := ContextWithRiverExecution(t.Context(), riverJob, owner, time.Minute)
	if _, err := repository.MarkRunning(ctx, id, attempt); err != nil {
		t.Fatal(err)
	}
	return job
}

func jobRiverID(t *testing.T, pool *pgxpool.Pool, id string) int64 {
	t.Helper()
	var riverID int64
	if err := pool.QueryRow(t.Context(), `SELECT river_job_id FROM jobs.job_history WHERE id = $1`, id).Scan(&riverID); err != nil {
		t.Fatal(err)
	}
	return riverID
}

func advanceRiverAttempt(t *testing.T, pool *pgxpool.Pool, riverID int64) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `
		UPDATE public.river_job
		SET attempt = 2, attempted_by = ARRAY['owner-a', 'owner-b']
		WHERE id = $1`, riverID); err != nil {
		t.Fatal(err)
	}
}

func assertProductRunning(t *testing.T, repository *Repository, id string, attempts int) {
	t.Helper()
	current, err := repository.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != jobs.StatusRunning || current.Attempts != attempts {
		t.Fatalf("product job = status %q attempts %d, want running/%d", current.Status, current.Attempts, attempts)
	}
}

func assertRiverFence(t *testing.T, pool *pgxpool.Pool, riverID int64, state rivertype.JobState, attempt int, owners []string, cancelAttempted bool) {
	t.Helper()
	var gotState rivertype.JobState
	var gotAttempt int
	var gotOwners []string
	var gotCancelAttempted bool
	if err := pool.QueryRow(t.Context(), `
		SELECT state, attempt, attempted_by, metadata ? 'cancel_attempted_at'
		FROM public.river_job WHERE id = $1`, riverID).Scan(&gotState, &gotAttempt, &gotOwners, &gotCancelAttempted); err != nil {
		t.Fatal(err)
	}
	if gotState != state || gotAttempt != attempt || !reflect.DeepEqual(gotOwners, owners) || gotCancelAttempted != cancelAttempted {
		t.Fatalf("River row = state %q attempt %d owners %#v cancel_attempted=%v, want %q/%d %#v/%v", gotState, gotAttempt, gotOwners, gotCancelAttempted, state, attempt, owners, cancelAttempted)
	}
}
