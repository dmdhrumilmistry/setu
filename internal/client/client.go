// Package client joins a shared terminal from another terminal.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/link"
	"github.com/dmdhrumilmistry/setu/internal/nostr"
	"github.com/dmdhrumilmistry/setu/internal/proto"
	"github.com/dmdhrumilmistry/setu/internal/rtc"
	"github.com/dmdhrumilmistry/setu/internal/secure"
	"github.com/dmdhrumilmistry/setu/internal/tty"
	"github.com/pion/webrtc/v4"
)

// Options configures Join.
type Options struct {
	Relays []string // override relays (else link r= or defaults)
	// Password is called when the host requires one.
	Password func() (string, error)
	// ShowAnswer prints the manual-mode answer code for the user to send back.
	ShowAnswer func(code string)
	Logf       func(format string, args ...any)
	Verbose    bool
	Timeout    time.Duration // signaling + connect timeout
	// Info describes this device to the host's audit log (see LocalInfo);
	// nil sends nothing.
	Info *proto.ClientInfo
	// DeviceKey proves which device is joining, so a host can approve it
	// automatically next time (see secure.LoadOrCreateDeviceKey). nil
	// sends no key.
	DeviceKey *secure.DeviceKey
	// NoReconnect turns off reconnecting after a dropped connection.
	NoReconnect bool
}

// ErrDetached is returned when the user typed the ~. escape.
var ErrDetached = errors.New("detached")

// ErrResumeRejected means the host no longer knows the session this client
// tried to resume (it expired or the share restarted); join afresh.
var ErrResumeRejected = errors.New(proto.ErrResume)

const (
	// pingInterval and deadAfter detect a connection that died without
	// ICE noticing yet (only with hosts that answer pings).
	pingInterval = 5 * time.Second
	deadAfter    = 15 * time.Second
	// reconnectWindow matches the host's resume grace period.
	reconnectWindow = 10 * time.Minute
	attemptTimeout  = 30 * time.Second
	minBackoff      = time.Second
	maxBackoff      = 30 * time.Second
)

// Join connects to a share and attaches the local terminal. It returns the
// remote command's exit code when known.
func Join(ctx context.Context, invite string, opt Options) (int, error) {
	c, err := Dial(ctx, invite, opt)
	if err != nil {
		return 1, err
	}
	defer c.Close()
	opt = c.opt
	opt.Logf("joined %q on %s as %s. Type <Enter> ~ . to detach.", c.Cmd, c.Host, c.Role)
	return c.attachTerminal(ctx)
}

// Conn is an authenticated connection to a host.
type Conn struct {
	opt             Options
	invite          string
	p               *peerConn
	pc              *webrtc.PeerConnection
	dc              *webrtc.DataChannel
	Role, Cmd, Host string
	SAS             string
	Approval        string               // how the host admitted us: proto.Approval* or empty
	Out             <-chan []byte        // terminal output
	Ctl             <-chan proto.Control // control frames after ready
	Closed, Failed  <-chan struct{}

	// Resumed is set on a connection made by Resume when the output
	// continues exactly where the previous connection stopped; otherwise
	// the host replays its whole buffer and the screen should be cleared.
	Resumed bool
	token   []byte // resume token from "ready" (nil: the host cannot resume)
	pos     int64  // output stream position of the first byte on Out
	hasPos  bool
}

// CanResume reports whether the host gave this connection a resume token.
func (c *Conn) CanResume() bool { return c.token != nil && c.hasPos }

// Pos is the output stream position after every byte delivered on Out.
func (c *Conn) Pos() int64 { return c.pos + c.p.recv.Load() }

// Resume makes a new connection to the same session, after this one was
// lost, without logging in again. The host sends only the output this
// connection missed when it still has it. c must not be used afterwards
// except to drain Out.
func (c *Conn) Resume(ctx context.Context) (*Conn, error) {
	if !c.CanResume() {
		return nil, errors.New("the host does not support resuming")
	}
	go c.Close()
	return dial(ctx, c.invite, c.opt, &resumeState{token: c.token, pos: c.Pos()})
}

