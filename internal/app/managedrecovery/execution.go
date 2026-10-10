package managedrecovery

import (
	"context"
	"errors"
	"time"

	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/flidai/leapview/internal/platform/typednil"
	"github.com/flidai/leapview/internal/refresh/recovery"
	"github.com/google/uuid"
)

// ManagedExecutionLedger is the exact occurrence execution capability. The
// generic queue selection port is deliberately absent.
type ManagedExecutionLedger interface {
	ClaimExact(context.Context, string, recovery.ClaimInput) (recovery.Occurrence, bool, error)
	Start(context.Context, string, recovery.Fence, time.Time) error
	Heartbeat(context.Context, string, recovery.Fence, time.Time, time.Duration) error
	Occurrence(context.Context, string) (recovery.Occurrence, error)
}

// RunManagedOccurrence starts the exact enrolled pending intent and renews its
// execution lease throughout physical provider effects. Losing authority
// cancels those effects; it never adopts another worker's live occurrence.
// A completed occurrence uses replay to reverify its retained recovery evidence
// without acquiring a new lease or rerunning physical restore work.
func RunManagedOccurrence(ctx context.Context, ledger ManagedExecutionLedger, id, actor string, run func(context.Context, recovery.Fence) (providerrestore.Report, error), replay func(context.Context) (providerrestore.Report, error)) (providerrestore.Report, error) {
	return runManagedOccurrence(ctx, ledger, id, actor, 3*time.Minute, 30*time.Second, run, replay)
}

// ExecuteManagedRestore supplies the same concrete coordinator and completed
// acknowledgement replay to the operator and qualification entrypoints.
// Its caller must hold exclusive ownership of the enrolled instance home.
func ExecuteManagedRestore(ctx context.Context, input ManagedInput, config ManagedConfig, authority ManagedAuthorities) (providerrestore.Report, error) {
	report, _, err := executeManagedRestore(ctx, input, config, authority, nil)
	return report, err
}

func executeManagedRestore(ctx context.Context, input ManagedInput, config ManagedConfig, authority ManagedAuthorities, beforeRestore func() error) (providerrestore.Report, bool, error) {
	ledger, ok := authority.Ledger.(ManagedExecutionLedger)
	if !ok || typednil.IsNil(ledger) {
		return providerrestore.Report{}, false, errors.New("managed authority lacks exact execution capability")
	}
	started := false
	report, err := RunManagedOccurrence(ctx, ledger, input.OccurrenceID, input.Validator, func(ctx context.Context, fence recovery.Fence) (providerrestore.Report, error) {
		if beforeRestore != nil {
			if err := beforeRestore(); err != nil {
				return providerrestore.Report{}, err
			}
		}
		started = true
		coordinator, err := NewManaged(ctx, config, authority)
		if err != nil {
			return providerrestore.Report{}, err
		}
		return coordinator.Run(ctx, providerrestore.Request{OccurrenceID: input.OccurrenceID, Fence: fence, RecoverySetID: input.RecoverySetID, TargetID: config.Credentials.TargetID, ValidationAttemptID: input.ValidationAttemptID, Validator: input.Validator, Publisher: input.Publisher})
	}, func(ctx context.Context) (providerrestore.Report, error) {
		return ReplayManagedRecovery(ctx, config, authority)
	})
	return report, started, err
}

func runManagedOccurrence(ctx context.Context, ledger ManagedExecutionLedger, id, actor string, lease, interval time.Duration, run func(context.Context, recovery.Fence) (providerrestore.Report, error), replay func(context.Context) (providerrestore.Report, error)) (providerrestore.Report, error) {
	if typednil.IsNil(ledger) || run == nil || interval <= 0 || lease <= interval {
		return providerrestore.Report{}, errors.New("exact managed execution capability and bounded lease required")
	}
	claimed, ok, err := ledger.ClaimExact(ctx, id, recovery.ClaimInput{WorkerID: "managed-recovery:" + uuid.NewString(), Actor: actor, Now: time.Now().UTC(), Lease: lease})
	if err != nil {
		return providerrestore.Report{}, err
	}
	if !ok {
		completed, readErr := ledger.Occurrence(ctx, id)
		if readErr != nil {
			return providerrestore.Report{}, readErr
		}
		if completed.ID == id && completed.Status == recovery.StatusSucceeded && replay != nil {
			return replay(ctx)
		}
		return providerrestore.Report{}, errors.New("exact managed recovery occurrence is not claimable")
	}
	if err := ledger.Start(ctx, id, claimed.Fence, time.Now().UTC()); err != nil {
		return providerrestore.Report{}, err
	}
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				done <- nil
				return
			case <-work.Done():
				done <- work.Err()
				return
			case <-ticker.C:
				renewalContext, stopRenewal := context.WithTimeout(work, interval)
				renewalErr := ledger.Heartbeat(renewalContext, id, claimed.Fence, time.Now().UTC(), lease)
				if renewalErr != nil {
					// Completion clears the live lease. Its durable matching generation is
					// the only terminal state that can explain a successful final heartbeat race.
					occurrence, readErr := ledger.Occurrence(renewalContext, id)
					if readErr == nil && occurrence.ID == id && occurrence.Status == recovery.StatusSucceeded && occurrence.Fence.Generation == claimed.Fence.Generation {
						stopRenewal()
						done <- nil
						return
					}
					stopRenewal()
					cancel()
					done <- errors.New("managed recovery lost its exact execution lease")
					return
				}
				stopRenewal()
			}
		}
	}()
	report, runErr := run(work, claimed.Fence)
	close(stop)
	renewalErr := <-done
	if runErr != nil {
		return report, runErr
	}
	if renewalErr != nil {
		return report, renewalErr
	}
	return report, nil
}
