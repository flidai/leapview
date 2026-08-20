package runtimehost

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

func (m *Manager) Acquire(context.Context) (Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil || m.current.closing {
		return nil, errors.New("no active LeapView serving state")
	}
	m.current.refs++
	return &runtimeLease{manager: m, managed: m.current}, nil
}
func (m *Manager) LeasedSnapshots() []int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	set := map[int64]struct{}{}
	if m.current != nil && m.current.snapshotID > 0 {
		set[m.current.snapshotID] = struct{}{}
	}
	for _, r := range m.retired {
		if r.snapshotID > 0 {
			set[r.snapshotID] = struct{}{}
		}
	}
	return snapshotKeys(set)
}
func (m *Manager) Close() error {
	return m.close(false)
}

func (m *Manager) closeWithoutReleaseQueue() error {
	return m.close(true)
}

func (m *Manager) close(skipReleaseQueue bool) error {
	m.mu.Lock()
	current := m.current
	m.current = nil
	targets := m.retireLocked(current)
	waiting := m.scheduledCleanupLocked()
	m.mu.Unlock()
	m.cleanupRetired(targets)
	cleanupErr := m.waitForCleanup(waiting)
	if cleanupErr != nil {
		// Keep the release queue alive while reader-draining generations still
		// own persistent snapshot leases. A later Release can then enqueue its
		// cleanup after the caller resolves the shutdown timeout.
		go m.closeReleaseQueueAfterCleanup(waiting)
		return cleanupErr
	}
	var queueErr error
	if !skipReleaseQueue && m.releaseQueue != nil {
		queueErr = m.releaseQueue.close(m.releaseShutdownTimeout)
	}
	return errors.Join(cleanupErr, queueErr)
}

func (m *Manager) closeReleaseQueueAfterCleanup(targets []*managedRuntime) {
	for _, runtime := range targets {
		if runtime != nil && runtime.cleanupDone != nil {
			<-runtime.cleanupDone
		}
	}
	if m.releaseQueue != nil {
		_ = m.releaseQueue.close(m.releaseShutdownTimeout)
	}
}

func (m *Manager) retireLocked(runtime *managedRuntime) *managedRuntime {
	if runtime == nil {
		return nil
	}
	if runtime.closing {
		return nil
	}
	runtime.closing = true
	runtime.cleanupState = generationCleanupDraining
	runtime.cleanupDone = make(chan struct{})
	m.retired = append(m.retired, runtime)
	if runtime.refs == 0 {
		runtime.cleanupState = generationCleanupPending
		return runtime
	}
	return nil
}
func (m *Manager) release(runtime *managedRuntime) {
	var drained *managedRuntime
	m.mu.Lock()
	if runtime != nil && runtime.refs > 0 {
		runtime.refs--
		if runtime.refs == 0 && runtime.closing {
			drained = runtime
			runtime.cleanupState = generationCleanupPending
		}
	}
	m.mu.Unlock()
	m.cleanupRetired(drained)
}

type managedRuntime struct {
	identity                projectgraph.ServingIdentity
	authorization           accesssnapshot.AuthorizationSnapshot
	servingStateID          servingstate.ID
	digest, managedRevision string
	runtime                 Runtime
	managedData             ManagedDataLifetime
	snapshotLease           *persistentSnapshotLease
	runtimeLifetime         RuntimeLifetime
	snapshotID              int64
	sealed                  bool
	refs                    int
	closing                 bool
	cleanupState            generationCleanupState
	cleanupDone             chan struct{}
	cleanupErr              error
	cleanupOnce             sync.Once
	cleanupResults          []cleanupResult
}
type generationCleanupState uint8

const (
	generationCleanupNone generationCleanupState = iota
	generationCleanupDraining
	generationCleanupPending
	generationCleanupRunning
	generationCleanupFinished
)

type GenerationCleanupState string

const (
	GenerationCleanupDraining GenerationCleanupState = "draining_readers"
	GenerationCleanupPending  GenerationCleanupState = "cleanup_pending"
	GenerationCleanupRunning  GenerationCleanupState = "cleanup_running"
)

type RetiredGeneration struct {
	ServingStateID     servingstate.ID
	DuckLakeSnapshotID int64
	Readers            int
	CleanupState       GenerationCleanupState
}

