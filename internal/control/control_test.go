package control

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/host"
)

type fakeHandler struct{ kicked []int }

func (f *fakeHandler) Shares() []host.Status {
	return []host.Status{{ID: "abc", Profile: "p", Clients: []host.ClientStatus{{ID: 3, Name: "alice"}}}}
}

func (f *fakeHandler) Disconnect(profile string, client int) error {
	if profile != "p" || client != 3 {
		return host.ErrNoClient
	}
	f.kicked = append(f.kicked, client)
	return nil
}

// shortTemp is a directory with a short path: socket paths are limited.
func shortTemp(t *testing.T) string {
	base := ""
	if runtime.GOOS != "windows" {
		base = "/tmp"
	}
	d, err := os.MkdirTemp(base, "sc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func startServer(t *testing.T, dir string, h Handler) {
	t.Helper()
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go Serve(ctx, ln, h, t.Logf)
}

func TestStatusAndDisconnect(t *testing.T) {
	dir := shortTemp(t)
	h := &fakeHandler{}
	startServer(t, dir, h)
	ctx := context.Background()

	resp, err := Call(ctx, dir, Request{Op: OpStatus})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Shares) != 1 || resp.Shares[0].Clients[0].Name != "alice" {
		t.Fatalf("status %+v", resp)
	}
	if _, err := Call(ctx, dir, Request{Op: OpDisconnect, Profile: "p", Client: 3}); err != nil {
		t.Fatal(err)
	}
	if len(h.kicked) != 1 {
		t.Fatal("disconnect did not reach the handler")
	}
	if _, err := Call(ctx, dir, Request{Op: OpDisconnect, Profile: "p", Client: 4}); err == nil || !strings.Contains(err.Error(), "no such client") {
		t.Fatalf("handler error not passed back: %v", err)
	}
	if _, err := Call(ctx, dir, Request{Op: "shell"}); err == nil {
		t.Fatal("unknown operation accepted")
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(dir, SocketName))
		if st.Mode().Perm()&0o077 != 0 {
			t.Fatalf("socket mode %v", st.Mode().Perm())
		}
	}
}

func TestMalformedRequest(t *testing.T) {
	dir := shortTemp(t)
	startServer(t, dir, &fakeHandler{})
	c, err := net.Dial("unix", SocketPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte(`{"op":"status","extra":1}` + "\n"))
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	b := make([]byte, 256)
	n, _ := c.Read(b)
	if !strings.Contains(string(b[:n]), "malformed") {
		t.Fatalf("unknown fields accepted: %s", b[:n])
	}
}

func TestNotRunningAndStale(t *testing.T) {
	dir := shortTemp(t)
	if _, err := Call(context.Background(), dir, Request{Op: OpStatus}); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("no socket: %v", err)
	}
	// A socket left behind by a dead process is replaced...
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ul, ok := ln.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	ln.Close()
	startServer(t, dir, &fakeHandler{})
	// ...but a live one is not taken over.
	if _, err := Listen(dir); err == nil || !strings.Contains(err.Error(), "already listening") {
		t.Fatalf("took over a live socket: %v", err)
	}
}

func TestLongDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), strings.Repeat("d", 60), strings.Repeat("e", 60))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	startServer(t, dir, &fakeHandler{})
	if p := SocketPath(dir); len(p) > maxPath || strings.HasPrefix(p, dir) {
		t.Fatalf("socket path %q", p)
	}
	if _, err := Call(context.Background(), dir, Request{Op: OpStatus}); err != nil {
		t.Fatal(err)
	}
}
