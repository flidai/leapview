// Package module owns durable job persistence and runner lifecycle.
package module

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	jobpolicy "github.com/flidai/leapview/internal/platform/jobs"
	jobsqlite "github.com/flidai/leapview/internal/platform/jobs/sqlite"
	"github.com/flidai/leapview/internal/platform/transaction"
	"github.com/flidai/leapview/pkg/jobs"
)

type Config struct {
	Database     *sql.DB
	Admission    jobs.Admitter
	LeaseTimeout time.Duration
	PollInterval time.Duration
	Logger       *slog.Logger
}

type Module struct {
	repository *jobsqlite.Repository
	config     Config
	runner     *jobs.Runner
	handlers   map[string]struct{}
	mu         sync.Mutex
	cancel     context.CancelFunc
	done       chan struct{}
}

func Build(_ context.Context, config Config) (*Module, error) {
	if config.Database == nil || config.Admission == nil {
		return nil, errors.New("job database and admission are required")
	}
	return &Module{repository: jobsqlite.NewRepository(config.Database), config: config}, nil
}

func (m *Module) RegisterHandlers(handlers []jobs.Handler) error {
	if m == nil || m.repository == nil {
		return errors.New("job module is not initialized")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runner != nil || m.cancel != nil {
		return errors.New("job handlers are already registered")
	}
	runner, err := jobs.NewRunner(jobs.RunnerConfig{
		Repository: m.repository, Admission: m.config.Admission, Handlers: handlers,
		Classes:      []string{jobpolicy.WorkloadClassControl, jobpolicy.WorkloadClassBackground},
		LeaseTimeout: m.config.LeaseTimeout, PollInterval: m.config.PollInterval, Logger: m.config.Logger,
		OwnerFactory: func() string { return jobpolicy.WorkerOwner(time.Now().UnixNano()) },
		FailureEncoder: func(err error) []byte {
			payload, marshalErr := json.Marshal(map[string]string{"code": "ASYNC_JOB_FAILED", "detail": err.Error()})
			if marshalErr != nil {
				return []byte(`{"code":"ASYNC_JOB_FAILED","detail":"job failed"}`)
			}
			return payload
		},
	})
	if err != nil {
		return err
	}
	m.runner = runner
	m.handlers = make(map[string]struct{}, len(handlers))
	for _, handler := range handlers {
		m.handlers[handler.Kind()] = struct{}{}
	}
	return nil
}

func (m *Module) Start(ctx context.Context) error {
	if m == nil || m.runner == nil {
		return errors.New("job module is not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reapStoppedLocked()
	if m.cancel != nil {
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	m.cancel, m.done = cancel, done
	go func() {
		m.runner.Run(runCtx)
		m.mu.Lock()
		if m.done == done {
			m.cancel, m.done = nil, nil
		}
		m.mu.Unlock()
		close(done)
	}()
	return nil
}

func (m *Module) Stop(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	cancel, done := m.cancel, m.done
	if cancel == nil {
		m.mu.Unlock()
		return nil
	}
	cancel()
	m.mu.Unlock()
	select {
	case <-done:
		m.mu.Lock()
		if m.done == done {
			m.cancel, m.done = nil, nil
		}
		m.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// reapStoppedLocked makes a timed-out stop recoverable once the runner has
// actually exited. The stop call remains bounded by its caller's context;
// the terminal worker state is observed by the next lifecycle operation.
func (m *Module) reapStoppedLocked() {
	if m.done == nil {
		return
	}
	select {
	case <-m.done:
		m.cancel, m.done = nil, nil
	default:
	}
}

func (m *Module) Enqueue(ctx context.Context, input jobs.EnqueueInput) (jobs.Job, error) {
	m.mu.Lock()
	_, registered := m.handlers[input.Kind]
	configured := m.handlers != nil
	m.mu.Unlock()
	if !configured || !registered {
		return jobs.Job{}, errors.Join(jobs.ErrUnknownKind, errors.New(input.Kind))
	}
	return m.repository.Enqueue(ctx, input)
}
func (m *Module) Get(ctx context.Context, id string) (jobs.Job, error) {
	return m.repository.Get(ctx, id)
}
func (m *Module) Candidates(ctx context.Context, class string, limit int) ([]jobs.Job, error) {
	return m.repository.Candidates(ctx, class, limit)
}
func (m *Module) ClaimByID(ctx context.Context, id, class, owner string, lease time.Duration) (jobs.Job, bool, error) {
	return m.repository.ClaimByID(ctx, id, class, owner, lease)
}
func (m *Module) Renew(ctx context.Context, id string, fence jobs.Fence, lease time.Duration) error {
	return m.repository.Renew(ctx, id, fence, lease)
}
func (m *Module) Complete(ctx context.Context, id string, fence jobs.Fence) error {
	return m.repository.Complete(ctx, id, fence)
}
func (m *Module) Fail(ctx context.Context, id string, fence jobs.Fence, problem []byte) error {
	return m.repository.Fail(ctx, id, fence, problem)
}
func (m *Module) Cancel(ctx context.Context, id string) error {
	return m.repository.Cancel(ctx, id)
}
func (m *Module) CancelClaimed(ctx context.Context, id string, fence jobs.Fence) error {
	return m.repository.CancelClaimed(ctx, id, fence)
}
func (m *Module) AppendEvent(ctx context.Context, kind, id, event string, data []byte) (jobs.Event, error) {
	return m.repository.AppendEvent(ctx, kind, id, event, data)
}
func (m *Module) RecordWorkflow(ctx context.Context, tx transaction.Transaction, intent jobs.WorkflowIntent) error {
	if intent.Job.ID == "" {
		return m.repository.RecordWorkflow(ctx, tx, intent)
	}
	m.mu.Lock()
	_, registered := m.handlers[intent.Job.Kind]
	configured := m.handlers != nil
	m.mu.Unlock()
	if !configured || !registered {
		return errors.Join(jobs.ErrUnknownKind, errors.New(intent.Job.Kind))
	}
	return m.repository.RecordWorkflow(ctx, tx, intent)
}
func (m *Module) CommitWorkflow(ctx context.Context, intent jobs.WorkflowIntent) error {
	if m == nil || m.repository == nil || m.config.Database == nil {
		return jobs.ErrStoreRequired
	}
	tx, err := m.config.Database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := m.RecordWorkflow(ctx, tx, intent); err != nil {
		return err
	}
	return tx.Commit()
}
func (m *Module) ListEvents(ctx context.Context, kind, id string, after int64, limit int) ([]jobs.Event, error) {
	return m.repository.ListEvents(ctx, kind, id, after, limit)
}
