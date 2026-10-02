// Package client joins a shared terminal from another terminal.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
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
}

// ErrDetached is returned when the user typed the ~. escape.
var ErrDetached = errors.New("detached")

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
	pc              *webrtc.PeerConnection
	dc              *webrtc.DataChannel
	Role, Cmd, Host string
	SAS             string
	Out             <-chan []byte        // terminal output
	Ctl             <-chan proto.Control // control frames after ready
	Closed, Failed  <-chan struct{}
}

// Write sends keystrokes (ignored by the host for view-only clients).
func (c *Conn) Write(b []byte) error { return c.dc.Send(b) }

// Resize reports the local terminal size.
func (c *Conn) Resize(cols, rows int) error {
	return sendCtl(c.dc, proto.Control{T: proto.CtlResize, Cols: cols, Rows: rows})
}

// Close tears the connection down.
func (c *Conn) Close() error { return c.pc.Close() }

func sendCtl(dc *webrtc.DataChannel, c proto.Control) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return dc.SendText(string(b))
}

// Dial performs signaling, the WebRTC connection and the setu handshake.
func Dial(ctx context.Context, invite string, opt Options) (*Conn, error) {
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
		pc, err = signalNostr(sctx, inv.Secret, relays, opt.Logf, debugf)
	} else {
		pc, err = signalManual(sctx, inv.Offer, opt)
	}
	if err != nil {
		return nil, err
	}
	c, err := handshake(ctx, pc, opt)
	if err != nil {
		pc.pc.Close()
		return nil, err
	}
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
}

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
					p.ctl <- c
				}
				return
			}
			p.out <- append([]byte(nil), m.Data...)
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

func signalNostr(ctx context.Context, secretStr string, relays []string, logf, debugf func(string, ...any)) (*peerConn, error) {
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
	send(proto.Signal{Type: proto.SigHello, SID: sid})
	for {
		select {
		case r := <-done:
			if r.err == nil {
				// Give the duplicate answer a moment before the pool closes.
				time.Sleep(1200 * time.Millisecond)
			}
			return r.pc, r.err
		case <-ticker.C:
			send(proto.Signal{Type: proto.SigHello, SID: sid})
		case <-ctx.Done():
			return nil, fmt.Errorf("host did not answer (is `setu share` running and the link current?): %w", ctx.Err())
		}
	}
}

// handshake waits for the data channel and runs hello -> auth -> ready.
func handshake(ctx context.Context, p *peerConn, opt Options) (*Conn, error) {
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
	conn := &Conn{opt: opt, pc: pc, dc: dc, SAS: sas, Out: out, Ctl: ctl, Closed: closed, Failed: failed}
	for conn.Role == "" {
		select {
		case c := <-ctl:
			switch c.T {
			case proto.CtlHello:
				if c.V != proto.Version {
					return nil, fmt.Errorf("protocol version mismatch (host %d, client %d) — update setu", c.V, proto.Version)
				}
				opt.Logf("connected peer-to-peer. Verification code: %s (should match the host)", sas)
				auth := proto.Control{T: proto.CtlAuth}
				if c.Auth == proto.AuthPassword {
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
				conn.Role, conn.Cmd, conn.Host = c.Role, c.Cmd, c.Host
			case proto.CtlError:
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

	return conn, nil
}

// attachTerminal wires the local terminal to the connection.
func (conn *Conn) attachTerminal(ctx context.Context) (int, error) {
	role, dc := conn.Role, conn.dc
	out, ctl, closed, failed := conn.Out, conn.Ctl, conn.Closed, conn.Failed
	send := func(c proto.Control) error { return sendCtl(dc, c) }
	// Attach the terminal.
	isTTY := tty.IsTerminal(os.Stdin)
	if isTTY {
		restore, err := tty.MakeRaw(os.Stdin)
		if err == nil {
			defer restore()
		}
	}
	sendSize := func() {
		c, r := tty.Size(os.Stdout)
		_ = send(proto.Control{T: proto.CtlResize, Cols: c, Rows: r})
	}
	if role == proto.RoleControl {
		sendSize()
	}
	resize := make(chan struct{}, 1)
	stopResize := tty.NotifyResize(resize)
	defer stopResize()

	detached := make(chan struct{})
	go func() {
		var esc tty.Escape
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				fwd, detach := esc.Filter(buf[:n])
				if detach {
					close(detached)
					return
				}
				if role == proto.RoleControl && len(fwd) > 0 {
					_ = dc.Send(fwd)
				}
			}
			if err != nil {
				return // stdin closed; keep showing output
			}
		}
	}()

	for {
		select {
		case b := <-out:
			os.Stdout.Write(b)
		case c := <-ctl:
			switch c.T {
			case proto.CtlExit:
				drain(out)
				code := 0
				if c.Code != nil {
					code = *c.Code
				}
				return code, nil
			case proto.CtlError:
				return 1, fmt.Errorf("host: %s", c.Msg)
			case proto.CtlInfo:
				fmt.Fprintf(os.Stderr, "\r\n[setu] %s\r\n", c.Msg)
			}
		case <-resize:
			if role == proto.RoleControl {
				sendSize()
			}
		case <-detached:
			return 0, ErrDetached
		case <-closed:
			drain(out)
			return 1, errors.New("host closed the connection")
		case <-failed:
			return 1, errors.New("connection lost")
		case <-ctx.Done():
			return 1, ctx.Err()
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
