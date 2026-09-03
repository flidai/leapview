package module

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jobpolicy "github.com/flidai/leapview/internal/platform/jobs"
	jobpostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/internal/workload"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBuildRejectsUnmarkedOrLegacyAuthorityForProduction(t *testing.T) {
	admitter := jobs.AdmitterFunc(func(context.Context, jobs.AdmissionRequest) (jobs.AdmissionLease, error) {
		return nil, nil
	})
	if _, err := Build(t.Context(), Config{Admission: admitter}); err == nil || !strings.Contains(err.Error(), "persistence") {
		t.Fatalf("missing authority build error = %v, want persistence rejection", err)
	}
	if _, err := Build(t.Context(), Config{Persistence: &Persistence{}, Production: true, Admission: admitter}); err == nil || !strings.Contains(err.Error(), "PostgreSQL") {
		t.Fatalf("unmarked production build error = %v, want PostgreSQL rejection", err)
	}
	if _, err := Build(t.Context(), Config{Production: true, Admission: admitter}); err == nil || !strings.Contains(err.Error(), "persistence") {
		t.Fatalf("missing production authority error = %v, want persistence rejection", err)
	}
}

func TestNewPostgresPersistenceRejectsUnconfiguredRepository(t *testing.T) {
	if _, err := NewPostgresPersistence(jobpostgres.NewRepository(nil)); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("unconfigured PostgreSQL repository error = %v", err)
	}
}

