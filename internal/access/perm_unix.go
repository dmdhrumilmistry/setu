//go:build !windows

package access

import (
	"fmt"
	"os"
)

// checkPrivate refuses a rules file that group or others can access: anyone
// who could edit it could let themselves in or lock the owner out.
func checkPrivate(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is accessible by other users (mode %v); run: chmod 600 %q", path, st.Mode().Perm(), path)
	}
	return nil
}
