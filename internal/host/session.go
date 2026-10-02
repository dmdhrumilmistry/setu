// Package host shares a local PTY with remote peers over WebRTC.
package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/proto"
	"github.com/dmdhrumilmistry/setu/internal/secure"
	"github.com/dmdhrumilmistry/setu/internal/tty"
)

// Invite is handed to Config.OnInvite so the CLI can print links / QR codes.
type Invite struct {
	Role string // proto.RoleControl or proto.RoleView
	Link string
	Code string // manual mode: the raw offer code
}

// Config controls a sharing session.
type Config struct {
	Command []string

	// Signaling.
	Manual        bool           // copy/paste signaling instead of nostr
	ControlSecret *secure.Secret // nostr mode: full-control invite
	ViewSecret    *secure.Secret // nostr mode: optional read-only invite
	Relays        []string
	ICE           []proto.ICEServer

	// Access control.
	Password   string        // empty = no password
	MaxClients int           // concurrent authenticated clients
	Once       bool          // disable invites after the first successful join
	Approve    bool          // ask on the host terminal before admitting a client
	Expire     time.Duration // disable invites after this long (0 = never)

	// Local terminal.
	Mirror bool // attach the host terminal to the shared PTY

	OnInvite   func(Invite)
	ReadAnswer func(ctx context.Context) (string, error) // manual mode
	Log        io.Writer
	Verbose    bool
}

const (
	maxPendingHandshakes = 3
	maxAuthFailures      = 5
	lockoutDuration      = 10 * time.Minute
	maxTotalFailures     = 20
	replayBytes          = 256 << 10
	handshakeTimeout     = 2 * time.Minute
)

// Session is a running share.
type Session struct {
	cfg Config

	ptmx *os.File
	cmd  *exec.Cmd

	mu          sync.Mutex
	peers       map[int]*peer
	nextID      int
	ring        []byte
	cols, rows  int
	accepting   bool
	failures    int
	totalFails  int
	lockedUntil time.Time
	stopSignal  context.CancelFunc

	inputMu sync.Mutex

	pwKey  []byte
	pwSalt []byte
	host   string

	router    *stdinRouter
	approval  sync.Mutex
	rawMode   bool
	mirroring bool
	logMu     sync.Mutex
}

// New validates cfg and prepares a session.
func New(cfg Config) (*Session, error) {
	if len(cfg.Command) == 0 {
		return nil, errors.New("no command to run")
	}
	if !cfg.Manual && cfg.ControlSecret == nil {
		return nil, errors.New("nostr mode needs a control secret")
	}
	if cfg.MaxClients <= 0 {
		cfg.MaxClients = 1
	}
	if cfg.Manual {
		cfg.MaxClients = 1
		cfg.Approve = false // pasting the answer code on the host is the approval
	}
	if cfg.Log == nil {
		cfg.Log = os.Stderr
	}
	s := &Session{cfg: cfg, peers: map[int]*peer{}, accepting: true, router: &stdinRouter{}}
	if cfg.Password != "" {
		s.pwSalt = secure.RandomBytes(16)
		k, err := secure.PasswordKey(cfg.Password, s.pwSalt, secure.PBKDF2Iterations)
		if err != nil {
			return nil, err
		}
		s.pwKey = k
		s.cfg.Password = "" // keep only the derived key in memory
	}
	s.host, _ = os.Hostname()
	return s, nil
}

func (s *Session) logf(format string, args ...any) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	msg := fmt.Sprintf(format, args...)
	if s.rawMode {
		fmt.Fprintf(s.cfg.Log, "\r\n\x1b[1;36m[setu]\x1b[0m %s\r\n", strings.ReplaceAll(msg, "\n", "\r\n"))
	} else {
		fmt.Fprintf(s.cfg.Log, "[setu] %s\n", msg)
	}
}

func (s *Session) debugf(format string, args ...any) {
	if s.cfg.Verbose {
		s.logf(format, args...)
	}
}

