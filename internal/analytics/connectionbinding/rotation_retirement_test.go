package connectionbinding

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPoolManagerRetireBoundedDrainsBeforeDeadlineAndRejectsNewLeases(t *testing.T) {
	now := time.Now().UTC()
	factory := &recordingPoolFactory{}
	manager := newRetirementTestManager(t, now, factory)
	require.NoError(t, manager.RefreshNow(context.Background()))
	lease, err := manager.Lease()
	require.NoError(t, err)
	pool := lease.Pool().(*recordingRuntimePool)

	retired := make(chan error, 1)
	go func() {
		retired <- manager.RetireBounded(context.Background(), time.Now().Add(time.Second))
	}()
	startWait := time.NewTimer(time.Second)
	defer startWait.Stop()
	for {
		manager.mu.Lock()
		started := manager.retired
		manager.mu.Unlock()
		if started {
			break
		}
		select {
		case <-startWait.C:
			t.Fatal("retirement did not close admission")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	select {
	case err := <-retired:
		t.Fatalf("retirement completed before reader release: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if _, err := manager.Lease(); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("Lease() during retirement error = %v", err)
	}
	if pool.closed {
		t.Fatal("pool closed before the outstanding reader drained")
	}

	lease.Release()
	select {
	case err := <-retired:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("retirement did not complete after reader release")
	}
	if !pool.closed {
		t.Fatal("drained pool was not closed")
	}
}

func TestPoolManagerRetireBoundedForceClosesAtDeadline(t *testing.T) {
	now := time.Now().UTC()
	factory := &recordingPoolFactory{}
	manager := newRetirementTestManager(t, now, factory)
	require.NoError(t, manager.RefreshNow(context.Background()))
	lease, err := manager.Lease()
	require.NoError(t, err)
	pool := lease.Pool().(*recordingRuntimePool)

	err = manager.RetireBounded(context.Background(), time.Now().Add(40*time.Millisecond))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RetireBounded() error = %v, want deadline exceeded after forced close", err)
	}
	if !pool.closed {
		t.Fatal("pool was not force-closed at retirement deadline")
	}

	// A late reader release must not close the runtime a second time or reopen
	// the manager's admission fence.
	lease.Release()
	if _, err := manager.Lease(); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("Lease() after forced retirement error = %v", err)
	}
}

func TestPoolManagerDisableBoundedPersistsFenceAndForceClosesReaders(t *testing.T) {
	now := time.Now().UTC()
	factory := &recordingPoolFactory{}
	store := &recordingBindingStore{}
	manager, err := NewPoolManager(PoolManagerConfig{
		Binding: validTargetBinding(t),
		Resolver: &sequenceResolver{snapshots: []CredentialSnapshot{
			testSnapshot(t, "version-retirement", now),
		}},
		Factory: factory, Store: store, Audit: noOpRotationAudit{},
		Now: func() time.Time { return now }, StaleAfter: time.Hour,
	})
	require.NoError(t, err)
	require.NoError(t, manager.RefreshNow(context.Background()))
	lease, err := manager.Lease()
	require.NoError(t, err)
	pool := lease.Pool().(*recordingRuntimePool)

	err = manager.DisableBounded(context.Background(), now.Add(time.Minute), time.Now().Add(40*time.Millisecond))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.False(t, store.binding.Enabled)
	require.True(t, pool.closed)
	_, err = manager.Lease()
	require.ErrorIs(t, err, ErrProviderUnavailable)
	lease.Release()
}

func TestPoolManagerDisableBoundedClosesPoolAfterPostFencePersistenceErrors(t *testing.T) {
	for _, failure := range []error{errors.New("persistence failed"), ErrIncompatibleBinding} {
		t.Run(failure.Error(), func(t *testing.T) {
			now := time.Now().UTC()
			factory := &recordingPoolFactory{}
			store := &retirementFailingStore{failure: failure, failAt: 2}
			manager, err := NewPoolManager(PoolManagerConfig{
				Binding: validTargetBinding(t), Resolver: &sequenceResolver{snapshots: []CredentialSnapshot{testSnapshot(t, "version-retirement", now)}},
				Factory: factory, Store: store, Audit: noOpRotationAudit{}, Now: func() time.Time { return now }, StaleAfter: time.Hour,
			})
			require.NoError(t, err)
			require.NoError(t, manager.RefreshNow(context.Background()))
			lease, err := manager.Lease()
			require.NoError(t, err)
			pool := lease.Pool().(*recordingRuntimePool)
			err = manager.DisableBounded(context.Background(), now.Add(time.Minute), time.Now().Add(40*time.Millisecond))
			require.ErrorIs(t, err, failure)
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.True(t, pool.closed)
			require.ErrorIs(t, func() error { _, leaseErr := manager.Lease(); return leaseErr }(), ErrProviderUnavailable)
			lease.Release()
		})
	}
}

func TestPoolManagerRetireBoundedIsIdempotent(t *testing.T) {
	now := time.Now().UTC()
	factory := &retirementCountingFactory{}
	manager, err := NewPoolManager(PoolManagerConfig{
		Binding:  validTargetBinding(t),
		Resolver: &sequenceResolver{snapshots: []CredentialSnapshot{testSnapshot(t, "version-retirement", now)}},
		Factory:  factory, Store: &recordingBindingStore{}, Audit: noOpRotationAudit{},
		Now: func() time.Time { return now }, StaleAfter: time.Hour,
	})
	require.NoError(t, err)
	require.NoError(t, manager.RefreshNow(context.Background()))

	if err := manager.RetireBounded(context.Background(), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := manager.RetireBounded(context.Background(), time.Now().Add(time.Second)); err != nil {
		t.Fatalf("second RetireBounded() error = %v", err)
	}
	if err := manager.Retire(); err != nil {
		t.Fatalf("Retire() after RetireBounded() error = %v", err)
	}
	factory.pool.mu.Lock()
	closed, closeCount := factory.pool.closed, factory.pool.closeCount
	factory.pool.mu.Unlock()
	if !closed || closeCount != 1 {
		t.Fatalf("retired pool closed=%t close_count=%d, want one close", closed, closeCount)
	}
}

func TestPoolManagerRetireBoundedCancellationFailsClosed(t *testing.T) {
	now := time.Now().UTC()
	factory := &recordingPoolFactory{}
	manager := newRetirementTestManager(t, now, factory)
	require.NoError(t, manager.RefreshNow(context.Background()))
	lease, err := manager.Lease()
	require.NoError(t, err)
	pool := lease.Pool().(*recordingRuntimePool)

	ctx, cancel := context.WithCancel(context.Background())
	retired := make(chan error, 1)
	go func() { retired <- manager.RetireBounded(ctx, time.Now().Add(time.Second)) }()
	startWait := time.NewTimer(time.Second)
	defer startWait.Stop()
	for {
		manager.mu.Lock()
		started := manager.retired
		manager.mu.Unlock()
		if started {
			break
		}
		select {
		case <-startWait.C:
			t.Fatal("retirement did not close admission")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	err = <-retired
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RetireBounded() cancellation error = %v, want context canceled", err)
	}
	if !pool.closed {
		t.Fatal("canceled retirement left the credential-bearing pool open")
	}
	lease.Release()
}

func TestPoolManagerRetireBoundedPropagatesPoolCloseError(t *testing.T) {
	now := time.Now().UTC()
	closeErr := errors.New("runtime close failed")
	factory := &retirementCountingFactory{pool: &retirementCountingPool{closeErr: closeErr}}
	manager, err := NewPoolManager(PoolManagerConfig{
		Binding:  validTargetBinding(t),
		Resolver: &sequenceResolver{snapshots: []CredentialSnapshot{testSnapshot(t, "version-retirement", now)}},
		Factory:  factory, Store: &recordingBindingStore{}, Audit: noOpRotationAudit{},
		Now: func() time.Time { return now }, StaleAfter: time.Hour,
	})
	require.NoError(t, err)
	require.NoError(t, manager.RefreshNow(context.Background()))

	if err := manager.RetireBounded(context.Background(), time.Now().Add(time.Second)); !errors.Is(err, closeErr) {
		t.Fatalf("RetireBounded() close error = %v, want %v", err, closeErr)
	}
}

func TestPoolManagerRetirementFencesRefreshResurrection(t *testing.T) {
	now := time.Now().UTC()
	resolver := &retirementBlockingResolver{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		snapshot: testSnapshot(t, "version-after-retirement", now),
	}
	factory := &recordingPoolFactory{}
	manager, err := NewPoolManager(PoolManagerConfig{
		Binding: validTargetBinding(t), Resolver: resolver, Factory: factory,
		Store: &recordingBindingStore{}, Audit: noOpRotationAudit{},
		Now: func() time.Time { return now }, StaleAfter: time.Hour,
	})
	require.NoError(t, err)
	refreshDone := make(chan error, 1)
	go func() { refreshDone <- manager.RefreshNow(context.Background()) }()
	<-resolver.started

	require.NoError(t, manager.RetireBounded(context.Background(), time.Now().Add(time.Second)))
	close(resolver.release)
	if err := <-refreshDone; !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("refresh finishing after retirement error = %v", err)
	}
	if len(factory.pools) != 0 {
		t.Fatalf("refresh resurrected %d pool(s) after retirement", len(factory.pools))
	}
}

func TestPoolManagerRetirementFencesProviderFailurePersistence(t *testing.T) {
	now := time.Now().UTC()
	resolver := &retirementBlockingErrorResolver{started: make(chan struct{}), release: make(chan struct{})}
	store := &recordingBindingStore{}
	manager, err := NewPoolManager(PoolManagerConfig{
		Binding: validTargetBinding(t), Resolver: resolver, Factory: &recordingPoolFactory{},
		Store: store, Audit: noOpRotationAudit{}, Now: func() time.Time { return now }, StaleAfter: time.Hour,
	})
	require.NoError(t, err)
	refreshDone := make(chan error, 1)
	go func() { refreshDone <- manager.RefreshNow(context.Background()) }()
	<-resolver.started
	require.NoError(t, manager.RetireBounded(context.Background(), time.Now().Add(time.Second)))
	close(resolver.release)
	require.ErrorIs(t, <-refreshDone, ErrProviderUnavailable)
	if store.binding.ID != "" {
		t.Fatalf("retired refresh persisted degraded evidence: %#v", store.binding)
	}
}

func TestPoolManagerRetirementDoesNotWaitForBlockedPersistence(t *testing.T) {
	now := time.Now().UTC()
	store := &retirementBlockingStore{started: make(chan struct{}), release: make(chan struct{})}
	manager, err := NewPoolManager(PoolManagerConfig{
		Binding: validTargetBinding(t),
		Resolver: &sequenceResolver{snapshots: []CredentialSnapshot{
			testSnapshot(t, "version-retirement", now), testSnapshot(t, "version-retirement", now.Add(time.Minute)),
		}},
		Factory: &recordingPoolFactory{}, Store: store, Audit: noOpRotationAudit{},
		Now: func() time.Time { return now }, StaleAfter: time.Hour,
	})
	require.NoError(t, err)
	require.NoError(t, manager.RefreshNow(t.Context()))
	now = now.Add(time.Minute)
	refreshDone := make(chan error, 1)
	go func() { refreshDone <- manager.RefreshNow(context.Background()) }()
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not reach blocked persistence")
	}

	retireDone := make(chan error, 1)
	go func() { retireDone <- manager.RetireBounded(context.Background(), time.Now().Add(time.Second)) }()
	select {
	case err := <-retireDone:
		require.NoError(t, err)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("retirement waited behind blocked persistence")
	}
	close(store.release)
	require.ErrorIs(t, <-refreshDone, ErrProviderUnavailable)
}

func TestPoolManagerRetireBoundedCancelsResolverAndWaitsForExit(t *testing.T) {
	now := time.Now().UTC()
	resolver := &retirementExitResolver{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
		release:  make(chan struct{}),
	}
	manager, err := NewPoolManager(PoolManagerConfig{
		Binding: validTargetBinding(t), Resolver: resolver, Factory: &recordingPoolFactory{},
		Store: &recordingBindingStore{}, Audit: noOpRotationAudit{},
		Now: func() time.Time { return now }, StaleAfter: time.Hour,
	})
	require.NoError(t, err)
	refreshDone := make(chan error, 1)
	go func() { refreshDone <- manager.RefreshNow(context.Background()) }()
	<-resolver.started

	retireDone := make(chan error, 1)
	go func() { retireDone <- manager.RetireBounded(context.Background(), time.Now().Add(time.Second)) }()
	<-resolver.canceled
	select {
	case err := <-retireDone:
		t.Fatalf("retirement completed before resolver exited: %v", err)
	default:
	}
	close(resolver.release)
	require.NoError(t, <-retireDone)
	require.ErrorIs(t, <-refreshDone, ErrProviderUnavailable)
}

func TestPoolManagerDisableBoundedCancelsRefreshBeforePersistingFence(t *testing.T) {
	now := time.Now().UTC()
	resolver := &retirementExitResolver{started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{})}
	store := &recordingBindingStore{}
	manager, err := NewPoolManager(PoolManagerConfig{
		Binding: validTargetBinding(t), Resolver: resolver, Factory: &recordingPoolFactory{}, Store: store,
		Audit: noOpRotationAudit{}, Now: func() time.Time { return now }, StaleAfter: time.Hour,
	})
	require.NoError(t, err)
	refreshDone := make(chan error, 1)
	go func() { refreshDone <- manager.RefreshNow(context.Background()) }()
	<-resolver.started
	disableDone := make(chan error, 1)
	go func() {
		disableDone <- manager.DisableBounded(context.Background(), now.Add(time.Minute), time.Now().Add(time.Second))
	}()
	<-resolver.canceled
	select {
	case err := <-disableDone:
		t.Fatalf("disable completed before canceled refresh exited: %v", err)
	default:
	}
	close(resolver.release)
	require.NoError(t, <-disableDone)
	require.ErrorIs(t, <-refreshDone, ErrProviderUnavailable)
	require.False(t, manager.HealthStatus().HasActivePool)
}

