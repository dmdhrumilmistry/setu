package main

import (
	"os"
	"os/exec"
	"syscall"
)

// reexec starts exe detached and lets this process exit: Windows cannot
// replace a running process image.
func reexec(exe string, args []string) error {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x00000008 | 0x00000200} // DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
