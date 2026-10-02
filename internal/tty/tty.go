// Package tty contains small terminal helpers shared by host and client.
package tty

import (
	"os"

	"golang.org/x/term"
)

// IsTerminal reports whether f is a terminal.
func IsTerminal(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

// MakeRaw puts f into raw mode and returns a restore func.
func MakeRaw(f *os.File) (func(), error) {
	st, err := term.MakeRaw(int(f.Fd()))
	if err != nil {
		return func() {}, err
	}
	return func() { _ = term.Restore(int(f.Fd()), st) }, nil
}

// Size returns the terminal size of f, or a sane default.
func Size(f *os.File) (cols, rows int) {
	c, r, err := term.GetSize(int(f.Fd()))
	if err != nil || c <= 0 || r <= 0 {
		return 120, 40
	}
	return c, r
}

// ReadPassword reads a line from the terminal without echo.
func ReadPassword(f *os.File) (string, error) {
	b, err := term.ReadPassword(int(f.Fd()))
	return string(b), err
}

// Escape detects the ssh-style "<Enter> ~ ." sequence in a keystroke stream.
// "~~" sends a literal tilde.
type Escape struct {
	started      bool
	notLineStart bool
	pendingTilde bool
}

// Filter returns the bytes to forward and whether the escape was typed.
func (e *Escape) Filter(in []byte) (out []byte, detach bool) {
	out = make([]byte, 0, len(in))
	for _, c := range in {
		if e.pendingTilde {
			e.pendingTilde = false
			switch c {
			case '.':
				return out, true
			case '~':
				out = append(out, '~')
				e.notLineStart = true
				continue
			default:
				out = append(out, '~')
			}
		}
		if !e.notLineStart && c == '~' {
			e.pendingTilde = true
			continue
		}
		out = append(out, c)
		e.notLineStart = !(c == '\r' || c == '\n')
	}
	return out, false
}
