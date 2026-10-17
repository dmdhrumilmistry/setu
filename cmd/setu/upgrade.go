package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/policy"
	"github.com/dmdhrumilmistry/setu/internal/service"
	"github.com/dmdhrumilmistry/setu/internal/update"
)

const upgradeUsage = `Usage:
  setu upgrade [--check] [--version vX.Y.Z] [--force]   install the latest release (or the version your organization pins)
  setu upgrade auto on|off|status                      let the service install updates by itself (on by default)

The download is checked against the release's SHA256SUMS (and its signature,
when this build has a release key) and run once before it replaces this
binary. Running shares keep going on the version they started with; the
service switches to the new one only when nobody is connected.
`

func runUpgrade(args []string) (int, error) {
	if len(args) > 0 && args[0] == "auto" {
		return upgradeAuto(args[1:])
	}
	fs := flag.NewFlagSet("upgrade", flag.ExitOnError)
	check := fs.Bool("check", false, "only report whether an update is available")
	want := fs.String("version", "", "install this release instead of the latest (for example v1.4.0)")
	force := fs.Bool("force", false, "also replace a development build, which has no release version")
	fs.Usage = func() { fmt.Fprint(os.Stderr, upgradeUsage, "\n"); fs.PrintDefaults() }
	_ = fs.Parse(args)
	if fs.NArg() != 0 {
		fs.Usage()
		return 2, nil
	}
	pol, err := policy.Load()
	if err != nil {
		return 1, err
	}
	if err := pol.CheckUpdate(); err != nil {
		return 1, err
	}
	pinned := pol.UpdateVersion
	switch {
	case *want != "" && pinned != "" && *want != pinned:
		return 1, &policy.Violation{Key: "UpdateVersion", Msg: "your organization pins setu " + pinned}
	case *want != "":
		if !update.Valid(*want) {
			return 2, fmt.Errorf("--version %q: want a release version like v1.4.0", *want)
		}
		pinned = *want
	}
	if !update.Valid(version) && !*force && pinned == "" {
		return 1, fmt.Errorf("this is a development build (%s): install a release, or pass --force to replace it with the latest one", versionString())
	}
	src := update.Source{BaseURL: pol.UpdateURL}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	target, err := src.Target(ctx, version, pinned)
	if err != nil {
		return 1, err
	}
	if target == "" {
		fmt.Printf("setu %s is up to date\n", version)
		return 0, nil
	}
	if *check {
		fmt.Printf("setu %s is available (running %s); install it with `setu upgrade`\n", target, version)
		return 0, nil
	}
	exe, err := update.Executable()
	if err != nil {
		return 1, err
	}
	if !src.Signed() {
		fmt.Fprintln(os.Stderr, "note: this build has no release signing key; the download is verified against the release's checksums over https")
	}
	fmt.Fprintf(os.Stderr, "downloading setu %s…\n", target)
	if err := src.Apply(ctx, exe, target); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return 1, fmt.Errorf("%w\n(%s is not writable by you: it was probably installed by an administrator, who updates it)", err, exe)
		}
		return 1, err
	}
	fmt.Printf("setu %s installed at %s (was %s).\n", target, exe, version)
	if dir, err := service.Dir(); err == nil {
		if _, ok := service.RunningPID(service.LockPath(dir)); ok {
			fmt.Println("The service switches to it automatically as soon as nobody is connected.")
		}
	}
	fmt.Println("Shares already running keep their version until they end.")
	return 0, nil
}

func upgradeAuto(args []string) (int, error) {
	if len(args) != 1 {
		return 2, errors.New("usage: setu upgrade auto on|off|status")
	}
	pol, err := policy.Load()
	if err != nil {
		return 1, err
	}
	dir, err := service.Dir()
	if err != nil {
		return 1, err
	}
	path := service.ConfigPath(dir)
	cfg, err := service.Load(path)
	if err != nil {
		return 1, err
	}
	switch args[0] {
	case "status":
		on := cfg.AutoUpdate == nil || *cfg.AutoUpdate
		state := map[bool]string{true: "on", false: "off"}[on]
		switch {
		case pol.DisableUpdate:
			state = "off (your organization updates setu)"
		case pol.UpdateVersion != "":
			state = "pinned to " + pol.UpdateVersion + " by your organization"
		case pol.AutoUpdate:
			state = "on (required by your organization)"
		case !update.Valid(version):
			state += " (inactive: this is a development build)"
		}
		fmt.Printf("automatic updates: %s\nrunning: %s\n", state, versionString())
		if _, ok := service.RunningPID(service.LockPath(dir)); !ok {
			fmt.Println("the service is not running; it is what installs updates (setu service install)")
		}
		return 0, nil
	case "on", "off":
		on := args[0] == "on"
		if !on && pol.AutoUpdate {
			return 1, &policy.Violation{Key: "AutoUpdate", Msg: "your organization requires automatic updates"}
		}
		if on {
			if err := pol.CheckUpdate(); err != nil {
				return 1, err
			}
		}
		cfg.AutoUpdate = &on
		if err := service.Save(path, cfg); err != nil {
			return 1, err
		}
		fmt.Printf("automatic updates %s\n", args[0])
		return 0, nil
	}
	return 2, errors.New("usage: setu upgrade auto on|off|status")
}
