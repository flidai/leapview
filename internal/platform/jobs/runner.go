package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const (
	WorkloadClassBackground = "background"
	WorkloadClassControl    = "control"
)

type AdmissionRequest struct {
	Class                string
	PrincipalID          string
	GroupIDs             []string
	EstimatedMemoryBytes int64
	Operation            string
}

type AdmissionLease interface {
	Context() context.Context
	Release()
}

type Admitter interface {
	Acquire(context.Context, AdmissionRequest) (AdmissionLease, error)
}

type AdmitterFunc func(context.Context, AdmissionRequest) (AdmissionLease, error)

func (f AdmitterFunc) Acquire(ctx context.Context, request AdmissionRequest) (AdmissionLease, error) {
	return f(ctx, request)
}

type Handler interface {
	Kind() string
	Handle(context.Context, Job) error
}

type HandlerFunc struct {
	JobKind               string
	Run                   func(context.Context, Job) error
	ExecutionLeaseTimeout time.Duration
}

func (h HandlerFunc) Kind() string { return h.JobKind }

func (h HandlerFunc) Handle(ctx context.Context, job Job) error {
	if h.Run == nil {
		return fmt.Errorf("job handler %q is not configured", h.JobKind)
	}
	return h.Run(ctx, job)
}

// LeaseTimeout lets a capability declare a longer lease for handlers whose
// durable work can legitimately outlive the generic background-job timeout.
// A non-positive value falls back to RunnerConfig.LeaseTimeout.
func (h HandlerFunc) LeaseTimeout() time.Duration { return h.ExecutionLeaseTimeout }

type RunnerConfig struct {
	Repository   Repository
	Admission    Admitter
	Handlers     []Handler
	LeaseTimeout time.Duration
	PollInterval time.Duration
	Logger       *slog.Logger
}

// Runner owns generic polling, admission, claims, lease renewal, and terminal
// persistence. Capability handlers own payload decoding and business behavior.
type Runner struct {
	repository   Repository
	admission    Admitter
	handlers     map[string]Handler
	leaseTimeout time.Duration
	pollInterval time.Duration
	logger       *slog.Logger
}

func NewRunner(config RunnerConfig) (*Runner, error) {
	if config.Repository == nil || config.Admission == nil {
		return nil, errors.New("job repository and workload controller are required")
	}
	leaseTimeout := config.LeaseTimeout
	if leaseTimeout <= 0 {
		leaseTimeout = 2 * time.Minute
	}
	pollInterval := config.PollInterval
	if pollInterval <= 0 {
		pollInterval = 250 * time.Millisecond
	}
	handlers := make(map[string]Handler, len(config.Handlers))
	for _, handler := range config.Handlers {
		if handler == nil || handler.Kind() == "" {
			return nil, errors.New("job handler kind is required")
		}
		if _, exists := handlers[handler.Kind()]; exists {
			return nil, fmt.Errorf("duplicate job handler %q", handler.Kind())
		}
		handlers[handler.Kind()] = handler
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{repository: config.Repository, admission: config.Admission, handlers: handlers, leaseTimeout: leaseTimeout, pollInterval: pollInterval, logger: logger}, nil
}

func (r *Runner) Run(ctx context.Context) {
	owner := fmt.Sprintf("leapview-jobs-%d", time.Now().UnixNano())
	var pumps sync.WaitGroup
	for _, class := range []string{WorkloadClassControl, WorkloadClassBackground} {
		class := class
		pumps.Add(1)
		go func() {
			defer pumps.Done()
			r.runPump(ctx, owner, class)
		}()
	}
	pumps.Wait()
}

func (r *Runner) runPump(ctx context.Context, owner, class string) {
	poll := time.NewTicker(r.pollInterval)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		candidates, err := r.repository.Candidates(ctx, string(class), 16)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			r.logger.WarnContext(ctx, "list async job candidates failed", "class", class, "error", err)
		} else {
			var batch sync.WaitGroup
			for _, candidate := range candidates {
				candidate := candidate
				batch.Add(1)
				go func() {
					defer batch.Done()
					r.dispatchCandidate(ctx, owner, class, candidate)
				}()
			}
			batch.Wait()
		}
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
		}
	}
}

func (r *Runner) dispatchCandidate(ctx context.Context, owner, class string, candidate Job) {
	lease, err := r.admission.Acquire(ctx, AdmissionRequest{
		Class: candidate.WorkloadClass, PrincipalID: candidate.PrincipalID, GroupIDs: append([]string(nil), candidate.GroupIDs...),
		EstimatedMemoryBytes: candidate.EstimatedMemoryBytes, Operation: candidate.Kind,
	})
	if err != nil {
		return
	}
	defer lease.Release()
	leaseTimeout := r.leaseTimeoutFor(candidate.Kind)
	job, ok, err := r.repository.ClaimByID(lease.Context(), candidate.ID, string(class), owner, leaseTimeout)
	if err != nil || !ok {
		return
	}
	r.executeClaimedWithTimeout(lease.Context(), job, leaseTimeout)
}

func (r *Runner) executeClaimed(parent context.Context, job Job) {
	r.executeClaimedWithTimeout(parent, job, r.leaseTimeoutFor(job.Kind))
}

func (r *Runner) leaseTimeoutFor(kind string) time.Duration {
	if handler, ok := r.handlers[kind]; ok {
		if provider, ok := handler.(interface{ LeaseTimeout() time.Duration }); ok {
			if timeout := provider.LeaseTimeout(); timeout > 0 {
				return timeout
			}
		}
	}
	return r.leaseTimeout
}

func (r *Runner) executeClaimedWithTimeout(parent context.Context, job Job, leaseTimeout time.Duration) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	done := make(chan struct{})
	go func() {
		interval := leaseTimeout / 2
		if interval <= 0 {
			interval = time.Millisecond
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := r.repository.Renew(context.WithoutCancel(ctx), job.ID, job.Fence(), leaseTimeout); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	handler, ok := r.handlers[job.Kind]
	var err error
	if !ok {
		err = fmt.Errorf("unsupported async job kind %q", job.Kind)
	} else {
		err = handler.Handle(ctx, job)
	}
	close(done)
	if ctx.Err() != nil {
		return
	}
	if err == nil {
		_ = r.repository.Complete(context.WithoutCancel(ctx), job.ID, job.Fence())
		return
	}
	problem, _ := json.Marshal(map[string]any{"code": "ASYNC_JOB_FAILED", "detail": err.Error()})
	_ = r.repository.Fail(context.WithoutCancel(ctx), job.ID, job.Fence(), problem)
	r.logger.ErrorContext(ctx, "async job failed", "kind", job.Kind, "resource", job.ResourceID, "error", err)
}
