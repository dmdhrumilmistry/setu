package policy

import (
	"fmt"
	"os"
	"os/exec"
)

// ManagedPrefsPath is where macOS materialises a configuration profile
// payload whose PayloadType is Domain (device-level profiles).
const ManagedPrefsPath = "/Library/Managed Preferences/" + Domain + ".plist"

func filePaths() []string {
	return []string{"/Library/Application Support/setu/policy.json"}
}

// loadNative reads the managed preferences an MDM configuration profile
// installed. plutil converts the (usually binary) plist to JSON.
func loadNative() (*Policy, error) {
	if _, err := os.Stat(ManagedPrefsPath); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("managed policy %s: %w", ManagedPrefsPath, err)
	}
	if err := checkAdminOwned(ManagedPrefsPath); err != nil {
		return nil, fmt.Errorf("managed policy %s: %w", ManagedPrefsPath, err)
	}
	out, err := exec.Command("/usr/bin/plutil", "-convert", "json", "-o", "-", ManagedPrefsPath).Output()
	if err != nil {
		return nil, fmt.Errorf("managed policy %s: plutil: %w", ManagedPrefsPath, err)
	}
	return parseProfilePrefs(out, ManagedPrefsPath)
}
