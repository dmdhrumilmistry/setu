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
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/proto"
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
	// Attributes are added to the resource of every record, e.g. the
	// device serial number or asset tag from a managed policy.
	Attributes map[string]string
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
	user     string // OS account that shared commands run as
	host     string
	hostID   string // stable machine id (OpenTelemetry host.id)
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

	host, _ := os.Hostname()
	hostID := machineID()
	username := ""
	if u, err := user.Current(); err == nil {
		username = u.Username
	}

	provider := sdklog.NewLoggerProvider(
		sdklog.WithResource(processResource(opt.Version, host, hostID, username, opt.Attributes)),
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
		user:     username,
		host:     host,
		hostID:   hostID,
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
	l.emitAt(time.Now(), sev, name, body, attrs...)
}

// emitAt emits a record that happened at at (the record timestamp); the
// observed timestamp is when setu produced it.
func (l *Logger) emitAt(at time.Time, sev otellog.Severity, name, body string, attrs ...attribute.KeyValue) {
	var r otellog.Record
	r.SetTimestamp(at)
	r.SetObservedTimestamp(time.Now())
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

func processResource(version, host, hostID, username string, extra map[string]string) *resource.Resource {
	attrs := []attribute.KeyValue{
		attribute.String("service.name", "setu"),
		attribute.String("service.version", version),
		attribute.String("service.instance.id", secure.RandomID(8)),
		attribute.String("host.name", host),
		attribute.String("os.type", runtime.GOOS),
		attribute.Int("process.pid", os.Getpid()),
	}
	if username != "" {
		attrs = append(attrs, attribute.String("user.name", username))
	}
	if hostID != "" {
		attrs = append(attrs, attribute.String("host.id", hostID))
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		attrs = append(attrs, attribute.String(k, extra[k]))
	}
	return resource.NewSchemaless(attrs...)
}

// SessionInfo describes one shared session.
type SessionInfo struct {
	ID         string
	Command    string
	Kind       string // proto.KindTerminal or proto.KindDesktop
	Mode       string // "nostr" or "manual"
	Profile    string // service profile name, if any
	Password   bool
	ViewLink   bool
	MaxClients int
}

// Session records events for one shared session. A nil *Session is valid.
type Session struct {
	l       *Logger
	attrs   []attribute.KeyValue
	command string
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
	s := &Session{l: l, attrs: attrs, command: info.Command}
	s.emit(otellog.SeverityInfo, "setu.session.start", "session started: "+info.Command,
		attribute.String("setu.session.command", info.Command),
		attribute.String("setu.session.kind", info.Kind),
		attribute.String("setu.session.mode", info.Mode),
		attribute.Bool("setu.session.password", info.Password),
		attribute.Bool("setu.session.view_link", info.ViewLink),
		attribute.Int("setu.session.max_clients", info.MaxClients),
	)
	return s
}

func (s *Session) emit(sev otellog.Severity, name, body string, attrs ...attribute.KeyValue) {
	s.emitAt(time.Now(), sev, name, body, attrs...)
}

func (s *Session) emitAt(at time.Time, sev otellog.Severity, name, body string, attrs ...attribute.KeyValue) {
	s.l.emitAt(at, sev, name, body, append(append([]attribute.KeyValue(nil), s.attrs...), attrs...)...)
}

// Client identifies who is connected (or, with Via "local", the host's own
// terminal).
type Client struct {
	ID            int
	Role          string
	Via           string // "nostr", "manual" or "local"
	KeyName       string // the named invite key the client used, if any
	SAS           string
	Fingerprint   string // remote DTLS certificate fingerprint
	Address       string // remote end of the selected ICE path (a TURN server if relayed)
	Port          int
	CandidateType string // host, srflx, prflx or relay
	Transport     string // udp or tcp
	// PublicAddresses are the client's own public (server reflexive)
	// addresses, from its SDP: the real network origin even when the
	// selected path is a relay. LocalAddresses are its LAN addresses
	// (browsers hide these behind mDNS names, which are skipped).
	PublicAddresses []string
	LocalAddresses  []string
	// Device is what the client reported about itself (self-reported).
	Device *proto.ClientInfo
	// DeviceKey is the fingerprint of the client's device key, verified by
	// a signature over this connection (unlike Device).
	DeviceKey string
}

// ActorName is the best identity setu has for a client: the name of the
// personal key it joined with, the host's own terminal, or (for a shared
// link that several people may hold) the link it used.
func (c Client) ActorName() string {
	switch {
	case c.KeyName != "":
		return c.KeyName
	case c.Via == "local":
		return "host-terminal"
	case c.Role == "view":
		return "shared-view-link"
	}
	return "shared-control-link"
}

// Actor is a one-line, human-readable "who": name, network origin, device
// and verification code.
func (c Client) Actor() string {
	s := c.ActorName()
	if c.Via != "local" {
		s += fmt.Sprintf(" (client #%d", c.ID)
		switch {
		case c.CandidateType == "relay" && len(c.PublicAddresses) > 0:
			s += " from " + c.PublicAddresses[0] + " via TURN relay"
		case c.Address != "":
			s += fmt.Sprintf(" from %s", net.JoinHostPort(c.Address, fmt.Sprint(c.Port)))
		}
		if d := c.Device.Summary(); d != "" {
			s += ", device " + d
		}
		if c.DeviceKey != "" {
			s += ", device key " + c.DeviceKey
		}
		if c.SAS != "" {
			s += ", verification code " + c.SAS
		}
		s += ")"
	}
	return s
}

func (c Client) attrs() []attribute.KeyValue {
	a := []attribute.KeyValue{
		attribute.String("setu.actor", c.Actor()),
		attribute.String("setu.actor.name", c.ActorName()),
		attribute.Int("setu.client.id", c.ID),
		attribute.String("setu.client.role", c.Role),
		attribute.String("setu.client.via", c.Via),
	}
	if c.KeyName != "" {
		a = append(a, attribute.String("setu.actor.key", c.KeyName))
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
	if c.Transport != "" {
		a = append(a, attribute.String("network.transport", c.Transport))
	}
	if c.DeviceKey != "" {
		a = append(a, attribute.String("setu.client.device_key", c.DeviceKey))
	}
	if len(c.PublicAddresses) > 0 {
		a = append(a, attribute.StringSlice("setu.client.public_addresses", c.PublicAddresses))
	}
	if len(c.LocalAddresses) > 0 {
		a = append(a, attribute.StringSlice("setu.client.local_addresses", c.LocalAddresses))
	}
	return append(a, deviceAttrs(c.Device)...)
}

// deviceAttrs records the client's self-reported device details. They help
// an investigator, but a client can send anything, so they are flagged.
func deviceAttrs(d *proto.ClientInfo) []attribute.KeyValue {
	if d == nil {
		return nil
	}
	a := []attribute.KeyValue{
		attribute.Bool("setu.client.device.self_reported", true),
		attribute.Bool("setu.client.device.mobile", d.Mobile),
	}
	for _, f := range []struct{ k, v string }{
		{"setu.client.device.summary", d.Summary()},
		{"setu.client.device.app", d.App},
		{"setu.client.device.version", d.Version},
		{"setu.client.device.id", d.DeviceID},
		{"setu.client.device.os", d.OS},
		{"setu.client.device.os_version", d.OSVersion},
		{"setu.client.device.arch", d.Arch},
		{"setu.client.device.model", d.Model},
		{"setu.client.device.hostname", d.Hostname},
		{"setu.client.device.user", d.User},
		{"setu.client.device.language", d.Language},
		{"setu.client.device.timezone", d.Timezone},
		{"setu.client.device.screen", d.Screen},
		{"user_agent.original", d.UserAgent},
	} {
		if f.v != "" {
			a = append(a, attribute.String(f.k, f.v))
		}
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
func (s *Session) ClientRejected(c Client, reason string) {
	if s == nil {
		return
	}
	s.emit(otellog.SeverityWarn, "setu.client.rejected", "join request from "+c.ActorName()+" rejected: "+reason,
		attribute.String("setu.actor.name", c.ActorName()),
		attribute.String("setu.client.role", c.Role),
		attribute.String("setu.reason", reason))
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
func (s *Session) Approval(c Client, allowed bool, method string) {
	if s == nil {
		return
	}
	verdict := "denied"
	if allowed {
		verdict = "approved"
	}
	s.emit(otellog.SeverityInfo, "setu.client.approval",
		fmt.Sprintf("client #%d %s (%s)", c.ID, verdict, method),
		append(c.attrs(), attribute.Bool("setu.approved", allowed), attribute.String("setu.approval.method", method))...)
}

// How an approval decision was made (setu.approval.method).
const (
	ApprovedTrustedDevice = "trusted-device" // known device key, no prompt
	ApprovedAlways        = "prompt-always"  // host answered "always": device now trusted
	ApprovedOnce          = "prompt-once"    // host answered "yes"
	DeniedByHost          = "prompt-denied"  // host answered "no" or did not answer
	DeniedUnknownDevice   = "unknown-device" // no prompt available, device not trusted
)

// Clipboard directions (setu.clipboard.direction).
const (
	ClipboardToHost   = "to_host"
	ClipboardToClient = "to_client"
)

// Clipboard records clipboard text crossing between a client and the host
// during a desktop share. Only its size is recorded, never the text, which
// often holds passwords and tokens.
func (s *Session) Clipboard(c Client, direction string, size int) {
	if s == nil {
		return
	}
	body := fmt.Sprintf("clipboard text (%d bytes) from client #%d to the host", size, c.ID)
	if direction == ClipboardToClient {
		body = fmt.Sprintf("host clipboard text (%d bytes) to client #%d", size, c.ID)
	}
	s.emit(otellog.SeverityInfo, "setu.desktop.clipboard", body,
		append(c.attrs(), attribute.String("setu.clipboard.direction", direction), attribute.Int("setu.clipboard.bytes", size))...)
}

// DisplaySelected records a client switching the shared display.
func (s *Session) DisplaySelected(c Client, display string) {
	if s == nil {
		return
	}
	s.emit(otellog.SeverityInfo, "setu.desktop.display", fmt.Sprintf("client #%d switched to display %s", c.ID, display),
		append(c.attrs(), attribute.String("setu.desktop.display", display))...)
}

// DesktopInput summarises a control client's remote input when it leaves.
// Keystrokes on a desktop go to arbitrary applications, so only counts are
// recorded.
func (s *Session) DesktopInput(c Client, clicks, keys, scrolls, typedChars int) {
	if s == nil {
		return
	}
	s.emit(otellog.SeverityInfo, "setu.desktop.input",
		fmt.Sprintf("client #%d input: %d clicks, %d key presses, %d scrolls, %d typed characters", c.ID, clicks, keys, scrolls, typedChars),
		append(c.attrs(),
			attribute.Int("setu.desktop.clicks", clicks),
			attribute.Int("setu.desktop.key_presses", keys),
			attribute.Int("setu.desktop.scrolls", scrolls),
			attribute.Int("setu.desktop.typed_chars", typedChars),
		)...)
}

// FileUpload records a file a client uploaded to the host, with its SHA-256
// so the exact content can be identified later.
func (s *Session) FileUpload(c Client, name, path string, size int64, sha256 string) {
	if s == nil {
		return
	}
	s.emit(otellog.SeverityInfo, "setu.file.upload", fmt.Sprintf("client #%d uploaded %s (%d bytes)", c.ID, name, size),
		append(c.attrs(),
			attribute.String("file.name", name),
			attribute.String("file.path", path),
			attribute.Int64("file.size", size),
			attribute.String("file.hash.sha256", sha256),
		)...)
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
	in.ed.feed(b, time.Now(), in.record)
}

// Close records any typed but unsubmitted text.
func (in *Input) Close() {
	if in == nil {
		return
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	in.ed.now = time.Now() // an unsubmitted remainder is recorded as of now
	in.ed.flush(false, in.record)
}

// record emits one command (or control key) as a self-contained record:
// who typed it (setu.actor*, client.*, and the account it ran as), when
// (the record timestamp plus explicit started/submitted times) and what
// (the body and setu.command.text, plus the program it was typed into).
func (in *Input) record(e event) {
	at := e.at
	if at.IsZero() {
		at = time.Now()
	}
	who := append(slices.Clip(in.attrs),
		attribute.String("user.name", in.s.l.user),
		attribute.String("host.name", in.s.l.host),
		attribute.String("host.id", in.s.l.hostID),
	)
	if e.control != "" {
		in.s.emitAt(at, otellog.SeverityInfo, "setu.input.control", e.control,
			append(who,
				attribute.String("setu.input.key", e.control),
				attribute.String("setu.command.submitted_at", at.UTC().Format(time.RFC3339Nano)),
				attribute.String("setu.session.command", in.s.command),
			)...)
		return
	}
	text, wasRedacted := e.text, false
	if e.secret {
		text, wasRedacted = "[REDACTED: typed with terminal echo off]", true
	} else {
		text, wasRedacted = in.s.l.redact.apply(text)
	}
	started := e.started
	if started.IsZero() {
		started = at
	}
	in.s.emitAt(at, otellog.SeverityInfo, "setu.command", text,
		append(who,
			attribute.String("setu.command.text", text),
			attribute.String("setu.command.submitted_at", at.UTC().Format(time.RFC3339Nano)),
			attribute.String("setu.command.started_at", started.UTC().Format(time.RFC3339Nano)),
			attribute.String("setu.session.command", in.s.command),
			attribute.Bool("setu.input.submitted", e.submitted),
			attribute.Bool("setu.input.edited", e.edited),
			attribute.Bool("setu.input.truncated", e.truncated),
			attribute.Bool("setu.input.redacted", wasRedacted),
		)...)
}
