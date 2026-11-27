//go:build !windows

package history

import (
	"fmt"
	"os"
)

// checkPrivate refuses a history file that group or others can access: it
// says who connected from where, so it gets the same care as service.log.
func checkPrivate(f *os.File) error {
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is accessible by other users (mode %v); run: chmod 600 %q", f.Name(), st.Mode().Perm(), f.Name())
	}
	return nil
}
