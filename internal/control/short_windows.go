package control

import (
	"os"
	"path/filepath"
)

// shortDir is a directory for a socket whose own directory has too long a
// path. On Windows the temporary directory is already per user
// (%LOCALAPPDATA%\Temp), with an ACL that keeps other users out.
func shortDir() (string, error) {
	dir := filepath.Join(os.TempDir(), "setu")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}
