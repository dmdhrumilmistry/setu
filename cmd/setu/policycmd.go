package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/dmdhrumilmistry/setu/internal/policy"
)

// resolveCommand returns command with its program resolved to an absolute
// path when possible, so path-based policy entries can match.
func resolveCommand(command []string) []string {
	out := append([]string(nil), command...)
	if len(out) > 0 {
		if p, err := exec.LookPath(out[0]); err == nil {
			if abs, err := filepath.Abs(p); err == nil {
				out[0] = abs
			}
		}
	}
	return out
}

// runPolicy prints the managed policy in force on this device.
func runPolicy(args []string) (int, error) {
	if len(args) > 0 && args[0] != "show" {
		return 2, fmt.Errorf("usage: setu policy [show]")
	}
	p, err := policy.Load()
	if err != nil {
		return 1, err
	}
	if !p.Managed() {
		fmt.Println("no managed policy on this device")
		return 0, nil
	}
	fmt.Printf("source: %s\nhash:   %s\n\n", p.Source(), p.Hash())
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return 0, enc.Encode(p.Redacted())
}
