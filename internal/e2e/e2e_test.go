// Package e2e runs a real host (PTY + WebRTC) against the Go client over an
// in-process nostr relay.
package e2e

import (
	"bytes"
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/audit"
	"github.com/dmdhrumilmistry/setu/internal/audit/audittest"
	"github.com/dmdhrumilmistry/setu/internal/client"
	"github.com/dmdhrumilmistry/setu/internal/host"
	"github.com/dmdhrumilmistry/setu/internal/link"
	"github.com/dmdhrumilmistry/setu/internal/nostr/relaytest"
	"github.com/dmdhrumilmistry/setu/internal/proto"
	"github.com/dmdhrumilmistry/setu/internal/secure"
)

// echoCommand prints READY, then echoes every input line as "got:<line>".
func echoCommand() []string {
	if runtime.GOOS == "windows" {
		return []string{"powershell.exe", "-NoLogo", "-NoProfile", "-Command",
			`Write-Output READY; while ($true) { $l = [Console]::ReadLine(); if ($null -eq $l) { break }; Write-Output ('got:' + $l) }`}
	}
	return []string{"sh", "-c", `echo READY; while read l; do echo "got:$l"; done`}
}

// tlog forwards host logs to the test until the test finishes; the host may
// still log while it shuts down, which testing forbids after completion.
type tlog struct {
	t    *testing.T
	mu   sync.Mutex
	done bool
}

func newTlog(t *testing.T) *tlog {
	l := &tlog{t: t}
	t.Cleanup(func() {
		l.mu.Lock()
		l.done = true
		l.mu.Unlock()
	})
	return l
}

func (w *tlog) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.done {
		w.t.Logf("host: %s", bytes.TrimSpace(b))
	}
	return len(b), nil
}

// runHost runs s until the test ends, then cancels it and waits for it to stop.
func runHost(t *testing.T, ctx context.Context, s *host.Session) {
	ctx, cancel := context.WithCancel(ctx)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_, _ = s.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
		}
	})
}

func startHost(t *testing.T, ctx context.Context, relay string, mod func(*host.Config)) (control, view string) {
	t.Helper()
	cs, _ := secure.NewSecret()
	vs, _ := secure.NewSecret()
	cfg := host.Config{
		Command:       echoCommand(),
		ControlSecret: &cs,
		ViewSecret:    &vs,
		Relays:        []string{relay},
		ICE:           []proto.ICEServer{}, // local candidates only
		MaxClients:    3,
		Log:           newTlog(t),
		Verbose:       true,
	}
	if mod != nil {
		mod(&cfg)
	}
	s, err := host.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ctx, s)
	time.Sleep(300 * time.Millisecond) // let the host subscribe
	return link.Build("https://example.invalid/", link.Invite{Secret: cs.String(), Relays: []string{relay}}),
		link.Build("", link.Invite{Secret: vs.String(), Relays: []string{relay}})
}

func dial(t *testing.T, ctx context.Context, inv, password string) (*client.Conn, error) {
	return client.Dial(ctx, inv, client.Options{
		Timeout:  30 * time.Second,
		Logf:     t.Logf,
		Password: func() (string, error) { return password, nil },
	})
}

func waitFor(t *testing.T, c *client.Conn, want string) {
	t.Helper()
	var buf bytes.Buffer
	deadline := time.After(30 * time.Second) // PowerShell on CI starts slowly
	for !strings.Contains(buf.String(), want) {
		select {
		case b := <-c.Out:
			buf.Write(b)
		case <-deadline:
			t.Fatalf("timed out waiting for %q; got %q", want, buf.String())
		}
	}
}

func TestControlAndView(t *testing.T) {
	r := relaytest.New()
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ctl, view := startHost(t, ctx, r.URL(), nil)

	c, err := dial(t, ctx, ctl, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Role != proto.RoleControl {
		t.Fatalf("role %q", c.Role)
	}
	waitFor(t, c, "READY")
	if err := c.Write([]byte("hello\r")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, c, "got:hello")

	v, err := dial(t, ctx, view, "")
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if v.Role != proto.RoleView {
		t.Fatalf("view role %q", v.Role)
	}
	waitFor(t, v, "got:hello") // replay buffer
	_ = v.Write([]byte("evil\r"))
	_ = c.Write([]byte("second\r"))
	waitFor(t, c, "got:second")
	time.Sleep(300 * time.Millisecond)
	for {
		select {
		case b := <-c.Out:
			if strings.Contains(string(b), "evil") {
				t.Fatal("view-only client was able to type")
			}
			continue
		default:
		}
		break
	}
	if c.SAS == "" || len(c.SAS) != 7 {
		t.Fatalf("bad SAS %q", c.SAS)
	}
}

func TestPassword(t *testing.T) {
	r := relaytest.New()
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ctl, _ := startHost(t, ctx, r.URL(), func(c *host.Config) { c.Password = "s3cret-pass" })

	if _, err := dial(t, ctx, ctl, "wrong-password"); err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("expected auth failure, got %v", err)
	}
	c, err := dial(t, ctx, ctl, "s3cret-pass")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	waitFor(t, c, "READY")
}

