//go:build windows

package tty

// NotifyResize is a no-op on Windows (no SIGWINCH); the initial size is used.
func NotifyResize(ch chan<- struct{}) func() { return func() {} }
