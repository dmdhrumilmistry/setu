//go:build windows

package proc

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// NoWindow makes a background helper start without a console window. A
// console-subsystem program started from a process that has no console (the
// service after DetachConsole, or a detached helper) otherwise gets a new
// visible one, which flashes on screen. Flags already set on cmd are kept.
// Do not use it together with DETACHED_PROCESS, and not for ConPTY children,
// which get a pseudoconsole instead.
func NoWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}
