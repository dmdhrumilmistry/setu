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
	// Only VT processing is added: DISABLE_NEWLINE_AUTO_RETURN would make a
	// plain "\n" skip the carriage return and staircase ordinary output.
	if windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) != nil {
		return func() {}
	}
	return func() { _ = windows.SetConsoleMode(h, mode) }
}

// DisableNewlineAutoReturn sets DISABLE_NEWLINE_AUTO_RETURN on a console
// output handle. Use it only while a remote PTY stream (which already sends
// \r\n) is copied raw to the console: auto-return there misplaces the cursor
// at the last column. It returns a func restoring the previous mode and is a
// no-op for non-console handles or builds that do not support the flag.
func DisableNewlineAutoReturn(f *os.File) func() {
	h := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return func() {}
	}
	if windows.SetConsoleMode(h, mode|windows.DISABLE_NEWLINE_AUTO_RETURN) != nil {
		return func() {}
	}
	return func() { _ = windows.SetConsoleMode(h, mode) }
}
