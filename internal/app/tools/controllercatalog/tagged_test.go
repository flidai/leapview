//go:build cgo && duckdb_arrow

package main

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/app/cli/composectl"
	"github.com/flidai/leapview/internal/app/cli/ctl"
)

func TestHostPayloadIncludesDirectTransitionHandler(t *testing.T) {
	root, err := ctl.NewCommand(context.Background(), composectl.Options{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := commandCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	upgrade := rowAt(t, rows, "host upgrade")
	if !upgrade.HasHandler {
		t.Fatal("host payload lost its direct transition handler")
	}
	for _, name := range []string{"operation-id", "candidate-image", "migration-owner-registry"} {
		found := false
		for _, flag := range upgrade.Flags {
			found = found || flag.Name == name
		}
		if !found {
			t.Fatalf("host payload lost %s flag", name)
		}
	}
}