// resumeState is what a reconnecting client presents.
type resumeState struct {
	token []byte
	pos   int64
}

// Write sends keystrokes (ignored by the host for view-only clients).
func (c *Conn) Write(b []byte) error { return c.dc.Send(b) }

// Resize reports the local terminal size.
func (c *Conn) Resize(cols, rows int) error {
	return sendCtl(c.dc, proto.Control{T: proto.CtlResize, Cols: cols, Rows: rows})
}

// Close tears the connection down.
func (c *Conn) Close() error {
	c.p.stop()
	return c.pc.Close()
}

func sendCtl(dc *webrtc.DataChannel, c proto.Control) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return dc.SendText(string(b))
}

// Dial performs signaling, the WebRTC connection and the setu handshake.
func Dial(ctx context.Context, invite string, opt Options) (*Conn, error) {
	return dial(ctx, invite, opt, nil)
}

func dial(ctx context.Context, invite string, opt Options, rs *resumeState) (*Conn, error) {
	inv, err := link.Parse(invite)
	if err != nil {
		return nil, err
	}
	if opt.Logf == nil {
		opt.Logf = func(f string, a ...any) { fmt.Fprintf(os.Stderr, "[setu] "+f+"\n", a...) }
	}
	if opt.Timeout == 0 {
		opt.Timeout = 90 * time.Second
	}
	debugf := func(f string, a ...any) {
		if opt.Verbose {
			opt.Logf(f, a...)
		}
	}

	var pc *peerConn
	sctx, cancel := context.WithTimeout(ctx, opt.Timeout)
	defer cancel()
	if inv.Secret != "" {
		relays := inv.Relays
		if len(opt.Relays) > 0 {
			relays = opt.Relays
		}
		if len(relays) == 0 {
			relays = nostr.DefaultRelays
		}
		resumeID := ""
		if rs != nil {
			resumeID = secure.ResumeID(rs.token)
		}
		pc, err = signalNostr(sctx, inv.Secret, relays, resumeID, opt.Logf, debugf)
	} else {
		if rs != nil {
			return nil, errors.New("a --manual share cannot be resumed")
		}
		pc, err = signalManual(sctx, inv.Offer, opt)
	}
	if err != nil {
		return nil, err
	}
	c, err := handshake(ctx, pc, opt, rs)
	if err != nil {
		pc.stop()
		pc.pc.Close()
		return nil, err
	}
	c.invite = invite
	return c, nil
}

// peerConn is a PeerConnection whose data channel handlers are attached
// before negotiation starts, so no early frame can be missed.
type peerConn struct {
	pc     *webrtc.PeerConnection
	ctl    chan proto.Control
	out    chan []byte
	opened chan *webrtc.DataChannel
	closed chan struct{}
	failed chan struct{}
	done   chan struct{} // closed when the connection is abandoned
	recv   atomic.Int64  // output bytes received
	once   sync.Once
}

// stop unblocks the data channel handlers of an abandoned connection.
func (p *peerConn) stop() { p.once.Do(func() { close(p.done) }) }

// newPeer uses exactly the ICE servers the host chose.
func newPeer(ice []proto.ICEServer) (*peerConn, error) {
	pc, err := rtc.NewPeer(ice)
	if err != nil {
		return nil, err
	}
	p := &peerConn{
		pc:     pc,
		ctl:    make(chan proto.Control, 16),
		out:    make(chan []byte, 256),
		opened: make(chan *webrtc.DataChannel, 1),
		closed: make(chan struct{}),
		failed: make(chan struct{}),
		done:   make(chan struct{}),
	}
	var failOnce, closeOnce sync.Once
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() != proto.DataChannelLabel {
			dc.Close()
			return
		}
		dc.OnClose(func() { closeOnce.Do(func() { close(p.closed) }) })
		dc.OnMessage(func(m webrtc.DataChannelMessage) {
			if m.IsString {
				var c proto.Control
				if json.Unmarshal(m.Data, &c) == nil {
					select {
					case p.ctl <- c:
					case <-p.done:
					}
				}
				return
			}
			select {
			case p.out <- append([]byte(nil), m.Data...):
				p.recv.Add(int64(len(m.Data)))
			case <-p.done:
			}
		})
		dc.OnOpen(func() {
			select {
			case p.opened <- dc:
			default:
			}
		})
	})
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		if st == webrtc.PeerConnectionStateFailed || st == webrtc.PeerConnectionStateClosed {
			failOnce.Do(func() { close(p.failed) })
		}
	})
	return p, nil
}

