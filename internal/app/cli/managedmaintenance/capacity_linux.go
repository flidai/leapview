//go:build linux

package managedmaintenance

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func measureFilesystemCapacity(path string) (filesystemCapacity, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return filesystemCapacity{}, err
	}
	if resolved != path {
		return filesystemCapacity{}, errors.New("capacity path must not traverse symlinks")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return filesystemCapacity{}, err
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	var fs unix.Statfs_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return filesystemCapacity{}, err
	}
	if err := unix.Fstatfs(fd, &fs); err != nil {
		return filesystemCapacity{}, err
	}
	return capacityFromStat(stat.Dev, fs)
}

func capacityFromStat(device uint64, fs unix.Statfs_t) (filesystemCapacity, error) {
	if fs.Flags&unix.ST_RDONLY != 0 {
		return filesystemCapacity{}, errors.New("capacity filesystem is read-only")
	}
	// Zero inode totals mean this filesystem does not expose usable inode
	// accounting. Sentinel/invalid counters and arithmetic overflow fail closed.
	if fs.Frsize <= 0 || fs.Bsize <= 0 || fs.Blocks == 0 || fs.Bfree > fs.Blocks || fs.Bavail > fs.Bfree || fs.Files == 0 || fs.Files == math.MaxUint64 || fs.Ffree > fs.Files {
		return filesystemCapacity{}, errors.New("filesystem byte/inode accounting is unavailable")
	}
	blockSize := uint64(fs.Frsize)
	if fs.Blocks > math.MaxUint64/blockSize {
		return filesystemCapacity{}, errors.New("filesystem byte accounting overflows")
	}
	return filesystemCapacity{Device: fmt.Sprintf("linux-device:%d", device), FreeBytes: fs.Bavail * blockSize, FreeInodes: fs.Ffree}, nil
}