func TestPoolManagerRetireBoundedCancelsFactoryAndWaitsForExit(t *testing.T) {
	now := time.Now().UTC()
	factory := &retirementExitFactory{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
		release:  make(chan struct{}),
	}
	manager, err := NewPoolManager(PoolManagerConfig{
		Binding: validTargetBinding(t),
		Resolver: &sequenceResolver{snapshots: []CredentialSnapshot{
			testSnapshot(t, "version-factory-cancel", now),
		}},
		Factory: factory, Store: &recordingBindingStore{}, Audit: noOpRotationAudit{},
		Now: func() time.Time { return now }, StaleAfter: time.Hour,
	})
	require.NoError(t, err)
	refreshDone := make(chan error, 1)
	go func() { refreshDone <- manager.RefreshNow(context.Background()) }()
	<-factory.started

	retireDone := make(chan error, 1)
	go func() { retireDone <- manager.RetireBounded(context.Background(), time.Now().Add(time.Second)) }()
	<-factory.canceled
	select {
	case err := <-retireDone:
		t.Fatalf("retirement completed before factory exited: %v", err)
	default:
	}
	close(factory.release)
	require.NoError(t, <-retireDone)
	require.ErrorIs(t, <-refreshDone, ErrProviderUnavailable)
}

func TestPoolManagerRetireBoundedTimeoutDoesNotPublishLiveRefreshCompletion(t *testing.T) {
	now := time.Now().UTC()
	resolver := &retirementExitResolver{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
		release:  make(chan struct{}),
	}
	manager, err := NewPoolManager(PoolManagerConfig{
		Binding: validTargetBinding(t), Resolver: resolver, Factory: &recordingPoolFactory{},
		Store: &recordingBindingStore{}, Audit: noOpRotationAudit{},
		Now: func() time.Time { return now }, StaleAfter: time.Hour,
	})
	require.NoError(t, err)
	refreshDone := make(chan error, 1)
	go func() { refreshDone <- manager.RefreshNow(context.Background()) }()
	<-resolver.started

	err = manager.RetireBounded(context.Background(), time.Now().Add(30*time.Millisecond))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	select {
	case <-manager.retireDone:
		t.Fatal("retirement published completion while resolver remained live")
	default:
	}

	close(resolver.release)
	require.ErrorIs(t, <-refreshDone, ErrProviderUnavailable)
	select {
	case <-manager.retireDone:
	case <-time.After(time.Second):
		t.Fatal("retirement did not complete after the refresh exited")
	}
}

