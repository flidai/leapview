package hostinstall

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

type payloadFile struct {
	Source string
	Target func(Paths) string
	Mode   os.FileMode
}

var requiredPayloadFiles = []payloadFile{
	{Source: "leapviewctl", Target: func(paths Paths) string { return filepath.Join(paths.Root, "leapviewctl") }, Mode: 0o700},
	{Source: "compose.yaml", Target: func(paths Paths) string { return filepath.Join(paths.Root, "compose.yaml") }, Mode: 0o600},
	{Source: "compose.https.yaml", Target: func(paths Paths) string { return filepath.Join(paths.Root, "compose.https.yaml") }, Mode: 0o600},
	{Source: "Caddyfile", Target: func(paths Paths) string { return filepath.Join(paths.Root, "Caddyfile") }, Mode: 0o600},
	{Source: "deployment.env.example", Target: func(paths Paths) string { return filepath.Join(paths.Root, "deployment.env.example") }, Mode: 0o600},
	{Source: "leapviewctl-wrapper", Target: func(paths Paths) string { return filepath.Join(paths.SystemBin, "leapviewctl") }, Mode: 0o700},
	{Source: "leapview-backup-hook", Target: func(paths Paths) string { return filepath.Join(paths.SystemBin, "leapview-backup-hook") }, Mode: 0o700},
	{Source: "leapview-backup.service", Target: func(paths Paths) string { return filepath.Join(paths.Systemd, "leapview-backup.service") }, Mode: 0o644},
	{Source: "leapview-backup.timer", Target: func(paths Paths) string { return filepath.Join(paths.Systemd, "leapview-backup.timer") }, Mode: 0o644},
}

func readPayload(directory string) (map[string][]byte, error) {
	contents := make(map[string][]byte, len(requiredPayloadFiles))
	for _, file := range requiredPayloadFiles {
		path := filepath.Join(directory, file.Source)
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%s must be a regular file", file.Source)
		}
		contents[file.Source], err = os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if len(contents[file.Source]) == 0 {
			return nil, fmt.Errorf("%s is empty", file.Source)
		}
	}
	return contents, nil
}

func installInitialFile(path string, contents []byte, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("target %s is a symbolic link", path)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("target %s is not a regular file", path)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	return writeAtomic(path, contents, mode)
}

func installFile(path string, contents []byte, mode os.FileMode, installed bool) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("target %s is a symbolic link", path)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("target %s is not a regular file", path)
		}
		current, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if bytes.Equal(current, contents) {
			return os.Chmod(path, mode)
		}
		if installed {
			return fmt.Errorf("installed file differs from the immutable payload; use the upgrade workflow")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return writeAtomic(path, contents, mode)
}

func writeAtomic(path string, contents []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
