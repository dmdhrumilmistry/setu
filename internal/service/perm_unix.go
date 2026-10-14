//go:build !windows

package service

import (
	"fmt"
	"os"
)

// checkPrivate refuses a secrets file that group or others can access.
func checkPrivate(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s holds secrets but is accessible by other users (mode %v); run: chmod 600 %q", path, st.Mode().Perm(), path)
	}
	return nil
}
