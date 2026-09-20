package module

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func assertExpiredRiverJobRecoveryLifecycle(t *testing.T, db *pgxpool.Pool, queue *PostgresJobsAdapter, runID string, riverID int64) {
	t.Helper()
	if recovered, err := queue.RecoverExpiredRiverJobs(t.Context(), 100); err != nil || recovered != 0 {
		t.Fatalf("live refresh recovery = %d, %v; want 0, nil", recovered, err)
	}
	var liveState string
	if err := db.QueryRow(t.Context(), `SELECT state::text FROM public.river_job WHERE id=$1`, riverID).Scan(&liveState); err != nil {
		t.Fatal(err)
	}
	if liveState != "running" {
		t.Fatalf("live refresh River state = %q, want running", liveState)
	}
	expireTx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer expireTx.Rollback(t.Context())
	if _, err := expireTx.Exec(t.Context(), `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := expireTx.Exec(t.Context(), `
		UPDATE refresh.run SET lease_expires_at=clock_timestamp()-interval '1 second'
		WHERE run_id=$1 AND status='running' AND lease_owner='owner-b' AND fence_generation=2`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := expireTx.Exec(t.Context(), `
		UPDATE refresh.attempt SET lease_expires_at=clock_timestamp()-interval '1 second'
		WHERE run_id=$1 AND status='running' AND owner_id='owner-b' AND fence_generation=2`, runID); err != nil {
		t.Fatal(err)
	}
	if err := expireTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	recovered, err := queue.RecoverExpiredRiverJobs(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 1 {
		t.Fatalf("recovered refresh jobs = %d, want 1", recovered)
	}
	var recoveredState string
	if err := db.QueryRow(t.Context(), `SELECT state::text FROM public.river_job WHERE id=$1`, riverID).Scan(&recoveredState); err != nil {
		t.Fatal(err)
	}
	if recoveredState != "retryable" {
		t.Fatalf("recovered River state = %q, want retryable", recoveredState)
	}
	if recovered, err := queue.RecoverExpiredRiverJobs(t.Context(), 100); err != nil || recovered != 0 {
		t.Fatalf("recovery replay = %d, %v; want 0, nil", recovered, err)
	}
	if _, err := db.Exec(t.Context(), `UPDATE public.river_job SET state='running' WHERE id=$1 AND state='retryable'`, riverID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `
		UPDATE refresh.run SET lease_expires_at=clock_timestamp()+interval '500 milliseconds'
		WHERE run_id=$1 AND status='running' AND lease_owner='owner-b' AND fence_generation=2`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `
		UPDATE refresh.attempt SET lease_expires_at=clock_timestamp()+interval '500 milliseconds'
		WHERE run_id=$1 AND status='running' AND owner_id='owner-b' AND fence_generation=2`, runID); err != nil {
		t.Fatal(err)
	}
	refreshModule := &Module{runs: &postgresRunPersistence{jobs: queue}, leaseTimeout: 3 * time.Second, logger: slog.Default()}
	if err := refreshModule.Start(t.Context()); err != nil {
		t.Fatalf("start refresh module before lease expiry: %v", err)
	}
	t.Cleanup(func() { _ = refreshModule.Stop(context.Background()) })
	if err := db.QueryRow(t.Context(), `SELECT state::text FROM public.river_job WHERE id=$1`, riverID).Scan(&recoveredState); err != nil {
		t.Fatal(err)
	}
	if recoveredState != "running" {
		t.Fatalf("live River state after startup = %q, want running", recoveredState)
	}
	deadline := time.Now().Add(5 * time.Second)
	for recoveredState != "retryable" && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		if err := db.QueryRow(t.Context(), `SELECT state::text FROM public.river_job WHERE id=$1`, riverID).Scan(&recoveredState); err != nil {
			t.Fatal(err)
		}
	}
	if recoveredState != "retryable" {
		t.Fatalf("post-expiry River state = %q, want retryable", recoveredState)
	}
}
