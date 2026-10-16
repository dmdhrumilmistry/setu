package service

import (
	"errors"
	"testing"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/policy"
)

func desktopProfile(t *testing.T) *Profile {
	t.Helper()
	p := testProfile(t)
	p.Command = nil
	p.Desktop = &DesktopProfile{FPS: 15, Display: -1, X11Display: ":0", XAuthority: "/run/user/1000/gdm/Xauthority"}
	return p
}

func TestDesktopProfileValidate(t *testing.T) {
	if err := desktopProfile(t).Validate(); err != nil {
		t.Fatalf("valid desktop profile refused: %v", err)
	}
	if got := desktopProfile(t).Label(); got != "remote desktop" {
		t.Errorf("label %q", got)
	}
	for name, mut := range map[string]func(*Profile){
		"with a command":   func(p *Profile) { p.Command = []string{"sh"} },
		"fps above 60":     func(p *Profile) { p.Desktop.FPS = 61 },
		"negative fps":     func(p *Profile) { p.Desktop.FPS = -1 },
		"bad display":      func(p *Profile) { p.Desktop.Display = -2 },
		"terminal, no cmd": func(p *Profile) { p.Desktop = nil },
	} {
		p := desktopProfile(t)
		mut(p)
		if p.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestDesktopProfilePolicy(t *testing.T) {
	now := time.Now()
	for key, doc := range map[string]string{
		"DisableDesktop":  `{"DisableDesktop": true}`,
		"AllowedCommands": `{"AllowedCommands": ["sh"]}`, // restricts sharing, does not list @desktop
	} {
		pol, err := policy.Parse([]byte(doc), "test")
		if err != nil {
			t.Fatal(err)
		}
		var viol *policy.Violation
		if err := desktopProfile(t).CheckPolicy(pol, now); !errors.As(err, &viol) || viol.Key != key {
			t.Errorf("%s: got %v", key, err)
		}
	}
	pol, _ := policy.Parse([]byte(`{"AllowedCommands": ["sh", "@desktop"]}`), "test")
	if err := desktopProfile(t).CheckPolicy(pol, now); err != nil {
		t.Errorf("@desktop allowed but refused: %v", err)
	}
}

func TestDesktopHostConfig(t *testing.T) {
	p := desktopProfile(t)
	cfg, err := hostConfig("desk", p, &policy.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	d := cfg.Desktop
	if d == nil || cfg.Command != nil || d.FPS != 15 || d.Display != -1 || d.X11Display != ":0" || d.XAuthority != p.Desktop.XAuthority {
		t.Fatalf("host config %+v / desktop %+v", cfg, d)
	}
	if cfg.ControlSecret == nil || cfg.Profile != "desk" {
		t.Errorf("keys or profile name missing: %+v", cfg)
	}
}
