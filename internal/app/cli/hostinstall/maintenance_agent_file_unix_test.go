//go:build !windows

package hostinstall

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestAgentTransitionRejectsFIFOWithoutBlocking(t *testing.T) {
	root, _ := agentTransitionFixture(t)
	path := filepath.Join(root, "agent-credential-transitions", "fixture.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := ReadAgentTransitionIntent(root, "fixture"); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO blocked private admission")
	}
}
