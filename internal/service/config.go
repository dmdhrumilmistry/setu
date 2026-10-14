// Package service runs long-lived shares in the background: a per-user
// supervisor that starts at login, serves every enabled, unexpired profile
// (a command plus its persistent invite keys) and restarts commands that
// exit, so a profile's link keeps working across reboots.
//
// State lives in one directory, readable only by the user:
//
//	config.json     profiles, their keys and password verifiers, audit settings
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
	"time"

	"github.com/dmdhrumilmistry/setu/internal/audit"
	"github.com/dmdhrumilmistry/setu/internal/link"
	"github.com/dmdhrumilmistry/setu/internal/nostr"
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
	Command    []string                 `json:"command"`
	Dir        string                   `json:"dir,omitempty"`
	Enabled    bool                     `json:"enabled"`
	ControlKey string                   `json:"control_key"`
	ViewKey    string                   `json:"view_key,omitempty"`
	Password   *secure.PasswordVerifier `json:"password,omitempty"`
	MaxClients int                      `json:"max_clients"`

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

// NewKeys replaces the profile's invite keys (and adds a view key if
// withView), restarting the expiry clock. Old links stop working.
func (p *Profile) NewKeys(withView bool, now time.Time) error {
	cs, err := secure.NewSecret()
	if err != nil {
		return err
	}
	p.ControlKey, p.ViewKey = cs.String(), ""
	if withView {
		vs, err := secure.NewSecret()
		if err != nil {
			return err
		}
		p.ViewKey = vs.String()
	}
	p.ExpiresAt = time.Time{}
	if p.ValidFor > 0 {
		p.ExpiresAt = now.Add(time.Duration(p.ValidFor)).UTC()
	}
	return nil
}

// Validate checks a profile before it is saved or served.
func (p *Profile) Validate() error {
	if len(p.Command) == 0 || p.Command[0] == "" {
		return errors.New("profile has no command")
	}
	if p.Dir != "" {
		if st, err := os.Stat(p.Dir); err != nil || !st.IsDir() {
			return fmt.Errorf("working directory %q does not exist", p.Dir)
		}
	}
	if _, err := secure.ParseSecret(p.ControlKey); err != nil {
		return fmt.Errorf("control key: %w", err)
	}
	if p.ViewKey != "" {
		if _, err := secure.ParseSecret(p.ViewKey); err != nil {
			return fmt.Errorf("view key: %w", err)
		}
	}
	if p.Password != nil {
		if err := p.Password.Validate(); err != nil {
			return err
		}
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

// Links returns the control link and, if configured, the view-only link.
func (p *Profile) Links() []string {
	inv := func(key string) string {
		return link.Build(p.WebURL, link.Invite{Secret: key, Relays: p.Relays})
	}
	out := []string{inv(p.ControlKey)}
	if p.ViewKey != "" {
		out = append(out, inv(p.ViewKey))
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
