//go:build windows

package managedrecovery

import (
	"errors"
	"os/exec"
)

func configureRestoreProcess(*exec.Cmd) error {
	return errors.New("managed local restore requires a supported Unix host")
}
