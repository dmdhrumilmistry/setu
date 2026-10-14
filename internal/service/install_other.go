//go:build !linux && !darwin && !windows

package service

import (
	"fmt"
	"runtime"
)

// Install is not supported here; run `setu service run` under the system's
// own service manager.
func Install(u Unit, _ bool) (string, error) {
	return "", fmt.Errorf("autostart is not supported on %s: run `%s service run` under your init system", runtime.GOOS, u.Exe)
}

// Uninstall is not supported here.
func Uninstall() error { return fmt.Errorf("autostart is not supported on %s", runtime.GOOS) }

// AutostartStatus describes the autostart registration.
func AutostartStatus() string { return "autostart not supported on " + runtime.GOOS }
