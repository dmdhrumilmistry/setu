package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/daemon"
	"github.com/dmdhrumilmistry/setu/internal/policy"
	"github.com/dmdhrumilmistry/setu/internal/service"
	"github.com/dmdhrumilmistry/setu/internal/update"
)

// The CLI updates itself when the always-on service is not there to do it:
// a user-facing command starts a detached `setu upgrade --auto` at most once
// a day and never waits for it. The next command reports the result once.

const (
	autoUpdateEvery   = 24 * time.Hour
	autoUpdateTimeout = 10 * time.Minute
	stampFile         = "autoupdate.stamp"
	logFileName       = "autoupdate.log"
	doneFile          = "autoupdate.done"
)

// autoUpdateState is everything the decision to check for an update depends
// on, so the decision is a pure function.
type autoUpdateState struct {
	Command, Version string
	Args             []string
	NoAutoUpdateEnv  bool // SETU_NO_AUTO_UPDATE is set
	CI               bool // CI is set
	Now, Last        time.Time
	ServiceRunning   bool
	Policy           *policy.Policy // nil: the policy could not be loaded
	AutoPref         bool           // the service config's AutoUpdate (default on)
}

// userFacingCommand reports whether command is one a person runs at a
// terminal. Updates, version and help, the background share child
// (-session-dir) and the service supervisor (service run) are not.
func userFacingCommand(command string, args []string) bool {
	switch command {
	case "share", "desktop":
		for _, a := range args {
			if a == "--" {
				break
			}
			if strings.TrimLeft(a, "-") == "session-dir" || strings.HasPrefix(strings.TrimLeft(a, "-"), "session-dir=") {
				return false
			}
		}
		return true
	case "join", "ps", "list", "ls", "stop", "link", "links", "logs", "log",
		"policy", "device", "doctor":
		return true
	case "service", "svc":
		return len(args) == 0 || args[0] != "run"
	}
	return false
}

// shouldAutoUpdate decides whether to start a background update check now.
// It mirrors what the service supervisor honours (see updater.enabled).
func shouldAutoUpdate(s autoUpdateState) bool {
	switch {
	case !userFacingCommand(s.Command, s.Args),
		s.NoAutoUpdateEnv, s.CI,
		!update.Valid(s.Version), // development builds are never updated
		s.ServiceRunning,         // the service installs updates itself
		s.Policy == nil, s.Policy.DisableUpdate:
		return false
	}
	// An organization's pin applies regardless of the user's choice.
	if s.Policy.UpdateVersion == "" && !s.Policy.AutoUpdate && !s.AutoPref {
		return false
	}
	// A stamp from the future (clock change, damaged file) must not block
	// updates for good.
	if !s.Last.IsZero() && !s.Last.After(s.Now) && s.Now.Sub(s.Last) < autoUpdateEvery {
		return false
	}
	return true
}

// autoUpdateHook runs at the start of main. Every failure is silent: the
// user's command comes first.
func autoUpdateHook(command string, args []string) {
	if !userFacingCommand(command, args) {
		return
	}
	dir, err := service.Dir()
	if err != nil {
		return
	}
	announceAutoUpdate(dir)
	if os.Getenv("SETU_NO_AUTO_UPDATE") != "" || os.Getenv("CI") != "" || !update.Valid(version) {
		return
	}
	pol, err := policy.Load()
	if err != nil {
		pol = nil
	}
	st := autoUpdateState{
		Command: command, Args: args, Version: version,
		NoAutoUpdateEnv: os.Getenv("SETU_NO_AUTO_UPDATE") != "",
		CI:              os.Getenv("CI") != "",
		Now:             time.Now(),
		Last:            readStamp(filepath.Join(dir, stampFile)),
		Policy:          pol,
	}
	_, st.ServiceRunning = service.RunningPID(service.LockPath(dir))
	if cfg, err := service.Load(service.ConfigPath(dir)); err == nil {
		st.AutoPref = cfg.AutoUpdate == nil || *cfg.AutoUpdate
	}
	if !shouldAutoUpdate(st) {
		return
	}
	// Stamp first, so a failing check or start is not retried by every
	// command.
	if err := service.EnsureDir(dir); err != nil {
		return
	}
	if writePrivate(filepath.Join(dir, stampFile), st.Now.UTC().Format(time.RFC3339)+"\n") != nil {
		return
	}
	startAutoUpgrade(dir)
}

