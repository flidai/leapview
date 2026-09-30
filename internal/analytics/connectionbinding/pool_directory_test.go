package connectionbinding

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPoolDirectoryCreatesOneManagerPerBindingRevision(t *testing.T) {
	binding := validTargetBinding(t)
	var builds int
	var managers []*PoolManager
	directory, err := NewPoolDirectory(PoolDirectoryConfig{
		Build: func(current TargetBinding) (*PoolManager, error) {
			builds++
			manager, err := NewPoolManager(PoolManagerConfig{
				Binding: current,
				Resolver: &sequenceResolver{
					snapshots: []CredentialSnapshot{testSnapshot(t, "version-1", current.UpdatedAt)},
				},
				Factory:    &recordingPoolFactory{},
				Store:      &recordingBindingStore{},
				Audit:      noOpRotationAudit{},
				Now:        func() time.Time { return current.UpdatedAt },
				StaleAfter: time.Hour,
			})
			if err == nil {
				managers = append(managers, manager)
			}
			return manager, err
		},
		RefreshTimeout: time.Second,
		MaxConcurrent:  1,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = directory.Close() })

	first, err := directory.Pool(binding)
	require.NoError(t, err)
	same, err := directory.Pool(binding)
	require.NoError(t, err)
	if first != same || builds != 1 {
		t.Fatalf("same revision returned pools %p and %p after %d builds", first, same, builds)
	}

	updated, err := binding.UpdateConfiguration(TargetBindingConfiguration{
		ConnectorKind: binding.ConnectorKind, AuthenticationMode: binding.AuthenticationMode,
		Endpoint:            EndpointConfig{Host: "warehouse-next.internal", Port: binding.Endpoint.Port},
		CredentialReference: binding.CredentialReference,
	}, binding.UpdatedAt.Add(time.Minute))
	require.NoError(t, err)
	replacement, err := directory.Pool(updated)
	require.NoError(t, err)
	if replacement == first || builds != 2 {
		t.Fatalf("new revision returned pool %p after %d builds; old=%p", replacement, builds, first)
	}
	if _, err := managers[0].Lease(); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("retired manager lease error = %v", err)
	}
}

func TestPoolDirectoryBoundsRefreshConcurrencyAndTimeout(t *testing.T) {
	binding := validTargetBinding(t)
	second := binding
	second.ID = "binding_reporting"
	second.ConnectionID = "reporting"
	resolvers := map[string]*blockingResolver{}
	concurrency := &resolverConcurrency{}
	directory, err := NewPoolDirectory(PoolDirectoryConfig{
		Build: func(current TargetBinding) (*PoolManager, error) {
			resolver := &blockingResolver{
				started: make(chan struct{}), release: make(chan struct{}), concurrency: concurrency,
			}
			resolvers[current.ID.String()] = resolver
			return NewPoolManager(PoolManagerConfig{
				Binding: current, Resolver: resolver, Factory: &recordingPoolFactory{},
				Store: &recordingBindingStore{}, Now: time.Now, StaleAfter: time.Hour,
				Audit: noOpRotationAudit{},
			})
		},
		RefreshTimeout: 40 * time.Millisecond,
		MaxConcurrent:  1,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = directory.Close() })
	first, err := directory.Pool(binding)
	require.NoError(t, err)
	other, err := directory.Pool(second)
	require.NoError(t, err)

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- first.Refresh(context.Background(), RefreshRequest{
			Actor: "principal:operator-1", Operation: RefreshRequested,
		})
	}()
	<-resolvers[binding.ID.String()].started

	start := time.Now()
	err = other.Refresh(context.Background(), RefreshRequest{
		Actor: "principal:operator-1", Operation: RefreshRequested,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued refresh error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("bounded refresh took %s", elapsed)
	}
	if err := <-firstDone; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("active refresh error = %v", err)
	}
	if concurrency.max != 1 {
		t.Fatalf("maximum concurrent resolver calls = %d", concurrency.max)
	}
}

