package qualificationbarrier

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWaitAfterPartialTusPatchIsInertByDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnabledEnv, "")
	t.Setenv(PathEnv, "")
	t.Setenv(ProjectIDEnv, "")
	if err := os.WriteFile(filepath.Join(dir, ArmedMarker), []byte("armed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WaitAfterPartialTusPatch(context.Background(), EvaluationProjectID); err != nil {
		t.Fatalf("WaitAfterPartialTusPatch() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ArmedMarker)); err != nil {
		t.Fatalf("armed marker changed while barrier was inert: %v", err)
	}
}

func TestWaitAfterPartialTusPatchRequiresExactEvaluationProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ArmedMarker), []byte("armed"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnabledEnv, EnabledValue)
	t.Setenv(PathEnv, dir)
	for _, projectID := range []string{"project:other", ""} {
		t.Setenv(ProjectIDEnv, projectID)
		if err := WaitAfterPartialTusPatch(context.Background(), EvaluationProjectID); err != nil {
			t.Fatalf("mismatched project error = %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ArmedMarker)); err != nil {
		t.Fatalf("armed marker consumed for mismatched project: %v", err)
	}
}

func TestWaitAfterPartialTusPatchIsOneShotAndReleases(t *testing.T) {
	dir := t.TempDir()
	armed := filepath.Join(dir, ArmedMarker)
	reached := filepath.Join(dir, ReachedMarker)
	if err := os.WriteFile(armed, []byte("armed"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnabledEnv, EnabledValue)
	t.Setenv(PathEnv, dir)
	t.Setenv(ProjectIDEnv, EvaluationProjectID)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- WaitAfterPartialTusPatch(ctx, EvaluationProjectID) }()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(reached); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("barrier was not reached")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := os.Stat(armed); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("armed marker still exists after claim: %v", err)
	}
	if err := os.Remove(reached); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("released barrier error = %v", err)
	}
	if err := WaitAfterPartialTusPatch(context.Background(), EvaluationProjectID); err != nil {
		t.Fatalf("second one-shot call error = %v", err)
	}
}

func TestWaitAfterPartialTusPatchRejectsStaleReachedMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ArmedMarker), []byte("armed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ReachedMarker), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnabledEnv, EnabledValue)
	t.Setenv(PathEnv, dir)
	t.Setenv(ProjectIDEnv, EvaluationProjectID)
	if err := WaitAfterPartialTusPatch(t.Context(), EvaluationProjectID); err == nil {
		t.Fatal("stale reached marker unexpectedly accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, ArmedMarker)); err != nil {
		t.Fatalf("armed marker changed after stale reached rejection: %v", err)
	}
}

func TestWaitAfterPartialTusPatchHonorsCancellation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ArmedMarker), []byte("armed"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnabledEnv, EnabledValue)
	t.Setenv(PathEnv, dir)
	t.Setenv(ProjectIDEnv, EvaluationProjectID)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*100)
	defer cancel()
	if err := WaitAfterPartialTusPatch(ctx, EvaluationProjectID); err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation error = %v", err)
	}
}
