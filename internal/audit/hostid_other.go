//go:build !linux && !darwin && !windows

package audit

// machineID is not implemented on this platform.
func machineID() string { return "" }
