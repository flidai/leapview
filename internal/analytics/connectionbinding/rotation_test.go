package connectionbinding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPoolManagerActivatesValidatedReplacementAndDrainsPreviousLeases(t *testing.T) {
	now := time.Date(2026, 7, 29, 17, 0, 0, 0, time.UTC)
	resolver := &sequenceResolver{snapshots: []CredentialSnapshot{
		testSnapshot(t, "version-1", now),
		testSnapshot(t, "version-2", now.Add(time.Minute)),
	}}
	factory := &recordingPoolFactory{}
	store := &recordingBindingStore{}
	manager, err := NewPoolManager(PoolManagerConfig{
		Binding: validTargetBinding(t), Resolver: resolver, Factory: factory, Store: store,
		Audit: noOpRotationAudit{},
		Now:   func() time.Time { return now }, StaleAfter: time.Hour,
	})
	require.NoError(t, err)
	if err := manager.RefreshNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	firstLease, err := manager.Lease()
	require.NoError(t, err)
	first := firstLease.Pool().(*recordingRuntimePool)

	now = now.Add(time.Minute)
	if err := manager.RefreshNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	secondLease, err := manager.Lease()
	require.NoError(t, err)
	second := secondLease.Pool().(*recordingRuntimePool)
	if first == second || first.closed {
		t.Fatalf("first=%p second=%p first.closed=%t", first, second, first.closed)
	}
	if got := manager.Evidence().ValidatedVersion; got != "version-2" {
		t.Fatalf("validated version = %q", got)
	}
	secondLease.Release()
	if first.closed {
		t.Fatal("previous pool closed before its outstanding lease drained")
	}
	firstLease.Release()
	if !first.closed || second.closed {
		t.Fatalf("first.closed=%t second.closed=%t", first.closed, second.closed)
	}
}

func TestPoolManagerRequiresAuditRecorder(t *testing.T) {
	binding := validTargetBinding(t)
	_, err := NewPoolManager(PoolManagerConfig{
		Binding: binding, Resolver: &sequenceResolver{}, Factory: &recordingPoolFactory{},
		Store: &recordingBindingStore{}, Now: time.Now, StaleAfter: time.Hour,
	})
	if !errors.Is(err, ErrRotationAuditUnavailable) {
		t.Fatalf("NewPoolManager() error = %v, want ErrRotationAuditUnavailable", err)
	}
}

func TestPoolManagerPreservesSuccessfulRotationAndObservesBestEffortAuditFailure(t *testing.T) {
	now := time.Date(2026, 7, 29, 17, 0, 0, 0, time.UTC)
	var logs bytes.Buffer
	manager, err := NewPoolManager(PoolManagerConfig{
		Binding:  validTargetBinding(t),
		Resolver: &sequenceResolver{snapshots: []CredentialSnapshot{testSnapshot(t, "version-1", now)}},
		Factory:  &recordingPoolFactory{}, Store: &recordingBindingStore{},
		Audit: failingRotationAudit{}, Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
		Now: func() time.Time { return now }, StaleAfter: time.Hour,
	})
	require.NoError(t, err)
	if err := manager.RefreshNow(context.Background()); err != nil {
		t.Fatalf("RefreshNow() changed successful rotation result after audit failure: %v", err)
	}
	if evidence := manager.Evidence(); evidence.ValidatedVersion != "version-1" || evidence.Health != HealthHealthy {
		t.Fatalf("rotation evidence = %#v", evidence)
	}
	if output := logs.String(); !strings.Contains(output, "best-effort credential rotation audit failed") ||
		!strings.Contains(output, string(RefreshRequested)) || !strings.Contains(output, manager.Evidence().BindingID) {
		t.Fatalf("audit failure log = %s", output)
	}
}

