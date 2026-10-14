package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const systemdUnit = "setu.service"

func unitPath() (string, error) {
	base, err := os.UserConfigDir() // $XDG_CONFIG_HOME or ~/.config
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "systemd", "user", systemdUnit), nil
}

// systemdQuote quotes one word for an ExecStart= or Environment= line.
func systemdQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`, `$`, `$$`)
	return `"` + r.Replace(s) + `"`
}

func renderSystemd(u Unit) string {
	u.LogFile = "" // systemd collects stdout/stderr in the journal
	words := []string{systemdQuote(u.Exe)}
	for _, a := range u.Args() {
		words = append(words, systemdQuote(a))
	}
	env := u.env()
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var envLines strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&envLines, "Environment=%s\n", systemdQuote(k+"="+env[k]))
	}
	return `[Unit]
Description=setu background terminal shares
Documentation=https://github.com/dmdhrumilmistry/setu
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=` + strings.Join(words, " ") + `
` + envLines.String() + `WorkingDirectory=%h
Restart=on-failure
RestartSec=5
UMask=0077
# The shared shells run under this unit; stopping it ends them all.
KillMode=control-group
TimeoutStopSec=20

[Install]
WantedBy=default.target
`
}

// Install writes and starts a systemd user unit.
func Install(u Unit, start bool) (string, error) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return "", fmt.Errorf("systemctl not found: run `%s service run` under your init system instead", u.Exe)
	}
	path, err := unitPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := writeFile(path, []byte(renderSystemd(u)), 0o644); err != nil {
		return "", err
	}
	if err := run("systemctl", "--user", "daemon-reload"); err != nil {
		return "", err
	}
	args := []string{"--user", "enable", systemdUnit}
	if start {
		args = []string{"--user", "enable", "--now", systemdUnit}
	}
	if err := run("systemctl", args...); err != nil {
		return "", err
	}
	return fmt.Sprintf(`installed systemd user unit %s
  logs:   journalctl --user -u setu -f
  status: systemctl --user status setu
To keep it running while you are logged out (and start it at boot):
  loginctl enable-linger %s`, path, os.Getenv("USER")), nil
}

// Uninstall stops and removes the unit.
func Uninstall() error {
	path, err := unitPath()
	if err != nil {
		return err
	}
	_ = run("systemctl", "--user", "disable", "--now", systemdUnit)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return run("systemctl", "--user", "daemon-reload")
}

// AutostartStatus describes the autostart registration.
func AutostartStatus() string {
	path, err := unitPath()
	if err != nil {
		return err.Error()
	}
	if _, err := os.Stat(path); err != nil {
		return "not installed (run `setu service install`)"
	}
	out, _ := exec.Command("systemctl", "--user", "is-active", systemdUnit).Output()
	return fmt.Sprintf("systemd user unit %s: %s", path, strings.TrimSpace(string(out)))
}
