package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	platformlifecycle "github.com/flidai/leapview/internal/platform/lifecycle"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This runs within the real TLS PostgreSQL18 admission fixture, after the
// production pools have been constructed and their initial checks succeeded.
func assertMaintenanceSQLPreparation(t *testing.T, lifecycle *postgresControlPlaneLifecycle, adminURL, runtimeName string) {
	t.Helper()
	admin, err := pgxpool.New(t.Context(), adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err = admin.Exec(t.Context(), `CREATE TABLE public.maintenance_effect_fixture (effects integer NOT NULL); INSERT INTO public.maintenance_effect_fixture VALUES (0)`); err != nil {
		t.Fatal(err)
	}
	worked := make(chan error, 1)
	var started atomic.Int32
	worker := platformlifecycle.NewIntervalWorker(time.Hour, func(ctx context.Context) {
		_, err := admin.Exec(ctx, `UPDATE public.maintenance_effect_fixture SET effects = effects + 1`)
		worked <- err
	})
	gate := newMaintenanceAdmission("test-revision", lifecycle.Start, func(ctx context.Context) error { started.Add(1); return worker.Start(ctx) }, worker.Stop)
	if _, err = admin.Exec(t.Context(), "REVOKE SELECT ON recovery.recovery_set FROM "+runtimeName); err != nil {
		t.Fatal(err)
	}
	if err = gate.prepare(t.Context(), "operation"); err == nil {
		t.Fatal("prepared after required runtime role privilege was revoked")
	}
	if err = gate.open(t.Context(), "operation", time.Minute); err == nil {
		t.Fatal("admitted work after failed SQL preparation")
	}
	var effects int
	if err = admin.QueryRow(t.Context(), `SELECT effects FROM public.maintenance_effect_fixture`).Scan(&effects); err != nil {
		t.Fatal(err)
	}
	if effects != 0 || started.Load() != 0 {
		t.Fatal("unprepared worker performed SQL effects")
	}
	if _, err = admin.Exec(t.Context(), "GRANT SELECT ON recovery.recovery_set TO "+runtimeName); err != nil {
		t.Fatal(err)
	}
	if err = gate.prepare(t.Context(), "operation"); err != nil {
		t.Fatal(err)
	}
	if started.Load() != 0 {
		t.Fatal("preparation started SQL worker")
	}
	if err = gate.open(t.Context(), "operation", time.Minute); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-worked:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("authorized worker did not run")
	}
	if err = gate.close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = admin.QueryRow(t.Context(), `SELECT effects FROM public.maintenance_effect_fixture`).Scan(&effects); err != nil {
		t.Fatal(err)
	}
	if effects != 1 {
		t.Fatalf("acknowledged SQL effects after drain = %d, want 1", effects)
	}
}
