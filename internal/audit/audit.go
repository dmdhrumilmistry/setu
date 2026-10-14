// Package audit records who did what in a shared terminal and ships it, as
// OpenTelemetry log records over OTLP/HTTP, to a collector you run.
//
// What is recorded:
//
//   - session lifecycle: start (command, access settings), end (exit code)
//   - client lifecycle: connected, disconnected, rejected, failed password,
//     approved/denied, with the client's verification code, DTLS
//     fingerprint and network address
//   - commands: each line a person submits, reconstructed from their
//     keystrokes and attributed to the client (or the host terminal) that
//     typed it, plus ^C / ^D / ^Z
//
// Command lines are best effort: setu sees keystrokes, not what the shell
// finally ran, so lines involving cursor keys, history or completion are
// flagged "edited". Lines typed while the terminal has echo off (password
// prompts, Unix only) are never recorded, and known secret formats are
// redacted before a record leaves the process.
//
// Export never blocks the terminal: records go through a bounded batch queue
// (oldest dropped when full) and are flushed every second, so a session
// participant cannot erase what was already exported by tampering with the
// host afterwards.
package audit

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/user"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/secure"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
)

// Input modes.
const (
	InputLines = "lines" // record reconstructed command lines (default)
	InputOff   = "off"   // lifecycle events only
)

// Config selects and secures the OTLP/HTTP collector.
type Config struct {
	// Endpoint is the OTLP/HTTP logs URL, e.g.
	// https://otel.example.com:4318/v1/logs. A bare origin gets /v1/logs.
	Endpoint string `json:"endpoint"`
	// Headers are sent with every export, typically Authorization. Never
	// put them on the command line: use OTEL_EXPORTER_OTLP_HEADERS or the
	// service config file.
	Headers map[string]string `json:"headers,omitempty"`
	// CAFile pins the collector's CA; CertFile/KeyFile enable mutual TLS.
	CAFile   string `json:"ca_file,omitempty"`
	CertFile string `json:"cert_file,omitempty"`
	KeyFile  string `json:"key_file,omitempty"`
	// Insecure allows plain http:// to a non-loopback collector.
	Insecure bool `json:"insecure,omitempty"`
	// Input is InputLines (default) or InputOff.
	Input string `json:"input,omitempty"`
	// Redact holds extra regular expressions whose matches are replaced.
	Redact []string `json:"redact,omitempty"`
	// Required makes setu refuse to share unless the collector accepts a
	// probe record at startup (fail closed).
	Required bool `json:"required,omitempty"`
}

// Enabled reports whether auditing is configured.
func (c Config) Enabled() bool { return c.Endpoint != "" }

// Standard OpenTelemetry environment variables honoured by FromEnv.
var (
	endpointEnv = []string{"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "OTEL_EXPORTER_OTLP_ENDPOINT"}
	headersEnv  = []string{"OTEL_EXPORTER_OTLP_HEADERS", "OTEL_EXPORTER_OTLP_LOGS_HEADERS"} // later wins
)

// FromEnv fills an empty Endpoint and merges headers from the standard
// OTEL_EXPORTER_OTLP_* variables. Headers already in c take precedence.
func (c *Config) FromEnv() error {
	if c.Endpoint == "" {
		if v := os.Getenv(endpointEnv[0]); v != "" {
			c.Endpoint = v
		} else if v := os.Getenv(endpointEnv[1]); v != "" {
			c.Endpoint = strings.TrimRight(v, "/") + "/v1/logs"
		}
	}
	merged := map[string]string{}
	for _, k := range headersEnv {
		h, err := ParseHeaders(os.Getenv(k))
		if err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
		for hk, hv := range h {
			merged[hk] = hv
		}
	}
	for hk, hv := range c.Headers {
		merged[hk] = hv
	}
	if len(merged) > 0 {
		c.Headers = merged
	}
	return nil
}

// ScrubEnv removes exporter credentials from this process's environment so
// they are not inherited by the shared command.
func ScrubEnv() {
	for _, k := range headersEnv {
		os.Unsetenv(k)
	}
}

// ParseHeaders parses the "k=v,k2=v2" OTLP header format (values may be
// percent-encoded).
func ParseHeaders(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, errors.New("malformed header list (want k=v,k2=v2)")
		}
		dv, err := url.QueryUnescape(strings.TrimSpace(v))
		if err != nil {
			return nil, fmt.Errorf("header %q: %w", k, err)
		}
		out[k] = dv
	}
	return out, nil
}

