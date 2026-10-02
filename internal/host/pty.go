package host

import "io"

// ptyProcess is a command running attached to a pseudo terminal: a Unix PTY
// (creack/pty) or a Windows pseudo console (ConPTY).
type ptyProcess interface {
	io.Reader // terminal output
	io.Writer // keystrokes
	Resize(cols, rows int) error
	// Wait blocks until the command exits and returns its exit code. After
	// Wait returns, Read drains the remaining output and then fails.
	Wait() int
	Kill() error
	Close() error
}
