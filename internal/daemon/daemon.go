// Package daemon runs `setu share` detached from the terminal and keeps a
// small per-user registry of background sessions so they can be listed,
// inspected and stopped later.
//
// Each session lives in <UserConfigDir>/setu/sessions/<id>/ (mode 0700):
//
//	info.json  pid, command, start time
//	links      invite links (0600) — they are secrets
//	log        the background process's output
//
// The background process removes its directory when it exits.
package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/secure"
)

// Info describes a background session.
type Info struct {
	ID      string    `json:"id"`
	PID     int       `json:"pid"`
	Command string    `json:"command"`
	Started time.Time `json:"started"`
	Dir     string    `json:"-"`
	Alive   bool      `json:"-"`
}

const (
	infoFile  = "info.json"
	linksFile = "links"
	logFile   = "log"
)

// Root returns the directory holding all background sessions.
func Root() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "setu", "sessions"), nil
}

// LinksPath, LogPath and InfoPath locate a session's files.
func LinksPath(dir string) string { return filepath.Join(dir, linksFile) }
func LogPath(dir string) string   { return filepath.Join(dir, logFile) }
func InfoPath(dir string) string  { return filepath.Join(dir, infoFile) }

// Register is called by the background process itself: it records its PID
// and returns a cleanup func that removes the session directory.
func Register(dir, command string) (func(), error) {
	info := Info{ID: filepath.Base(dir), PID: os.Getpid(), Command: command, Started: time.Now()}
	b, _ := json.MarshalIndent(info, "", "  ")
	if err := os.WriteFile(InfoPath(dir), b, 0o600); err != nil {
		return func() {}, err
	}
	return func() { _ = os.RemoveAll(dir) }, nil
}

// Spawn starts setu again, detached, with childArgs (which must include
// --session-dir pointing at the returned Info.Dir). secretInput, if not
// empty, is written to the child's stdin (used for the password, so it
// never appears in argv or the environment). It waits until the child has
// written its invite links, or fails with the child's log.
func Spawn(childArgs func(dir string) []string, secretInput string, timeout time.Duration) (Info, error) {
	root, err := Root()
	if err != nil {
		return Info{}, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return Info{}, err
	}
	id := secure.RandomID(4)
	dir := filepath.Join(root, id)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return Info{}, err
	}
	fail := func(err error) (Info, error) {
		_ = os.RemoveAll(dir)
		return Info{}, err
	}

	exe, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	logf, err := os.OpenFile(LogPath(dir), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fail(err)
	}
	defer logf.Close()

	cmd := exec.Command(exe, childArgs(dir)...)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.Env = withoutEnv(os.Environ(), "SETU_PASSWORD")
	detach(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fail(err)
	}
	if err := cmd.Start(); err != nil {
		return fail(fmt.Errorf("start background process: %w", err))
	}
	if secretInput != "" {
		_, _ = stdin.Write([]byte(secretInput + "\n"))
	}
	stdin.Close()

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.After(timeout)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-exited:
			out, _ := os.ReadFile(LogPath(dir))
			_ = os.RemoveAll(dir)
			return Info{}, fmt.Errorf("background session exited early (%v):\n%s", err, strings.TrimSpace(string(out)))
		case <-deadline:
			_ = cmd.Process.Kill()
			return fail(errors.New("background session did not start in time; see its log"))
		case <-tick.C:
			if st, err := os.Stat(LinksPath(dir)); err == nil && st.Size() > 0 {
				pid := cmd.Process.Pid // read before Release, which resets it
				_ = cmd.Process.Release()
				return Info{ID: id, PID: pid, Dir: dir, Alive: true}, nil
			}
		}
	}
}

func withoutEnv(env []string, key string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return out
}

// List returns all known sessions, newest first, and removes the
// directories of sessions whose process is gone.
func List() ([]Info, error) {
	root, err := Root()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		b, err := os.ReadFile(InfoPath(dir))
		if err != nil {
			// Still starting (no info yet) or broken; leave young dirs alone.
			if st, serr := os.Stat(dir); serr == nil && time.Since(st.ModTime()) > time.Minute {
				_ = os.RemoveAll(dir)
			}
			continue
		}
		var info Info
		if json.Unmarshal(b, &info) != nil {
			continue
		}
		info.Dir = dir
		info.Alive = alive(info.PID)
		if !info.Alive {
			_ = os.RemoveAll(dir)
			continue
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	return out, nil
}

// Find resolves an id (or unique id prefix).
func Find(id string) (Info, error) {
	all, err := List()
	if err != nil {
		return Info{}, err
	}
	var hits []Info
	for _, s := range all {
		if s.ID == id {
			return s, nil
		}
		if strings.HasPrefix(s.ID, id) {
			hits = append(hits, s)
		}
	}
	switch len(hits) {
	case 0:
		return Info{}, fmt.Errorf("no running background session %q (see `setu ps`)", id)
	case 1:
		return hits[0], nil
	}
	return Info{}, fmt.Errorf("%q matches several sessions; use the full id", id)
}

// Stop terminates a session and removes its directory.
func Stop(s Info) error {
	if err := terminate(s.PID); err != nil && alive(s.PID) {
		return err
	}
	for i := 0; i < 50 && alive(s.PID); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if alive(s.PID) {
		_ = forceKill(s.PID)
	}
	_ = os.RemoveAll(s.Dir)
	return nil
}
