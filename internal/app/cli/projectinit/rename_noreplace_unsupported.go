//go:build !linux && !darwin

package projectinit

import (
	"errors"
	"runtime"
)

func renameNoReplace(_ uintptr, _, _ string) error {
	return errors.New("atomic project initialization is unsupported on " + runtime.GOOS)
}
