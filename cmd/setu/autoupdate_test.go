package main

import (
	"testing"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/policy"
)

func TestShouldAutoUpdate(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	base := func() autoUpdateState {
		return autoUpdateState{
			Command: "share", Version: "v1.2.3", Now: now,
			Last: now.Add(-25 * time.Hour), Policy: &policy.Policy{}, AutoPref: true,
		}
	}
	tests := []struct {
		name string
		mod  func(*autoUpdateState)
		want bool
	}{
		{"due", func(*autoUpdateState) {}, true},
		{"never checked", func(s *autoUpdateState) { s.Last = time.Time{} }, true},
		{"stamp in the future", func(s *autoUpdateState) { s.Last = now.Add(time.Hour) }, true},
		{"checked recently", func(s *autoUpdateState) { s.Last = now.Add(-time.Hour) }, false},
		{"upgrade command", func(s *autoUpdateState) { s.Command = "upgrade" }, false},
		{"update command", func(s *autoUpdateState) { s.Command = "update" }, false},
		{"version command", func(s *autoUpdateState) { s.Command = "version" }, false},
		{"help command", func(s *autoUpdateState) { s.Command = "help" }, false},
		{"unknown command", func(s *autoUpdateState) { s.Command = "bogus" }, false},
		{"join", func(s *autoUpdateState) { s.Command = "join" }, true},
		{"service list", func(s *autoUpdateState) { s.Command, s.Args = "service", []string{"list"} }, true},
		{"service run", func(s *autoUpdateState) { s.Command, s.Args = "service", []string{"run"} }, false},
		{"background share child", func(s *autoUpdateState) { s.Args = []string{"-session-dir", "/x"} }, false},
		{"background desktop child", func(s *autoUpdateState) { s.Command, s.Args = "desktop", []string{"--session-dir=/x"} }, false},
		{"session-dir after --", func(s *autoUpdateState) { s.Args = []string{"--", "-session-dir"} }, true},
		{"dev build", func(s *autoUpdateState) { s.Version = "dev" }, false},
		{"service running", func(s *autoUpdateState) { s.ServiceRunning = true }, false},
		{"env opt out", func(s *autoUpdateState) { s.NoAutoUpdateEnv = true }, false},
		{"CI", func(s *autoUpdateState) { s.CI = true }, false},
		{"config off", func(s *autoUpdateState) { s.AutoPref = false }, false},
		{"policy unreadable", func(s *autoUpdateState) { s.Policy = nil }, false},
		{"policy disables updates", func(s *autoUpdateState) { s.Policy = &policy.Policy{DisableUpdate: true} }, false},
		{"policy requires updates", func(s *autoUpdateState) {
			s.Policy, s.AutoPref = &policy.Policy{AutoUpdate: true}, false
		}, true},
		{"policy pin beats config off", func(s *autoUpdateState) {
			s.Policy, s.AutoPref = &policy.Policy{UpdateVersion: "v1.0.0"}, false
		}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := base()
			tc.mod(&s)
			if got := shouldAutoUpdate(s); got != tc.want {
				t.Errorf("shouldAutoUpdate = %v, want %v", got, tc.want)
			}
		})
	}
}
