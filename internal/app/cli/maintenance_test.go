package cli

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMaintenanceListenerIsPrivateAndRejectsNonSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix maintenance control")
	}
	dir, err := os.MkdirTemp("", "lv-control-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "control.sock")
	listener, server, err := maintenanceListener(path, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("socket permissions=%v", info.Mode())
	}
	_ = listener.Close()
	if err = os.WriteFile(path, []byte("operator data"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = maintenanceListener(path, http.NotFoundHandler()); err == nil {
		t.Fatal("replaced a regular operator file")
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "operator data" {
		t.Fatal("changed operator data")
	}
}
