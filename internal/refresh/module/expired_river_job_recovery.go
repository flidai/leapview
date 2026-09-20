package module

import (
	"context"
	"fmt"
	"time"
)

func (m *Module) recoverExpiredRiverJobsOnStart(ctx context.Context) (PostgresJobsAuthority, error) {
	persistence, ok := m.runs.(*postgresRunPersistence)
	if !ok || persistence == nil || persistence.jobs == nil {
		return nil, nil
	}
	recovery := persistence.jobs
	const recoveryBatch = 100
	for {
		recovered, err := recovery.RecoverExpiredRiverJobs(ctx, recoveryBatch)
		if err != nil {
			return nil, fmt.Errorf("recover expired refresh jobs: %w", err)
		}
		if recovered < recoveryBatch {
			break
		}
	}
	return recovery, nil
}

func (m *Module) runExpiredRiverJobRecovery(ctx context.Context, recovery PostgresJobsAuthority) {
	defer m.wg.Done()
	ticker := time.NewTicker(expiredRiverJobRecoveryInterval(m.leaseTimeout))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			const recoveryBatch = 100
			for {
				recovered, err := recovery.RecoverExpiredRiverJobs(ctx, recoveryBatch)
				if err != nil {
					if ctx.Err() == nil {
						m.logger.WarnContext(ctx, "recover expired refresh jobs failed", "error", err)
					}
					break
				}
				if recovered < recoveryBatch {
					break
				}
			}
		}
	}
}

func expiredRiverJobRecoveryInterval(lease time.Duration) time.Duration {
	interval := lease / 3
	if interval < time.Second {
		return time.Second
	}
	if interval > 30*time.Second {
		return 30 * time.Second
	}
	return interval
}
