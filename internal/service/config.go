// Package service runs long-lived shares in the background: a per-user
// supervisor that starts at login, serves every enabled, unexpired profile
// (a command plus its persistent invite keys) and restarts commands that
// exit, so a profile's link keeps working across reboots.
//
// State lives in one directory, readable only by the user:
//
//	config.json     profiles, their keys and password verifiers, audit settings
//	devices.json    devices each profile admits, and pending pairing codes
//	supervisor.pid  lock held by the running supervisor
//	service.log     supervisor log (macOS and Windows; systemd uses the journal)
//
// On Windows the directory is under %LOCALAPPDATA%, which does not roam to
// other machines with the user profile.
package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/audit"
	"github.com/dmdhrumilmistry/setu/internal/link"
	"github.com/dmdhrumilmistry/setu/internal/nostr"
	"github.com/dmdhrumilmistry/setu/internal/policy"
	"github.com/dmdhrumilmistry/setu/internal/proto"
	"github.com/dmdhrumilmistry/setu/internal/rtc"
	"github.com/dmdhrumilmistry/setu/internal/secure"
)

const configVersion = 1

// Config is the whole service configuration.
type Config struct {
	Version  int                 `json:"version"`
	Audit    audit.Config        `json:"audit"`
	Profiles map[string]*Profile `json:"profiles"`
}

// Profile is one persistent share. Its keys are secrets: whoever holds the
// control key (and the password) gets a shell.
type Profile struct {
	Command []string `json:"command"`
	// Desktop, if set, makes the profile share this computer's desktop
	// (screen, mouse, keyboard, clipboard) instead of running Command.
	Desktop *DesktopProfile `json:"desktop,omitempty"`
	// NoUpload refuses file uploads from clients.
	// RecordDir receives an asciicast recording of each session (terminal
	// profiles only).
	RecordDir string `json:"record_dir,omitempty"`
	// IdleTimeout disconnects control clients after this long without input.
	IdleTimeout Duration `json:"idle_timeout,omitempty"`
	NoUpload    bool     `json:"no_upload,omitempty"`
	Dir         string   `json:"dir,omitempty"`
	Enabled     bool     `json:"enabled"`
	// ControlKey and ViewKey are shared links: audit records can only say
	// "someone holding the shared link". Either may be empty.
	ControlKey string `json:"control_key,omitempty"`
	ViewKey    string `json:"view_key,omitempty"`
	// PersonalKeys gives each person their own link, keyed by their name,
	// so audit records say who connected and typed each command.
	PersonalKeys map[string]*PersonalKey `json:"personal_keys,omitempty"`
	// RequireDevice admits only devices listed for the profile in
	// devices.json; a new device joins once with a pairing code (`setu
	// service pair`). It is the default for new profiles, and excludes a
	// password.
	RequireDevice bool                     `json:"require_device,omitempty"`
	Password      *secure.PasswordVerifier `json:"password,omitempty"`
	// PasswordEveryJoin asks for the password on every join, instead of
	// once per device.
	PasswordEveryJoin bool `json:"password_every_join,omitempty"`
	MaxClients        int  `json:"max_clients"`

	// Keys stop working at ExpiresAt; rotating them restarts the clock
	// with ValidFor.
	ValidFor  Duration  `json:"valid_for,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`

	Relays       []string `json:"relays,omitempty"`
	STUN         []string `json:"stun,omitempty"`
	TURN         string   `json:"turn,omitempty"`
	TURNUser     string   `json:"turn_user,omitempty"`
	TURNPassword string   `json:"turn_password,omitempty"`
	WebURL       string   `json:"web_url"`

	Created time.Time `json:"created"`
	Rotated time.Time `json:"rotated,omitzero"`
}

// DesktopProfile configures a remote desktop profile.
type DesktopProfile struct {
	FPS     int `json:"fps,omitempty"` // frame rate cap (0 = default)
	Display int `json:"display"`       // display index; -1 = the primary display
	// X11Display and XAuthority are the graphical session's DISPLAY and
	// XAUTHORITY, captured when the profile is created: service managers
	// start without that environment (Linux and the BSDs only).
	X11Display string `json:"x11_display,omitempty"`
	XAuthority string `json:"xauthority,omitempty"`
}

