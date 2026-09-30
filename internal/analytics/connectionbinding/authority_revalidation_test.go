package connectionbinding

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/flidai/leapview/internal/analytics/sourcework"
	"github.com/stretchr/testify/require"
)

var errPoolAuthorityRevoked = errors.New("test caller authority revoked")

type poolAuthorityCheck struct{ revoked atomic.Bool }

func (check *poolAuthorityCheck) Context(ctx context.Context) context.Context {
	return sourcework.WithRevalidator(ctx, func(context.Context) error {
		if check.revoked.Load() {
			return errPoolAuthorityRevoked
		}
		return nil
	})
}

func TestPoolManagerRevalidatesAfterSourceWorkAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		binding := validTargetBinding(t)
		gate := &sourcework.Gate{}
		pause, err := gate.Pause()
		require.NoError(t, err)
		resolver := &sequenceResolver{snapshots: []CredentialSnapshot{testSnapshot(t, "provider-v1", binding.UpdatedAt)}}
		factory := &recordingPoolFactory{}
		store := &recordingBindingStore{}
		audit := &recordingRotationAudit{}
		manager, err := NewPoolManager(PoolManagerConfig{
			Binding: binding, Resolver: resolver, Factory: factory, Store: store,
			Audit: audit, Now: func() time.Time { return binding.UpdatedAt }, StaleAfter: time.Hour, SourceWork: gate,
		})
		require.NoError(t, err)
		check := &poolAuthorityCheck{}
		done := make(chan error, 1)
		go func() {
			done <- manager.Refresh(check.Context(context.Background()), RefreshRequest{Actor: "runtime:target-1", Operation: RefreshRuntime})
		}()
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("refresh passed paused source-work gate: %v", err)
		default:
		}
		check.revoked.Store(true)
		require.NoError(t, pause.Resume())
		require.ErrorIs(t, <-done, errPoolAuthorityRevoked)
		require.Zero(t, resolverCallCount(resolver))
		require.Empty(t, factoryPools(factory))
		require.Empty(t, store.binding.ID)
		require.Empty(t, audit.events)
		current := manager.Evidence()
		require.Equal(t, HealthPending, current.Health)
		require.Empty(t, current.ValidatedVersion)
	})
}

func TestPoolManagerRevalidatesAfterResolverBeforePreparingPool(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		binding := validTargetBinding(t)
		resolver := &blockingAuthorityResolver{
			started: make(chan struct{}), release: make(chan struct{}),
			snapshot: testSnapshot(t, "provider-v1", binding.UpdatedAt),
		}
		factory := &recordingPoolFactory{}
		store := &recordingBindingStore{}
		audit := &recordingRotationAudit{}
		manager, err := NewPoolManager(PoolManagerConfig{
			Binding: binding, Resolver: resolver, Factory: factory, Store: store,
			Audit: audit, Now: func() time.Time { return binding.UpdatedAt }, StaleAfter: time.Hour,
		})
		require.NoError(t, err)
		check := &poolAuthorityCheck{}
		done := make(chan error, 1)
		go func() {
			done <- manager.Refresh(check.Context(context.Background()), RefreshRequest{Actor: "runtime:target-1", Operation: RefreshRuntime})
		}()
		<-resolver.started
		check.revoked.Store(true)
		close(resolver.release)
		require.ErrorIs(t, <-done, errPoolAuthorityRevoked)
		require.Empty(t, factoryPools(factory), "revocation after credential resolution must prevent pool preparation")
		require.Empty(t, store.binding.ID)
		require.Empty(t, audit.events)
		require.Equal(t, HealthPending, manager.Evidence().Health)
		require.ErrorIs(t, resolver.snapshot.Use(func(map[string]string) error { return nil }), ErrInvalidBinding,
			"the resolved credential snapshot must be wiped when revalidation denies use")
	})
}

func TestPoolManagerRevalidatesBeforeDegradingAfterResolverFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		binding := validTargetBinding(t)
		resolver := &blockingAuthorityResolver{
			started: make(chan struct{}), release: make(chan struct{}), resolveErr: ErrProviderUnavailable,
		}
		factory := &recordingPoolFactory{}
		store := &recordingBindingStore{}
		audit := &recordingRotationAudit{}
		manager, err := NewPoolManager(PoolManagerConfig{
			Binding: binding, Resolver: resolver, Factory: factory, Store: store,
			Audit: audit, Now: func() time.Time { return binding.UpdatedAt }, StaleAfter: time.Hour,
		})
		require.NoError(t, err)
		check := &poolAuthorityCheck{}
		done := make(chan error, 1)
		go func() {
			done <- manager.Refresh(check.Context(context.Background()), RefreshRequest{Actor: "runtime:target-1", Operation: RefreshRuntime})
		}()
		<-resolver.started
		check.revoked.Store(true)
		close(resolver.release)
		require.ErrorIs(t, <-done, errPoolAuthorityRevoked)
		require.Empty(t, factoryPools(factory))
		require.Empty(t, store.binding.ID)
		require.Empty(t, audit.events)
		require.Equal(t, HealthPending, manager.Evidence().Health)
	})
}

