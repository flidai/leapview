package postgres

import (
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/rivertype"
)

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
	if _, err := repository.MarkRunning(t.Context(), id, attempt); err != nil {
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
