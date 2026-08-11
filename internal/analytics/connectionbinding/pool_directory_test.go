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
	second.LogicalConnectionID = "reporting"
	resolvers := map[string]*blockingResolver{}
	concurrency := &resolverConcurrency{}
	directory, err := NewPoolDirectory(PoolDirectoryConfig{
		Build: func(current TargetBinding) (*PoolManager, error) {
			resolver := &blockingResolver{
				started: make(chan struct{}), release: make(chan struct{}), concurrency: concurrency,
			}
			resolvers[current.ID] = resolver
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
			Actor: "principal:operator-1", Operation: RefreshTest,
		})
	}()
	<-resolvers[binding.ID].started

	start := time.Now()
	err = other.Refresh(context.Background(), RefreshRequest{
		Actor: "principal:operator-1", Operation: RefreshTest,
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
		Actor: "principal:operator-1", Operation: RefreshTest,
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