func signalManual(ctx context.Context, offerCode string, opt Options) (*peerConn, error) {
	var offer proto.Signal
	if err := link.DecodeCode(offerCode, &offer); err != nil {
		return nil, err
	}
	if offer.Type != proto.SigOffer {
		return nil, errors.New("code is not an offer")
	}
	pc, err := newPeer(offer.ICE)
	if err != nil {
		return nil, err
	}
	sdp, err := rtc.Answer(ctx, pc.pc, offer.SDP)
	if err != nil {
		pc.pc.Close()
		return nil, err
	}
	code, err := link.EncodeCode(proto.Signal{Type: proto.SigAnswer, SID: offer.SID, SDP: sdp, TS: time.Now().Unix()})
	if err != nil {
		pc.pc.Close()
		return nil, err
	}
	if opt.ShowAnswer != nil {
		opt.ShowAnswer(code)
	}
	return pc, nil
}

func signalNostr(ctx context.Context, secretStr string, relays []string, resumeID string, logf, debugf func(string, ...any)) (*peerConn, error) {
	sec, err := secure.ParseSecret(secretStr)
	if err != nil {
		return nil, err
	}
	room, err := secure.NewRoom(sec)
	if err != nil {
		return nil, err
	}
	kp, err := nostr.GenerateKey()
	if err != nil {
		return nil, err
	}
	pool := nostr.NewPool(ctx, relays, debugf)
	defer pool.Close() // signaling is over once WebRTC is up

	sid := secure.RandomID(8)
	type result struct {
		pc  *peerConn
		err error
	}
	done := make(chan result, 1)
	var once sync.Once
	finish := func(r result) {
		once.Do(func() { done <- r })
	}

	send := func(m proto.Signal) {
		m.TS = time.Now().Unix()
		pt, _ := json.Marshal(m)
		ct, err := room.Seal(pt, secure.SignalAAD(room.ID, kp.Pub, room.ID))
		if err != nil {
			return
		}
		ev := nostr.Event{Kind: nostr.KindSignal, Tags: [][]string{{"p", room.ID}}, Content: ct}
		if kp.Sign(&ev) == nil {
			pool.Publish(ev)
		}
	}

	var gotOffer sync.Once
	pool.Subscribe("setu-"+secure.RandomID(4), nostr.Filter{
		Kinds: []int{nostr.KindSignal},
		P:     []string{kp.Pub},
		Since: time.Now().Add(-time.Minute).Unix(),
	}, func(ev nostr.Event) {
		pt, err := room.Open(ev.Content, secure.SignalAAD(room.ID, ev.PubKey, kp.Pub))
		if err != nil {
			return
		}
		var m proto.Signal
		if json.Unmarshal(pt, &m) != nil || m.SID != sid {
			return
		}
		switch m.Type {
		case proto.SigReject:
			finish(result{err: fmt.Errorf("host rejected the connection: %s", m.Reason)})
		case proto.SigOffer:
			gotOffer.Do(func() {
				go func() {
					debugf("received offer, answering")
					pc, err := newPeer(m.ICE)
					if err != nil {
						finish(result{err: err})
						return
					}
					sdp, err := rtc.Answer(ctx, pc.pc, m.SDP)
					if err != nil {
						pc.pc.Close()
						finish(result{err: err})
						return
					}
					send(proto.Signal{Type: proto.SigAnswer, SID: sid, SDP: sdp})
					// Publish twice: ephemeral events are best effort.
					time.AfterFunc(time.Second, func() { send(proto.Signal{Type: proto.SigAnswer, SID: sid, SDP: sdp}) })
					finish(result{pc: pc})
				}()
			})
		}
	})

	logf("contacting host via %d nostr relay(s)…", len(relays))
	if err := pool.WaitConnected(ctx, 1); err != nil {
		return nil, err
	}
	// Retransmit hello until we get an offer; relays may connect at
	// different times and ephemeral events are not stored.
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	hello := proto.Signal{Type: proto.SigHello, SID: sid, Resume: resumeID}
	send(hello)
	for {
		select {
		case r := <-done:
			if r.err == nil {
				// Give the duplicate answer a moment before the pool closes.
				time.Sleep(1200 * time.Millisecond)
			}
			return r.pc, r.err
		case <-ticker.C:
			send(hello)
		case <-ctx.Done():
			return nil, fmt.Errorf("host did not answer (is `setu share` running and the link current?): %w", ctx.Err())
		}
	}
}

