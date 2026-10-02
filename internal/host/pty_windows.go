//go:build windows

package host

import (
	"errors"
	"os"
	"os/exec"
)

func startPTY(command []string, cols, rows int) (*os.File, *exec.Cmd, error) {
	return nil, nil, errors.New("sharing a terminal from Windows is not supported yet (use WSL); `setu join` works")
}

func setSize(f *os.File, cols, rows int) error { return nil }