func newRetirementTestManager(t *testing.T, now time.Time, factory *recordingPoolFactory) *PoolManager {
	t.Helper()
	manager, err := NewPoolManager(PoolManagerConfig{
		Binding:  validTargetBinding(t),
		Resolver: &sequenceResolver{snapshots: []CredentialSnapshot{testSnapshot(t, "version-retirement", now)}},
		Factory:  factory, Store: &recordingBindingStore{}, Audit: noOpRotationAudit{},
		Now: func() time.Time { return now }, StaleAfter: time.Hour,
	})
	require.NoError(t, err)
	return manager
}

type retirementBlockingResolver struct {
	once     sync.Once
	started  chan struct{}
	release  chan struct{}
	snapshot CredentialSnapshot
}

type retirementExitResolver struct {
	started      chan struct{}
	canceled     chan struct{}
	release      chan struct{}
	startedOnce  sync.Once
	canceledOnce sync.Once
}

func (resolver *retirementExitResolver) Resolve(ctx context.Context, _ CredentialReference) (CredentialSnapshot, error) {
	resolver.startedOnce.Do(func() { close(resolver.started) })
	select {
	case <-ctx.Done():
		resolver.canceledOnce.Do(func() { close(resolver.canceled) })
		<-resolver.release
		return CredentialSnapshot{}, ctx.Err()
	case <-resolver.release:
		return CredentialSnapshot{}, ErrProviderUnavailable
	}
}

