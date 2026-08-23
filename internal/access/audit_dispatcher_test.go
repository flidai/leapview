package access

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type dispatcherStore struct {
	mu sync.Mutex

	lease          AuditIntentLease
	found          bool
	claim          int
	completeErr    error
	retryErr       error
	poisonErr      error
	quarantineErr  error
	retries        int
	poisons        int
	quarantines    int
	nextRetry      time.Time
	retryCode      string
	poisonCode     string
	quarantineCode string
}

func (s *dispatcherStore) ClaimAuditIntent(_ context.Context, _ string, _ time.Duration) (AuditIntentLease, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claim++
	return s.lease, s.found, nil
}

func (s *dispatcherStore) CompleteAuditIntent(context.Context, AuditIntentLease) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.completeErr
}

func (s *dispatcherStore) RetryAuditIntent(_ context.Context, _ AuditIntentLease, next time.Time, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retries++
	s.nextRetry, s.retryCode = next, code
	return s.retryErr
}

func (s *dispatcherStore) PoisonAuditIntent(_ context.Context, _ AuditIntentLease, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.poisons++
	s.poisonCode = code
	return s.poisonErr
}

func (s *dispatcherStore) QuarantineAuditIntent(_ context.Context, _ AuditIntentLease, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quarantines++
	s.quarantineCode = code
	return s.quarantineErr
}

func (s *dispatcherStore) RequeueAuditIntent(context.Context, string) error { return nil }

func (s *dispatcherStore) AuditOutboxStats(context.Context, time.Time) (AuditOutboxStats, error) {
	return AuditOutboxStats{}, nil
}

func dispatcherLease(attempt int) AuditIntentLease {
	return AuditIntentLease{
		Intent: AuditIntent{EventID: "dispatcher-event"},
		State:  AuditIntentLeased, AttemptCount: attempt, LeaseOwner: "dispatcher", LeaseGeneration: 1,
	}
}

func newTestDispatcher(t *testing.T, store *dispatcherStore) *AuditDispatcher {
	t.Helper()
	dispatcher, err := NewAuditDispatcher(AuditDispatcherConfig{
		Store: store, PollInterval: time.Millisecond, LeaseDuration: time.Minute,
		BaseRetry: 2 * time.Second, MaxRetry: 10 * time.Second, MaxAttempts: 3,
		OwnerFactory: func() string { return "test-owner" }, Now: func() time.Time { return time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC) },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func TestAuditDispatcherDispatchOneRetriesTransientFailure(t *testing.T) {
	store := &dispatcherStore{lease: dispatcherLease(1), found: true, completeErr: errors.New("sink unavailable")}
	dispatcher := newTestDispatcher(t, store)
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	delivered, err := dispatcher.DispatchOne(context.Background(), "worker")
	if err != nil || !delivered {
		t.Fatalf("dispatch retry = delivered %v err %v, want handled", delivered, err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.retries != 1 || store.poisons != 0 {
		t.Fatalf("retry/poison calls = %d/%d, want 1/0", store.retries, store.poisons)
	}
	if !store.nextRetry.Equal(now.Add(2*time.Second)) || store.retryCode != "AUDIT_SINK_UNAVAILABLE" {
		t.Fatalf("retry transition = %s/%q, want %s/AUDIT_SINK_UNAVAILABLE", store.nextRetry, store.retryCode, now.Add(2*time.Second))
	}
}

func TestAuditDispatcherDispatchOnePoisonsConflictsAndExhaustedAttempts(t *testing.T) {
	for _, test := range []struct {
		name     string
		attempt  int
		err      error
		code     string
		terminal string
	}{
		{name: "payload conflict", attempt: 1, err: ErrAuditIntentConflict, code: "AUDIT_INTENT_CONFLICT", terminal: "quarantine"},
		{name: "max attempts", attempt: 3, err: errors.New("sink unavailable"), code: "AUDIT_SINK_UNAVAILABLE", terminal: "poison"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &dispatcherStore{lease: dispatcherLease(test.attempt), found: true, completeErr: test.err}
			dispatcher := newTestDispatcher(t, store)
			delivered, err := dispatcher.DispatchOne(context.Background(), "worker")
			if err != nil || !delivered {
				t.Fatalf("dispatch poison = delivered %v err %v, want handled", delivered, err)
			}
			store.mu.Lock()
			defer store.mu.Unlock()
			if test.terminal == "quarantine" {
				if store.quarantines != 1 || store.poisons != 0 || store.quarantineCode != test.code {
					t.Fatalf("quarantine/poison calls/code = %d/%d/%q, want 1/0/%q", store.quarantines, store.poisons, store.quarantineCode, test.code)
				}
			} else if store.poisons != 1 || store.quarantines != 0 || store.poisonCode != test.code {
				t.Fatalf("poison/quarantine calls/code = %d/%d/%q, want 1/0/%q", store.poisons, store.quarantines, store.poisonCode, test.code)
			}
		})
	}
}

func TestAuditDispatcherLeavesLeaseRecoverableOnCancellation(t *testing.T) {
	store := &dispatcherStore{lease: dispatcherLease(1), found: true, completeErr: context.Canceled}
	dispatcher := newTestDispatcher(t, store)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	delivered, err := dispatcher.DispatchOne(ctx, "worker")
	if !errors.Is(err, context.Canceled) || delivered {
		t.Fatalf("cancelled dispatch = delivered %v err %v, want false/context.Canceled", delivered, err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.retries != 0 || store.poisons != 0 {
		t.Fatalf("cancelled retry/poison calls = %d/%d, want 0/0", store.retries, store.poisons)
	}
}

func TestAuditDispatcherStartStopIsIdempotentAndCancellationSafe(t *testing.T) {
	store := &dispatcherStore{found: false}
	dispatcher := newTestDispatcher(t, store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := dispatcher.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Start(ctx); err != nil {
		t.Fatalf("second start: %v", err)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := dispatcher.Stop(stopCtx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := dispatcher.Stop(stopCtx); err != nil {
		t.Fatalf("second stop: %v", err)
	}
}

func TestNewAuditDispatcherValidatesConfiguration(t *testing.T) {
	if _, err := NewAuditDispatcher(AuditDispatcherConfig{}); err == nil {
		t.Fatal("nil store accepted")
	}
	store := &dispatcherStore{}
	if _, err := NewAuditDispatcher(AuditDispatcherConfig{Store: store, BaseRetry: 2 * time.Second, MaxRetry: time.Second}); err == nil {
		t.Fatal("max retry shorter than base accepted")
	}
	dispatcher := newTestDispatcher(t, store)
	if err := dispatcher.Start(context.Background()); err != nil {
		if err := dispatcher.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		t.Fatal(err)
	}
	if err := dispatcher.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAuditDispatcher(AuditDispatcherConfig{Store: store, OwnerFactory: func() string { return "" }}); err != nil {
		// OwnerFactory is evaluated by Start, not construction.
		t.Fatal(err)
	}
	badOwner, err := NewAuditDispatcher(AuditDispatcherConfig{Store: store, OwnerFactory: func() string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	if err := badOwner.Start(context.Background()); err == nil {
		t.Fatal("empty owner accepted")
	}
}
