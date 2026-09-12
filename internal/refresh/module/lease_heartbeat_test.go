package module

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	refreshrun "github.com/flidai/leapview/internal/refresh/run"
)

func TestExecuteWithLeaseHeartbeatRenewsLongRun(t *testing.T) {
	lease := 30 * time.Millisecond
	var renewals atomic.Int32
	started := time.Now()
	err := executeWithLeaseHeartbeat(t.Context(), refreshrun.JobRecord{}, lease,
		func(context.Context, refreshrun.JobRecord, time.Duration) error {
			renewals.Add(1)
			return nil
		},
		func(ctx context.Context, _ refreshrun.JobRecord) error {
			timer := time.NewTimer(4 * lease)
			defer timer.Stop()
			select {
			case <-timer.C:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	if err != nil {
		t.Fatalf("long execution error = %v", err)
	}
	if time.Since(started) < 3*lease || renewals.Load() < 2 {
		t.Fatalf("long execution renewals = %d after %s, want at least 2", renewals.Load(), time.Since(started))
	}
}

func TestExecuteWithLeaseHeartbeatStopsOnStaleFence(t *testing.T) {
	lease := 30 * time.Millisecond
	err := executeWithLeaseHeartbeat(t.Context(), refreshrun.JobRecord{}, lease,
		func(context.Context, refreshrun.JobRecord, time.Duration) error {
			return refreshrun.ErrLeaseLost
		},
		func(ctx context.Context, _ refreshrun.JobRecord) error {
			<-ctx.Done()
			return ctx.Err()
		})
	if !errors.Is(err, refreshrun.ErrLeaseLost) {
		t.Fatalf("stale fence error = %v, want ErrLeaseLost", err)
	}
}
