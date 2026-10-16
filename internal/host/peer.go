package host

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/audit"
	"github.com/dmdhrumilmistry/setu/internal/desktop"
	"github.com/dmdhrumilmistry/setu/internal/proto"
	"github.com/dmdhrumilmistry/setu/internal/rtc"
	"github.com/dmdhrumilmistry/setu/internal/secure"
	"github.com/pion/webrtc/v4"
)

type peerState int

const (
	stateNegotiating peerState = iota // offer sent, waiting for answer / DTLS
	stateAuthing                      // data channel open, hello sent
	stateApproving                    // authenticated, waiting for host approval
	stateReady                        // streaming
	stateClosed
)

const (
	maxInputFrame = 64 << 10
	// A client that cannot keep up gets disconnected instead of making the
	// host buffer without bound.
	maxBuffered = 8 << 20
)

type peer struct {
	s     *Session
	id    int
	role  string
	label string
	// keyName is the personal key the client joined with ("" for the
	// shared links). The host derives it from which key decrypted the join
	// request, never from anything the client claims.
	keyName string
	// deviceFP is the fingerprint of the client's device key, set only when
	// its signature over this connection verified.
	deviceFP string

	pc      *webrtc.PeerConnection
	dc      *webrtc.DataChannel
	screen  *webrtc.DataChannel // desktop shares: screen updates
	clip    strings.Builder     // clipboard text being received (data channel goroutine only)
	clipOut sync.Mutex          // one outgoing clipboard transfer at a time

	mu          sync.Mutex
	state       peerState
	nonce       []byte
	localFP     string
	remoteFP    string
	sas         string
	onClose     []func()
	deadline    *time.Timer
	publicAddrs []string          // from the client's SDP answer
	localAddrs  []string          // from the client's SDP answer
	info        *proto.ClientInfo // self-reported device details (sanitized)
	input       *audit.Input      // keystroke audit (control clients, once ready)
	client      *audit.Client     // audit identity, fixed once ready
	viewer      *desktop.Viewer   // desktop shares, once ready
	inputErrAt  time.Time         // last remote input failure reported
	uploads     int               // uploads in progress

	lastInput   atomic.Int64 // unix nanoseconds of the client's last input
	idleWarned  atomic.Bool
	idleClosing atomic.Bool

	// mustResume: admitted only because it named a resume ticket, so it
	// must present that ticket's token instead of a normal login.
	mustResume bool
	ticket     *ticket      // resume ticket, once ready (guarded by s.mu)
	lastRx     atomic.Int64 // unix nanos of the last message received
	pinger     atomic.Bool  // the client sends liveness pings
	hidden     atomic.Bool  // the client's page is in the background
}

// newPeer registers a peer after checking admission. resumeID is the
// resume ticket the client says it holds, if any.
func (s *Session) newPeer(role, label, keyName, resumeID string) (*peer, error) {
	resume := s.hasTicket(resumeID)
	if err := s.admit(resume); err != nil {
		return nil, err
	}
	pc, err := rtc.NewPeer(s.cfg.ICE)
	if err != nil {
		return nil, err
	}
	dc, err := pc.CreateDataChannel(proto.DataChannelLabel, nil)
	if err != nil {
		pc.Close()
		return nil, err
	}
	var screen *webrtc.DataChannel
	if s.desk != nil {
		if screen, err = pc.CreateDataChannel(proto.ScreenChannelLabel, nil); err != nil {
			pc.Close()
			return nil, err
		}
	}
	s.mu.Lock()
	s.nextID++
	p := &peer{s: s, id: s.nextID, role: role, label: label, keyName: keyName, pc: pc, dc: dc, screen: screen, nonce: secure.RandomBytes(32), mustResume: resume}
	s.peers[p.id] = p
	s.mu.Unlock()

	p.deadline = time.AfterFunc(handshakeTimeout, func() {
		if !p.isReady() {
			p.close("handshake timed out")
		}
	})
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		s.debugf("client #%d connection state %s", p.id, st)
		switch st {
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			p.close("connection " + st.String())
		}
	})
	pc.OnDataChannel(p.onDataChannel)
	dc.OnOpen(p.onOpen)
	dc.OnClose(func() { p.close("channel closed") })
	dc.OnMessage(p.onMessage)
	return p, nil
}

func (p *peer) isReady() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state == stateReady
}

func (p *peer) offer(ctx context.Context) (string, error) {
	return rtc.Offer(ctx, p.pc)
}

func (p *peer) acceptAnswer(sdp string) error {
	remoteFP, err := secure.Fingerprint(sdp)
	if err != nil {
		return err
	}
	ld := p.pc.LocalDescription()
	if ld == nil {
		return errors.New("no local description")
	}
	localFP, err := secure.Fingerprint(ld.SDP)
	if err != nil {
		return err
	}
	public, local := rtc.Addresses(sdp)
	p.mu.Lock()
	p.localFP, p.remoteFP = localFP, remoteFP
	p.publicAddrs, p.localAddrs = public, local
	p.sas = secure.SAS(localFP, remoteFP)
	p.mu.Unlock()
	return p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp})
}