type retirementExitFactory struct {
	started      chan struct{}
	canceled     chan struct{}
	release      chan struct{}
	startedOnce  sync.Once
	canceledOnce sync.Once
}

func (factory *retirementExitFactory) Prepare(ctx context.Context, _ TargetBinding, _ CredentialSnapshot) (RuntimePool, error) {
	factory.startedOnce.Do(func() { close(factory.started) })
	select {
	case <-ctx.Done():
		factory.canceledOnce.Do(func() { close(factory.canceled) })
		<-factory.release
		return nil, ctx.Err()
	case <-factory.release:
		return nil, ErrProviderUnavailable
	}
}

type retirementBlockingErrorResolver struct {
	started chan struct{}
	release chan struct{}
}

func (resolver *retirementBlockingErrorResolver) Resolve(ctx context.Context, _ CredentialReference) (CredentialSnapshot, error) {
	close(resolver.started)
	select {
	case <-ctx.Done():
		return CredentialSnapshot{}, ctx.Err()
	case <-resolver.release:
		return CredentialSnapshot{}, ErrProviderUnavailable
	}
}

type retirementCountingFactory struct {
	pool *retirementCountingPool
}

func (factory *retirementCountingFactory) Prepare(context.Context, TargetBinding, CredentialSnapshot) (RuntimePool, error) {
	if factory.pool == nil {
		factory.pool = &retirementCountingPool{}
	}
	return factory.pool, nil
}

