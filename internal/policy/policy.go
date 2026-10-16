// Package policy reads the managed policy an organisation deploys to a
// device through its MDM (Intune, Jamf, Kandji, VaanarSena, Group Policy,
// configuration management, ...) and checks setu's settings against it.
//
// Sources, first found wins (all are writable only by administrators):
//
//	Windows  registry  HKLM\SOFTWARE\Policies\setu
//	         file      %ProgramData%\setu\policy.json
//	macOS    profile   /Library/Managed Preferences/io.github.dmdhrumilmistry.setu.plist
//	         file      /Library/Application Support/setu/policy.json
//	Linux    file      /etc/setu/policy.json
//
// The policy fails closed: if a source exists but cannot be read, is
// malformed, uses an unknown key, or could be changed by a non-administrator,
// Load returns an error and setu refuses to run.
//
// A policy is a guardrail for the setu binary, not a sandbox: a local
// administrator can remove it, and a user can run a different program. Pair
// it with egress controls if that matters.
package policy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/audit"
)

// Domain is the macOS preference domain (PayloadType of the managed
// preferences payload) and the registry key name.
const Domain = "io.github.dmdhrumilmistry.setu"

// Policy is the managed policy. Every field is optional; the zero value
// allows everything (the same as having no policy).
type Policy struct {
	// Feature switches.
	DisableShare   bool `json:"DisableShare,omitempty"`   // forbid ad-hoc `setu share`
	DisableService bool `json:"DisableService,omitempty"` // forbid `setu service`
	DisableManual  bool `json:"DisableManual,omitempty"`  // forbid --manual (copy/paste signaling)
	DisableDesktop bool `json:"DisableDesktop,omitempty"` // forbid `setu desktop` (remote desktop)
	// DisableFileTransfer turns off uploads from clients to this device.
	DisableFileTransfer bool `json:"DisableFileTransfer,omitempty"`

	// Access requirements.
	RequirePassword     bool     `json:"RequirePassword,omitempty"`
	MinPasswordLength   int      `json:"MinPasswordLength,omitempty"`
	RequirePersonalKeys bool     `json:"RequirePersonalKeys,omitempty"` // service: no shared control links
	MaxClients          int      `json:"MaxClients,omitempty"`
	MaxShareDuration    Duration `json:"MaxShareDuration,omitempty"` // `setu share` must expire within this
	MaxKeyValidity      Duration `json:"MaxKeyValidity,omitempty"`   // service keys must expire within this
	// MaxIdleTime disconnects control clients after this long without input.
	MaxIdleTime Duration `json:"MaxIdleTime,omitempty"`

	// Allow lists (empty = anything).
	AllowedCommands []string `json:"AllowedCommands,omitempty"` // program names or absolute paths
	AllowedRelays   []string `json:"AllowedRelays,omitempty"`
	AllowedWebURLs  []string `json:"AllowedWebURLs,omitempty"`

	// Audit: when AuditEndpoint is set, auditing is forced on with these
	// settings and users cannot turn it off or redirect it.
	AuditEndpoint string            `json:"AuditEndpoint,omitempty"`
	AuditHeaders  map[string]string `json:"AuditHeaders,omitempty"`
	AuditCAFile   string            `json:"AuditCAFile,omitempty"`
	AuditCertFile string            `json:"AuditCertFile,omitempty"`
	AuditKeyFile  string            `json:"AuditKeyFile,omitempty"`
	AuditInsecure bool              `json:"AuditInsecure,omitempty"`
	AuditRequired bool              `json:"AuditRequired,omitempty"`
	AuditInput    string            `json:"AuditInput,omitempty"`
	AuditRedact   []string          `json:"AuditRedact,omitempty"`
	// ResourceAttributes are added to every audit record, e.g. the device
	// serial number or asset tag your MDM substitutes into the payload.
	ResourceAttributes map[string]string `json:"ResourceAttributes,omitempty"`

	source string
}