// Run starts the command and serves peers until it exits or ctx ends. It
// returns the command's exit code.
func (s *Session) Run(ctx context.Context) (int, error) {
	cols, rows := 120, 40
	if s.cfg.Mirror {
		cols, rows = tty.Size(os.Stdout)
	}
	ptmx, cmd, err := startPTY(s.cfg.Command, cols, rows)
	if err != nil {
		return 1, err
	}
	s.ptmx, s.cmd, s.cols, s.rows = ptmx, cmd, cols, rows
	defer ptmx.Close()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	exited := make(chan int, 1)
	go func() {
		code := 0
		if err := cmd.Wait(); err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			} else {
				code = 1
			}
		}
		exited <- code
	}()

	readDone := make(chan struct{})
	go s.pumpOutput(readDone)

	sigCtx, stopSignal := context.WithCancel(ctx)
	s.mu.Lock()
	s.stopSignal = stopSignal
	s.mu.Unlock()
	if s.cfg.Expire > 0 {
		t := time.AfterFunc(s.cfg.Expire, func() { s.stopAccepting("invite expired") })
		defer t.Stop()
	}

	signalErr := make(chan error, 1)
	if s.cfg.Manual {
		if err := s.serveManual(sigCtx); err != nil {
			_ = cmd.Process.Kill()
			return 1, err
		}
		defer s.attachLocal(ctx, cancel)()
	} else {
		go func() { signalErr <- s.serveNostr(sigCtx) }()
		if s.cfg.OnInvite != nil {
			s.cfg.OnInvite(Invite{Role: proto.RoleControl})
		}
		defer s.attachLocal(ctx, cancel)()
	}

	var code int
	select {
	case code = <-exited:
	case err := <-signalErr:
		if err != nil && ctx.Err() == nil {
			_ = cmd.Process.Kill()
			<-exited
			s.closeAll("host error")
			return 1, err
		}
		code = <-exited
	case <-ctx.Done():
		// Host ended the share (~. or a signal): not a command failure.
		_ = cmd.Process.Kill()
		<-exited
		code = 0
	}
	select {
	case <-readDone:
	case <-time.After(500 * time.Millisecond):
	}
	s.broadcastExit(code)
	time.Sleep(200 * time.Millisecond) // let the exit frame flush
	s.closeAll("session ended")
	return code, nil
}

// attachLocal mirrors the PTY on the host terminal (raw mode) and routes
// host keystrokes. "<Enter> ~ ." on the host ends the share. The returned
// func restores the terminal and must run before the process exits.
func (s *Session) attachLocal(ctx context.Context, cancel context.CancelFunc) (restore func()) {
	restore = func() {}
	if !s.cfg.Mirror && !s.cfg.Approve {
		return restore
	}
	if s.cfg.Mirror {
		if r, err := tty.MakeRaw(os.Stdin); err == nil {
			s.logMu.Lock()
			s.rawMode = true
			s.logMu.Unlock()
			restore = func() {
				s.logMu.Lock()
				s.rawMode = false
				s.logMu.Unlock()
				r()
			}
		}
		var esc tty.Escape
		s.router.setForward(func(b []byte) {
			out, detach := esc.Filter(b)
			if len(out) > 0 {
				s.writeInput(out)
			}
			if detach {
				s.logf("host typed ~. — ending share")
				cancel()
			}
		})
		s.mu.Lock()
		os.Stdout.Write(s.ring) // catch the host terminal up
		s.mirroring = true
		s.mu.Unlock()
		resize := make(chan struct{}, 1)
		stop := tty.NotifyResize(resize)
		go func() {
			defer stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-resize:
					c, r := tty.Size(os.Stdout)
					s.resize(c, r)
				}
			}
		}()
	}
	go s.router.run(os.Stdin)
	return restore
}

