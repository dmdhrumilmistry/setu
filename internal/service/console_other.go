//go:build !windows

package service

// DetachConsole is only needed on Windows.
func DetachConsole() {}
