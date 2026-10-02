//go:build windows

package daemon

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// detach starts the child without a console and outside our process group,
// so closing this terminal window does not stop it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if windows.GetExitCodeProcess(h, &code) != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE
}

// terminate: a detached process has no console to receive Ctrl+C, so the
// process is terminated; the shared command (inside its ConPTY) goes with
// its pseudo console.
func terminate(pid int) error { return forceKill(pid) }

func forceKill(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}
