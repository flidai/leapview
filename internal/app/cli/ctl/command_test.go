package ctl

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/app/cli/composectl"
)

func TestNewCommandConstructsControllerAndHostWithoutEffects(t *testing.T) {
	root := filepath.Join(t.TempDir(), "absent-payload")
	command, err := NewCommand(context.Background(), composectl.Options{
		Root: root, DockerBin: filepath.Join(root, "absent-docker"),
		Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if command.Name() != "leapviewctl" {
		t.Fatalf("unexpected root %q", command.Name())
	}
	for _, path := range [][]string{{"init"}, {"qualify", "client-worker"}, {"host", "install"}, {"host", "admit-recovery"}, {"host", "upgrade", "migrate-copy"}} {
		found, remaining, err := command.Find(path)
		if err != nil || len(remaining) != 0 || found.Name() != path[len(path)-1] {
			t.Fatalf("missing constructed command %v: %v %v", path, remaining, err)
		}
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("construction touched payload root: %v", err)
	}
}

func TestNewCommandRetainsControllerRootValidation(t *testing.T) {
	if _, err := NewCommand(context.Background(), composectl.Options{}); err == nil {
		t.Fatal("empty controller root accepted")
	}
}
