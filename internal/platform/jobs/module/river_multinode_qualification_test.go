package module

import (
	"context"
	"encoding/json"
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
	projectpipelineplan "github.com/flidai/leapview/internal/project/contracts/pipelineplan"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
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

type refreshOrphanTestHandler struct {
	recovery *refreshmodule.PostgresJobsAdapter
	refresh  *refreshpostgres.Repository
	lease    time.Duration
	started  chan riverMultiNodeInvocation
}

func (h *refreshOrphanTestHandler) Kind() string                { return refreshPipelineKind }
func (h *refreshOrphanTestHandler) LeaseTimeout() time.Duration { return h.lease }
func (h *refreshOrphanTestHandler) RecoverOrphanedJobs(ctx context.Context) error {
	return h.recovery.RecoverExpiredRefreshJobs(ctx, 100)
}
func (h *refreshOrphanTestHandler) Handle(ctx context.Context, job jobs.Job) error {
	claimed, err := h.recovery.ClaimRiverJob(ctx, job, h.lease)
	if err != nil {
		return err
	}
	if err := h.refresh.InTx(ctx, func(tx refreshpostgres.Tx) error {
		return h.refresh.CompleteRunTreeTx(ctx, tx, claimed.RunID, claimed.LeaseOwner, claimed.LeaseRevision, json.RawMessage(`{"recovered":true}`))
	}); err != nil {
		return err
	}
	h.started <- riverMultiNodeInvocation{owner: job.LeaseOwner, attempt: job.Attempts}
	return nil
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
	riverID := markRiverMultiNodeOrphan(t, ctx, poolA, job.ID)
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
	var orphanOwners []string
	if err := poolA.QueryRow(ctx, `SELECT attempted_by FROM public.river_job WHERE id=$1`, riverID).Scan(&orphanOwners); err != nil {
		t.Fatal(err)
	}
	if len(orphanOwners) != 1 || orphanOwners[0] != riverMultiNodeOwnerA {
		t.Fatalf("orphaned River owners = %#v, want only dead owner", orphanOwners)
	}

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
	// The same rescue pass sees the three-minute-old long-running orphan, but
	// its preserved 25-hour rescue horizon leaves it on attempt one.
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
		t.Fatalf("long-running orphan was reclaimed at the two-minute candidate horizon: %#v", unexpected)
	default:
	}
}