func TestPoolManagerKeepsHealthyPoolWhenNewVersionFailsValidation(t *testing.T) {
	now := time.Date(2026, 7, 29, 17, 0, 0, 0, time.UTC)
	resolver := &sequenceResolver{snapshots: []CredentialSnapshot{
		testSnapshot(t, "version-1", now),
		testSnapshot(t, "version-bad", now.Add(time.Minute)),
	}}
	factory := &recordingPoolFactory{healthFailures: map[string]error{"version-bad": errors.New("source-secret-must-not-leak")}}
	store := &recordingBindingStore{}
	manager, err := NewPoolManager(PoolManagerConfig{
		Binding: validTargetBinding(t), Resolver: resolver, Factory: factory, Store: store,
		Audit: noOpRotationAudit{},
		Now:   func() time.Time { return now }, StaleAfter: time.Hour,
	})
	require.NoError(t, err)
	if err := manager.RefreshNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	healthy, err := manager.Lease()
	require.NoError(t, err)
	active := healthy.Pool()
	healthy.Release()

	now = now.Add(time.Minute)
	err = manager.RefreshNow(context.Background())
	if !errors.Is(err, ErrInvalidCredentialBundle) || containsSecret(err) {
		t.Fatalf("RefreshNow() error = %v", err)
	}
	current, err := manager.Lease()
	require.NoError(t, err)
	defer current.Release()
	if current.Pool() != active || manager.Evidence().Health != HealthDegraded ||
		manager.Evidence().ValidatedVersion != "version-1" {
		t.Fatalf("active replaced after invalid rotation: evidence=%#v", manager.Evidence())
	}
	if !factory.pools[1].closed {
		t.Fatal("invalid replacement pool was not closed")
	}
}

func TestPoolManagerCoalescesConcurrentRefreshAndDisableFailsClosed(t *testing.T) {
	now := time.Date(2026, 7, 29, 17, 0, 0, 0, time.UTC)
	resolver := &sequenceResolver{snapshots: []CredentialSnapshot{testSnapshot(t, "version-1", now)}}
	resolver.delay = 20 * time.Millisecond
	factory := &recordingPoolFactory{}
	manager, err := NewPoolManager(PoolManagerConfig{
		Binding: validTargetBinding(t), Resolver: resolver, Factory: factory, Store: &recordingBindingStore{},
		Audit: noOpRotationAudit{},
		Now:   func() time.Time { return now }, StaleAfter: time.Hour,
	})
	require.NoError(t, err)
	var wait sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errs <- manager.RefreshNow(context.Background())
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	if resolver.calls != 1 || len(factory.pools) != 1 {
		t.Fatalf("resolver calls=%d pools=%d", resolver.calls, len(factory.pools))
	}
	lease, err := manager.Lease()
	require.NoError(t, err)
	if err := manager.Disable(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Lease(); !errors.Is(err, ErrDisabledBinding) {
		t.Fatalf("Lease() after disable error = %v", err)
	}
	active := lease.Pool().(*recordingRuntimePool)
	if active.closed {
		t.Fatal("disabled pool closed before outstanding lease drained")
	}
	lease.Release()
	if !active.closed {
		t.Fatal("disabled pool did not drain")
	}
}

func TestPoolManagerRetainsValidatedPoolOnlyWithinStalePolicyDuringProviderOutage(t *testing.T) {
	now := time.Date(2026, 7, 29, 17, 0, 0, 0, time.UTC)
	resolver := &sequenceResolver{
		snapshots: []CredentialSnapshot{testSnapshot(t, "version-1", now)},
		errs:      []error{nil, ErrProviderUnavailable},
	}
	manager, err := NewPoolManager(PoolManagerConfig{
		Binding: validTargetBinding(t), Resolver: resolver, Factory: &recordingPoolFactory{}, Store: &recordingBindingStore{},
		Audit: noOpRotationAudit{},
		Now:   func() time.Time { return now }, StaleAfter: 5 * time.Minute,
	})
	require.NoError(t, err)
	if err := manager.RefreshNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := manager.RefreshNow(context.Background()); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("provider outage error = %v", err)
	}
	if manager.Evidence().Health != HealthDegraded || manager.Evidence().ValidatedVersion != "version-1" {
		t.Fatalf("outage evidence = %#v", manager.Evidence())
	}
	lease, err := manager.Lease()
	if err != nil {
		t.Fatalf("lease inside stale policy: %v", err)
	}
	lease.Release()
	now = now.Add(5 * time.Minute)
	if _, err := manager.Lease(); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("lease outside stale policy error = %v", err)
	}
}