func (s *Session) pumpOutput(done chan<- struct{}) {
	defer close(done)
	buf := make([]byte, 32<<10)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			s.mu.Lock()
			s.ring = append(s.ring, chunk...)
			if len(s.ring) > replayBytes {
				s.ring = append([]byte(nil), s.ring[len(s.ring)-replayBytes:]...)
			}
			mirror := s.mirroring
			for _, p := range s.peers {
				if p.isReady() {
					p.sendOutput(chunk)
				}
			}
			s.mu.Unlock()
			if mirror {
				os.Stdout.Write(chunk)
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *Session) writeInput(b []byte) {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	_, _ = s.ptmx.Write(b)
}

func (s *Session) resize(cols, rows int) {
	if cols < 1 || rows < 1 || cols > 1000 || rows > 1000 {
		return
	}
	s.mu.Lock()
	same := s.cols == cols && s.rows == rows
	s.cols, s.rows = cols, rows
	s.mu.Unlock()
	if !same {
		_ = setSize(s.ptmx, cols, rows)
	}
}

func (s *Session) commandLine() string { return strings.Join(s.cfg.Command, " ") }

// admit decides whether a new handshake may start.
func (s *Session) admit() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.accepting {
		return errors.New("invite no longer valid")
	}
	if time.Now().Before(s.lockedUntil) {
		return errors.New("host is temporarily locked after failed logins")
	}
	pending, ready := 0, 0
	for _, p := range s.peers {
		if p.isReady() {
			ready++
		} else {
			pending++
		}
	}
	if pending >= maxPendingHandshakes {
		return errors.New("too many pending connections")
	}
	if ready+pending >= s.cfg.MaxClients {
		return fmt.Errorf("session full (max %d clients)", s.cfg.MaxClients)
	}
	return nil
}

func (s *Session) authFailed(p *peer) {
	s.mu.Lock()
	s.failures++
	s.totalFails++
	fails, total := s.failures, s.totalFails
	if fails >= maxAuthFailures {
		s.lockedUntil = time.Now().Add(lockoutDuration)
		s.failures = 0
	}
	s.mu.Unlock()
	s.logf("WARNING: client #%d failed password authentication (%d recent, %d total)", p.id, fails, total)
	if fails >= maxAuthFailures {
		s.logf("WARNING: too many failures — refusing new clients for %s", lockoutDuration)
	}
	if total >= maxTotalFailures {
		s.stopAccepting("too many failed logins")
	}
}

func (s *Session) stopAccepting(reason string) {
	s.mu.Lock()
	was := s.accepting
	s.accepting = false
	stop := s.stopSignal
	s.mu.Unlock()
	if was {
		s.logf("invite disabled: %s (connected clients stay connected)", reason)
		if stop != nil && !s.cfg.Manual {
			stop()
		}
	}
}

// approve asks the host user to accept a client.
func (s *Session) approve(p *peer) bool {
	if !s.cfg.Approve {
		return true
	}
	s.approval.Lock()
	defer s.approval.Unlock()
	s.logf("client #%d (%s, role=%s) wants to join. Verification code: %s\nAllow? [y/N] (60s)", p.id, p.label, p.role, p.sas)
	c, ok := s.router.ask(60 * time.Second)
	allowed := ok && (c == 'y' || c == 'Y')
	if allowed {
		s.logf("client #%d approved", p.id)
	} else {
		s.logf("client #%d denied", p.id)
	}
	return allowed
}

func (s *Session) broadcastExit(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.peers {
		if p.isReady() {
			c := code
			p.sendControl(proto.Control{T: proto.CtlExit, Code: &c})
		}
	}
}

func (s *Session) closeAll(reason string) {
	s.mu.Lock()
	peers := make([]*peer, 0, len(s.peers))
	for _, p := range s.peers {
		peers = append(peers, p)
	}
	s.mu.Unlock()
	for _, p := range peers {
		p.close(reason)
	}
}

// stdinRouter forwards host keystrokes to the PTY, except while an approval
// prompt is waiting for a y/n answer.
type stdinRouter struct {
	mu      sync.Mutex
	pending chan byte
	forward func([]byte)
}

func (r *stdinRouter) run(in io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := in.Read(buf)
		if n > 0 {
			r.mu.Lock()
			if r.pending != nil {
				r.pending <- buf[0]
				r.pending = nil
				r.mu.Unlock()
				continue
			}
			f := r.forward
			r.mu.Unlock()
			if f != nil {
				f(buf[:n])
			}
		}
		if err != nil {
			return
		}
	}
}

func (r *stdinRouter) setForward(f func([]byte)) {
	r.mu.Lock()
	r.forward = f
	r.mu.Unlock()
}

func (r *stdinRouter) ask(timeout time.Duration) (byte, bool) {
	ch := make(chan byte, 1)
	r.mu.Lock()
	r.pending = ch
	r.mu.Unlock()
	select {
	case c := <-ch:
		return c, true
	case <-time.After(timeout):
		r.mu.Lock()
		if r.pending == ch {
			r.pending = nil
		}
		r.mu.Unlock()
		return 0, false
	}
}
