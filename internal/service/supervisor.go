package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/audit"
	"github.com/dmdhrumilmistry/setu/internal/daemon"
	"github.com/dmdhrumilmistry/setu/internal/host"
	"github.com/dmdhrumilmistry/setu/internal/secure"
)

const (
	pollInterval      = 2 * time.Second  // config file change detection
	reconcileInterval = 30 * time.Second // expiry and audit retry, even without changes
	minBackoff        = time.Second
	maxBackoff        = time.Minute
	stableRun         = time.Minute // a run this long resets the backoff
)

// Supervisor serves every active profile in one directory.
type Supervisor struct {
	Dir     string
	Version string
	Log     *log.Logger

	running  map[string]*runner
	audit    *audit.Logger
	auditFP  string
	auditErr error

	logged, seen map[string]bool // problem messages: already logged / seen this round

	hostHook func(*host.Config) // tests: adjust each session's config
}

type runner struct {
	fp     string
	cancel context.CancelFunc
	done   chan struct{}
}

// Run serves profiles until ctx ends. It holds the directory's lock, so only
// one supervisor serves a set of keys at a time.
func (sv *Supervisor) Run(ctx context.Context) error {
	if sv.Log == nil {
		sv.Log = log.New(os.Stderr, "", log.LstdFlags)
	}
	if err := EnsureDir(sv.Dir); err != nil {
		return err
	}
	release, err := acquireLock(LockPath(sv.Dir))
	if err != nil {
		return err
	}
	defer release()
	sv.running, sv.logged = map[string]*runner{}, map[string]bool{}
	sv.Log.Printf("setu service %s started (pid %d), config %s", sv.Version, os.Getpid(), ConfigPath(sv.Dir))

	var lastStat string
	lastReconcile := time.Time{}
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	for {
		st := statKey(ConfigPath(sv.Dir))
		if st != lastStat || time.Since(lastReconcile) >= reconcileInterval {
			lastStat, lastReconcile = st, time.Now()
			sv.reconcile(ctx)
		}
		select {
		case <-ctx.Done():
			sv.stopAll()
			if err := sv.audit.Shutdown(context.Background()); err != nil {
				sv.Log.Printf("audit flush: %v", err)
			}
			sv.Log.Printf("setu service stopped")
			return nil
		case <-tick.C:
		}
	}
}

func statKey(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return "missing"
	}
	return fmt.Sprintf("%d/%d", st.Size(), st.ModTime().UnixNano())
}

// reconcile makes the running set match the configuration.
func (sv *Supervisor) reconcile(ctx context.Context) {
	sv.seen = map[string]bool{}
	defer func() {
		// Forget problems that went away, so they are logged if they recur.
		for m := range sv.logged {
			if !sv.seen[m] {
				delete(sv.logged, m)
			}
		}
	}()
	cfg, err := Load(ConfigPath(sv.Dir))
	if err != nil {
		sv.logOnce("config error (keeping current shares): %v", err)
		return
	}

	// A changed audit configuration restarts everything, so no session is
	// split across two pipelines. An enabled but unusable audit pipeline
	// fails closed: nothing is served until it works.
	if fp := fingerprint(cfg.Audit); fp != sv.auditFP || sv.auditErr != nil {
		sv.stopAll()
		_ = sv.audit.Shutdown(context.Background())
		sv.audit, sv.auditErr = audit.New(ctx, cfg.Audit, audit.Options{Version: sv.Version, Logf: sv.Log.Printf})
		sv.auditFP = fp
		switch {
		case sv.auditErr != nil:
			sv.logOnce("audit unavailable, not serving any profile until it works (retrying): %v", sv.auditErr)
			return
		case cfg.Audit.Enabled():
			sv.Log.Printf("audit: exporting to %s", cfg.Audit.Endpoint)
		}
	}

	now := time.Now()
	desired := map[string]string{}
	for _, name := range cfg.Names() {
		p := cfg.Profiles[name]
		if !p.Active(now) {
			continue
		}
		if err := p.Validate(); err != nil {
			sv.logOnce("profile %s: %v (not served)", name, err)
			continue
		}
		desired[name] = fingerprint(p)
	}
	for name, r := range sv.running {
		if fp, ok := desired[name]; !ok || fp != r.fp {
			sv.Log.Printf("profile %s: stopping (removed, disabled, expired or changed)", name)
			r.stop()
			delete(sv.running, name)
		}
	}
	for name, fp := range desired {
		if _, ok := sv.running[name]; ok {
			continue
		}
		rctx, cancel := context.WithCancel(ctx)
		r := &runner{fp: fp, cancel: cancel, done: make(chan struct{})}
		sv.running[name] = r
		go func(name string, p *Profile, al *audit.Logger) {
			defer close(r.done)
			sv.serve(rctx, name, p, al)
		}(name, cfg.Profiles[name], sv.audit)
	}
}

