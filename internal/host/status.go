package host

import (
	"errors"
	"sort"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/audit"
	"github.com/dmdhrumilmistry/setu/internal/history"
	"github.com/dmdhrumilmistry/setu/internal/proto"
)

// Status is a point-in-time view of a share for the local dashboard. It
// holds nothing secret: no links, keys, tokens or password material.
type Status struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"` // proto.KindTerminal or proto.KindDesktop
	Command    string    `json:"command"`
	Profile    string    `json:"profile,omitempty"`
	Started    time.Time `json:"started"`
	Accepting  bool      `json:"accepting"`
	Auth       string    `json:"auth"` // "device", "password" or "none"
	ViewLink   bool      `json:"view_link"`
	MaxClients int       `json:"max_clients"`
	// LockedUntil is set while new joins are refused after failed logins.
	LockedUntil    time.Time      `json:"locked_until,omitzero"`
	RecentFailures int            `json:"recent_failures"`
	TotalFailures  int            `json:"total_failures"`
	Pending        int            `json:"pending"`   // handshakes in progress
	Resumable      int            `json:"resumable"` // dropped clients that may still come back
	Clients        []ClientStatus `json:"clients"`
}

// ClientStatus describes one connected client.
type ClientStatus struct {
	ID   int    `json:"id"`
	Name string `json:"name"` // personal key, or which shared link
	Role string `json:"role"`
	// Personal is true when Name comes from a personal key, which names
	// one person; a shared link cannot tell its holders apart.
	Personal      bool      `json:"personal"`
	Auth          string    `json:"auth,omitempty"` // proto.Auth*
	Since         time.Time `json:"since"`
	FirstID       int       `json:"first_id,omitempty"` // set when the client resumed
	LastInput     time.Time `json:"last_input,omitzero"`
	Hidden        bool      `json:"hidden,omitempty"` // its page is in the background
	Address       string    `json:"address,omitempty"`
	Port          int       `json:"port,omitempty"`
	CandidateType string    `json:"candidate_type,omitempty"`
	Transport     string    `json:"transport,omitempty"`
	DeviceKey     string    `json:"device_key,omitempty"`
	// Device is what the client said about itself (sanitized): display
	// only, never a basis for a decision.
	Device string `json:"device,omitempty"`
	SAS    string `json:"verification_code,omitempty"`
}

// Status reports the share's current state.
func (s *Session) Status() Status {
	now := time.Now()
	s.mu.Lock()
	st := Status{
		ID: s.id, Kind: s.kind(), Command: s.commandLine(), Profile: s.cfg.Profile, Started: s.started,
		Accepting: s.accepting, ViewLink: s.cfg.ViewSecret != nil, MaxClients: s.cfg.MaxClients,
		RecentFailures: s.failures, TotalFailures: s.totalFails,
	}
	if now.Before(s.lockedUntil) {
		st.LockedUntil = s.lockedUntil
	}
	var ready []*peer
	for _, p := range s.peers {
		if p.isReady() {
			ready = append(ready, p)
		} else {
			st.Pending++
		}
	}
	for _, t := range s.tickets {
		if t.peer == nil && t.live(now) {
			st.Resumable++
		}
	}
	s.mu.Unlock()
	switch {
	case s.cfg.RequireDevice:
		st.Auth = "device"
	case s.pwKey != nil:
		st.Auth = "password"
	default:
		st.Auth = "none"
	}
	st.Clients = []ClientStatus{}
	for _, p := range ready {
		st.Clients = append(st.Clients, p.status())
	}
	sort.Slice(st.Clients, func(i, j int) bool { return st.Clients[i].ID < st.Clients[j].ID })
	return st
}

func (p *peer) status() ClientStatus {
	p.mu.Lock()
	c := p.client
	cs := ClientStatus{ID: p.id, Role: p.role, Auth: p.authBy, Since: p.connectedAt, FirstID: p.firstID, Personal: p.keyName != ""}
	p.mu.Unlock()
	if c == nil {
		ac := p.auditClient()
		c = &ac
	}
	cs.Name = c.ActorName()
	cs.Address, cs.Port, cs.CandidateType, cs.Transport = c.Address, c.Port, c.CandidateType, c.Transport
	if c.CandidateType == "relay" && len(c.PublicAddresses) > 0 {
		cs.Address, cs.Port = c.PublicAddresses[0], 0 // the client's own address, not the TURN server's
	}
	cs.DeviceKey, cs.SAS, cs.Device = c.DeviceKey, c.SAS, c.Device.Summary()
	if at := p.lastInput.Load(); at > 0 && p.role == proto.RoleControl {
		cs.LastInput = time.Unix(0, at)
	}
	cs.Hidden = p.hidden.Load()
	return cs
}

// ErrNoClient is returned by Disconnect for a client that is not connected.
var ErrNoClient = errors.New("no such client is connected")

// Disconnect ends client id's connection on the host's behalf. Its resume
// token is revoked first, so it cannot slip back in by reconnecting: it
// has to join (and log in) again.
func (s *Session) Disconnect(id int, reason string) error {
	s.mu.Lock()
	p := s.peers[id]
	s.mu.Unlock()
	if p == nil || !p.isReady() {
		return ErrNoClient
	}
	if reason == "" {
		reason = "disconnected by the host"
	}
	s.revokeTicket(p)
	p.mu.Lock()
	p.kicked = reason
	p.mu.Unlock()
	p.sendControl(proto.Control{T: proto.CtlError, Msg: "the host disconnected you"})
	s.logf("client #%d: %s", id, reason)
	time.AfterFunc(200*time.Millisecond, func() { p.close(reason) })
	return nil
}

// event hands e to Config.Events, filling in what every event shares.
func (s *Session) event(e history.Event) {
	if s.cfg.Events == nil {
		return
	}
	e.Time = time.Now()
	e.Session = s.id
	s.cfg.Events(e)
}

// clientEvent is event for something a client did.
func (s *Session) clientEvent(kind string, c audit.Client, reason string, d time.Duration) {
	addr := c.Address
	if c.CandidateType == "relay" && len(c.PublicAddresses) > 0 {
		addr = c.PublicAddresses[0]
	}
	s.event(history.Event{Kind: kind, Client: c.ID, Name: c.ActorName(), Role: c.Role, DeviceKey: c.DeviceKey,
		Device: c.Device.Summary(), Address: addr, Reason: reason, Seconds: d.Round(time.Second).Seconds()})
}