func TestPoolManagerRevalidatesAfterPoolPreparationAndHealth(t *testing.T) {
	for _, stage := range []string{"prepare", "prepare_error", "health"} {
		t.Run(stage, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				binding := validTargetBinding(t)
				pool := &blockingAuthorityPool{healthStarted: make(chan struct{}), healthRelease: make(chan struct{})}
				factory := &blockingAuthorityPoolFactory{pool: pool, prepareStarted: make(chan struct{}), prepareRelease: make(chan struct{})}
				if stage == "health" {
					factory.blockPrepare = false
					pool.blockHealth = true
					pool.healthErr = errors.New("health diagnostic must not replace caller revocation")
				} else {
					factory.blockPrepare = true
					if stage == "prepare_error" {
						factory.prepareErr = ErrProviderUnavailable
					}
				}
				store := &recordingBindingStore{}
				audit := &recordingRotationAudit{}
				manager, err := NewPoolManager(PoolManagerConfig{
					Binding: binding, Resolver: &sequenceResolver{snapshots: []CredentialSnapshot{testSnapshot(t, "provider-v1", binding.UpdatedAt)}},
					Factory: factory, Store: store, Audit: audit, Now: func() time.Time { return binding.UpdatedAt }, StaleAfter: time.Hour,
				})
				require.NoError(t, err)
				check := &poolAuthorityCheck{}
				done := make(chan error, 1)
				go func() {
					done <- manager.Refresh(check.Context(context.Background()), RefreshRequest{Actor: "runtime:target-1", Operation: RefreshRuntime})
				}()
				if stage == "prepare" || stage == "prepare_error" {
					<-factory.prepareStarted
					check.revoked.Store(true)
					close(factory.prepareRelease)
				} else {
					<-pool.healthStarted
					check.revoked.Store(true)
					close(pool.healthRelease)
				}
				require.ErrorIs(t, <-done, errPoolAuthorityRevoked)
				require.Equal(t, 1, pool.closeCount)
				require.Empty(t, store.binding.ID)
				require.Empty(t, audit.events)
				require.Equal(t, HealthPending, manager.Evidence().Health)
			})
		})
	}
}

func TestPoolDirectoryDeniesHotLeaseAfterAuthorityRevocation(t *testing.T) {
	binding := validTargetBinding(t)
	resolver := &sequenceResolver{snapshots: []CredentialSnapshot{testSnapshot(t, "provider-v1", binding.UpdatedAt)}}
	factory := &recordingPoolFactory{}
	store := &recordingBindingStore{}
	directory, err := NewPoolDirectory(PoolDirectoryConfig{
		Build: func(current TargetBinding) (*PoolManager, error) {
			return NewPoolManager(PoolManagerConfig{
				Binding: current, Resolver: resolver, Factory: factory, Store: store,
				Audit: noOpRotationAudit{}, Now: func() time.Time { return binding.UpdatedAt }, StaleAfter: time.Hour,
			})
		},
		RefreshTimeout: time.Second, MaxConcurrent: 1,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = directory.Close() })
	first, err := directory.AcquireValidated(context.Background(), binding, "candidate:cand_1")
	require.NoError(t, err)
	first.Release()
	binding = store.binding
	manager := directory.pools[binding.ID].manager
	t.Run("already revoked before lookup", func(t *testing.T) {
		check := &poolAuthorityCheck{}
		check.revoked.Store(true)
		lease, err := directory.AcquireValidated(check.Context(context.Background()), binding, "candidate:cand_2")
		if lease != nil {
			lease.Release()
		}
		require.ErrorIs(t, err, errPoolAuthorityRevoked)
		require.Nil(t, lease)
		require.Zero(t, manager.active.leases)
	})
	t.Run("revoked after first check", func(t *testing.T) {
		var checks atomic.Int32
		ctx := sourcework.WithRevalidator(context.Background(), func(context.Context) error {
			if checks.Add(1) == 1 {
				return nil
			}
			return errPoolAuthorityRevoked
		})
		lease, err := directory.AcquireValidated(ctx, binding, "candidate:cand_3")
		if lease != nil {
			lease.Release()
		}
		require.ErrorIs(t, err, errPoolAuthorityRevoked)
		require.Nil(t, lease)
		require.Equal(t, int32(2), checks.Load(), "authority must be checked before Pool and after leasing the cached pool")
		require.Zero(t, manager.active.leases, "denied cached lease must be released")
		authorized, err := directory.AcquireValidated(context.Background(), binding, "candidate:cand_4")
		require.NoError(t, err, "the cached pool remains usable after a rejected lease")
		require.Same(t, first.Pool(), authorized.Pool())
		authorized.Release()
	})
}