func TestModuleFailurePayloadOmitsHandlerErrorText(t *testing.T) {
	admission, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Close()
	module, err := Build(t.Context(), Config{
		Persistence: testPersistence(t), Admission: testAdmission(admission), PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := module.RegisterHandlers([]jobs.Handler{jobs.HandlerFunc{
		JobKind: "secret.failure",
		Run:     func(context.Context, jobs.Job) error { return errors.New("attacker-secret-sql-and-payload") },
	}}); err != nil {
		t.Fatal(err)
	}
	if err := module.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer module.Stop(context.Background())
	const id = "secret-failure"
	if _, err := module.Enqueue(t.Context(), jobs.EnqueueInput{
		ID: id, Kind: "secret.failure", WorkloadClass: "control", PrincipalID: jobpolicy.SystemPrincipalID, GroupIDs: []string{}, EstimatedMemoryBytes: 1,
		ResourceKind: "test", ResourceID: id, Payload: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		job, getErr := module.Get(t.Context(), id)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if job.Status == jobs.StatusFailed {
			if strings.Contains(job.ErrorJSON, "attacker-secret") || job.ErrorJSON != `{"code":"ASYNC_JOB_FAILED"}` {
				t.Fatalf("failure payload = %s, want stable code without handler error", job.ErrorJSON)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job status = %q, want failed", job.Status)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestModulePostgreSQL18BuildRunnerAndNativeWorkflow(t *testing.T) {
	harness := postgrestest.Start(t)
	database := harness.NewDatabase(t, "jobs_module_test")
	pool, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(t.Context(), jobpostgres.SchemaSQL()); err != nil {
		t.Fatal(err)
	}
	repository := jobpostgres.NewRepository(pool)
	persistence, err := NewPostgresPersistence(repository)
	if err != nil {
		t.Fatal(err)
	}
	admission, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Close()
	module, err := Build(t.Context(), Config{
		Persistence: &persistence, Production: true, Admission: testAdmission(admission), PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := module.RegisterHandlers([]jobs.Handler{jobs.HandlerFunc{
		JobKind: "module.pg18",
		Run:     func(context.Context, jobs.Job) error { return nil },
	}}); err != nil {
		t.Fatal(err)
	}
	if err := module.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer module.Stop(context.Background())
	const jobID = "module-pg18-job"
	if _, err := module.Enqueue(t.Context(), jobs.EnqueueInput{
		ID: jobID, Kind: "module.pg18", WorkloadClass: "control", PrincipalID: jobpolicy.SystemPrincipalID, GroupIDs: []string{}, EstimatedMemoryBytes: 1,
		PartitionKey: "module:pg18",
		ResourceKind: "module", ResourceID: jobID, Payload: []byte(`{"ok":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	waitForJobStatus(t, module, jobID, jobs.StatusSucceeded)

	workflow := jobs.WorkflowIntent{
		Event: jobs.EventInput{Key: "module.pg18.commit", ResourceKind: "module", ResourceID: "workflow", EventType: "module.committed", Data: []byte(`{"status":"committed"}`)},
		Job: jobs.EnqueueInput{ID: "module-pg18-workflow", Kind: "module.pg18", WorkloadClass: "control", PrincipalID: jobpolicy.SystemPrincipalID, GroupIDs: []string{}, PartitionKey: "module:workflow", EstimatedMemoryBytes: 1,
			ResourceKind: "module", ResourceID: "workflow", Payload: []byte(`{}`)},
	}
	if err := module.CommitWorkflow(t.Context(), workflow); err != nil {
		t.Fatal(err)
	}
	if _, err := module.Get(t.Context(), workflow.Job.ID); err != nil {
		t.Fatalf("committed workflow job: %v", err)
	}

	native := jobs.WorkflowIntent{Event: jobs.EventInput{Key: "module.pg18.native", ResourceKind: "module", ResourceID: "native", EventType: "module.native", Data: []byte(`{"status":"native"}`)}}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := module.RecordWorkflowTx(t.Context(), tx, native); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if events, err := module.ListEvents(t.Context(), "module", "native", 0, 10); err != nil {
		t.Fatal(err)
	} else if len(events) != 0 {
		t.Fatalf("rolled-back native workflow events = %#v", events)
	}
	tx, err = pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := module.RecordWorkflowTx(t.Context(), tx, native); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if events, err := module.ListEvents(t.Context(), "module", "native", 0, 10); err != nil {
		t.Fatal(err)
	} else if len(events) != 1 || events[0].EventType != "module.native" {
		t.Fatalf("committed native workflow events = %#v", events)
	}
}

func waitForJobStatus(t *testing.T, module *Module, id string, want jobs.Status) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		job, err := module.Get(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job status = %q, want %q", job.Status, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func testAdmission(controller workload.Admitter) jobs.Admitter {
	return jobs.AdmitterFunc(func(ctx context.Context, request jobs.AdmissionRequest) (jobs.AdmissionLease, error) {
		return controller.Acquire(ctx, workload.Request{
			Class: workload.Class(request.Class), PrincipalID: request.PrincipalID, GroupIDs: request.GroupIDs,
			EstimatedMemoryBytes: request.EstimatedMemoryBytes, Operation: request.Operation,
		})
	})
}

func testPersistence(t *testing.T) *Persistence {
	t.Helper()
	queue := &inMemoryRepository{jobs: make(map[string]jobs.Job), events: make(map[string][]jobs.Event)}
	return &Persistence{
		Repository: queue, backend: backendPostgres,
		NativeWorkflow: testWorkflowRecorder{}, NativeCommitter: queue,
	}
}

// These test-only adapters exercise queue lifecycle behavior without selecting
// a storage topology. The production module accepts only the native authority
// constructor; package-local tests may inject this structural double.
type testWorkflowRecorder struct{}

func (testWorkflowRecorder) RecordWorkflow(context.Context, jobpostgres.Tx, jobs.WorkflowIntent) error {
	return nil
}

type inMemoryRepository struct {
	mu     sync.Mutex
	jobs   map[string]jobs.Job
	events map[string][]jobs.Event
	nextID int64
}

func cloneJob(job jobs.Job) jobs.Job {
	job.GroupIDs = append([]string(nil), job.GroupIDs...)
	job.Payload = append([]byte(nil), job.Payload...)
	return job
}

func (r *inMemoryRepository) Enqueue(_ context.Context, input jobs.EnqueueInput) (jobs.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.jobs[input.ID]; ok {
		return cloneJob(existing), nil
	}
	groups, err := jobs.CanonicalActor(input.PrincipalID, input.GroupIDs)
	if err != nil {
		return jobs.Job{}, err
	}
	if input.ID == "" || input.Kind == "" || input.ResourceKind == "" || input.ResourceID == "" || input.EstimatedMemoryBytes <= 0 || !json.Valid(input.Payload) {
		return jobs.Job{}, fmt.Errorf("invalid async job")
	}
	job := jobs.Job{ID: input.ID, Kind: input.Kind, WorkloadClass: input.WorkloadClass, PrincipalID: input.PrincipalID, GroupIDs: groups,
		PartitionKey: input.PartitionKey, ResourceKind: input.ResourceKind, ResourceID: input.ResourceID, EstimatedMemoryBytes: input.EstimatedMemoryBytes,
		Payload: append([]byte(nil), input.Payload...), Status: jobs.StatusQueued, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	r.jobs[input.ID] = job
	return cloneJob(job), nil
}

func (r *inMemoryRepository) Get(_ context.Context, id string) (jobs.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[id]
	if !ok {
		return jobs.Job{}, jobs.ErrNotFound
	}
	return cloneJob(job), nil
}

func (r *inMemoryRepository) Candidates(_ context.Context, class string, limit int) ([]jobs.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	result := make([]jobs.Job, 0, len(r.jobs))
	for _, job := range r.jobs {
		if job.WorkloadClass != class || (job.Status != jobs.StatusQueued && !(job.Status == jobs.StatusRunning && leaseExpired(job.LeaseExpiresAt, now))) {
			continue
		}
		result = append(result, cloneJob(job))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt < result[j].CreatedAt })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func leaseExpired(value string, now time.Time) bool {
	if value == "" {
		return true
	}
	expires, err := time.Parse(time.RFC3339Nano, value)
	return err != nil || !expires.After(now)
}

func (r *inMemoryRepository) ClaimByID(_ context.Context, id, class, owner string, lease time.Duration) (jobs.Job, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[id]
	if !ok || job.WorkloadClass != class || (job.Status != jobs.StatusQueued && !(job.Status == jobs.StatusRunning && leaseExpired(job.LeaseExpiresAt, time.Now()))) {
		return jobs.Job{}, false, nil
	}
	job.Status, job.Attempts = jobs.StatusRunning, job.Attempts+1
	job.LeaseOwner, job.LeaseGeneration = owner, job.LeaseGeneration+1
	job.LeaseExpiresAt = time.Now().Add(lease).UTC().Format(time.RFC3339Nano)
	if job.StartedAt == "" {
		job.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	r.jobs[id] = job
	return cloneJob(job), true, nil
}

func (r *inMemoryRepository) fenced(id string, fence jobs.Fence) (jobs.Job, error) {
	job, ok := r.jobs[id]
	if !ok {
		return jobs.Job{}, jobs.ErrNotFound
	}
	if job.Status != jobs.StatusRunning || job.LeaseOwner != fence.Owner || job.LeaseGeneration != fence.Generation {
		return jobs.Job{}, jobs.ErrConflict
	}
	return job, nil
}

func (r *inMemoryRepository) Renew(_ context.Context, id string, fence jobs.Fence, lease time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, err := r.fenced(id, fence)
	if err != nil {
		return err
	}
	job.LeaseExpiresAt = time.Now().Add(lease).UTC().Format(time.RFC3339Nano)
	r.jobs[id] = job
	return nil
}

func (r *inMemoryRepository) Complete(_ context.Context, id string, fence jobs.Fence) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, err := r.fenced(id, fence)
	if err != nil {
		return err
	}
	job.Status, job.FinishedAt, job.LeaseOwner, job.LeaseExpiresAt = jobs.StatusSucceeded, time.Now().UTC().Format(time.RFC3339Nano), "", ""
	r.jobs[id] = job
	return nil
}

func (r *inMemoryRepository) Fail(_ context.Context, id string, fence jobs.Fence, problem []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, err := r.fenced(id, fence)
	if err != nil {
		return err
	}
	job.Status, job.ErrorJSON, job.FinishedAt, job.LeaseOwner, job.LeaseExpiresAt = jobs.StatusFailed, string(problem), time.Now().UTC().Format(time.RFC3339Nano), "", ""
	r.jobs[id] = job
	return nil
}

func (r *inMemoryRepository) Cancel(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[id]
	if !ok {
		return jobs.ErrNotFound
	}
	if job.Status != jobs.StatusQueued {
		return jobs.ErrConflict
	}
	job.Status, job.FinishedAt = jobs.StatusCancelled, time.Now().UTC().Format(time.RFC3339Nano)
	r.jobs[id] = job
	return nil
}

func (r *inMemoryRepository) CancelClaimed(_ context.Context, id string, fence jobs.Fence) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, err := r.fenced(id, fence)
	if err != nil {
		return err
	}
	job.Status, job.FinishedAt, job.LeaseOwner, job.LeaseExpiresAt = jobs.StatusCancelled, time.Now().UTC().Format(time.RFC3339Nano), "", ""
	r.jobs[id] = job
	return nil
}

func (r *inMemoryRepository) AppendEvent(_ context.Context, kind, id, event string, data []byte) (jobs.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	record := jobs.Event{ID: r.nextID, ResourceKind: kind, ResourceID: id, EventType: event, Data: append([]byte(nil), data...), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	r.events[kind+"\x00"+id] = append(r.events[kind+"\x00"+id], record)
	return record, nil
}

func (r *inMemoryRepository) ListEvents(_ context.Context, kind, id string, after int64, limit int) ([]jobs.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	values := r.events[kind+"\x00"+id]
	result := make([]jobs.Event, 0, len(values))
	for _, event := range values {
		if event.ID > after {
			result = append(result, event)
		}
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func (r *inMemoryRepository) CommitWorkflow(_ context.Context, intent jobs.WorkflowIntent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	event := intent.Event
	record := jobs.Event{ID: r.nextID, ResourceKind: event.ResourceKind, ResourceID: event.ResourceID, EventType: event.EventType, Data: append([]byte(nil), event.Data...), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	r.events[event.ResourceKind+"\x00"+event.ResourceID] = append(r.events[event.ResourceKind+"\x00"+event.ResourceID], record)
	if intent.Job.ID != "" {
		input := intent.Job
		groups, err := jobs.CanonicalActor(input.PrincipalID, input.GroupIDs)
		if err != nil {
			return err
		}
		r.jobs[input.ID] = jobs.Job{ID: input.ID, Kind: input.Kind, WorkloadClass: input.WorkloadClass, PrincipalID: input.PrincipalID, GroupIDs: groups, PartitionKey: input.PartitionKey, ResourceKind: input.ResourceKind, ResourceID: input.ResourceID, EstimatedMemoryBytes: input.EstimatedMemoryBytes, Payload: append([]byte(nil), input.Payload...), Status: jobs.StatusQueued, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	}
	return nil
}

func (r *inMemoryRepository) Expire(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job := r.jobs[id]
	job.LeaseExpiresAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	r.jobs[id] = job
}

func TestModuleRestartRecoversInterruptedClaim(t *testing.T) {
	persistence := testPersistence(t)
	admission, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Close()

	first, err := Build(t.Context(), Config{
		Persistence: persistence, Admission: testAdmission(admission),
		LeaseTimeout: time.Minute, PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	if err := first.RegisterHandlers([]jobs.Handler{jobs.HandlerFunc{
		JobKind: "release.finalize",
		Run: func(ctx context.Context, _ jobs.Job) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := first.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Enqueue(t.Context(), jobs.EnqueueInput{
		ID: "release:one:finalize", Kind: "release.finalize", WorkloadClass: "control", PrincipalID: jobpolicy.SystemPrincipalID, GroupIDs: []string{}, EstimatedMemoryBytes: 1,
		ResourceKind: "release", ResourceID: "one", Payload: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first worker did not claim the job")
	}
	stopContext, cancelStop := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancelStop()
	if err := first.Stop(stopContext); err != nil {
		t.Fatalf("stop first module: %v", err)
	}
	interrupted, err := first.Get(t.Context(), "release:one:finalize")
	if err != nil {
		t.Fatal(err)
	}
	if interrupted.Status != jobs.StatusRunning || interrupted.FinishedAt != "" {
		t.Fatalf("interrupted job = %#v, want recoverable running claim", interrupted)
	}
	persistence.Repository.(*inMemoryRepository).Expire(interrupted.ID)

	second, err := Build(t.Context(), Config{
		Persistence: persistence, Admission: testAdmission(admission),
		LeaseTimeout: time.Minute, PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	handled := make(chan struct{})
	if err := second.RegisterHandlers([]jobs.Handler{jobs.HandlerFunc{
		JobKind: "release.finalize",
		Run: func(context.Context, jobs.Job) error {
			close(handled)
			return nil
		},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := second.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer second.Stop(context.Background())
	select {
	case <-handled:
	case <-time.After(2 * time.Second):
		t.Fatal("replacement worker did not reclaim the interrupted job")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		finished, getErr := second.Get(t.Context(), interrupted.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if finished.Status == jobs.StatusSucceeded {
			if finished.Attempts != 2 {
				t.Fatalf("recovered job attempts = %d, want 2", finished.Attempts)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("recovered job status = %q, want succeeded", finished.Status)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestModuleRejectsDuplicateKindsBeforeStarting(t *testing.T) {
	persistence := testPersistence(t)
	admission, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Close()
	module, err := Build(t.Context(), Config{Persistence: persistence, Admission: testAdmission(admission)})
	if err != nil {
		t.Fatal(err)
	}
	handler := jobs.HandlerFunc{JobKind: "duplicate", Run: func(context.Context, jobs.Job) error { return nil }}
	if err := module.RegisterHandlers([]jobs.Handler{handler, handler}); err == nil {
		t.Fatal("duplicate handler kinds were accepted")
	}
}

func TestModuleLifecycleIsIdempotent(t *testing.T) {
	persistence := testPersistence(t)
	admission, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Close()
	module, err := Build(t.Context(), Config{Persistence: persistence, Admission: testAdmission(admission)})
	if err != nil {
		t.Fatal(err)
	}
	if err := module.RegisterHandlers(nil); err != nil {
		t.Fatal(err)
	}
	if err := module.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := module.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := module.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := module.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestModuleCanRestartAfterTimedOutStopEventuallyFinishes(t *testing.T) {
	persistence := testPersistence(t)
	admission, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Close()
	module, err := Build(t.Context(), Config{
		Persistence: persistence, Admission: testAdmission(admission), PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondHandled := make(chan struct{})
	var calls atomic.Int32
	if err := module.RegisterHandlers([]jobs.Handler{jobs.HandlerFunc{
		JobKind: "restartable",
		Run: func(context.Context, jobs.Job) error {
			switch calls.Add(1) {
			case 1:
				close(firstStarted)
				<-releaseFirst
			case 2:
				close(secondHandled)
			}
			return nil
		},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := module.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := module.Enqueue(t.Context(), jobs.EnqueueInput{
		ID: "restartable-one", Kind: "restartable", WorkloadClass: "control", PrincipalID: jobpolicy.SystemPrincipalID, GroupIDs: []string{}, EstimatedMemoryBytes: 1,
		ResourceKind: "test", ResourceID: "one", Payload: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first job did not start")
	}
	module.mu.Lock()
	firstDone := module.done
	module.mu.Unlock()
	stopContext, cancelStop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err = module.Stop(stopContext)
	cancelStop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed-out Stop() error = %v, want deadline exceeded", err)
	}
	close(releaseFirst)
	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("canceled runner did not eventually finish")
	}

	if err := module.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer module.Stop(context.Background())
	if _, err := module.Enqueue(t.Context(), jobs.EnqueueInput{
		ID: "restartable-two", Kind: "restartable", WorkloadClass: "control", PrincipalID: jobpolicy.SystemPrincipalID, GroupIDs: []string{}, EstimatedMemoryBytes: 1,
		ResourceKind: "test", ResourceID: "two", Payload: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondHandled:
	case <-time.After(2 * time.Second):
		t.Fatal("module did not restart after the timed-out stop completed")
	}
}

func TestModuleRecordsTerminalEventWithoutRegisteredFollowupKind(t *testing.T) {
	persistence := testPersistence(t)
	admission, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Close()
	module, err := Build(t.Context(), Config{Persistence: persistence, Admission: testAdmission(admission)})
	if err != nil {
		t.Fatal(err)
	}
	err = module.CommitWorkflow(t.Context(), jobs.WorkflowIntent{Event: jobs.EventInput{
		Key: "release.ready", ResourceKind: "release", ResourceID: "release-1",
		EventType: "release.ready", Data: []byte(`{"status":"ready"}`),
	}})
	if err != nil {
		t.Fatalf("CommitWorkflow() terminal event error = %v", err)
	}
	events, err := module.ListEvents(t.Context(), "release", "release-1", 0, 10)
	if err != nil || len(events) != 1 || events[0].EventType != "release.ready" {
		t.Fatalf("terminal events = %#v, %v", events, err)
	}
}

func TestModuleCommitsWorkflowAtomically(t *testing.T) {
	persistence := testPersistence(t)
	admission, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Close()
	module, err := Build(t.Context(), Config{Persistence: persistence, Admission: testAdmission(admission)})
	if err != nil {
		t.Fatal(err)
	}
	if err := module.RegisterHandlers([]jobs.Handler{jobs.HandlerFunc{JobKind: "deployment.activate", Run: func(context.Context, jobs.Job) error { return nil }}}); err != nil {
		t.Fatal(err)
	}
	intent := jobs.WorkflowIntent{
		Event: jobs.EventInput{Key: "deployment.queued", ResourceKind: "deployment", ResourceID: "deployment-1", EventType: "deployment.queued", Data: []byte(`{"status":"queued"}`)},
		Job:   jobs.EnqueueInput{ID: "deployment:deployment-1:activate", Kind: "deployment.activate", WorkloadClass: "control", PrincipalID: jobpolicy.SystemPrincipalID, GroupIDs: []string{}, EstimatedMemoryBytes: 1, ResourceKind: "deployment", ResourceID: "deployment-1", Payload: []byte(`{}`)},
	}
	if err := module.CommitWorkflow(t.Context(), intent); err != nil {
		t.Fatal(err)
	}
	if _, err := module.Get(t.Context(), intent.Job.ID); err != nil {
		t.Fatalf("committed job: %v", err)
	}
	events, err := module.ListEvents(t.Context(), "deployment", "deployment-1", 0, 10)
	if err != nil || len(events) != 1 || events[0].EventType != "deployment.queued" {
		t.Fatalf("committed events = %#v, %v", events, err)
	}
}

func TestModuleCommitWorkflowRejectsUnknownKindWithoutPersistingEvent(t *testing.T) {
	persistence := testPersistence(t)
	admission, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Close()
	module, err := Build(t.Context(), Config{Persistence: persistence, Admission: testAdmission(admission)})
	if err != nil {
		t.Fatal(err)
	}
	if err := module.RegisterHandlers(nil); err != nil {
		t.Fatal(err)
	}
	err = module.CommitWorkflow(t.Context(), jobs.WorkflowIntent{
		Event: jobs.EventInput{Key: "deployment.queued", ResourceKind: "deployment", ResourceID: "deployment-1", EventType: "deployment.queued", Data: []byte(`{"status":"queued"}`)},
		Job:   jobs.EnqueueInput{ID: "deployment:deployment-1:activate", Kind: "unknown", WorkloadClass: "control", PrincipalID: jobpolicy.SystemPrincipalID, GroupIDs: []string{}, ResourceKind: "deployment", ResourceID: "deployment-1", Payload: []byte(`{}`)},
	})
	if !errors.Is(err, jobs.ErrUnknownKind) {
		t.Fatalf("CommitWorkflow() error = %v, want unknown kind", err)
	}
	events, listErr := module.ListEvents(t.Context(), "deployment", "deployment-1", 0, 10)
	if listErr != nil || len(events) != 0 {
		t.Fatalf("rolled-back events = %#v, %v", events, listErr)
	}
}

func TestModuleRejectsUnknownEnqueuedKind(t *testing.T) {
	persistence := testPersistence(t)
	admission, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Close()
	module, err := Build(t.Context(), Config{Persistence: persistence, Admission: testAdmission(admission)})
	if err != nil {
		t.Fatal(err)
	}
	handler := jobs.HandlerFunc{JobKind: "known", Run: func(context.Context, jobs.Job) error { return nil }}
	if err := module.RegisterHandlers([]jobs.Handler{handler}); err != nil {
		t.Fatal(err)
	}
	_, err = module.Enqueue(t.Context(), jobs.EnqueueInput{
		ID: "unknown-1", Kind: "unknown", WorkloadClass: "control", PrincipalID: jobpolicy.SystemPrincipalID, GroupIDs: []string{}, EstimatedMemoryBytes: 1,
		ResourceKind: "test", ResourceID: "unknown-1", Payload: []byte(`{}`),
	})
	if !errors.Is(err, jobs.ErrUnknownKind) {
		t.Fatalf("Enqueue() error = %v, want ErrUnknownKind", err)
	}
}
