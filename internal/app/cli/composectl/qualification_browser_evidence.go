package composectl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

func prepareQualificationBrowserEvidence(options qualificationAuthoringOptions, cleanup *qualificationCleanup) (string, error) {
	if options.PreloadedBrowserImage == "" {
		return options.EvidenceDir, nil
	}
	return privateQualificationBrowserEvidence(options.EvidenceDir, 1000, 1000, cleanup)
}

// Only this child is writable by the image's unprivileged browser user. The
// private parent still protects host-side access to all retained evidence.
func privateQualificationBrowserEvidence(root string, uid, gid int, cleanup *qualificationCleanup) (string, error) {
	if runtime.GOOS != "linux" {
		return "", errors.New("preloaded browser qualification requires a Linux host")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 || info.Mode()&os.ModeSetgid != 0 {
		return "", errors.New("preloaded browser requires a private evidence directory with mode 0700")
	}
	path := filepath.Join(root, "browser")
	if err := os.Mkdir(path, 0o700); err != nil {
		return "", err
	}
	// With no setgid inheritance, the new child belongs to this process.
	ownerUID, ownerGID := os.Geteuid(), os.Getegid()
	if err := os.Chown(path, uid, gid); err != nil {
		return "", errors.Join(err, os.Remove(path))
	}
	// Added before container creation, hence run after browser removal by the
	// reverse-order cleanup even if setup fails before the worker starts.
	cleanup.Add(func(context.Context) error {
		return os.Chown(path, ownerUID, ownerGID)
	})
	return path, nil
}
