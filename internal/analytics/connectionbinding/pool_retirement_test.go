package connectionbinding

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPoolDirectoryRetireBindingWaitsForCapturedPoolCloseAndRetries(t *testing.T) {
	binding := validTargetBinding(t)
	closeErr := errors.New("runtime close failed")
	pool := newRetirementBlockingClosePool(closeErr)
	directory, _, manager, expected := newPoolRetirementFixture(t, binding, pool, nil, 40*time.Millisecond)
	t.Cleanup(pool.allowClose)
	retirement, err := directory.RetireBinding(expected)
	require.NoError(t, err)
	firstWait := make(chan error, 1)
	go func() { firstWait <- retirement.Wait(context.Background(), time.Now().Add(40*time.Millisecond)) }()
	select {
	case <-pool.started:
	case <-time.After(time.Second):
		t.Fatal("retirement did not request the captured pool close")
	}
	if err := <-firstWait; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first retirement wait error = %v, want deadline while close is blocked", err)
	}

	select {
	case <-manager.retireDone:
		t.Fatal("manager published retirement before RuntimePool.Close returned")
	default:
	}
	pool.allowClose()
	if err := retirement.Wait(context.Background(), time.Now().Add(time.Second)); !errors.Is(err, closeErr) {
		t.Fatalf("retry retirement wait error = %v, want sticky close error %v", err, closeErr)
	}
	if err := retirement.Wait(context.Background(), time.Now().Add(time.Second)); !errors.Is(err, closeErr) {
		t.Fatalf("repeated retirement wait error = %v, want sticky close error %v", err, closeErr)
	}
	if calls := pool.calls.Load(); calls != 1 {
		t.Fatalf("captured pool Close calls = %d, want 1", calls)
	}
}

func newPoolRetirementFixture(
	t *testing.T,
	binding TargetBinding,
	pool *retirementBlockingClosePool,
	resolver CredentialResolver,
	timeout time.Duration,
) (*PoolDirectory, AdministrationPool, *PoolManager, TargetBinding) {
	t.Helper()
	if resolver == nil {
		resolver = &sequenceResolver{snapshots: []CredentialSnapshot{testSnapshot(t, "retirement-version", binding.UpdatedAt)}}
	}
	directory, err := NewPoolDirectory(PoolDirectoryConfig{
		Build: func(current TargetBinding) (*PoolManager, error) {
			return NewPoolManager(PoolManagerConfig{
				Binding: current, Resolver: resolver, Factory: retirementFixedPoolFactory{pool: pool},
				Store: &recordingBindingStore{}, Audit: noOpRotationAudit{},
				Now: func() time.Time { return current.UpdatedAt }, StaleAfter: time.Hour,
			})
		},
		RefreshTimeout: timeout, MaxConcurrent: 2,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = directory.Close() })
	administration, err := directory.Pool(binding)
	require.NoError(t, err)
	require.NoError(t, administration.Refresh(context.Background(), RefreshRequest{
		Actor: "principal:operator-1", Operation: RefreshRequested,
	}))
	bounded, ok := administration.(*boundedAdministrationPool)
	require.True(t, ok)
	manager := bounded.manager
	manager.mu.Lock()
	expected := manager.binding
	manager.mu.Unlock()
	return directory, administration, manager, expected
}