// Duration is a time.Duration written as "720h" (or a number of seconds).
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var n float64
	if json.Unmarshal(b, &n) == nil {
		*d = Duration(time.Duration(n * float64(time.Second)))
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

// D returns the duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// Source says where the policy came from ("" when there is none).
func (p *Policy) Source() string {
	if p == nil {
		return ""
	}
	return p.source
}

// Managed reports whether a policy is in force.
func (p *Policy) Managed() bool { return p != nil && p.source != "" }

// Hash identifies the exact policy in force (for audit records).
func (p *Policy) Hash() string {
	if !p.Managed() {
		return ""
	}
	b, _ := json.Marshal(p)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}

// Parse decodes a JSON policy strictly: unknown keys are errors, so a typo
// in a restriction cannot silently disable it.
func Parse(b []byte, source string) (*Policy, error) {
	var p Policy
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("managed policy %s: %w", source, err)
	}
	p.source = source
	return &p, p.validate()
}

// parseProfilePrefs parses managed preferences from a configuration profile
// (as JSON). Profile bookkeeping keys (PayloadUUID, PayloadIdentifier, ...)
// are ignored; everything else is parsed strictly.
func parseProfilePrefs(b []byte, source string) (*Policy, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("managed policy %s: %w", source, err)
	}
	for k := range m {
		if strings.HasPrefix(k, "Payload") || k == "PolicyVersion" {
			delete(m, k)
		}
	}
	clean, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return Parse(clean, source)
}

func (p *Policy) validate() error {
	bad := func(f string, a ...any) error {
		return fmt.Errorf("managed policy %s: %s", p.source, fmt.Sprintf(f, a...))
	}
	if p.MinPasswordLength < 0 || p.MaxClients < 0 || p.MaxShareDuration < 0 || p.MaxKeyValidity < 0 || p.MaxIdleTime < 0 {
		return bad("limits must not be negative")
	}
	switch p.AuditInput {
	case "", "lines", "off":
	default:
		return bad("AuditInput %q: want lines or off", p.AuditInput)
	}
	if p.AuditEndpoint == "" && (p.AuditRequired || len(p.AuditHeaders) > 0 || p.AuditCAFile != "" || p.AuditCertFile != "") {
		return bad("Audit* settings need AuditEndpoint")
	}
	return nil
}

// Load reads the policy for this device. It returns an empty, unmanaged
// policy when none is deployed, and an error when one is deployed but
// unusable.
func Load() (*Policy, error) {
	p, err := loadNative()
	if err != nil || p != nil {
		return p, err
	}
	for _, path := range filePaths() {
		p, err := LoadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		return p, err
	}
	return &Policy{}, nil
}

// LoadFile reads a JSON policy file, refusing one a non-administrator could
// have written.
func LoadFile(path string) (*Policy, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := checkAdminOwned(path); err != nil {
		return nil, fmt.Errorf("managed policy %s: %w", path, err)
	}
	return Parse(b, path)
}

// Violation is returned when a setting is not allowed by the policy.
type Violation struct{ Key, Msg string }

func (v *Violation) Error() string {
	return fmt.Sprintf("blocked by your organization's policy (%s): %s", v.Key, v.Msg)
}

func violation(key, f string, a ...any) error {
	return &Violation{Key: key, Msg: fmt.Sprintf(f, a...)}
}

// CheckShare reports whether ad-hoc sharing is allowed.
func (p *Policy) CheckShare(manual bool) error {
	if p == nil {
		return nil
	}
	if p.DisableShare {
		return violation("DisableShare", "`setu share` is disabled on this device")
	}
	if manual && p.DisableManual {
		return violation("DisableManual", "--manual is disabled on this device")
	}
	return nil
}

// DesktopCommand is how AllowedCommands names remote desktop sharing, so a
// policy that restricts what may be shared also restricts the desktop.
const DesktopCommand = "@desktop"