func TestPoolManagerRunUsesIntervalThenExponentialBackoffAndStopsOnCancellation(t *testing.T) {
	now := time.Date(2026, 7, 29, 17, 0, 0, 0, time.UTC)
	resolver := &sequenceResolver{
		snapshots: []CredentialSnapshot{testSnapshot(t, "version-1", now)},
		errs:      []error{nil, ErrProviderUnavailable, ErrProviderUnavailable, nil},
	}
	waiter := &recordingWaiter{cancelAfter: 4}
	manager, err := NewPoolManager(PoolManagerConfig{
		Binding: validTargetBinding(t), Resolver: resolver, Factory: &recordingPoolFactory{},
		Store: &recordingBindingStore{}, Now: func() time.Time { return now }, StaleAfter: time.Hour,
		Audit: noOpRotationAudit{},
		Schedule: RefreshSchedule{
			Interval: 10 * time.Minute, BackoffInitial: time.Second, BackoffMax: time.Minute,
			JitterRatio: 0.1, Random: func() float64 { return 1 }, Wait: waiter.Wait,
		},
	})
	require.NoError(t, err)
	err = manager.Run(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
	want := []time.Duration{11 * time.Minute, 1100 * time.Millisecond, 2200 * time.Millisecond, 11 * time.Minute}
	if len(waiter.delays) != len(want) {
		t.Fatalf("delays = %v, want %v", waiter.delays, want)
	}
	for index := range want {
		if waiter.delays[index] != want[index] {
			t.Fatalf("delay[%d] = %s, want %s", index, waiter.delays[index], want[index])
		}
	}
	if resolver.calls != 4 {
		t.Fatalf("resolver calls = %d, want 4", resolver.calls)
	}
}

func TestPoolManagerAuditsActivationDegradationRecoveryAndExplicitRefreshActor(t *testing.T) {
	now := time.Date(2026, 7, 29, 17, 0, 0, 0, time.UTC)
	resolver := &sequenceResolver{
		snapshots: []CredentialSnapshot{
			testSnapshot(t, "version-1", now),
			testSnapshot(t, "version-bad", now.Add(time.Minute)),
			testSnapshot(t, "version-2", now.Add(2*time.Minute)),
		},
	}
	factory := &recordingPoolFactory{healthFailures: map[string]error{"version-bad": errors.New("source-secret-must-not-leak")}}
	audit := &recordingRotationAudit{}
	manager, err := NewPoolManager(PoolManagerConfig{
		Binding: validTargetBinding(t), Resolver: resolver, Factory: factory, Store: &recordingBindingStore{},
		Audit: audit, Now: func() time.Time { return now }, StaleAfter: time.Hour,
	})
	require.NoError(t, err)
	if err := manager.Refresh(context.Background(), RefreshRequest{Actor: "runtime:target-1", Operation: RefreshScheduled}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := manager.Refresh(context.Background(), RefreshRequest{Actor: "principal:author-1", Operation: RefreshRequested}); !errors.Is(err, ErrInvalidCredentialBundle) {
		t.Fatalf("bad refresh error = %v", err)
	}
	now = now.Add(time.Minute)
	if err := manager.Refresh(context.Background(), RefreshRequest{Actor: "principal:author-1", Operation: RefreshRequested}); err != nil {
		t.Fatal(err)
	}

	if len(audit.events) != 3 {
		t.Fatalf("audit events = %#v", audit.events)
	}
	assertRotationAudit(t, audit.events[0], "runtime:target-1", RefreshScheduled, RotationActivated, "version-1", "")
	assertRotationAudit(t, audit.events[1], "principal:author-1", RefreshRequested, RotationDegraded, "version-bad", "POOL_HEALTH_CHECK_FAILED")
	assertRotationAudit(t, audit.events[2], "principal:author-1", RefreshRequested, RotationActivated, "version-2", "")
	for _, event := range audit.events {
		encoded, err := json.Marshal(event)
		require.NoError(t, err)
		if strings.Contains(string(encoded), "source-secret") || strings.Contains(string(encoded), "connection_string") {
			t.Fatalf("audit disclosed credential material: %s", encoded)
		}
	}
}

func TestPoolManagerCancellationDoesNotDegradeOrPersist(t *testing.T) {
	now := time.Date(2026, 7, 29, 17, 0, 0, 0, time.UTC)
	manager, err := NewPoolManager(PoolManagerConfig{
		Binding: validTargetBinding(t), Resolver: canceledResolver{}, Factory: &recordingPoolFactory{},
		Store: &recordingBindingStore{}, Now: func() time.Time { return now }, StaleAfter: time.Hour,
		Audit: noOpRotationAudit{},
	})
	require.NoError(t, err)
	if err := manager.RefreshNow(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("RefreshNow() error = %v", err)
	}
	if evidence := manager.Evidence(); evidence.Health != HealthPending || evidence.BindingRevision != 1 {
		t.Fatalf("cancellation changed binding evidence: %#v", evidence)
	}
}

func TestPoolManagerRestartRevalidatesPersistedVersionAndRepeatedOutageIsIdempotent(t *testing.T) {
	now := time.Date(2026, 7, 29, 17, 0, 0, 0, time.UTC)
	binding, err := validTargetBinding(t).MarkValidated("version-1", now)
	require.NoError(t, err)
	now = now.Add(time.Minute)
	resolver := &sequenceResolver{
		snapshots: []CredentialSnapshot{testSnapshot(t, "version-1", now)},
		errs:      []error{nil, ErrProviderUnavailable, ErrProviderUnavailable},
	}
	factory := &recordingPoolFactory{}
	store := &recordingBindingStore{}
	manager, err := NewPoolManager(PoolManagerConfig{
		Binding: binding, Resolver: resolver, Factory: factory, Store: store,
		Audit: noOpRotationAudit{},
		Now:   func() time.Time { return now }, StaleAfter: time.Hour,
	})
	require.NoError(t, err)
	if err := manager.RefreshNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(factory.pools) != 1 {
		t.Fatalf("restart prepared pools = %d, want 1", len(factory.pools))
	}
	now = now.Add(time.Minute)
	if err := manager.RefreshNow(context.Background()); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("first outage error = %v", err)
	}
	degradedRevision := manager.Evidence().BindingRevision
	now = now.Add(time.Minute)
	if err := manager.RefreshNow(context.Background()); !errors.Is(err, ErrProviderUnavailable) ||
		errors.Is(err, ErrIncompatibleBinding) {
		t.Fatalf("repeated outage error = %v", err)
	}
	if got := manager.Evidence().BindingRevision; got != degradedRevision {
		t.Fatalf("repeated outage revision = %d, want idempotent %d", got, degradedRevision)
	}
}

func TestPoolManagerHealthStatusIsCompleteAndRedacted(t *testing.T) {
	now := time.Date(2026, 7, 29, 17, 0, 0, 0, time.UTC)
	resolver := &sequenceResolver{
		snapshots: []CredentialSnapshot{testSnapshot(t, "version-1", now)},
		errs:      []error{nil, ErrProviderUnavailable},
	}
	manager, err := NewPoolManager(PoolManagerConfig{
		Binding: validTargetBinding(t), Resolver: resolver, Factory: &recordingPoolFactory{},
		Store: &recordingBindingStore{}, Now: func() time.Time { return now }, StaleAfter: 10 * time.Minute,
		Audit: noOpRotationAudit{},
	})
	require.NoError(t, err)
	if err := manager.RefreshNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if err := manager.RefreshNow(context.Background()); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("RefreshNow() error = %v", err)
	}
	status := manager.HealthStatus()
	if status.BindingID != "binding_prod_warehouse" || status.Health != HealthDegraded ||
		status.ValidatedVersion != "version-1" || !status.LastAttemptAt.Equal(now) ||
		!status.LastValidatedAt.Equal(now.Add(-2*time.Minute)) ||
		status.StaleAgeSeconds != 120 || status.DiagnosticCode != "PROVIDER_UNAVAILABLE" ||
		!status.HasActivePool {
		t.Fatalf("health status = %#v", status)
	}
	encoded, err := json.Marshal(status)
	require.NoError(t, err)
	for _, forbidden := range []string{"source-secret", "connection_string", "infisical-project", "/leapview/sales"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("health status disclosed %q: %s", forbidden, encoded)
		}
	}
}

