// Package host shares a local PTY with remote peers over WebRTC.
package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/audit"
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

// Key is a named, personal invite key.
type Key struct {
	Name   string
	Role   string // proto.RoleControl or proto.RoleView
	Secret secure.Secret
}

// Config controls a sharing session.
type Config struct {
	Command []string
	// Desktop, if set, shares this computer's screen, mouse, keyboard and
	// clipboard instead of running Command.
	Desktop *DesktopConfig
	Dir     string // working directory for Command; empty = current
	Profile string // service profile name, for logs and audit records

	// Signaling.
	Manual        bool           // copy/paste signaling instead of nostr
	ControlSecret *secure.Secret // nostr mode: full-control invite
	ViewSecret    *secure.Secret // nostr mode: optional read-only invite
	// Keys are personal invites: each names the one person who holds it,
	// so logs and audit records can say who connected and typed.
	Keys   []Key
	Relays []string
	ICE    []proto.ICEServer

	// Access control.
	Password string // empty = no password
	// PasswordVerifier is an alternative to Password: a key derived ahead
	// of time, so the plaintext never has to be stored.
	PasswordVerifier *secure.PasswordVerifier
	MaxClients       int           // concurrent authenticated clients
	Once             bool          // disable invites after the first successful join
	Approve          bool          // ask on the host terminal before admitting a client
	Expire           time.Duration // disable invites after this long (0 = never)
	// TrustedDevices are device key fingerprints approved without asking
	// (secure.DeviceFingerprint form). Answering "a" at the prompt adds a
	// device to this set, in memory, for the rest of the session.
	TrustedDevices []string
	// NoPrompt means nobody can answer approval prompts (headless or in
	// the background): with Approve, trusted devices are admitted and every
	// other client is denied.
	NoPrompt bool
	// Ask overrides how approval answers are read (tests); by default the
	// host terminal is asked.
	Ask func(timeout time.Duration) (byte, bool)

	// Local terminal.
	Mirror bool // attach the host terminal to the shared PTY

	OnInvite   func(Invite)
	ReadAnswer func(ctx context.Context) (string, error) // manual mode
	Log        io.Writer
	Verbose    bool

	// IdleTimeout disconnects control clients after this long without input
	// (0 = never). Watching is not input, so view-only clients stay.
	IdleTimeout time.Duration

	// UploadDir is where control clients' uploads are saved; empty turns
	// uploads off.
	UploadDir string

	// Audit, if not nil, receives session, client and command records.
	Audit *audit.Logger
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

	pty ptyProcess

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
	pwIter int
	host   string

	id         string
	desk       *desktopShare // nil for a terminal share
	audit      *audit.Session
	localInput *audit.Input

	router    *stdinRouter
	approval  sync.Mutex
	trusted   map[string]bool // device fingerprints approved automatically (guarded by approval)
	rawMode   bool
	mirroring bool
	logMu     sync.Mutex
}

