//go:build !windows

package tty

import "os"

// EnableVT is a no-op: Unix terminals always interpret VT sequences.
func EnableVT(f *os.File) func() { return func() {} }
