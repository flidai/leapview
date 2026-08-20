package runtimehost

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type persistentSnapshotLease struct {
	repo           SnapshotLeaseRepository
	id             string
	servingStateID servingstate.ID
	snapshotID     int64
	cancel         context.CancelFunc
	enqueue        func(snapshotLeaseReleaseTask) error
	once           sync.Once
	err            error
}

func (l *persistentSnapshotLease) Close() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		if l.cancel != nil {
			l.cancel()
		}
		if l.enqueue != nil {
			l.err = l.enqueue(snapshotLeaseReleaseTask{repo: l.repo, leaseID: l.id, servingStateID: l.servingStateID, snapshotID: l.snapshotID})
		} else {
			l.err = releaseSnapshotLease(l.repo, l.id)
		}
	})
	return l.err
}
func (m *Manager) createPersistentLease(ctx context.Context, id servingstate.ID, snapshotID int64) (*persistentSnapshotLease, error) {
	repo, ok := m.repo.(SnapshotLeaseRepository)
	if !ok || snapshotID <= 0 {
		return nil, nil
	}
	expiresAt := time.Now().UTC().Add(m.leaseTTL)
	leaseID, err := repo.CreateQuerySnapshotLease(ctx, servingstate.SnapshotLeaseInput{ServingStateID: id, DuckLakeSnapshotID: snapshotID, OwnerID: m.leaseOwner, ExpiresAt: expiresAt})
	if err != nil {
		return nil, err
	}
	heartbeatCtx, cancel := context.WithCancel(context.Background())
	go m.heartbeatLease(heartbeatCtx, repo, leaseID, expiresAt)
	return &persistentSnapshotLease{repo: repo, id: leaseID, servingStateID: id, snapshotID: snapshotID, cancel: cancel, enqueue: m.releaseQueue.enqueue}, nil
}
func (m *Manager) heartbeatLease(ctx context.Context, repo SnapshotLeaseRepository, id string, confirmedExpiry ...time.Time) {
	interval := m.leaseTTL / 2
	if interval <= 0 {
		interval = time.Minute
	}
	deadline := time.Now().UTC().Add(m.leaseTTL)
	if len(confirmedExpiry) > 0 && !confirmedExpiry[0].IsZero() {
		deadline = confirmedExpiry[0]
	}
	if remaining := time.Until(deadline); remaining <= 0 {
		interval = 0
	} else if interval > remaining {
		interval = remaining
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			m.setLeaseRenewalError(id, nil)
			return
		case <-timer.C:
			expires := time.Now().UTC().Add(m.leaseTTL)
			renewCtx, cancel := context.WithDeadline(ctx, deadline)
			err := renewSnapshotLease(renewCtx, repo, id, expires, 3, 100*time.Millisecond)
			cancel()
			if err == nil && !time.Now().UTC().Before(deadline) {
				err = context.DeadlineExceeded
			}
			if err == nil {
				deadline = expires
				m.setLeaseRenewalError(id, nil)
				if m.onLeaseRenewalFailure != nil {
					m.onLeaseRenewalFailure(nil)
				}
				timer.Reset(interval)
				continue
			}
			// A failed renewal is transient while the last confirmed durable
			// expiry remains in the future. Retry quickly instead of poisoning
			// serving health after one provider burst.
			if time.Now().UTC().Before(deadline) {
				retry := interval / 4
				if retry <= 0 {
					retry = time.Millisecond
				}
				remaining := time.Until(deadline)
				if remaining > 0 && retry > remaining {
					retry = remaining
				}
				timer.Reset(retry)
				continue
			}
			m.setLeaseRenewalError(id, err)
			if m.onLeaseRenewalFailure != nil {
				m.onLeaseRenewalFailure(err)
			}
			return
		}
	}
}
func renewSnapshotLease(ctx context.Context, repo SnapshotLeaseRepository, id string, expires time.Time, attempts int, backoff time.Duration) error {
	var last error
	for i := 0; i < attempts; i++ {
		request, cancel := context.WithTimeout(ctx, 5*time.Second)
		last = repo.ExtendQuerySnapshotLease(request, id, expires)
		cancel()
		if last == nil {
			return nil
		}
		if i+1 < attempts {
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			backoff *= 2
		}
	}
	return fmt.Errorf("extend snapshot lease %q after %d attempts: %w", id, attempts, last)
}
func releaseSnapshotLease(repo SnapshotLeaseRepository, id string) error {
	if repo == nil || id == "" {
		return nil
	}
	delay := 25 * time.Millisecond
	var last error
	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		last = repo.ReleaseQuerySnapshotLease(ctx, id)
		cancel()
		if last == nil {
			return nil
		}
		time.Sleep(delay)
		delay *= 2
	}
	return last
}
func (m *Manager) releaseSnapshotLease(task snapshotLeaseReleaseTask) error {
	err := releaseSnapshotLease(task.repo, task.leaseID)
	if err != nil && m.onCleanupFailure != nil {
		m.onCleanupFailure(CleanupFailure{ProjectID: m.ProjectID(), ServingStateID: task.servingStateID, DuckLakeSnapshotID: task.snapshotID, Resource: CleanupResourceSnapshotLease, Err: err})
	}
	return err
}

