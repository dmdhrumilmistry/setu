package host

import (
	"testing"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/proto"
	"github.com/dmdhrumilmistry/setu/internal/secure"
)

func testSession(t *testing.T) *Session {
	t.Helper()
	sec, _ := secure.NewSecret()
	s, err := New(Config{Command: []string{"true"}, ControlSecret: &sec})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func issue(s *Session, p *peer) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.issueTicket(p, nil, "")
}

// A client in a background tab keeps its session resumable for longer.
func TestResumeGraceBackground(t *testing.T) {
	for _, hidden := range []bool{false, true} {
		s := testSession(t)
		p := &peer{s: s, id: 1, role: proto.RoleControl}
		issue(s, p)
		p.hidden.Store(hidden)
		s.releaseTicket(p)
		want := resumeGrace
		if hidden {
			want = backgroundGrace
		}
		left := time.Until(p.ticket.expires)
		if left < want-time.Minute || left > want {
			t.Errorf("hidden=%v: ticket expires in %s, want about %s", hidden, left, want)
		}
	}
}

// Tokens are bound to the role and rotate on every resume.
func TestClaimTicket(t *testing.T) {
	s := testSession(t)
	p := &peer{s: s, id: 1, role: proto.RoleControl}
	tok := issue(s, p)
	s.releaseTicket(p)

	if _, _, err := s.claimTicket(&peer{s: s, id: 2, role: proto.RoleView}, tok); err == nil {
		t.Fatal("a view client claimed a control ticket")
	}
	bad := append([]byte(nil), tok...)
	bad[0] ^= 1
	if _, _, err := s.claimTicket(&peer{s: s, id: 2, role: proto.RoleControl}, bad); err == nil {
		t.Fatal("a wrong token was accepted")
	}
	p2 := &peer{s: s, id: 2, role: proto.RoleControl}
	tk, _, err := s.claimTicket(p2, tok)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.issueTicket(p2, tk, "")
	s.mu.Unlock()
	if _, _, err := s.claimTicket(&peer{s: s, id: 3, role: proto.RoleControl}, tok); err == nil {
		t.Fatal("a rotated token was accepted again")
	}
}

// A background client that cannot ping is not dropped as unresponsive.
func TestDropIdleSkipsBackground(t *testing.T) {
	s := testSession(t)
	stale := time.Now().Add(-2 * idleTimeout).UnixNano()
	mk := func(id int, hidden bool) *peer {
		p := &peer{s: s, id: id, role: proto.RoleControl, state: stateReady}
		p.pinger.Store(true)
		p.hidden.Store(hidden)
		p.lastRx.Store(stale)
		s.peers[id] = p
		return p
	}
	mk(1, true)
	fg := mk(2, false)
	idle := s.idlePeers(time.Now())
	if len(idle) != 1 || idle[0] != fg {
		t.Fatalf("idle peers = %v, want only the foreground one", idle)
	}
}
