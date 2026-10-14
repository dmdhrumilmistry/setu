package service

import (
	"bytes"
	"context"
	"log"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/client"
	"github.com/dmdhrumilmistry/setu/internal/host"
	"github.com/dmdhrumilmistry/setu/internal/link"
	"github.com/dmdhrumilmistry/setu/internal/nostr/relaytest"
	"github.com/dmdhrumilmistry/setu/internal/proto"
	"github.com/dmdhrumilmistry/setu/internal/secure"
)

func echoCommand() []string {
	if runtime.GOOS == "windows" {
		return []string{"powershell.exe", "-NoLogo", "-NoProfile", "-Command",
			`Write-Output READY; while ($true) { $l = [Console]::ReadLine(); if ($null -eq $l) { break }; Write-Output ('got:' + $l) }`}
	}
	return []string{"sh", "-c", `echo READY; while read l; do echo "got:$l"; done`}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func dialAndEcho(ctx context.Context, t *testing.T, key, relay, password, line string) error {
	t.Helper()
	inv := link.Build("", link.Invite{Secret: key, Relays: []string{relay}})
	c, err := client.Dial(ctx, inv, client.Options{
		Timeout:  20 * time.Second,
		Logf:     t.Logf,
		Password: func() (string, error) { return password, nil },
	})
	if err != nil {
		return err
	}
	defer c.Close()
	var got bytes.Buffer
	sent := false
	deadline := time.After(30 * time.Second)
	for !strings.Contains(got.String(), "got:"+line) {
		if !sent && strings.Contains(got.String(), "READY") {
			_ = c.Write([]byte(line + "\r"))
			sent = true
		}
		select {
		case b := <-c.Out:
			got.Write(b)
		case <-deadline:
			t.Fatalf("no echo of %q; got %q", line, got.String())
		}
	}
	return nil
}

func TestSupervisor(t *testing.T) {
	r := relaytest.New()
	defer r.Close()
	dir := t.TempDir()
	path := ConfigPath(dir)
	v, err := secure.NewPasswordVerifier("service-test-password")
	if err != nil {
		t.Fatal(err)
	}
	p := &Profile{Command: echoCommand(), Enabled: true, MaxClients: 2, Password: v, Relays: []string{r.URL()}}
	if err := p.NewKeys(false, time.Now()); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{Profiles: map[string]*Profile{"echo": p}}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	logs := &syncBuf{}
	sv := &Supervisor{Dir: dir, Version: "test", Log: log.New(logs, "", 0),
		hostHook: func(c *host.Config) { c.ICE = []proto.ICEServer{} }}
	done := make(chan error, 1)
	go func() { done <- sv.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
		t.Logf("supervisor log:\n%s", logs.String())
	})
	time.Sleep(time.Second)

	// One supervisor per directory.
	if err := (&Supervisor{Dir: dir, Log: log.New(&bytes.Buffer{}, "", 0)}).Run(ctx); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second supervisor: %v", err)
	}

	if err := dialAndEcho(ctx, t, p.ControlKey, r.URL(), "service-test-password", "first"); err != nil {
		t.Fatal(err)
	}

	// Rotation: the old key stops working, the new one works.
	old := p.ControlKey
	if err := p.NewKeys(false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2*pollInterval + time.Second)
	short, cancelShort := context.WithTimeout(ctx, 6*time.Second)
	if err := dialAndEcho(short, t, old, r.URL(), "service-test-password", "x"); err == nil {
		t.Fatal("rotated-out key still works")
	}
	cancelShort()
	if err := dialAndEcho(ctx, t, p.ControlKey, r.URL(), "service-test-password", "rotated"); err != nil {
		t.Fatal(err)
	}

	// Disabling stops serving.
	p.Enabled = false
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2*pollInterval + time.Second)
	short2, cancelShort2 := context.WithTimeout(ctx, 6*time.Second)
	defer cancelShort2()
	if err := dialAndEcho(short2, t, p.ControlKey, r.URL(), "service-test-password", "x"); err == nil {
		t.Fatal("disabled profile still served")
	}
}

func TestSupervisorAuditFailsClosed(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Profiles: map[string]*Profile{}}
	cfg.Audit.Endpoint = "http://127.0.0.1:1/v1/logs" // nothing listens here
	cfg.Audit.Required = true
	p := &Profile{Command: echoCommand(), Enabled: true, MaxClients: 1}
	if err := p.NewKeys(false, time.Now()); err != nil {
		t.Fatal(err)
	}
	cfg.Profiles["echo"] = p
	if err := Save(ConfigPath(dir), cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	logs := &syncBuf{}
	sv := &Supervisor{Dir: dir, Version: "test", Log: log.New(logs, "", 0)}
	done := make(chan error, 1)
	go func() { done <- sv.Run(ctx) }()
	deadline := time.Now().Add(30 * time.Second)
	for !strings.Contains(logs.String(), "audit unavailable") {
		if time.Now().After(deadline) {
			t.Fatalf("no fail-closed message; log:\n%s", logs.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
	if strings.Contains(logs.String(), "profile echo: serving") {
		t.Fatalf("profile served without a working audit pipeline:\n%s", logs.String())
	}
	cancel()
	<-done
}
