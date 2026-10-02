//go:build windows

package tty

import (
	"os"
	"time"
)

// NotifyResize polls the console size (Windows has no SIGWINCH) and delivers
// a value on ch whenever it changes.
func NotifyResize(ch chan<- struct{}) func() {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		c, r := Size(os.Stdout)
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if nc, nr := Size(os.Stdout); nc != c || nr != r {
					c, r = nc, nr
					select {
					case ch <- struct{}{}:
					default:
					}
				}
			}
		}
	}()
	return func() { close(done) }
}
