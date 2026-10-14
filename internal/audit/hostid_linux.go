package audit

import (
	"os"
	"strings"
)

// machineID returns the systemd/dbus machine id, which MDM and fleet tools
// also report, so audit records can be joined with device inventory.
func machineID() string {
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if b, err := os.ReadFile(p); err == nil {
			if id := strings.TrimSpace(string(b)); id != "" {
				return id
			}
		}
	}
	return ""
}