func readStamp(path string) time.Time {
	b, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b)))
	if err != nil {
		return time.Time{}
	}
	return t
}

// startAutoUpgrade runs `setu upgrade --auto` detached, logging to the
// service directory. It does not wait for it.
func startAutoUpgrade(dir string) {
	exe, err := update.Executable()
	if err != nil {
		return
	}
	logf, err := os.OpenFile(filepath.Join(dir, logFileName), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	defer logf.Close()
	cmd := exec.Command(exe, "upgrade", "--auto")
	cmd.Stdout, cmd.Stderr = logf, logf
	for _, kv := range os.Environ() {
		// The helper only downloads a release, so it gets no SETU_* variable
		// (some carry secrets) except the one that says where its state lives.
		if !strings.HasPrefix(kv, "SETU_") || strings.HasPrefix(kv, "SETU_SERVICE_DIR=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "SETU_NO_AUTO_UPDATE=1")
	daemon.Detach(cmd)
	if err := cmd.Start(); err != nil {
		return
	}
	_ = cmd.Process.Release()
}

// updateNote is the marker a successful background install leaves behind.
type updateNote struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// announceAutoUpdate prints the result of a background install once.
func announceAutoUpdate(dir string) {
	path := filepath.Join(dir, doneFile)
	f, err := os.Open(path)
	if err != nil {
		return
	}
	b, _ := io.ReadAll(io.LimitReader(f, 4096))
	f.Close()
	_ = os.Remove(path)
	var n updateNote
	// Both versions are checked before they reach the terminal.
	if json.Unmarshal(b, &n) == nil && update.Valid(n.From) && update.Valid(n.To) {
		fmt.Fprintf(os.Stderr, "setu updated itself to %s (was %s).\n", n.To, n.From)
	}
}

// runAutoUpgrade is the hidden `setu upgrade --auto`: install the latest (or
// pinned) release without a terminal, log what happened, and leave a marker
// for the next command to report.
func runAutoUpgrade() (int, error) {
	lg := log.New(os.Stdout, "", log.LstdFlags)
	fail := func(err error) (int, error) {
		lg.Printf("auto update: %v", err)
		return 1, err
	}
	if !update.Valid(version) {
		return fail(errors.New("development build"))
	}
	pol, err := policy.Load()
	if err != nil {
		return fail(err)
	}
	if err := pol.CheckUpdate(); err != nil {
		return fail(err)
	}
	dir, err := service.Dir()
	if err != nil {
		return fail(err)
	}
	exe, err := update.Executable()
	if err != nil {
		return fail(err)
	}
	src := update.Source{BaseURL: pol.UpdateURL}
	ctx, cancel := context.WithTimeout(context.Background(), autoUpdateTimeout)
	defer cancel()
	target, err := src.Target(ctx, version, pol.UpdateVersion)
	if err != nil {
		return fail(err)
	}
	if target == "" {
		lg.Printf("auto update: setu %s is up to date", version)
		return 0, nil
	}
	lg.Printf("auto update: installing setu %s (running %s)", target, version)
	if err := src.Apply(ctx, exe, target); err != nil {
		return fail(err)
	}
	b, _ := json.Marshal(updateNote{From: version, To: target})
	if err := service.EnsureDir(dir); err == nil {
		err = writePrivate(filepath.Join(dir, doneFile), string(b)+"\n")
		if err != nil {
			lg.Printf("auto update: could not leave a note: %v", err)
		}
	}
	lg.Printf("auto update: setu %s installed", target)
	return 0, nil
}
