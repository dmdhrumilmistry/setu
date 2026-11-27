//go:build !windows

package control

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// shortDir is a private per-user directory under /tmp (or $TMPDIR) for a
// socket whose own directory has too long a path. The temporary directory
// is shared with other users, so the directory is created 0700 and, if it
// already exists, used only when it is a real directory (not a symlink)
// owned by this user that nobody else can enter.
func shortDir() (string, error) {
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("setu-%d", os.Getuid()))
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return "", err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !st.IsDir() || !ok || int(sys.Uid) != os.Getuid() || st.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s is not a private directory of this user; refusing to put a control socket there", dir)
	}
	return dir, nil
}
