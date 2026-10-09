//go:build !windows

package managedrecovery

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func configureRestoreProcess(command *exec.Cmd) error {
	// Start the tool in a new session directly, without a forked setsid waiter.
	// Its SFTP terminal handling cannot change the controller's foreground group.
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	command.WaitDelay = 5 * time.Second
	return nil
}
