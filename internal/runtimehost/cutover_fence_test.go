package runtimehost

import (
	"context"
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
