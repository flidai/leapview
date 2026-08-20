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
	// DefaultCandidateLimit bounds each class poll without imposing a product
	// specific queue policy.
	DefaultCandidateLimit = 16
	defaultLeaseTimeout   = 2 * time.Minute
	defaultPollInterval   = 250 * time.Millisecond
)

// AdmissionRequest describes the resources and actor identity needed to run
// one job.
type AdmissionRequest struct {
	Class                string
	PrincipalID          string
	GroupIDs             []string
	EstimatedMemoryBytes int64
	Operation            string
}

// AdmissionLease remains valid for one admitted handler invocation.
type AdmissionLease interface {
	Context() context.Context
	Release()
}

// Admitter gates work before a durable claim is attempted.
type Admitter interface {
	Acquire(context.Context, AdmissionRequest) (AdmissionLease, error)
}

// AdmitterFunc adapts a function to Admitter.
type AdmitterFunc func(context.Context, AdmissionRequest) (AdmissionLease, error)

func (f AdmitterFunc) Acquire(ctx context.Context, request AdmissionRequest) (AdmissionLease, error) {
	return f(ctx, request)
}

// Handler owns payload decoding and business behavior for one job kind.
type Handler interface {
	Kind() string
	Handle(context.Context, Job) error
}

// HandlerFunc adapts a function to Handler.
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

// FailurePayloadEncoder converts a handler error into the durable failure
// payload consumed by the Repository. Applications can encode their own
// problem contract; the default is a small generic JSON detail object.
type FailurePayloadEncoder func(error) []byte

// FailureEncoder is a concise alias for FailurePayloadEncoder.
type FailureEncoder = FailurePayloadEncoder

// OwnerFactory returns the lease-owner identity for one Runner invocation.
type OwnerFactory func() string

// RunnerConfig controls polling, leasing, admission, worker identity, and
// terminal failure policy. Classes must contain the workload classes that
// this runner services.
type RunnerConfig struct {
	Repository     Repository
	Admission      Admitter
	Handlers       []Handler
	Classes        []string
	LeaseTimeout   time.Duration
	PollInterval   time.Duration
	CandidateLimit int
	OwnerID        string
	OwnerFactory   OwnerFactory
	FailureEncoder FailurePayloadEncoder
	Logger         *slog.Logger
}

// Runner owns generic polling, admission, claims, lease renewal, and
// terminal persistence. Capability handlers own payload decoding and business
// behavior.
type Runner struct {
	repository     Repository
	admission      Admitter
	handlers       map[string]Handler
	classes        []string
	leaseTimeout   time.Duration
	pollInterval   time.Duration
	candidateLimit int
	ownerFactory   func() string
	failureEncoder FailurePayloadEncoder
	logger         *slog.Logger
}

// NewRunner validates configuration and constructs a durable worker runner.
func NewRunner(config RunnerConfig) (*Runner, error) {
	if config.Repository == nil || config.Admission == nil {
		return nil, errors.New("job repository and workload admission are required")
	}
	if len(config.Classes) == 0 {
		return nil, errors.New("at least one workload class is required")
	}
	classes := make([]string, 0, len(config.Classes))
	seenClasses := make(map[string]struct{}, len(config.Classes))
	for _, class := range config.Classes {
		if class == "" {
			return nil, errors.New("workload class is required")
		}
		if _, exists := seenClasses[class]; exists {
			return nil, fmt.Errorf("duplicate workload class %q", class)
		}
		seenClasses[class] = struct{}{}
		classes = append(classes, class)
	}
	if config.OwnerID != "" && config.OwnerFactory != nil {
		return nil, errors.New("configure either owner id or owner factory, not both")
	}
	ownerFactory := config.OwnerFactory
	if ownerFactory == nil {
		if config.OwnerID != "" {
			ownerID := config.OwnerID
			ownerFactory = func() string { return ownerID }
		} else {
			ownerFactory = func() string { return fmt.Sprintf("jobs-%d", time.Now().UnixNano()) }
		}
	}
	leaseTimeout := config.LeaseTimeout
	if leaseTimeout <= 0 {
		leaseTimeout = defaultLeaseTimeout
	}
	pollInterval := config.PollInterval
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	candidateLimit := config.CandidateLimit
	if candidateLimit <= 0 {
		candidateLimit = DefaultCandidateLimit
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
	failureEncoder := config.FailureEncoder
	if failureEncoder == nil {
		failureEncoder = defaultFailurePayload
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{
		repository: config.Repository, admission: config.Admission,
		handlers: handlers, classes: classes, leaseTimeout: leaseTimeout,
		pollInterval: pollInterval, candidateLimit: candidateLimit,
		ownerFactory: ownerFactory, failureEncoder: failureEncoder, logger: logger,
	}, nil
}

// Run polls each configured class until ctx is cancelled. One owner identity
// is shared by all class pumps for this runner invocation.
func (r *Runner) Run(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	owner := r.ownerFactory()
	if owner == "" {
		r.logger.ErrorContext(ctx, "job runner owner is empty")
		return
	}
	var pumps sync.WaitGroup
	for _, class := range r.classes {
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
		candidates, err := r.repository.Candidates(ctx, class, r.candidateLimit)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			r.logger.WarnContext(ctx, "list job candidates failed", "class", class, "error", err)
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
		Class: candidate.WorkloadClass, PrincipalID: candidate.PrincipalID,
		GroupIDs:             append([]string(nil), candidate.GroupIDs...),
		EstimatedMemoryBytes: candidate.EstimatedMemoryBytes, Operation: candidate.Kind,
	})
	if err != nil || lease == nil {
		return
	}
	defer lease.Release()
	leaseContext := lease.Context()
	if leaseContext == nil {
		leaseContext = ctx
	}
	leaseTimeout := r.leaseTimeoutFor(candidate.Kind)
	job, ok, err := r.repository.ClaimByID(leaseContext, candidate.ID, class, owner, leaseTimeout)
	if err != nil || !ok {
		return
	}
	r.executeClaimedWithTimeout(leaseContext, job, leaseTimeout)
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
	if parent == nil {
		parent = context.Background()
	}
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
	problem := r.failureEncoder(err)
	_ = r.repository.Fail(context.WithoutCancel(ctx), job.ID, job.Fence(), problem)
	r.logger.ErrorContext(ctx, "job failed", "kind", job.Kind, "resource", job.ResourceID, "error", err)
}

func defaultFailurePayload(err error) []byte {
	detail := "job failed"
	if err != nil {
		detail = err.Error()
	}
	payload, marshalErr := json.Marshal(map[string]string{"detail": detail})
	if marshalErr != nil {
		return []byte(`{"detail":"job failed"}`)
	}
	return payload
}