// Validate checks the configuration without contacting the collector.
func (c Config) Validate() error {
	if !c.Enabled() {
		return nil
	}
	if _, err := c.endpointURL(); err != nil {
		return err
	}
	switch c.Input {
	case "", InputLines, InputOff:
	default:
		return fmt.Errorf("audit input mode %q: want %q or %q", c.Input, InputLines, InputOff)
	}
	if (c.CertFile == "") != (c.KeyFile == "") {
		return errors.New("audit mTLS needs both a client certificate and a key")
	}
	for k, v := range c.Headers {
		if strings.ContainsAny(k+v, "\r\n") {
			return fmt.Errorf("audit header %q contains a line break", k)
		}
	}
	_, err := newRedactor(c.Redact)
	return err
}

func (c Config) endpointURL() (*url.URL, error) {
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("audit endpoint: %w", err)
	}
	if u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("audit endpoint %q: want an http(s):// URL", c.Endpoint)
	}
	if u.User != nil {
		return nil, errors.New("audit endpoint must not embed credentials; send them as headers (OTEL_EXPORTER_OTLP_HEADERS)")
	}
	if u.Scheme == "http" && !c.Insecure && !isLoopback(u.Hostname()) {
		return nil, fmt.Errorf("audit endpoint %q is plain http: use https, or allow it explicitly with --audit-insecure", c.Endpoint)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/v1/logs"
	}
	return u, nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c Config) tlsConfig() (*tls.Config, error) {
	t := &tls.Config{MinVersion: tls.VersionTLS12}
	if c.CAFile != "" {
		pem, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, fmt.Errorf("audit CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("audit CA %s: no PEM certificates", c.CAFile)
		}
		t.RootCAs = pool
	}
	if c.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("audit client certificate: %w", err)
		}
		t.Certificates = []tls.Certificate{cert}
	}
	return t, nil
}

// Options are the process-level details attached to every record.
type Options struct {
	Version string
	// Logf reports export problems (rate limited). Defaults to stderr.
	Logf func(format string, args ...any)

	exporter sdklog.Exporter // tests
}

// Logger exports audit records. A nil *Logger is valid and records nothing.
type Logger struct {
	provider *sdklog.LoggerProvider
	log      otellog.Logger
	input    bool
	redact   *redactor
}

const (
	queueSize      = 8192
	exportInterval = time.Second
	exportTimeout  = 10 * time.Second
)

// New builds a Logger for cfg, or returns nil if auditing is disabled. With
// cfg.Required it also proves the collector accepts records.
func New(ctx context.Context, cfg Config, opt Options) (*Logger, error) {
	if !cfg.Enabled() {
		return nil, nil
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	red, err := newRedactor(cfg.Redact)
	if err != nil {
		return nil, err
	}
	if opt.Logf == nil {
		opt.Logf = func(f string, a ...any) { fmt.Fprintf(os.Stderr, "[setu] "+f+"\n", a...) }
	}
	exp := opt.exporter
	if exp == nil {
		u, _ := cfg.endpointURL()
		opts := []otlploghttp.Option{
			otlploghttp.WithEndpointURL(u.String()),
			otlploghttp.WithHeaders(cfg.Headers),
			otlploghttp.WithCompression(otlploghttp.GzipCompression),
			otlploghttp.WithTimeout(exportTimeout),
		}
		if u.Scheme == "https" {
			tlsCfg, err := cfg.tlsConfig()
			if err != nil {
				return nil, err
			}
			opts = append(opts, otlploghttp.WithTLSClientConfig(tlsCfg))
		} else if cfg.CAFile != "" || cfg.CertFile != "" {
			return nil, errors.New("audit TLS options need an https:// endpoint")
		}
		exp, err = otlploghttp.New(ctx, opts...)
		if err != nil {
			return nil, fmt.Errorf("audit exporter: %w", err)
		}
	}
	setErrorHandler(opt.Logf)

	provider := sdklog.NewLoggerProvider(
		sdklog.WithResource(processResource(opt.Version)),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exp,
			sdklog.WithMaxQueueSize(queueSize),
			sdklog.WithExportInterval(exportInterval),
			sdklog.WithExportTimeout(exportTimeout),
		)),
		sdklog.WithAttributeValueLengthLimit(2*maxLineBytes),
	)
	l := &Logger{
		provider: provider,
		log:      provider.Logger("github.com/dmdhrumilmistry/setu/audit", otellog.WithInstrumentationVersion(opt.Version)),
		input:    cfg.Input != InputOff,
		redact:   red,
	}
	if cfg.Required {
		if err := l.probe(ctx); err != nil {
			_ = l.Shutdown(context.Background())
			return nil, fmt.Errorf("audit collector rejected the startup probe (required): %w", err)
		}
	}
	return l, nil
}

