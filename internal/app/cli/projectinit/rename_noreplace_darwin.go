//go:build darwin

package projectinit

import "golang.org/x/sys/unix"

func renameNoReplace(parent uintptr, source, destination string) error {
	return unix.RenameatxNp(int(parent), source, int(parent), destination, unix.RENAME_EXCL)
}
