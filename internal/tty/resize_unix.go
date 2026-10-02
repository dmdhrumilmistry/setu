//go:build !windows

package tty

import (
	"os"
	"os/signal"
	"syscall"
)

// NotifyResize delivers a value on ch whenever the terminal is resized.
// The returned func stops notifications.
func NotifyResize(ch chan<- struct{}) func() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-sig:
				select {
				case ch <- struct{}{}:
				default:
				}
			}
		}
	}()
	return func() { signal.Stop(sig); close(done) }
}