func TestPoolDirectoryCloseRetiresManagersAndRejectsNewPools(t *testing.T) {
	binding := validTargetBinding(t)
	factory := &recordingPoolFactory{}
	directory, err := NewPoolDirectory(PoolDirectoryConfig{
		Build: func(current TargetBinding) (*PoolManager, error) {
			return NewPoolManager(PoolManagerConfig{
				Binding: current,
				Resolver: &sequenceResolver{
					snapshots: []CredentialSnapshot{testSnapshot(t, "version-1", current.UpdatedAt)},
				},
				Factory: factory, Store: &recordingBindingStore{},
				Audit: noOpRotationAudit{},
				Now:   func() time.Time { return current.UpdatedAt }, StaleAfter: time.Hour,
			})
		},
		RefreshTimeout: time.Second,
		MaxConcurrent:  1,
	})
	require.NoError(t, err)
	pool, err := directory.Pool(binding)
	require.NoError(t, err)
	if err := pool.Refresh(context.Background(), RefreshRequest{
		Actor: "principal:operator-1", Operation: RefreshRequested,
	}); err != nil {
		t.Fatal(err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	if len(factory.pools) != 1 || !factory.pools[0].closed {
		t.Fatalf("closed pools = %#v", factory.pools)
	}
	if _, err := directory.Pool(binding); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("Pool() after Close error = %v", err)
	}
}

func TestPoolDirectoryCloseFencesAllManagersAndRetainsCloseResultForRetry(t *testing.T) {
	binding := validTargetBinding(t)
	secondBinding := binding
	secondBinding.ID = "binding_reporting"
	secondBinding.ConnectionID = "reporting"
	closeErr := errors.New("runtime close failed")
	pools := map[string]*retirementBlockingClosePool{
		binding.ID.String():       newRetirementBlockingClosePool(closeErr),
		secondBinding.ID.String(): newRetirementBlockingClosePool(nil),
	}
	for _, pool := range pools {
		t.Cleanup(pool.allowClose)
	}
	managers := map[string]*PoolManager{}
	directory, err := NewPoolDirectory(PoolDirectoryConfig{
		Build: func(current TargetBinding) (*PoolManager, error) {
			manager, err := NewPoolManager(PoolManagerConfig{
				Binding: current,
				Resolver: &sequenceResolver{snapshots: []CredentialSnapshot{
					testSnapshot(t, "version-1", current.UpdatedAt),
				}},
				Factory: retirementFixedPoolFactory{pool: pools[current.ID.String()]},
				Store:   &recordingBindingStore{}, Audit: noOpRotationAudit{},
				Now: func() time.Time { return current.UpdatedAt }, StaleAfter: time.Hour,
			})
			if err == nil {
				managers[current.ID.String()] = manager
			}
			return manager, err
		},
		RefreshTimeout: 45 * time.Millisecond,
		MaxConcurrent:  2,
	})
	require.NoError(t, err)
	for _, current := range []TargetBinding{binding, secondBinding} {
		pool, err := directory.Pool(current)
		require.NoError(t, err)
		require.NoError(t, pool.Refresh(context.Background(), RefreshRequest{
			Actor: "principal:operator-1", Operation: RefreshRequested,
		}))
	}

	firstClose := make(chan error, 1)
	go func() { firstClose <- directory.Close() }()
	select {
	case <-pools[binding.ID.String()].started:
	case <-pools[secondBinding.ID.String()].started:
	case <-time.After(time.Second):
		t.Fatal("directory close did not start physical cleanup")
	}
	for id, manager := range managers {
		manager.mu.Lock()
		retired := manager.retired
		manager.mu.Unlock()
		if !retired {
			t.Fatalf("manager %s was not fenced before the first close wait", id)
		}
	}

	secondClose := make(chan error, 1)
	go func() { secondClose <- directory.Close() }()
	for _, pool := range pools {
		select {
		case <-pool.started:
		case <-time.After(time.Second):
			t.Fatal("directory close did not request every manager close")
		}
	}
	for _, done := range []<-chan error{firstClose, secondClose} {
		select {
		case err := <-done:
			require.ErrorIs(t, err, context.DeadlineExceeded)
		case <-time.After(time.Second):
			t.Fatal("bounded directory close did not return at its deadline")
		}
	}
	for _, manager := range managers {
		select {
		case <-manager.retireDone:
			t.Fatal("directory close published manager completion before physical close returned")
		default:
		}
	}

	pools[binding.ID.String()].allowClose()
	pools[secondBinding.ID.String()].allowClose()
	err = directory.Close()
	require.ErrorIs(t, err, closeErr)
	if len(directory.pools) != 2 {
		t.Fatalf("retained pool managers = %d, want 2 for close retries", len(directory.pools))
	}
	if _, err := directory.Pool(binding); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("Pool() after Close error = %v", err)
	}
}

