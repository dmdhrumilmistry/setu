//go:build !windows

package host

import (
	"os"
	"os/exec"

	"github.com/creack/pty"
)

func startPTY(command []string, cols, rows int) (*os.File, *exec.Cmd, error) {
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Env = append(os.Environ(), "SETU_SESSION=1")
	if os.Getenv("TERM") == "" {
		cmd.Env = append(cmd.Env, "TERM=xterm-256color")
	}
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, nil, err
	}
	return f, cmd, nil
}

func setSize(f *os.File, cols, rows int) error {
	return pty.Setsize(f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}
