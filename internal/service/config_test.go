package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/secure"
)

func testProfile(t *testing.T) *Profile {
	t.Helper()
	p := &Profile{Command: []string{"sh"}, Enabled: true, MaxClients: 1, ValidFor: Duration(time.Hour), WebURL: "https://example.invalid/"}
	if err := p.NewKeys(true, time.Now()); err != nil {
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
	if err := p.NewKeys(false, now); err != nil {
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