// probe emits a record and waits until the exporter has delivered it.
func (l *Logger) probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	l.emit(otellog.SeverityInfo, "setu.audit.start", "audit pipeline started")
	return l.provider.ForceFlush(ctx)
}

// Shutdown flushes pending records. Call it before the process exits.
func (l *Logger) Shutdown(ctx context.Context) error {
	if l == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return l.provider.Shutdown(ctx)
}

func (l *Logger) emit(sev otellog.Severity, name, body string, attrs ...attribute.KeyValue) {
	var r otellog.Record
	now := time.Now()
	r.SetTimestamp(now)
	r.SetObservedTimestamp(now)
	r.SetEventName(name)
	r.SetSeverity(sev)
	r.SetSeverityText(sev.String())
	r.SetBody(attribute.StringValue(body))
	r.AddAttributes(attrs...)
	l.log.Emit(context.Background(), r)
}

var (
	handlerOnce sync.Once
	handlerLogf atomic.Pointer[func(string, ...any)]
	lastErrAt   atomic.Int64
)

// setErrorHandler routes OpenTelemetry export errors to the setu log, at
// most once a minute, so a dead collector is visible but not noisy.
func setErrorHandler(logf func(string, ...any)) {
	handlerLogf.Store(&logf)
	handlerOnce.Do(func() {
		otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
			now := time.Now().Unix()
			last := lastErrAt.Load()
			if now-last < 60 || !lastErrAt.CompareAndSwap(last, now) {
				return
			}
			if f := handlerLogf.Load(); f != nil {
				(*f)("WARNING: audit export failing: %v", err)
			}
		}))
	})
}

func processResource(version string) *resource.Resource {
	host, _ := os.Hostname()
	attrs := []attribute.KeyValue{
		attribute.String("service.name", "setu"),
		attribute.String("service.version", version),
		attribute.String("service.instance.id", secure.RandomID(8)),
		attribute.String("host.name", host),
		attribute.String("os.type", runtime.GOOS),
		attribute.Int("process.pid", os.Getpid()),
	}
	if u, err := user.Current(); err == nil {
		attrs = append(attrs, attribute.String("user.name", u.Username))
	}
	return resource.NewSchemaless(attrs...)
}

// SessionInfo describes one shared session.
type SessionInfo struct {
	ID         string
	Command    string
	Mode       string // "nostr" or "manual"
	Profile    string // service profile name, if any
	Password   bool
	ViewLink   bool
	MaxClients int
}

// Session records events for one shared session. A nil *Session is valid.
type Session struct {
	l     *Logger
	attrs []attribute.KeyValue
}

// Session starts recording a session.
func (l *Logger) Session(info SessionInfo) *Session {
	if l == nil {
		return nil
	}
	attrs := []attribute.KeyValue{attribute.String("setu.session.id", info.ID)}
	if info.Profile != "" {
		attrs = append(attrs, attribute.String("setu.profile", info.Profile))
	}
	s := &Session{l: l, attrs: attrs}
	s.emit(otellog.SeverityInfo, "setu.session.start", "session started: "+info.Command,
		attribute.String("setu.session.command", info.Command),
		attribute.String("setu.session.mode", info.Mode),
		attribute.Bool("setu.session.password", info.Password),
		attribute.Bool("setu.session.view_link", info.ViewLink),
		attribute.Int("setu.session.max_clients", info.MaxClients),
	)
	return s
}

func (s *Session) emit(sev otellog.Severity, name, body string, attrs ...attribute.KeyValue) {
	s.l.emit(sev, name, body, append(append([]attribute.KeyValue(nil), s.attrs...), attrs...)...)
}

// Client identifies who is connected (or, with Via "local", the host's own
// terminal).
type Client struct {
	ID            int
	Role          string
	Via           string // "nostr", "manual" or "local"
	SAS           string
	Fingerprint   string // remote DTLS certificate fingerprint
	Address       string
	Port          int
	CandidateType string // host, srflx, prflx or relay
}

func (c Client) attrs() []attribute.KeyValue {
	a := []attribute.KeyValue{
		attribute.Int("setu.client.id", c.ID),
		attribute.String("setu.client.role", c.Role),
		attribute.String("setu.client.via", c.Via),
	}
	if c.SAS != "" {
		a = append(a, attribute.String("setu.client.verification_code", c.SAS))
	}
	if c.Fingerprint != "" {
		a = append(a, attribute.String("setu.client.dtls_fingerprint", c.Fingerprint))
	}
	if c.Address != "" {
		a = append(a, attribute.String("client.address", c.Address), attribute.Int("client.port", c.Port))
	}
	if c.CandidateType != "" {
		a = append(a, attribute.String("setu.client.candidate_type", c.CandidateType))
	}
	return a
}

