package host

import (
	"crypto/subtle"
	"errors"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/secure"
)

// Resume tickets let a client whose connection dropped (a phone switching
// networks, a laptop waking from sleep) reconnect to the same session
// without typing the password or being approved again, and without being
// refused because its old, dead connection still holds its slot.
//
// A ticket is created when a client becomes ready. Its token is random, is
// sent only over the authenticated data channel and is replaced on every
// resume, and the host keeps only a hash of it. A ticket is bound to the
// invite (role and personal key) and, if the client proved a device key, to
// that key. It outlives its connection by resumeGrace.
const (
	resumeGrace = 10 * time.Minute
	maxTickets  = 64
	// A client that sends pings but goes silent for this long is dropped,
	// so a hung connection does not hold its slot until ICE gives up. It is
	// long because browsers slow timers down in background tabs.
	idleTimeout = 90 * time.Second
)

var errResume = errors.New("invalid or expired resume token")

type ticket struct {
	id       string
	hash     [32]byte
	role     string
	keyName  string
	deviceFP string
	approval string
	firstID  int       // the client id the ticket was first issued to
	peer     *peer     // the connection using it, if any
	expires  time.Time // when unused; zero while a connection holds it
}

func (t *ticket) live(now time.Time) bool {
	return t.peer != nil || now.Before(t.expires)
}

// hasTicket reports whether id names a usable ticket. It is used before a
// connection exists, so it only decides admission; the token is checked later.
func (s *Session) hasTicket(id string) bool {
	if id == "" || s.cfg.Manual {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tickets[id]
	return t != nil && t.live(time.Now())
}

// claimTicket checks a resume token presented by p and hands the ticket to
// p. It returns the connection that held it before, which the caller
// closes. p's role, personal key and device key must match the ticket's.
func (s *Session) claimTicket(p *peer, token []byte) (*ticket, *peer, error) {
	if len(token) != secure.ResumeTokenSize || s.cfg.Manual {
		return nil, nil, errResume
	}
	id, hash := secure.ResumeID(token), secure.ResumeHash(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tickets[id]
	if t == nil || subtle.ConstantTimeCompare(t.hash[:], hash[:]) != 1 || !t.live(time.Now()) {
		return nil, nil, errResume
	}
	p.mu.Lock()
	dev := p.deviceFP
	p.mu.Unlock()
	if t.role != p.role || t.keyName != p.keyName || (t.deviceFP != "" && t.deviceFP != dev) {
		return nil, nil, errResume
	}
	old := t.peer
	t.peer, t.expires = p, time.Time{}
	p.ticket = t // so the grace period restarts if this connection fails too
	return t, old, nil
}

// issueTicket gives p a fresh token: a new ticket for a new client, or the
// rotated one it resumed with. Called with s.mu held.
func (s *Session) issueTicket(p *peer, t *ticket, approval string) []byte {
	if s.cfg.Manual {
		return nil
	}
	if t == nil {
		s.gcTicketsLocked(time.Now())
		if len(s.tickets) >= maxTickets {
			return nil // resuming is a convenience; never grow without bound
		}
		t = &ticket{role: p.role, keyName: p.keyName, deviceFP: p.deviceFP, approval: approval, firstID: p.id}
	} else {
		delete(s.tickets, t.id)
	}
	token := secure.RandomBytes(secure.ResumeTokenSize)
	t.id, t.hash = secure.ResumeID(token), secure.ResumeHash(token)
	t.peer, t.expires = p, time.Time{}
	s.tickets[t.id] = t
	p.ticket = t
	return token
}

// releaseTicket starts the grace period of the ticket p holds, if any.
func (s *Session) releaseTicket(p *peer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := p.ticket
	if t == nil || t.peer != p {
		return
	}
	t.peer, t.expires = nil, time.Now().Add(resumeGrace)
}

// liveTicketsLocked reports whether any client could still resume.
func (s *Session) liveTicketsLocked() bool {
	now := time.Now()
	for _, t := range s.tickets {
		if t.live(now) {
			return true
		}
	}
	return false
}

// gcTickets drops expired tickets and, once the invite has been disabled
// and nobody can resume any more, stops signaling.
func (s *Session) gcTickets() {
	s.mu.Lock()
	s.gcTicketsLocked(time.Now())
	stop := !s.accepting && len(s.tickets) == 0 && s.stopSignal != nil && !s.cfg.Manual
	sig := s.stopSignal
	s.mu.Unlock()
	if stop {
		sig()
	}
}

func (s *Session) gcTicketsLocked(now time.Time) {
	for id, t := range s.tickets {
		if !t.live(now) {
			delete(s.tickets, id)
		}
	}
}

// dropIdle closes clients that send pings but stopped answering.
func (s *Session) dropIdle() {
	now := time.Now()
	for _, p := range s.readyPeers(false) {
		if at := p.lastRx.Load(); p.pinger.Load() && at > 0 && now.Sub(time.Unix(0, at)) > idleTimeout {
			p.close("client unresponsive")
		}
	}
}
