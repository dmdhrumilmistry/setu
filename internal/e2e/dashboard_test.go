package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/history"
	"github.com/dmdhrumilmistry/setu/internal/host"
	"github.com/dmdhrumilmistry/setu/internal/link"
	"github.com/dmdhrumilmistry/setu/internal/nostr/relaytest"
	"github.com/dmdhrumilmistry/setu/internal/proto"
	"github.com/dmdhrumilmistry/setu/internal/secure"
)

type eventLog struct {
	mu  sync.Mutex
	evs []history.Event
}

func (l *eventLog) add(e history.Event) {
	l.mu.Lock()
	l.evs = append(l.evs, e)
	l.mu.Unlock()
}

func (l *eventLog) kinds() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, e := range l.evs {
		out = append(out, e.Kind)
	}
	return out
}

func (l *eventLog) find(kind string) (history.Event, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.evs {
		if e.Kind == kind {
			return e, true
		}
	}
	return history.Event{}, false
}

// TestStatusDisconnectAndHistory covers what `setu dashboard` relies on: the
// host's status snapshot, disconnecting a client on the host's behalf (which
// must also revoke its resume token) and the connection history events.
func TestStatusDisconnectAndHistory(t *testing.T) {
	t.Parallel()
	r := relaytest.New()
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cs, _ := secure.NewSecret()
	vs, _ := secure.NewSecret()
	var evs eventLog
	s, err := host.New(host.Config{
		Command: echoCommand(), ControlSecret: &cs, ViewSecret: &vs, Relays: []string{r.URL()},
		ICE: []proto.ICEServer{}, MaxClients: 3, Log: newTlog(t), Password: "pass-word-1",
		Events: evs.add,
	})
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ctx, s)
	time.Sleep(300 * time.Millisecond)
	ctl := link.Build("", link.Invite{Secret: cs.String(), Relays: []string{r.URL()}})
	view := link.Build("", link.Invite{Secret: vs.String(), Relays: []string{r.URL()}})

	if _, err := dial(t, ctx, ctl, "wrong"); err == nil {
		t.Fatal("wrong password accepted")
	}
	c, err := dial(t, ctx, ctl, "pass-word-1")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	waitFor(t, c, "READY")
	v, err := dial(t, ctx, view, "pass-word-1")
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()

	// A successful login clears the run of failures, not the total.
	st := s.Status()
	if len(st.Clients) != 2 || st.Auth != "password" || !st.Accepting || !st.ViewLink || st.TotalFailures != 1 || st.RecentFailures != 0 || st.Started.IsZero() {
		t.Fatalf("status: %+v", st)
	}
	var ctlID int
	for _, cl := range st.Clients {
		if cl.Since.IsZero() || cl.SAS == "" || cl.Address == "" {
			t.Fatalf("client status missing details: %+v", cl)
		}
		switch cl.Role {
		case proto.RoleControl:
			ctlID = cl.ID
			if cl.Name != "shared-control-link" || cl.Personal || cl.Auth != proto.AuthPassword || cl.SAS != c.SAS {
				t.Fatalf("control client: %+v", cl)
			}
		case proto.RoleView:
			if cl.Name != "shared-view-link" || !cl.LastInput.IsZero() {
				t.Fatalf("view client: %+v", cl)
			}
		}
	}
	// Nothing secret may leave the host in a status.
	b, _ := json.Marshal(st)
	for _, secret := range []string{cs.String(), vs.String(), "pass-word-1"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("status leaks a secret: %s", b)
		}
	}

	if err := s.Disconnect(999, ""); !errors.Is(err, host.ErrNoClient) {
		t.Fatalf("disconnecting an unknown client: %v", err)
	}
	if err := s.Disconnect(ctlID, "disconnected from the dashboard"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Closed:
	case <-time.After(10 * time.Second):
		t.Fatal("client was not disconnected")
	}
	// It cannot slip back in with its resume token.
	short, cancel2 := context.WithTimeout(ctx, 10*time.Second)
	defer cancel2()
	if _, err := c.Resume(short); err == nil {
		t.Fatal("a disconnected client resumed")
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(s.Status().Clients) != 1 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if n := len(s.Status().Clients); n != 1 {
		t.Fatalf("%d clients after disconnect", n)
	}

	want := []string{history.SessionStart, history.AuthFailure, history.Connect, history.Disconnect}
	got := strings.Join(evs.kinds(), ",")
	for _, k := range want {
		if !strings.Contains(got, k) {
			t.Fatalf("history events %s lack %s", got, k)
		}
	}
	d, _ := evs.find(history.Disconnect)
	if d.Reason != "disconnected from the dashboard" || d.Name != "shared-control-link" || d.Session == "" || d.Time.IsZero() {
		t.Fatalf("disconnect event: %+v", d)
	}
	f, _ := evs.find(history.AuthFailure)
	if f.Role != proto.RoleControl {
		t.Fatalf("auth failure event: %+v", f)
	}
}
