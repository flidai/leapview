package postgres

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/release"
	publicjobs "github.com/flidai/leapview/pkg/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/riverqueue/river/rivertype"
)

const riverFinalizeQueue = "control"

type riverFinalizeArgs struct {
	ProjectID     string `json:"project_id"`
	ReleaseID     string `json:"release_id"`
	RequestDigest string `json:"request_digest"`
}

func (riverFinalizeArgs) Kind() string { return "release.finalize" }

type proofRetryPolicy struct{}

func (proofRetryPolicy) NextRetry(*rivertype.JobRow) time.Time {
	return time.Now().UTC().Add(10 * time.Millisecond)
}

type proofAdmission struct {
	active   atomic.Int32
	total    atomic.Int32
	released atomic.Int32
}

func (a *proofAdmission) acquire(ctx context.Context) (context.Context, func()) {
	a.active.Add(1)
	a.total.Add(1)
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			a.active.Add(-1)
			a.released.Add(1)
		})
	}
}

type riverFinalizeWorker struct {
	river.WorkerDefaults[riverFinalizeArgs]

	pool             *pgxpool.Pool
	releases         *Repository
	admission        *proofAdmission
	transientRelease string
	blockingRelease  string
	blockStarted     chan struct{}
	attempts         atomic.Int32
	transientOnce    atomic.Bool
}

func (w *riverFinalizeWorker) Work(ctx context.Context, job *river.Job[riverFinalizeArgs]) error {
	workCtx, releaseLease := w.admission.acquire(ctx)
	defer releaseLease()
	w.attempts.Add(1)
	if job.Args.ReleaseID == w.blockingRelease {
		select {
		case w.blockStarted <- struct{}{}:
		default:
		}
		<-workCtx.Done()
		return errors.New("RELEASE_FINALIZE_CANCELLED")
	}
	if job.Args.ReleaseID == w.transientRelease && w.transientOnce.CompareAndSwap(false, true) {
		// River persists returned errors, so this bounded classification contains
		// no wrapped driver text, SQL, payload, or credential-bearing detail.
		return errors.New("RELEASE_FINALIZE_TRANSIENT")
	}
	tx, err := w.pool.Begin(workCtx)
	if err != nil {
		return errors.New("RELEASE_FINALIZE_DATABASE_UNAVAILABLE")
	}
	defer tx.Rollback(context.WithoutCancel(workCtx))
	if _, err := w.releases.CompleteFinalizationJobTx(workCtx, tx, job.Args.ProjectID, job.Args.ReleaseID, job.Args.RequestDigest); err != nil {
		if errors.Is(err, ErrConflict) {
			return errors.New("RELEASE_FINALIZE_CONFLICT")
		}
		return errors.New("RELEASE_FINALIZE_FAILED")
	}
	if _, err := river.JobCompleteTx[*riverpgxv5.Driver](workCtx, tx, job); err != nil {
		return errors.New("RELEASE_FINALIZE_COMPLETION_FAILED")
	}
	if err := tx.Commit(workCtx); err != nil {
		return errors.New("RELEASE_FINALIZE_COMMIT_FAILED")
	}
	return nil
}

