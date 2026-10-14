//go:build !windows

package policy

import (
	"fmt"
	"os"
	"syscall"
)

// checkAdminOwned refuses a policy file not owned by root, or writable by
// group or others: a user must not be able to plant or edit the policy.
func checkAdminOwned(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && sys.Uid != 0 {
		return fmt.Errorf("owned by uid %d, not root", sys.Uid)
	}
	if st.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("writable by group or others (mode %v)", st.Mode().Perm())
	}
	return nil
}
