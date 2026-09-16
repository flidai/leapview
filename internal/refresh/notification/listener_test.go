package notification

import (
	"testing"
	"time"

	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
)

func TestListenerWakesOnPostgresNotificationAndStops(t *testing.T) {
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "refresh_notification_test")
	pool, err := platformpostgres.Open(t.Context(), platformpostgres.Config{
		URL: db.AdminURL(), ExpectedMajor: platformpostgres.DefaultExpectedMajor,
		RuntimeRole: "postgres", Intent: platformpostgres.IntentReadWrite,
		MinConns: 0, MaxConns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	wakes := make(chan Change, 4)
	listener := NewListener(pool, func(change Change) { wakes <- change })
	if err := listener.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Stop(t.Context()) })
	wait := func() Change {
		t.Helper()
		select {
		case change := <-wakes:
			return change
		case <-time.After(3 * time.Second):
			t.Fatal("refresh listener did not wake")
			return Change{}
		}
	}
	if change := wait(); !change.Resync {
		t.Fatalf("initial wake = %+v, want resync", change)
	}
	if _, err := pool.Exec(t.Context(), `SELECT pg_notify('leapview_refresh_changed', '{"projectId":"p","environment":"prod"}')`); err != nil {
		t.Fatal(err)
	}
	if change := wait(); change.ProjectID != "p" || change.Environment != "prod" || change.Resync {
		t.Fatalf("notification wake = %+v", change)
	}
	var backendPID int
	if err := pool.QueryRow(t.Context(), `SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND query='LISTEN leapview_refresh_changed' AND pid<>pg_backend_pid() LIMIT 1`).Scan(&backendPID); err != nil {
		t.Fatalf("find dedicated listener connection: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `SELECT pg_terminate_backend($1)`, backendPID); err != nil {
		t.Fatal(err)
	}
	if change := wait(); !change.Resync {
		t.Fatalf("reconnection wake = %+v, want resync", change)
	}
	if _, err := pool.Exec(t.Context(), `SELECT pg_notify('leapview_refresh_changed', '{"projectId":"p","environment":"prod"}')`); err != nil {
		t.Fatal(err)
	}
	if change := wait(); change.ProjectID != "p" || change.Environment != "prod" {
		t.Fatalf("post-reconnect notification wake = %+v", change)
	}
	if err := listener.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `SELECT pg_notify('leapview_refresh_changed', '{"projectId":"p","environment":"prod"}')`); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wakes:
		t.Fatal("stopped listener still delivered a wake")
	case <-time.After(100 * time.Millisecond):
	}
}
