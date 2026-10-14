//go:build !windows && !darwin

package policy

func filePaths() []string { return []string{"/etc/setu/policy.json"} }

// loadNative: Linux and the BSDs have no MDM preference store; MDM and
// configuration management tools deploy /etc/setu/policy.json instead.
func loadNative() (*Policy, error) { return nil, nil }