func TestPoolDirectoryRetireBindingRequiresExactCurrentTuple(t *testing.T) {
	binding := validTargetBinding(t)
	pool := newRetirementBlockingClosePool(nil)
	directory, _, manager, expected := newPoolRetirementFixture(t, binding, pool, nil, time.Second)
	t.Cleanup(pool.allowClose)
	var nilRetirement *PoolRetirement
	if err := nilRetirement.Wait(context.Background(), time.Now().Add(time.Second)); !errors.Is(err, ErrInvalidBinding) {
		t.Fatalf("nil retirement handle wait = %v, want invalid binding", err)
	}

	var nilDirectory *PoolDirectory
	if _, err := nilDirectory.RetireBinding(expected); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("nil directory retirement error = %v, want provider unavailable", err)
	}
	if _, err := directory.RetireBinding(TargetBinding{}); !errors.Is(err, ErrInvalidBinding) {
		t.Fatalf("invalid binding retirement error = %v, want invalid binding", err)
	}
	missing := expected
	missing.ID = BindingID("binding_missing")
	if _, err := directory.RetireBinding(missing); !errors.Is(err, ErrBindingNotFound) {
		t.Fatalf("missing binding retirement error = %v, want binding not found", err)
	}

	mismatches := []struct {
		name   string
		mutate func(*TargetBinding)
	}{
		{name: "target", mutate: func(b *TargetBinding) { b.TargetID = "lvinst_other" }},
		{name: "connection", mutate: func(b *TargetBinding) { b.ConnectionID = "reporting" }},
		{name: "connector", mutate: func(b *TargetBinding) { b.ConnectorKind = "mysql" }},
		{name: "scope", mutate: func(b *TargetBinding) { b.Scope.ProjectID = "finance" }},
		{name: "endpoint config", mutate: func(b *TargetBinding) { b.Endpoint.Host = "other.internal" }},
		{name: "credential reference", mutate: func(b *TargetBinding) { b.CredentialReference.SecretKey = "other_warehouse" }},
		{name: "authentication mode", mutate: func(b *TargetBinding) {
			b.AuthenticationMode = AuthenticationNone
			b.CredentialReference = CredentialReference{}
		}},
		{name: "enabled state", mutate: func(b *TargetBinding) {
			b.Enabled, b.Health, b.ValidatedVersion, b.LastValidatedAt = false, HealthDisabled, "", time.Time{}
		}},
		{name: "revision", mutate: func(b *TargetBinding) { b.Revision++ }},
		{name: "validated version", mutate: func(b *TargetBinding) { b.ValidatedVersion = "provider-stale" }},
		{name: "health", mutate: func(b *TargetBinding) { b.Health, b.HealthReason = HealthDegraded, "TEST_FAILURE" }},
	}
	for _, test := range mismatches {
		t.Run(test.name, func(t *testing.T) {
			mismatch := expected
			test.mutate(&mismatch)
			require.NoError(t, mismatch.Validate())
			if _, err := directory.RetireBinding(mismatch); !errors.Is(err, ErrIncompatibleBinding) {
				t.Fatalf("mismatched binding retirement error = %v, want incompatible binding", err)
			}
			lease, err := manager.Lease()
			require.NoError(t, err, "mismatch must leave the current manager usable")
			lease.Release()
		})
	}

	// UpdatedAt and CreatedAt are bookkeeping timestamps, not execution identity.
	dateOnly := expected
	dateOnly.CreatedAt = dateOnly.CreatedAt.Add(-time.Second)
	dateOnly.UpdatedAt = dateOnly.UpdatedAt.Add(time.Second)
	require.NoError(t, dateOnly.Validate())
	pool.allowClose()
	retirement, err := directory.RetireBinding(dateOnly)
	require.NoError(t, err)
	if err := retirement.Wait(nil, time.Now().Add(time.Second)); !errors.Is(err, ErrInvalidBinding) {
		t.Fatalf("nil-context retirement wait = %v, want invalid binding", err)
	}
	if err := retirement.Wait(context.Background(), time.Time{}); !errors.Is(err, ErrInvalidBinding) {
		t.Fatalf("zero-deadline retirement wait = %v, want invalid binding", err)
	}
	if err := retirement.Wait(context.Background(), time.Now().Add(time.Second)); err != nil {
		t.Fatalf("matching binding with different bookkeeping timestamps failed to retire: %v", err)
	}
	require.NoError(t, directory.Close())
	if _, err := directory.RetireBinding(expected); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("closed directory retirement error = %v, want provider unavailable", err)
	}
}

