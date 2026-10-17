package service

import (
	"strings"
	"testing"
	"time"
)

func TestFindings(t *testing.T) {
	now := time.Now()
	p := &Profile{Enabled: true, ControlKey: "x", MaxClients: 1}
	pd := &ProfileDevices{Devices: map[string]*Device{"aaaa-bbbb-cccc-dddd-eeee": {Added: now.Add(-60 * 24 * time.Hour)}}}
	got := map[string]string{}
	for _, f := range p.Findings("claude", pd, now) {
		got[f.Msg] = f.Level
	}
	want := map[string]string{
		"anyone holding the link gets a shell": LevelFail,
		"shared control link":                  LevelWarn,
		"keys never expire":                    LevelWarn,
		"stays connected indefinitely":         LevelWarn,
		"lost device keeps access":             LevelWarn,
		"has not joined since":                 LevelWarn,
	}
	for frag, level := range want {
		found := false
		for msg, l := range got {
			if strings.Contains(msg, frag) && l == level {
				found = true
			}
		}
		if !found {
			t.Errorf("no %s finding about %q in %v", level, frag, got)
		}
	}
	safe := &Profile{Enabled: true, RequireDevice: true, IdleTimeout: Duration(time.Hour), DeviceIdle: Duration(time.Hour), ExpiresAt: now.Add(60 * 24 * time.Hour), MaxClients: 1}
	for _, f := range safe.Findings("ok", &ProfileDevices{Devices: map[string]*Device{"a": {LastSeen: now}}}, now) {
		t.Errorf("hardened profile: unexpected finding %+v", f)
	}
}