// CheckDesktop reports whether remote desktop sharing is allowed.
func (p *Policy) CheckDesktop(manual bool) error {
	if p == nil {
		return nil
	}
	if err := p.CheckShare(manual); err != nil {
		return err
	}
	if p.DisableDesktop {
		return violation("DisableDesktop", "`setu desktop` is disabled on this device")
	}
	return p.CheckCommand([]string{DesktopCommand})
}

// FileTransferAllowed reports whether clients may upload files.
func (p *Policy) FileTransferAllowed() bool { return p == nil || !p.DisableFileTransfer }

// CheckService reports whether the background service is allowed.
func (p *Policy) CheckService() error {
	if p != nil && p.DisableService {
		return violation("DisableService", "`setu service` is disabled on this device")
	}
	return nil
}

// CheckCommand checks the shared program against AllowedCommands. An entry
// with a path separator must match the resolved path exactly; a bare name
// matches the program's base name (without .exe/.cmd/.bat on Windows).
func (p *Policy) CheckCommand(command []string) error {
	if p == nil || len(p.AllowedCommands) == 0 {
		return nil
	}
	if len(command) == 0 {
		return violation("AllowedCommands", "no command")
	}
	prog := command[0]
	base := strings.ToLower(filepath.Base(prog))
	for _, ext := range []string{".exe", ".cmd", ".bat"} {
		base = strings.TrimSuffix(base, ext)
	}
	for _, a := range p.AllowedCommands {
		if strings.ContainsAny(a, `/\`) {
			if filepath.Clean(a) == filepath.Clean(prog) {
				return nil
			}
		} else if strings.EqualFold(a, base) {
			return nil
		}
	}
	return violation("AllowedCommands", "%q may not be shared (allowed: %s)", prog, strings.Join(p.AllowedCommands, ", "))
}

// Relays returns the relays to use: the requested ones if allowed, or the
// allowed list when none were requested (nil means the built-in default).
func (p *Policy) Relays(requested []string) ([]string, error) {
	if p == nil || len(p.AllowedRelays) == 0 {
		return requested, nil
	}
	if len(requested) == 0 {
		return p.AllowedRelays, nil
	}
	for _, r := range requested {
		if !slices.Contains(p.AllowedRelays, r) {
			return nil, violation("AllowedRelays", "relay %s is not allowed", r)
		}
	}
	return requested, nil
}

// CheckWebURL checks the web client base URL ("" is always allowed: it
// means terminal-only links).
func (p *Policy) CheckWebURL(u string) error {
	if p == nil || len(p.AllowedWebURLs) == 0 || u == "" {
		return nil
	}
	if slices.Contains(p.AllowedWebURLs, u) {
		return nil
	}
	return violation("AllowedWebURLs", "web client %s is not allowed (allowed: %s)", u, strings.Join(p.AllowedWebURLs, ", "))
}

// MinPassword is the minimum password length, at least floor.
func (p *Policy) MinPassword(floor int) int {
	if p != nil && p.MinPasswordLength > floor {
		return p.MinPasswordLength
	}
	return floor
}

// CheckPassword checks whether a password is present and long enough.
func (p *Policy) CheckPassword(has bool, length int) error {
	if p == nil {
		return nil
	}
	if !has && p.RequirePassword {
		return violation("RequirePassword", "a password is required (use --password)")
	}
	if has && p.MinPasswordLength > 0 && length < p.MinPasswordLength {
		return violation("MinPasswordLength", "passwords need at least %d characters", p.MinPasswordLength)
	}
	return nil
}

// Clients applies MaxClients: an unset request gets the cap, an explicit
// request above it is refused.
func (p *Policy) Clients(n int, explicit bool) (int, error) {
	if p == nil || p.MaxClients == 0 || n <= p.MaxClients {
		return n, nil
	}
	if explicit {
		return 0, violation("MaxClients", "at most %d client(s)", p.MaxClients)
	}
	return p.MaxClients, nil
}

// ShareExpiry applies MaxShareDuration to `setu share --expire`: no expiry
// becomes the maximum, a longer one is refused.
func (p *Policy) ShareExpiry(d time.Duration) (time.Duration, error) {
	if p == nil || p.MaxShareDuration == 0 {
		return d, nil
	}
	if d == 0 {
		return p.MaxShareDuration.D(), nil
	}
	if d > p.MaxShareDuration.D() {
		return 0, violation("MaxShareDuration", "shares must expire within %s", p.MaxShareDuration.D())
	}
	return d, nil
}

// IdleTimeout applies MaxIdleTime to an idle timeout (0 = none): an unset
// one gets the maximum; turning it off, or a longer one, is refused when
// explicitly asked for.
func (p *Policy) IdleTimeout(d time.Duration, explicit bool) (time.Duration, error) {
	if p == nil || p.MaxIdleTime == 0 {
		return d, nil
	}
	max := p.MaxIdleTime.D()
	switch {
	case d == 0 && !explicit:
		return max, nil
	case d == 0 || d > max:
		return 0, violation("MaxIdleTime", "control clients must be disconnected after at most %s without input", max)
	}
	return d, nil
}

// CheckKeyValidity checks a service key lifetime (0 = never expires).
func (p *Policy) CheckKeyValidity(d time.Duration) error {
	if p == nil || p.MaxKeyValidity == 0 {
		return nil
	}
	if d == 0 || d > p.MaxKeyValidity.D() {
		return violation("MaxKeyValidity", "keys must expire within %s (use --valid-for)", p.MaxKeyValidity.D())
	}
	return nil
}

// AuditConfig merges a user's audit settings with the policy. When the
// policy names a collector, auditing is forced on with the policy's
// settings: users may add redaction patterns or make it required, but they
// cannot turn it off, lower it to connections only, or send it elsewhere.
func (p *Policy) AuditConfig(user audit.Config) (audit.Config, error) {
	if p == nil || p.AuditEndpoint == "" {
		return user, nil
	}
	if user.Endpoint != "" && user.Endpoint != p.AuditEndpoint {
		return audit.Config{}, violation("AuditEndpoint", "audit records go to %s; a different collector cannot be set", p.AuditEndpoint)
	}
	input := p.AuditInput
	if user.Input == audit.InputOff && input != audit.InputOff {
		return audit.Config{}, violation("AuditInput", "command auditing cannot be turned off")
	}
	if input == "" {
		input = user.Input
	}
	return audit.Config{
		Endpoint: p.AuditEndpoint,
		Headers:  p.AuditHeaders,
		CAFile:   p.AuditCAFile,
		CertFile: p.AuditCertFile,
		KeyFile:  p.AuditKeyFile,
		Insecure: p.AuditInsecure,
		Input:    input,
		Redact:   append(append([]string(nil), p.AuditRedact...), user.Redact...),
		Required: p.AuditRequired || user.Required,
	}, nil
}

// AuditAttributes are the attributes to stamp on every audit record: the
// admin's ResourceAttributes plus where the policy came from and its hash.
func (p *Policy) AuditAttributes() map[string]string {
	if !p.Managed() {
		return nil
	}
	m := map[string]string{"setu.policy.source": p.source, "setu.policy.hash": p.Hash()}
	for k, v := range p.ResourceAttributes {
		m[k] = v
	}
	return m
}

// Redacted returns a copy safe to print: header values are hidden.
func (p *Policy) Redacted() *Policy {
	if p == nil {
		return nil
	}
	c := *p
	if len(p.AuditHeaders) > 0 {
		c.AuditHeaders = map[string]string{}
		for k := range p.AuditHeaders {
			c.AuditHeaders[k] = "[hidden]"
		}
	}
	return &c
}
