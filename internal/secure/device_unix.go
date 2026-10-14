//go:build !windows

package secure

import (
	"fmt"
	"os"
)

func checkKeyFileMode(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is accessible by other users (mode %v); run: chmod 600 %q", path, st.Mode().Perm(), path)
	}
	return nil
}
