//go:build !windows

package main

import (
	"os"
	"syscall"
)

// reexec replaces this process with exe (same pid, so service managers do
// not notice a restart).
func reexec(exe string, args []string) error {
	return syscall.Exec(exe, append([]string{exe}, args...), os.Environ())
}
