package jobs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testAdmissionLease struct{ ctx context.Context }

func (l testAdmissionLease) Context() context.Context { return l.ctx }

func (l testAdmissionLease) Release() {}

type testAdmission struct{}

func (testAdmission) Acquire(ctx context.Context, _ AdmissionRequest) (AdmissionLease, error) {
	return testAdmissionLease{ctx: ctx}, nil
}

func TestNewRunnerRejectsMissingClassesAndDuplicateHandlerKinds(t *testing.T) {
	repository := &runnerTestRepository{}
	if _, err := NewRunner(RunnerConfig{Repository: repository, Admission: testAdmission{}}); err == nil {
		t.Fatal("missing classes were accepted")
	}
	ownerRunner, err := NewRunner(RunnerConfig{Repository: repository, Admission: testAdmission{}, Classes: []string{"control"}, OwnerID: "owner-a"})
	if err != nil {
		t.Fatal(err)
	}
	if got := ownerRunner.ownerFactory(); got != "owner-a" {
		t.Fatalf("owner id = %q", got)
	}
	if _, err := NewRunner(RunnerConfig{Repository: repository, Admission: testAdmission{}, Classes: []string{"control"}, OwnerID: "owner-a", OwnerFactory: func() string { return "owner-b" }}); err == nil {
		t.Fatal("owner id and owner factory were accepted together")
	}
	defaultRunner, err := NewRunner(RunnerConfig{Repository: repository, Admission: testAdmission{}, Classes: []string{"control"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := defaultRunner.candidateLimit; got != DefaultCandidateLimit {
		t.Fatalf("default candidate limit = %d, want %d", got, DefaultCandidateLimit)
	}
	_, err = NewRunner(RunnerConfig{Repository: repository, Admission: testAdmission{}, Classes: []string{"control"}, Handlers: []Handler{
		HandlerFunc{JobKind: "release.finalize", Run: func(context.Context, Job) error { return nil }},
		HandlerFunc{JobKind: "release.finalize", Run: func(context.Context, Job) error { return nil }},
	}})
	if err == nil {
		t.Fatal("duplicate job kinds were accepted")
	}
}

func TestRunnerUsesConfiguredClassesLimitOwnerAndFailureEncoder(t *testing.T) {
	repository := &candidateRunnerRepository{candidates: []Job{{ID: "job-1", Kind: "success", WorkloadClass: "urgent"}}, started: make(chan struct{}, 4)}
	var ownerCalls atomic.Int32
	var encoded atomic.Int32
	runner, err := NewRunner(RunnerConfig{
		Repository: repository, Admission: testAdmission{}, Classes: []string{"urgent", "slow"}, CandidateLimit: 7,
		LeaseTimeout: 20 * time.Millisecond, PollInterval: time.Hour,
		Handlers: []Handler{HandlerFunc{JobKind: "success", Run: func(context.Context, Job) error { return nil }}},
		OwnerFactory: func() string {
			ownerCalls.Add(1)
			return "worker-a"
		},
		FailureEncoder: func(err error) []byte {
			if err == nil || !strings.Contains(err.Error(), "unsupported") {
				t.Errorf("unexpected failure: %v", err)
			}
			encoded.Add(1)
			return []byte(`{"custom":"failure"}`)
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
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
	select {
	case <-repository.started:
	case <-time.After(time.Second):
		t.Fatal("candidate poll did not start")
	}
	cancel()
	<-finished
	if got := repository.limit.Load(); got != 7 {
		t.Fatalf("candidate limit = %d, want 7", got)
	}
	if ownerCalls.Load() != 1 {
		t.Fatalf("owner factory calls = %d, want 1", ownerCalls.Load())
	}
	// Exercise the configured encoder directly as well as through polling.
	runner.executeClaimed(context.Background(), Job{ID: "job-2", Kind: "unknown"})
	if encoded.Load() != 1 || string(repository.problem) != `{"custom":"failure"}` {
		t.Fatalf("encoded=%d problem=%s", encoded.Load(), repository.problem)
	}
}

func TestRunnerFailsUnknownJobKindExplicitly(t *testing.T) {
	repository := &recordingRunnerRepository{}
	runner, err := NewRunner(RunnerConfig{Repository: repository, Admission: testAdmission{}, Classes: []string{"control"}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	runner.executeClaimed(t.Context(), Job{ID: "job-1", Kind: "unknown"})
	if repository.failed != "job-1" || !strings.Contains(string(repository.problem), "unsupported async job kind") {
		t.Fatalf("failed=%q problem=%s", repository.failed, repository.problem)
	}
}

func TestRunnerSkipsUnknownCandidateBeforeAdmissionAndClaim(t *testing.T) {
	repository := &candidateKindRunnerRepository{}
	var admitted atomic.Int32
	var handled atomic.Int32
	runner, err := NewRunner(RunnerConfig{
		Repository: repository,
		Admission: AdmitterFunc(func(ctx context.Context, _ AdmissionRequest) (AdmissionLease, error) {
			admitted.Add(1)
			return testAdmissionLease{ctx: ctx}, nil
		}),
		Classes: []string{"control"},
		Handlers: []Handler{HandlerFunc{JobKind: "known", Run: func(context.Context, Job) error {
			handled.Add(1)
			return nil
		}}},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}

	runner.dispatchCandidate(t.Context(), "owner", "control", Job{ID: "unknown", Kind: "unknown", WorkloadClass: "control"})
	if got := admitted.Load(); got != 0 {
		t.Fatalf("unknown candidate admission calls = %d, want 0", got)
	}
	if got := repository.claimed.Load(); got != 0 {
		t.Fatalf("unknown candidate claim calls = %d, want 0", got)
	}
	if got := repository.failed.Load(); got != 0 {
		t.Fatalf("unknown candidate failure calls = %d, want 0", got)
	}

	runner.dispatchCandidate(t.Context(), "owner", "control", Job{ID: "known", Kind: "known", WorkloadClass: "control"})
	if got := admitted.Load(); got != 1 {
		t.Fatalf("known candidate admission calls = %d, want 1", got)
	}
	if got := repository.claimed.Load(); got != 1 {
		t.Fatalf("known candidate claim calls = %d, want 1", got)
	}
	if got := handled.Load(); got != 1 {
		t.Fatalf("known candidate handler calls = %d, want 1", got)
	}
}

func TestRunnerDurablyRequeuesExplicitRetryableFailure(t *testing.T) {
	repository := &retryRecordingRunnerRepository{}
	runner, err := NewRunner(RunnerConfig{
		Repository: repository, Admission: testAdmission{}, Classes: []string{"control"},
		Handlers: []Handler{HandlerFunc{JobKind: "retryable", Run: func(context.Context, Job) error {
			return Retryable(errors.New("runtime reconciliation unavailable"), 2*time.Second)
		}}},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	runner.executeClaimed(t.Context(), Job{ID: "job-retry", Kind: "retryable", LeaseOwner: "owner", LeaseGeneration: 7})
	if repository.retried != "job-retry" || repository.delay != 2*time.Second || repository.failed != "" {
		t.Fatalf("retried=%q delay=%s failed=%q", repository.retried, repository.delay, repository.failed)
	}
	if !strings.Contains(string(repository.problem), "runtime reconciliation unavailable") {
		t.Fatalf("retry problem = %s", repository.problem)
	}
}

func TestRunnerRenewsLeaseDuringLongHandler(t *testing.T) {
	repository := &recordingRunnerRepository{}
	runner, err := NewRunner(RunnerConfig{
		Repository: repository, Admission: testAdmission{}, Classes: []string{"background"}, LeaseTimeout: 20 * time.Millisecond,
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
	repository := &leaseRecordingRunnerRepository{
		candidate: Job{ID: "job-1", Kind: "slow", WorkloadClass: "control", PrincipalID: "test", EstimatedMemoryBytes: 1},
		renewed:   make(chan struct{}),
	}
	runner, err := NewRunner(RunnerConfig{
		Repository: repository, Admission: testAdmission{}, Classes: []string{"control"}, LeaseTimeout: 5 * time.Millisecond,
		Handlers: []Handler{HandlerFunc{JobKind: "slow", ExecutionLeaseTimeout: 50 * time.Millisecond, Run: func(ctx context.Context, _ Job) error {
			select {
			case <-repository.renewed:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner.dispatchCandidate(t.Context(), "owner", "control", repository.candidate)
	if repository.claimTimeout != 50*time.Millisecond {
		t.Fatalf("claim timeout = %s, want 50ms", repository.claimTimeout)
	}
	if repository.renewTimeout != 50*time.Millisecond {
		t.Fatalf("renew timeout = %s, want 50ms", repository.renewTimeout)
	}
}

func TestRunnerLeavesClaimRecoverableWhenWorkerContextStops(t *testing.T) {
	repository := &recordingRunnerRepository{}
	started := make(chan struct{})
	runner, err := NewRunner(RunnerConfig{Repository: repository, Admission: testAdmission{}, Classes: []string{"background"}, Handlers: []Handler{
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
	repository := &countingRunnerRepository{}
	var logs bytes.Buffer
	runner, err := NewRunner(RunnerConfig{
		Repository: repository, Admission: testAdmission{}, Classes: []string{"control", "background"},
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	runner.Run(ctx)

	if calls := repository.candidateCalls.Load(); calls != 0 {
		t.Fatalf("candidate polls after shutdown = %d", calls)
	}
	if output := logs.String(); output != "" {
		t.Fatalf("shutdown logs = %q", output)
	}
}

func TestRunnerShutdownDoesNotWarnWhenCandidatePollingIsCanceled(t *testing.T) {
	repository := &blockingRunnerRepository{started: make(chan struct{}, 2)}
	var logs bytes.Buffer
	runner, err := NewRunner(RunnerConfig{
		Repository: repository, Admission: testAdmission{}, Classes: []string{"control", "background"},
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
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
		t.Fatalf("shutdown logs = %q", output)
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

type candidateRunnerRepository struct {
	runnerTestRepository
	candidates []Job
	started    chan struct{}
	limit      atomic.Int64
	problem    []byte
}

type candidateKindRunnerRepository struct {
	runnerTestRepository
	claimed atomic.Int32
	failed  atomic.Int32
}

func (r *candidateKindRunnerRepository) ClaimByID(_ context.Context, id, _ string, owner string, _ time.Duration) (Job, bool, error) {
	r.claimed.Add(1)
	return Job{ID: id, Kind: "known", LeaseOwner: owner}, true, nil
}

func (r *candidateKindRunnerRepository) Fail(_ context.Context, _ string, _ Fence, _ []byte) error {
	r.failed.Add(1)
	return nil
}

func (r *candidateRunnerRepository) Candidates(_ context.Context, class string, limit int) ([]Job, error) {
	r.limit.Store(int64(limit))
	r.started <- struct{}{}
	if class != "urgent" {
		return nil, nil
	}
	return append([]Job(nil), r.candidates...), nil
}

func (r *candidateRunnerRepository) ClaimByID(_ context.Context, id, _, owner string, _ time.Duration) (Job, bool, error) {
	return Job{ID: id, Kind: "success", LeaseOwner: owner}, true, nil
}

func (r *candidateRunnerRepository) Fail(_ context.Context, _ string, _ Fence, problem []byte) error {
	r.problem = append([]byte(nil), problem...)
	return nil
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

type retryRecordingRunnerRepository struct {
	recordingRunnerRepository
	retried string
	delay   time.Duration
}

func (r *retryRecordingRunnerRepository) Retry(_ context.Context, id string, _ Fence, delay time.Duration, problem []byte) error {
	r.retried = id
	r.delay = delay
	r.problem = append([]byte(nil), problem...)
	return nil
}

type leaseRecordingRunnerRepository struct {
	runnerTestRepository
	candidate    Job
	claimTimeout time.Duration
	renewTimeout time.Duration
	renewed      chan struct{}
	renewOnce    sync.Once
}

func (r *leaseRecordingRunnerRepository) ClaimByID(_ context.Context, id, _ string, _ string, timeout time.Duration) (Job, bool, error) {
	r.claimTimeout = timeout
	if id != r.candidate.ID {
		return Job{}, false, nil
	}
	return r.candidate, true, nil
}

func (r *leaseRecordingRunnerRepository) Renew(_ context.Context, _ string, _ Fence, timeout time.Duration) error {
	r.renewOnce.Do(func() {
		r.renewTimeout = timeout
		close(r.renewed)
	})
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