func TestPoolRetirementHandleStaysWithOldManagerAfterReplacement(t *testing.T) {
	binding := validTargetBinding(t)
	oldPool := newRetirementBlockingClosePool(nil)
	newPool := newRetirementBlockingClosePool(nil)
	var builds atomic.Int32
	directory, err := NewPoolDirectory(PoolDirectoryConfig{
		Build: func(current TargetBinding) (*PoolManager, error) {
			pool := oldPool
			if builds.Add(1) > 1 {
				pool = newPool
			}
			return NewPoolManager(PoolManagerConfig{
				Binding: current,
				Resolver: &sequenceResolver{snapshots: []CredentialSnapshot{
					testSnapshot(t, "retirement-version", current.UpdatedAt),
				}},
				Factory: retirementFixedPoolFactory{pool: pool}, Store: &recordingBindingStore{},
				Audit: noOpRotationAudit{}, Now: func() time.Time { return current.UpdatedAt }, StaleAfter: time.Hour,
			})
		},
		RefreshTimeout: 40 * time.Millisecond,
		MaxConcurrent:  1,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = directory.Close() })
	t.Cleanup(oldPool.allowClose)
	t.Cleanup(newPool.allowClose)
	oldAdministration, err := directory.Pool(binding)
	require.NoError(t, err)
	require.NoError(t, oldAdministration.Refresh(context.Background(), RefreshRequest{
		Actor: "principal:operator-1", Operation: RefreshRequested,
	}))
	oldManager := oldAdministration.(*boundedAdministrationPool).manager
	oldManager.mu.Lock()
	expected := oldManager.binding
	oldManager.mu.Unlock()
	retirement, err := directory.RetireBinding(expected)
	require.NoError(t, err)
	firstWait := make(chan error, 1)
	go func() { firstWait <- retirement.Wait(context.Background(), time.Now().Add(30*time.Millisecond)) }()
	select {
	case <-oldPool.started:
	case <-time.After(time.Second):
		t.Fatal("captured old manager did not begin closing")
	}
	if err := <-firstWait; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first old manager wait = %v, want deadline", err)
	}
	oldPool.allowClose()
	if err := retirement.Wait(context.Background(), time.Now().Add(time.Second)); err != nil {
		t.Fatalf("old manager retirement retry: %v", err)
	}

	updated, err := expected.UpdateConfiguration(TargetBindingConfiguration{
		ConnectorKind: expected.ConnectorKind, AuthenticationMode: expected.AuthenticationMode,
		Endpoint: EndpointConfig{
			Host: "warehouse-replacement.internal", Port: expected.Endpoint.Port,
			Database: expected.Endpoint.Database, TLSMode: expected.Endpoint.TLSMode,
		},
		CredentialReference: expected.CredentialReference,
	}, expected.UpdatedAt.Add(time.Minute))
	require.NoError(t, err)
	newAdministration, err := directory.Pool(updated)
	require.NoError(t, err)
	require.NoError(t, newAdministration.Refresh(context.Background(), RefreshRequest{
		Actor: "principal:operator-1", Operation: RefreshRequested,
	}))
	newManager := newAdministration.(*boundedAdministrationPool).manager
	if err := retirement.Wait(context.Background(), time.Now().Add(time.Second)); err != nil {
		t.Fatalf("captured old handle changed after replacement: %v", err)
	}
	if _, err := oldManager.Lease(); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("old manager lease after replacement = %v, want provider unavailable", err)
	}
	if _, err := directory.RetireBinding(expected); !errors.Is(err, ErrIncompatibleBinding) {
		t.Fatalf("stale old binding retirement against replacement = %v, want incompatible binding", err)
	}
	lease, err := newManager.Lease()
	require.NoError(t, err, "old retirement handle/request must not fence the replacement")
	lease.Release()
}

type poolRetirementBlockingRefreshResolver struct {
	calls        atomic.Int32
	initial      CredentialSnapshot
	refreshStart chan struct{}
}

