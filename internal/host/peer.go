package host

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"sync"
	"time"

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

	pc *webrtc.PeerConnection
	dc *webrtc.DataChannel

	mu       sync.Mutex
	state    peerState
	nonce    []byte
	localFP  string
	remoteFP string
	sas      string
	onClose  []func()
	deadline *time.Timer
}

// newPeer registers a peer after checking admission.
func (s *Session) newPeer(role, label string) (*peer, error) {
	if err := s.admit(); err != nil {
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
	s.mu.Lock()
	s.nextID++
	p := &peer{s: s, id: s.nextID, role: role, label: label, pc: pc, dc: dc, nonce: secure.RandomBytes(32)}
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
	p.mu.Lock()
	p.localFP, p.remoteFP = localFP, remoteFP
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
		hello.Iter = secure.PBKDF2Iterations
	}
	p.sendControl(hello)
}

func (p *peer) onMessage(msg webrtc.DataChannelMessage) {
	p.mu.Lock()
	st := p.state
	p.mu.Unlock()

	if !msg.IsString {
		if st == stateReady && p.role == proto.RoleControl && len(msg.Data) <= maxInputFrame {
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
		p.mu.Unlock()
		if p.s.pwKey != nil {
			want := secure.AuthProof(p.s.pwKey, p.nonce, p.localFP, p.remoteFP)
			if !hmac.Equal(want, c.Proof) {
				p.s.authFailed(p)
				p.sendControl(proto.Control{T: proto.CtlError, Msg: "authentication failed"})
				time.AfterFunc(200*time.Millisecond, func() { p.close("authentication failed") })
				return
			}
		}
		go p.activate()
	case proto.CtlResize:
		if st == stateReady && p.role == proto.RoleControl {
			p.s.resize(c.Cols, c.Rows)
		}
	}
}

func (p *peer) activate() {
	if !p.s.approve(p) {
		p.sendControl(proto.Control{T: proto.CtlError, Msg: "the host denied the connection"})
		time.AfterFunc(200*time.Millisecond, func() { p.close("denied by host") })
		return
	}
	s := p.s
	s.mu.Lock()
	p.mu.Lock()
	if p.state != stateApproving {
		p.mu.Unlock()
		s.mu.Unlock()
		return
	}
	p.state = stateReady
	p.mu.Unlock()
	s.failures = 0
	p.sendControl(proto.Control{T: proto.CtlReady, V: proto.Version, Role: p.role, Cmd: s.commandLine(), Host: s.host, Cols: s.cols, Rows: s.rows})
	if len(s.ring) > 0 {
		for off := 0; off < len(s.ring); off += 16 << 10 {
			end := min(off+16<<10, len(s.ring))
			p.sendOutput(s.ring[off:end])
		}
	}
	s.mu.Unlock()
	p.deadline.Stop()
	s.logf("client #%d connected via %s, role=%s, verification code %s", p.id, p.label, p.role, p.sas)
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
	hooks := p.onClose
	p.mu.Unlock()

	if p.deadline != nil {
		p.deadline.Stop()
	}
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
