//go:build windows

package managedrecovery

import (
	"errors"
	"os"
	"os/exec"
)

func managedFileOwned(os.FileInfo) bool { return false }

func configureRestoreProcess(*exec.Cmd) error {
	return errors.New("managed local restore requires a supported Unix host")
}
