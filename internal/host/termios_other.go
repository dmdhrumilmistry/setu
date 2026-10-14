//go:build !windows && !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package host

// termiosPasswordMode is not implemented on this platform; redaction
// patterns still apply.
func termiosPasswordMode(uintptr) bool { return false }
