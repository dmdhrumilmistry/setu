//go:build !windows

package daemon

import (
	"errors"
	"os/exec"
	"syscall"
)

// detach starts the child in its own session: no controlling terminal, so
// closing the shell (SIGHUP) does not stop it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// terminate asks the session to end gracefully (it tells clients and
// cleans up).
func terminate(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }

func forceKill(pid int) error { return syscall.Kill(pid, syscall.SIGKILL) }