// End records the end of the session.
func (s *Session) End(code int, reason string) {
	if s == nil {
		return
	}
	s.emit(otellog.SeverityInfo, "setu.session.end", "session ended: "+reason,
		attribute.Int("process.exit.code", code), attribute.String("setu.reason", reason))
}

// ClientConnected records a client that passed every check and is streaming.
func (s *Session) ClientConnected(c Client) {
	if s == nil {
		return
	}
	s.emit(otellog.SeverityInfo, "setu.client.connect",
		fmt.Sprintf("client #%d connected (role=%s)", c.ID, c.Role), c.attrs()...)
}

// ClientDisconnected records a connected client leaving.
func (s *Session) ClientDisconnected(c Client, reason string) {
	if s == nil {
		return
	}
	s.emit(otellog.SeverityInfo, "setu.client.disconnect",
		fmt.Sprintf("client #%d disconnected: %s", c.ID, reason),
		append(c.attrs(), attribute.String("setu.reason", reason))...)
}

// ClientRejected records a join request refused before a connection existed.
func (s *Session) ClientRejected(role, reason string) {
	if s == nil {
		return
	}
	s.emit(otellog.SeverityWarn, "setu.client.rejected", "join request rejected: "+reason,
		attribute.String("setu.client.role", role), attribute.String("setu.reason", reason))
}

// AuthFailed records a wrong password.
func (s *Session) AuthFailed(c Client) {
	if s == nil {
		return
	}
	s.emit(otellog.SeverityWarn, "setu.client.auth_failure",
		fmt.Sprintf("client #%d failed password authentication", c.ID), c.attrs()...)
}

// Approval records the host's answer to an --approve prompt.
func (s *Session) Approval(c Client, allowed bool) {
	if s == nil {
		return
	}
	verdict := "denied"
	if allowed {
		verdict = "approved"
	}
	s.emit(otellog.SeverityInfo, "setu.client.approval",
		fmt.Sprintf("client #%d %s by host", c.ID, verdict),
		append(c.attrs(), attribute.Bool("setu.approved", allowed))...)
}

// InviteDisabled records the invite no longer accepting clients.
func (s *Session) InviteDisabled(reason string) {
	if s == nil {
		return
	}
	s.emit(otellog.SeverityInfo, "setu.invite.disabled", "invite disabled: "+reason, attribute.String("setu.reason", reason))
}

// Input returns a recorder for the keystrokes of one client. sensitive, if
// not nil, reports whether the terminal is currently reading a password.
// It returns nil when input auditing is off.
func (s *Session) Input(c Client, sensitive func() bool) *Input {
	if s == nil || !s.l.input {
		return nil
	}
	return &Input{s: s, attrs: slices.Clip(c.attrs()), sensitive: sensitive}
}

// Input turns one client's keystrokes into command records. A nil *Input is
// valid and records nothing.
type Input struct {
	s         *Session
	attrs     []attribute.KeyValue
	sensitive func() bool

	mu sync.Mutex
	ed lineEditor
}

// Write feeds keystrokes exactly as they are written to the terminal.
func (in *Input) Write(b []byte) {
	if in == nil {
		return
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.sensitive != nil && in.sensitive() {
		in.ed.secret = true
	}
	in.ed.feed(b, in.record)
}

// Close records any typed but unsubmitted text.
func (in *Input) Close() {
	if in == nil {
		return
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	in.ed.flush(false, in.record)
}

func (in *Input) record(e event) {
	if e.control != "" {
		in.s.emit(otellog.SeverityInfo, "setu.input.control", e.control,
			append(in.attrs, attribute.String("setu.input.key", e.control))...)
		return
	}
	text, wasRedacted := e.text, false
	if e.secret {
		text, wasRedacted = "[REDACTED: typed with terminal echo off]", true
	} else {
		text, wasRedacted = in.s.l.redact.apply(text)
	}
	in.s.emit(otellog.SeverityInfo, "setu.command", text,
		append(in.attrs,
			attribute.Bool("setu.input.submitted", e.submitted),
			attribute.Bool("setu.input.edited", e.edited),
			attribute.Bool("setu.input.truncated", e.truncated),
			attribute.Bool("setu.input.redacted", wasRedacted),
		)...)
}
