package service

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/policy"
	"github.com/dmdhrumilmistry/setu/internal/proto"
	"github.com/dmdhrumilmistry/setu/internal/secure"
)

func testProfile(t *testing.T) *Profile {
	t.Helper()
	p := &Profile{Command: []string{"sh"}, Enabled: true, MaxClients: 1, ValidFor: Duration(time.Hour), WebURL: "https://example.invalid/"}
	if err := p.NewKeys(true, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "svc", "config.json")
	v, err := secure.NewPasswordVerifier("a long password")
	if err != nil {
		t.Fatal(err)
	}
	p := testProfile(t)
	p.Password = v
	in := &Config{Profiles: map[string]*Profile{"claude": p}}
	in.Audit.Endpoint = "https://otel.example.com"
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(path)
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("config mode %v, want 0600", st.Mode().Perm())
		}
		dst, _ := os.Stat(filepath.Dir(path))
		if dst.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode %v, want 0700", dst.Mode().Perm())
		}
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := out.Profiles["claude"]
	if got.ControlKey != p.ControlKey || got.ViewKey != p.ViewKey || got.ValidFor != p.ValidFor ||
		!got.ExpiresAt.Equal(p.ExpiresAt) || string(got.Password.Key) != string(v.Key) || out.Audit.Endpoint != in.Audit.Endpoint {
		t.Fatalf("round trip mismatch:\n%+v\n%+v", got, p)
	}
	if fingerprint(got) != fingerprint(p) {
		t.Fatal("fingerprint changed across a round trip; the supervisor would restart the profile")
	}
}

func TestLoadRejects(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, err := Load(write("unknown.json", `{"version":1,"profiles":{},"surprise":1}`, 0o600)); err == nil {
		t.Error("unknown field accepted")
	}
	if _, err := Load(write("version.json", `{"version":9,"profiles":{}}`, 0o600)); err == nil {
		t.Error("unknown version accepted")
	}
	if runtime.GOOS != "windows" {
		p := write("open.json", `{"version":1,"profiles":{}}`, 0o644)
		if err := os.Chmod(p, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "chmod 600") {
			t.Errorf("world-readable config accepted: %v", err)
		}
	}
	c, err := Load(filepath.Join(dir, "missing.json"))
	if err != nil || len(c.Profiles) != 0 {
		t.Errorf("missing config: %v %v", c, err)
	}
}

func TestProfileValidateAndActive(t *testing.T) {
	now := time.Now()
	p := testProfile(t)
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if !p.Active(now) || p.Active(now.Add(2*time.Hour)) {
		t.Error("expiry not honoured")
	}
	p.Enabled = false
	if p.Active(now) {
		t.Error("disabled profile is active")
	}
	old := p.ControlKey
	if err := p.NewKeys(true, false, now); err != nil {
		t.Fatal(err)
	}
	if p.ControlKey == old || p.ViewKey != "" {
		t.Error("NewKeys did not replace the keys")
	}
	for _, mut := range []func(*Profile){
		func(p *Profile) { p.Command = nil },
		func(p *Profile) { p.ControlKey = "short" },
		func(p *Profile) { p.MaxClients = 0 },
		func(p *Profile) { p.MaxClients = MaxClientsLimit + 1 },
		func(p *Profile) { p.Dir = filepath.Join(t.TempDir(), "nope") },
		func(p *Profile) { p.Password = &secure.PasswordVerifier{Iter: 10} },
		func(p *Profile) { p.STUN = []string{"http://not-stun"} },
		func(p *Profile) { p.WebURL = "--view-link" },
		func(p *Profile) { p.WebURL = "http://pages.example.com/" },
	} {
		q := testProfile(t)
		mut(q)
		if err := q.Validate(); err == nil {
			t.Errorf("invalid profile accepted: %+v", q)
		}
	}
}