type retirementCountingPool struct {
	mu         sync.Mutex
	closed     bool
	closeCount int
	closeErr   error
}

type retirementBlockingStore struct {
	recordingBindingStore
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
}

type retirementFailingStore struct {
	recordingBindingStore
	calls   int
	failAt  int
	failure error
}

func (store *retirementFailingStore) Save(ctx context.Context, binding TargetBinding, expectedRevision int64) (TargetBinding, error) {
	store.calls++
	if store.calls == store.failAt {
		return TargetBinding{}, store.failure
	}
	return store.recordingBindingStore.Save(ctx, binding, expectedRevision)
}

func (store *retirementBlockingStore) Save(ctx context.Context, binding TargetBinding, expectedRevision int64) (TargetBinding, error) {
	store.mu.Lock()
	store.calls++
	call := store.calls
	store.mu.Unlock()
	if call == 2 {
		close(store.started)
		select {
		case <-ctx.Done():
			return TargetBinding{}, ctx.Err()
		case <-store.release:
		}
	}
	return store.recordingBindingStore.Save(ctx, binding, expectedRevision)
}

func (pool *retirementCountingPool) HealthCheck(context.Context) error { return nil }

func (pool *retirementCountingPool) Close() error {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	pool.closed = true
	pool.closeCount++
	return pool.closeErr
}

func (resolver *retirementBlockingResolver) Resolve(ctx context.Context, _ CredentialReference) (CredentialSnapshot, error) {
	resolver.once.Do(func() { close(resolver.started) })
	select {
	case <-ctx.Done():
		return CredentialSnapshot{}, ctx.Err()
	case <-resolver.release:
		return resolver.snapshot, nil
	}
}
