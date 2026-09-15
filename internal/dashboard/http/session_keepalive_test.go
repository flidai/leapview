package http

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/dashboard/filter"
	dashboardsession "github.com/flidai/leapview/internal/dashboard/session"
)

func TestKeepDashboardSessionAliveTouchesAnIdleSession(t *testing.T) {
	store := &touchRecordingSessionStore{MemoryStore: dashboardsession.NewMemoryStore(), touched: make(chan struct{}, 1)}
	key := dashboardsession.Key{
		ProjectID: "workspace", PrincipalOrClient: "client", DashboardID: "dash",
		ServingStateID: "serving", StreamInstanceID: "stream",
	}
	if _, err := store.Create(context.Background(), key, dashboardsession.NewState("overview", filter.NewMachine(
		filter.ApplicationImmediate, nil,
	).Snapshot())); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// A reconnect may load a session near expiry, so renewal must not wait for
	// the first periodic interval.
	go keepDashboardSessionAlive(ctx, store, key, time.Hour, cancel)

	select {
	case <-store.touched:
	case <-time.After(time.Second):
		t.Fatal("idle dashboard session was not touched")
	}
}

func TestKeepDashboardSessionAliveCancelsOnTouchFailure(t *testing.T) {
	want := errors.New("session store unavailable")
	store := &touchRecordingSessionStore{
		MemoryStore: dashboardsession.NewMemoryStore(), touched: make(chan struct{}, 1), touchErr: want,
	}
	key := dashboardsession.Key{
		ProjectID: "workspace", PrincipalOrClient: "client", DashboardID: "dash",
		ServingStateID: "serving", StreamInstanceID: "stream",
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	failed := make(chan struct{})
	go keepDashboardSessionAlive(ctx, store, key, time.Millisecond, func() { close(failed) })

	select {
	case <-failed:
	case <-time.After(time.Second):
		t.Fatal("touch failure did not terminate the dashboard stream")
	}
	if store.lastErr != want {
		t.Fatalf("touch error = %v, want %v", store.lastErr, want)
	}
}

type touchRecordingSessionStore struct {
	*dashboardsession.MemoryStore
	touched  chan struct{}
	touchErr error
	lastErr  error
}

func (store *touchRecordingSessionStore) Touch(ctx context.Context, key dashboardsession.Key) error {
	if store.touched == nil {
		store.touched = make(chan struct{}, 1)
	}
	select {
	case store.touched <- struct{}{}:
	default:
	}
	if store.touchErr != nil {
		store.lastErr = store.touchErr
		return store.touchErr
	}
	return store.MemoryStore.Touch(ctx, key)
}
