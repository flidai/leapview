package http

import (
	"context"
	"errors"
	"fmt"
	"io"
	stdhttp "net/http"
	"strings"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	analyticsresource "github.com/flidai/leapview/internal/analytics/resource"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

const (
	dashboardNativeArrowStreamMaximumLifetime = 30 * time.Second
	dashboardNativeArrowStreamIdleTimeout     = 5 * time.Second
	dashboardNativeArrowStreamCleanupBound    = 2 * time.Second
	dashboardNativeArrowStreamCleanupP95      = time.Second
)

var (
	errDashboardNativeArrowStreamHardLimit     = errors.New("native dashboard Arrow stream lifetime exceeded")
	errDashboardNativeArrowStreamIdle          = errors.New("native dashboard Arrow stream made no write progress")
	errDashboardNativeArrowStreamIPCIncomplete = errors.New("native dashboard Arrow IPC stream did not close successfully")
)

type dashboardNativeArrowStreamPolicy struct {
	AnalyticalConnections int
	InstanceLimit         int
	PrincipalLimit        int
	ProjectLimit          int
	MaximumLifetime       time.Duration
	IdleWriteTimeout      time.Duration
	CleanupBound          time.Duration
	CleanupP95Target      time.Duration
}

func defaultDashboardNativeArrowStreamPolicy(analyticalConnections int) dashboardNativeArrowStreamPolicy {
	instanceLimit := 0
	if analyticalConnections >= 2 {
		instanceLimit = 1
	}
	return dashboardNativeArrowStreamPolicy{
		AnalyticalConnections: analyticalConnections,
		InstanceLimit:         instanceLimit,
		PrincipalLimit:        instanceLimit,
		ProjectLimit:          instanceLimit,
		MaximumLifetime:       dashboardNativeArrowStreamMaximumLifetime,
		IdleWriteTimeout:      dashboardNativeArrowStreamIdleTimeout,
		CleanupBound:          dashboardNativeArrowStreamCleanupBound,
		CleanupP95Target:      dashboardNativeArrowStreamCleanupP95,
	}
}

func (p dashboardNativeArrowStreamPolicy) validate() error {
	if p.AnalyticalConnections < 1 {
		return fmt.Errorf("native dashboard Arrow streaming requires a positive analytical connection count")
	}
	if p.AnalyticalConnections == 1 {
		if p.InstanceLimit != 0 || p.PrincipalLimit != 0 || p.ProjectLimit != 0 {
			return fmt.Errorf("native dashboard Arrow streaming must be disabled for a one-connection pool")
		}
	} else {
		maximum := dashboardNativeArrowStreamPoolLimit(p.AnalyticalConnections)
		if p.InstanceLimit < 1 || p.InstanceLimit > maximum {
			return fmt.Errorf("native dashboard Arrow instance limit must be between 1 and %d", maximum)
		}
		if p.PrincipalLimit < 1 || p.PrincipalLimit > p.InstanceLimit {
			return fmt.Errorf("native dashboard Arrow principal limit must be between 1 and the instance limit")
		}
		if p.ProjectLimit < 1 || p.ProjectLimit > p.InstanceLimit {
			return fmt.Errorf("native dashboard Arrow project limit must be between 1 and the instance limit")
		}
	}
	if p.MaximumLifetime <= 0 || p.IdleWriteTimeout <= 0 || p.CleanupBound <= 0 || p.CleanupP95Target <= 0 {
		return fmt.Errorf("native dashboard Arrow lifecycle durations must be positive")
	}
	if p.CleanupP95Target > p.CleanupBound {
		return fmt.Errorf("native dashboard Arrow cleanup p95 target exceeds the cleanup bound")
	}
	return nil
}

func dashboardNativeArrowStreamPoolLimit(analyticalConnections int) int {
	if analyticalConnections < 2 {
		return 0
	}
	quarter := analyticalConnections / 4
	if quarter < 1 {
		quarter = 1
	}
	return min(analyticalConnections-1, quarter)
}

type dashboardNativeArrowStreamRejectionReason string

const (
	dashboardNativeArrowStreamDisabled        dashboardNativeArrowStreamRejectionReason = "native_stream_disabled"
	dashboardNativeArrowStreamInstanceLimit   dashboardNativeArrowStreamRejectionReason = "native_stream_instance_limit"
	dashboardNativeArrowStreamPrincipalLimit  dashboardNativeArrowStreamRejectionReason = "native_stream_principal_limit"
	dashboardNativeArrowStreamProjectLimit    dashboardNativeArrowStreamRejectionReason = "native_stream_project_limit"
	dashboardNativeArrowStreamNestedAdmission dashboardNativeArrowStreamRejectionReason = "native_stream_nested_admission"
	dashboardNativeArrowStreamInvalidIdentity dashboardNativeArrowStreamRejectionReason = "native_stream_invalid_identity"
)

type dashboardNativeArrowStreamRejection struct {
	Reason      dashboardNativeArrowStreamRejectionReason
	PrincipalID string
	ProjectID   string
}

func (e *dashboardNativeArrowStreamRejection) Error() string {
	if e == nil {
		return "native dashboard Arrow stream rejected"
	}
	return fmt.Sprintf("native dashboard Arrow stream rejected: %s", e.Reason)
}

func (e *dashboardNativeArrowStreamRejection) WorkloadRejectionReason() string {
	if e == nil {
		return string(dashboardNativeArrowStreamDisabled)
	}
	return string(e.Reason)
}

type dashboardNativeArrowStreamCapacity struct {
	policy     dashboardNativeArrowStreamPolicy
	mu         sync.Mutex
	active     int
	principals map[string]int
	projects   map[string]int
}

func newDashboardNativeArrowStreamCapacity(policy dashboardNativeArrowStreamPolicy) (*dashboardNativeArrowStreamCapacity, error) {
	if err := policy.validate(); err != nil {
		return nil, err
	}
	return &dashboardNativeArrowStreamCapacity{
		policy: policy, principals: map[string]int{}, projects: map[string]int{},
	}, nil
}

func (c *dashboardNativeArrowStreamCapacity) acquire(ctx context.Context, principalID, projectID string, alreadyAdmitted bool) (*dashboardNativeArrowStreamSlot, error) {
	principalID = strings.TrimSpace(principalID)
	projectID = strings.TrimSpace(projectID)
	if principalID == "" || projectID == "" {
		return nil, &dashboardNativeArrowStreamRejection{Reason: dashboardNativeArrowStreamInvalidIdentity, PrincipalID: principalID, ProjectID: projectID}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if alreadyAdmitted {
		return nil, &dashboardNativeArrowStreamRejection{Reason: dashboardNativeArrowStreamNestedAdmission, PrincipalID: principalID, ProjectID: projectID}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.policy.InstanceLimit == 0 {
		return nil, &dashboardNativeArrowStreamRejection{Reason: dashboardNativeArrowStreamDisabled, PrincipalID: principalID, ProjectID: projectID}
	}
	if c.active >= c.policy.InstanceLimit {
		return nil, &dashboardNativeArrowStreamRejection{Reason: dashboardNativeArrowStreamInstanceLimit, PrincipalID: principalID, ProjectID: projectID}
	}
	if c.principals[principalID] >= c.policy.PrincipalLimit {
		return nil, &dashboardNativeArrowStreamRejection{Reason: dashboardNativeArrowStreamPrincipalLimit, PrincipalID: principalID, ProjectID: projectID}
	}
	if c.projects[projectID] >= c.policy.ProjectLimit {
		return nil, &dashboardNativeArrowStreamRejection{Reason: dashboardNativeArrowStreamProjectLimit, PrincipalID: principalID, ProjectID: projectID}
	}
	c.active++
	c.principals[principalID]++
	c.projects[projectID]++
	return &dashboardNativeArrowStreamSlot{capacity: c, principalID: principalID, projectID: projectID}, nil
}

type dashboardNativeArrowStreamSlot struct {
	capacity    *dashboardNativeArrowStreamCapacity
	principalID string
	projectID   string
	once        sync.Once
}

func (s *dashboardNativeArrowStreamSlot) Release() {
	if s == nil || s.capacity == nil {
		return
	}
	s.once.Do(func() {
		s.capacity.mu.Lock()
		defer s.capacity.mu.Unlock()
		s.capacity.active--
		decrementDashboardNativeArrowStreamActor(s.capacity.principals, s.principalID)
		decrementDashboardNativeArrowStreamActor(s.capacity.projects, s.projectID)
	})
}

func decrementDashboardNativeArrowStreamActor(counts map[string]int, identity string) {
	if counts[identity] <= 1 {
		delete(counts, identity)
		return
	}
	counts[identity]--
}

type dashboardNativeArrowStreamObservation struct {
	RowsEmitted                int64
	ProbeRows                  int64
	IPCBytes                   int64
	ConnectionHold             time.Duration
	AdmissionOccupancy         time.Duration
	TimeoutReason              string
	CancellationCleanupLatency time.Duration
	CleanupDuration            time.Duration
	CleanupBoundExceeded       bool
	PostCommitAbort            bool
	Success                    bool
}

type dashboardNativeArrowStreamObserver interface {
	ObserveDashboardNativeArrowStream(dashboardNativeArrowStreamObservation)
}

type dashboardNativeArrowStreamTerminalEvent struct {
	PrincipalID     string
	ProjectID       string
	Success         bool
	Committed       bool
	CursorPublished bool
	Err             error
}

type dashboardNativeArrowStreamTerminalRecorder interface {
	RecordDashboardNativeArrowStreamTerminal(dashboardNativeArrowStreamTerminalEvent)
}

type dashboardNativeArrowStreamDependencies struct {
	Capacity        *dashboardNativeArrowStreamCapacity
	Admitter        dashboardNativeArrowStreamAdmitter
	AlreadyAdmitted func(context.Context) bool
	Serving         projectruntime.Provider
	Database        analyticsresource.Provider
	Terminal        dashboardNativeArrowStreamTerminalRecorder
	Observer        dashboardNativeArrowStreamObserver
}

type dashboardNativeArrowStreamAdmission interface {
	Context() context.Context
	QueueWait() time.Duration
	Release()
}

type dashboardNativeArrowStreamAdmitter interface {
	AcquireDashboardNativeArrowStream(context.Context) (dashboardNativeArrowStreamAdmission, error)
}

type dashboardNativeArrowStreamRequest struct {
	PrincipalID   string
	ProjectID     string
	Writer        stdhttp.ResponseWriter
	PublishCursor func(context.Context, string) error
}

type dashboardNativeArrowStreamOperation func(context.Context, *dashboardNativeArrowStream) (string, error)

func runDashboardNativeArrowStream(
	ctx context.Context,
	dependencies dashboardNativeArrowStreamDependencies,
	request dashboardNativeArrowStreamRequest,
	operation dashboardNativeArrowStreamOperation,
) (observation dashboardNativeArrowStreamObservation, resultErr error) {
	if dependencies.Capacity == nil || dependencies.Admitter == nil || dependencies.AlreadyAdmitted == nil || dependencies.Serving == nil || dependencies.Database == nil || dependencies.Terminal == nil || dependencies.Observer == nil {
		return observation, errors.New("native dashboard Arrow stream lifecycle dependencies are incomplete")
	}
	if request.Writer == nil || request.PublishCursor == nil || operation == nil {
		return observation, errors.New("native dashboard Arrow stream transport is incomplete")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	policy := dependencies.Capacity.policy
	alreadyAdmitted := dependencies.AlreadyAdmitted(ctx)
	slot, err := dependencies.Capacity.acquire(ctx, request.PrincipalID, request.ProjectID, alreadyAdmitted)
	if err != nil {
		return observation, err
	}

	var admission dashboardNativeArrowStreamAdmission
	var serving projectruntime.Lease
	var database analyticsresource.Lease
	var stream *dashboardNativeArrowStream
	var admissionStarted, connectionStarted time.Time
	transportSucceeded := false
	cleanupStarted := time.Time{}
	defer func() {
		panicValue := recover()
		if panicValue != nil {
			resultErr = errors.New("native dashboard Arrow stream operation panicked")
		}
		if cleanupStarted.IsZero() {
			cleanupStarted = time.Now()
		}
		if stream != nil {
			stream.stop()
			if closeErr := stream.closeReader(); resultErr == nil && closeErr != nil {
				resultErr = closeErr
			}
		}
		if admission != nil {
			committed := stream != nil && stream.writer.committedResponse()
			dependencies.Terminal.RecordDashboardNativeArrowStreamTerminal(dashboardNativeArrowStreamTerminalEvent{
				PrincipalID: request.PrincipalID,
				ProjectID:   request.ProjectID,
				Success:     transportSucceeded, Committed: committed,
				CursorPublished: transportSucceeded,
				Err:             resultErr,
			})
		}
		if database != nil {
			database.Release()
			observation.ConnectionHold = time.Since(connectionStarted)
		}
		if admission != nil {
			admission.Release()
			observation.AdmissionOccupancy = time.Since(admissionStarted)
		}
		slot.Release()
		if serving != nil {
			serving.Release()
		}
		observation.CleanupDuration = time.Since(cleanupStarted)
		observation.CleanupBoundExceeded = observation.CleanupDuration > policy.CleanupBound
		if resultErr != nil {
			observation.TimeoutReason = dashboardNativeArrowStreamTimeoutReason(resultErr)
			if observation.TimeoutReason != "" || errors.Is(resultErr, context.Canceled) {
				observation.CancellationCleanupLatency = observation.CleanupDuration
			}
		}
		if stream != nil {
			observation.RowsEmitted = stream.rowsEmitted
			observation.ProbeRows = stream.probeRows
			observation.IPCBytes = stream.writer.bytesWritten()
			observation.PostCommitAbort = !transportSucceeded && stream.writer.committedResponse()
		}
		observation.Success = transportSucceeded
		if dependencies.Observer != nil {
			dependencies.Observer.ObserveDashboardNativeArrowStream(observation)
		}
		if panicValue != nil {
			panic(panicValue)
		}
	}()

	admission, err = dependencies.Admitter.AcquireDashboardNativeArrowStream(ctx)
	if err != nil {
		return observation, err
	}
	admissionStarted = time.Now()
	if admission.Context() == nil {
		return observation, errors.New("native dashboard Arrow admission returned no execution context")
	}
	maximumDeadline := admissionStarted.Add(policy.MaximumLifetime)
	deadline := maximumDeadline
	deadlineCause := errDashboardNativeArrowStreamHardLimit
	if earlier, ok := admission.Context().Deadline(); ok && earlier.Before(deadline) {
		deadline = earlier
		deadlineCause = context.DeadlineExceeded
	}
	deadlineContext, deadlineCancel := context.WithDeadlineCause(admission.Context(), deadline, deadlineCause)
	defer deadlineCancel()
	streamContext, streamCancel := context.WithCancelCause(deadlineContext)
	defer streamCancel(nil)

	serving, err = dependencies.Serving.Acquire(streamContext)
	if err != nil {
		return observation, err
	}
	database, err = dependencies.Database.Acquire(streamContext)
	if err != nil {
		return observation, err
	}
	connectionStarted = time.Now()
	budget, _ := dataquery.ResultBudgetFromContext(streamContext)
	stream = newDashboardNativeArrowStream(streamContext, streamCancel, request.Writer, budget, policy.IdleWriteTimeout, deadline, deadlineCause)

	cursor, err := operation(streamContext, stream)
	// IPC delivery is complete once the operation returns. Stop the no-progress
	// timer before reader close and cursor publication; the absolute lifecycle
	// deadline remains active through the cursor decision.
	stream.stop()
	cleanupStarted = time.Now()
	if err == nil {
		if cause := context.Cause(streamContext); cause != nil {
			err = cause
		} else if !stream.ipcClosedSuccessfully() {
			err = errDashboardNativeArrowStreamIPCIncomplete
		}
	}
	if closeErr := stream.closeReader(); closeErr != nil {
		err = errors.Join(err, closeErr)
	}
	if err == nil {
		if cause := context.Cause(streamContext); cause != nil {
			err = cause
		} else {
			err = request.PublishCursor(streamContext, cursor)
			if err == nil {
				transportSucceeded = true
				deadlineCancel()
			}
		}
	}
	return observation, err
}

type dashboardNativeArrowStream struct {
	ctx         context.Context
	cancel      context.CancelCauseFunc
	writer      *dashboardNativeArrowStreamWriter
	budget      *dataquery.ResultBudget
	mu          sync.Mutex
	reader      io.Closer
	ipcClosed   bool
	rowsEmitted int64
	probeRows   int64
}

func newDashboardNativeArrowStream(
	ctx context.Context,
	cancel context.CancelCauseFunc,
	writer stdhttp.ResponseWriter,
	budget *dataquery.ResultBudget,
	idle time.Duration,
	deadline time.Time,
	deadlineCause error,
) *dashboardNativeArrowStream {
	stream := &dashboardNativeArrowStream{ctx: ctx, cancel: cancel, budget: budget}
	stream.writer = &dashboardNativeArrowStreamWriter{
		ResponseWriter: writer, ctx: ctx, cancel: cancel, idle: idle,
		absoluteDeadline: deadline, deadlineCause: deadlineCause, budget: budget,
	}
	return stream
}

func (s *dashboardNativeArrowStream) Writer() io.Writer {
	if s == nil {
		return io.Discard
	}
	return s.writer
}

func (s *dashboardNativeArrowStream) RegisterReader(reader io.Closer) error {
	if s == nil || reader == nil {
		return errors.New("native dashboard Arrow reader is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reader != nil {
		return errors.New("native dashboard Arrow reader was already registered")
	}
	s.reader = reader
	return nil
}

func (s *dashboardNativeArrowStream) ChargeSchema(schemaBytes, metadataBytes int64) error {
	if schemaBytes < 0 || metadataBytes < 0 {
		return errors.New("native dashboard Arrow schema budget charge is invalid")
	}
	if s == nil || s.budget == nil {
		return nil
	}
	return s.budget.ConsumeSize(0, schemaBytes+metadataBytes)
}

func (s *dashboardNativeArrowStream) ChargeBatch(emittedRows, probeRows int, retainedBytes int64) error {
	if emittedRows < 0 || probeRows < 0 || probeRows > 1 || retainedBytes < 0 {
		return errors.New("native dashboard Arrow batch budget charge is invalid")
	}
	if s != nil && s.budget != nil {
		if err := s.budget.ConsumeSize(emittedRows+probeRows, retainedBytes); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.rowsEmitted += int64(emittedRows)
	s.probeRows += int64(probeRows)
	s.mu.Unlock()
	return nil
}

func (s *dashboardNativeArrowStream) MarkIPCClosed() error {
	if s == nil {
		return errDashboardNativeArrowStreamIPCIncomplete
	}
	if cause := context.Cause(s.ctx); cause != nil {
		return cause
	}
	s.mu.Lock()
	s.ipcClosed = true
	s.mu.Unlock()
	return nil
}

func (s *dashboardNativeArrowStream) ipcClosedSuccessfully() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ipcClosed
}

func (s *dashboardNativeArrowStream) closeReader() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	reader := s.reader
	s.reader = nil
	s.mu.Unlock()
	if reader == nil {
		return nil
	}
	if err := reader.Close(); err != nil {
		return fmt.Errorf("close native dashboard Arrow reader: %w", err)
	}
	return nil
}

func (s *dashboardNativeArrowStream) stop() {
	if s == nil || s.writer == nil {
		return
	}
	s.writer.stop()
}

type dashboardNativeArrowStreamWriter struct {
	stdhttp.ResponseWriter
	ctx              context.Context
	cancel           context.CancelCauseFunc
	idle             time.Duration
	absoluteDeadline time.Time
	deadlineCause    error
	budget           *dataquery.ResultBudget
	mu               sync.Mutex
	idleTimer        *time.Timer
	committed        bool
	bytes            int64
}

func (w *dashboardNativeArrowStreamWriter) Write(payload []byte) (int, error) {
	if w == nil || w.ResponseWriter == nil {
		return 0, errors.New("native dashboard Arrow writer is unavailable")
	}
	if cause := context.Cause(w.ctx); cause != nil {
		return 0, cause
	}
	deadline, deadlineCause := w.nextDeadline()
	if err := stdhttp.NewResponseController(w.ResponseWriter).SetWriteDeadline(deadline); err != nil {
		err = fmt.Errorf("set native dashboard Arrow write deadline: %w", err)
		w.cancel(err)
		return 0, err
	}
	written, err := w.ResponseWriter.Write(payload)
	w.mu.Lock()
	if written > 0 {
		w.committed = true
		w.bytes += int64(written)
	}
	w.mu.Unlock()
	if written > 0 && w.budget != nil {
		if budgetErr := w.budget.ConsumeSize(0, int64(written)); budgetErr != nil {
			w.cancel(budgetErr)
			return written, budgetErr
		}
	}
	if err == nil && written != len(payload) {
		err = io.ErrShortWrite
	}
	if err != nil {
		if timeout, ok := err.(interface{ Timeout() bool }); ok && timeout.Timeout() {
			w.cancel(deadlineCause)
			return written, deadlineCause
		}
		w.cancel(err)
		return written, err
	}
	w.refreshIdleTimer()
	return written, nil
}

func (w *dashboardNativeArrowStreamWriter) nextDeadline() (time.Time, error) {
	idleDeadline := time.Now().Add(w.idle)
	if !w.absoluteDeadline.IsZero() && !w.absoluteDeadline.After(idleDeadline) {
		return w.absoluteDeadline, w.deadlineCause
	}
	return idleDeadline, errDashboardNativeArrowStreamIdle
}

func (w *dashboardNativeArrowStreamWriter) refreshIdleTimer() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.idleTimer == nil {
		w.idleTimer = time.AfterFunc(w.idle, func() { w.cancel(errDashboardNativeArrowStreamIdle) })
		return
	}
	w.idleTimer.Reset(w.idle)
}

func (w *dashboardNativeArrowStreamWriter) committedResponse() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.committed
}

func (w *dashboardNativeArrowStreamWriter) bytesWritten() int64 {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.bytes
}

func (w *dashboardNativeArrowStreamWriter) stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	if w.idleTimer != nil {
		w.idleTimer.Stop()
	}
	w.mu.Unlock()
	_ = stdhttp.NewResponseController(w.ResponseWriter).SetWriteDeadline(time.Time{})
}

func dashboardNativeArrowStreamTimeoutReason(err error) string {
	switch {
	case errors.Is(err, errDashboardNativeArrowStreamIdle):
		return "idle_write_timeout"
	case errors.Is(err, errDashboardNativeArrowStreamHardLimit):
		return "maximum_lifetime"
	case errors.Is(err, context.DeadlineExceeded):
		return "request_deadline"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return ""
	}
}
