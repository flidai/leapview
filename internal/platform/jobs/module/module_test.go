package module

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/internal/workload"
)

func testAdmission(controller workload.Admitter) jobs.Admitter {
	return jobs.AdmitterFunc(func(ctx context.Context, request jobs.AdmissionRequest) (jobs.AdmissionLease, error) {
		return controller.Acquire(ctx, workload.Request{
			Class: workload.Class(request.Class), WorkspaceID: request.WorkspaceID, Operation: request.Operation,
		})
	})
}

func TestModuleRestartRecoversInterruptedClaim(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	admission, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Close()

	first, err := Build(t.Context(), Config{
		Database: store.SQLDB(), Admission: testAdmission(admission),
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
		ID: "release:one:finalize", Kind: "release.finalize", WorkloadClass: "control", WorkspaceID: "_node",
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
	if _, err := store.SQLDB().ExecContext(t.Context(),
		`UPDATE api_async_jobs SET lease_expires_at = datetime('now', '-1 second') WHERE id = ?`,
		interrupted.ID,
	); err != nil {
		t.Fatal(err)
	}

	second, err := Build(t.Context(), Config{
		Database: store.SQLDB(), Admission: testAdmission(admission),
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
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	admission, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Close()
	module, err := Build(t.Context(), Config{Database: store.SQLDB(), Admission: testAdmission(admission)})
	if err != nil {
		t.Fatal(err)
	}
	handler := jobs.HandlerFunc{JobKind: "duplicate", Run: func(context.Context, jobs.Job) error { return nil }}
	if err := module.RegisterHandlers([]jobs.Handler{handler, handler}); err == nil {
		t.Fatal("duplicate handler kinds were accepted")
	}
}

func TestModuleLifecycleIsIdempotent(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	admission, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Close()
	module, err := Build(t.Context(), Config{Database: store.SQLDB(), Admission: testAdmission(admission)})
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
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	admission, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Close()
	module, err := Build(t.Context(), Config{
		Database: store.SQLDB(), Admission: testAdmission(admission), PollInterval: time.Millisecond,
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
		ID: "restartable-one", Kind: "restartable", WorkloadClass: "control", WorkspaceID: "_node",
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
		ID: "restartable-two", Kind: "restartable", WorkloadClass: "control", WorkspaceID: "_node",
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
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	admission, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Close()
	module, err := Build(t.Context(), Config{Database: store.SQLDB(), Admission: testAdmission(admission)})
	if err != nil {
		t.Fatal(err)
	}
	if err := module.RegisterHandlers(nil); err != nil {
		t.Fatal(err)
	}
	tx, err := store.SQLDB().BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	err = module.RecordWorkflow(t.Context(), tx, jobs.WorkflowIntent{Event: jobs.EventInput{
		Key: "release.ready", ResourceKind: "release", ResourceID: "release-1",
		EventType: "release.ready", Data: []byte(`{"status":"ready"}`),
	}})
	if err != nil {
		t.Fatalf("RecordWorkflow() terminal event error = %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	events, err := module.ListEvents(t.Context(), "release", "release-1", 0, 10)
	if err != nil || len(events) != 1 || events[0].EventType != "release.ready" {
		t.Fatalf("terminal events = %#v, %v", events, err)
	}
}

func TestModuleCommitsWorkflowAtomically(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	admission, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Close()
	module, err := Build(t.Context(), Config{Database: store.SQLDB(), Admission: testAdmission(admission)})
	if err != nil {
		t.Fatal(err)
	}
	if err := module.RegisterHandlers([]jobs.Handler{jobs.HandlerFunc{JobKind: "deployment.activate", Run: func(context.Context, jobs.Job) error { return nil }}}); err != nil {
		t.Fatal(err)
	}
	intent := jobs.WorkflowIntent{
		Event: jobs.EventInput{Key: "deployment.queued", ResourceKind: "deployment", ResourceID: "deployment-1", EventType: "deployment.queued", Data: []byte(`{"status":"queued"}`)},
		Job:   jobs.EnqueueInput{ID: "deployment:deployment-1:activate", Kind: "deployment.activate", WorkloadClass: "control", WorkspaceID: "_node", ResourceKind: "deployment", ResourceID: "deployment-1", Payload: []byte(`{}`)},
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
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	admission, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Close()
	module, err := Build(t.Context(), Config{Database: store.SQLDB(), Admission: testAdmission(admission)})
	if err != nil {
		t.Fatal(err)
	}
	if err := module.RegisterHandlers(nil); err != nil {
		t.Fatal(err)
	}
	err = module.CommitWorkflow(t.Context(), jobs.WorkflowIntent{
		Event: jobs.EventInput{Key: "deployment.queued", ResourceKind: "deployment", ResourceID: "deployment-1", EventType: "deployment.queued", Data: []byte(`{"status":"queued"}`)},
		Job:   jobs.EnqueueInput{ID: "deployment:deployment-1:activate", Kind: "unknown", WorkloadClass: "control", WorkspaceID: "_node", ResourceKind: "deployment", ResourceID: "deployment-1", Payload: []byte(`{}`)},
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
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	admission, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Close()
	module, err := Build(t.Context(), Config{Database: store.SQLDB(), Admission: testAdmission(admission)})
	if err != nil {
		t.Fatal(err)
	}
	handler := jobs.HandlerFunc{JobKind: "known", Run: func(context.Context, jobs.Job) error { return nil }}
	if err := module.RegisterHandlers([]jobs.Handler{handler}); err != nil {
		t.Fatal(err)
	}
	_, err = module.Enqueue(t.Context(), jobs.EnqueueInput{
		ID: "unknown-1", Kind: "unknown", WorkloadClass: "control", WorkspaceID: "_node",
		ResourceKind: "test", ResourceID: "unknown-1", Payload: []byte(`{}`),
	})
	if !errors.Is(err, jobs.ErrUnknownKind) {
		t.Fatalf("Enqueue() error = %v, want ErrUnknownKind", err)
	}
}
