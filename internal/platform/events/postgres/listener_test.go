package postgres

import (
	"context"
	"errors"
	"testing"
	"time"
)

type listenerObservation struct {
	hint    string
	visible bool
	count   int
}

func TestPostgreSQL18ListenerReconcilesOnStartupReconnectAndCommit(t *testing.T) {
	db := eventTestDB(t)
	ctx := t.Context()
	r := New()
	appendEvent := func(commit bool) Event {
		t.Helper()
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		event, err := r.AppendEvent(ctx, tx, EventInput{
			ScopeID: "scope", AggregateType: "listener", AggregateID: "one",
			EventType: "listener.changed", SchemaVersion: 1, Payload: []byte(`{"opaque":true}`),
		})
		if err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if commit {
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
		} else if err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		return event
	}
	preexisting := appendEvent(true)
	listener, err := NewListener(db, ListenerOptions{MinBackoff: time.Millisecond, MaxBackoff: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	observed := make(chan listenerObservation, 10)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	failFirstHint := true
	go func() {
		done <- listener.Run(runCtx, func(reconcileCtx context.Context, hint string) error {
			identity := hint
			if identity == "" {
				identity = preexisting.EventID
			}
			var visible bool
			if err := db.QueryRow(reconcileCtx, `SELECT EXISTS (SELECT 1 FROM event.event_log WHERE event_id=$1::uuid)`, identity).Scan(&visible); err != nil {
				return err
			}
			var count int
			if err := db.QueryRow(reconcileCtx, `SELECT count(*) FROM event.event_log`).Scan(&count); err != nil {
				return err
			}
			observed <- listenerObservation{hint: hint, visible: visible, count: count}
			if hint != "" && failFirstHint {
				failFirstHint = false
				return errors.New("injected listener reconciliation failure")
			}
			return nil
		})
	}()
	wantObservation(t, observed, "", true, 1)
	first := appendEvent(true)
	wantObservation(t, observed, first.EventID, true, 2)
	// The injected callback failure forces a new connection. The listener must
	// establish LISTEN and reconcile durable state again before waiting.
	wantObservation(t, observed, "", true, 2)
	second := appendEvent(true)
	wantObservation(t, observed, second.EventID, true, 3)
	_ = appendEvent(false)
	select {
	case got := <-observed:
		t.Fatalf("rolled-back notification was observed: %#v", got)
	case <-time.After(250 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("event listener did not stop after cancellation")
	}
}

func wantObservation(t *testing.T, observed <-chan listenerObservation, hint string, visible bool, count int) {
	t.Helper()
	select {
	case got := <-observed:
		if got.hint != hint || got.visible != visible || got.count != count {
			t.Fatalf("listener observation = %#v, want hint=%q visible=%v count=%d", got, hint, visible, count)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for listener hint %q", hint)
	}
}
