package sealedcontrol

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestQualificationActivationBarrierIsInertOutsideEvaluation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LEAPVIEW_HOME", home)
	if err := os.WriteFile(filepath.Join(home, QualificationActivationBarrierArmedMarker), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if err := QualificationActivationBarrier(ctx, "prod"); err != nil {
		t.Fatalf("production barrier = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(home, QualificationActivationBarrierArmedMarker)); err != nil {
		t.Fatalf("production marker was consumed: %v", err)
	}
}

func TestQualificationActivationBarrierConsumesAndReachesBeforeBlocking(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LEAPVIEW_HOME", home)
	armed := filepath.Join(home, QualificationActivationBarrierArmedMarker)
	reached := filepath.Join(home, QualificationActivationBarrierReachedMarker)
	if err := os.WriteFile(armed, []byte("armed"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { result <- QualificationActivationBarrier(ctx, "evaluation") }()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(reached); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("barrier did not publish reached marker")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := os.Stat(armed); !os.IsNotExist(err) {
		t.Fatalf("armed marker still exists: %v", err)
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("barrier error = %v, want context canceled", err)
	}
}

func TestQualificationActivationBarrierRejectsStaleReachedMarker(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LEAPVIEW_HOME", home)
	if err := os.WriteFile(filepath.Join(home, QualificationActivationBarrierArmedMarker), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, QualificationActivationBarrierReachedMarker), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := QualificationActivationBarrier(t.Context(), "evaluation"); err == nil {
		t.Fatal("stale reached marker unexpectedly accepted")
	}
}
