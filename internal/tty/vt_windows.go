//go:build windows

package tty

import (
	"os"

	"golang.org/x/sys/windows"
)

// EnableVT turns on VT escape-sequence processing for a console output
// handle so colours, cursor movement and full-screen TUIs render correctly.
// It returns a func restoring the previous mode; it is a no-op for
// non-console handles.
func EnableVT(f *os.File) func() {
	h := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return func() {}
	}
	want := mode | windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING | windows.DISABLE_NEWLINE_AUTO_RETURN
	if windows.SetConsoleMode(h, want) != nil {
		// DISABLE_NEWLINE_AUTO_RETURN is unsupported on some builds.
		want = mode | windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING
		if windows.SetConsoleMode(h, want) != nil {
			return func() {}
		}
	}
	return func() { _ = windows.SetConsoleMode(h, mode) }
}
