package runtimehost

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAcquireCutoverFenceSerializesCutover(t *testing.T) {
	manager := &Manager{}
	release, err := manager.AcquireCutoverFence(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	acquired := make(chan struct{})
	go func() {
		close(started)
		manager.cutoverMu.Lock()
		close(acquired)
		manager.cutoverMu.Unlock()
	}()
	<-started
	select {
	case <-acquired:
		t.Fatal("cutover acquired the fence while the mutation still held it")
	case <-time.After(20 * time.Millisecond):
	}

	release()
	release()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("cutover did not proceed after the mutation released the fence")
	}
}

func TestAcquireCutoverFenceHonorsCancellationWhileCutoverIsActive(t *testing.T) {
	manager := &Manager{}
	manager.cutoverMu.Lock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	release, err := manager.AcquireCutoverFence(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AcquireCutoverFence error = %v, want deadline exceeded", err)
	}
	if release != nil {
		t.Fatal("canceled fence acquisition returned a release function")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("canceled fence acquisition returned after %s", elapsed)
	}

	manager.cutoverMu.Unlock()
	deadline := time.Now().Add(time.Second)
	for {
		locked := manager.cutoverMu.TryLock()
		if locked {
			manager.cutoverMu.Unlock()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("canceled reader was not released after cutover completed")
		}
		time.Sleep(time.Millisecond)
	}
}
