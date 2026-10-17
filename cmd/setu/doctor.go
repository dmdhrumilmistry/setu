package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/client"
	"github.com/dmdhrumilmistry/setu/internal/daemon"
	"github.com/dmdhrumilmistry/setu/internal/policy"
	"github.com/dmdhrumilmistry/setu/internal/service"
	"github.com/dmdhrumilmistry/setu/internal/update"
)

// runDoctor checks this computer's setu setup (policy, version, service,
// profiles, devices, keys) and says how to fix each problem. It exits 1 if
// anything fails, so it can gate scripts and MDM compliance checks.
func runDoctor(args []string) (int, error) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print the findings as JSON (for scripts and MDM compliance checks)")
	offline := fs.Bool("offline", false, "do not check for a newer setu release")
	_ = fs.Parse(args)

	var out []service.Finding
	add := func(level, area, msg, fix string) {
		out = append(out, service.Finding{Level: level, Area: area, Msg: msg, Fix: fix})
	}
	now := time.Now()

	pol, err := policy.Load()
	switch {
	case err != nil:
		add(service.LevelFail, "policy", err.Error(), "ask your administrator to fix the managed policy")
		pol = &policy.Policy{}
	case pol.Managed():
		add(service.LevelInfo, "policy", "managed policy in force: "+pol.Source(), "setu policy")
	}

	// Version and updates.
	switch {
	case !update.Valid(version):
		add(service.LevelInfo, "version", "development build "+versionString()+": not updated automatically", "install a release (setu upgrade --force)")
	case pol.DisableUpdate:
		add(service.LevelInfo, "version", "setu "+version+"; your organization handles updates", "")
	case !*offline:
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		target, err := update.Source{BaseURL: pol.UpdateURL}.Target(ctx, version, pol.UpdateVersion)
		cancel()
		switch {
		case err != nil:
			add(service.LevelInfo, "version", "could not check for updates: "+err.Error(), "")
		case target != "":
			add(service.LevelWarn, "version", fmt.Sprintf("setu %s is available (running %s)", target, version), "setu upgrade")
		}
	}

	// Service.
	dir, err := service.Dir()
	if err != nil {
		return 1, err
	}
	cfg, err := service.Load(service.ConfigPath(dir))
	if err != nil {
		add(service.LevelFail, "service", err.Error(), "chmod 600 "+service.ConfigPath(dir))
		cfg = &service.Config{}
	}
	devices, err := service.LoadDevices(dir)
	if err != nil {
		add(service.LevelFail, "service", err.Error(), "chmod 600 "+service.DevicesPath(dir))
		devices = &service.DeviceState{}
	}
	enabled := 0
	for _, name := range cfg.Names() {
		p := cfg.Profiles[name]
		if p.Enabled {
			enabled++
		}
		if err := p.CheckPolicy(pol, now); err != nil {
			add(service.LevelFail, "profile "+name, "violates the managed policy, so it is not served: "+err.Error(), "")
		}
		out = append(out, p.Findings(name, devices.Profiles[name], now)...)
	}
	if enabled > 0 {
		if _, ok := service.RunningPID(service.LockPath(dir)); !ok {
			add(service.LevelFail, "service", fmt.Sprintf("not running, so %d profile(s) are unreachable", enabled), "setu service install")
		} else if st := service.AutostartStatus(); strings.HasPrefix(st, "not installed") {
			add(service.LevelWarn, "service", "running, but will not start after a reboot", "setu service install")
		}
		if cfg.AutoUpdate != nil && !*cfg.AutoUpdate && !pol.AutoUpdate && !pol.DisableUpdate {
			add(service.LevelWarn, "service", "automatic updates are off: security fixes wait for you", "setu upgrade auto on")
		}
		if a, err := pol.AuditConfig(cfg.Audit); err == nil && !a.Enabled() {
			add(service.LevelInfo, "service", "no audit log of who connected and what they typed", "setu service audit --endpoint https://<your collector>:4318")
		}
	}

	// This device's key, used when joining with `setu join`.
	if path, err := client.DeviceKeyPath(); err == nil && runtime.GOOS != "windows" {
		if st, err := os.Stat(path); err == nil && st.Mode().Perm()&0o077 != 0 {
			add(service.LevelFail, "device key", path+" is readable by other users, who could join as this device", "chmod 600 "+path)
		}
	}
	if shares, err := daemon.List(); err == nil && len(shares) > 0 {
		add(service.LevelInfo, "background shares", fmt.Sprintf("%d running", len(shares)), "setu ps")
	}

	fails := 0
	for _, f := range out {
		if f.Level == service.LevelFail {
			fails++
		}
	}
	code := 0
	if fails > 0 {
		code = 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if out == nil {
			out = []service.Finding{}
		}
		return code, enc.Encode(map[string]any{"version": version, "ok": fails == 0, "findings": out})
	}
	if len(out) == 0 {
		fmt.Println("\x1b[32m✔\x1b[0m everything looks good")
		return 0, nil
	}
	for _, level := range []string{service.LevelFail, service.LevelWarn, service.LevelInfo} {
		for _, f := range out {
			if f.Level != level {
				continue
			}
			mark := map[string]string{service.LevelFail: "\x1b[31m✖\x1b[0m", service.LevelWarn: "\x1b[33m!\x1b[0m", service.LevelInfo: "\x1b[36mi\x1b[0m"}[level]
			fmt.Printf("%s %s: %s\n", mark, f.Area, f.Msg)
			if f.Fix != "" {
				fmt.Printf("    fix: %s\n", f.Fix)
			}
		}
	}
	return code, nil
}