func testSnapshot(t *testing.T, version string, now time.Time) CredentialSnapshot {
	t.Helper()
	snapshot, err := NewCredentialSnapshot(map[string]string{"connection_string": "source-secret"}, version, now, now.Add(time.Hour))
	require.NoError(t, err)
	return snapshot
}

type sequenceResolver struct {
	mu        sync.Mutex
	snapshots []CredentialSnapshot
	errs      []error
	calls     int
	delay     time.Duration
}

func (resolver *sequenceResolver) Resolve(context.Context, CredentialReference) (CredentialSnapshot, error) {
	if resolver.delay > 0 {
		time.Sleep(resolver.delay)
	}
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	resolver.calls++
	index := resolver.calls - 1
	if index < len(resolver.errs) && resolver.errs[index] != nil {
		return CredentialSnapshot{}, resolver.errs[index]
	}
	if index >= len(resolver.snapshots) {
		index = len(resolver.snapshots) - 1
	}
	return resolver.snapshots[index], nil
}

type recordingPoolFactory struct {
	mu             sync.Mutex
	pools          []*recordingRuntimePool
	healthFailures map[string]error
}

func (factory *recordingPoolFactory) Prepare(_ context.Context, _ TargetBinding, snapshot CredentialSnapshot) (RuntimePool, error) {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	pool := &recordingRuntimePool{version: snapshot.ProviderVersion(), healthError: factory.healthFailures[snapshot.ProviderVersion()]}
	factory.pools = append(factory.pools, pool)
	return pool, nil
}

