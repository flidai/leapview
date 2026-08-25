//go:build windows

package platform

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func instanceFilesystemFreeBytes(path string) (uint64, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var available uint64
	if err := windows.GetDiskFreeSpaceEx(pointer, &available, nil, nil); err != nil {
		return 0, err
	}
	return available, nil
}

func instanceFilesystemIdentity(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return strings.ToLower(filepath.VolumeName(abs)), nil
}
