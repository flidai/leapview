package sqlite

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/agent"
)

func TestSQLiteModelRequestUsageReserveIsAtomicAndRollsForward(t *testing.T) {
	ctx := context.Background()
	store, repo := openAgentRepo(t, ctx)

	initial, err := repo.ModelRequestUsage(ctx)
	if err != nil {
		t.Fatalf("read initial model request usage: %v", err)
	}
	if initial.Used != 0 || initial.ResetsAt.Before(time.Now().UTC()) {
		t.Fatalf("initial model request usage = %#v", initial)
	}

	const (
		limit   = int64(9)
		callers = 64
	)
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, reserveErr := repo.ReserveModelRequest(ctx, limit)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case reserveErr == nil:
				successes++
			case !errors.Is(reserveErr, agent.ErrModelRequestLimit):
				t.Errorf("concurrent reservation error = %v", reserveErr)
			}
		}()
	}
	wg.Wait()
	if successes != int(limit) {
		t.Fatalf("successful concurrent reservations = %d, want %d", successes, limit)
	}
	used, err := repo.ModelRequestUsage(ctx)
	if err != nil {
		t.Fatalf("read saturated model request usage: %v", err)
	}
	if used.Used != limit {
		t.Fatalf("saturated model request usage = %#v, want used=%d", used, limit)
	}

	// Reading stale state reports a reset without mutating the persisted row;
	// the next reservation performs the actual forward roll.
	if _, err := store.SQLDB().ExecContext(ctx, `
        UPDATE agent_model_request_usage
        SET usage_day = date('now', '-1 day'), used_requests = ?
        WHERE singleton_id = 1`, limit); err != nil {
		t.Fatalf("seed stale model request usage: %v", err)
	}
	stale, err := repo.ModelRequestUsage(ctx)
	if err != nil {
		t.Fatalf("read stale model request usage: %v", err)
	}
	if stale.Used != 0 {
		t.Fatalf("stale model request usage = %#v, want used=0", stale)
	}
	var persisted int64
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT used_requests FROM agent_model_request_usage WHERE singleton_id = 1`).Scan(&persisted); err != nil {
		t.Fatalf("inspect stale model request usage: %v", err)
	}
	if persisted != limit {
		t.Fatalf("read changed stale persisted usage = %d, want %d", persisted, limit)
	}
	rolled, err := repo.ReserveModelRequest(ctx, limit)
	if err != nil {
		t.Fatalf("reserve after UTC day rollover: %v", err)
	}
	if rolled.Used != 1 {
		t.Fatalf("rolled model request usage = %#v, want used=1", rolled)
	}

	if _, err := store.SQLDB().ExecContext(ctx, `
        UPDATE agent_model_request_usage
        SET usage_day = date('now', '+1 day'), used_requests = 2
        WHERE singleton_id = 1`); err != nil {
		t.Fatalf("seed future model request usage: %v", err)
	}
	future, err := repo.ReserveModelRequest(ctx, limit)
	if err != nil {
		t.Fatalf("reserve against future-dated usage: %v", err)
	}
	if future.Used != 3 {
		t.Fatalf("future-dated model request usage = %#v, want used=3", future)
	}
}