type recordingRuntimePool struct {
	version     string
	healthError error
	closed      bool
}

func (pool *recordingRuntimePool) HealthCheck(context.Context) error { return pool.healthError }
func (pool *recordingRuntimePool) Close() error {
	pool.closed = true
	return nil
}

type recordingBindingStore struct {
	mu      sync.Mutex
	binding TargetBinding
}

func (store *recordingBindingStore) Save(_ context.Context, binding TargetBinding, expectedRevision int64) (TargetBinding, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if binding.Revision != expectedRevision+1 {
		return TargetBinding{}, ErrIncompatibleBinding
	}
	store.binding = binding
	return binding, nil
}

type recordingWaiter struct {
	delays      []time.Duration
	cancelAfter int
}

func (waiter *recordingWaiter) Wait(_ context.Context, delay time.Duration) error {
	waiter.delays = append(waiter.delays, delay)
	if len(waiter.delays) >= waiter.cancelAfter {
		return context.Canceled
	}
	return nil
}

type recordingRotationAudit struct {
	events []RotationAuditEvent
}

type failingRotationAudit struct{}

func (failingRotationAudit) RecordCredentialRotation(context.Context, RotationAuditEvent) error {
	return errors.New("audit unavailable")
}

func (audit *recordingRotationAudit) RecordCredentialRotation(_ context.Context, event RotationAuditEvent) error {
	audit.events = append(audit.events, event)
	return nil
}

func assertRotationAudit(
	t *testing.T,
	event RotationAuditEvent,
	actor string,
	operation RefreshOperation,
	outcome RotationOutcome,
	version string,
	reason string,
) {
	t.Helper()
	if event.BindingID != "binding_prod_warehouse" || event.TargetID != "lvinst_prod" ||
		event.Actor != actor || event.Operation != operation || event.Outcome != outcome ||
		event.ProviderVersion != version || event.Reason != reason || event.Timestamp.IsZero() {
		t.Fatalf("audit event = %#v", event)
	}
}

type canceledResolver struct{}

func (canceledResolver) Resolve(context.Context, CredentialReference) (CredentialSnapshot, error) {
	return CredentialSnapshot{}, context.Canceled
}

func containsSecret(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "source-secret") || strings.Contains(err.Error(), "connection_string"))
}
