//go:build linux || darwin

package developmentprofile

import (
	"path/filepath"
	"syscall"
	"testing"
)

func TestLoadRejectsFIFOWithoutOpeningIt(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "profile.yaml")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := loadProfile(LoadOptions{CheckoutRoot: root, ProfileFile: path})
	assertDiagnostic(t, err, "profile.file_type", "")
}
