//go:build linux

package projectinit

import "golang.org/x/sys/unix"

func renameNoReplace(parent uintptr, source, destination string) error {
	return unix.Renameat2(int(parent), source, int(parent), destination, unix.RENAME_NOREPLACE)
}