// Label is what the profile shares, for listings and logs.
func (p *Profile) Label() string {
	if p.Desktop != nil {
		return "remote desktop"
	}
	return strings.Join(p.Command, " ")
}

// PersonalKey is an invite key held by one named person.
type PersonalKey struct {
	Key     string    `json:"key"`
	Role    string    `json:"role"` // proto.RoleControl or proto.RoleView
	Created time.Time `json:"created"`
}

// Duration is a time.Duration that reads and writes as "720h0m0s".
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	*d = Duration(v)
	return err
}

// MaxClientsLimit caps how many clients one profile may serve.
const MaxClientsLimit = 16

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// ValidName reports whether name can be used for a profile.
func ValidName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("profile name %q: use 1-32 lowercase letters, digits, '-' or '_'", name)
	}
	return nil
}

// Dir returns the service state directory. SETU_SERVICE_DIR overrides it.
func Dir() (string, error) {
	if d := os.Getenv("SETU_SERVICE_DIR"); d != "" {
		return filepath.Abs(d)
	}
	if runtime.GOOS == "windows" {
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "setu", "service"), nil
		}
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "setu", "service"), nil
}

// ConfigPath, LockPath and LogPath locate the service's files in dir.
func ConfigPath(dir string) string { return filepath.Join(dir, "config.json") }
func LockPath(dir string) string   { return filepath.Join(dir, "supervisor.pid") }
func LogPath(dir string) string    { return filepath.Join(dir, "service.log") }

// EnsureDir creates dir with owner-only permissions.
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

// Load reads the configuration. A missing file is an empty configuration.
// A file other users can read is refused, as ssh does with private keys.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{Version: configVersion, Profiles: map[string]*Profile{}}, nil
	}
	if err != nil {
		return nil, err
	}
	if err := checkPrivate(path); err != nil {
		return nil, err
	}
	var c Config
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.Version != configVersion {
		return nil, fmt.Errorf("%s: unsupported config version %d", path, c.Version)
	}
	if c.Profiles == nil {
		c.Profiles = map[string]*Profile{}
	}
	return &c, nil
}

// Save writes the configuration atomically with owner-only permissions.
func Save(path string, c *Config) error {
	c.Version = configVersion
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(path, b)
}

