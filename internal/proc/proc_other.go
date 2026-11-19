//go:build !windows

package proc

import "os/exec"

// NoWindow is a no-op outside Windows.
func NoWindow(*exec.Cmd) {}
