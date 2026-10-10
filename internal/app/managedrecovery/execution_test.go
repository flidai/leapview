package managedrecovery

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/flidai/leapview/internal/refresh/recovery"
)

type managedExecutionFixture struct {
	mu           sync.Mutex
	id           string
	ok           bool
	heartbeats   int
	heartbeatErr error
	started      bool
	status       string
	renewed      chan struct{}
}

func (f *managedExecutionFixture) ClaimExact(_ context.Context, id string, in recovery.ClaimInput) (recovery.Occurrence, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.id = id
	return recovery.Occurrence{ID: id, Fence: recovery.Fence{Owner: in.WorkerID, Generation: 7}}, f.ok, nil
}
func (f *managedExecutionFixture) Start(_ context.Context, id string, _ recovery.Fence, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id != f.id {
		return errors.New("foreign start")
	}
	f.started = true
	return nil
}
func (f *managedExecutionFixture) Heartbeat(_ context.Context, id string, _ recovery.Fence, _ time.Time, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id != f.id || !f.started {
		return errors.New("foreign/unstarted renewal")
	}
	f.heartbeats++
	if f.heartbeats == 2 && f.renewed != nil {
		close(f.renewed)
	}
	return f.heartbeatErr
}
func (f *managedExecutionFixture) Occurrence(_ context.Context, id string) (recovery.Occurrence, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return recovery.Occurrence{ID: id, Status: f.status, Fence: recovery.Fence{Generation: 7}}, nil
}

func TestManagedOccurrenceRenewsThroughoutProviderWork(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	fixture := &managedExecutionFixture{ok: true, renewed: make(chan struct{})}
	report, err := runManagedOccurrence(ctx, fixture, "exact-occurrence", "operator", 100*time.Millisecond, 10*time.Millisecond, func(ctx context.Context, fence recovery.Fence) (providerrestore.Report, error) {
		if fence.Generation != 7 || fence.Owner == "" {
			t.Fatal("missing exact claim fence")
		}
		select {
		case <-fixture.renewed:
			return providerrestore.Report{OccurrenceID: "exact-occurrence"}, nil
		case <-ctx.Done():
			return providerrestore.Report{}, ctx.Err()
		}
	}, nil)
	if err != nil || report.OccurrenceID != "exact-occurrence" {
		t.Fatalf("renewed provider work: %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.heartbeats < 2 || fixture.id != "exact-occurrence" {
		t.Fatal("provider effects ran without exact ongoing renewal")
	}
}
func TestManagedOccurrenceCancelsPhysicalWorkAfterLeaseLoss(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	fixture := &managedExecutionFixture{ok: true, heartbeatErr: recovery.ErrFenced, status: recovery.StatusRunning}
	canceled := false
	_, err := runManagedOccurrence(ctx, fixture, "exact-occurrence", "operator", 100*time.Millisecond, 10*time.Millisecond, func(ctx context.Context, _ recovery.Fence) (providerrestore.Report, error) {
		<-ctx.Done()
		canceled = true
		return providerrestore.Report{}, ctx.Err()
	}, nil)
	if err == nil || !canceled {
		t.Fatal("lost lease permitted physical work to continue")
	}
}
func TestManagedOccurrenceDoesNotRunWhenExactIntentUnavailable(t *testing.T) {
	fixture := &managedExecutionFixture{}
	_, err := runManagedOccurrence(t.Context(), fixture, "exact-occurrence", "operator", time.Second, time.Millisecond, func(context.Context, recovery.Fence) (providerrestore.Report, error) {
		t.Fatal("unclaimed work executed")
		return providerrestore.Report{}, nil
	}, nil)
	if err == nil || fixture.started {
		t.Fatal("unclaimable intent started")
	}
}
func TestManagedOccurrenceAcceptsOnlyMatchingSuccessfulTerminalRenewalRace(t *testing.T) {
	fixture := &managedExecutionFixture{ok: true, heartbeatErr: recovery.ErrFenced, status: recovery.StatusSucceeded, renewed: make(chan struct{})}
	_, err := runManagedOccurrence(t.Context(), fixture, "exact-occurrence", "operator", 100*time.Millisecond, 10*time.Millisecond, func(ctx context.Context, _ recovery.Fence) (providerrestore.Report, error) {
		select {
		case <-time.After(30 * time.Millisecond):
			return providerrestore.Report{Status: providerrestore.StatusSucceeded}, nil
		case <-ctx.Done():
			return providerrestore.Report{}, ctx.Err()
		}
	}, nil)
	if err != nil {
		t.Fatalf("successful final heartbeat race rejected: %v", err)
	}
}

func TestManagedOccurrenceReplaysCompletedRestoreWithoutClaimingOrStarting(t *testing.T) {
	fixture := &managedExecutionFixture{status: recovery.StatusSucceeded}
	replayed := false
	report, err := runManagedOccurrence(t.Context(), fixture, "exact-occurrence", "operator", time.Minute, time.Second,
		func(context.Context, recovery.Fence) (providerrestore.Report, error) {
			t.Fatal("completed restore executed physical work")
			return providerrestore.Report{}, nil
		}, func(context.Context) (providerrestore.Report, error) {
			replayed = true
			return providerrestore.Report{OccurrenceID: "exact-occurrence", Status: providerrestore.StatusSucceeded}, nil
		})
	if err != nil || !replayed || report.Status != providerrestore.StatusSucceeded || fixture.started || fixture.heartbeats != 0 {
		t.Fatalf("completed acknowledgement retry: report=%+v replayed=%v started=%v err=%v", report, replayed, fixture.started, err)
	}
}

func TestManagedOccurrenceReplayPropagatesVerificationFailure(t *testing.T) {
	fixture := &managedExecutionFixture{status: recovery.StatusSucceeded}
	denied := errors.New("original writer is no longer fenced")
	_, err := runManagedOccurrence(t.Context(), fixture, "exact-occurrence", "operator", time.Minute, time.Second,
		func(context.Context, recovery.Fence) (providerrestore.Report, error) {
			t.Fatal("completed physical work repeated")
			return providerrestore.Report{}, nil
		},
		func(context.Context) (providerrestore.Report, error) { return providerrestore.Report{}, denied })
	if !errors.Is(err, denied) || fixture.started {
		t.Fatalf("replay bypassed fresh verification: %v", err)
	}
}

func TestManagedOccurrenceNeverReplaysOtherUnclaimableStates(t *testing.T) {
	for _, status := range []string{recovery.StatusPending, recovery.StatusClaimed, recovery.StatusRunning, recovery.StatusFailed, recovery.StatusCanceled} {
		t.Run(status, func(t *testing.T) {
			fixture := &managedExecutionFixture{status: status}
			_, err := runManagedOccurrence(t.Context(), fixture, "exact-occurrence", "operator", time.Minute, time.Second,
				func(context.Context, recovery.Fence) (providerrestore.Report, error) {
					t.Fatal("unclaimed work ran")
					return providerrestore.Report{}, nil
				},
				func(context.Context) (providerrestore.Report, error) {
					t.Fatal("unsuccessful occurrence replayed")
					return providerrestore.Report{}, nil
				})
			if err == nil || fixture.started {
				t.Fatal("unclaimable occurrence accepted")
			}
		})
	}
}
