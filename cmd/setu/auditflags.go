package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/dmdhrumilmistry/setu/internal/audit"
	"github.com/dmdhrumilmistry/setu/internal/tty"
)

// repeatFlag collects every occurrence of a flag verbatim (unlike listFlag
// it does not split on commas, so it can hold regular expressions).
type repeatFlag []string

func (r *repeatFlag) String() string     { return strings.Join(*r, " ") }
func (r *repeatFlag) Set(v string) error { *r = append(*r, v); return nil }

// auditFlags are the audit options shared by `setu share` (prefixed
// "audit-") and `setu service audit` (unprefixed).
type auditFlags struct {
	endpoint, ca, cert, key, input *string
	insecure, required             *bool
	redact                         repeatFlag
}

func addAuditFlags(fs *flag.FlagSet, prefix string, requiredDefault bool) *auditFlags {
	a := &auditFlags{
		endpoint: fs.String(prefix+"endpoint", "", "OTLP/HTTP logs URL of your collector, e.g. https://otel.example.com:4318/v1/logs (default: OTEL_EXPORTER_OTLP_LOGS_ENDPOINT / OTEL_EXPORTER_OTLP_ENDPOINT)"),
		ca:       fs.String(prefix+"ca", "", "PEM CA bundle that signs the collector's certificate"),
		cert:     fs.String(prefix+"cert", "", "PEM client certificate for mutual TLS"),
		key:      fs.String(prefix+"key", "", "PEM client key for mutual TLS"),
		input:    fs.String(prefix+"input", audit.InputLines, "what to record from keystrokes: lines (reconstructed commands) or off (connections only)"),
		insecure: fs.Bool(prefix+"insecure", false, "allow plain http:// to a collector that is not on this machine"),
		required: fs.Bool(prefix+"required", requiredDefault, "refuse to share unless the collector accepts a test record at startup"),
	}
	fs.Var(&a.redact, prefix+"redact", "extra regular expression to redact from recorded commands (repeatable)")
	return a
}

// config builds the audit configuration. Headers (credentials) are taken
// only from OTEL_EXPORTER_OTLP_HEADERS / OTEL_EXPORTER_OTLP_LOGS_HEADERS,
// never from flags, so they do not show up in the process list.
func (a *auditFlags) config() (audit.Config, error) {
	c := audit.Config{
		Endpoint: *a.endpoint,
		CAFile:   *a.ca,
		CertFile: *a.cert,
		KeyFile:  *a.key,
		Insecure: *a.insecure,
		Input:    *a.input,
		Redact:   a.redact,
		Required: *a.required,
	}
	if err := c.FromEnv(); err != nil {
		return c, err
	}
	if !c.Enabled() {
		return c, errors.New("auditing needs a collector: pass the endpoint flag or set OTEL_EXPORTER_OTLP_LOGS_ENDPOINT")
	}
	return c, c.Validate()
}

// readHeadersStdin reads "Name=value,Name2=value2" from one line of stdin,
// without echo when stdin is a terminal.
func readHeadersStdin() (map[string]string, error) {
	var line string
	if tty.IsTerminal(os.Stdin) {
		fmt.Fprint(os.Stderr, "Collector headers (e.g. Authorization=Bearer%20xyz): ")
		l, err := tty.ReadPassword(os.Stdin)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return nil, err
		}
		line = l
	} else {
		l, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && l == "" {
			return nil, errors.New("no headers on stdin")
		}
		line = l
	}
	return audit.ParseHeaders(strings.TrimSpace(line))
}

// collectorHost names the collector without path or query, for messages.
func collectorHost(endpoint string) string {
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	return endpoint
}
