package e2e

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/access"
	"github.com/dmdhrumilmistry/setu/internal/client"
	"github.com/dmdhrumilmistry/setu/internal/host"
	"github.com/dmdhrumilmistry/setu/internal/link"
	"github.com/dmdhrumilmistry/setu/internal/nostr/relaytest"
	"github.com/dmdhrumilmistry/setu/internal/proto"
	"github.com/dmdhrumilmistry/setu/internal/secure"
)

// rules is a mutable rule set standing in for an access.json file.
type rules struct {
	mu sync.Mutex
	rs []access.Rule
}

func (r *rules) set(t *testing.T, kind, value, action string) {
	t.Helper()
	rule, err := access.New(kind, value, action, "", 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.rs = []access.Rule{rule}
	r.mu.Unlock()
}

func (r *rules) check(s access.Subject) (bool, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return access.Decide(r.rs, s, time.Now())
}

func closedWithin(t *testing.T, c *client.Conn, what string) {
	t.Helper()
	select {
	case <-c.Closed:
	case <-time.After(15 * time.Second):
		t.Fatalf("%s: client was not disconnected", what)
	}
}

// TestAccessRules: block and allow rules refuse logins, disconnect connected
// clients (who then cannot resume), and an allow list admits only its
// matches; pausing refuses new joins only; ending a session stops it.
func TestAccessRules(t *testing.T) {
	t.Parallel()
	r := relaytest.New()
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var rl rules
	cs, _ := secure.NewSecret()
	ps, _ := secure.NewSecret()
	s, err := host.New(host.Config{
		Command: echoCommand(), ControlSecret: &cs, Relays: []string{r.URL()}, ICE: []proto.ICEServer{},
		MaxClients: 4, Log: newTlog(t), Access: rl.check,
		Keys: []host.Key{{Name: "alice", Role: proto.RoleControl, Secret: ps}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ended := make(chan struct{})
	go func() { defer close(ended); _, _ = s.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)
	shared := link.Build("", link.Invite{Secret: cs.String(), Relays: []string{r.URL()}})
	alice := link.Build("", link.Invite{Secret: ps.String(), Relays: []string{r.URL()}})
	keyA, _ := secure.NewDeviceKey()
	keyB, _ := secure.NewDeviceKey()
	n := 0

	// A blocked device is refused at login.
	rl.set(t, access.KindDevice, keyA.Fingerprint(), access.Block)
	if _, err := dialSecret(ctx, t, shared, keyA, "", &n); err == nil || !strings.Contains(err.Error(), "does not allow") {
		t.Fatalf("blocked device joined: %v", err)
	}
	b, err := dialSecret(ctx, t, shared, keyB, "", &n)
	if err != nil {
		t.Fatalf("another device was refused: %v", err)
	}
	defer b.Close()

	// Blocking a connected device disconnects it, and its token is dead.
	rl.set(t, access.KindDevice, keyB.Fingerprint(), access.Block)
	if got := s.Enforce(); got != 1 {
		t.Fatalf("Enforce disconnected %d clients", got)
	}
	closedWithin(t, b, "block")
	short, c2 := context.WithTimeout(ctx, 10*time.Second)
	defer c2()
	if _, err := b.Resume(short); err == nil {
		t.Fatal("a blocked client resumed")
	}

	// An allow list admits only its matches: alice's personal key.
	rl.set(t, access.KindPerson, "alice", access.Allow)
	if _, err := dialSecret(ctx, t, shared, keyA, "", &n); err == nil {
		t.Fatal("a client off the allow list joined")
	}
	a, err := dialSecret(ctx, t, alice, keyA, "", &n)
	if err != nil {
		t.Fatalf("allowed person refused: %v", err)
	}
	defer a.Close()
	waitFor(t, a, "READY")

	// Pausing refuses new joins but keeps alice.
	s.Pause(true)
	if !s.Status().Paused {
		t.Fatal("status does not say paused")
	}
	short2, c3 := context.WithTimeout(ctx, 10*time.Second)
	defer c3()
	if _, err := dialSecret(short2, t, alice, keyB, "", &n); err == nil {
		t.Fatal("joined a paused share")
	}
	if err := a.Write([]byte("still\r")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, a, "got:still")
	s.Pause(false)
	a2, err := dialSecret(ctx, t, alice, keyB, "", &n)
	if err != nil {
		t.Fatalf("join after resuming: %v", err)
	}
	defer a2.Close()

	if got := s.DisconnectAll("test"); got != 2 {
		t.Fatalf("DisconnectAll disconnected %d clients", got)
	}
	closedWithin(t, a, "disconnect all")
	closedWithin(t, a2, "disconnect all")

	if err := s.Terminate("test"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("Terminate did not end the share")
	}
}
