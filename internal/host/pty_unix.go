//go:build !windows

package host

import (
	"errors"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

type unixPTY struct {
	f   *os.File
	cmd *exec.Cmd
}

func startPTY(command []string, dir string, cols, rows int) (ptyProcess, error) {
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "SETU_SESSION=1")
	if os.Getenv("TERM") == "" {
		cmd.Env = append(cmd.Env, "TERM=xterm-256color")
	}
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, err
	}
	return &unixPTY{f: f, cmd: cmd}, nil
}

func (p *unixPTY) Read(b []byte) (int, error)  { return p.f.Read(b) }
func (p *unixPTY) Write(b []byte) (int, error) { return p.f.Write(b) }
func (p *unixPTY) Close() error                { return p.f.Close() }
func (p *unixPTY) Kill() error                 { return p.cmd.Process.Kill() }

// PasswordMode reports whether the terminal is in canonical mode with echo
// off, which is how getpass-style password prompts read. Line editors and
// full-screen apps (readline, Claude Code) turn canonical mode off too, so
// their input is still audited.
func (p *unixPTY) PasswordMode() bool {
	// SyscallConn, not Fd: Fd would switch the PTY to blocking mode.
	rc, err := p.f.SyscallConn()
	if err != nil {
		return false
	}
	pw := false
	_ = rc.Control(func(fd uintptr) { pw = termiosPasswordMode(fd) })
	return pw
}

func (p *unixPTY) Resize(cols, rows int) error {
	return pty.Setsize(p.f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

func (p *unixPTY) Wait() int {
	err := p.cmd.Wait()
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}

// DefaultShell is used when no command is given.
func DefaultShell() []string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return []string{sh}
	}
	return []string{"/bin/sh"}
}
