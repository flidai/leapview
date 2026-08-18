package jobs

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/workload"
)

func testAdmitter(controller workload.Admitter) Admitter {
	return AdmitterFunc(func(ctx context.Context, request AdmissionRequest) (AdmissionLease, error) {
		return controller.Acquire(ctx, workload.Request{
			Class: workload.Class(request.Class), PrincipalID: request.PrincipalID, GroupIDs: request.GroupIDs,
			EstimatedMemoryBytes: request.EstimatedMemoryBytes, Operation: request.Operation,
		})
	})
}

func TestNewRunnerRejectsDuplicateHandlerKinds(t *testing.T) {
	controller, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	repository := &runnerTestRepository{}
	_, err = NewRunner(RunnerConfig{Repository: repository, Admission: testAdmitter(controller), Handlers: []Handler{
		HandlerFunc{JobKind: "release.finalize", Run: func(context.Context, Job) error { return nil }},
		HandlerFunc{JobKind: "release.finalize", Run: func(context.Context, Job) error { return nil }},
	}})
	if err == nil {
		t.Fatal("duplicate job kinds were accepted")
	}
}

func TestRunnerFailsUnknownJobKindExplicitly(t *testing.T) {
	controller, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	repository := &recordingRunnerRepository{}
	runner, err := NewRunner(RunnerConfig{Repository: repository, Admission: testAdmitter(controller)})
	if err != nil {
		t.Fatal(err)
	}
	runner.executeClaimed(t.Context(), Job{ID: "job-1", Kind: "unknown"})
	if repository.failed != "job-1" || !strings.Contains(string(repository.problem), "unsupported async job kind") {
		t.Fatalf("failed=%q problem=%s", repository.failed, repository.problem)
	}
}