func (resolver *poolRetirementBlockingRefreshResolver) Resolve(ctx context.Context, _ CredentialReference) (CredentialSnapshot, error) {
	if resolver.calls.Add(1) == 1 {
		return resolver.initial, nil
	}
	close(resolver.refreshStart)
	<-ctx.Done()
	return CredentialSnapshot{}, ctx.Err()
}

func TestPoolRetirementWaitersAndDirectoryCloseJoinRefreshCleanup(t *testing.T) {
	binding := validTargetBinding(t)
	pool := newRetirementBlockingClosePool(nil)
	resolver := &poolRetirementBlockingRefreshResolver{
		initial: testSnapshot(t, "retirement-version", binding.UpdatedAt), refreshStart: make(chan struct{}),
	}
	directory, err := NewPoolDirectory(PoolDirectoryConfig{
		Build: func(current TargetBinding) (*PoolManager, error) {
			return NewPoolManager(PoolManagerConfig{
				Binding: current, Resolver: resolver,
				Factory: retirementFixedPoolFactory{pool: pool}, Store: &recordingBindingStore{},
				Audit: noOpRotationAudit{}, Now: func() time.Time { return current.UpdatedAt }, StaleAfter: time.Hour,
			})
		},
		RefreshTimeout: 60 * time.Millisecond,
		MaxConcurrent:  1,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = directory.Close() })
	t.Cleanup(pool.allowClose)
	administration, err := directory.Pool(binding)
	require.NoError(t, err)
	require.NoError(t, administration.Refresh(context.Background(), RefreshRequest{
		Actor: "principal:operator-1", Operation: RefreshRequested,
	}))
	manager := administration.(*boundedAdministrationPool).manager
	manager.mu.Lock()
	expected := manager.binding
	manager.mu.Unlock()
	refreshDone := make(chan error, 1)
	go func() {
		refreshDone <- administration.Refresh(context.Background(), RefreshRequest{
			Actor: "principal:operator-1", Operation: RefreshRequested,
		})
	}()
	select {
	case <-resolver.refreshStart:
	case <-time.After(time.Second):
		t.Fatal("second refresh did not reach the blocking resolver")
	}
	retirement, err := directory.RetireBinding(expected)
	require.NoError(t, err)
	firstWait := make(chan error, 1)
	secondWait := make(chan error, 1)
	go func() { firstWait <- retirement.Wait(context.Background(), time.Now().Add(30*time.Millisecond)) }()
	go func() { secondWait <- retirement.Wait(context.Background(), time.Now().Add(time.Second)) }()
	closeDone := make(chan error, 1)
	go func() { closeDone <- directory.Close() }()
	select {
	case <-pool.started:
	case <-time.After(time.Second):
		t.Fatal("captured manager cleanup did not start after refresh cancellation")
	}
	if err := <-firstWait; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first concurrent handle wait = %v, want deadline", err)
	}
	if err := <-closeDone; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("directory Close while captured manager is blocked = %v, want deadline", err)
	}
	select {
	case err := <-refreshDone:
		if !errors.Is(err, ErrProviderUnavailable) {
			t.Fatalf("refresh racing retirement = %v, want provider unavailable", err)
		}
	case <-time.After(time.Second):
		t.Fatal("refresh did not exit after retirement canceled it")
	}
	select {
	case err := <-secondWait:
		t.Fatalf("second waiter returned before physical close: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	select {
	case <-manager.retireDone:
		t.Fatal("manager published retirement before physical close returned")
	default:
	}
	pool.allowClose()
	if err := <-secondWait; err != nil {
		t.Fatalf("second waiter after physical close: %v", err)
	}
	if err := directory.Close(); err != nil {
		t.Fatalf("directory Close retry after captured cleanup: %v", err)
	}
	if calls := pool.calls.Load(); calls != 1 {
		t.Fatalf("captured pool Close calls = %d, want 1", calls)
	}
}
