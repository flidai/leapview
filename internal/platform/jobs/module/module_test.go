package module

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	jobpostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

type testAdmissionLease struct{ ctx context.Context }

func (l testAdmissionLease) Context() context.Context { return l.ctx }
func (testAdmissionLease) Release()                   {}

func allowJobs(ctx context.Context, _ jobs.AdmissionRequest) (jobs.AdmissionLease, error) {
	return testAdmissionLease{ctx: ctx}, nil
}

func TestBuildRejectsMissingProductionAuthority(t *testing.T) {
	admission := jobs.AdmitterFunc(allowJobs)
	if _, err := Build(t.Context(), Config{Admission: admission}); err == nil || !strings.Contains(err.Error(), "persistence") {
		t.Fatalf("missing persistence error = %v", err)
	}
	if _, err := Build(t.Context(), Config{Persistence: &Persistence{}, Production: true, Admission: admission}); err == nil || !strings.Contains(err.Error(), "PostgreSQL") {
		t.Fatalf("unmarked production persistence error = %v", err)
	}
}

func TestRiverPostgreSQL18ExecutionAndProductHistory(t *testing.T) {
	harness := postgrestest.Start(t)
	database := harness.NewDatabase(t, "river_jobs_module")
	pool, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := migrations.ApplyRiver(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), jobpostgres.SchemaSQL()); err != nil {
		t.Fatal(err)
	}

	repository := jobpostgres.NewRepository(pool)
	persistence, err := NewPostgresPersistence(repository)
	if err != nil {
		t.Fatal(err)
	}
	module, err := Build(t.Context(), Config{
		Persistence:  &persistence,
		Production:   true,
		Admission:    jobs.AdmitterFunc(allowJobs),
		OwnerID:      "river-jobs-module-test",
		PollInterval: 5 * time.Millisecond,
		LeaseTimeout: 10 * time.Second,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	var attempts sync.Map
	if err := module.RegisterHandlers([]jobs.Handler{jobs.HandlerFunc{
		JobKind: "release.finalize",
		Run: func(_ context.Context, job jobs.Job) error {
			value, _ := attempts.LoadOrStore(job.ID, 0)
			attempt := value.(int) + 1
			attempts.Store(job.ID, attempt)
			switch job.ResourceID {
			case "retry":
				if attempt == 1 {
					return jobs.Retryable(errors.New("sensitive transient detail"), 10*time.Millisecond)
				}
			case "failure":
				return errors.New("sensitive terminal detail")
			}
			return nil
		},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := module.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = module.Stop(context.Background()) })

	success := enqueueTestJob(t, module, "job-success", "success")
	duplicate := enqueueTestJob(t, module, "job-success", "success")
	if duplicate.ID != success.ID {
		t.Fatalf("idempotent enqueue ID = %q, want %q", duplicate.ID, success.ID)
	}
	if _, err := module.Enqueue(t.Context(), testJobInput("job-success", "different")); !errors.Is(err, jobs.ErrConflict) {
		t.Fatalf("conflicting enqueue error = %v, want conflict", err)
	}
	retry := enqueueTestJob(t, module, "job-retry", "retry")
	failure := enqueueTestJob(t, module, "job-failure", "failure")

	waitForProductStatus(t, module, success.ID, jobs.StatusSucceeded)
	waitForProductStatus(t, module, retry.ID, jobs.StatusSucceeded)
	failed := waitForProductStatus(t, module, failure.ID, jobs.StatusFailed)
	if failed.ErrorJSON != `{"code": "ASYNC_JOB_FAILED"}` && failed.ErrorJSON != `{"code":"ASYNC_JOB_FAILED"}` {
		t.Fatalf("failure evidence = %q", failed.ErrorJSON)
	}
	if strings.Contains(failed.ErrorJSON, "sensitive") {
		t.Fatalf("failure evidence leaked handler text: %s", failed.ErrorJSON)
	}
	if retry.Attempts != 0 {
		// The returned enqueue projection is expected to remain queued; the
		// durable assertion below verifies the retried attempt count.
		t.Fatalf("initial retry projection attempts = %d, want 0", retry.Attempts)
	}
	retried, err := module.Get(t.Context(), retry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Attempts != 2 {
		t.Fatalf("retried product attempts = %d, want 2", retried.Attempts)
	}

	assertRiverState(t, pool, success.ID, rivertype.JobStateCompleted, 1)
	assertRiverState(t, pool, retry.ID, rivertype.JobStateCompleted, 2)
	assertRiverState(t, pool, failure.ID, rivertype.JobStateCancelled, 1)
	var riverRows int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM river_job WHERE kind='release.finalize'`).Scan(&riverRows); err != nil {
		t.Fatal(err)
	}
	if riverRows != 3 {
		t.Fatalf("River rows = %d, want 3 after idempotent enqueue", riverRows)
	}

	workflow := jobs.WorkflowIntent{
		Event: jobs.EventInput{Key: "workflow.rollback", ResourceKind: "release", ResourceID: "workflow", EventType: "release.queued", Data: []byte(`{"state":"queued"}`)},
		Job:   testJobInput("job-rollback", "workflow"),
	}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := module.RecordWorkflowTx(t.Context(), tx, workflow); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := module.Get(t.Context(), workflow.Job.ID); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("rolled-back product job lookup = %v, want not found", err)
	}
	var rolledBackRiverRows int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM river_job WHERE args->>'product_job_id'=$1`, workflow.Job.ID).Scan(&rolledBackRiverRows); err != nil {
		t.Fatal(err)
	}
	if rolledBackRiverRows != 0 {
		t.Fatalf("rolled-back River rows = %d, want 0", rolledBackRiverRows)
	}
}

func TestWorkRetainsRiverRetryWhenRetryPersistenceFails(t *testing.T) {
	harness := postgrestest.Start(t)
	database := harness.NewDatabase(t, "river_retry_persistence_failure")
	pool, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := migrations.ApplyRiver(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), jobpostgres.SchemaSQL()); err != nil {
		t.Fatal(err)
	}

	db := retryPersistenceFailureDB{Pool: pool}
	repository := jobpostgres.NewRepository(db)
	persistence, err := NewPostgresPersistence(repository)
	if err != nil {
		t.Fatal(err)
	}
	module, err := Build(t.Context(), Config{
		Persistence: &persistence,
		Production:  true,
		Admission:   jobs.AdmitterFunc(allowJobs),
		OwnerID:     "river-retry-persistence-failure-test",
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := module.RegisterHandlers([]jobs.Handler{jobs.HandlerFunc{
		JobKind: "release.finalize",
		Run: func(context.Context, jobs.Job) error {
			return jobs.Retryable(errors.New("transient"), time.Millisecond)
		},
	}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = module.Stop(context.Background()) })

	job, err := module.Enqueue(t.Context(), testJobInput("job-retry-persistence-failure", "retry"))
	if err != nil {
		t.Fatal(err)
	}
	var riverJobID int64
	if err := pool.QueryRow(t.Context(), `SELECT river_job_id FROM jobs.job_history WHERE id=$1`, job.ID).Scan(&riverJobID); err != nil {
		t.Fatal(err)
	}
	err = module.work(t.Context(), riverJobID, 1, jobpostgres.ExecutionArgs{ProductJobID: job.ID, RequestDigest: job.RequestDigest})
	if err == nil {
		t.Fatal("work succeeded after retry persistence failure")
	}
	if !strings.Contains(err.Error(), "ASYNC_JOB_RETRY_PERSISTENCE_FAILED") {
		t.Fatalf("work error = %v, want retry persistence code", err)
	}
	var cancelErr *river.JobCancelError
	if errors.As(err, &cancelErr) {
		t.Fatalf("work error = %v, want River to retain an automatic retry", err)
	}
	if _, ok := module.retryAt.Load(riverJobID); ok {
		t.Fatal("retry persistence failure scheduled a normal River retry")
	}
	current, err := module.Get(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != jobs.StatusRunning {
		t.Fatalf("product status after retry persistence failure = %q, want running", current.Status)
	}
}

func TestRequeueAfterFailureRejectsNonRunningRow(t *testing.T) {
	repository := jobpostgres.NewRepository(zeroRowsDB{})
	err := repository.RequeueAfterFailure(t.Context(), "job", 1, []byte(`{"code":"ASYNC_JOB_RETRY"}`))
	if !errors.Is(err, jobs.ErrConflict) {
		t.Fatalf("RequeueAfterFailure() error = %v, want conflict when no row is requeued", err)
	}
}

type zeroRowsDB struct{}

func (zeroRowsDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag("UPDATE 0"), nil
}

func (zeroRowsDB) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }

func (zeroRowsDB) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

type retryPersistenceFailureDB struct{ Pool *pgxpool.Pool }

func (db retryPersistenceFailureDB) NativePool() *pgxpool.Pool { return db.Pool }

func (db retryPersistenceFailureDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return retryPersistenceFailureTx{Tx: tx}, nil
}

func (db retryPersistenceFailureDB) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(query, "RequeueJobAfterFailure") {
		return pgconn.CommandTag{}, errors.New("simulated retry persistence failure")
	}
	return db.Pool.Exec(ctx, query, args...)
}

func (db retryPersistenceFailureDB) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	return db.Pool.Query(ctx, query, args...)
}

func (db retryPersistenceFailureDB) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	return db.Pool.QueryRow(ctx, query, args...)
}