func TestReleaseFinalizeFitsRiverTransactionalExecution(t *testing.T) {
	pool := testEffectsDB(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	driver := riverpgxv5.New(pool)
	migrator, err := rivermigrate.New(driver, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		t.Fatalf("migrate River schema: %v", err)
	}

	repository := New(pool)
	committed := createRiverProofRelease(t, repository, "river_committed", "generation_river_committed")
	rolledBack := createRiverProofRelease(t, repository, "river_rolled_back", "generation_river_rolled_back")
	cancelled := createRiverProofRelease(t, repository, "river_cancelled", "generation_river_cancelled")

	admission := &proofAdmission{}
	worker := &riverFinalizeWorker{
		pool: pool, releases: repository, admission: admission,
		transientRelease: committed.ID, blockingRelease: cancelled.ID,
		blockStarted: make(chan struct{}, 1),
	}
	workers := river.NewWorkers()
	river.AddWorker(workers, worker)
	client, err := river.NewClient(driver, &river.Config{
		FetchCooldown: 5 * time.Millisecond, FetchPollInterval: 20 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), MaxAttempts: 3,
		Queues:      map[string]river.QueueConfig{riverFinalizeQueue: {MaxWorkers: 2}},
		RetryPolicy: proofRetryPolicy{}, Workers: workers,
	})
	if err != nil {
		t.Fatal(err)
	}

	rolledBackResult := insertRiverFinalizeTx(t, pool, repository, client, rolledBack, true)
	if rolledBackResult == nil || rolledBackResult.Job == nil {
		t.Fatal("River rollback proof did not allocate a transactional job")
	}
	loaded, err := repository.Get(ctx, rolledBack.ServingIdentity.ProjectID, rolledBack.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != release.StatusDraft {
		t.Fatalf("rolled-back release status = %q, want draft", loaded.Status)
	}
	if _, err := client.JobGet(ctx, rolledBackResult.Job.ID); !errors.Is(err, rivertype.ErrNotFound) {
		t.Fatalf("rolled-back River job lookup = %v, want not found", err)
	}

	inserted := insertRiverFinalizeTx(t, pool, repository, client, committed, false)
	duplicate := insertRiverFinalizeTx(t, pool, repository, client, committed, false)
	if duplicate == nil || !duplicate.UniqueSkippedAsDuplicate || duplicate.Job.ID != inserted.Job.ID {
		t.Fatalf("River uniqueness result = %#v, first job %d", duplicate, inserted.Job.ID)
	}
	completed, unsubscribe := client.Subscribe(river.EventKindJobCompleted)
	defer unsubscribe()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-completed:
		if event.Job == nil || event.Job.ID != inserted.Job.ID {
			t.Fatalf("completed River event = %#v", event)
		}
	case <-ctx.Done():
		t.Fatalf("wait for River completion: %v", ctx.Err())
	}

	ready, err := repository.Get(ctx, committed.ServingIdentity.ProjectID, committed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ready.Status != release.StatusReady || ready.FinalizedAt == "" {
		t.Fatalf("River-completed release = %#v", ready)
	}
	finalizedAt := ready.FinalizedAt
	jobRow, err := client.JobGet(ctx, inserted.Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if jobRow.State != rivertype.JobStateCompleted || jobRow.Attempt != 2 {
		t.Fatalf("River job state/attempt = %q/%d, want completed/2", jobRow.State, jobRow.Attempt)
	}
	var persistedErrors string
	if err := pool.QueryRow(ctx, `SELECT errors::text FROM river_job WHERE id=$1`, inserted.Job.ID).Scan(&persistedErrors); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(persistedErrors, "RELEASE_FINALIZE_TRANSIENT") || strings.Contains(strings.ToLower(persistedErrors), "secret") {
		t.Fatalf("River persisted unsafe or unexpected error evidence %q", persistedErrors)
	}

	// A process-loss replay sees the product terminal record and produces no
	// second terminal effect. A divergent request digest fails closed.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CompleteFinalizationJobTx(ctx, tx, committed.ServingIdentity.ProjectID.String(), committed.ID, committed.RequestDigest); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("idempotent terminal replay: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	replayed, err := repository.Get(ctx, committed.ServingIdentity.ProjectID, committed.ID)
	if err != nil || replayed.FinalizedAt != finalizedAt {
		t.Fatalf("terminal replay changed release: %#v, %v", replayed, err)
	}
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CompleteFinalizationJobTx(ctx, tx, committed.ServingIdentity.ProjectID.String(), committed.ID, digest("f")); !errors.Is(err, ErrConflict) {
		_ = tx.Rollback(ctx)
		t.Fatalf("conflicting request digest = %v, want conflict", err)
	}
	_ = tx.Rollback(ctx)

	insertRiverFinalizeTx(t, pool, repository, client, cancelled, false)
	select {
	case <-worker.blockStarted:
	case <-ctx.Done():
		t.Fatalf("wait for cancellation worker: %v", ctx.Err())
	}
	if err := client.StopAndCancel(ctx); err != nil {
		t.Fatal(err)
	}
	if admission.active.Load() != 0 || admission.total.Load() != admission.released.Load() {
		t.Fatalf("workload leases active/total/released = %d/%d/%d", admission.active.Load(), admission.total.Load(), admission.released.Load())
	}

	if _, err := client.JobDelete(ctx, inserted.Job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.JobGet(ctx, inserted.Job.ID); !errors.Is(err, rivertype.ErrNotFound) {
		t.Fatalf("deleted River row lookup = %v, want not found", err)
	}
	if durable, err := repository.Get(ctx, committed.ServingIdentity.ProjectID, committed.ID); err != nil || durable.Status != release.StatusReady {
		t.Fatalf("release history after River cleanup = %#v, %v", durable, err)
	}
	var customJobs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM jobs.job`).Scan(&customJobs); err != nil {
		t.Fatal(err)
	}
	if customJobs != 0 {
		t.Fatalf("River proof wrote %d rows to removed custom execution queue", customJobs)
	}
}

func createRiverProofRelease(t *testing.T, repository *Repository, releaseID, generationID string) release.Release {
	t.Helper()
	id := identity(t, generationID)
	prov := provenance(t, id)
	input := release.CreateInput{ID: releaseID, ServingIdentity: id, ProjectDigest: prov.Artifact.ProjectDigest, ArtifactDigest: prov.Artifact.ContentDigest, RequestDigest: digest("6"), IdempotencyKey: "request_" + releaseID, CreatedBy: "principal_river", Provenance: &prov}
	created, err := repository.Create(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.RecordArtifact(t.Context(), release.Artifact{ReleaseID: created.ID, ServingIdentity: id, ExpectedDigest: input.ArtifactDigest, ActualDigest: input.ArtifactDigest, SizeBytes: 42}); err != nil {
		t.Fatal(err)
	}
	return created
}

func insertRiverFinalizeTx(t *testing.T, pool *pgxpool.Pool, repository *Repository, client *river.Client[pgx.Tx], item release.Release, rollback bool) *rivertype.JobInsertResult {
	t.Helper()
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := repository.BeginFinalizationTx(t.Context(), tx, item.ServingIdentity.ProjectID.String(), item.ID, publicjobs.WorkflowIntent{}); err != nil {
		t.Fatal(err)
	}
	result, err := client.InsertTx(t.Context(), tx, riverFinalizeArgs{ProjectID: item.ServingIdentity.ProjectID.String(), ReleaseID: item.ID, RequestDigest: item.RequestDigest}, &river.InsertOpts{
		Queue: riverFinalizeQueue, MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByArgs: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rollback {
		return result
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return result
}
