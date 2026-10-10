package managedrecovery

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/flidai/leapview/pkg/strictjson"
)

// InvalidateManagedAdmissionEvidence removes only our own private prior output.
// Invalid inputs or a failed new probe cannot leave a stale admission receipt.
// Unknown files, credentials, links and directories are never removed.
func InvalidateManagedAdmissionEvidence(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || validatePrivateSecretRoot(filepath.Dir(path)) != nil {
		return errors.New("private canonical managed admission output required")
	}
	raw, err := readBoundedManagedPrivateFile(path, 64<<10)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	var previous ManagedAdmissionEvidence
	if err != nil || strictjson.DecodeWithOptions(raw, &previous, strictjson.Options{MaxBytes: 64 << 10}) != nil || previous.SchemaVersion != 1 || previous.Kind != "leapview/managed-local-recovery-admission" || previous.Status != "verified-before-activation" {
		return errors.New("managed admission output contains another file or untrusted receipt")
	}
	if err := os.Remove(path); err != nil {
		return errors.New("prior managed admission output cannot be invalidated")
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
