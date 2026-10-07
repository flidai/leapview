//go:build linux

package hostinstall

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyControllerGateSurvivesProcessRestart(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "leapviewctl")
	if err := os.Symlink("current/leapviewctl", path); err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(t.TempDir(), "candidate-controller")
	if err := installMaintenanceController(root, candidate); err != nil {
		t.Fatal(err)
	}
	// A second controller process must accept only the same admitted controller.
	if err := installMaintenanceController(root, candidate); err != nil {
		t.Fatal(err)
	}
	if err := installMaintenanceController(root, candidate+"-different"); err == nil {
		t.Fatal("changed controller accepted")
	}
	if got, _ := os.Readlink(path); got != candidate {
		t.Fatal(got)
	}
	if err := maintenanceControllerLink(root, "current/leapviewctl"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.Readlink(path); got != "current/leapviewctl" {
		t.Fatal(got)
	}
}
