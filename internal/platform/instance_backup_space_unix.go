//go:build !windows

package platform

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func instanceFilesystemFreeBytes(path string) (uint64, error) {
	var stats unix.Statfs_t
	if err := unix.Statfs(path, &stats); err != nil {
		return 0, err
	}
	return stats.Bavail * uint64(stats.Bsize), nil
}

func instanceFilesystemIdentity(path string) (string, error) {
	var stats unix.Stat_t
	if err := unix.Stat(path, &stats); err != nil {
		return "", err
	}
	return fmt.Sprintf("device:%d", stats.Dev), nil
}
