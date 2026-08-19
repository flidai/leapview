package gcadapter

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestMaintenanceRunsAndSurfacesDegradedError(t *testing.T) {
	var calls atomic.Int32
	want := errors.New("gc failed")
	m, err := NewMaintenance(func(context.Context) error {
		calls.Add(1)
		return want
	}, 10*time.Millisecond, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer m.Stop(context.Background())
	deadline := time.Now().Add(time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls.Load() == 0 {
		t.Fatal("maintenance did not run")
	}
	if !errors.Is(m.Health(), want) {
		t.Fatalf("health=%v, want %v", m.Health(), want)
	}
}

func TestMaintenanceRunsOnceDuringStartup(t *testing.T) {
	called := make(chan struct{}, 1)
	m, err := NewMaintenance(func(context.Context) error {
		called <- struct{}{}
		return nil
	}, time.Second, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer m.Stop(context.Background())

	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("maintenance did not run during process startup")
	}
}