func TestBoundedRefreshReturnsWhileDiscardedPoolCleanupContinues(t *testing.T) {
	binding := validTargetBinding(t)
	closeErr := errors.New("secret-bearing driver close failure")
	pool := newRetirementBlockingClosePool(closeErr)
	t.Cleanup(pool.allowClose)
	pool.healthErr = errors.New("candidate health check failed")
	var manager *PoolManager
	directory, err := NewPoolDirectory(PoolDirectoryConfig{
		Build: func(current TargetBinding) (*PoolManager, error) {
			created, buildErr := NewPoolManager(PoolManagerConfig{
				Binding: current,
				Resolver: &sequenceResolver{snapshots: []CredentialSnapshot{
					testSnapshot(t, "version-1", current.UpdatedAt),
				}},
				Factory: retirementFixedPoolFactory{pool: pool},
				Store:   &recordingBindingStore{}, Audit: noOpRotationAudit{},
				Now: func() time.Time { return current.UpdatedAt }, StaleAfter: time.Hour,
			})
			manager = created
			return created, buildErr
		},
		RefreshTimeout: 40 * time.Millisecond,
		MaxConcurrent:  1,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = directory.Close() })

	administration, err := directory.Pool(binding)
	require.NoError(t, err)
	started := time.Now()
	err = administration.Refresh(context.Background(), RefreshRequest{
		Actor: "principal:operator-1", Operation: RefreshRequested,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded Refresh() error = %v, want deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Fatalf("bounded Refresh() waited for physical candidate cleanup: %s", elapsed)
	}
	select {
	case <-pool.started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start candidate cleanup")
	}

	err = manager.RetireBounded(context.Background(), time.Now().Add(35*time.Millisecond))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	select {
	case <-manager.retireDone:
		t.Fatal("retirement completed while discarded candidate cleanup was still running")
	default:
	}

	pool.allowClose()
	waitForManagerRetirement(t, manager)
	if err := manager.RetireBounded(context.Background(), time.Now().Add(time.Second)); !errors.Is(err, closeErr) {
		t.Fatalf("retirement result = %v, want retained cleanup error %v", err, closeErr)
	}
}

func TestPoolDirectoryAcquiresOnlyValidatedGenerationsAndReusesThem(t *testing.T) {
	binding := validTargetBinding(t)
	now := binding.UpdatedAt.Add(time.Minute)
	resolver := &sequenceResolver{
		snapshots: []CredentialSnapshot{testSnapshot(t, "provider-v2", now)},
	}
	factory := &recordingPoolFactory{}
	store := &recordingBindingStore{}
	directory, err := NewPoolDirectory(PoolDirectoryConfig{
		Build: func(current TargetBinding) (*PoolManager, error) {
			return NewPoolManager(PoolManagerConfig{
				Binding: current, Resolver: resolver, Factory: factory, Store: store,
				Audit: noOpRotationAudit{},
				Now:   func() time.Time { return now }, StaleAfter: time.Hour,
			})
		},
		RefreshTimeout: time.Second,
		MaxConcurrent:  1,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = directory.Close() })

	first, err := directory.AcquireValidated(
		t.Context(),
		binding,
		"candidate:cand_1",
	)
	require.NoError(t, err)
	if first.Pool() == nil {
		t.Fatal("validated lease has no runtime pool")
	}
	evidence := first.Evidence()
	if evidence.BindingID != binding.ID || evidence.BindingRevision != binding.Revision+1 ||
		evidence.ValidatedVersion != "provider-v2" || evidence.Health != HealthHealthy {
		t.Fatalf("validated evidence = %#v", evidence)
	}

	second, err := directory.AcquireValidated(
		t.Context(),
		store.binding,
		"candidate:cand_1",
	)
	require.NoError(t, err)
	if resolver.calls != 1 || len(factory.pools) != 1 || second.Pool() != first.Pool() {
		t.Fatalf(
			"pool reuse resolver=%d pools=%d first=%p second=%p",
			resolver.calls,
			len(factory.pools),
			first.Pool(),
			second.Pool(),
		)
	}

	first.Release()
	second.Release()
}

func TestPoolDirectoryValidatedAcquireIsBoundedAndFailsClosed(t *testing.T) {
	binding := validTargetBinding(t)
	resolver := &blockingResolver{
		started: make(chan struct{}), release: make(chan struct{}),
	}
	directory, err := NewPoolDirectory(PoolDirectoryConfig{
		Build: func(current TargetBinding) (*PoolManager, error) {
			return NewPoolManager(PoolManagerConfig{
				Binding: current, Resolver: resolver, Factory: &recordingPoolFactory{},
				Store: &recordingBindingStore{}, Now: time.Now, StaleAfter: time.Hour,
				Audit: noOpRotationAudit{},
			})
		},
		RefreshTimeout: 40 * time.Millisecond,
		MaxConcurrent:  1,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = directory.Close() })

	started := time.Now()
	_, err = directory.AcquireValidated(t.Context(), binding, "candidate:cand_1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AcquireValidated() error = %v, want bounded deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("bounded candidate acquire took %s", elapsed)
	}
	if len(directory.pools) != 1 {
		t.Fatalf("prepared pool managers = %d, want reusable degraded manager", len(directory.pools))
	}
}

type blockingResolver struct {
	once        sync.Once
	started     chan struct{}
	release     chan struct{}
	calls       int
	concurrency *resolverConcurrency
}

func (resolver *blockingResolver) Resolve(ctx context.Context, _ CredentialReference) (CredentialSnapshot, error) {
	resolver.calls++
	if resolver.concurrency != nil {
		resolver.concurrency.enter()
		defer resolver.concurrency.leave()
	}
	resolver.once.Do(func() { close(resolver.started) })
	select {
	case <-ctx.Done():
		return CredentialSnapshot{}, ctx.Err()
	case <-resolver.release:
		return CredentialSnapshot{}, ErrProviderUnavailable
	}
}

type resolverConcurrency struct {
	mu     sync.Mutex
	active int
	max    int
}

func (concurrency *resolverConcurrency) enter() {
	concurrency.mu.Lock()
	defer concurrency.mu.Unlock()
	concurrency.active++
	if concurrency.active > concurrency.max {
		concurrency.max = concurrency.active
	}
}

func (concurrency *resolverConcurrency) leave() {
	concurrency.mu.Lock()
	concurrency.active--
	concurrency.mu.Unlock()
}