func (m *Manager) RetiredGenerations() []RetiredGeneration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]RetiredGeneration, 0, len(m.retired))
	for _, r := range m.retired {
		state := GenerationCleanupDraining
		if r.cleanupState == generationCleanupPending {
			state = GenerationCleanupPending
		} else if r.cleanupState == generationCleanupRunning {
			state = GenerationCleanupRunning
		}
		out = append(out, RetiredGeneration{r.servingStateID, r.snapshotID, r.refs, state})
	}
	return out
}
func (m *Manager) cleanupRetired(runtime *managedRuntime) {
	if runtime == nil {
		return
	}
	m.mu.Lock()
	if runtime.cleanupState == generationCleanupPending && !m.cleanupWorkerRunning {
		m.cleanupWorkerRunning = true
		go m.runCleanupWorker()
	}
	m.mu.Unlock()
}
func (m *Manager) runCleanupWorker() {
	for {
		m.mu.Lock()
		var runtime *managedRuntime
		for _, r := range m.retired {
			if r.cleanupState == generationCleanupPending {
				runtime = r
				break
			}
		}
		if runtime == nil {
			m.cleanupWorkerRunning = false
			m.mu.Unlock()
			return
		}
		runtime.cleanupState = generationCleanupRunning
		m.mu.Unlock()
		results := m.closeManagedResources(runtime)
		for _, result := range results {
			if result.err != nil && m.onCleanupFailure != nil {
				m.onCleanupFailure(CleanupFailure{ProjectID: m.ProjectID(), ServingStateID: runtime.servingStateID, DuckLakeSnapshotID: runtime.snapshotID, Resource: result.resource, Err: result.err})
			}
		}
		m.mu.Lock()
		var errs []error
		for _, result := range results {
			errs = append(errs, result.err)
		}
		runtime.cleanupErr = errors.Join(errs...)
		runtime.cleanupState = generationCleanupFinished
		m.removeRetiredLocked(runtime)
		close(runtime.cleanupDone)
		m.mu.Unlock()
		if m.onDrained != nil {
			m.onDrained(runtime.servingStateID, runtime.snapshotID)
		}
	}
}
func (m *Manager) removeRetiredLocked(runtime *managedRuntime) {
	for i, r := range m.retired {
		if r == runtime {
			m.retired = append(m.retired[:i], m.retired[i+1:]...)
			return
		}
	}
}
func (m *Manager) scheduledCleanupLocked() []*managedRuntime {
	out := make([]*managedRuntime, 0, len(m.retired))
	for _, r := range m.retired {
		if r.cleanupState != generationCleanupFinished {
			out = append(out, r)
		}
	}
	return out
}
func (m *Manager) waitForCleanup(targets []*managedRuntime) error {
	if len(targets) == 0 {
		return nil
	}
	timer := time.NewTimer(m.cleanupDrainTimeout)
	defer timer.Stop()
	var errs []error
	for _, r := range targets {
		select {
		case <-r.cleanupDone:
			errs = append(errs, r.cleanupErr)
		case <-timer.C:
			return errors.Join(errors.Join(errs...), fmt.Errorf("runtime cleanup did not drain within %s", m.cleanupDrainTimeout))
		}
	}
	return errors.Join(errs...)
}

type cleanupResult struct {
	resource CleanupResource
	err      error
}

func (m *Manager) closeManaged(runtime *managedRuntime) error {
	var errs []error
	for _, result := range m.closeManagedResources(runtime) {
		errs = append(errs, result.err)
	}
	return errors.Join(errs...)
}
func (m *Manager) closeManagedResources(runtime *managedRuntime) []cleanupResult {
	if runtime == nil {
		return nil
	}
	runtime.cleanupOnce.Do(func() {
		var out []cleanupResult
		if err := closeRuntime(runtime.runtime); err != nil {
			out = append(out, cleanupResult{CleanupResourceRuntime, err})
		}
		if err := releaseManaged(runtime.managedData); err != nil {
			out = append(out, cleanupResult{CleanupResourceManagedData, err})
		}
		if err := closeSnapshotLease(runtime.snapshotLease); err != nil {
			out = append(out, cleanupResult{CleanupResourceSnapshotLease, err})
		}
		if err := closeRuntimeLifetime(runtime.runtimeLifetime); err != nil {
			out = append(out, cleanupResult{CleanupResourceDependency, err})
		}
		runtime.cleanupResults = out
	})
	return append([]cleanupResult(nil), runtime.cleanupResults...)
}

type runtimeLease struct {
	manager *Manager
	managed *managedRuntime
	once    sync.Once
}

func (l *runtimeLease) Runtime() Runtime {
	if l == nil || l.managed == nil {
		return nil
	}
	return l.managed.runtime
}
func (l *runtimeLease) AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot {
	if l == nil || l.managed == nil {
		return accesssnapshot.AuthorizationSnapshot{}
	}
	return l.managed.authorization
}
func (l *runtimeLease) Identity() projectgraph.ServingIdentity {
	if l == nil || l.managed == nil {
		return projectgraph.ServingIdentity{}
	}
	return l.managed.identity
}
func (l *runtimeLease) DuckLakeSnapshotID() int64 {
	if l == nil || l.managed == nil {
		return 0
	}
	return l.managed.snapshotID
}
func (l *runtimeLease) Release() {
	if l == nil || l.manager == nil {
		return
	}
	l.once.Do(func() { l.manager.release(l.managed) })
}
