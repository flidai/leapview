//go:build !cgo || !duckdb_arrow

package main

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/app/cli/composectl"
	"github.com/flidai/leapview/internal/app/cli/ctl"
)

func TestStandaloneIncludesMaintenanceGroupWithoutDirectTransition(t *testing.T) {
	root, err := ctl.NewCommand(context.Background(), composectl.Options{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := commandCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	upgrade := rowAt(t, rows, "host upgrade")
	if upgrade.HasHandler || len(upgrade.Flags) != 0 {
		t.Fatalf("standalone unexpectedly includes direct transition capabilities: %#v", upgrade)
	}
	if !rowAt(t, rows, "host upgrade apply").HasHandler {
		t.Fatal("standalone lost maintenance apply")
	}
}