// logOnce logs a problem the first time it is seen, not on every pass.
func (sv *Supervisor) logOnce(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	sv.seen[msg] = true
	if !sv.logged[msg] {
		sv.logged[msg] = true
		sv.Log.Print(msg)
	}
}

func (r *runner) stop() {
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(10 * time.Second):
	}
}

func (sv *Supervisor) stopAll() {
	var wg sync.WaitGroup
	for name, r := range sv.running {
		wg.Add(1)
		go func(r *runner) {
			defer wg.Done()
			r.stop()
		}(r)
		delete(sv.running, name)
	}
	wg.Wait()
}

// serve runs one profile's command, restarting it with backoff whenever it
// exits, until ctx ends or the keys expire.
func (sv *Supervisor) serve(ctx context.Context, name string, p *Profile, al *audit.Logger) {
	if !p.ExpiresAt.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, p.ExpiresAt)
		defer cancel()
	}
	out := &prefixWriter{l: sv.Log, prefix: name + ": "}
	backoff := minBackoff
	for {
		cfg, err := hostConfig(name, p)
		if err != nil {
			sv.Log.Printf("profile %s: %v", name, err)
			return
		}
		cfg.Audit, cfg.Log = al, out
		if sv.hostHook != nil {
			sv.hostHook(&cfg)
		}
		start := time.Now()
		s, err := host.New(cfg)
		if err == nil {
			sv.Log.Printf("profile %s: serving %q", name, strings.Join(p.Command, " "))
			var code int
			code, err = s.Run(ctx)
			if err == nil {
				sv.Log.Printf("profile %s: command exited with code %d", name, code)
			}
		}
		if err != nil {
			sv.Log.Printf("profile %s: %v", name, err)
		}
		if ctx.Err() != nil {
			if !p.ExpiresAt.IsZero() && !time.Now().Before(p.ExpiresAt) {
				sv.Log.Printf("profile %s: keys expired; rotate them with `setu service rotate %s`", name, name)
			}
			return
		}
		if time.Since(start) >= stableRun {
			backoff = minBackoff
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

func hostConfig(name string, p *Profile) (host.Config, error) {
	cs, err := secure.ParseSecret(p.ControlKey)
	if err != nil {
		return host.Config{}, err
	}
	cfg := host.Config{
		Command:          p.Command,
		Dir:              p.Dir,
		Profile:          name,
		ControlSecret:    &cs,
		Relays:           p.relays(),
		ICE:              p.ICE(),
		PasswordVerifier: p.Password,
		MaxClients:       p.MaxClients,
	}
	if p.ViewKey != "" {
		vs, err := secure.ParseSecret(p.ViewKey)
		if err != nil {
			return host.Config{}, err
		}
		cfg.ViewSecret = &vs
	}
	return cfg, nil
}

// prefixWriter turns host log output into timestamped, per-profile lines.
type prefixWriter struct {
	l      *log.Logger
	prefix string
}

func (w *prefixWriter) Write(b []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(b), "\r\n"), "\n") {
		if line = strings.TrimRight(line, "\r"); line != "" {
			w.l.Print(w.prefix + line)
		}
	}
	return len(b), nil
}

// acquireLock creates a pid file, replacing one left by a dead process.
func acquireLock(path string) (release func(), err error) {
	for range 2 {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, werr := fmt.Fprintf(f, "%d\n", os.Getpid())
			if cerr := f.Close(); werr == nil {
				werr = cerr
			}
			if werr != nil {
				os.Remove(path)
				return nil, werr
			}
			return func() { os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if pid, ok := RunningPID(path); ok {
			return nil, fmt.Errorf("setu service is already running (pid %d)", pid)
		}
		os.Remove(path) // stale
	}
	return nil, errors.New("could not acquire the service lock")
}

// RunningPID reports the pid of a live supervisor holding the lock file.
func RunningPID(lockPath string) (int, bool) {
	b, err := os.ReadFile(lockPath)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, daemon.Alive(pid)
}

// OpenLog opens the service log for appending, rotating it once it exceeds
// 10 MiB (one previous file is kept as service.log.1).
func OpenLog(path string) (*os.File, error) {
	if st, err := os.Stat(path); err == nil && st.Size() > 10<<20 {
		_ = os.Rename(path, path+".1")
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}
