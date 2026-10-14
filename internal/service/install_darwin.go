package service

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const launchdLabel = "io.github.dmdhrumilmistry.setu"

func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"), nil
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func renderPlist(u Unit) string {
	var args strings.Builder
	for _, a := range append([]string{u.Exe}, u.Args()...) {
		fmt.Fprintf(&args, "\n    <string>%s</string>", xmlEscape(a))
	}
	env := u.env()
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var envs strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&envs, "\n    <key>%s</key><string>%s</string>", xmlEscape(k), xmlEscape(env[k]))
	}
	crash := filepath.Join(u.Dir, "launchd.log")
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + launchdLabel + `</string>
  <key>ProgramArguments</key>
  <array>` + args.String() + `
  </array>
  <key>EnvironmentVariables</key>
  <dict>` + envs.String() + `
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>ProcessType</key><string>Standard</string>
  <key>Umask</key><integer>63</integer>
  <key>StandardOutPath</key><string>` + xmlEscape(crash) + `</string>
  <key>StandardErrorPath</key><string>` + xmlEscape(crash) + `</string>
</dict>
</plist>
`
}

func guiDomain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

// Install writes and loads a launchd agent for the current user.
func Install(u Unit, start bool) (string, error) {
	path, err := plistPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := writeFile(path, []byte(renderPlist(u)), 0o644); err != nil {
		return "", err
	}
	_ = run("launchctl", "bootout", guiDomain()+"/"+launchdLabel) // reinstall
	if start {
		if err := run("launchctl", "bootstrap", guiDomain(), path); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf(`installed launchd agent %s (starts at login)
  logs:   %s`, path, u.LogFile), nil
}

// Uninstall unloads and removes the agent.
func Uninstall() error {
	path, err := plistPath()
	if err != nil {
		return err
	}
	_ = run("launchctl", "bootout", guiDomain()+"/"+launchdLabel)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// AutostartStatus describes the autostart registration.
func AutostartStatus() string {
	path, err := plistPath()
	if err != nil {
		return err.Error()
	}
	if _, err := os.Stat(path); err != nil {
		return "not installed (run `setu service install`)"
	}
	state := "not loaded"
	if exec.Command("launchctl", "print", guiDomain()+"/"+launchdLabel).Run() == nil {
		state = "loaded"
	}
	return fmt.Sprintf("launchd agent %s: %s", path, state)
}
