package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/client"
	"github.com/dmdhrumilmistry/setu/internal/host"
	"github.com/dmdhrumilmistry/setu/internal/nostr/relaytest"
	"github.com/dmdhrumilmistry/setu/internal/proto"
)

// TestIdleTimeout: an idle control client is disconnected with the reason;
// one that keeps typing stays, and so does a view-only client, which only
// watches.
func TestIdleTimeout(t *testing.T) {
	r := relaytest.New()
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	const limit = 3 * time.Second
	ctl, view := startHost(t, ctx, r.URL(), func(c *host.Config) { c.IdleTimeout = limit })

	// The busy client starts typing as soon as it is in: connecting the
	// others can take longer than the timeout.
	busy, err := dial(t, ctx, ctl, "")
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-busy.Out: // keep reading, as a terminal does
			case <-time.After(300 * time.Millisecond):
				_ = busy.Write([]byte("x"))
			}
		}
	}()
	watcher, err := dial(t, ctx, view, "")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-watcher.Out:
			}
		}
	}()
	idle, err := dial(t, ctx, ctl, "")
	if err != nil {
		t.Fatal(err)
	}

	if msg := waitError(t, idle, 3*limit); !strings.Contains(msg, "without input") {
		t.Fatalf("idle client told %q", msg)
	}
	select {
	case <-idle.Closed:
	case <-idle.Failed:
	case <-time.After(5 * time.Second):
		t.Fatal("idle client was not disconnected")
	}
	time.Sleep(limit + time.Second) // longer than the timeout again
	for name, c := range map[string]*client.Conn{"typing control client": busy, "view-only client": watcher} {
		select {
		case <-c.Closed:
			t.Errorf("%s was disconnected", name)
		case <-c.Failed:
			t.Errorf("%s connection failed", name)
		default:
		}
	}
}

func waitError(t *testing.T, c *client.Conn, within time.Duration) string {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case m := <-c.Ctl:
			if m.T == proto.CtlError {
				return m.Msg
			}
		case <-c.Out:
		case <-deadline:
			t.Fatal("no error message")
			return ""
		}
	}
}
