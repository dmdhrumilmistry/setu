package audit

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/audit/audittest"
)

func TestEndToEndExport(t *testing.T) {
	col := audittest.New(t)
	ctx := context.Background()
	l, err := New(ctx, Config{
		Endpoint: col.URL, // plain http is allowed for loopback
		Headers:  map[string]string{"Authorization": "Bearer test-token"},
		Required: true,
	}, Options{Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	s := l.Session(SessionInfo{ID: "abc", Command: "bash", Mode: "nostr", MaxClients: 2})
	c := Client{ID: 1, Role: "control", Via: "nostr", SAS: "123 456", Address: "203.0.113.7", Port: 5000}
	s.ClientConnected(c)

	pw := false
	in := s.Input(c, func() bool { return pw })
	in.Write([]byte("export GITHUB_TOKEN=ghp_abc\r"))
	in.Write([]byte("sudo ls\r"))
	pw = true
	in.Write([]byte("hunter2\r"))
	pw = false
	in.Write([]byte("\x03"))
	in.Write([]byte("half"))
	in.Close()
	s.ClientDisconnected(c, "channel closed")
	s.End(0, "command exited")
	if err := l.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	if len(col.Events("setu.audit.start")) != 1 {
		t.Fatal("missing startup probe record")
	}
	cmds := col.Events("setu.command")
	var bodies []string
	for _, r := range cmds {
		bodies = append(bodies, audittest.Body(r))
		if audittest.Attr(r, "setu.session.id") != "abc" || audittest.Attr(r, "setu.client.id") != "1" ||
			audittest.Attr(r, "client.address") != "203.0.113.7" {
			t.Errorf("command record missing attribution: %v", r.Attributes)
		}
	}
	want := []string{
		"export GITHUB_TOKEN=[REDACTED]",
		"sudo ls",
		"[REDACTED: typed with terminal echo off]",
		"half",
	}
	if strings.Join(bodies, "|") != strings.Join(want, "|") {
		t.Fatalf("commands = %q, want %q", bodies, want)
	}
	if audittest.Attr(cmds[0], "setu.input.redacted") != "true" || audittest.Attr(cmds[1], "setu.input.redacted") != "false" {
		t.Error("redacted flag not set correctly")
	}
	if audittest.Attr(cmds[3], "setu.input.submitted") != "false" {
		t.Error("unsubmitted remainder should be flagged")
	}
	for _, ev := range []string{"setu.session.start", "setu.client.connect", "setu.input.control", "setu.client.disconnect", "setu.session.end"} {
		if n := len(col.Events(ev)); n != 1 {
			t.Errorf("want one %s record, got %d", ev, n)
		}
	}
	for _, h := range col.Headers() {
		if h.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("Authorization header %q", h.Get("Authorization"))
		}
	}
}

func TestRequiredFailsClosed(t *testing.T) {
	col := audittest.New(t)
	col.Fail()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := New(ctx, Config{Endpoint: col.URL, Required: true}, Options{}); err == nil {
		t.Fatal("expected New to fail when the collector rejects the probe")
	}
}

func TestInputOff(t *testing.T) {
	col := audittest.New(t)
	l, err := New(context.Background(), Config{Endpoint: col.URL, Input: InputOff}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Shutdown(context.Background())
	if in := l.Session(SessionInfo{ID: "x"}).Input(Client{ID: 1}, nil); in != nil {
		t.Fatal("input recorder should be nil when input auditing is off")
	}
}

func TestNilIsNoop(t *testing.T) {
	var l *Logger
	s := l.Session(SessionInfo{})
	s.ClientConnected(Client{})
	s.Input(Client{}, nil).Write([]byte("x\r"))
	s.End(0, "")
	if err := l.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestValidate(t *testing.T) {
	bad := []Config{
		{Endpoint: "http://collector.example.com:4318"},            // plain http off-box
		{Endpoint: "https://user:pw@collector.example.com"},        // credentials in URL
		{Endpoint: "ftp://collector.example.com"},                  // scheme
		{Endpoint: "https://c.example.com", CertFile: "a.pem"},     // cert without key
		{Endpoint: "https://c.example.com", Input: "everything"},   // input mode
		{Endpoint: "https://c.example.com", Redact: []string{"("}}, // regex
		{Endpoint: "https://c.example.com", Headers: map[string]string{"X": "a\r\nb"}},
	}
	for _, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("Validate(%+v) = nil, want an error", c)
		}
	}
	good := []Config{
		{},
		{Endpoint: "https://collector.example.com"},
		{Endpoint: "http://127.0.0.1:4318/v1/logs"},
		{Endpoint: "http://localhost:4318"},
		{Endpoint: "http://collector.internal:4318", Insecure: true},
	}
	for _, c := range good {
		if err := c.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v", c, err)
		}
	}
	u, _ := Config{Endpoint: "https://collector.example.com"}.endpointURL()
	if u.Path != "/v1/logs" {
		t.Errorf("default path %q", u.Path)
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://otel.example.com/")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "Authorization=Bearer%20abc,X-Tenant=team1")
	c := Config{Headers: map[string]string{"X-Tenant": "override"}}
	if err := c.FromEnv(); err != nil {
		t.Fatal(err)
	}
	if c.Endpoint != "https://otel.example.com/v1/logs" {
		t.Errorf("endpoint %q", c.Endpoint)
	}
	if c.Headers["Authorization"] != "Bearer abc" || c.Headers["X-Tenant"] != "override" {
		t.Errorf("headers %v", c.Headers)
	}
	ScrubEnv()
	if v, ok := os.LookupEnv("OTEL_EXPORTER_OTLP_HEADERS"); ok && v != "" {
		t.Error("ScrubEnv left the headers in the environment")
	}
}
