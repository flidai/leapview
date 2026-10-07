package composectl

import (
	"context"
	"github.com/flidai/leapview/internal/platform/hostmaintenance"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
	"os"
	"path/filepath"
	"testing"
)

func TestStartCannotBypassMaintenanceAfterControllerRestart(t *testing.T) {
	root := t.TempDir()
	for _, phase := range []string{"prepared", "capturing", "migrating", "starting", "validating", "restoring", "reopening", "committed"} {
		if err := os.WriteFile(filepath.Join(root, hostmaintenance.JournalName), []byte(`{"version":1,"state":{"phase":"`+phase+`"}}`), 0600); err != nil {
			t.Fatal(err)
		}
		// Construct a fresh controller: no process-local maintenance flag survives.
		c, err := New(Options{Root: root})
		if err != nil {
			t.Fatal(err)
		}
		started := false
		c.startOverride = func(_ context.Context) error { started = true; return nil }
		if c.Start(t.Context()) == nil || started {
			t.Fatalf("ordinary start bypassed %s", phase)
		}
	}
	if err := os.Remove(filepath.Join(root, hostmaintenance.JournalName)); err != nil {
		t.Fatal(err)
	}
	lock, err := instancelock.AcquireNamed(root, hostmaintenance.LockName)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	c, _ := New(Options{Root: root})
	if c.Start(t.Context()) == nil {
		t.Fatal("start bypassed active maintenance lock")
	}
}