func TestPoolManagerCoalescedRefreshCallerCannotInheritLeaderAuthority(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		binding := validTargetBinding(t)
		resolver := &blockingAuthorityResolver{
			started: make(chan struct{}), release: make(chan struct{}),
			snapshot: testSnapshot(t, "provider-v1", binding.UpdatedAt),
		}
		manager, err := NewPoolManager(PoolManagerConfig{
			Binding: binding, Resolver: resolver, Factory: &recordingPoolFactory{}, Store: &recordingBindingStore{},
			Audit: noOpRotationAudit{}, Now: func() time.Time { return binding.UpdatedAt }, StaleAfter: time.Hour,
		})
		require.NoError(t, err)
		leaderDone := make(chan error, 1)
		go func() {
			leaderDone <- manager.Refresh(context.Background(), RefreshRequest{Actor: "principal:leader", Operation: RefreshRequested})
		}()
		<-resolver.started

		waiterAuthority := &poolAuthorityCheck{}
		waiterDone := make(chan error, 1)
		go func() {
			waiterDone <- manager.Refresh(waiterAuthority.Context(context.Background()), RefreshRequest{Actor: "principal:waiter", Operation: RefreshRequested})
		}()
		synctest.Wait()
		select {
		case err := <-waiterDone:
			t.Fatalf("coalesced refresh returned before leader: %v", err)
		default:
		}
		waiterAuthority.revoked.Store(true)
		close(resolver.release)
		require.NoError(t, <-leaderDone)
		require.ErrorIs(t, <-waiterDone, errPoolAuthorityRevoked)
		require.Equal(t, 1, resolver.calls)
	})
}

type blockingAuthorityResolver struct {
	started    chan struct{}
	release    chan struct{}
	snapshot   CredentialSnapshot
	resolveErr error
	mu         sync.Mutex
	calls      int
}

func (resolver *blockingAuthorityResolver) Resolve(ctx context.Context, _ CredentialReference) (CredentialSnapshot, error) {
	resolver.mu.Lock()
	resolver.calls++
	resolver.mu.Unlock()
	close(resolver.started)
	select {
	case <-resolver.release:
		return resolver.snapshot, resolver.resolveErr
	case <-ctx.Done():
		return CredentialSnapshot{}, ctx.Err()
	}
}

type blockingAuthorityPoolFactory struct {
	pool           *blockingAuthorityPool
	prepareStarted chan struct{}
	prepareRelease chan struct{}
	blockPrepare   bool
	prepareErr     error
}

func (factory *blockingAuthorityPoolFactory) Prepare(ctx context.Context, _ TargetBinding, _ CredentialSnapshot) (RuntimePool, error) {
	if factory.blockPrepare {
		close(factory.prepareStarted)
		select {
		case <-factory.prepareRelease:
		case <-ctx.Done():
			return factory.pool, ctx.Err()
		}
	}
	return factory.pool, factory.prepareErr
}

type blockingAuthorityPool struct {
	healthStarted chan struct{}
	healthRelease chan struct{}
	healthErr     error
	blockHealth   bool
	closeCount    int
}

func (pool *blockingAuthorityPool) HealthCheck(ctx context.Context) error {
	if pool.blockHealth {
		close(pool.healthStarted)
		select {
		case <-pool.healthRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return pool.healthErr
}

func (pool *blockingAuthorityPool) Close() error {
	pool.closeCount++
	return nil
}

func resolverCallCount(resolver *sequenceResolver) int {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	return resolver.calls
}

func factoryPools(factory *recordingPoolFactory) []*recordingRuntimePool {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return append([]*recordingRuntimePool(nil), factory.pools...)
}
