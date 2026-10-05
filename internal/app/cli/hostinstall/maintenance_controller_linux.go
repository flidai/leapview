//go:build linux

package hostinstall

import (
	"errors"
	"os"
	"path/filepath"
)

// Legacy installed controllers predate the durable gate. While maintenance is
// pending, the supported wrapper must invoke the admitted controller instead.
// Both versions already use .leapviewctl.lock; changing this link while holding
// that lock also closes the post-crash/reboot path through the old controller.
// The immutable predecessor generation itself is never modified.
func installMaintenanceController(root, executable string) error {
	if !filepath.IsAbs(executable) || executable == filepath.Join(root, "leapviewctl") {
		return errors.New("maintenance requires a retained candidate controller")
	}
	path := filepath.Join(root, "leapviewctl")
	target, err := os.Readlink(path)
	if err != nil {
		return err
	}
	if target != "current/leapviewctl" && target != executable {
		return errors.New("unexpected installed controller binding")
	}
	return maintenanceControllerLink(root, executable)
}

func maintenanceControllerLink(root, target string) error {
	temporary := filepath.Join(root, ".maintenance-controller-link")
	if err := os.Remove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Symlink(target, temporary); err != nil {
		return err
	}
	defer os.Remove(temporary)
	if err := os.Rename(temporary, filepath.Join(root, "leapviewctl")); err != nil {
		return err
	}
	return syncPath(root)
}
