//go:build windows

package proc

import (
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func TestNoWindowSetsFlagsAndKeepsExisting(t *testing.T) {
	cmd := exec.Command("cmd.exe")
	NoWindow(cmd)
	a := cmd.SysProcAttr
	if a == nil || !a.HideWindow || a.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatalf("flags not set: %+v", a)
	}

	cmd = exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	NoWindow(cmd)
	if cmd.SysProcAttr.CreationFlags != windows.CREATE_NEW_PROCESS_GROUP|windows.CREATE_NO_WINDOW {
		t.Fatalf("existing flags lost: %#x", cmd.SysProcAttr.CreationFlags)
	}
}