// handshake waits for the data channel and runs hello -> auth -> ready.
func handshake(ctx context.Context, p *peerConn, opt Options, rs *resumeState) (*Conn, error) {
	ctl, out, closed, opened, failed := p.ctl, p.out, p.closed, p.opened, p.failed
	pc := p.pc
	var dc *webrtc.DataChannel
	select {
	case dc = <-opened:
	case <-failed:
		return nil, errors.New("WebRTC connection failed (both sides behind strict NAT? try --turn on the host)")
	case <-time.After(opt.Timeout):
		return nil, errors.New("timed out establishing the WebRTC connection")
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	localFP, err := secure.Fingerprint(pc.LocalDescription().SDP)
	if err != nil {
		return nil, err
	}
	remoteFP, err := secure.Fingerprint(pc.RemoteDescription().SDP)
	if err != nil {
		return nil, err
	}
	sas := secure.SAS(localFP, remoteFP)

	send := func(c proto.Control) error { return sendCtl(dc, c) }

	// Handshake: hello -> auth -> ready.
	conn := &Conn{opt: opt, p: p, pc: pc, dc: dc, SAS: sas, Out: out, Ctl: ctl, Closed: closed, Failed: failed}
	for conn.Role == "" {
		select {
		case c := <-ctl:
			switch c.T {
			case proto.CtlHello:
				if c.V != proto.Version {
					return nil, fmt.Errorf("protocol version mismatch (host %d, client %d) — update setu", c.V, proto.Version)
				}
				if rs == nil {
					opt.Logf("connected peer-to-peer. Verification code: %s (should match the host)", sas)
				}
				auth := proto.Control{T: proto.CtlAuth}
				auth.Client = opt.Info
				if opt.DeviceKey != nil {
					sig, err := opt.DeviceKey.Sign(c.Nonce, localFP, remoteFP)
					if err != nil {
						return nil, err
					}
					auth.DeviceKey, auth.DeviceSig = opt.DeviceKey.Public(), sig
				}
				if rs != nil {
					// The resume token replaces the password proof.
					pos := rs.pos
					auth.Resume, auth.Pos = rs.token, &pos
				} else if c.Auth == proto.AuthPassword {
					if opt.Password == nil {
						return nil, errors.New("host requires a password")
					}
					pw, err := opt.Password()
					if err != nil {
						return nil, err
					}
					key, err := secure.PasswordKey(pw, c.Salt, c.Iter)
					if err != nil {
						return nil, err
					}
					auth.Proof = secure.AuthProof(key, c.Nonce, localFP, remoteFP)
				}
				if err := send(auth); err != nil {
					return nil, err
				}
			case proto.CtlReady:
				if c.Kind == proto.KindDesktop {
					return nil, errors.New("this invite shares a remote desktop: open the browser link instead of using setu join")
				}
				conn.Role, conn.Cmd, conn.Host, conn.Approval = c.Role, c.Cmd, c.Host, c.Approval
				conn.token, conn.Resumed = c.Resume, rs != nil && c.Resumed
				if c.Pos != nil {
					// Every output byte follows "ready", so recv counts from here.
					conn.pos, conn.hasPos = *c.Pos, true
				}
			case proto.CtlError:
				if rs != nil && c.Msg == proto.ErrResume {
					return nil, ErrResumeRejected
				}
				return nil, fmt.Errorf("host: %s", c.Msg)
			}
		case <-closed:
			return nil, errors.New("host closed the connection")
		case <-failed:
			return nil, errors.New("connection lost")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if conn.Approval == proto.ApprovalTrusted {
		opt.Logf("the host recognised this device and approved it automatically")
	}
	return conn, nil
}

// attachTerminal wires the local terminal to the connection, and to the
// connections that replace it when the network drops and the host lets the
// session be resumed.
func (conn *Conn) attachTerminal(ctx context.Context) (int, error) {
	isTTY := tty.IsTerminal(os.Stdin)
	if isTTY {
		restore, err := tty.MakeRaw(os.Stdin)
		if err == nil {
			defer restore()
		}
	}
	resize := make(chan struct{}, 1)
	stopResize := tty.NotifyResize(resize)
	defer stopResize()

	// Keystrokes go through a channel, so they reach whichever connection
	// is current; keystrokes typed while reconnecting are dropped.
	input := make(chan []byte, 64)
	detached := make(chan struct{})
	go func() {
		var esc tty.Escape
		lastCR := false
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				fwd, detach := esc.Filter(buf[:n])
				if detach {
					close(detached)
					return
				}
				if !isTTY {
					// Piped input: send Enter the way a terminal does (CR).
					// A Unix PTY maps CR to LF, but a Windows ConPTY only
					// treats CR as Enter, so a bare LF would never submit.
					fwd = pipedNewlines(fwd, &lastCR)
				}
				if len(fwd) > 0 {
					input <- append([]byte(nil), fwd...)
				}
			}
			if err != nil {
				return // stdin closed; keep showing output
			}
		}
	}()

	cur := conn
	defer func() { cur.Close() }()
	for {
		code, err, lost := cur.serve(ctx, input, resize, detached)
		if !lost {
			return code, err
		}
		if cur.opt.NoReconnect || !cur.CanResume() {
			return 1, err
		}
		next, rerr := cur.reconnect(ctx, err, input, detached)
		if next == nil {
			return 1, rerr
		}
		if next.Resumed {
			cur.opt.Logf("reconnected")
		} else {
			os.Stdout.WriteString("\x1b[H\x1b[2J") // the host replays its whole buffer
			cur.opt.Logf("reconnected (some output was lost while disconnected)")
		}
		cur = next
	}
}

// serve runs one connection. lost reports a connection that dropped
// without the session ending, which may be resumed.
func (conn *Conn) serve(ctx context.Context, input <-chan []byte, resize <-chan struct{}, detached <-chan struct{}) (code int, err error, lost bool) {
	role, dc := conn.Role, conn.dc
	out, ctl, closed, failed := conn.Out, conn.Ctl, conn.Closed, conn.Failed
	send := func(c proto.Control) error { return sendCtl(dc, c) }
	sendSize := func() {
		c, r := tty.Size(os.Stdout)
		_ = send(proto.Control{T: proto.CtlResize, Cols: c, Rows: r})
	}
	if role == proto.RoleControl {
		sendSize()
	}
	// Hosts that hand out resume tokens answer pings; others are left alone.
	var tick <-chan time.Time
	if conn.CanResume() {
		t := time.NewTicker(pingInterval)
		defer t.Stop()
		tick = t.C
	}
	lastRx := time.Now()

	for {
		select {
		case b := <-out:
			lastRx = time.Now()
			os.Stdout.Write(b)
		case c := <-ctl:
			lastRx = time.Now()
			switch c.T {
			case proto.CtlExit:
				drain(out)
				code := 0
				if c.Code != nil {
					code = *c.Code
				}
				return code, nil, false
			case proto.CtlError:
				return 1, fmt.Errorf("host: %s", c.Msg), false
			case proto.CtlInfo:
				fmt.Fprintf(os.Stderr, "\r\n[setu] %s\r\n", c.Msg)
			}
		case b := <-input:
			if role == proto.RoleControl {
				_ = dc.Send(b)
			}
		case <-tick:
			if time.Since(lastRx) > deadAfter {
				drain(out)
				return 1, errors.New("connection lost (no response from host)"), true
			}
			_ = send(proto.Control{T: proto.CtlPing})
		case <-resize:
			if role == proto.RoleControl {
				sendSize()
			}
		case <-detached:
			return 0, ErrDetached, false
		case <-closed:
			return conn.ended(errors.New("host closed the connection"))
		case <-failed:
			return conn.ended(errors.New("connection lost"))
		case <-ctx.Done():
			return 1, ctx.Err(), false
		}
	}
}

// ended handles a closed connection: the session is over if the host said
// so (its last frames may still be queued), otherwise the connection was lost.
func (conn *Conn) ended(cause error) (int, error, bool) {
	drain(conn.Out)
	for {
		select {
		case c := <-conn.Ctl:
			switch c.T {
			case proto.CtlExit:
				drain(conn.Out)
				code := 0
				if c.Code != nil {
					code = *c.Code
				}
				return code, nil, false
			case proto.CtlError:
				return 1, fmt.Errorf("host: %s", c.Msg), false
			}
		default:
			return 1, cause, true
		}
	}
}

// reconnect resumes the session after cause, retrying with jittered
// exponential backoff until the host's resume window closes. It returns nil
// and the reason when it gives up.
func (conn *Conn) reconnect(ctx context.Context, cause error, input <-chan []byte, detached <-chan struct{}) (*Conn, error) {
	opt := conn.opt
	opt.Logf("%v; reconnecting… (<Enter> ~ . to give up)", cause)
	deadline := time.Now().Add(reconnectWindow)
	rs := &resumeState{token: conn.token, pos: conn.Pos()}
	go conn.Close()
	type result struct {
		c   *Conn
		err error
	}
	backoff := minBackoff
	for attempt := 1; ; attempt++ {
		actx, cancel := context.WithTimeout(ctx, attemptTimeout)
		ropt := opt
		ropt.Timeout = attemptTimeout
		ropt.Logf = func(string, ...any) {} // keep the screen quiet between attempts
		done := make(chan result, 1)
		go func() {
			c, err := dial(actx, conn.invite, ropt, rs)
			done <- result{c, err}
		}()
		var r result
	wait:
		for {
			select {
			case r = <-done:
				break wait
			case <-input: // not sent: the host may never see it
			case <-detached:
				cancel()
				return nil, ErrDetached
			}
		}
		cancel()
		if r.err == nil {
			r.c.opt = opt
			return r.c, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(r.err, ErrResumeRejected) || strings.HasPrefix(r.err.Error(), "host rejected") {
			return nil, fmt.Errorf("could not reconnect: %w", r.err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("could not reconnect within %s: %w", reconnectWindow, r.err)
		}
		opt.Logf("reconnect attempt %d failed (%v); retrying", attempt, r.err)
		// Full jitter, so many clients of one host do not retry in step.
		sleep := backoff/2 + rand.N(backoff/2+1)
		backoff = min(backoff*2, maxBackoff)
		t := time.NewTimer(sleep)
	pause:
		for {
			select {
			case <-t.C:
				break pause
			case <-input:
			case <-detached:
				t.Stop()
				return nil, ErrDetached
			case <-ctx.Done():
				t.Stop()
				return nil, ctx.Err()
			}
		}
	}
}

func drain(out <-chan []byte) {
	for {
		select {
		case b := <-out:
			os.Stdout.Write(b)
		default:
			return
		}
	}
}

// pipedNewlines converts LF and CRLF line endings to CR. lastCR carries
// across reads so a CRLF split between two reads is not doubled.
func pipedNewlines(in []byte, lastCR *bool) []byte {
	out := make([]byte, 0, len(in))
	for _, c := range in {
		switch {
		case c == '\n' && *lastCR:
			// second half of CRLF: already sent the CR
		case c == '\n':
			out = append(out, '\r')
		default:
			out = append(out, c)
		}
		*lastCR = c == '\r'
	}
	return out
}