type retryPersistenceFailureTx struct{ pgx.Tx }

func (tx retryPersistenceFailureTx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(query, "RequeueJobAfterFailure") {
		return pgconn.CommandTag{}, errors.New("simulated retry persistence failure")
	}
	return tx.Tx.Exec(ctx, query, args...)
}

func testJobInput(id, resourceID string) jobs.EnqueueInput {
	return jobs.EnqueueInput{
		ID: id, Kind: "release.finalize", WorkloadClass: "control",
		PrincipalID: "system", GroupIDs: []string{}, PartitionKey: "release:" + id,
		ResourceKind: "release", ResourceID: resourceID, EstimatedMemoryBytes: 1,
		Payload: []byte(`{"release_id":"` + resourceID + `"}`),
	}
}

func enqueueTestJob(t *testing.T, module *Module, id, resourceID string) jobs.Job {
	t.Helper()
	job, err := module.Enqueue(t.Context(), testJobInput(id, resourceID))
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func waitForProductStatus(t *testing.T, module *Module, id string, want jobs.Status) jobs.Job {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		job, err := module.Get(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == want {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s status = %q, want %q", id, job.Status, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func assertRiverState(t *testing.T, pool *pgxpool.Pool, productID string, state rivertype.JobState, attempts int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		var gotState string
		var gotAttempts int
		if err := pool.QueryRow(t.Context(), `SELECT r.state,r.attempt FROM river_job r JOIN jobs.job_history h ON h.river_job_id=r.id WHERE h.id=$1`, productID).Scan(&gotState, &gotAttempts); err != nil {
			t.Fatal(err)
		}
		if gotState == string(state) && gotAttempts == attempts {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("River state/attempt for %s = %s/%d, want %s/%d", productID, gotState, gotAttempts, state, attempts)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
