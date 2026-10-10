//go:build !windows

package hostinstall

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func readPrivateAgentFile(root string, parts ...string) ([]byte, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, errAgentTransition
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errAgentTransition
	}
	defer func() { _ = unix.Close(fd) }()
	segments := strings.Split(strings.TrimPrefix(root, "/"), "/")
	segments = append(segments, parts...)
	privateFrom := len(strings.Split(strings.TrimPrefix(root, "/"), "/")) - 1
	for index, segment := range segments {
		if segment == "" || segment == "." || segment == ".." || strings.Contains(segment, "/") {
			return nil, errAgentTransition
		}
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC
		if index != len(segments)-1 {
			flags |= unix.O_DIRECTORY
		} else {
			flags |= unix.O_NONBLOCK
		}
		next, openErr := unix.Openat(fd, segment, flags, 0)
		if openErr != nil {
			return nil, errAgentTransition
		}
		_ = unix.Close(fd)
		fd = next
		var stat unix.Stat_t
		if unix.Fstat(fd, &stat) != nil {
			return nil, errAgentTransition
		}
		if index >= privateFrom {
			mode := uint32(0700)
			if index == len(segments)-1 {
				mode = 0600
			}
			if stat.Uid != uint32(os.Geteuid()) || stat.Mode&07777 != mode {
				return nil, errAgentTransition
			}
		} else if stat.Uid != 0 || stat.Mode&0022 != 0 {
			// Non-root unit tests may use the system sticky temporary directory.
			// A privileged deployment never accepts writable staging ancestors.
			if os.Geteuid() == 0 || !((stat.Uid == 0 && stat.Mode&unix.S_ISVTX != 0) || (stat.Uid == uint32(os.Geteuid()) && stat.Mode&0022 == 0)) {
				return nil, errAgentTransition
			}
		}
		if index == len(segments)-1 && (stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Size > 131072) {
			return nil, errAgentTransition
		}
	}
	file := os.NewFile(uintptr(fd), "private-agent-transition")
	fd = -1
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 131073))
	if err != nil || len(raw) > 131072 {
		return nil, errAgentTransition
	}
	return raw, nil
}