type snapshotLeaseReleaseTask struct {
	repo           SnapshotLeaseRepository
	leaseID        string
	servingStateID servingstate.ID
	snapshotID     int64
}
type snapshotLeaseReleaseQueue struct {
	mu         sync.Mutex
	queue      chan snapshotLeaseReleaseTask
	accepting  bool
	workerDone chan struct{}
	process    func(snapshotLeaseReleaseTask) error
	pending    atomic.Int64
}

func newSnapshotLeaseReleaseQueue(capacity int, process func(snapshotLeaseReleaseTask) error) *snapshotLeaseReleaseQueue {
	q := &snapshotLeaseReleaseQueue{queue: make(chan snapshotLeaseReleaseTask, capacity), accepting: true, workerDone: make(chan struct{}), process: process}
	go q.run()
	return q
}
func (q *snapshotLeaseReleaseQueue) enqueue(task snapshotLeaseReleaseTask) error {
	if q == nil || task.repo == nil || task.leaseID == "" {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.accepting {
		return errors.New("snapshot lease release queue is closed")
	}
	select {
	case q.queue <- task:
		q.pending.Add(1)
		return nil
	default:
		return errors.New("snapshot lease release queue is full")
	}
}
func (q *snapshotLeaseReleaseQueue) run() {
	defer close(q.workerDone)
	for task := range q.queue {
		if q.process != nil {
			_ = q.process(task)
		}
		q.pending.Add(-1)
	}
}
func (q *snapshotLeaseReleaseQueue) close(timeout time.Duration) error {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	if q.accepting {
		q.accepting = false
		close(q.queue)
	}
	q.mu.Unlock()
	if timeout <= 0 {
		<-q.workerDone
		return nil
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-q.workerDone:
		return nil
	case <-timer.C:
		return fmt.Errorf("snapshot lease release queue did not drain before shutdown (remaining=%d)", q.len())
	}
}
func (q *snapshotLeaseReleaseQueue) len() int {
	if q == nil {
		return 0
	}
	return int(q.pending.Load())
}

func closeRuntime(runtime Runtime) error {
	if runtime == nil {
		return nil
	}
	return runtime.Close()
}
func releaseManaged(value ManagedDataLifetime) error {
	if value == nil {
		return nil
	}
	return value.Release()
}
func closeRuntimeLifetime(value RuntimeLifetime) error {
	if value == nil {
		return nil
	}
	return value.Close()
}
func closeSnapshotLease(value *persistentSnapshotLease) error {
	if value == nil {
		return nil
	}
	return value.Close()
}
func closeCandidatePreparationLifetime(candidate *candidatePreparationContext) error {
	if candidate == nil {
		return nil
	}
	err := closeRuntimeLifetime(candidate.lifetime)
	candidate.lifetime = nil
	return err
}
func normalizedLeaseTTL(value time.Duration) time.Duration {
	if value <= 0 {
		return 5 * time.Minute
	}
	return value
}
func normalizedCleanupDrainTimeout(value time.Duration) time.Duration {
	if value <= 0 {
		return 15 * time.Second
	}
	return value
}
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
func snapshotKeys(values map[int64]struct{}) []int64 {
	keys := make([]int64, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}