func TestRunnerRenewsLeaseDuringLongHandler(t *testing.T) {
	controller, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	repository := &recordingRunnerRepository{}
	runner, err := NewRunner(RunnerConfig{
		Repository: repository, Admission: testAdmitter(controller), LeaseTimeout: 20 * time.Millisecond,
		Handlers: []Handler{HandlerFunc{JobKind: "slow", Run: func(context.Context, Job) error {
			time.Sleep(55 * time.Millisecond)
			return nil
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner.executeClaimed(t.Context(), Job{ID: "job-1", Kind: "slow"})
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.renewed < 2 || repository.completed != "job-1" {
		t.Fatalf("renewed=%d completed=%q", repository.renewed, repository.completed)
	}
}

func TestRunnerUsesHandlerLeaseTimeout(t *testing.T) {
	controller, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	repository := &leaseRecordingRunnerRepository{candidate: Job{ID: "job-1", Kind: "slow", WorkloadClass: WorkloadClassControl, PrincipalID: "test", EstimatedMemoryBytes: 1}}
	runner, err := NewRunner(RunnerConfig{
		Repository: repository, Admission: testAdmitter(controller), LeaseTimeout: 5 * time.Millisecond,
		Handlers: []Handler{HandlerFunc{JobKind: "slow", ExecutionLeaseTimeout: 50 * time.Millisecond, Run: func(context.Context, Job) error {
			time.Sleep(40 * time.Millisecond)
			return nil
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner.dispatchCandidate(t.Context(), "owner", WorkloadClassControl, repository.candidate)
	if repository.claimTimeout != 50*time.Millisecond {
		t.Fatalf("claim timeout = %s, want 50ms", repository.claimTimeout)
	}
	if repository.renewTimeout != 50*time.Millisecond {
		t.Fatalf("renew timeout = %s, want 50ms", repository.renewTimeout)
	}
}

func TestRunnerLeavesClaimRecoverableWhenWorkerContextStops(t *testing.T) {
	controller, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	repository := &recordingRunnerRepository{}
	started := make(chan struct{})
	runner, err := NewRunner(RunnerConfig{Repository: repository, Admission: testAdmitter(controller), Handlers: []Handler{
		HandlerFunc{JobKind: "blocking", Run: func(ctx context.Context, _ Job) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		runner.executeClaimed(ctx, Job{ID: "job-1", Kind: "blocking"})
	}()
	<-started
	cancel()
	<-finished
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.cancelled != "" || repository.failed != "" || repository.completed != "" {
		t.Fatalf("cancelled=%q failed=%q completed=%q", repository.cancelled, repository.failed, repository.completed)
	}
}

func TestRunnerShutdownDoesNotStartOrWarnAboutCandidatePolling(t *testing.T) {
	controller, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	repository := &countingRunnerRepository{}
	var logs bytes.Buffer
	runner, err := NewRunner(RunnerConfig{
		Repository: repository,
		Admission:  testAdmitter(controller),
		Logger:     slog.New(slog.NewTextHandler(&logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	runner.Run(ctx)

	if calls := repository.candidateCalls.Load(); calls != 0 {
		t.Fatalf("candidate polls after shutdown = %d, want 0", calls)
	}
	if output := logs.String(); output != "" {
		t.Fatalf("shutdown logs = %q, want none", output)
	}
}

func TestRunnerShutdownDoesNotWarnWhenCandidatePollingIsCanceled(t *testing.T) {
	controller, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	repository := &blockingRunnerRepository{started: make(chan struct{}, 2)}
	var logs bytes.Buffer
	runner, err := NewRunner(RunnerConfig{
		Repository: repository,
		Admission:  testAdmitter(controller),
		Logger:     slog.New(slog.NewTextHandler(&logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		runner.Run(ctx)
	}()
	for range 2 {
		select {
		case <-repository.started:
		case <-time.After(time.Second):
			t.Fatal("candidate poll did not start")
		}
	}

	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("runner did not stop after cancellation")
	}

	if output := logs.String(); output != "" {
		t.Fatalf("shutdown logs = %q, want none", output)
	}
}

type runnerTestRepository struct{}

func (*runnerTestRepository) Enqueue(context.Context, EnqueueInput) (Job, error) { return Job{}, nil }
func (*runnerTestRepository) Get(context.Context, string) (Job, error)           { return Job{}, nil }
func (*runnerTestRepository) Candidates(context.Context, string, int) ([]Job, error) {
	return nil, nil
}
func (*runnerTestRepository) ClaimByID(context.Context, string, string, string, time.Duration) (Job, bool, error) {
	return Job{}, false, nil
}
func (*runnerTestRepository) Renew(context.Context, string, Fence, time.Duration) error { return nil }
func (*runnerTestRepository) Complete(context.Context, string, Fence) error             { return nil }
func (*runnerTestRepository) Fail(context.Context, string, Fence, []byte) error         { return nil }
func (*runnerTestRepository) Cancel(context.Context, string) error                      { return nil }
func (*runnerTestRepository) CancelClaimed(context.Context, string, Fence) error        { return nil }
func (*runnerTestRepository) AppendEvent(context.Context, string, string, string, []byte) (Event, error) {
	return Event{}, nil
}
func (*runnerTestRepository) ListEvents(context.Context, string, string, int64, int) ([]Event, error) {
	return nil, nil
}

type countingRunnerRepository struct {
	runnerTestRepository
	candidateCalls atomic.Int64
}

func (r *countingRunnerRepository) Candidates(context.Context, string, int) ([]Job, error) {
	r.candidateCalls.Add(1)
	return nil, nil
}

type blockingRunnerRepository struct {
	runnerTestRepository
	started chan struct{}
}

func (r *blockingRunnerRepository) Candidates(ctx context.Context, _ string, _ int) ([]Job, error) {
	r.started <- struct{}{}
	<-ctx.Done()
	return nil, ctx.Err()
}

type recordingRunnerRepository struct {
	runnerTestRepository
	mu        sync.Mutex
	renewed   int
	completed string
	failed    string
	cancelled string
	problem   []byte
}

type leaseRecordingRunnerRepository struct {
	runnerTestRepository
	candidate    Job
	claimTimeout time.Duration
	renewTimeout time.Duration
}

func (r *leaseRecordingRunnerRepository) ClaimByID(_ context.Context, id, _ string, _ string, timeout time.Duration) (Job, bool, error) {
	r.claimTimeout = timeout
	if id != r.candidate.ID {
		return Job{}, false, nil
	}
	return r.candidate, true, nil
}

func (r *leaseRecordingRunnerRepository) Renew(_ context.Context, _ string, _ Fence, timeout time.Duration) error {
	r.renewTimeout = timeout
	return nil
}

func (r *recordingRunnerRepository) Renew(context.Context, string, Fence, time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.renewed++
	return nil
}

func (r *recordingRunnerRepository) Complete(_ context.Context, id string, _ Fence) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.completed = id
	return nil
}

func (r *recordingRunnerRepository) Fail(_ context.Context, id string, _ Fence, problem []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failed = id
	r.problem = append([]byte(nil), problem...)
	return nil
}

func (r *recordingRunnerRepository) CancelClaimed(_ context.Context, id string, _ Fence) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancelled = id
	return nil
}
