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

func startPTY(command []string, cols, rows int) (ptyProcess, error) {
	cmd := exec.Command(command[0], command[1:]...)
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