func TestRefreshPipelineOrphanIsReclaimedWithoutTouchingLiveOrLongJobs(t *testing.T) {
	harness := postgrestest.Start(t)
	database := harness.NewDatabase(t, "river_refresh_pipeline_orphan")
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
	if _, err := poolA.Exec(ctx, refreshpostgres.SchemaSQL()); err != nil {
		t.Fatal(err)
	}

	repositoryA, repositoryB := jobpostgres.NewRepository(poolA), jobpostgres.NewRepository(poolB)
	refreshA, refreshB := refreshpostgres.New(poolA), refreshpostgres.New(poolB)
	nodeA := buildRiverMultiNodeModule(t, repositoryA, riverMultiNodeOwnerA)
	nodeB := buildRiverMultiNodeModule(t, repositoryB, riverMultiNodeOwnerB)
	started := make(chan riverMultiNodeInvocation, 2)
	lease := 100 * time.Millisecond
	refreshHandlerA := &refreshOrphanTestHandler{recovery: refreshmodule.NewPostgresJobsAdapter(repositoryA, refreshA), refresh: refreshA, lease: lease, started: started}
	refreshHandlerB := &refreshOrphanTestHandler{recovery: refreshmodule.NewPostgresJobsAdapter(repositoryB, refreshB), refresh: refreshB, lease: lease, started: started}
	longStarted := make(chan riverMultiNodeInvocation, 1)
	longHandler := jobs.HandlerFunc{JobKind: "release.finalize", Run: func(_ context.Context, job jobs.Job) error {
		longStarted <- riverMultiNodeInvocation{owner: job.LeaseOwner, attempt: job.Attempts}
		return nil
	}}
	if err := nodeA.RegisterHandlers([]jobs.Handler{refreshHandlerA, longHandler}); err != nil {
		t.Fatal(err)
	}
	if err := nodeB.RegisterHandlers([]jobs.Handler{refreshHandlerB, longHandler}); err != nil {
		t.Fatal(err)
	}

	seedRefreshRecoveryPageSaturation(t, ctx, poolA)
	dead := createRefreshOrphanFixture(t, ctx, nodeA, refreshA, poolA, "refresh-dead-job", "refresh-dead-run", "project-dead", lease, false, false)
	establishDivergentRefreshOrphan(t, ctx, refreshHandlerA.recovery, poolA, dead, lease)
	healthy := createRefreshOrphanFixture(t, ctx, nodeA, refreshA, poolA, "refresh-live-job", "refresh-live-run", "project-live", lease, true, true)
	liveLease := createRefreshOrphanFixture(t, ctx, nodeA, refreshA, poolA, "refresh-current-job", "refresh-current-run", "project-current", time.Hour, false, true)
	changed := createRefreshOrphanFixture(t, ctx, nodeA, refreshA, poolA, "refresh-changed-job", "refresh-changed-run", "project-changed", lease, true, true)
	if _, err := poolA.Exec(ctx, `UPDATE public.river_job SET state='cancelled',finalized_at=clock_timestamp() WHERE id=(SELECT river_job_id FROM jobs.job_history WHERE id=$1)`, changed.ID); err != nil {
		t.Fatal(err)
	}
	releaseHealthy, head, err := repositoryA.AcquirePartition(ctx, healthy)
	if err != nil || !head {
		t.Fatalf("hold healthy refresh partition: head=%v err=%v", head, err)
	}
	t.Cleanup(releaseHealthy)
	longRunning := enqueueTestJob(t, nodeA, "river-refresh-long-running", "release-refresh-long")
	markRiverMultiNodeOrphan(t, ctx, poolA, longRunning.ID)

	tx, err := poolB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rescued, err := repositoryB.RescueExpiredRefreshJobTx(ctx, tx, longRunning.ID, "not-a-refresh-run", riverMultiNodeOwnerA, 1)
	_ = tx.Rollback(ctx)
	if err != nil || rescued {
		t.Fatalf("wrong-kind orphan rescue = %v, %v; want false, nil", rescued, err)
	}
	if err := nodeA.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nodeA.Stop(context.Background()) })
	invocation := waitRiverMultiNodeInvocation(t, ctx, started)
	if invocation.owner != riverMultiNodeOwnerA || invocation.attempt != 3 {
		t.Fatalf("refresh recovery invocation = %#v, want attempt 3 on replacement node", invocation)
	}
	recovered := waitRiverMultiNodeProduct(t, ctx, nodeA, dead.ID, jobs.StatusSucceeded)
	if recovered.Attempts != 3 {
		t.Fatalf("recovered refresh product attempts = %d, want 3", recovered.Attempts)
	}
	assertRiverMultiNodeEvidence(t, ctx, poolB, dead.ID, rivertype.JobStateCompleted, 3, 1)
	var runStatus string
	var runAttempts int
	if err := poolB.QueryRow(ctx, `SELECT status,attempt_count FROM refresh.run WHERE run_id=$1`, dead.ResourceID).Scan(&runStatus, &runAttempts); err != nil {
		t.Fatal(err)
	}
	if runStatus != "succeeded" || runAttempts != 2 {
		t.Fatalf("recovered refresh run = %s/%d, want succeeded/2", runStatus, runAttempts)
	}
	var operationalRows, productRows int
	if err := poolB.QueryRow(ctx, `SELECT count(*) FROM public.river_job r JOIN jobs.job_history h ON h.river_job_id=r.id WHERE h.resource_kind='refresh_run' AND h.resource_id=$1`, dead.ResourceID).Scan(&operationalRows); err != nil {
		t.Fatal(err)
	}
	if err := poolB.QueryRow(ctx, `SELECT count(*) FROM jobs.job_history WHERE resource_kind='refresh_run' AND resource_id=$1`, dead.ResourceID).Scan(&productRows); err != nil {
		t.Fatal(err)
	}
	if operationalRows != 1 || productRows != 1 {
		t.Fatalf("refresh intent rows = River %d/product %d, want one of each", operationalRows, productRows)
	}

	for _, untouched := range []jobs.Job{healthy, liveLease, longRunning} {
		assertRiverMultiNodeEvidence(t, ctx, poolB, untouched.ID, rivertype.JobStateRunning, 1, 1)
		current, err := nodeB.Get(ctx, untouched.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != jobs.StatusRunning || current.Attempts != 1 {
			t.Fatalf("protected job %s = %s/%d, want running/1", untouched.ID, current.Status, current.Attempts)
		}
	}
	assertRiverMultiNodeEvidence(t, ctx, poolB, changed.ID, rivertype.JobStateCancelled, 1, 1)
	changedProduct, err := nodeB.Get(ctx, changed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if changedProduct.Status != jobs.StatusRunning || changedProduct.Attempts != 1 {
		t.Fatalf("concurrently changed refresh product = %s/%d, want untouched running/1", changedProduct.Status, changedProduct.Attempts)
	}
	select {
	case unexpected := <-longStarted:
		t.Fatalf("24-hour worker was reclaimed by refresh recovery: %#v", unexpected)
	default:
	}
}

func seedRefreshRecoveryPageSaturation(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	const marker = "refresh-page-saturation"
	run := func(query string, args ...any) {
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	run(`INSERT INTO public.river_job(state,attempt,max_attempts,attempted_at,finalized_at,scheduled_at,attempted_by,args,kind) SELECT 'cancelled',1,3,clock_timestamp()-interval '10 minutes',clock_timestamp()-interval '10 minutes',clock_timestamp()-interval '10 minutes',ARRAY['page-saturation-dead'],jsonb_build_object('page_saturation',$1::text),'refresh_pipeline' FROM generate_series(1,101)`, marker)
	run(`INSERT INTO jobs.job_history(id,kind,workload_class,principal_id,group_ids,partition_key,resource_kind,resource_id,estimated_memory_bytes,payload,request_digest,river_job_id) SELECT 'refresh-page-saturated-job-'||r.id,'refresh_pipeline','background','system','[]','refresh:page-saturation:production','refresh_run','refresh-page-saturated-run-'||r.id,1,'{}','sha256:'||repeat('a',64),r.id FROM public.river_job r WHERE r.kind='refresh_pipeline' AND r.args->>'page_saturation'=$1`, marker)
	run(`INSERT INTO refresh.run(run_id,project_id,environment,generation_id,pipeline_id,semantic_model_id,target_type,target_id,trigger_type,invocation_source,plan_digest,artifact_digest,principal_id,job_id) SELECT 'refresh-page-saturated-run-'||r.id,'page-saturation','production','generation-1','pipeline-1','semantic-1','refresh_pipeline','pipeline-1','manual','manual','sha256:'||repeat('a',64),'sha256:'||repeat('a',64),'system',h.id FROM public.river_job r JOIN jobs.job_history h ON h.river_job_id=r.id WHERE r.kind='refresh_pipeline' AND r.args->>'page_saturation'=$1`, marker)
	run(`UPDATE refresh.run SET status='running',attempt_count=1,fence_generation=1,lease_owner='page-saturation-dead',lease_expires_at=clock_timestamp()+interval '100 milliseconds',started_at=clock_timestamp() WHERE project_id='page-saturation'; INSERT INTO refresh.attempt(run_id,attempt_number,fence_generation,owner_id,lease_expires_at) SELECT run_id,1,1,'page-saturation-dead',lease_expires_at FROM refresh.run WHERE project_id='page-saturation'`)
	time.Sleep(200 * time.Millisecond)
	run(`UPDATE jobs.job_history SET status='running',attempt_count=1,started_at=clock_timestamp()-interval '10 minutes' WHERE partition_key='refresh:page-saturation:production'`)
}

func createRefreshOrphanFixture(t *testing.T, ctx context.Context, node *Module, refresh *refreshpostgres.Repository, pool *pgxpool.Pool, jobID, runID, projectID string, lease time.Duration, expire, claim bool) jobs.Job {
	t.Helper()
	input := testJobInput(jobID, runID)
	input.Kind, input.WorkloadClass = refreshPipelineKind, "background"
	input.PartitionKey, input.ResourceKind = "refresh:"+projectID+":production", "refresh_run"
	digest := "sha256:" + strings.Repeat("a", 64)
	plan, err := projectpipelineplan.New(projectpipelineplan.Plan{ID: "plan-" + runID, PipelineID: "pipeline-1", ProjectID: projectID, Environment: "production", SemanticModelID: "semantic-1", ServingGenerationID: "generation-1", ArtifactDigest: digest, SelectionDigest: digest, MaterializationScope: []string{"model-1"}, ModelExecutionOrder: []string{"model-1"}, InvocationSource: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	input.Payload, err = json.Marshal(map[string]any{"pipelinePlan": &plan, "input": json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	job, err := node.Enqueue(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO refresh.run(run_id,project_id,environment,generation_id,pipeline_id,semantic_model_id,target_type,target_id,trigger_type,invocation_source,plan_digest,artifact_digest,principal_id,job_id) VALUES($1,$2,'production','generation-1','pipeline-1','semantic-1','refresh_pipeline','pipeline-1','manual','manual',$3,$3,$4,$5)`, runID, projectID, digest, input.PrincipalID, job.ID); err != nil {
		t.Fatal(err)
	}
	if claim {
		if _, err := refresh.ClaimAttempt(ctx, runID, riverMultiNodeOwnerA, 1, lease); err != nil {
			t.Fatal(err)
		}
		if expire {
			waitForExpiredRefreshLease(t, ctx, pool, runID)
		}
	}
	markRiverMultiNodeOrphan(t, ctx, pool, job.ID)
	return job
}

func establishDivergentRefreshOrphan(t *testing.T, ctx context.Context, adapter *refreshmodule.PostgresJobsAdapter, pool *pgxpool.Pool, job jobs.Job, lease time.Duration) {
	t.Helper()
	var riverID int64
	if err := pool.QueryRow(ctx, `SELECT river_job_id FROM jobs.job_history WHERE id=$1`, job.ID).Scan(&riverID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE public.river_job SET state='retryable' WHERE id=$1`, riverID); err != nil {
		t.Fatal(err)
	}
	firstJob := &river.Job[jobpostgres.RefreshPipelineArgs]{JobRow: &rivertype.JobRow{ID: riverID, Attempt: 1, State: rivertype.JobStateRunning, AttemptedBy: []string{riverMultiNodeOwnerA}}}
	firstCtx := jobpostgres.ContextWithRiverExecution(ctx, firstJob, riverMultiNodeOwnerA, lease)
	firstHistory, err := adapter.Jobs.Get(firstCtx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	firstHistory.LeaseOwner, firstHistory.LeaseGeneration = riverMultiNodeOwnerA, 1
	if _, err := adapter.ClaimRiverJob(firstCtx, firstHistory, lease); !errors.Is(err, jobs.ErrConflict) {
		t.Fatalf("rescued pre-claim attempt error = %v, want conflict", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE public.river_job SET state='running',attempt=2,attempted_by=array_append(attempted_by,$2) WHERE id=$1`, riverID, riverMultiNodeOwnerB); err != nil {
		t.Fatal(err)
	}
	riverJob := &river.Job[jobpostgres.RefreshPipelineArgs]{JobRow: &rivertype.JobRow{ID: riverID, Attempt: 2, State: rivertype.JobStateRunning, AttemptedBy: []string{riverMultiNodeOwnerA, riverMultiNodeOwnerB}}}
	executionCtx := jobpostgres.ContextWithRiverExecution(ctx, riverJob, riverMultiNodeOwnerB, lease)
	history, err := adapter.Jobs.MarkRunning(executionCtx, job.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	history.LeaseOwner, history.LeaseGeneration = riverMultiNodeOwnerB, 2
	if _, err := adapter.ClaimRiverJob(executionCtx, history, lease); err != nil {
		t.Fatal(err)
	}
	var attempts, fence int
	if err := pool.QueryRow(ctx, `SELECT attempt_count,fence_generation FROM refresh.run WHERE run_id=$1`, job.ResourceID).Scan(&attempts, &fence); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || fence != 2 {
		t.Fatalf("pre-claim handoff evidence = attempts %d/fence %d, want 1/2", attempts, fence)
	}
	waitForExpiredRefreshLease(t, ctx, pool, job.ResourceID)
}

func waitForExpiredRefreshLease(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var expired bool
		if err := pool.QueryRow(ctx, `SELECT r.lease_expires_at <= clock_timestamp() AND EXISTS (SELECT 1 FROM refresh.attempt a WHERE a.run_id=r.run_id AND a.attempt_number=r.attempt_count AND a.lease_expires_at <= clock_timestamp()) FROM refresh.run r WHERE r.run_id=$1`, runID).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("refresh lease for %s did not expire", runID)
		}
		time.Sleep(10 * time.Millisecond)
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