func TestPasswordVerifier(t *testing.T) {
	r := relaytest.New()
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	v, err := secure.NewPasswordVerifier("stored-pass-123")
	if err != nil {
		t.Fatal(err)
	}
	ctl, _ := startHost(t, ctx, r.URL(), func(c *host.Config) { c.PasswordVerifier = v })
	if _, err := dial(t, ctx, ctl, "wrong-password"); err == nil {
		t.Fatal("wrong password accepted")
	}
	c, err := dial(t, ctx, ctl, "stored-pass-123")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	waitFor(t, c, "READY")
}

func TestAudit(t *testing.T) {
	r := relaytest.New()
	defer r.Close()
	col := audittest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	al, err := audit.New(ctx, audit.Config{Endpoint: col.URL, Required: true}, audit.Options{Version: "e2e"})
	if err != nil {
		t.Fatal(err)
	}
	ctl, view := startHost(t, ctx, r.URL(), func(c *host.Config) { c.Audit = al })

	c, err := dial(t, ctx, ctl, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, c, "READY")
	_ = c.Write([]byte("echo audited\r"))
	waitFor(t, c, "got:echo audited")
	v, err := dial(t, ctx, view, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, v, "got:echo audited")
	_ = v.Write([]byte("view cannot type\r"))
	time.Sleep(300 * time.Millisecond)
	v.Close()
	c.Close()
	time.Sleep(500 * time.Millisecond) // let the host notice the disconnects
	if err := al.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	cmds := col.Events("setu.command")
	if len(cmds) != 1 || audittest.Body(cmds[0]) != "echo audited" {
		var got []string
		for _, r := range cmds {
			got = append(got, audittest.Body(r))
		}
		t.Fatalf("commands %q, want only the control client's line", got)
	}
	if audittest.Attr(cmds[0], "setu.client.role") != proto.RoleControl || audittest.Attr(cmds[0], "setu.client.via") != "nostr" {
		t.Errorf("command attribution: %v", cmds[0].Attributes)
	}
	conns := col.Events("setu.client.connect")
	if len(conns) != 2 {
		t.Fatalf("want 2 connect records, got %d", len(conns))
	}
	if got := audittest.Attr(conns[0], "setu.client.verification_code"); got != c.SAS {
		t.Errorf("verification code %q, client saw %q", got, c.SAS)
	}
	if audittest.Attr(conns[0], "client.address") == "" {
		t.Error("connect record has no client address")
	}
	if len(col.Events("setu.session.start")) != 1 || len(col.Events("setu.client.disconnect")) != 2 {
		t.Errorf("lifecycle records: start=%d disconnect=%d", len(col.Events("setu.session.start")), len(col.Events("setu.client.disconnect")))
	}
}

// TestAuditEchoOff checks that a line typed at a password prompt (canonical
// mode, echo off) never reaches the collector.
func TestAuditEchoOff(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ConPTY does not expose the terminal mode")
	}
	r := relaytest.New()
	defer r.Close()
	col := audittest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	al, err := audit.New(ctx, audit.Config{Endpoint: col.URL}, audit.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctl, _ := startHost(t, ctx, r.URL(), func(c *host.Config) {
		c.Audit = al
		c.Command = []string{"sh", "-c", `stty -echo; echo PROMPT; read pw; stty echo; echo "len:${#pw}"; read l; echo "got:$l"`}
	})
	c, err := dial(t, ctx, ctl, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	waitFor(t, c, "PROMPT")
	_ = c.Write([]byte("hunter2-secret\r"))
	waitFor(t, c, "len:14")
	_ = c.Write([]byte("visible\r"))
	waitFor(t, c, "got:visible")
	if err := al.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, rec := range col.Events("setu.command") {
		got = append(got, audittest.Body(rec))
	}
	if strings.Join(got, "|") != "[REDACTED: typed with terminal echo off]|visible" {
		t.Fatalf("commands %q", got)
	}
}

func TestOnceAndWrongSecret(t *testing.T) {
	r := relaytest.New()
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ctl, _ := startHost(t, ctx, r.URL(), func(c *host.Config) { c.Once = true })

	c, err := dial(t, ctx, ctl, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	short, cancelShort := context.WithTimeout(ctx, 8*time.Second)
	defer cancelShort()
	if _, err := client.Dial(short, ctl, client.Options{Timeout: 8 * time.Second, Logf: t.Logf}); err == nil {
		t.Fatal("second join succeeded despite --once")
	}

	bogus, _ := secure.NewSecret()
	short2, cancel2 := context.WithTimeout(ctx, 5*time.Second)
	defer cancel2()
	if _, err := client.Dial(short2, link.Build("", link.Invite{Secret: bogus.String(), Relays: []string{r.URL()}}), client.Options{Timeout: 5 * time.Second, Logf: t.Logf}); err == nil {
		t.Fatal("join with an unknown secret succeeded")
	}
}

func TestManual(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	offers := make(chan string, 1)
	answers := make(chan string, 1)
	s, err := host.New(host.Config{
		Command:    echoCommand(),
		Manual:     true,
		ICE:        []proto.ICEServer{},
		Log:        newTlog(t),
		OnInvite:   func(inv host.Invite) { offers <- inv.Code },
		ReadAnswer: func(ctx context.Context) (string, error) { return <-answers, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ctx, s)
	code := <-offers
	c, err := client.Dial(ctx, code, client.Options{Timeout: 30 * time.Second, Logf: t.Logf, ShowAnswer: func(a string) { answers <- a }})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	waitFor(t, c, "READY")
	_ = c.Write([]byte("manual\r"))
	waitFor(t, c, "got:manual")
}