// writePrivate replaces path atomically with an owner-only file.
func writePrivate(path string, b []byte) error {
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Names returns the profile names in order.
func (c *Config) Names() []string {
	names := make([]string, 0, len(c.Profiles))
	for n := range c.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Get returns a profile or a helpful error.
func (c *Config) Get(name string) (*Profile, error) {
	p, ok := c.Profiles[name]
	if !ok {
		return nil, fmt.Errorf("no service profile %q (see `setu service list`)", name)
	}
	return p, nil
}

func newKey() (string, error) {
	s, err := secure.NewSecret()
	return s.String(), err
}

// NewKeys sets fresh shared keys (a shared control link if sharedControl,
// a shared view link if sharedView) and restarts the expiry clock.
func (p *Profile) NewKeys(sharedControl, sharedView bool, now time.Time) error {
	p.ControlKey, p.ViewKey = "", ""
	var err error
	if sharedControl {
		if p.ControlKey, err = newKey(); err != nil {
			return err
		}
	}
	if sharedView {
		if p.ViewKey, err = newKey(); err != nil {
			return err
		}
	}
	p.restartExpiry(now)
	return nil
}

// Rotate replaces every key the profile has (shared and personal) and
// restarts the expiry clock. All old links stop working.
func (p *Profile) Rotate(now time.Time) error {
	if err := p.NewKeys(p.ControlKey != "", p.ViewKey != "", now); err != nil {
		return err
	}
	for _, k := range p.PersonalKeys {
		var err error
		if k.Key, err = newKey(); err != nil {
			return err
		}
	}
	return nil
}

func (p *Profile) restartExpiry(now time.Time) {
	p.ExpiresAt = time.Time{}
	if p.ValidFor > 0 {
		p.ExpiresAt = now.Add(time.Duration(p.ValidFor)).UTC()
	}
}

// AddPersonalKey creates a key for one person (role proto.RoleControl or
// proto.RoleView). Their records are attributed to person.
func (p *Profile) AddPersonalKey(person, role string, now time.Time) error {
	if err := ValidName(person); err != nil {
		return fmt.Errorf("person: %w", err)
	}
	if role != proto.RoleControl && role != proto.RoleView {
		return fmt.Errorf("role %q: want %q or %q", role, proto.RoleControl, proto.RoleView)
	}
	if _, dup := p.PersonalKeys[person]; dup {
		return fmt.Errorf("%s already has a key (remove it first to issue a new one)", person)
	}
	key, err := newKey()
	if err != nil {
		return err
	}
	if p.PersonalKeys == nil {
		p.PersonalKeys = map[string]*PersonalKey{}
	}
	p.PersonalKeys[person] = &PersonalKey{Key: key, Role: role, Created: now.UTC()}
	return nil
}

// People returns the names of the personal key holders, sorted.
func (p *Profile) People() []string {
	names := make([]string, 0, len(p.PersonalKeys))
	for n := range p.PersonalKeys {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Validate checks a profile before it is saved or served.
func (p *Profile) Validate() error {
	switch {
	case p.Desktop != nil && len(p.Command) > 0:
		return errors.New("a desktop profile has no command")
	case p.Desktop != nil && p.RecordDir != "":
		return errors.New("recording is for terminal profiles, not the desktop")
	case p.RecordDir != "" && !filepath.IsAbs(p.RecordDir):
		return errors.New("the recording folder must be an absolute path")
	case p.Desktop != nil && (p.Desktop.FPS < 0 || p.Desktop.FPS > 60):
		return errors.New("desktop frame rate must be 1..60 (0 = default)")
	case p.Desktop != nil && p.Desktop.Display < -1:
		return errors.New("desktop display must be -1 (primary) or a display number")
	case p.Desktop == nil && (len(p.Command) == 0 || p.Command[0] == ""):
		return errors.New("profile has no command")
	}
	if p.Dir != "" {
		if st, err := os.Stat(p.Dir); err != nil || !st.IsDir() {
			return fmt.Errorf("working directory %q does not exist", p.Dir)
		}
	}
	seen := map[string]bool{}
	check := func(what, key string) error {
		if _, err := secure.ParseSecret(key); err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		if seen[key] {
			return fmt.Errorf("%s: the same key is used twice", what)
		}
		seen[key] = true
		return nil
	}
	controls := 0
	if p.ControlKey != "" {
		if err := check("shared control key", p.ControlKey); err != nil {
			return err
		}
		controls++
	}
	if p.ViewKey != "" {
		if err := check("shared view key", p.ViewKey); err != nil {
			return err
		}
	}
	for _, name := range p.People() {
		k := p.PersonalKeys[name]
		if err := ValidName(name); err != nil {
			return err
		}
		if k.Role != proto.RoleControl && k.Role != proto.RoleView {
			return fmt.Errorf("key of %s: bad role %q", name, k.Role)
		}
		if err := check("key of "+name, k.Key); err != nil {
			return err
		}
		if k.Role == proto.RoleControl {
			controls++
		}
	}
	if controls == 0 && p.ViewKey == "" && len(p.PersonalKeys) == 0 {
		return errors.New("profile has no keys")
	}
	if p.Password != nil {
		if err := p.Password.Validate(); err != nil {
			return err
		}
	}
	switch {
	case p.RequireDevice && p.Password != nil:
		return errors.New("a profile uses either paired devices or a password, not both")
	case p.PasswordEveryJoin && p.Password == nil:
		return errors.New("password_every_join needs a password")
	}
	if p.IdleTimeout < 0 {
		return errors.New("idle timeout must not be negative")
	}
	if p.MaxClients < 1 || p.MaxClients > MaxClientsLimit {
		return fmt.Errorf("max clients must be 1..%d", MaxClientsLimit)
	}
	if p.WebURL != "" {
		// The invite secret rides in this URL's fragment, so the page must be
		// served over https (or from this machine).
		u, err := url.Parse(p.WebURL)
		if err != nil || u.Host == "" || u.Fragment != "" ||
			(u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"))) {
			return fmt.Errorf("web URL %q: want an https:// base URL (or empty for terminal-only links)", p.WebURL)
		}
	}
	return rtc.ValidateICE(p.ICE())
}

// keyValiditySlack absorbs the time between creating keys and checking them.
const keyValiditySlack = time.Minute

// CheckPolicy checks a profile against the managed policy. The supervisor
// runs it before serving, because the config file is the user's to edit.
func (p *Profile) CheckPolicy(pol *policy.Policy, now time.Time) error {
	if !pol.Managed() {
		return nil
	}
	if err := pol.CheckService(); err != nil {
		return err
	}
	if p.Desktop != nil {
		if err := pol.CheckDesktop(false); err != nil {
			return err
		}
	} else if err := pol.CheckCommand(p.Command); err != nil {
		return err
	}
	if err := pol.CheckWebURL(p.WebURL); err != nil {
		return err
	}
	if _, err := pol.Relays(p.Relays); err != nil {
		return err
	}
	if err := pol.CheckPassword(p.Password != nil, servicePasswordFloor); err != nil {
		return err
	}
	if _, err := pol.Clients(p.MaxClients, true); err != nil {
		return err
	}
	if pol.RequireDevicePairing && !p.RequireDevice {
		return &policy.Violation{Key: "RequireDevicePairing", Msg: "profiles must admit only paired devices (setu service auth NAME device)"}
	}
	if pol.RequirePersonalKeys && p.ControlKey != "" {
		return &policy.Violation{Key: "RequirePersonalKeys", Msg: "shared control links are not allowed; give each person their own key"}
	}
	if _, err := pol.IdleTimeout(time.Duration(p.IdleTimeout), false); err != nil {
		return err
	}
	if err := pol.CheckKeyValidity(time.Duration(p.ValidFor)); err != nil {
		return err
	}
	if max := pol.MaxKeyValidity.D(); max > 0 && (p.ExpiresAt.IsZero() || p.ExpiresAt.Sub(now) > max+keyValiditySlack) {
		return &policy.Violation{Key: "MaxKeyValidity", Msg: fmt.Sprintf("keys must expire within %s; rotate them", max)}
	}
	return nil
}

// servicePasswordFloor stands in for the length of a stored password, which
// is only kept as a verifier; the CLI checks the real length when it is set.
const servicePasswordFloor = 1 << 10

// Active reports whether the supervisor should serve the profile now.
func (p *Profile) Active(now time.Time) bool {
	return p.Enabled && (p.ExpiresAt.IsZero() || now.Before(p.ExpiresAt))
}

// ICE returns the profile's STUN/TURN configuration.
func (p *Profile) ICE() []proto.ICEServer {
	ice := rtc.DefaultICE
	if len(p.STUN) > 0 {
		ice = []proto.ICEServer{{URLs: p.STUN}}
	}
	if p.TURN != "" {
		ice = append(append([]proto.ICEServer(nil), ice...), proto.ICEServer{URLs: []string{p.TURN}, Username: p.TURNUser, Credential: p.TURNPassword})
	}
	return ice
}

func (p *Profile) relays() []string {
	if len(p.Relays) > 0 {
		return p.Relays
	}
	return nostr.DefaultRelays
}

// AuthMode describes how new devices get in: AuthDevices, AuthPassword or
// AuthNone.
func (p *Profile) AuthMode() string {
	switch {
	case p.RequireDevice:
		return AuthDevices
	case p.Password != nil:
		return AuthPassword
	}
	return AuthNone
}

// Profile auth modes (`setu service add --auth`).
const (
	AuthDevices  = "device"   // paired devices only
	AuthPassword = "password" // a password, asked once per device
	AuthNone     = "none"     // the link alone
)

// Link is one invite link of a profile.
type Link struct {
	Person string // "" for the shared links
	Role   string
	URL    string
}

// Links returns the shared links and then every personal link.
func (p *Profile) Links() []Link {
	inv := func(key string) string {
		return link.Build(p.WebURL, link.Invite{Secret: key, Relays: p.Relays})
	}
	var out []Link
	if p.ControlKey != "" {
		out = append(out, Link{Role: proto.RoleControl, URL: inv(p.ControlKey)})
	}
	if p.ViewKey != "" {
		out = append(out, Link{Role: proto.RoleView, URL: inv(p.ViewKey)})
	}
	for _, name := range p.People() {
		k := p.PersonalKeys[name]
		out = append(out, Link{Person: name, Role: k.Role, URL: inv(k.Key)})
	}
	return out
}

// fingerprint changes whenever anything that affects serving changes, so
// the supervisor restarts exactly the profiles that were edited.
func fingerprint(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}