// New validates cfg and prepares a session.
func New(cfg Config) (*Session, error) {
	if len(cfg.Command) == 0 && cfg.Desktop == nil {
		return nil, errors.New("no command to run")
	}
	if !cfg.Manual && cfg.ControlSecret == nil && len(cfg.Keys) == 0 {
		return nil, errors.New("nostr mode needs a control secret or a personal key")
	}
	for _, k := range cfg.Keys {
		if k.Name == "" || (k.Role != proto.RoleControl && k.Role != proto.RoleView) {
			return nil, fmt.Errorf("personal key %q: needs a name and a role", k.Name)
		}
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
	s := &Session{cfg: cfg, peers: map[int]*peer{}, accepting: true, router: &stdinRouter{}, id: secure.RandomID(8), trusted: map[string]bool{}}
	for _, fp := range cfg.TrustedDevices {
		n, err := secure.NormalizeDeviceFingerprint(fp)
		if err != nil {
			return nil, err
		}
		s.trusted[n] = true
	}
	if cfg.NoPrompt && cfg.Approve && len(s.trusted) == 0 {
		return nil, errors.New("approval without a prompt needs at least one trusted device (nobody could be admitted)")
	}
	switch {
	case cfg.Password != "" && cfg.PasswordVerifier != nil:
		return nil, errors.New("set either a password or a password verifier, not both")
	case cfg.Password != "":
		v, err := secure.NewPasswordVerifier(cfg.Password)
		if err != nil {
			return nil, err
		}
		s.cfg.Password = "" // keep only the derived key in memory
		s.pwSalt, s.pwIter, s.pwKey = v.Salt, v.Iter, v.Key
	case cfg.PasswordVerifier != nil:
		v := cfg.PasswordVerifier
		if err := v.Validate(); err != nil {
			return nil, err
		}
		s.pwSalt, s.pwIter, s.pwKey = v.Salt, v.Iter, v.Key
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
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	exited := make(chan int, 1)
	readDone := make(chan struct{})
	kill := func() {}
	if s.cfg.Desktop != nil {
		stop, err := s.startDesktop(ctx)
		if err != nil {
			return 1, err
		}
		defer stop()
		close(readDone)
		// A desktop share has no command: it lasts until the host ends it.
		go func() { <-ctx.Done(); exited <- 0 }()
	} else {
		cols, rows := 120, 40
		if s.cfg.Mirror {
			cols, rows = tty.Size(os.Stdout)
		}
		p, err := startPTY(s.cfg.Command, s.cfg.Dir, cols, rows)
		if err != nil {
			return 1, err
		}
		s.pty, s.cols, s.rows = p, cols, rows
		defer p.Close()
		go func() { exited <- p.Wait() }()
		go s.pumpOutput(readDone)
		kill = func() { _ = p.Kill() }
	}

	mode := "nostr"
	if s.cfg.Manual {
		mode = "manual"
	}
	s.audit = s.cfg.Audit.Session(audit.SessionInfo{
		ID:         s.id,
		Command:    s.commandLine(),
		Kind:       s.kind(),
		Mode:       mode,
		Profile:    s.cfg.Profile,
		Password:   s.pwKey != nil,
		ViewLink:   s.cfg.ViewSecret != nil,
		MaxClients: s.cfg.MaxClients,
	})

	sigCtx, stopSignal := context.WithCancel(ctx)
	s.mu.Lock()
	s.stopSignal = stopSignal
	s.mu.Unlock()
	if s.cfg.IdleTimeout > 0 {
		go s.idleLoop(ctx)
	}
	if s.cfg.Expire > 0 {
		t := time.AfterFunc(s.cfg.Expire, func() { s.stopAccepting("invite expired") })
		defer t.Stop()
	}

	signalErr := make(chan error, 1)
	if s.cfg.Manual {
		if err := s.serveManual(sigCtx); err != nil {
			kill()
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
	reason := "command exited"
	select {
	case code = <-exited:
	case err := <-signalErr:
		if err != nil && ctx.Err() == nil {
			kill()
			cancel()
			<-exited
			s.closeAll("host error")
			s.endAudit(1, "host error: "+err.Error())
			return 1, err
		}
		code = <-exited
	case <-ctx.Done():
		// Host ended the share (~. or a signal): not a command failure.
		kill()
		<-exited
		code = 0
		reason = "share ended by host"
	}
	if s.cfg.Desktop != nil {
		reason = "share ended by host"
	}
	select {
	case <-readDone:
	case <-time.After(500 * time.Millisecond):
	}
	s.broadcastExit(code)
	time.Sleep(200 * time.Millisecond) // let the exit frame flush
	s.closeAll("session ended")
	s.endAudit(code, reason)
	return code, nil
}

func (s *Session) endAudit(code int, reason string) {
	s.localInput.Close()
	s.audit.End(code, reason)
}

// passwordPrompt reports whether the shared command is reading a password
// (canonical mode with echo off), so those keystrokes are never audited.
func (s *Session) passwordPrompt() bool {
	if pm, ok := s.pty.(interface{ PasswordMode() bool }); ok {
		return pm.PasswordMode()
	}
	return false
}

// attachLocal mirrors the PTY on the host terminal (raw mode) and routes
// host keystrokes. "<Enter> ~ ." on the host ends the share. The returned
// func restores the terminal and must run before the process exits.
func (s *Session) attachLocal(ctx context.Context, cancel context.CancelFunc) (restore func()) {
	restore = func() {}
	if !s.cfg.Mirror && (!s.cfg.Approve || s.cfg.NoPrompt) {
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
		s.localInput = s.audit.Input(audit.Client{Role: proto.RoleControl, Via: "local"}, s.passwordPrompt)
		s.router.setForward(func(b []byte) {
			out, detach := esc.Filter(b)
			if len(out) > 0 {
				s.localInput.Write(out)
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
		n, err := s.pty.Read(buf)
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
	_, _ = s.pty.Write(b)
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
		_ = s.pty.Resize(cols, rows)
	}
}

func (s *Session) commandLine() string {
	if s.cfg.Desktop != nil {
		return "remote desktop"
	}
	return strings.Join(s.cfg.Command, " ")
}

func (s *Session) kind() string {
	if s.cfg.Desktop != nil {
		return proto.KindDesktop
	}
	return proto.KindTerminal
}

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
	who := p.auditClient()
	s.audit.AuthFailed(who)
	s.logf("WARNING: client #%d failed password authentication (%d recent, %d total): %s", p.id, fails, total, who.Actor())
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
		s.audit.InviteDisabled(reason)
		s.logf("invite disabled: %s (connected clients stay connected)", reason)
		if stop != nil && !s.cfg.Manual {
			stop()
		}
	}
}

// approve asks the host user to accept a client.
// approve decides whether a client may join when --approve is on. Known
// devices (a verified device key in the trusted set) are admitted without
// asking; otherwise the host is asked, and may answer "a" to trust the
// device for the rest of the session. It returns how the client was
// admitted (proto.Approval*), or ok=false if it was denied.
func (s *Session) approve(p *peer) (how string, ok bool) {
	if !s.cfg.Approve {
		return "", true
	}
	s.approval.Lock()
	defer s.approval.Unlock()
	who := p.auditClient()
	if p.deviceFP != "" && s.trusted[p.deviceFP] {
		s.audit.Approval(who, true, audit.ApprovedTrustedDevice)
		s.logf("client #%d approved automatically: trusted device %s", p.id, p.deviceFP)
		return proto.ApprovalTrusted, true
	}
	if s.cfg.NoPrompt {
		s.audit.Approval(who, false, audit.DeniedUnknownDevice)
		s.logf("client #%d denied: device %s is not trusted and nobody can approve it here", p.id, orNone(p.deviceFP))
		return "", false
	}
	choices := "[y/N]"
	if p.deviceFP != "" {
		choices = "[y] once, [a] always for this device, [N] no"
	}
	noKey := ""
	if p.deviceFP == "" {
		noKey = " (no device key, so it cannot be remembered)"
	}
	s.logf("client #%d (role=%s) wants to join: %s%s\nAllow? %s (60s)", p.id, p.role, who.Actor(), noKey, choices)
	ask := s.cfg.Ask
	if ask == nil {
		ask = s.router.ask
	}
	c, answered := ask(60 * time.Second)
	switch {
	case answered && (c == 'a' || c == 'A') && p.deviceFP != "":
		s.trusted[p.deviceFP] = true
		s.audit.Approval(who, true, audit.ApprovedAlways)
		s.logf("client #%d approved; device %s will be approved automatically for the rest of this share", p.id, p.deviceFP)
	case answered && (c == 'y' || c == 'Y'):
		s.audit.Approval(who, true, audit.ApprovedOnce)
		s.logf("client #%d approved", p.id)
	default:
		s.audit.Approval(who, false, audit.DeniedByHost)
		s.logf("client #%d denied", p.id)
		return "", false
	}
	return proto.ApprovalPrompt, true
}

func orNone(s string) string {
	if s == "" {
		return "(none sent)"
	}
	return s
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