func (p *peer) onOpen() {
	p.mu.Lock()
	if p.state != stateNegotiating {
		p.mu.Unlock()
		return
	}
	p.state = stateAuthing
	p.mu.Unlock()
	hello := proto.Control{T: proto.CtlHello, V: proto.Version, Auth: proto.AuthNone, Nonce: p.nonce, Role: p.role}
	if p.s.pwKey != nil {
		hello.Auth = proto.AuthPassword
		hello.Salt = p.s.pwSalt
		hello.Iter = p.s.pwIter
	}
	p.sendControl(hello)
}

func (p *peer) onMessage(msg webrtc.DataChannelMessage) {
	p.lastRx.Store(time.Now().UnixNano())
	p.mu.Lock()
	st, input := p.state, p.input
	p.mu.Unlock()

	if !msg.IsString {
		if st == stateReady && p.role == proto.RoleControl && p.s.desk == nil && len(msg.Data) <= maxInputFrame {
			p.touch()
			input.Write(msg.Data)
			p.s.writeInput(msg.Data)
		}
		return
	}
	var c proto.Control
	if len(msg.Data) > maxInputFrame || json.Unmarshal(msg.Data, &c) != nil {
		return
	}
	switch c.T {
	case proto.CtlAuth:
		if st != stateAuthing {
			return
		}
		p.mu.Lock()
		p.state = stateApproving
		p.info = c.Client.Sanitized() // before the password check, so failures are attributed too
		p.mu.Unlock()
		if len(c.DeviceKey) > 0 {
			// A device key must prove itself on this connection; a client that
			// sends a bad one is broken or forging, so it is refused.
			fp, err := secure.VerifyDevice(c.DeviceKey, c.DeviceSig, p.nonce, p.localFP, p.remoteFP)
			if err != nil {
				p.s.authFailed(p)
				p.sendControl(proto.Control{T: proto.CtlError, Msg: "invalid device key signature"})
				time.AfterFunc(200*time.Millisecond, func() { p.close("invalid device key") })
				return
			}
			p.mu.Lock()
			p.deviceFP = fp
			p.mu.Unlock()
		}
		if len(c.Resume) > 0 || p.mustResume {
			p.resume(c)
			return
		}
		if p.s.pwKey != nil {
			want := secure.AuthProof(p.s.pwKey, p.nonce, p.localFP, p.remoteFP)
			if !hmac.Equal(want, c.Proof) {
				p.s.authFailed(p)
				p.sendControl(proto.Control{T: proto.CtlError, Msg: "authentication failed"})
				time.AfterFunc(200*time.Millisecond, func() { p.close("authentication failed") })
				return
			}
		}
		go p.activate(nil, nil)
	case proto.CtlPing:
		if st != stateClosed {
			p.pinger.Store(true)
			p.sendControl(proto.Control{T: proto.CtlPong})
		}
	case proto.CtlVisibility:
		if st == stateReady && p.hidden.Swap(c.Hidden) != c.Hidden {
			p.s.debugf("client #%d is now in the %s", p.id, map[bool]string{true: "background", false: "foreground"}[c.Hidden])
		}
	case proto.CtlResize:
		if st == stateReady && p.role == proto.RoleControl && p.s.desk == nil {
			p.s.resize(c.Cols, c.Rows)
		}
	default:
		if st == stateReady && p.s.desk != nil {
			p.onDesktopMessage(c)
		}
	}
}

// resume handles an "auth" that presents a resume token. The token stands
// in for the password and the approval the client passed when it first
// joined; a client that only got in by naming a ticket has no other way in.
func (p *peer) resume(c proto.Control) {
	t, old, err := p.s.claimTicket(p, c.Resume)
	if err != nil {
		if p.mustResume {
			p.s.authFailed(p)
		}
		p.sendControl(proto.Control{T: proto.CtlError, Msg: proto.ErrResume})
		time.AfterFunc(200*time.Millisecond, func() { p.close("invalid resume token") })
		return
	}
	if old != nil {
		old.close("replaced by its reconnection")
	}
	go p.activate(t, c.Pos)
}