func TestPersonalKeys(t *testing.T) {
	now := time.Now()
	p := &Profile{Command: []string{"sh"}, Enabled: true, MaxClients: 1}
	if err := p.NewKeys(false, false, now); err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(); err == nil {
		t.Fatal("profile without any key accepted")
	}
	if err := p.AddPersonalKey("alice", proto.RoleControl, now); err != nil {
		t.Fatal(err)
	}
	if err := p.AddPersonalKey("bob", proto.RoleView, now); err != nil {
		t.Fatal(err)
	}
	if err := p.AddPersonalKey("alice", proto.RoleControl, now); err == nil {
		t.Error("duplicate person accepted")
	}
	for _, bad := range [][2]string{{"Alice", proto.RoleControl}, {"carol", "admin"}} {
		if err := p.AddPersonalKey(bad[0], bad[1], now); err == nil {
			t.Errorf("AddPersonalKey(%q, %q) accepted", bad[0], bad[1])
		}
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	links := p.Links()
	if len(links) != 2 || links[0].Person != "alice" || links[1].Person != "bob" || links[1].Role != proto.RoleView {
		t.Fatalf("links %+v", links)
	}

	cfg, err := hostConfig("x", p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ControlSecret != nil || len(cfg.Keys) != 2 || cfg.Keys[0].Name != "alice" || cfg.Keys[0].Role != proto.RoleControl {
		t.Fatalf("host config keys %+v", cfg.Keys)
	}

	old := p.PersonalKeys["alice"].Key
	if err := p.Rotate(now); err != nil {
		t.Fatal(err)
	}
	if p.PersonalKeys["alice"].Key == old || p.ControlKey != "" {
		t.Error("Rotate must replace personal keys and keep the shared link off")
	}

	p.PersonalKeys["bob"].Key = p.PersonalKeys["alice"].Key
	if err := p.Validate(); err == nil {
		t.Error("two people sharing one key accepted")
	}
}

func TestCheckPolicy(t *testing.T) {
	now := time.Now()
	pol, err := policy.Parse([]byte(`{"RequirePersonalKeys": true, "RequirePassword": true, "MaxKeyValidity": "168h", "MaxClients": 1}`), "test")
	if err != nil {
		t.Fatal(err)
	}
	v, _ := secure.NewPasswordVerifier("a long enough password")
	good := func() *Profile {
		p := &Profile{Command: []string{"sh"}, Enabled: true, MaxClients: 1, Password: v, ValidFor: Duration(24 * time.Hour)}
		if err := p.NewKeys(false, false, now); err != nil {
			t.Fatal(err)
		}
		if err := p.AddPersonalKey("alice", proto.RoleControl, now); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if err := good().CheckPolicy(pol, now); err != nil {
		t.Fatalf("compliant profile refused: %v", err)
	}
	for key, mut := range map[string]func(*Profile){
		"RequirePersonalKeys": func(p *Profile) { p.ControlKey = p.PersonalKeys["alice"].Key + "x" },
		"RequirePassword":     func(p *Profile) { p.Password = nil },
		"MaxClients":          func(p *Profile) { p.MaxClients = 3 },
		"MaxKeyValidity":      func(p *Profile) { p.ValidFor = 0 },
		"MaxKeyValidity ":     func(p *Profile) { p.ExpiresAt = now.Add(365 * 24 * time.Hour) }, // hand-edited expiry
	} {
		p := good()
		mut(p)
		var viol *policy.Violation
		if err := p.CheckPolicy(pol, now); !errors.As(err, &viol) || viol.Key != strings.TrimSpace(key) {
			t.Errorf("%s: got %v", key, err)
		}
	}
	if err := good().CheckPolicy(nil, now); err != nil {
		t.Errorf("no policy: %v", err)
	}
}

func TestValidName(t *testing.T) {
	for _, n := range []string{"claude", "a", "dev-box_2"} {
		if ValidName(n) != nil {
			t.Errorf("%q rejected", n)
		}
	}
	for _, n := range []string{"", "Claude", "-x", "a/b", "../x", strings.Repeat("a", 33)} {
		if ValidName(n) == nil {
			t.Errorf("%q accepted", n)
		}
	}
}
