package module

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	refreshrun "github.com/flidai/leapview/internal/refresh/run"
)

// executeWithLeaseHeartbeat keeps the capability-owned run and attempt lease
// alive for executions that are longer than their initial lease. The renewal
// context is detached from the worker's cancellation so a bounded cleanup can
// finish even while the executor is unwinding; every renewal call still has a
// finite timeout.
func executeWithLeaseHeartbeat(
	ctx context.Context,
	job refreshrun.JobRecord,
	lease time.Duration,
	renew func(context.Context, refreshrun.JobRecord, time.Duration) error,
	execute func(context.Context, refreshrun.JobRecord) error,
) error {
	if renew == nil || lease <= 0 {
		return execute(ctx, job)
	}
	interval := lease / 3
	if interval <= 0 {
		interval = time.Nanosecond
	}
	executionCtx, cancelExecution := context.WithCancel(ctx)
	defer cancelExecution()
	stop := make(chan struct{})
	heartbeatErr := make(chan error, 1)
	var executionDone atomic.Bool
	var fenced atomic.Bool
	var heartbeatWG sync.WaitGroup
	heartbeatWG.Add(1)
	go func() {
		defer heartbeatWG.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				heartbeatCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), heartbeatTimeout(lease))
				err := renew(heartbeatCtx, job, lease)
				cancel()
				if err != nil {
					if executionDone.Load() {
						return
					}
					fenced.Store(true)
					select {
					case heartbeatErr <- err:
					default:
					}
					cancelExecution()
					return
				}
			case <-stop:
				return
			}
		}
	}()

	result := execute(executionCtx, job)
	executionDone.Store(true)
	close(stop)
	heartbeatWG.Wait()
	select {
	case err := <-heartbeatErr:
		if ctx.Err() != nil || !fenced.Load() {
			return result
		}
		if result == nil || errors.Is(result, context.Canceled) {
			return errors.Join(refreshrun.ErrLeaseLost, err)
		}
		return errors.Join(result, refreshrun.ErrLeaseLost, err)
	default:
		return result
	}
}

func heartbeatTimeout(lease time.Duration) time.Duration {
	limit := lease / 3
	if limit < time.Second {
		return time.Second
	}
	if limit > 5*time.Second {
		return 5 * time.Second
	}
	return limit
}