// activate admits an authenticated client: a new one (t == nil) after the
// host's approval, or one resuming ticket t, which continues the output at
// pos when the host still has it.
func (p *peer) activate(t *ticket, pos *int64) {
	how := ""
	if t == nil {
		var ok bool
		if how, ok = p.s.approve(p); !ok {
			p.sendControl(proto.Control{T: proto.CtlError, Msg: "the host denied the connection"})
			time.AfterFunc(200*time.Millisecond, func() { p.close("denied by host") })
			return
		}
	} else {
		how = t.approval
	}
	s := p.s
	c := p.auditClient()
	s.mu.Lock()
	p.mu.Lock()
	if p.state != stateApproving {
		p.mu.Unlock()
		s.mu.Unlock()
		return
	}
	// The input recorder is set in the same critical section as the state,
	// so no keystroke can reach the PTY unaudited.
	p.state = stateReady
	p.client = &c
	p.touch()
	if t != nil && t.lastInput > 0 {
		// Reconnecting is not input: the idle timeout keeps counting.
		p.lastInput.Store(t.lastInput)
	}
	if p.role == proto.RoleControl && s.desk == nil {
		p.input = s.audit.Input(c, s.passwordPrompt)
	}
	p.mu.Unlock()
	if t == nil {
		s.failures = 0
	}
	firstID := 0
	if t != nil {
		firstID = t.firstID
	}
	token := s.issueTicket(p, t, how)
	// Replay what the client has not seen: everything we still have for a
	// new client, only the missed part for one that resumes.
	start := s.outTotal - int64(len(s.ring))
	from, resumed := start, false
	if t != nil && pos != nil && *pos >= start && *pos <= s.outTotal {
		from, resumed = *pos, true
	}
	ready := proto.Control{T: proto.CtlReady, V: proto.Version, Role: p.role, Cmd: s.commandLine(), Host: s.host, Cols: s.cols, Rows: s.rows,
		Approval: how, Kind: s.kind(), OS: s.hostOS(), Upload: s.uploadsAllowed(p.role), Resume: token}
	if s.desk == nil {
		ready.Pos, ready.Resumed = &from, resumed
	}
	p.sendControl(ready)
	if replay := s.ring[from-start:]; len(replay) > 0 && s.desk == nil {
		for off := 0; off < len(replay); off += 16 << 10 {
			end := min(off+16<<10, len(replay))
			p.sendOutput(replay[off:end])
		}
	}
	s.mu.Unlock()
	p.deadline.Stop()
	if s.desk != nil {
		p.joinDesktop()
	}
	if t != nil {
		s.audit.ClientReconnected(c, firstID)
		s.logf("client #%d reconnected via %s (first joined as #%d), role=%s: %s", p.id, p.label, firstID, p.role, c.Actor())
		return
	}
	s.audit.ClientConnected(c)
	s.logf("client #%d connected via %s, role=%s: %s", p.id, p.label, p.role, c.Actor())
	if s.cfg.Once {
		s.stopAccepting("--once: first client connected")
	}
}

func (p *peer) sendControl(c proto.Control) {
	b, err := json.Marshal(c)
	if err != nil {
		return
	}
	_ = p.dc.SendText(string(b))
}

// sendOutput must be called with s.mu held (keeps ordering with replay).
func (p *peer) sendOutput(b []byte) {
	if p.dc.BufferedAmount() > maxBuffered {
		go p.close("client too slow")
		return
	}
	_ = p.dc.Send(b)
}

func (p *peer) close(reason string) {
	p.mu.Lock()
	if p.state == stateClosed {
		p.mu.Unlock()
		return
	}
	wasReady := p.state == stateReady
	p.state = stateClosed
	hooks, input, client, viewer := p.onClose, p.input, p.client, p.viewer
	p.viewer = nil
	p.mu.Unlock()
	input.Close()
	if viewer != nil {
		p.leaveDesktop(viewer, client)
	}
	if wasReady && client != nil {
		p.s.audit.ClientDisconnected(*client, reason)
	}

	if p.deadline != nil {
		p.deadline.Stop()
	}
	p.s.releaseTicket(p)
	p.s.mu.Lock()
	delete(p.s.peers, p.id)
	p.s.mu.Unlock()
	go p.pc.Close()
	for _, h := range hooks {
		h()
	}
	if wasReady {
		p.s.logf("client #%d disconnected (%s)", p.id, reason)
	} else {
		p.s.debugf("client #%d dropped before joining (%s)", p.id, reason)
	}
}

func (p *peer) addCloseHook(f func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onClose = append(p.onClose, f)
}

// auditClient describes this peer for audit records, including the network
// address of the selected ICE candidate once connected.
func (p *peer) auditClient() audit.Client {
	p.mu.Lock()
	c := audit.Client{ID: p.id, Role: p.role, Via: p.label, KeyName: p.keyName, SAS: p.sas, Fingerprint: p.remoteFP,
		PublicAddresses: p.publicAddrs, LocalAddresses: p.localAddrs, Device: p.info, DeviceKey: p.deviceFP}
	p.mu.Unlock()
	if sctp := p.pc.SCTP(); sctp != nil {
		if pair, err := sctp.Transport().ICETransport().GetSelectedCandidatePair(); err == nil && pair != nil && pair.Remote != nil {
			c.Address, c.Port, c.CandidateType = pair.Remote.Address, int(pair.Remote.Port), pair.Remote.Typ.String()
			c.Transport = pair.Remote.Protocol.String()
		}
	}
	return c
}
