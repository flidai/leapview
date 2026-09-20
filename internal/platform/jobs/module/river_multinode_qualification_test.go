package module

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	jobpostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/rivertype"
)

const (
	riverMultiNodeOwnerA = "river-multinode-node-a"
	riverMultiNodeOwnerB = "river-multinode-node-b"
)

type riverMultiNodeInvocation struct {
	owner   string
	attempt int
}

type riverMultiNodeRecorder struct {
	mu              sync.Mutex
	invocations     map[string][]riverMultiNodeInvocation
	started         map[string]chan riverMultiNodeInvocation
	release         chan struct{}
	takeoverRelease chan struct{}
}

func newRiverMultiNodeRecorder() *riverMultiNodeRecorder {
	return &riverMultiNodeRecorder{
		invocations: make(map[string][]riverMultiNodeInvocation),
		started: map[string]chan riverMultiNodeInvocation{
			"success":  make(chan riverMultiNodeInvocation, 4),
			"takeover": make(chan riverMultiNodeInvocation, 4),
		},
		release:         make(chan struct{}),
		takeoverRelease: make(chan struct{}),
	}
}

func (r *riverMultiNodeRecorder) handler() jobs.Handler {
	return jobs.HandlerFunc{
		JobKind: "release.finalize",
		Run: func(ctx context.Context, job jobs.Job) error {
			invocation := riverMultiNodeInvocation{owner: job.LeaseOwner, attempt: job.Attempts}
			r.mu.Lock()
			r.invocations[job.ID] = append(r.invocations[job.ID], invocation)
			started := r.started[job.ResourceID]
			r.mu.Unlock()
			if started != nil {
				started <- invocation
			}

			switch job.ResourceID {
			case "success":
				select {
				case <-r.release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			case "takeover":
				if job.Attempts == 1 {
					select {
					case <-r.takeoverRelease:
						// A retryable process-loss classification leaves the product
						// row queued while River schedules the same operational row.
						return jobs.Retryable(errors.New("simulated process loss"), 6*time.Second)
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			}
			return nil
		},
	}
}

func (r *riverMultiNodeRecorder) count(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.invocations[id])
}

func TestPostgreSQL18MultiNodeRiverQualification(t *testing.T) {
	harness := postgrestest.Start(t)
	database := harness.NewDatabase(t, "river_jobs_multinode")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	poolA, err := pgxpool.New(ctx, database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(poolA.Close)
	poolB, err := pgxpool.New(ctx, database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(poolB.Close)
	if poolA == poolB {
		t.Fatal("multi-node qualification reused one PostgreSQL pool")
	}
	if err := migrations.ApplyRiver(ctx, poolA); err != nil {
		t.Fatal(err)
	}
	if _, err := poolA.Exec(ctx, jobpostgres.SchemaSQL()); err != nil {
		t.Fatal(err)
	}

	repositoryA := jobpostgres.NewRepository(poolA)
	repositoryB := jobpostgres.NewRepository(poolB)
	if repositoryA == repositoryB {
		t.Fatal("multi-node qualification reused one jobs repository")
	}
	nodeA := buildRiverMultiNodeModule(t, repositoryA, riverMultiNodeOwnerA)
	nodeB := buildRiverMultiNodeModule(t, repositoryB, riverMultiNodeOwnerB)

	recorder := newRiverMultiNodeRecorder()
	if err := nodeA.RegisterHandlers([]jobs.Handler{recorder.handler()}); err != nil {
		t.Fatal(err)
	}
	if err := nodeB.RegisterHandlers([]jobs.Handler{recorder.handler()}); err != nil {
		t.Fatal(err)
	}
	startErrors := make(chan error, 2)
	go func() { startErrors <- nodeA.Start(ctx) }()
	go func() { startErrors <- nodeB.Start(ctx) }()
	for range 2 {
		if err := <-startErrors; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = nodeA.Stop(context.Background()) })
	t.Cleanup(func() { _ = nodeB.Stop(context.Background()) })
	t.Cleanup(func() {
		select {
		case <-recorder.release:
		default:
			close(recorder.release)
		}
	})
	t.Cleanup(func() {
		select {
		case <-recorder.takeoverRelease:
		default:
			close(recorder.takeoverRelease)
		}
	})

	success := enqueueTestJob(t, nodeA, "river-multinode-success", "success")
	successInvocation := waitRiverMultiNodeInvocation(t, ctx, recorder.started["success"])
	if successInvocation.attempt != 1 || !riverMultiNodeKnownOwner(successInvocation.owner) {
		t.Fatalf("simultaneous success invocation = %#v, want first attempt on a configured owner", successInvocation)
	}
	close(recorder.release)
	success = waitRiverMultiNodeProduct(t, ctx, nodeA, success.ID, jobs.StatusSucceeded)
	if success.Attempts != 1 {
		t.Fatalf("simultaneous success product attempts = %d, want 1", success.Attempts)
	}
	assertRiverMultiNodeEvidence(t, ctx, poolA, success.ID, rivertype.JobStateCompleted, 1, 1)
	if got := recorder.count(success.ID); got != 1 {
		t.Fatalf("simultaneous success handler invocations = %d, want 1", got)
	}

	takeover := enqueueTestJob(t, nodeA, "river-multinode-takeover", "takeover")
	firstInvocation := waitRiverMultiNodeInvocation(t, ctx, recorder.started["takeover"])
	if firstInvocation.attempt != 1 || !riverMultiNodeKnownOwner(firstInvocation.owner) {
		t.Fatalf("takeover first invocation = %#v, want first attempt on a configured owner", firstInvocation)
	}
	close(recorder.takeoverRelease)
	waitRiverMultiNodeRetryQueued(t, ctx, nodeA, takeover.ID)
	stopOwner := nodeA
	if firstInvocation.owner == riverMultiNodeOwnerB {
		stopOwner = nodeB
	}
	if err := stopOwner.Stop(ctx); err != nil {
		t.Fatalf("stop owner %q for takeover: %v", firstInvocation.owner, err)
	}
	secondInvocation := waitRiverMultiNodeInvocation(t, ctx, recorder.started["takeover"])
	if secondInvocation.attempt != 2 || secondInvocation.owner == firstInvocation.owner || !riverMultiNodeKnownOwner(secondInvocation.owner) {
		t.Fatalf("takeover second invocation = %#v after first %#v, want later attempt on surviving owner", secondInvocation, firstInvocation)
	}
	takeover = waitRiverMultiNodeProduct(t, ctx, nodeB, takeover.ID, jobs.StatusSucceeded)
	if takeover.Attempts != 2 {
		t.Fatalf("takeover product attempts = %d, want 2", takeover.Attempts)
	}
	assertRiverMultiNodeEvidence(t, ctx, poolA, takeover.ID, rivertype.JobStateCompleted, 2, 1)
	if got := recorder.count(takeover.ID); got != 2 {
		t.Fatalf("takeover handler invocations = %d, want exactly 2", got)
	}
}

func TestApprovalActivationOrphanIsReclaimedByReplacementNode(t *testing.T) {
	harness := postgrestest.Start(t)
	database := harness.NewDatabase(t, "river_approval_activation_orphan")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	poolA, err := pgxpool.New(ctx, database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(poolA.Close)
	poolB, err := pgxpool.New(ctx, database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(poolB.Close)
	if err := migrations.ApplyRiver(ctx, poolA); err != nil {
		t.Fatal(err)
	}
	if _, err := poolA.Exec(ctx, jobpostgres.SchemaSQL()); err != nil {
		t.Fatal(err)
	}

	repositoryA := jobpostgres.NewRepository(poolA)
	repositoryB := jobpostgres.NewRepository(poolB)
	nodeA := buildRiverMultiNodeModule(t, repositoryA, riverMultiNodeOwnerA)
	nodeB := buildRiverMultiNodeModule(t, repositoryB, riverMultiNodeOwnerB)
	approvalStarted := make(chan riverMultiNodeInvocation, 1)
	longRunningStarted := make(chan riverMultiNodeInvocation, 1)
	handlers := []jobs.Handler{jobs.HandlerFunc{
		JobKind:               approvalActivationKind,
		ExecutionLeaseTimeout: time.Minute,
		Run: func(_ context.Context, job jobs.Job) error {
			approvalStarted <- riverMultiNodeInvocation{owner: job.LeaseOwner, attempt: job.Attempts}
			return nil
		},
	}, jobs.HandlerFunc{
		JobKind: "release.finalize",
		Run: func(_ context.Context, job jobs.Job) error {
			longRunningStarted <- riverMultiNodeInvocation{owner: job.LeaseOwner, attempt: job.Attempts}
			return nil
		},
	}}
	if err := nodeA.RegisterHandlers(handlers); err != nil {
		t.Fatal(err)
	}
	if err := nodeB.RegisterHandlers(handlers); err != nil {
		t.Fatal(err)
	}

	input := testJobInput("river-approval-activation-orphan", "publication-orphan")
	input.Kind = approvalActivationKind
	input.PartitionKey = "delivery-target:orphan"
	input.ResourceKind = "delivery_publication"
	job, err := nodeA.Enqueue(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	longRunning := enqueueTestJob(t, nodeA, "river-long-running-orphan", "release-orphan")
	markRiverMultiNodeOrphan(t, ctx, poolA, job.ID)
	markRiverMultiNodeOrphan(t, ctx, poolA, longRunning.ID)
	orphan, err := nodeA.Get(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if orphan.Status != jobs.StatusRunning || orphan.Attempts != 1 {
		t.Fatalf("orphaned product job = %q/%d, want running/1", orphan.Status, orphan.Attempts)
	}
	assertRiverMultiNodeEvidence(t, ctx, poolA, job.ID, rivertype.JobStateRunning, 1, 1)
	assertRiverMultiNodeEvidence(t, ctx, poolA, longRunning.ID, rivertype.JobStateRunning, 1, 1)

	if err := nodeB.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nodeB.Stop(context.Background()) })
	invocation := waitRiverMultiNodeInvocation(t, ctx, approvalStarted)
	if invocation.owner != riverMultiNodeOwnerB || invocation.attempt != 2 {
		t.Fatalf("reclaimed invocation = %#v, want attempt 2 on replacement node", invocation)
	}
	recovered := waitRiverMultiNodeProduct(t, ctx, nodeB, job.ID, jobs.StatusSucceeded)
	if recovered.ID != job.ID || recovered.Attempts != 2 {
		t.Fatalf("recovered product job = %q/%d, want same job %q at attempt 2", recovered.ID, recovered.Attempts, job.ID)
	}
	assertRiverMultiNodeEvidence(t, ctx, poolB, job.ID, rivertype.JobStateCompleted, 2, 1)
	// The same rescue pass sees this three-minute-old row, but the configured
	// 24-hour worker keeps its 25-hour rescue horizon.
	assertRiverMultiNodeEvidence(t, ctx, poolB, longRunning.ID, rivertype.JobStateRunning, 1, 1)
	longProduct, err := nodeB.Get(ctx, longRunning.ID)
	if err != nil {
		t.Fatal(err)
	}
	if longProduct.Status != jobs.StatusRunning || longProduct.Attempts != 1 {
		t.Fatalf("long-running product job = %q/%d, want preserved running/1", longProduct.Status, longProduct.Attempts)
	}
	select {
	case unexpected := <-longRunningStarted:
		t.Fatalf("long-running orphan was reclaimed at the two-minute horizon: %#v", unexpected)
	default:
	}
}

func buildRiverMultiNodeModule(t *testing.T, repository *jobpostgres.Repository, owner string) *Module {
	t.Helper()
	persistence, err := NewPostgresPersistence(repository)
	if err != nil {
		t.Fatal(err)
	}
	module, err := Build(t.Context(), Config{
		Persistence:     &persistence,
		Production:      true,
		Admission:       jobs.AdmitterFunc(allowJobs),
		OwnerID:         owner,
		PollInterval:    5 * time.Millisecond,
		LeaseTimeout:    30 * time.Second,
		RiverJobTimeout: 24 * time.Hour,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return module
}

func riverMultiNodeKnownOwner(owner string) bool {
	return owner == riverMultiNodeOwnerA || owner == riverMultiNodeOwnerB
}

func waitRiverMultiNodeInvocation(t *testing.T, ctx context.Context, started <-chan riverMultiNodeInvocation) riverMultiNodeInvocation {
	t.Helper()
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	select {
	case invocation := <-started:
		return invocation
	case <-timer.C:
		t.Fatalf("timed out waiting for River worker invocation")
	case <-ctx.Done():
		t.Fatalf("waiting for River worker invocation: %v", ctx.Err())
	}
	return riverMultiNodeInvocation{}
}

func waitRiverMultiNodeProduct(t *testing.T, ctx context.Context, module *Module, id string, want jobs.Status) jobs.Job {
	t.Helper()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		job, err := module.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == want {
			return job
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("job %s status = %q, want %q", id, job.Status, want)
		case <-ctx.Done():
			t.Fatalf("waiting for product job %s: %v", id, ctx.Err())
		}
	}
}

func waitRiverMultiNodeRetryQueued(t *testing.T, ctx context.Context, module *Module, id string) {
	t.Helper()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		job, err := module.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == jobs.StatusQueued && job.Attempts == 1 {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("job %s status/attempts = %q/%d, want queued/1 after process-loss replay", id, job.Status, job.Attempts)
		case <-ctx.Done():
			t.Fatalf("waiting for queued process-loss replay %s: %v", id, ctx.Err())
		}
	}
}

func assertRiverMultiNodeEvidence(t *testing.T, ctx context.Context, pool *pgxpool.Pool, productID string, wantState rivertype.JobState, wantAttempt, wantRows int) {
	t.Helper()
	var state string
	var attempt int
	var attemptedBy []string
	if err := pool.QueryRow(ctx, `SELECT r.state,r.attempt,r.attempted_by FROM public.river_job r JOIN jobs.job_history h ON h.river_job_id=r.id WHERE h.id=$1`, productID).Scan(&state, &attempt, &attemptedBy); err != nil {
		t.Fatal(err)
	}
	if state != string(wantState) || attempt != wantAttempt {
		t.Fatalf("River operational evidence for %s = %s/%d, want %s/%d", productID, state, attempt, wantState, wantAttempt)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.river_job r JOIN jobs.job_history h ON h.river_job_id=r.id WHERE h.id=$1`, productID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != wantRows {
		t.Fatalf("River operational rows for %s = %d, want %d", productID, rows, wantRows)
	}
	if wantAttempt > 1 && len(attemptedBy) < 2 {
		t.Fatalf("River attempted_by for takeover %s = %#v, want both node owners", productID, attemptedBy)
	}
}

func markRiverMultiNodeOrphan(t *testing.T, ctx context.Context, pool *pgxpool.Pool, productID string) int64 {
	t.Helper()
	var riverID int64
	if err := pool.QueryRow(ctx, `SELECT river_job_id FROM jobs.job_history WHERE id=$1`, productID).Scan(&riverID); err != nil {
		t.Fatal(err)
	}
	// Model the exact durable state left by SIGKILL: River and product history
	// both record attempt one as running, but no live client owns the claim and
	// no retryable result was returned.
	if _, err := pool.Exec(ctx, `
		UPDATE public.river_job
		SET state='running', attempt=1, attempted_by=ARRAY[$2],
		    attempted_at=clock_timestamp()-interval '3 minutes'
		WHERE id=$1`, riverID, riverMultiNodeOwnerA); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE jobs.job_history
		SET status='running', attempt_count=1,
		    started_at=clock_timestamp()-interval '3 minutes'
		WHERE id=$1`, productID); err != nil {
		t.Fatal(err)
	}
	return riverID
}
