package e2e

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/host"
	"github.com/dmdhrumilmistry/setu/internal/nostr/relaytest"
	"github.com/dmdhrumilmistry/setu/internal/proto"
)

// TestResume reconnects a client the way a phone that switched networks
// does: without the password, past --once and --max-clients 1, and getting
// only the output it missed.
func TestResume(t *testing.T) {
	r := relaytest.New()
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	ctl, _ := startHost(t, ctx, r.URL(), func(c *host.Config) {
		c.Password = "correct horse"
		c.Once = true
		c.MaxClients = 1
	})

	c, err := dial(t, ctx, ctl, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !c.CanResume() {
		t.Fatal("host did not hand out a resume token")
	}
	waitFor(t, c, "READY")
	if err := c.Write([]byte("one\r")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, c, "got:one")

	// The old connection may still hold the only slot when the new one
	// arrives, and --once has disabled the invite: neither may stop it.
	c2, err := c.Resume(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if !c2.Resumed {
		t.Fatal("output did not continue where the old connection stopped")
	}
	if c2.Role != proto.RoleControl {
		t.Fatalf("role %q", c2.Role)
	}
	if err := c2.Write([]byte("two\r")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	deadline := time.After(30 * time.Second)
	for !strings.Contains(buf.String(), "got:two") {
		select {
		case b := <-c2.Out:
			buf.Write(b)
		case <-deadline:
			t.Fatalf("timed out; got %q", buf.String())
		}
	}
	if strings.Contains(buf.String(), "READY") || strings.Contains(buf.String(), "got:one") {
		t.Fatalf("resumed connection replayed output it already had: %q", buf.String())
	}

	// The token is single use: the old connection's one no longer works,
	// and the invite itself stays disabled for anyone else.
	if _, err := c.Resume(ctx); err == nil {
		t.Fatal("a used resume token worked twice")
	}
	short, cancel2 := context.WithTimeout(ctx, 8*time.Second)
	defer cancel2()
	if _, err := dial(t, short, ctl, "correct horse"); err == nil {
		t.Fatal("--once invite accepted a second client")
	}

	// The current connection can resume again.
	c3, err := c2.Resume(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c3.Close()
	if err := c3.Write([]byte("three\r")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, c3, "got:three")
}

// TestResumeView keeps a view-only client view-only across a reconnect.
func TestResumeView(t *testing.T) {
	r := relaytest.New()
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, view := startHost(t, ctx, r.URL(), nil)

	v, err := dial(t, ctx, view, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, v, "READY")
	v2, err := v.Resume(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer v2.Close()
	if v2.Role != proto.RoleView {
		t.Fatalf("resumed view client got role %q", v2.Role)
	}
}
